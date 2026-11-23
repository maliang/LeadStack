#!/usr/bin/env node
/**
 * 登录冒烟（Playwright 无头）—— hermetic：不需要 Go 后端。
 *
 * 做法：
 *  1. 起一个静态服务器托管 web-next/dist（hash 路由，只需 index.html 兜底）。
 *  2. 用 page.route() 拦 /api/v1/*，返回一套内存里的假后端（登录 / me / 登出）。
 *  3. 断言五步：未登录被守卫拦到 /login → 密码错误显示可读错误 → 正确密码进首页
 *     → 刷新后会话仍在 → 登出回到 /login 且 token 被清掉。
 *
 * 用法：
 *   node scripts/smoke-login.mjs            # 无头
 *   node scripts/smoke-login.mjs --headed   # 看画面
 *   node scripts/smoke-login.mjs --dist dist --port 0
 *
 * 退出码：0 全部通过 / 1 有断言失败 / 2 环境缺失（没装 playwright 或没有 dist）。
 */
import { createReadStream, existsSync, readdirSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import { dirname, extname, join, normalize, resolve } from 'node:path'
import { homedir, platform } from 'node:os'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const require = createRequire(import.meta.url)

const argv = process.argv.slice(2)
const flag = (name, fallback = null) => {
  const index = argv.indexOf(`--${name}`)
  if (index === -1) return fallback
  const next = argv[index + 1]
  return next && !next.startsWith('--') ? next : true
}
const HEADED = flag('headed', false) === true
const DIST = resolve(WEB_ROOT, String(flag('dist', 'dist')))
const REQUESTED_PORT = Number(flag('port', 0)) || 0

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.map': 'application/json; charset=utf-8',
}

const SMOKE_USER = {
  id: 'smoke-1',
  username: 'admin',
  role: 'admin',
  role_name: '管理员',
  permissions: ['server.read', 'check.read', 'alert.read', 'task.read', 'deploy.read'],
  scope: { mode: 'all', group_ids: [], server_ids: [] },
}
const SMOKE_TOKEN = 'smoke-token-1'
const SMOKE_PASSWORD = 'smoke-pass-1'

function log(step, message) {
  console.log(`  [${step}] ${message}`)
}

/** 从本地或全局 node_modules 载入 playwright；找不到就按环境缺失处理。 */
function loadPlaywright() {
  const candidates = ['playwright']
  try {
    candidates.push(join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'))
  } catch {
    /* 没有 npm 也能继续尝试本地包 */
  }
  const errors = []
  for (const id of candidates) {
    try {
      return id.startsWith('/') ? require(id) : require(id)
    } catch (error) {
      errors.push(`${id}: ${error.message}`)
    }
  }
  try {
    // ESM 解析路径（pnpm 全局安装时 require 可能拿不到）
    const globalRoot = execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim()
    return { __esm: pathToFileURL(join(globalRoot, 'playwright', 'index.mjs')).href }
  } catch {
    /* ignore */
  }
  const hint = [
    '找不到 playwright —— 这是环境缺失，不是断言失败。',
    '安装：pnpm --dir web-next add -D playwright && pnpm --dir web-next exec playwright install chromium',
    ...errors.map((line) => `  尝试过 ${line}`),
  ].join('\n')
  return { error: hint }
}

async function resolvePlaywright() {
  const loaded = loadPlaywright()
  if (loaded && loaded.error) throw new Error(loaded.error)
  if (loaded && loaded.__esm) {
    const mod = await import(loaded.__esm)
    return mod.chromium ? mod : mod.default
  }
  return loaded
}

/**
 * 兜底：本机已有 playwright 浏览器缓存、但与当前 playwright 版本要求的 revision 不一致时，
 * 直接指向缓存里最新的 chromium 可执行文件（版本差一个小版本通常可用）。
 */
function findCachedChromium() {
  const roots = [
    process.env.PLAYWRIGHT_BROWSERS_PATH,
    platform() === 'darwin'
      ? join(homedir(), 'Library', 'Caches', 'ms-playwright')
      : join(homedir(), '.cache', 'ms-playwright'),
  ].filter(Boolean)

  const candidates = []
  for (const root of roots) {
    if (!existsSync(root)) continue
    let entries = []
    try {
      entries = readdirSync(root, { withFileTypes: true }).filter((entry) => entry.isDirectory())
    } catch {
      continue
    }
    for (const entry of entries) {
      const match = /^(?:chromium_headless_shell|chromium)-(\d+)$/.exec(entry.name)
      if (!match) continue
      const base = join(root, entry.name)
      const exe = platform() === 'darwin'
        ? join(base, 'chrome-headless-shell-mac-arm64', 'chrome-headless-shell')
        : join(base, 'chrome-linux', 'chrome-headless-shell')
      const appExe = platform() === 'darwin'
        ? join(base, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium')
        : join(base, 'chrome-linux', 'chrome')
      for (const path of [exe, appExe]) {
        if (existsSync(path)) candidates.push({ revision: Number(match[1]), path })
      }
    }
  }
  candidates.sort((left, right) => right.revision - left.revision)
  return candidates.length > 0 ? candidates[0].path : null
}

async function launchChromium(chromium, headless) {
  try {
    return await chromium.launch({ headless })
  } catch (error) {
    const message = error && error.message ? error.message : String(error)
    if (!/Executable doesn't exist|is not found at/i.test(message)) throw error
    const cached = findCachedChromium()
    if (!cached) throw error
    console.warn(`! playwright 自带 chromium 缺失，改用缓存版本：${cached}`)
    return await chromium.launch({ headless, executablePath: cached })
  }
}

function createStaticServer(distDir) {
  const indexFile = join(distDir, 'index.html')
  return createServer((request, response) => {
    const urlPath = decodeURIComponent((request.url || '/').split('?')[0].split('#')[0])
    const relative = urlPath === '/' ? '/index.html' : urlPath
    const target = normalize(join(distDir, relative))
    const inside = target.startsWith(distDir)
    const found = inside && existsSync(target) && statSync(target).isFile() ? target : indexFile
    if (!existsSync(found)) {
      response.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' })
      response.end('not found')
      return
    }
    response.writeHead(200, {
      'content-type': MIME[extname(found)] || 'application/octet-stream',
      'cache-control': 'no-store',
    })
    createReadStream(found).pipe(response)
  })
}

/** 假后端：只覆盖登录冒烟需要的三个端点，其余 404，避免静默放过。 */
async function installMockApi(context) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({
        status,
        contentType: 'application/json; charset=utf-8',
        body: JSON.stringify(body),
      })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      const payload = (() => {
        try {
          return JSON.parse(request.postData() || '{}')
        } catch {
          return {}
        }
      })()
      if (payload.username === 'admin' && payload.password === SMOKE_PASSWORD) {
        return json(200, { token: SMOKE_TOKEN, user: SMOKE_USER })
      }
      return json(401, {
        error: {
          code: 'invalid_credentials',
          message: 'invalid username or password',
          details: { remaining_attempts: 2 },
        },
      })
    }

    if (path === '/api/v1/auth/me' && method === 'GET') {
      const header = request.headers().authorization || ''
      if (header === `Bearer ${SMOKE_TOKEN}`) return json(200, SMOKE_USER)
      return json(401, { error: { code: 'unauthorized', message: 'missing or invalid token' } })
    }

    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

/** 读 localStorage 里是否有冒烟 token（不写死 storage key，避免和后端命名耦合）。 */
async function tokenKeys(page) {
  return page.evaluate((token) => {
    const keys = []
    for (let index = 0; index < window.localStorage.length; index += 1) {
      const key = window.localStorage.key(index)
      if (key && window.localStorage.getItem(key) === token) keys.push(key)
    }
    return keys
  }, SMOKE_TOKEN)
}

async function run() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error(`✗ 没有构建产物：${join(DIST, 'index.html')}\n  先跑：pnpm --dir web-next run build`)
    process.exitCode = 2
    return
  }

  let chromium
  try {
    const playwright = await resolvePlaywright()
    chromium = playwright && playwright.chromium
  } catch (error) {
    console.error(`✗ ${error.message}`)
    process.exitCode = 2
    return
  }
  if (!chromium) {
    console.error('✗ playwright 已加载但没有 chromium 导出 —— 环境异常。')
    process.exitCode = 2
    return
  }

  const results = []
  const check = (name, ok, detail = '') => {
    results.push({ name, ok, detail })
    console.log(`${ok ? '✓' : '✗'} ${name}${detail ? `：${detail}` : ''}`)
  }

  const server = createStaticServer(DIST)
  await new Promise((done, fail) => {
    server.once('error', fail)
    server.listen(REQUESTED_PORT, '127.0.0.1', done)
  })
  const base = `http://127.0.0.1:${server.address().port}`
  log('serve', `${DIST} → ${base}`)

  let browser
  try {
    browser = await launchChromium(chromium, !HEADED)
  } catch (error) {
    console.error(`✗ 启动 chromium 失败：${error && error.message ? error.message.split('\n')[0] : error}`)
    console.error('  安装：pnpm --dir web-next exec playwright install chromium')
    await new Promise((done) => server.close(done))
    process.exitCode = 2
    return
  }
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await installMockApi(context)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))

  const apiCalls = []
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/')) apiCalls.push(`${request.method()} ${new URL(request.url()).pathname}`)
  })

  try {
    // 1) 未登录访问受保护页面 → 守卫拦到 /login
    await page.goto(`${base}/#/`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('未登录被守卫拦到 /login', page.url().includes('#/login'), page.url().split('#')[1] || '')
    check('登录页无外壳（blank 版式）', (await page.locator('.ls-shell').count()) === 0)
    check('未登录时未打 /auth/me（无 token 不该发请求）', !apiCalls.includes('GET /api/v1/auth/me'))

    // 2) 密码错误 → 可读错误 + 重试入口，仍停在登录页
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', 'wrong-pass')
    await page.click('#login-submit')
    const errorBox = page.locator('#login-error')
    await errorBox.waitFor({ state: 'visible', timeout: 10000 })
    const errorText = (await errorBox.innerText()).trim()
    check('密码错误显示可读错误', errorText.length > 0 && /2|次|attempt/i.test(errorText), errorText.replace(/\s+/g, ' ').slice(0, 60))
    check('失败后仍在登录页', page.url().includes('#/login'))
    check('失败态有重试入口', (await page.locator('#login-error .ls-login__retry').count()) === 1)

    // 3) 正确密码 → 进首页，并且 token 落库
    await page.fill('#login-password', SMOKE_PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    check('登录成功进入外壳页面', !page.url().includes('#/login'), page.url().split('#')[1] || '/')
    const keys = await tokenKeys(page)
    check('登录后 token 已持久化', keys.length > 0, keys.join(',') || '未找到')
    check('顶栏显示当前账号', (await page.locator('.ls-user__name').first().innerText()).trim() === 'admin')
    check('侧边栏按权限渲染（含服务器项）', (await page.locator('.ls-nav__item').count()) > 0)

    // 4) 刷新 → 带 token 重新校验会话，仍停在内页
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    check('刷新后会话仍在（守卫用 /auth/me 复验）', !page.url().includes('#/login'))
    check('刷新后确实复验了会话', apiCalls.includes('GET /api/v1/auth/me'))

    // 5) 登出 → 清 token 并回到登录页；刷新后仍停在登录页（不靠内存态兜底）
    await page.click('#topbar-account')
    await page.waitForSelector('.ls-topbar-account__menu', { timeout: 5000 })
    await page.click('.ls-topbar-account__menu .ls-topbar-account__item--danger')
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('登出后回到登录页', page.url().includes('#/login'))
    check('登出后 token 已清除', (await tokenKeys(page)).length === 0)
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('登出后刷新仍停在登录页', page.url().includes('#/login'), page.url().split('#')[1] || '')
    check('登出后刷新 token 仍为空', (await tokenKeys(page)).length === 0)

    // 6) 窄宽 320px：登录卡片不产生横向滚动，提交按钮仍在视口内
    await page.setViewportSize({ width: 320, height: 800 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-submit', { timeout: 15000 })
    const narrow = await page.evaluate(() => {
      const card = document.querySelector('.ls-login__card')
      const submit = document.querySelector('#login-submit')
      const rect = card ? card.getBoundingClientRect() : null
      const button = submit ? submit.getBoundingClientRect() : null
      return {
        overflow: document.documentElement.scrollWidth - window.innerWidth,
        cardWidth: rect ? Math.round(rect.width) : 0,
        buttonVisible: Boolean(button && button.width > 0 && button.bottom <= window.innerHeight + 1),
      }
    })
    check('320px 无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 登录卡片不超宽', narrow.cardWidth > 0 && narrow.cardWidth <= 320, `${narrow.cardWidth}px`)
    check('320px 提交按钮可点到', narrow.buttonVisible)

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(`\n${failed.length === 0 ? '✓ 登录冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`)
  if (failed.length > 0) {
    for (const item of failed) console.log(`  - ${item.name}${item.detail ? `：${item.detail}` : ''}`)
    process.exitCode = 1
  }
}

run().catch((error) => {
  console.error(error && error.stack ? error.stack : error)
  process.exitCode = 1
})
