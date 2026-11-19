#!/usr/bin/env node
/**
 * 登出冒烟（Playwright 无头）—— 钉住「点退出登录必须回到登录页」的产品行为。
 *
 * 背景：登录冒烟曾把 /auth/me 的 mock 形状写错（返回 { user } 包装），刷新后
 * permissions 归一成空数组 → 守卫把用户扔到 public 的 /forbidden → 旧版 AppShell
 * 的 status watcher 对 public 路由早退，登出后停在 /forbidden，#login-username 超时。
 * 这里把两侧分开钉：
 *   场景 1（正常内页）：登录 → 在 / 点顶栏登出 → #/login、token 清空、刷新仍在登录页。
 *   场景 2（public 外壳页）：登录 → 直接进 #/forbidden → 点顶栏登出 → #/login、
 *   token 清空、刷新仍在登录页。旧版实现在这里必然失败。
 * 另外打印 `.ls-topbar__end` 里被选中的元素（aria-label / 元素数），
 * 证明点击的不是别的图标按钮。
 *
 * 用法：node scripts/smoke-logout.mjs [--headed] [--dist dist] [--port 0]
 * 退出码：0 全部通过 / 1 断言失败 / 2 环境缺失（playwright 或 dist）。
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

const results = []
function check(name, ok, detail = '') {
  results.push({ name, ok, detail })
  console.log(`${ok ? '✓' : '✗'} ${name}${detail ? `：${detail}` : ''}`)
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
      return require(id)
    } catch (error) {
      errors.push(`${id}: ${error.message}`)
    }
  }
  try {
    const globalRoot = execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim()
    return { __esm: pathToFileURL(join(globalRoot, 'playwright', 'index.mjs')).href }
  } catch {
    /* ignore */
  }
  throw new Error(
    [
      '找不到 playwright —— 这是环境缺失，不是断言失败。',
      '安装：pnpm --dir web-next add -D playwright && pnpm --dir web-next exec playwright install chromium',
      ...errors.map((line) => `  尝试过 ${line}`),
    ].join('\n'),
  )
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

/** 假后端：登录 / me / 登出。me 返回裸 user 对象（与后端 writeData(userPayload) 对齐，不再包 { user }）。 */
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
        error: { code: 'invalid_credentials', message: 'invalid username or password' },
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

/** 读 localStorage 里是否还存着冒烟 token（不写死 storage key）。 */
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

function hashOf(page) {
  return page.url().split('#')[1] || '/'
}

async function loginThroughUi(page, base) {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 15000 })
  await page.fill('#login-username', 'admin')
  await page.fill('#login-password', SMOKE_PASSWORD)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 15000 })
  await page.waitForSelector('.ls-user__name', { timeout: 15000 })
}

/**
 * 在 `.ls-topbar__end` 里定位登出按钮：先把所有 button 的 aria-label 打出来，
 * 再断言冒烟用的选择器恰好命中唯一一个、且就是带登出文案的按钮。
 */
async function inspectLogoutButton(page) {
  const snapshot = await page.evaluate(() => {
    const end = document.querySelector('.ls-topbar__end')
    if (!end) return null
    const buttons = Array.from(end.querySelectorAll('button')).map((button) => ({
      ariaLabel: button.getAttribute('aria-label') || '',
      title: button.getAttribute('title') || '',
      iconCount: button.querySelectorAll('svg.ls-icon').length,
      isLastOfType: button === end.querySelector('button:last-of-type'),
    }))
    const picked = end.querySelector('.ls-icon-btn:last-of-type')
    return {
      endHtml: end.innerHTML.slice(0, 400),
      buttons,
      pickedAriaLabel: picked ? picked.getAttribute('aria-label') || '' : null,
      pickedTag: picked ? picked.tagName : null,
    }
  })
  return snapshot
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

  const server = createStaticServer(DIST)
  await new Promise((done, fail) => {
    server.once('error', fail)
    server.listen(REQUESTED_PORT, '127.0.0.1', done)
  })
  const base = `http://127.0.0.1:${server.address().port}`
  console.log(`  [serve] ${DIST} → ${base}`)

  let browser
  try {
    browser = await launchChromium(chromium, !HEADED)
  } catch (error) {
    console.error(`✗ 启动 chromium 失败：${error && error.message ? error.message.split('\n')[0] : error}`)
    await new Promise((done) => server.close(done))
    process.exitCode = 2
    return
  }

  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await installMockApi(context)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))

  try {
    // ── 场景 1：正常内页（/）登出 ────────────────────────────────
    console.log('  [step] 场景 1：登录 → 内页登出 → 刷新')
    await loginThroughUi(page, base)
    check('登录后进入内页', !page.url().includes('#/login'), hashOf(page))

    const snapshot = await inspectLogoutButton(page)
    const buttonCount = snapshot ? snapshot.buttons.length : -1
    const labels = snapshot ? snapshot.buttons.map((item) => item.ariaLabel).join(' / ') : ''
    console.log(`  [dom] .ls-topbar__end buttons=${buttonCount} aria-labels=[${labels}]`)
    console.log(`  [dom] picked tag=${snapshot ? snapshot.pickedTag : 'null'} aria-label=${snapshot ? snapshot.pickedAriaLabel : 'null'}`)
    check('.ls-topbar__end 只有一个按钮（登出）', buttonCount === 1, `实际 ${buttonCount} 个：${labels}`)
    check(
      '冒烟选择器命中的就是登出按钮（aria-label / last-of-type）',
      Boolean(snapshot) &&
        snapshot.pickedTag === 'BUTTON' &&
        snapshot.buttons.length === 1 &&
        snapshot.buttons[0].isLastOfType === true &&
        /退出登录|sign ?out|log ?out/i.test(snapshot.pickedAriaLabel || ''),
      snapshot ? `${snapshot.pickedTag} aria-label=${snapshot.pickedAriaLabel}` : '未找到 .ls-topbar__end',
    )

    await page.click('.ls-topbar__end .ls-icon-btn:last-of-type')
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('内页登出后回到登录页', page.url().includes('#/login'), hashOf(page))
    check('内页登出后 token 已清除', (await tokenKeys(page)).length === 0, (await tokenKeys(page)).join(',') || '空')
    check('内页登出后外壳已卸载', (await page.locator('.ls-shell').count()) === 0)

    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('内页登出后刷新仍停在登录页', page.url().includes('#/login'), hashOf(page))
    check('内页登出后刷新 token 仍为空', (await tokenKeys(page)).length === 0)

    // ── 场景 2：public 外壳页（/forbidden）登出 ──────────────────
    console.log('  [step] 场景 2：登录 → /forbidden（public + 外壳）→ 登出 → 刷新')
    await loginThroughUi(page, base)
    await page.goto(`${base}/#/forbidden`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.waitForSelector('.ls-user__name', { timeout: 15000 })
    check('停在 /forbidden 且外壳仍在（public 路由）', hashOf(page).startsWith('/forbidden'), hashOf(page))
    check('public 路由上仍有登出按钮', (await page.locator('.ls-topbar__end .ls-icon-btn:last-of-type').count()) === 1)
    check('public 路由上 token 仍在（已登录态）', (await tokenKeys(page)).length > 0)

    await page.click('.ls-topbar__end .ls-icon-btn:last-of-type')
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('public 路由登出后回到登录页', page.url().includes('#/login'), hashOf(page))
    check('public 路由登出后 token 已清除', (await tokenKeys(page)).length === 0)
    check('public 路由登出后外壳已卸载', (await page.locator('.ls-shell').count()) === 0)

    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    check('public 路由登出后刷新仍停在登录页', page.url().includes('#/login'), hashOf(page))
    check('public 路由登出后刷新 token 仍为空', (await tokenKeys(page)).length === 0)

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message.split('\n')[0] : String(error))
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(`\n${failed.length === 0 ? '✓ 登出冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`)
  if (failed.length > 0) {
    for (const item of failed) console.log(`  - ${item.name}${item.detail ? `：${item.detail}` : ''}`)
    process.exitCode = 1
  }
}

await run()
