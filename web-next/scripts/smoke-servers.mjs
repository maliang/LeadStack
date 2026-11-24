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
/** 批量动作里 srv-beta 的失败原文：弹窗必须原样透传，不得被前端包装。 */
const BATCH_FAIL_ERROR = 'agent offline: 无法执行批量动作（mock 失败原文）'
/** 重试仍失败时的第二次 error 原文：用于反例断言「失败段 error 被更新」。 */
const RETRY_STILL_FAIL_ERROR = 'agent offline: 重试仍失败（第二次原文）'

function user(terminalUse, write = false) {
  return {
    id: 'smoke-servers-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: [
      'server.read',
      ...(terminalUse ? ['terminal.use'] : []),
      ...(write ? ['server.write', 'group.write'] : []),
    ],
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
    panel_credential_id: null,
    ...overrides,
  }
}

const SERVERS = [
  serverFixture({ id: 'srv-alpha', panel_credential_id: '{{SECRET_hnjdy2wo}}' }),
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
  state.groups = state.groups || [
    { id: 'grp-default', name: '默认分组', description: '', member_count: 1, updated_at: Date.now() },
    { id: 'grp-test', name: '测试分组', description: '', member_count: 1, updated_at: Date.now() },
  ]
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: user(state.terminalUse, state.write) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: user(state.terminalUse, state.write) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      state.serverCalls += 1
      return json(200, { data: SERVERS })
    }
    if (path === '/api/v1/servers' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      state.serverCreateBodies = state.serverCreateBodies || []
      state.serverCreateBodies.push(body)
      return json(200, {
        data: {
          ...serverFixture({ id: 'srv-new', name: body.name, host: body.host, mode: body.mode }),
          port: body.port,
        },
      })
    }
    if (path === '/api/v1/credentials' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      state.createdCredential = body
      return json(201, {
        data: { id: 'cred-new', ...body, username: body.username || '', server_count: 0, fingerprint: null, created_at: Date.now(), panel_url: body.panel_url || null, insecure_skip_verify: false },
      })
    }
    if (path === '/api/v1/credentials' && method === 'GET') {
      return json(200, {
        data: [
          { id: 'cred-ssh', name: 'deploy-key', kind: 'key', username: 'root', server_count: 0 },
          { id: 'cred-baota', name: 'panel-key', kind: 'baota', username: 'baota', panel_url: 'https://panel.example.com:8888/entry', server_count: 0 },
        ],
      })
    }
    if (path === '/api/v1/server-groups' && method === 'GET') {
      return json(200, { data: state.groups })
    }
    if (path === '/api/v1/server-groups' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      const created = {
        id: `grp-${state.groups.length + 1}`,
        name: body.name,
        description: body.description || '',
        member_count: 0,
        updated_at: Date.now(),
      }
      state.groups.push(created)
      return json(201, { data: created })
    }
    const groupItem = /^\/api\/v1\/server-groups\/([^/]+)$/.exec(path)
    if (groupItem && method === 'PATCH') {
      const id = decodeURIComponent(groupItem[1])
      const body = JSON.parse(request.postData() || '{}')
      const found = state.groups.find((group) => group.id === id)
      if (!found) return json(404, { error: { code: 'not_found' } })
      Object.assign(found, { name: body.name ?? found.name, description: body.description ?? found.description, updated_at: Date.now() })
      return json(200, { data: found })
    }
    if (groupItem && method === 'DELETE') {
      const id = decodeURIComponent(groupItem[1])
      state.groups = state.groups.filter((group) => group.id !== id)
      return json(200, { data: { ok: true } })
    }

    const detail = /^\/api\/v1\/servers\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      const found = SERVERS.find((item) => item.id === decodeURIComponent(detail[1]))
      return found ? json(200, { data: found }) : json(404, { error: { code: 'not_found' } })
    }
    if (detail && method === 'PATCH') {
      const id = decodeURIComponent(detail[1])
      const body = JSON.parse(request.postData() || '{}')
      state.serverPatchBodies = state.serverPatchBodies || []
      state.serverPatchBodies.push(body)
      const found = SERVERS.find((item) => item.id === id) ?? SERVERS[0]
      return json(200, { data: { ...found, ...body } })
    }

    const panelLogin = /^\/api\/v1\/servers\/([^/]+)\/panel-login$/.exec(path)
    if (panelLogin && method === 'POST') {
      return json(200, {
        data: { url: 'https://panel.example.com:8888/login?tmp_token=smoke-token', expires_in: 120 },
      })
    }

    const panel = /^\/api\/v1\/servers\/([^/]+)\/panel$/.exec(path)
    if (panel && method === 'GET') {
      return json(200, {
        data: { server_id: decodeURIComponent(panel[1]), panel_url: 'https://panel.example.com:8888/entry', auth_type: 'session' },
      })
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

    if (path === '/api/v1/servers/batch' && method === 'POST') {
      state.batchCalls += 1
      const body = JSON.parse(request.postData() || '{}')
      state.batchBodies.push(body)
      // 默认按「alpha 成功 / beta 失败」返回；指定 batchResponder 时按调用序号定制（重试场景）。
      const data =
        typeof state.batchResponder === 'function'
          ? state.batchResponder(state.batchCalls, body)
          : {
              results: [
                { id: 'srv-alpha', ok: true },
                { id: 'srv-beta', ok: false, error: BATCH_FAIL_ERROR },
              ],
              summary: { ok: 1, failed: 1 },
            }
      return json(200, { data })
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

  const state = { terminalUse: false, write: false, serverCalls: 0, filePaths: [], batchCalls: 0, batchBodies: [] }
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  await page.addInitScript(() => {
    window.__opened = []
    window.open = (target) => {
      const fake = {
        closed: false,
        opener: null,
        location: { replace: (url) => window.__opened.push(String(url)) },
      }
      if (target && target !== 'about:blank') window.__opened.push(String(target))
      return fake
    }
  })
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

    // 2a) 列表项基础监控信息
    const firstRowText = await page.locator('.ls-srv-row').first().innerText()
    check(
      '列表项显示 CPU / 内存 / 磁盘指标',
      firstRowText.includes('CPU 13%') && firstRowText.includes('内存 33%') && firstRowText.includes('磁盘 40%'),
      firstRowText.replace(/\s+/g, ' ').slice(0, 140),
    )

    // 2b) 宝塔 API 一键登录：只有绑定了面板凭据的行显示入口
    check(
      '仅绑定面板凭据的服务器显示宝塔登录',
      (await page.locator('#server-srv-alpha-panel').count()) === 1 &&
        (await page.locator('#server-srv-beta-panel').count()) === 0,
    )
    check('列表项宝塔按钮文案为「宝塔登录」', (await page.locator('#server-srv-alpha-panel').innerText()).includes('宝塔登录'))
    await page.click('#server-srv-alpha-panel')
    await page.waitForTimeout(250)
    const openedPanel = await page.evaluate(() => window.__opened.slice())
    check(
      '点击宝塔登录打开一次性登录地址且不进页面文案',
      openedPanel.some((url) => url.includes('tmp_token=smoke-token')) &&
        !(await page.locator('body').innerText()).includes('panel.example.com'),
      openedPanel.join(',').slice(0, 80),
    )
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

    // 8) 手动刷新会重新请求列表
    const callsBefore = state.serverCalls
    await page.click('#servers-refresh')
    await page.waitForTimeout(500)
    check('刷新按钮重新请求列表', state.serverCalls > callsBefore, `${callsBefore} → ${state.serverCalls}`)
    await shot(page, 'servers-1280-list-expanded')

    // 9) n3 批量操作（有 server.write）：选择集 → POST /servers/batch 请求体、结果弹窗、删除二次确认
    const batchContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    const batchState = {
      terminalUse: false,
      write: true,
      serverCalls: 0,
      filePaths: [],
      batchCalls: 0,
      batchBodies: [],
      serverCreateBodies: [],
      serverPatchBodies: [],
    }
    await installMockApi(batchContext, batchState)
    const batchPage = await batchContext.newPage()
    const batchErrors = []
    batchPage.on('pageerror', (error) => batchErrors.push(error.message))
    const loginAs = async (target) => {
      await target.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
      await target.waitForSelector('#login-username', { timeout: 15000 })
      await target.fill('#login-username', 'admin')
      await target.fill('#login-password', PASSWORD)
      await target.click('#login-submit')
      await target.waitForSelector('.ls-shell', { timeout: 15000 })
      await target.goto(`${base}/#/servers`, { waitUntil: 'domcontentloaded' })
      await target.waitForSelector('.ls-srv-row', { timeout: 15000 })
    }
    try {
      await loginAs(batchPage)

      /* 新增服务器：agent / SSH 两种模式 + 请求体字段 */
      await batchPage.click('#servers-create')
      await batchPage.waitForSelector('#server-form', { timeout: 5000 })
      await batchPage.fill('#server-form-name', 'gamma-node')
      await batchPage.fill('#server-form-host', '10.0.0.33')
      await batchPage.click('#server-form-mode-ssh')
      await batchPage.waitForSelector('#server-form-credential-username', { timeout: 5000 })
      await batchPage.fill('#server-form-credential-username', 'root')
      await batchPage.fill('#server-form-credential-password', 'server-password')
      await batchPage.fill('#server-form-tags', 'edge, 测试,')
      check('标签输入分隔符后渲染为可删除标签块', (await batchPage.locator('#server-form .ls-tag').count()) === 2)
      await batchPage.selectOption('#server-form-group', 'grp-test')
      const createResponse = batchPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/servers',
        { timeout: 10000 },
      )
      await batchPage.click('#server-form-submit')
      await createResponse
      await batchPage.waitForSelector('#server-form', { state: 'detached', timeout: 8000 })
      const createBody = batchState.serverCreateBodies.at(-1) || {}
      check(
        '新增服务器提交 name/host/port/mode/credential',
        createBody.name === 'gamma-node' &&
          createBody.host === '10.0.0.33' &&
          createBody.port === 22 &&
          createBody.mode === 'ssh' &&
          createBody.credential_id === 'cred-new' &&
          batchState.createdCredential?.kind === 'password' &&
          batchState.createdCredential?.username === 'root',
        JSON.stringify(createBody),
      )
      check(
        '新增服务器标签按逗号/空白拆分、分组按 group_ids 提交',
        Array.isArray(createBody.tags) &&
          createBody.tags.includes('edge') &&
          createBody.tags.includes('测试') &&
          createBody.group === '测试分组' &&
          Array.isArray(createBody.group_ids) &&
          createBody.group_ids[0] === 'grp-test',
        JSON.stringify({ tags: createBody.tags, group: createBody.group, group_ids: createBody.group_ids }),
      )
      await batchPage.waitForSelector('#servers-action-notice', { timeout: 5000 })

      /* 编辑 agent 服务器：地址/端口只读，PATCH 不提交 host/port/mode/credential */
      await batchPage.click('#server-srv-alpha-edit')
      await batchPage.waitForSelector('#server-form', { timeout: 5000 })
      check(
        'agent 编辑时地址只读、端口输入不渲染',
        (await batchPage.locator('#server-form-host').getAttribute('readonly')) !== null &&
          (await batchPage.locator('#server-form-port').count()) === 0,
      )
      await batchPage.fill('#server-form-name', 'alpha-renamed')
      const patchResponse = batchPage.waitForResponse(
        (response) =>
          response.request().method() === 'PATCH' && new URL(response.url()).pathname === '/api/v1/servers/srv-alpha',
        { timeout: 10000 },
      )
      await batchPage.click('#server-form-submit')
      await patchResponse
      await batchPage.waitForSelector('#server-form', { state: 'detached', timeout: 8000 })
      const patchBody = batchState.serverPatchBodies.at(-1) || {}
      check(
        '编辑 agent 服务器只提交名称，不提交 host/port',
        patchBody.name === 'alpha-renamed' && !('host' in patchBody) && !('port' in patchBody),
        JSON.stringify(patchBody),
      )
      check(
        '编辑服务器不提交接入方式与凭证',
        !('mode' in patchBody) && !('credential_id' in patchBody),
        JSON.stringify(patchBody),
      )

      /* 分组管理弹窗：新增 / 删除 */
      await batchPage.click('#servers-groups')
      await batchPage.waitForSelector('#group-manager-create', { timeout: 5000 })
      await batchPage.waitForFunction(() => document.querySelectorAll('.ls-group-manager__item').length === 2, null, { timeout: 5000 })
      check('分组管理弹窗展示现有分组', (await batchPage.locator('.ls-group-manager__item').count()) === 2)
      await batchPage.click('#group-manager-create')
      await batchPage.fill('#group-manager-name', '临时分组')
      await batchPage.fill('#group-manager-desc', '弹窗 CRUD 测试')
      await batchPage.locator('.ls-group-manager__form-actions .ls-btn--primary').click()
      await batchPage.waitForFunction(() => document.body.innerText.includes('临时分组'), null, { timeout: 5000 })
      check('分组管理可以新增分组', (await batchPage.locator('.ls-group-manager__item').count()) === 3)
      const tempItem = batchPage.locator('.ls-group-manager__item', { hasText: '临时分组' })
      await tempItem.locator('.ls-btn--danger').click()
      await tempItem.locator('.ls-btn--danger').click()
      await batchPage.waitForFunction(() => document.querySelectorAll('.ls-group-manager__item').length === 2, null, { timeout: 5000 })
      check('分组管理可以删除分组', (await batchPage.locator('.ls-group-manager__item').count()) === 2)
      await batchPage.locator('.ls-modal__foot button', { hasText: '关闭' }).click()

      check('有 server.write 时每行出现选择框', (await batchPage.locator('.ls-srv-row__select input').count()) === 2)
      check('未选中时不出批量工具条', (await batchPage.locator('.ls-servers-batch').count()) === 0)
      await batchPage.locator('.ls-srv-row__select input').nth(0).check()
      await batchPage.locator('.ls-srv-row__select input').nth(1).check()
      await batchPage.waitForSelector('.ls-servers-batch', { timeout: 5000 })
      const batchBarText = (await batchPage.locator('.ls-servers-batch__count').innerText()).trim()
      check('批量工具条显示已选 2 台', batchBarText === '已选 2 台', batchBarText)

      await batchPage.locator('.ls-servers-batch__actions button', { hasText: '设置分组' }).click()
      await batchPage.waitForSelector('#servers-batch-form', { timeout: 5000 })
      const groupOptions = await batchPage.locator('#servers-batch-group option').allInnerTexts()
      check(
        '分组下拉带出既有分组',
        groupOptions.includes('默认分组') && groupOptions.includes('测试分组'),
        groupOptions.join(' / '),
      )
      await batchPage.selectOption('#servers-batch-group', { label: '测试分组' })
      const batchResponse = batchPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/servers/batch',
        { timeout: 10000 },
      )
      await batchPage.locator('.ls-modal__foot button', { hasText: '设置分组' }).click()
      await batchResponse
      await batchPage.waitForSelector('.ls-batch-result', { timeout: 10000 })

      const batchBody = batchState.batchBodies.at(-1) || {}
      check('设置分组只发一次 POST /servers/batch', batchState.batchCalls === 1, `${batchState.batchCalls} 次`)
      check(
        '请求体 ids 恰好是选中的 2 台',
        Array.isArray(batchBody.ids) &&
          batchBody.ids.length === 2 &&
          batchBody.ids.includes('srv-alpha') &&
          batchBody.ids.includes('srv-beta'),
        JSON.stringify(batchBody.ids),
      )
      check('请求体 action 为 set-group', batchBody.action === 'set-group', String(batchBody.action))
      check(
        '请求体 params 只带 group',
        Boolean(batchBody.params) &&
          Object.keys(batchBody.params).length === 1 &&
          batchBody.params.group === '测试分组',
        JSON.stringify(batchBody.params),
      )

      const summaryText = (await batchPage.locator('.ls-batch-result__summary').innerText()).trim()
      check('结果弹窗汇总等于「成功 1 台，失败 1 台」', summaryText === '成功 1 台，失败 1 台', summaryText)
      const failedRowText = (await batchPage.locator('.ls-batch-result__error').innerText()).trim()
      check('失败行原样显示后端 error 文案', failedRowText === BATCH_FAIL_ERROR, failedRowText)
      await batchPage.locator('.ls-modal__foot button', { hasText: '关闭' }).click()
      await batchPage.waitForSelector('.ls-batch-result', { state: 'detached', timeout: 5000 })

      // 删除必须二次确认：取消后一次请求都不能发
      await batchPage.locator('.ls-srv-row__select input').nth(0).check()
      await batchPage.locator('.ls-srv-row__select input').nth(1).check()
      await batchPage.waitForSelector('.ls-servers-batch', { timeout: 5000 })
      await batchPage.locator('.ls-servers-batch__actions button', { hasText: '删除' }).click()
      await batchPage.waitForSelector('.ls-batch-form__warning', { timeout: 5000 })
      const deleteWarning = (await batchPage.locator('.ls-batch-form__warning').innerText()).trim()
      check('删除确认写明将永久删除 2 台', deleteWarning.includes('2 台'), deleteWarning)
      await batchPage.locator('.ls-modal__foot button', { hasText: '取消' }).click()
      await batchPage.waitForTimeout(400)
      check('删除取消后零请求', batchState.batchCalls === 1, `${batchState.batchCalls} 次`)

      // 320px：批量工具条必须换行而不是横向溢出（先等字体与布局稳定再量）
      await batchPage.setViewportSize({ width: 320, height: 800 })
      await batchPage.waitForSelector('.ls-servers-batch', { timeout: 5000 })
      await batchPage.evaluate(() => document.fonts.ready)
      await batchPage.waitForTimeout(400)
      const narrowBatch = await batchPage.evaluate(() => {
        const root = document.documentElement
        const bar = document.querySelector('.ls-servers-batch')
        return {
          overflow: root.scrollWidth - root.clientWidth,
          barWidth: bar ? bar.getBoundingClientRect().width : 0,
          viewport: window.innerWidth,
          buttons: document.querySelectorAll('.ls-servers-batch__actions button').length,
        }
      })
      check(
        '320px 批量工具条不横向溢出',
        narrowBatch.overflow <= 0 && narrowBatch.barWidth <= narrowBatch.viewport + 1,
        `溢出 ${narrowBatch.overflow}px / 工具条 ${Math.round(narrowBatch.barWidth)}px / ${narrowBatch.buttons} 个动作`,
      )
      check('批量页无未捕获异常', batchErrors.length === 0, batchErrors.slice(0, 2).join(' | '))
    } finally {
      await batchContext.close().catch(() => {})
    }

    // 10) 无 server.write：选择框与工具条都不渲染，批量接口零请求
    const readOnlyBatchContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    const readOnlyBatchState = {
      terminalUse: false,
      write: false,
      serverCalls: 0,
      filePaths: [],
      batchCalls: 0,
      batchBodies: [],
    }
    await installMockApi(readOnlyBatchContext, readOnlyBatchState)
    const readOnlyBatchPage = await readOnlyBatchContext.newPage()
    const readOnlyBatchErrors = []
    readOnlyBatchPage.on('pageerror', (error) => readOnlyBatchErrors.push(error.message))
    try {
      await loginAs(readOnlyBatchPage)
      check(
        '无 server.write 时不渲染选择框',
        (await readOnlyBatchPage.locator('.ls-srv-row__select input').count()) === 0,
      )
      check(
        '无 server.write 时不渲染批量工具条',
        (await readOnlyBatchPage.locator('.ls-servers-batch').count()) === 0,
      )
      check(
        '无 server.write 时 POST /servers/batch 零请求',
        readOnlyBatchState.batchCalls === 0,
        `${readOnlyBatchState.batchCalls} 次`,
      )
      check('无 server.write 页面无未捕获异常', readOnlyBatchErrors.length === 0, readOnlyBatchErrors.slice(0, 2).join(' | '))
    } finally {
      await readOnlyBatchContext.close().catch(() => {})
    }

    // 11) 失败行单独重试：只重发失败那台；重试成功后失败段消失、汇总变「成功 2 台，失败 0 台」
    const retryContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    const retryState = {
      terminalUse: false,
      write: true,
      serverCalls: 0,
      filePaths: [],
      batchCalls: 0,
      batchBodies: [],
      // 第一次批量：alpha 成功 + beta 失败；第二次（重试）：beta 成功
      batchResponder: (call) =>
        call === 1
          ? {
              results: [
                { id: 'srv-alpha', ok: true },
                { id: 'srv-beta', ok: false, error: BATCH_FAIL_ERROR },
              ],
              summary: { ok: 1, failed: 1 },
            }
          : { results: [{ id: 'srv-beta', ok: true }], summary: { ok: 1, failed: 0 } },
    }
    await installMockApi(retryContext, retryState)
    const retryPage = await retryContext.newPage()
    const retryErrors = []
    retryPage.on('pageerror', (error) => retryErrors.push(error.message))
    try {
      await loginAs(retryPage)
      await retryPage.locator('.ls-srv-row__select input').nth(0).check()
      await retryPage.locator('.ls-srv-row__select input').nth(1).check()
      await retryPage.waitForSelector('.ls-servers-batch', { timeout: 5000 })
      const firstBatch = retryPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname === '/api/v1/servers/batch',
      )
      await retryPage.locator('.ls-servers-batch__actions button', { hasText: '设置分组' }).click()
      await retryPage.waitForSelector('#servers-batch-form', { timeout: 5000 })
      await retryPage.selectOption('#servers-batch-group', { label: '测试分组' })
      await retryPage.locator('.ls-modal__foot button', { hasText: '设置分组' }).click()
      await firstBatch
      await retryPage.waitForSelector('.ls-batch-result', { timeout: 10000 })
      const firstBody = retryState.batchBodies[0] || {}

      const retryFailedRows = retryPage.locator('.ls-batch-result__row--failed')
      const failedRowCount = await retryFailedRows.count()
      check('失败段只出现 srv-beta 一行', failedRowCount === 1, `${failedRowCount} 行`)
      const failedRowName = (await retryFailedRows.locator('.ls-batch-result__name').innerText()).trim()
      check('失败行显示 srv-beta 的名称 beta-node', failedRowName === 'beta-node', failedRowName)
      const failedRowError = (await retryFailedRows.locator('.ls-batch-result__error').innerText()).trim()
      check('失败行原样显示后端 error 文案', failedRowError === BATCH_FAIL_ERROR, failedRowError)
      const retryButtons = retryFailedRows.locator('.ls-batch-result__retry')
      const retryButtonCount = await retryButtons.count()
      check('失败行重试按钮恰 1 个', retryButtonCount === 1, `${retryButtonCount} 个`)

      const retryResponse = retryPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname === '/api/v1/servers/batch',
      )
      await retryButtons.first().click()
      await retryResponse
      const retryBody = retryState.batchBodies.at(-1) || {}
      check(
        '重试只重发失败的那台',
        Array.isArray(retryBody.ids) && retryBody.ids.length === 1 && retryBody.ids[0] === 'srv-beta',
        JSON.stringify(retryBody.ids),
      )
      check('重试新增恰好 1 次 POST', retryState.batchCalls === 2, `${retryState.batchCalls} 次`)
      check(
        '重试 action 与首次相同',
        retryBody.action === firstBody.action && retryBody.action === 'set-group',
        String(retryBody.action),
      )
      check(
        '重试 params 与首次相同',
        JSON.stringify(retryBody.params) === JSON.stringify(firstBody.params),
        JSON.stringify(retryBody.params),
      )

      await retryPage.waitForFunction(
        () => document.querySelectorAll('.ls-batch-result__row--failed').length === 0,
        null,
        { timeout: 10000 },
      )
      check('重试成功后失败段消失', (await retryFailedRows.count()) === 0, `${await retryFailedRows.count()} 行`)
      const okSummary = (await retryPage.locator('.ls-batch-result__summary').innerText()).trim()
      check('重试成功后汇总变为成功 2 台失败 0 台', okSummary === '成功 2 台，失败 0 台', okSummary)
      const okRowCount = await retryPage.locator('.ls-batch-result__row--ok').count()
      check('重试成功后成功段为 2 行', okRowCount === 2, `${okRowCount} 行`)
      check('重试页无未捕获异常', retryErrors.length === 0, retryErrors.slice(0, 2).join(' | '))
    } finally {
      await retryContext.close().catch(() => {})
    }

    // 12) 反例：重试仍失败 —— srv-beta 留在失败段，error 更新为第二次的原文
    const retryFailContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    const retryFailState = {
      terminalUse: false,
      write: true,
      serverCalls: 0,
      filePaths: [],
      batchCalls: 0,
      batchBodies: [],
      batchResponder: (call) =>
        call === 1
          ? {
              results: [
                { id: 'srv-alpha', ok: true },
                { id: 'srv-beta', ok: false, error: BATCH_FAIL_ERROR },
              ],
              summary: { ok: 1, failed: 1 },
            }
          : {
              results: [{ id: 'srv-beta', ok: false, error: RETRY_STILL_FAIL_ERROR }],
              summary: { ok: 0, failed: 1 },
            },
    }
    await installMockApi(retryFailContext, retryFailState)
    const retryFailPage = await retryFailContext.newPage()
    try {
      await loginAs(retryFailPage)
      await retryFailPage.locator('.ls-srv-row__select input').nth(0).check()
      await retryFailPage.locator('.ls-srv-row__select input').nth(1).check()
      await retryFailPage.waitForSelector('.ls-servers-batch', { timeout: 5000 })
      const firstFailBatch = retryFailPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname === '/api/v1/servers/batch',
      )
      await retryFailPage.locator('.ls-servers-batch__actions button', { hasText: '设置分组' }).click()
      await retryFailPage.waitForSelector('#servers-batch-form', { timeout: 5000 })
      await retryFailPage.selectOption('#servers-batch-group', { label: '测试分组' })
      await retryFailPage.locator('.ls-modal__foot button', { hasText: '设置分组' }).click()
      await firstFailBatch
      await retryFailPage.waitForSelector('.ls-batch-result', { timeout: 10000 })

      const stillFailedRows = retryFailPage.locator('.ls-batch-result__row--failed')
      const secondFailBatch = retryFailPage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname === '/api/v1/servers/batch',
      )
      await stillFailedRows.locator('.ls-batch-result__retry').first().click()
      await secondFailBatch
      await retryFailPage.waitForFunction(
        (expected) => {
          const error = document.querySelector('.ls-batch-result__row--failed .ls-batch-result__error')
          return error !== null && error.textContent.trim() === expected
        },
        RETRY_STILL_FAIL_ERROR,
        { timeout: 10000 },
      )
      const stillFailedCount = await stillFailedRows.count()
      const stillFailedName = (await stillFailedRows.locator('.ls-batch-result__name').innerText()).trim()
      check(
        '反例：重试仍失败时 srv-beta 留在失败段',
        stillFailedCount === 1 && stillFailedName === 'beta-node',
        `${stillFailedCount} 行 / ${stillFailedName}`,
      )
      const stillFailedError = (await stillFailedRows.locator('.ls-batch-result__error').innerText()).trim()
      check('反例：error 更新为第二次原文', stillFailedError === RETRY_STILL_FAIL_ERROR, stillFailedError)
      const stillFailedSummary = (await retryFailPage.locator('.ls-batch-result__summary').innerText()).trim()
      check('反例：汇总仍为成功 1 台失败 1 台', stillFailedSummary === '成功 1 台，失败 1 台', stillFailedSummary)
      const stillFailedIds = retryFailState.batchBodies.at(-1)?.ids
      check(
        '反例：重试请求仍只发失败的那台',
        Array.isArray(stillFailedIds) && stillFailedIds.length === 1 && stillFailedIds[0] === 'srv-beta',
        JSON.stringify(stillFailedIds),
      )
    } finally {
      await retryFailContext.close().catch(() => {})
    }

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
