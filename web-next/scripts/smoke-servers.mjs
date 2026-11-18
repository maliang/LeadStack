#!/usr/bin/env node
/**
 * 服务器列表页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：默认列表视图 / 卡片⇄列表切换与 localStorage 记忆 / 纯前端筛选与清除 /
 * 无 terminal.use 时两个面板的占位 / 有权限时内嵌终端与文件浏览 / 320px 无横向滚动。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-servers.mjs [--headed]
 */
import { createServer } from 'node:http'
import { createReadStream, existsSync, readdirSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { homedir, platform } from 'node:os'
import { dirname, extname, join, normalize, resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
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
const VIEW_KEY = 'leadstack.servers.view'
const SHOTS = typeof flag('shots', null) === 'string' ? resolve(String(flag('shots'))) : null
const shot = async (page, name) => {
  if (!SHOTS) return
  await page.screenshot({ path: resolve(SHOTS, `${name}.png`), fullPage: true })
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.map': 'application/json; charset=utf-8',
}

const TOKEN = 'smoke-servers-token'
const PASSWORD = 'smoke-servers-password'

function user(terminalUse) {
  return {
    id: 'smoke-servers-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: terminalUse ? ['server.read', 'terminal.use'] : ['server.read'],
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

function serverFixture(overrides) {
  return {
    id: 'srv-alpha',
    name: 'alpha-node',
    host: '10.0.0.11',
    port: 22,
    mode: 'agent',
    status: 'online',
    last_seen_at: Date.now() - 30_000,
    agent_version: '1.4.0',
    os: 'linux',
    arch: 'amd64',
    kernel: '6.8.0',
    tags: ['prod', 'edge'],
    metrics_summary: { cpu_pct: 12.5, mem_pct: 33.1, disk_pct: 40 },
    created_at: Date.now() - 86_400_000,
    group: '默认分组',
    group_ids: ['grp-default'],
    group_names: ['默认分组'],
    ...overrides,
  }
}

const SERVERS = [
  serverFixture({ id: 'srv-alpha' }),
  serverFixture({
    id: 'srv-beta',
    name: 'beta-node',
    host: '10.0.0.12',
    status: 'offline',
    last_seen_at: null,
    tags: ['staging'],
    group: '测试分组',
    group_ids: ['grp-test'],
    group_names: ['测试分组'],
    os: 'darwin',
    arch: 'arm64',
  }),
]

function log(step, message) {
  console.log(`  [${step}] ${message}`)
}

/** 从本地或全局 node_modules 载入 playwright；找不到按环境缺失处理（退出码 2）。 */
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
  return {
    error: ['找不到 playwright —— 这是环境缺失，不是断言失败。', ...errors.map((line) => `  尝试过 ${line}`)].join(
      '\n',
    ),
  }
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

/** playwright 自带的 revision 与缓存不一致时，退到缓存里最新的 chromium。 */
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
      const paths =
        platform() === 'darwin'
          ? [
              join(base, 'chrome-headless-shell-mac-arm64', 'chrome-headless-shell'),
              join(base, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium'),
            ]
          : [join(base, 'chrome-linux', 'chrome-headless-shell'), join(base, 'chrome-linux', 'chrome')]
      for (const path of paths) {
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

/** 托管 dist 的静态服务器；未知路径回 index.html（hash 路由）。 */
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

/** 假后端：只覆盖服务器页需要的端点，其他一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: user(state.terminalUse) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: user(state.terminalUse) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      state.serverCalls += 1
      return json(200, { data: SERVERS })
    }

    const detail = /^\/api\/v1\/servers\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      const found = SERVERS.find((item) => item.id === decodeURIComponent(detail[1]))
      return found ? json(200, { data: found }) : json(404, { error: { code: 'not_found' } })
    }

    const ticket = /^\/api\/v1\/servers\/([^/]+)\/terminal-ticket$/.exec(path)
    if (ticket && method === 'POST') {
      return json(200, { data: { ticket: 'smoke-terminal-ticket', expires_in: 60 } })
    }

    const files = /^\/api\/v1\/servers\/([^/]+)\/files$/.exec(path)
    if (files && method === 'GET') {
      const target = url.searchParams.get('path') || '/'
      state.filePaths.push(target)
      if (target === '/') {
        return json(200, {
          data: {
            entries: [
              { name: 'logs', is_dir: true, size: 0, mod_time: Date.now() - 3_600_000, mode: 'drwxr-xr-x' },
              { name: 'app.log', is_dir: false, size: 1234, mod_time: Date.now() - 60_000, mode: '-rw-r--r--' },
            ],
          },
        })
      }
      if (target === '/logs') {
        return json(200, {
          data: {
            entries: [
              { name: 'access.log', is_dir: false, size: 64, mod_time: Date.now() - 600_000, mode: '-rw-r-----' },
            ],
          },
        })
      }
      return json(200, { data: { entries: [] } })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
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
    await new Promise((done) => server.close(done))
    process.exitCode = 2
    return
  }

  const state = { terminalUse: false, serverCalls: 0, filePaths: [] }
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const apiCalls = []
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/')) {
      apiCalls.push(`${request.method()} ${new URL(request.url()).pathname}`)
    }
  })

  // 内嵌终端的 WS 也走 mock：握手成功并回一条 banner，避免依赖真 agent。
  await page.routeWebSocket(/\/api\/v1\/servers\/[^/]+\/terminal\?ticket=/, (ws) => {
    ws.onMessage(() => {})
    ws.send(JSON.stringify({ type: 'output', data: 'mock shell ready\r\n' }))
  })

  try {
    // 1) 登录（mock token 落库）后进入 /servers
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.goto(`${base}/#/servers`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-servers__title', { timeout: 15000 })
    check('登录后进入服务器页', new URL(page.url()).hash === '#/servers', new URL(page.url()).hash)
    check('列表接口只拉一次（无轮询抖动）', state.serverCalls === 1, `${state.serverCalls} 次`)

    // 2) 默认列表视图
    check(
      '默认选中列表视图',
      (await page.locator('#servers-view-list').getAttribute('aria-pressed')) === 'true',
    )
    check('默认渲染列表容器而非卡片网格', (await page.locator('.ls-servers__list').count()) === 1 && (await page.locator('.ls-servers__cards').count()) === 0)
    check('两台服务器都渲染', (await page.locator('.ls-srv-row').count()) === 2)

    // 3) 卡片视图 + localStorage 记忆
    await page.click('#servers-view-card')
    await page.waitForSelector('.ls-servers__cards', { timeout: 5000 })
    const storedCard = await page.evaluate((key) => window.localStorage.getItem(key), VIEW_KEY)
    check('切到卡片视图写入 localStorage', storedCard === 'card', String(storedCard))
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-servers__title', { timeout: 15000 })
    check(
      '刷新后仍是卡片视图（localStorage 生效）',
      (await page.locator('#servers-view-card').getAttribute('aria-pressed')) === 'true' &&
        (await page.locator('.ls-servers__cards').count()) === 1,
    )
    // 3b) 卡片标题是进详情页的入口：点标题后 hash 切到 /servers/<id>
    await page.locator('.ls-srv-card__title a').first().click()
    await page.waitForSelector('.ls-detail__title', { timeout: 10000 }).catch(() => {})
    const cardHash = await page.evaluate(() => window.location.hash)
    check('点卡片标题进入详情页', cardHash === '#/servers/srv-alpha', cardHash)
    await page.goto(`${base}/#/servers`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-servers__title', { timeout: 15000 })
    await page.click('#servers-view-list')
    await page.waitForSelector('.ls-servers__list', { timeout: 5000 })
    const storedList = await page.evaluate((key) => window.localStorage.getItem(key), VIEW_KEY)
    check('切回列表写入 localStorage', storedList === 'list', String(storedList))

    // 4) 纯前端筛选：关键字 / 状态 / 分组 / 清除
    await page.fill('#servers-search-input', 'beta')
    await page.waitForTimeout(150)
    check('关键字筛选只留 beta', (await page.locator('.ls-srv-row').count()) === 1)
    check('计数文案跟随筛选', (await page.locator('.ls-servers-toolbar__count').innerText()).includes('1 / 2'))

    await page.fill('#servers-search-input', '')
    await page.selectOption('#servers-filter-group', '默认分组')
    await page.selectOption('#servers-filter-status', 'offline')
    await page.waitForTimeout(150)
    check('分组+状态组合无匹配时显示空态', (await page.locator('.ls-srv-row').count()) === 0 && (await page.locator('.ls-notice').count()) >= 1)
    check('有筛选时出现清除入口', (await page.locator('#servers-filter-reset').count()) === 1)
    await page.click('#servers-filter-reset')
    await page.waitForTimeout(150)
    check('清除筛选恢复全部', (await page.locator('.ls-srv-row').count()) === 2)

    // 5) 无 terminal.use：两个面板都显示占位，不是白屏；展开按钮不得误跳详情页
    const hashBeforeExpand = await page.evaluate(() => window.location.hash)
    await page.click('#server-srv-alpha-terminal')
    await page.waitForSelector('.ls-srv-row__panels', { timeout: 5000 })
    const hashAfterExpand = await page.evaluate(() => window.location.hash)
    check('展开终端不误跳详情页', hashAfterExpand === hashBeforeExpand, hashAfterExpand)
    check('行内两个面板都渲染', (await page.locator('.ls-srv-panel').count()) === 2)
    const blocked = (await page.locator('.ls-srv-panel').allInnerTexts()).join('\n')
    check(
      '无权限时两个面板都有「无终端权限」',
      (blocked.match(/无终端权限/g) || []).length === 2,
      blocked.replace(/\s+/g, ' ').slice(0, 60),
    )

    // 6) 320px：展开面板后无横向滚动，操作按钮仍在视口内
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(200)
    const narrow = await page.evaluate(() => {
      const button = document.querySelector('#server-srv-alpha-files')
      const rect = button ? button.getBoundingClientRect() : null
      return {
        overflow: document.documentElement.scrollWidth - window.innerWidth,
        buttonVisible: Boolean(rect && rect.width > 0 && rect.right <= window.innerWidth + 1),
      }
    })
    check('320px 展开面板无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 展开按钮可见可点', narrow.buttonVisible)
    await shot(page, 'servers-320-list-expanded')

    // 7) 有 terminal.use：终端 xterm 挂载、文件面包屑与元信息
    state.terminalUse = true
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-servers__title', { timeout: 15000 })
    await page.click('#server-srv-alpha-terminal')
    await page.waitForSelector('.ls-srv-row__panels', { timeout: 5000 })
    await page.waitForSelector('.xterm', { timeout: 10000 })
    check('有权限时终端挂载 xterm', (await page.locator('.ls-srv-term .xterm').count()) === 1)
    check('终端拿到 ticket 并发起 WS', apiCalls.some((line) => line.includes('POST /api/v1/servers/srv-alpha/terminal-ticket')))

    await page.waitForSelector('.ls-srv-files__entry', { timeout: 10000 })
    check('文件面板列出根目录', (await page.locator('.ls-srv-files__entry').count()) === 2)
    const hashBeforeFileClick = new URL(page.url()).hash
    await page.locator('.ls-srv-files__entry', { hasText: 'app.log' }).click()
    await page.waitForTimeout(150)
    const hashAfterFileClick = new URL(page.url()).hash
    check('点文件没有触发下载/跳转', hashBeforeFileClick === '#/servers' && hashAfterFileClick === '#/servers', `${hashBeforeFileClick} → ${hashAfterFileClick}`)
    check('点文件没有误跳详情页', hashAfterFileClick !== '#/servers/srv-alpha', hashAfterFileClick)
    await page.waitForSelector('.ls-srv-files__meta', { timeout: 5000 })
    const meta = await page.locator('.ls-srv-files__meta').innerText()
    check('点文件只显示元信息（名称/大小/权限）', meta.includes('app.log') && meta.includes('-rw-r--r--'), meta.replace(/\s+/g, ' ').slice(0, 80))

    await page.locator('.ls-srv-files__entry', { hasText: 'logs' }).click()
    await page.waitForTimeout(300)
    const currentCrumb = await page.locator('.ls-srv-files__crumb[aria-current="location"]').innerText()
    check('面包屑可进入子目录', currentCrumb.trim() === 'logs', currentCrumb)
    check('请求带上了目标 path', state.filePaths.includes('/logs'), state.filePaths.join(' → '))

    // 8) 手动刷新会重新请求列表
    const callsBefore = state.serverCalls
    await page.click('#servers-refresh')
    await page.waitForTimeout(500)
    check('刷新按钮重新请求列表', state.serverCalls > callsBefore, `${callsBefore} → ${state.serverCalls}`)
    await shot(page, 'servers-1280-list-expanded')

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(
    `\n${failed.length === 0 ? '✓ 服务器页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
  )
  if (failed.length > 0) {
    for (const item of failed) console.log(`  - ${item.name}${item.detail ? `：${item.detail}` : ''}`)
    process.exitCode = 1
  }
}

run().catch((error) => {
  console.error(error && error.stack ? error.stack : error)
  process.exitCode = 1
})
