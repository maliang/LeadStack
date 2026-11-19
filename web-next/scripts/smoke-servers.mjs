#!/usr/bin/env node
/**
 * 服务器列表页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：默认列表视图 / 卡片⇄列表切换与 localStorage 记忆 / 纯前端筛选与清除 /
 * 无 terminal.use 时两个面板的占位 / 有权限时内嵌终端与文件浏览 / 320px 无横向滚动 /
 * 终端拒绝帧三态（JSON error / 纯文本 / 二进制纯文本）与正常二进制 PTY 数据不误判。
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

  // 内嵌终端的 WS 也走 mock：连上后**不主动发帧**——后端没有独立 ready 帧，
  // 接受会话时第一帧就是 PTY 输出，拒绝时第一帧是 {type:'error'}（见 handlers_terminal.go）。
  // 首帧由用例显式注入，才能验证「未收到就绪帧前不得显示已连接」（t28 回归）。
  const terminalSockets = []
  const terminalFrames = []
  const terminalClosed = []
  await page.routeWebSocket(/\/api\/v1\/servers\/[^/]+\/terminal\?ticket=/, (ws) => {
    terminalSockets.push(ws)
    ws.onMessage((message) => {
      terminalFrames.push(typeof message === 'string' ? message : message.toString())
    })
    ws.onClose(() => terminalClosed.push(ws))
  })

  try {
    // 1) 登录（mock token 落库）后进入 /servers
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    // 登录后默认落在 #/：首页总览会自行预取 /servers、/checks/summary、/apps。
    // 这些预取必须留在原始计数里（不靠过滤抹掉），但「服务器页自己只拉一次」要单独看：
    // 等首页首屏（总览标题）渲染完记基线，之后只数目标页产生的增量。
    await page.waitForSelector('.ls-adminpage__title', { timeout: 15000 })
    const serverCallsSince = state.serverCalls
    await page.goto(`${base}/#/servers`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-servers__title', { timeout: 15000 })
    await page.waitForSelector('.ls-srv-row', { timeout: 15000 })
    // 抖动窗口：轮询一旦发生，增量会从 1 变成 >1。
    await page.waitForTimeout(800)
    check('登录后进入服务器页', new URL(page.url()).hash === '#/servers', new URL(page.url()).hash)
    const serverPageCalls = state.serverCalls - serverCallsSince
    check(
      '列表接口只拉一次（无轮询抖动）',
      serverPageCalls === 1,
      `目标页 ${serverPageCalls} 次（首页预取 ${serverCallsSince} 次，总计 ${state.serverCalls} 次）`,
    )

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

    // 7.1) 服务端就绪帧：WS 已 open 但服务端尚未发第一帧时，状态只能是「连接中」
    const termStateText = async () => (await page.locator('.ls-srv-term__state').innerText()).trim()
    const waitTermState = (text) =>
      page.waitForFunction(
        (expected) => document.querySelector('.ls-srv-term__state')?.textContent?.trim() === expected,
        text,
        { timeout: 5000 },
      )
    // 记录状态文本的变化轨迹（拒绝用例断言「全程不得出现已连接」）。
    const trackTermState = () =>
      page.evaluate(() => {
        const node = document.querySelector('.ls-srv-term__state')
        window.__termStateLog = [node?.textContent?.trim() ?? '']
        window.__termStateObserver?.disconnect?.()
        window.__termStateObserver = new MutationObserver(() => {
          window.__termStateLog.push(node?.textContent?.trim() ?? '')
        })
        if (node) window.__termStateObserver.observe(node, { childList: true, characterData: true, subtree: true })
      })
    await page.waitForTimeout(600)
    const opened = terminalFrames.some((frame) => frame.includes('"type":"resize"'))
    check('服务端就绪帧：ws.onopen 已执行（mock 收到 resize 帧）', opened, terminalFrames.join(' | ').slice(0, 80))
    const beforeReady = await termStateText()
    check(
      '服务端就绪帧：未收到就绪帧时状态为「连接中」（不得显示「已连接」）',
      beforeReady === '连接中',
      beforeReady,
    )

    // 7.2) 拒绝帧（agent 回「已有终端会话在运行」）：首帧就是拒绝时必须是「已断开」，
    //      面板里给可读原因 + 「重新连接」，状态轨迹里全程不得出现「已连接」（P2 回归）。
    const readySocket = terminalSockets[terminalSockets.length - 1]
    check('拒绝帧：mock WS 已建立（可注入首帧）', Boolean(readySocket))
    await trackTermState()
    readySocket?.send(JSON.stringify({ type: 'error', message: '已有终端会话在运行' }))
    await waitTermState('已断开')
    // 等一帧，让所有后续状态变化都进轨迹再断言。
    await page.waitForTimeout(200)
    const rejectStates = await page.evaluate(() => window.__termStateLog.slice())
    const rejectStateText = await termStateText()
    const noticeText = (await page.locator('.ls-srv-term .ls-notice').innerText().catch(() => '')).replace(/\s+/g, ' ')
    const retryCount = await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).count()
    check('拒绝帧：状态文本为「已断开」', rejectStateText === '已断开', rejectStateText)
    check('拒绝帧：状态轨迹里从未出现「已连接」', !rejectStates.includes('已连接'), rejectStates.join(' → '))
    check('拒绝帧：面板显示拒绝原因', noticeText.includes('已有终端会话在运行'), noticeText.slice(0, 80))
    check('拒绝帧：给出「重新连接」按钮', retryCount === 1, `retry=${retryCount}`)

    // 7.3) 重连：握手成功不置位，收到真实数据帧才显示「已连接」
    const socketsBeforeReconnect = terminalSockets.length
    await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).click()
    for (let i = 0; i < 50 && terminalSockets.length <= socketsBeforeReconnect; i += 1) {
      await page.waitForTimeout(100)
    }
    check('重连：点了「重新连接」后建立新 WS', terminalSockets.length > socketsBeforeReconnect, `${socketsBeforeReconnect} → ${terminalSockets.length}`)
    await page.waitForTimeout(400)
    const reconnectingState = await termStateText()
    check('重连：未收到数据帧时仍是「连接中」', reconnectingState === '连接中', reconnectingState)
    const reconnectedSocket = terminalSockets[terminalSockets.length - 1]
    reconnectedSocket?.send(JSON.stringify({ type: 'output', data: 'mock shell ready\r\n' }))
    await waitTermState('已连接')
    const reconnectReadyState = await termStateText()
    check('重连：收到真实数据帧后才显示「已连接」', reconnectReadyState === '已连接', reconnectReadyState)

    // 7.4) 旧后端兼容：纯文本拒绝只会在**首帧**下发（没有 JSON 帧）。这里先用一个 JSON
    //      error 断开已连接的会话，重连后把「错误：…」当首帧注入，必须落「已断开」并给重连。
    //      （已连接后同样的文本是程序正常输出、不得断开 —— 见 7.7。）
    reconnectedSocket?.send(JSON.stringify({ type: 'error', message: '断开以验证首帧纯文本拒绝' }))
    await waitTermState('已断开')
    const socketsBeforeTextReject = terminalSockets.length
    await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).click()
    for (let i = 0; i < 50 && terminalSockets.length <= socketsBeforeTextReject; i += 1) {
      await page.waitForTimeout(100)
    }
    await page.waitForTimeout(300)
    const textRejectSocket = terminalSockets[terminalSockets.length - 1]
    textRejectSocket?.send('错误：已有终端会话在运行，请先关闭当前会话。')
    await waitTermState('已断开')
    const legacyState = await termStateText()
    const legacyRetry = await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).count()
    const legacyNotice = (await page.locator('.ls-srv-term .ls-notice').innerText().catch(() => '')).replace(/\s+/g, ' ')
    check(
      '旧后端纯文本：以「错误：」开头的文本帧也转「已断开」并给重连',
      legacyState === '已断开' && legacyRetry === 1,
      `${legacyState} / retry=${legacyRetry}`,
    )
    check('旧后端纯文本：拒绝原因写进面板', legacyNotice.includes('已有终端会话在运行'), legacyNotice.slice(0, 80))

    // 7.5) 二进制拒绝帧：旧控制端把 error 降级成纯文本后一路 BinaryMessage 写浏览器，
    //      前端必须先 TextDecoder 解码、再按帧首前缀识别拒绝；轨迹只能是「连接中 → 已断开」，
    //      全程不得出现「已连接」（否则用户会看到红字却仍显示在线）。
    const socketsBeforeBinaryReject = terminalSockets.length
    await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).click()
    for (let i = 0; i < 50 && terminalSockets.length <= socketsBeforeBinaryReject; i += 1) {
      await page.waitForTimeout(100)
    }
    check(
      '二进制拒绝：重连后建立新 WS',
      terminalSockets.length > socketsBeforeBinaryReject,
      `${socketsBeforeBinaryReject} → ${terminalSockets.length}`,
    )
    await page.waitForTimeout(300)
    const beforeBinaryReject = await termStateText()
    check('二进制拒绝：注入首帧前仍是「连接中」', beforeBinaryReject === '连接中', beforeBinaryReject)
    await trackTermState()
    const binaryRejectSocket = terminalSockets[terminalSockets.length - 1]
    binaryRejectSocket?.send(Buffer.from('错误：已有终端会话在运行', 'utf8'))
    await waitTermState('已断开')
    await page.waitForTimeout(200)
    const binaryRejectStates = await page.evaluate(() => window.__termStateLog.slice())
    const binaryRejectText = await termStateText()
    const binaryRejectNotice = (await page.locator('.ls-srv-term .ls-notice').innerText().catch(() => '')).replace(/\s+/g, ' ')
    const binaryRejectRetry = await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).count()
    check('二进制拒绝：状态为「已断开」', binaryRejectText === '已断开', binaryRejectText)
    check(
      '二进制拒绝：状态轨迹为「连接中 → 已断开」且从未出现「已连接」',
      !binaryRejectStates.includes('已连接'),
      binaryRejectStates.join(' → '),
    )
    check('二进制拒绝：面板显示拒绝原因', binaryRejectNotice.includes('已有终端会话在运行'), binaryRejectNotice.slice(0, 80))
    check('二进制拒绝：给出「重新连接」按钮', binaryRejectRetry === 1, `retry=${binaryRejectRetry}`)

    // 7.6) 正常二进制 PTY 数据（含中文与控制字符）不得被误判：首帧即「已连接」并写屏。
    const socketsBeforeBinaryData = terminalSockets.length
    await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).click()
    for (let i = 0; i < 50 && terminalSockets.length <= socketsBeforeBinaryData; i += 1) {
      await page.waitForTimeout(100)
    }
    check(
      '二进制正常数据：重连后建立新 WS',
      terminalSockets.length > socketsBeforeBinaryData,
      `${socketsBeforeBinaryData} → ${terminalSockets.length}`,
    )
    const binaryDataSocket = terminalSockets[terminalSockets.length - 1]
    binaryDataSocket?.send(Buffer.from('\r\n$ ls\r\n你好.txt\r\n', 'utf8'))
    await waitTermState('已连接')
    await page.waitForFunction(
      () => (document.querySelector('.ls-srv-term .xterm-rows')?.textContent ?? '').includes('你好.txt'),
      undefined,
      { timeout: 5000 },
    )
    const binaryDataState = await termStateText()
    const screenText = await page.locator('.ls-srv-term .xterm-rows').innerText()
    const binaryDataNotice = await page.locator('.ls-srv-term .ls-notice').count()
    check('二进制正常数据：首帧即「已连接」', binaryDataState === '已连接', binaryDataState)
    check(
      '二进制正常数据：中文与控制字符写进终端屏幕',
      screenText.includes('$ ls') && screenText.includes('你好.txt'),
      screenText.replace(/\s+/g, ' ').slice(0, 80),
    )
    check('二进制正常数据：不被误判为拒绝（无错误提示）', binaryDataNotice === 0, `notice=${binaryDataNotice}`)

    // 7.7) P0 回归 1（v3 打回）：前缀兜底只认首帧。已连接后程序正常输出「错误：…」
    //      不是拒绝 —— 状态必须保持「已连接」、不出现 notice、socket 不能被关。
    //      旧代码对每一帧都判前缀，轨迹会变成 连接中→已连接→已断开。
    const closedBeforeNormalPrefix = terminalClosed.length
    const noticeBeforeNormalPrefix = await page.locator('.ls-srv-term .ls-notice').count()
    binaryDataSocket?.send('错误：这是正常输出')
    await page
      .waitForFunction(
        () => (document.querySelector('.ls-srv-term .xterm-rows')?.textContent ?? '').includes('错误：这是正常输出'),
        undefined,
        { timeout: 3000 },
      )
      .catch(() => {})
    await page.waitForTimeout(200)
    const normalPrefixState = await termStateText()
    const normalPrefixNotice = await page.locator('.ls-srv-term .ls-notice').count()
    const normalPrefixClosed = terminalClosed.length
    const normalPrefixScreen = await page.locator('.ls-srv-term .xterm-rows').innerText()
    check('P0 回归 1：已连接后「错误：」开头的正常输出仍显示「已连接」', normalPrefixState === '已连接', normalPrefixState)
    check(
      'P0 回归 1：正常输出不产生错误提示',
      normalPrefixNotice === noticeBeforeNormalPrefix,
      `notice ${noticeBeforeNormalPrefix} → ${normalPrefixNotice}`,
    )
    check(
      'P0 回归 1：正常输出不关闭 socket',
      normalPrefixClosed === closedBeforeNormalPrefix,
      `closed ${closedBeforeNormalPrefix} → ${normalPrefixClosed}`,
    )
    check(
      'P0 回归 1：「错误：」开头的正常输出写进终端屏幕',
      normalPrefixScreen.includes('错误：这是正常输出'),
      normalPrefixScreen.replace(/\s+/g, ' ').slice(-80),
    )

    // 7.8) P0 回归 2（v3 打回）：UTF-8 跨帧。把「你好世界」的 12 字节拆成 1 + 11
    //      两个二进制帧（故意劈开「你」的 3 字节序列：E4 | BD A0 …），且首帧就在
    //      连接中状态。前端必须把原始字节交给 xterm 流式解码 → 屏幕上无 U+FFFD 且
    //      中文完整；旧代码逐帧 `new TextDecoder().decode()`，首帧 E4 就成「�」。
    //      先断开已连接会话再重连，让 1 字节帧成为新连接的**首帧**（同时覆盖首帧探针解码）。
    binaryDataSocket?.send(JSON.stringify({ type: 'error', message: '断开以验证 UTF-8 跨帧' }))
    await waitTermState('已断开')
    const socketsBeforeSplit = terminalSockets.length
    await page.locator('.ls-srv-term__bar button', { hasText: '重新连接' }).click()
    for (let i = 0; i < 50 && terminalSockets.length <= socketsBeforeSplit; i += 1) {
      await page.waitForTimeout(100)
    }
    await page.waitForTimeout(300)
    const splitSocket = terminalSockets[terminalSockets.length - 1]
    const helloBytes = Buffer.from('你好世界', 'utf8')
    splitSocket?.send(helloBytes.subarray(0, 1))
    splitSocket?.send(helloBytes.subarray(1))
    await page
      .waitForFunction(
        () => (document.querySelector('.ls-srv-term .xterm-rows')?.textContent ?? '').includes('你好世界'),
        undefined,
        { timeout: 3000 },
      )
      .catch(() => {})
    const splitScreen = await page.locator('.ls-srv-term .xterm-rows').innerText()
    const splitState = await termStateText()
    check('P0 回归 2：UTF-8 跨帧后中文完整显示', splitScreen.includes('你好世界'), splitScreen.replace(/\s+/g, ' ').slice(-80))
    check('P0 回归 2：跨帧不出现 U+FFFD 替换字符', !splitScreen.includes('\uFFFD'), splitScreen.replace(/\s+/g, ' ').slice(-80))
    check('P0 回归 2：跨帧正常数据后状态为「已连接」', splitState === '已连接', splitState)

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
