#!/usr/bin/env node
/**
 * 服务器详情页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
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
// 直接读源模块拿到「宝塔」示例原文：断言「从示例插入」必须与 login-samples.ts 逐字节一致，
// 不能在冒烟里抄一份副本（抄的副本会和源文件一起漂移，红不了）。
import { LOGIN_SCRIPT_SAMPLES } from '../src/lib/login-samples.ts'

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

const ALL_PERMISSIONS = ['server.read', 'server.write', 'server.update', 'server.edit', 'terminal.use']


function user(permissions = ALL_PERMISSIONS) {
  return {
    id: 'smoke-servers-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: [...permissions],
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


const PANEL_URL = 'https://panel.example.com:8888/alpha'
const LOGIN_TERMINAL = 'cd /www/wwwroot/alpha && bt 14 && echo ok'

/* n3 新界面：指标 / 测试连接 / 刷新主机信息 / agent 升级 */
const METRIC_POINTS = 6
const METRICS_ERROR = 'metrics backend exploded while loading series'
const CONN_ERROR = 'ssh: handshake failed: no matching host key type'
const UPGRADE_ERROR = 'agent archive checksum mismatch: expected a valid sha256'
const NEW_HOSTNAME = 'alpha-node-reborn'

const CONN_OK = {
  ok: true,
  latency_ms: 12,
  ssh_banner: 'SSH-2.0-OpenSSH_9.6p1',
  host_key_fingerprint: 'SHA256:alphaHostKeyFingerprint0123456789',
  error: null,
}

/** mock 指标点：均匀落在 [from, to] 内，点数固定，便于断言 polyline 点数。 */
function metricPoints(from, to, base, count = METRIC_POINTS) {
  return Array.from({ length: count }, (_, index) => ({
    ts: from + Math.round(((to - from) * (index + 1)) / (count + 1)),
    value: base + index * 1024 * 1024,
  }))
}


const SERVER = serverFixture({
  id: 'srv-alpha',
  name: 'alpha-node',
  host: '10.0.0.11',
  status: 'online',
  panel_credential_id: 'cred-panel-1',
  credential_id: 'cred-ssh-1',
  notes: '存量备注：只读巡检机',
  login_script: 'cd /www/wwwroot/alpha && bt 14',
  custom_fields: { location: 'beijing' },
  host_info: {
    hostname: 'alpha-node',
    os: 'ubuntu',
    os_version: '24.04',
    kernel: '6.8.0',
    arch: 'amd64',
    cpu_model: 'Xeon',
    cpu_cores: 8,
    mem_total_bytes: 8 * 1024 ** 3,
    disk_total_bytes: 200 * 1024 ** 3,
    disk_used_bytes: 40 * 1024 ** 3,
    uptime_s: 3600,
    timezone: 'Asia/Shanghai',
    docker: true,
    docker_version: '27.0.0',
    ip_addrs: ['10.0.0.11'],
    collected_at: Date.now() - 60_000,
  },
})

const ACCESS = {
  server_id: 'srv-alpha',
  name: 'alpha-node',
  host: '10.0.0.11',
  port: 22,
  username: 'root',
  password: 'p@ssw0rd-LEAK',
  auth_type: 'password',
}

const ACCOUNT_PASSWORD = '{{SECRET_o7tocwoa}}'
const ACCOUNT_PANEL_URL = 'https://10.0.0.11:8888/entry'
const ACCOUNTS = [
  {
    id: 'acc-1',
    server_id: 'srv-alpha',
    name: '宝塔面板',
    address: 'https://10.0.0.11:8888',
    username: 'admin',
    note: '生产面板',
    custom_fields: { 环境: '生产' },
    login_script: 'printf \'{"url":"https://10.0.0.11:8888/entry"}\'',
    has_password: true,
    has_login_script: true,
    created_at: Date.now() - 1000,
    updated_at: Date.now() - 1000,
  },
]

const CHECKS = [
  {
    id: 'chk-1',
    name: 'alpha tcp 22',
    kind: 'tcp',
    target: '10.0.0.11:22',
    interval_s: 60,
    timeout_s: 5,
    params: {},
    enabled: true,
    created_at: Date.now() - 86_400_000,
    server_ids: ['srv-alpha'],
    server_count: 1,
    state: 'up',
    availability_24h: 99.95,
    last_result: { ok: true, latency_ms: 12, status_code: null, message: '', ts: Date.now() - 30_000 },
  },
  {
    id: 'chk-2',
    name: 'beta ping',
    kind: 'ping',
    target: '10.0.0.12',
    interval_s: 60,
    timeout_s: 5,
    params: {},
    enabled: true,
    created_at: Date.now() - 86_400_000,
    server_ids: ['srv-beta'],
    server_count: 1,
    state: 'down',
    availability_24h: 88,
    last_result: { ok: false, latency_ms: null, status_code: null, message: 'timeout', ts: Date.now() - 30_000 },
  },
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

/** 假后端：服务器详情页端点。未覆盖的一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: user(state.permissions) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: user(state.permissions) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, { data: [state.server] })
    }
    if (path === '/api/v1/checks' && method === 'GET') {
      state.checkCalls += 1
      return json(200, { data: CHECKS })
    }

    const detail = /^\/api\/v1\/servers\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      state.detailCalls += 1
      if (decodeURIComponent(detail[1]) !== state.server.id) {
        return json(404, { error: { code: 'not_found', message: 'server not found' } })
      }
      return json(200, { data: state.server })
    }
    if (detail && method === 'PATCH') {
      const body = JSON.parse(request.postData() || '{}')
      state.patchBodies.push(body)
      // 契约：PATCH 响应不含档案字段，档案必须重新 GET 详情读回
      state.server = {
        ...state.server,
        ...(typeof body.notes === 'string' ? { notes: body.notes } : {}),
        ...(typeof body.login_script === 'string' ? { login_script: body.login_script } : {}),
        ...(body.custom_fields && typeof body.custom_fields === 'object'
          ? { custom_fields: body.custom_fields }
          : {}),
      }
      return json(200, { data: { ...state.server, notes: undefined, custom_fields: undefined } })
    }

    const accountsPath = /^\/api\/v1\/servers\/([^/]+)\/accounts$/.exec(path)
    if (accountsPath && method === 'GET') {
      return json(200, { data: state.accounts })
    }
    if (accountsPath && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      state.accountCreateBodies.push(body)
      const created = {
        id: 'acc-new',
        server_id: state.server.id,
        name: body.name,
        address: body.address || '',
        username: body.username || '',
        note: body.note || '',
        custom_fields: body.custom_fields || {},
        login_script: body.login_script || '',
        has_password: Boolean(body.password),
        has_login_script: Boolean(body.login_script),
        created_at: Date.now(),
        updated_at: Date.now(),
      }
      state.accounts.push(created)
      return json(201, { data: created })
    }
    const accountSecret = /^\/api\/v1\/servers\/([^/]+)\/accounts\/([^/]+)\/secret$/.exec(path)
    if (accountSecret && method === 'GET') {
      state.accountSecretCalls += 1
      return json(200, { data: { password: ACCOUNT_PASSWORD } })
    }
    const accountLogin = /^\/api\/v1\/servers\/([^/]+)\/accounts\/([^/]+)\/login$/.exec(path)
    if (accountLogin && method === 'POST') {
      state.accountLoginCalls += 1
      return json(200, {
        data: {
          server_id: state.server.id,
          account_id: decodeURIComponent(accountLogin[2]),
          url: ACCOUNT_PANEL_URL,
          terminal: '',
          output: '',
        },
      })
    }
    const accountItem = /^\/api\/v1\/servers\/([^/]+)\/accounts\/([^/]+)$/.exec(path)
    if (accountItem && method === 'PATCH') {
      const id = decodeURIComponent(accountItem[2])
      const body = JSON.parse(request.postData() || '{}')
      state.accountPatchBodies.push(body)
      const found = state.accounts.find((item) => item.id === id)
      if (found) {
        Object.assign(found, body)
        if (typeof body.password === 'string') found.has_password = body.password !== ''
        if (typeof body.login_script === 'string') found.has_login_script = body.login_script !== ''
      }
      return json(200, { data: found || state.accounts[0] })
    }
    if (accountItem && method === 'DELETE') {
      const id = decodeURIComponent(accountItem[2])
      state.accountDeletes.push(id)
      state.accounts = state.accounts.filter((item) => item.id !== id)
      return json(200, { data: { deleted: true } })
    }

    const access = /^\/api\/v1\/servers\/([^/]+)\/access$/.exec(path)
    if (access && method === 'GET') {
      state.accessCalls += 1
      return json(200, { data: ACCESS })
    }

    const securityAudit = /^\/api\/v1\/servers\/([^/]+)\/security\/audit$/.exec(path)
    if (securityAudit && method === 'POST') {
      state.securityAuditCalls = (state.securityAuditCalls || 0) + 1
      return json(200, {
        data: {
          server_id: state.server.id,
          score: 70,
          checked_at: Date.now(),
          findings: [
            { id: 'firewall', level: 'high', title: '未检测到启用的主机防火墙', detail: 'ufw inactive', recommendation: '启用 ufw' },
            { id: 'fail2ban', level: 'ok', title: 'fail2ban 正在运行', detail: 'active' },
          ],
        },
      })
    }

    const panelLogin = /^\/api\/v1\/servers\/([^/]+)\/panel-login$/.exec(path)
    if (panelLogin && method === 'POST') {
      state.panelLoginCalls = (state.panelLoginCalls || 0) + 1
      return json(200, { data: { url: PANEL_URL, expires_in: 120 } })
    }

    const panel = /^\/api\/v1\/servers\/([^/]+)\/panel$/.exec(path)
    if (panel && method === 'GET') {
      state.panelCalls += 1
      return json(200, { data: { server_id: state.server.id, panel_url: PANEL_URL, auth_type: 'session' } })
    }

    const login = /^\/api\/v1\/servers\/([^/]+)\/login$/.exec(path)
    if (login && method === 'POST') {
      state.loginCalls += 1
      return json(200, { data: { server_id: state.server.id, url: '', terminal: LOGIN_TERMINAL, output: '' } })
    }

    const ticket = /^\/api\/v1\/servers\/([^/]+)\/terminal-ticket$/.exec(path)
    if (ticket && method === 'POST') {
      state.ticketCalls += 1
      return json(200, { data: { ticket: 'one-shot-ticket', expires_in: 60 } })
    }

    const files = /^\/api\/v1\/servers\/([^/]+)\/files$/.exec(path)
    if (files && method === 'GET') {
      state.filesCalls += 1
      const dir = url.searchParams.get('path') || '/'
      const entries =
        dir === '/'
          ? [
              { name: 'etc', is_dir: true, size: 0, mod_time: Date.now() - 60_000, mode: 'drwxr-xr-x' },
              { name: 'app.log', is_dir: false, size: 4096, mod_time: Date.now() - 30_000, mode: '-rw-r--r--' },
            ]
          : []
      return json(200, { data: entries })
    }

    const logs = /^\/api\/v1\/servers\/([^/]+)\/logs$/.exec(path)
    if (logs && method === 'GET') {
      state.logsCalls += 1
      state.logPaths.push(url.searchParams.get('path') || '')
      return json(200, {
        data: {
          lines: ['2026-11-18T10:00:00 line-one', '2026-11-18T10:00:05 line-two'],
          truncated: false,
          path: '/var/log/system.log',
        },
      })
    }

    // n3：指标曲线（GET /metrics?from&to&step）
    const metrics = /^\/api\/v1\/servers\/([^/]+)\/metrics$/.exec(path)
    if (metrics && method === 'GET') {
      const from = Number(url.searchParams.get('from'))
      const to = Number(url.searchParams.get('to'))
      const step = url.searchParams.get('step') || ''
      state.metricsCalls.push({ from, to, step })
      if (state.metricsMode === 'error') {
        return json(500, { error: { code: 'internal', message: METRICS_ERROR } })
      }
      if (state.metricsMode === 'empty') {
        return json(200, { series: [], step })
      }
      return json(200, {
        // 后端契约的字段名是 metric（不是 name）；两条曲线：内存与磁盘
        series: [
          { metric: 'mem.used_bytes', points: metricPoints(from, to, 2 * 1024 ** 3) },
          { metric: 'disk.used_bytes', points: metricPoints(from, to, 40 * 1024 ** 3) },
        ],
        step,
      })
    }

    // n3：测试连接（ok=false 时 error 原样展示）
    const testConnection = /^\/api\/v1\/servers\/([^/]+)\/test-connection$/.exec(path)
    if (testConnection && method === 'POST') {
      state.connectionCalls += 1
      if (state.connectionMode === 'fail') {
        return json(200, {
          ok: false,
          latency_ms: null,
          ssh_banner: null,
          host_key_fingerprint: null,
          error: CONN_ERROR,
        })
      }
      return json(200, CONN_OK)
    }

    // n3：刷新主机信息 → 与后端 handlers_ssh.go:44 一致的真实形状 {data:{host_info:{...}}}
    // 注意不能平铺成 host_info 本身，否则前端漏解包时回归仍会假绿。
    const refreshHost = /^\/api\/v1\/servers\/([^/]+)\/refresh-host-info$/.exec(path)
    if (refreshHost && method === 'POST') {
      state.hostCalls += 1
      return json(200, {
        data: {
          host_info: {
            ...SERVER.host_info,
            hostname: NEW_HOSTNAME,
            collected_at: Date.now(),
          },
        },
      })
    }

    // n3：agent 升级（请求体必须恰好 version/url/sha256 三字段）
    const agentUpgrade = /^\/api\/v1\/servers\/([^/]+)\/agent-upgrade$/.exec(path)
    if (agentUpgrade && method === 'POST') {
      state.upgradeCalls += 1
      state.upgradeBodies.push(JSON.parse(request.postData() || '{}'))
      if (state.upgradeMode === 'fail') {
        return json(200, { server_id: state.server.id, ok: false, error: UPGRADE_ERROR })
      }
      return json(202, { server_id: state.server.id, ok: true })
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

  const state = {
    server: structuredClone(SERVER),
    permissions: ALL_PERMISSIONS,
    detailCalls: 0,
    accessCalls: 0,
    panelCalls: 0,
    loginCalls: 0,
    checkCalls: 0,
    ticketCalls: 0,
    filesCalls: 0,
    logsCalls: 0,
    logPaths: [],
    patchBodies: [],
    // n3：指标曲线 / 测试连接 / 刷新主机信息 / agent 升级
    metricsCalls: [],
    metricsMode: 'ok',
    connectionCalls: 0,
    connectionMode: 'ok',
    hostCalls: 0,
    upgradeCalls: 0,
    upgradeMode: 'ok',
    upgradeBodies: [],
    accounts: structuredClone(ACCOUNTS),
    accountSecretCalls: 0,
    accountLoginCalls: 0,
    accountPatchBodies: [],
    accountCreateBodies: [],
    accountDeletes: [],
  }
  const context = await browser.newContext({ viewport: { width: 320, height: 800 } })
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

  const noHorizontalScroll = () =>
    page.evaluate(() => {
      const root = document.documentElement
      return { overflow: root.scrollWidth - root.clientWidth, width: window.innerWidth }
    })

  /** 320px 下 tab 栏横向滚动：选中的 tab 必须完整落在可视区内。 */
  const activeTabVisible = () =>
    page.evaluate(() => {
      const list = document.querySelector('.ls-tabs')
      const active = list && list.querySelector('[aria-selected="true"]')
      if (!list || !active) return false
      const listRect = list.getBoundingClientRect()
      const activeRect = active.getBoundingClientRect()
      return activeRect.left >= listRect.left - 1 && activeRect.right <= listRect.right + 1
    })


  try {
    // 1) 登录 → 详情页（320px 视口）
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.goto(`${base}/#/servers/srv-alpha`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-detail__title', { timeout: 15000 })
    check('详情页渲染标题', (await page.locator('.ls-detail__title').innerText()).includes('alpha-node'))
    check('详情只拉一次 /servers/{id}', state.detailCalls === 1, `${state.detailCalls} 次`)
    check('七个 tab 都在', (await page.locator('.ls-tabs__btn').count()) === 7, String(await page.locator('.ls-tabs__btn').count()))
    await shot(page, '01-overview')
    check('概览 tab 默认选中', (await page.locator('#server-tab-overview').getAttribute('aria-selected')) === 'true')

    // 2) 概览：面板按钮存在（有 panel_credential_id）
    const panelButton = page.locator('.ls-card button', { hasText: '打开面板' }).first()
    check('有面板凭据时显示「打开面板」', (await panelButton.count()) === 1)
    await panelButton.click()
    await page.waitForTimeout(300)
    const opened = await page.evaluate(() => window.__opened.slice())
    check('打开面板走一次性登录地址', opened.includes(PANEL_URL) && state.panelLoginCalls === 1, opened.join(',').slice(0, 60))
    check('面板地址不进页面文案', !(await page.locator('body').innerText()).includes(PANEL_URL))

    // 3) 登录信息：默认折叠且不发请求
    await page.click('#server-tab-access')
    await page.waitForSelector('.ls-card', { timeout: 5000 })
    await shot(page, '02-access-collapsed')
    check('登录信息默认折叠（无内容块）', (await page.locator('.ls-access').count()) === 0)
    check('折叠态不请求 /access', state.accessCalls === 0, `${state.accessCalls} 次`)

    await page.locator('.ls-card button[aria-expanded="false"]').first().click()
    await page.waitForSelector('.ls-access', { timeout: 5000 })
    await page.waitForSelector('.ls-access__foot', { timeout: 5000 })
    check('展开后才请求 /access', state.accessCalls === 1, `${state.accessCalls} 次`)
    const accessText = await page.locator('.ls-access').innerText()
    await shot(page, '03-access-expanded')
    check('密码默认掩码', accessText.includes('••••••••'))
    check('明文密码不在 DOM 文案里', !accessText.includes(ACCESS.password))
    check('有复制入口', (await page.locator('.ls-kv__tools button').count()) >= 4)

    await page.locator('.ls-card button[aria-expanded="true"]').first().click()
    await page.waitForTimeout(200)
    check('收起后丢弃内容', (await page.locator('.ls-access').count()) === 0)
    check('收起不重复请求', state.accessCalls === 1, `${state.accessCalls} 次`)
    const narrowAccess = await noHorizontalScroll()
    check('320px 登录信息无横向滚动', narrowAccess.overflow <= 0, `溢出 ${narrowAccess.overflow}px`)

    // 3b) 访问账号：多条登录信息 + 按需读密码 + 账号级一键登录
    await page.click('#server-tab-accounts')
    await page.waitForSelector('.ls-account', { timeout: 8000 })
    const accountText = await page.locator('.ls-account').innerText()
    check('访问账号渲染名称/地址/用户名/备注', accountText.includes('宝塔面板') && accountText.includes('10.0.0.11') && accountText.includes('admin') && accountText.includes('生产面板'))
    check('自定义字段可见', accountText.includes('环境') && accountText.includes('生产'))
    check('列表默认掩码密码', accountText.includes('••••••••') && !accountText.includes(ACCOUNT_PASSWORD))
    check('账号不请求密码直到用户点击', state.accountSecretCalls === 0, `${state.accountSecretCalls} 次`)

    await page.locator('.ls-account button[aria-label="显示密码"]').first().click()
    await page.waitForFunction((pwd) => document.body.innerText.includes(pwd), ACCOUNT_PASSWORD, { timeout: 5000 })
    check('点显示密码后才请求 secret', state.accountSecretCalls === 1, `${state.accountSecretCalls} 次`)

    await page.locator('.ls-account__actions button', { hasText: '一键登录' }).click()
    await page.waitForTimeout(300)
    const openedAccount = await page.evaluate(() => window.__opened.slice())
    check('账号脚本返回 url 时打开新窗口', openedAccount.includes(ACCOUNT_PANEL_URL), openedAccount.join(',').slice(0, 80))
    check('账号登录打到账号级端点', state.accountLoginCalls === 1, `${state.accountLoginCalls} 次`)

    await page.locator('.ls-account__actions button', { hasText: '编辑' }).click()
    await page.waitForSelector('#server-account-form', { timeout: 5000 })
    await page.fill('#account-form-address', 'https://10.0.0.11:9999')
    await page.click('#server-account-submit')
    await page.waitForSelector('#server-account-form', { state: 'detached', timeout: 8000 })
    const accountPatch = state.accountPatchBodies.at(-1) || {}
    check(
      '编辑账号 PATCH 提交地址且密码留空不提交',
      accountPatch.address === 'https://10.0.0.11:9999' && accountPatch.password === undefined,
      JSON.stringify(accountPatch),
    )
    const narrowAccounts = await noHorizontalScroll()
    check('320px 访问账号无横向滚动', narrowAccounts.overflow <= 0, `溢出 ${narrowAccounts.overflow}px`)

    // 4) 一键登录：terminal 态给命令 + 复制
    await page.click('#server-tab-login')
    await page.waitForSelector('#server-login-run', { timeout: 5000 })
    await page.click('#server-login-run')
    await page.waitForSelector('.ls-code', { timeout: 5000 })
    await shot(page, '04-login')
    check('一键登录发出 POST /login', state.loginCalls === 1, `${state.loginCalls} 次`)
    check('terminal 态渲染命令', (await page.locator('.ls-code').innerText()).includes('bt 14'))
    check('命令带复制按钮', (await page.locator('button', { hasText: '复制命令' }).count()) >= 1)
    check('320px 登录结果无横向滚动', (await noHorizontalScroll()).overflow <= 0)

    // 5b) 登录脚本编辑：行内 textarea + 示例插入 + 保存/清空/超限
    console.log('')
    log('登录脚本编辑', '行内编辑器：保存请求体逐键 / 示例逐字节 / 8192 超限 / 清空回未配置')
    await page.click('#server-tab-login')
    await page.waitForSelector('#login-script-editor', { timeout: 5000 })
    await shot(page, '05b-login-script')
    const editor = page.locator('#login-script-editor')
    check(
      '脚本编辑器回填当前脚本',
      (await editor.inputValue()) === 'cd /www/wwwroot/alpha && bt 14',
      await editor.inputValue(),
    )
    const baotaScript = LOGIN_SCRIPT_SAMPLES.find((item) => item.key === 'baota')?.script ?? ''
    check('login-samples.ts 的宝塔示例非空', baotaScript.length > 0, `${baotaScript.length} 字符`)
    await editor.fill('')
    await page.click('#login-script-sample-baota')
    const inserted = await editor.inputValue()
    check(
      '从示例插入后 textarea 逐字节等于 login-samples.ts 的宝塔示例',
      inserted === baotaScript,
      `长度 ${inserted.length} vs ${baotaScript.length}`,
    )
    const counter = page.locator('section[aria-label="登录脚本"] span').filter({ hasText: /\/ 8192/ }).first()
    const baotaRunes = [...baotaScript].length
    check(
      '示例插入后计数器按码点计到宝塔脚本长度',
      (await counter.count()) === 1 && (await counter.innerText()).trim() === `${baotaRunes} / 8192`,
      (await counter.count()) === 1 ? (await counter.innerText()).trim() : '无计数器',
    )

    const explicitScript = '#!/usr/bin/env bash\nprintf \'{"terminal":"ssh -p 22 root@1.2.3.4"}\''
    await editor.fill(explicitScript)
    await page.click('#login-script-save')
    await page.waitForTimeout(500)
    const savedPatch = state.patchBodies[state.patchBodies.length - 1] || null
    const savedKeys = savedPatch ? Object.keys(savedPatch).sort() : []
    check(
      '保存请求体恰为 {"login_script":"..."}（逐键比对）',
      savedPatch !== null && savedKeys.length === 1 && savedKeys[0] === 'login_script',
      savedKeys.join(','),
    )
    check(
      '保存请求体的 login_script 与编辑器逐字节一致',
      savedPatch !== null && savedPatch.login_script === explicitScript,
      savedPatch ? `${String(savedPatch.login_script).length} 字符` : '无 PATCH',
    )

    const overlong = 'x'.repeat(8193)
    await editor.evaluate((element, value) => {
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
      setter?.call(element, value)
      element.dispatchEvent(new Event('input', { bubbles: true }))
    }, overlong)
    await page.waitForTimeout(200)
    const afterOverflow = state.patchBodies.length
    const saveDisabled = await page.locator('#login-script-save').isDisabled()
    if (!saveDisabled) await page.click('#login-script-save')
    await page.waitForTimeout(300)
    check(
      '超过 8192 字符时点保存 = 0 次 PATCH',
      state.patchBodies.length === afterOverflow,
      `${afterOverflow} → ${state.patchBodies.length}（保存按钮${saveDisabled ? '禁用' : '可点'}）`,
    )
    const scriptError = page.locator('#login-script-error')
    check(
      '超过 8192 字符出现字段级错误且文案含 8192',
      (await scriptError.count()) === 1 && (await scriptError.innerText()).includes('8192'),
      (await scriptError.count()) === 1 ? (await scriptError.innerText()).slice(0, 60) : '无错误节点',
    )

    await page.click('#login-script-clear')
    await page.waitForSelector('#login-script-clear-confirm', { timeout: 3000 })
    await page.click('#login-script-clear-confirm')
    await page.waitForTimeout(500)
    const clearedPatch = state.patchBodies[state.patchBodies.length - 1] || null
    check(
      '清空请求体恰为 {"login_script":""}',
      clearedPatch !== null &&
        Object.keys(clearedPatch).length === 1 &&
        clearedPatch.login_script === '',
      clearedPatch ? JSON.stringify(clearedPatch).slice(0, 60) : '无 PATCH',
    )
    await page.waitForSelector('section[aria-label="一键登录"]', { timeout: 5000 })
    const loginCardText = await page.locator('section[aria-label="一键登录"]').innerText()
    check('清空后页面回到「未配置」', loginCardText.includes('还没有配置登录脚本'), loginCardText.slice(0, 40))
    check('清空后编辑器 value 为空串', (await editor.inputValue()) === '', JSON.stringify(await editor.inputValue()).slice(0, 60))

    // 6) 检测：只读展示本服务器命中的检测项，无执行按钮
    await page.click('#server-tab-checks')
    await page.waitForSelector('.ls-checks', { timeout: 5000 })
    await shot(page, '06-checks')
    check('检测只拉一次 /checks', state.checkCalls === 1, `${state.checkCalls} 次`)
    check('只显示本服务器的检测项', (await page.locator('.ls-checks__row').count()) === 1)
    check('检测项名称正确', (await page.locator('.ls-checks__name').innerText()) === 'alpha tcp 22')
    check('只读标识可见', (await page.locator('section[aria-label="检测"]').innerText()).includes('只读'))
    check('没有执行检测按钮', (await page.locator('.ls-checks button', { hasText: '执行' }).count()) === 0)
    await page.click('#security-audit-run')
    await page.waitForSelector('.ls-security-score', { timeout: 5000 })
    check('安全体检分数与风险项渲染', (await page.locator('.ls-security-score__value').innerText()).trim() === '70' && (await page.locator('.ls-security-item--high').count()) === 1)
    check('安全体检只发一次请求', state.securityAuditCalls === 1, `${state.securityAuditCalls} 次`)
    const narrowChecks = await noHorizontalScroll()
    check('320px 检测页无横向滚动', narrowChecks.overflow <= 0, `溢出 ${narrowChecks.overflow}px`)

    // 7) 键盘可达：ArrowRight 切 tab
    await page.locator('#server-tab-checks').focus()
    await page.keyboard.press('ArrowRight')
    await page.waitForTimeout(150)
    const activeTab = await page.evaluate(() => document.activeElement && document.activeElement.id)
    check('ArrowRight 焦点回到 tabs 内', String(activeTab).startsWith('server-tab-'), String(activeTab))

    // 10) 切 tab 写回 URL（replace，不新增历史）
    await page.click('#server-tab-overview')
    state.logsCalls = 0
    const historyBefore = await page.evaluate(() => history.length)
    await page.click('#server-tab-logs')
    await page.waitForSelector('.ls-logs', { timeout: 5000 })
    await page.waitForFunction(() => location.hash.includes('tab=logs'), null, { timeout: 5000 })
    check('切到日志 tab 后 URL 同步 ?tab=logs', page.url().includes('?tab=logs'))
    const historyAfter = await page.evaluate(() => history.length)
    check('切 tab 用 replace 不新增历史', historyAfter === historyBefore, `${historyBefore} → ${historyAfter}`)
    check('日志 tab 只请求一次 /logs', state.logsCalls === 1, `${state.logsCalls} 次`)
    await page.waitForFunction(
      () => (document.querySelector('.ls-logs__pre')?.textContent || '').includes('line-one'),
      null,
      { timeout: 5000 },
    )
    check('日志渲染 mock 行', (await page.locator('.ls-logs__pre').innerText()).includes('line-one'))
    await shot(page, '09-logs')
    check('自动刷新默认关闭', (await page.locator('.ls-logs__auto input').isChecked()) === false)
    check('日志行数默认 200', (await page.locator('#server-logs-lines').inputValue()) === '200')
    check('日志路径默认留空（自动选择）', (await page.locator('#server-logs-path').inputValue()) === '')
    check('首次日志请求 path=auto', state.logPaths[0] === 'auto', String(state.logPaths[0]))
    check('实际日志源回显 system.log', (await page.locator('.ls-logs__file').innerText()).includes('/var/log/system.log'))
    check('320px 日志 tab 无横向滚动', (await noHorizontalScroll()).overflow <= 0)

    await page.click('#server-tab-overview')
    await page.waitForSelector('.ls-card', { timeout: 5000 })
    await page.waitForFunction(() => !location.hash.includes('tab='), null, { timeout: 5000 })
    check('切回概览后 URL 去掉 tab 参数', !page.url().includes('tab='))

    // 11) n3 新界面：指标曲线在安全标签 / 测试连接 / 刷新主机信息 / agent 升级
    await page.click('#server-tab-checks')
    await page.waitForSelector('.ls-metrics__grid', { timeout: 15000 })
    const metricsCard = page.locator('.ls-metrics')
    check('指标卡挂在安全标签里', (await metricsCard.count()) === 1)
    const autoQuery = state.metricsCalls[state.metricsCalls.length - 1] || null
    check(
      '进详情自动拉指标且默认窗口 1h',
      autoQuery !== null && autoQuery.to - autoQuery.from === 3600000,
      JSON.stringify(autoQuery),
    )
    check('自动拉取用默认步长 raw', autoQuery !== null && autoQuery.step === 'raw', String(autoQuery?.step))

    const metricsRequest = () =>
      page.waitForResponse(
        (response) =>
          response.request().method() === 'GET' &&
          /\/api\/v1\/servers\/srv-alpha\/metrics\?/.test(response.url()),
        { timeout: 10000 },
      )

    /** 点一下就等这次 /metrics 请求回来，再留 150ms 让 svg 重渲染。 */
    const clickAndAwaitMetrics = async (locator) => {
      const before = state.metricsCalls.length
      await Promise.all([metricsRequest(), locator.click()])
      await page.waitForTimeout(150)
      return state.metricsCalls.length > before ? state.metricsCalls[state.metricsCalls.length - 1] : null
    }

    state.metricsMode = 'ok'
    state.metricsCalls = []
    const metricsRefresh = page.locator('.ls-metrics .ls-card__actions > button')
    check('指标卡有手动刷新入口', (await metricsRefresh.count()) === 1)
    await page.waitForFunction(
      () => {
        const button = document.querySelector('.ls-metrics .ls-card__actions > button')
        return button !== null && button.disabled === false
      },
      null,
      { timeout: 10000 },
    )
    const baselineQuery = await clickAndAwaitMetrics(metricsRefresh)
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-metrics__line').length === 2,
      null,
      { timeout: 10000 },
    )
    check('指标卡画了两条曲线（memory/disk）', (await page.locator('.ls-metrics__line').count()) === 2)
    const memoryPath = (await page.locator('.ls-metrics__line').first().getAttribute('d')) || ''
    const memoryPointCount = await page.locator('.ls-metrics__item').first().locator('.ls-metrics__point').count()
    check('指标曲线 path 非空', memoryPath.trim().length > 0, memoryPath.slice(0, 40))
    check(
      '曲线点数与 mock 数据点一致',
      memoryPointCount === METRIC_POINTS,
      `${memoryPointCount} vs ${METRIC_POINTS}`,
    )
    await page.locator('.ls-metrics__item').first().locator('svg').hover()
    await page.waitForSelector('.ls-metrics__tooltip', { timeout: 5000 })
    const tooltipText = (await page.locator('.ls-metrics__tooltip').innerText()).trim()
    check('hover 曲线显示时间与数值 tooltip', tooltipText.length > 0, tooltipText)
    await page.locator('.ls-metrics__item').first().locator('svg').hover({ position: { x: 2, y: 2 } })
    check('曲线 hover 有十字准线', (await page.locator('.ls-metrics__crosshair').count()) === 1)
    check(
      '默认时间窗 1h（from/to 差 3600000）',
      baselineQuery !== null && baselineQuery.to - baselineQuery.from === 3600000,
      JSON.stringify(baselineQuery),
    )
    check('默认步长 raw', baselineQuery !== null && baselineQuery.step === 'raw', String(baselineQuery?.step))

    const stepButtons = page.locator('.ls-metrics .ls-seg').first().locator('.ls-seg__btn')
    check('步长三档都在', (await stepButtons.count()) === 3, String(await stepButtons.count()))
    const sentSteps = []
    for (const index of [1, 2, 0]) {
      const query = await clickAndAwaitMetrics(stepButtons.nth(index))
      sentSteps.push(query ? query.step : null)
    }
    check('step 三档都按档位发出（1m/1h/raw）', sentSteps.join(',') === '1m,1h,raw', sentSteps.join(','))
    check(
      '步长选中态跟随点击',
      (await stepButtons.nth(0).getAttribute('aria-pressed')) === 'true' &&
        (await stepButtons.nth(2).getAttribute('aria-pressed')) === 'false',
    )

    const windowButtons = page.locator('.ls-metrics .ls-seg').nth(1).locator('.ls-seg__btn')
    check('时间窗三档都在', (await windowButtons.count()) === 3, String(await windowButtons.count()))
    // 1h 是默认档，先点 6h/24h 再回 1h，保证每次点击都真的触发一次请求
    const windowQueries = []
    for (const index of [1, 2, 0]) {
      const query = await clickAndAwaitMetrics(windowButtons.nth(index))
      windowQueries.push(query)
    }
    const windowSpans = windowQueries.map((query) => (query ? query.to - query.from : null))
    check(
      '切 6h/24h/1h 时 from/to 真实变化',
      windowSpans.join(',') === '21600000,86400000,3600000',
      windowSpans.join(','),
    )
    check(
      '窗口越大 from 越靠前',
      windowQueries[0] !== null &&
        windowQueries[1] !== null &&
        windowQueries[0].from < (baselineQuery?.from ?? 0) &&
        windowQueries[1].from < windowQueries[0].from,
      `6h.from=${windowQueries[0]?.from} 24h.from=${windowQueries[1]?.from} 1h.from=${baselineQuery?.from}`,
    )
    check(
      '切时间窗后 step 不变',
      state.metricsCalls[state.metricsCalls.length - 1]?.step === 'raw',
      String(state.metricsCalls[state.metricsCalls.length - 1]?.step),
    )

    // 空态
    state.metricsMode = 'empty'
    await clickAndAwaitMetrics(windowButtons.nth(1))
    await page.waitForFunction(
      () => (document.querySelector('.ls-metrics')?.textContent || '').includes('暂无指标数据'),
      null,
      { timeout: 10000 },
    )
    check('空数据显示「暂无指标数据」', (await metricsCard.innerText()).includes('暂无指标数据'))
    check('空数据不画曲线', (await page.locator('.ls-metrics__line').count()) === 0)

    // 错误态 + 重试
    state.metricsMode = 'error'
    await clickAndAwaitMetrics(metricsRefresh)
    await page.waitForSelector('.ls-metrics [role="alert"]', { timeout: 10000 })
    const metricsErrorText = await page.locator('.ls-metrics [role="alert"]').innerText()
    check('500 时指标卡显示后端错误原文', metricsErrorText.includes(METRICS_ERROR), metricsErrorText.slice(0, 60))
    check('错误态提供重试按钮', (await page.locator('.ls-metrics [role="alert"] button').count()) === 1)
    check('错误态不再画曲线', (await page.locator('.ls-metrics__line').count()) === 0)

    state.metricsMode = 'ok'
    const retryQuery = await clickAndAwaitMetrics(page.locator('.ls-metrics [role="alert"] button'))
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-metrics__line').length === 2,
      null,
      { timeout: 10000 },
    )
    check('重试重新请求并恢复曲线', retryQuery !== null && (await page.locator('.ls-metrics__line').count()) === 2)
    check(
      '重试请求仍带当前窗口（6h）与步长',
      retryQuery !== null && retryQuery.to - retryQuery.from === 21600000 && retryQuery.step === 'raw',
      JSON.stringify(retryQuery),
    )

    // 320px 横向溢出（先确认根元素已就绪再量 scrollWidth）
    const metricsOverflow = await page.evaluate(() => {
      const card = document.querySelector('.ls-metrics')
      const root = document.documentElement
      return {
        card: card ? card.scrollWidth - card.clientWidth : -1,
        root: root.scrollWidth - root.clientWidth,
        width: window.innerWidth,
      }
    })
    check(
      '320px 指标卡无横向溢出',
      metricsOverflow.width === 320 && metricsOverflow.card <= 0 && metricsOverflow.root <= 0,
      JSON.stringify(metricsOverflow),
    )

    await page.click('#server-tab-overview')
    await page.waitForSelector('section[aria-label="测试连接"]', { timeout: 5000 })

    // 12) 测试连接：ok=true 显示延迟/banner，ok=false 原样显示 error
    const connCard = page.locator('section[aria-label="测试连接"]')
    check('测试连接卡存在', (await connCard.count()) === 1)
    const connButton = connCard.locator('header button.ls-btn')
    state.connectionMode = 'ok'
    state.connectionCalls = 0
    await connButton.click()
    await page.waitForFunction(
      () => (document.querySelector('section[aria-label="测试连接"] .ls-conn')?.textContent || '').includes('12 ms'),
      null,
      { timeout: 10000 },
    )
    const connOkText = await connCard.innerText()
    check('测试连接 POST 发出一次', state.connectionCalls === 1, `${state.connectionCalls} 次`)
    check('ok=true 显示延迟', connOkText.includes('12 ms'))
    check('ok=true 显示 banner', connOkText.includes('SSH-2.0-OpenSSH_9.6p1'))
    check('ok=true 显示连接正常', connOkText.includes('连接正常'))

    state.connectionMode = 'fail'
    await connButton.click()
    await page.waitForFunction(
      (message) =>
        (document.querySelector('section[aria-label="测试连接"]')?.textContent || '').includes(message),
      CONN_ERROR,
      { timeout: 10000 },
    )
    const connFailText = await connCard.innerText()
    check('ok=false 原样显示后端 error', connFailText.includes(CONN_ERROR), connFailText.replace(/\s+/g, ' ').slice(0, 80))
    check('ok=false 不误报连接正常', !connFailText.includes('连接正常'))

    // 13) 刷新主机信息：POST 恰好 1 次，成功后展示新主机名
    const hostCard = page.locator('section[aria-label="主机信息"]')
    state.hostCalls = 0
    await hostCard.locator('header button.ls-btn').click()
    await page.waitForFunction(
      (hostname) =>
        (document.querySelector('section[aria-label="主机信息"]')?.textContent || '').includes(hostname),
      NEW_HOSTNAME,
      { timeout: 10000 },
    )
    check('刷新主机信息 POST 恰好 1 次', state.hostCalls === 1, `${state.hostCalls} 次`)
    check('成功后展示新主机名', (await hostCard.innerText()).includes(NEW_HOSTNAME))
    // 真值断言：读「主机名」这一行的 dd，必须等于后端 host_info.hostname，而不是空/占位。
    // 后端返回 {data:{host_info:{...}}}，前端若漏解包这里会显示「—」或空态文案。
    const refreshedHostname = await page.evaluate(() => {
      const section = document.querySelector('section[aria-label="主机信息"]')
      const rows = Array.from(section?.querySelectorAll('dl.ls-kv > div') ?? [])
      const row = rows.find((item) => (item.querySelector('dt')?.textContent || '').includes('主机名'))
      return row?.querySelector('dd')?.textContent?.trim() ?? ''
    })
    check(
      '刷新后主机名等于后端 host_info.hostname（非空、非 —）',
      refreshedHostname === NEW_HOSTNAME && refreshedHostname !== '—',
      JSON.stringify({ refreshedHostname, expected: NEW_HOSTNAME }),
    )
    check(
      '刷新后不再显示主机信息空态',
      !(await hostCard.innerText()).includes('尚未采集'),
    )
    check('成功后有「主机信息已更新」提示', (await hostCard.innerText()).includes('主机信息已更新'))

    // 14) agent 升级：sha256 前端校验 + POST 体三字段 + ok=false 原样 error
    const upgradeCard = page.locator('section[aria-label="升级 Agent"]')
    check('升级卡存在', (await upgradeCard.count()) === 1)
    const upgradeSubmit = upgradeCard.locator('button[type="submit"]')
    const AGENT_VERSION = '0.9.9'
    const AGENT_URL = 'https://dl.example.com/leadstack-agent_0.9.9_linux_amd64.tar.gz'
    const AGENT_SHA = 'a1b2c3d4e5f60718'.repeat(4)
    state.upgradeMode = 'ok'
    state.upgradeCalls = 0
    state.upgradeBodies = []
    await page.fill('#server-agent-upgrade-version', AGENT_VERSION)
    await page.fill('#server-agent-upgrade-url', AGENT_URL)
    await page.fill('#server-agent-upgrade-sha256', 'abc')
    await upgradeSubmit.click()
    await page.waitForTimeout(500)
    check('sha256 非法时不发请求', state.upgradeCalls === 0, `${state.upgradeCalls} 次`)
    check('sha256 非法时显示格式错误', (await upgradeCard.innerText()).includes('64 位十六进制'))
    check('sha256 非法时没有结果块', (await page.locator('.ls-upgrade__result').count()) === 0)

    await page.fill('#server-agent-upgrade-sha256', AGENT_SHA)
    const [upgradeResponse] = await Promise.all([
      page.waitForResponse(
        (response) => response.request().method() === 'POST' && response.url().includes('/agent-upgrade'),
        { timeout: 10000 },
      ),
      upgradeSubmit.click(),
    ])
    await page.waitForFunction(
      () => (document.querySelector('.ls-upgrade__result')?.textContent || '').includes('升级指令已排队'),
      null,
      { timeout: 10000 },
    )
    check('合法 sha256 只发一次 POST', state.upgradeCalls === 1, `${state.upgradeCalls} 次`)
    check('升级接口回 202', upgradeResponse.status() === 202, String(upgradeResponse.status()))
    const upgradeBody = state.upgradeBodies[state.upgradeBodies.length - 1] || {}
    check(
      'POST 体恰好 version/url/sha256 三字段',
      Object.keys(upgradeBody).sort().join(',') === 'sha256,url,version',
      Object.keys(upgradeBody).join(','),
    )
    check(
      'POST 体字段值与表单一致',
      upgradeBody.version === AGENT_VERSION &&
        upgradeBody.url === AGENT_URL &&
        upgradeBody.sha256 === AGENT_SHA,
      JSON.stringify(upgradeBody).slice(0, 120),
    )
    check('升级成功显示排队提示', (await upgradeCard.innerText()).includes('升级指令已排队（HTTP 202）'))

    state.upgradeMode = 'fail'
    await upgradeSubmit.click()
    await page.waitForFunction(
      (message) => (document.querySelector('.ls-upgrade__result')?.textContent || '').includes(message),
      UPGRADE_ERROR,
      { timeout: 10000 },
    )
    check('升级 ok=false 原样显示 error', (await upgradeCard.innerText()).includes(UPGRADE_ERROR))


    // 8) 未知服务器 id → 404 提示而不是白屏
    await page.goto(`${base}/#/servers/does-not-exist`, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(2000)
    const notFoundText = await page.locator('body').innerText()
    log('debug-404', `${page.url()} | ${notFoundText.replace(/\s+/g, ' ').slice(0, 160)}`)
    check(
      '不存在的服务器给可读 404 文案',
      (await page.locator('.ls-notice').count()) >= 1 && notFoundText.includes('不存在'),
      notFoundText.replace(/\s+/g, ' ').slice(0, 80),
    )

    // 13) 只读（仅 server.read）：指标仍可见，写操作按钮全隐且零请求
    const readonlyState = {
      server: structuredClone(SERVER),
      permissions: ['server.read'],
      detailCalls: 0,
      accessCalls: 0,
      panelCalls: 0,
      loginCalls: 0,
      checkCalls: 0,
      ticketCalls: 0,
      filesCalls: 0,
      logsCalls: 0,
      patchBodies: [],
      metricsCalls: [],
      metricsMode: 'ok',
      connectionCalls: 0,
      connectionMode: 'ok',
      hostCalls: 0,
      upgradeCalls: 0,
      upgradeMode: 'ok',
      upgradeBodies: [],
    }
    const readonlyContext = await browser.newContext({ viewport: { width: 320, height: 800 } })
    await installMockApi(readonlyContext, readonlyState)
    const readonlyPage = await readonlyContext.newPage()
    const readonlyErrors = []
    readonlyPage.on('pageerror', (error) => readonlyErrors.push(error.message))
    try {
      await readonlyPage.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
      await readonlyPage.waitForSelector('#login-username', { timeout: 15000 })
      await readonlyPage.fill('#login-username', 'admin')
      await readonlyPage.fill('#login-password', PASSWORD)
      await readonlyPage.click('#login-submit')
      await readonlyPage.waitForSelector('.ls-shell', { timeout: 15000 })
      await readonlyPage.goto(`${base}/#/servers/srv-alpha`, { waitUntil: 'domcontentloaded' })
      await readonlyPage.waitForSelector('.ls-detail__title', { timeout: 15000 })
      await readonlyPage.click('#server-tab-checks')
      await readonlyPage.waitForSelector('.ls-metrics__grid', { timeout: 15000 })
      check(
        '只读用户仍能看到指标曲线',
        (await readonlyPage.locator('.ls-metrics__chart path.ls-metrics__line').count()) === 2,
      )
      check('只读用户自动拉指标', readonlyState.metricsCalls.length === 1, `${readonlyState.metricsCalls.length} 次`)
      await readonlyPage.click('#server-tab-overview')
      await readonlyPage.waitForSelector('section[aria-label="测试连接"]', { timeout: 5000 })
      check(
        '只读用户看不到刷新主机信息按钮',
        (await readonlyPage.locator('section[aria-label="主机信息"] button').count()) === 0,
      )
      check(
        '只读用户看不到测试连接按钮',
        (await readonlyPage.locator('section[aria-label="测试连接"] button').count()) === 0,
      )
      check('只读用户看不到 agent 升级卡', (await readonlyPage.locator('section[aria-label="升级 Agent"]').count()) === 0)
      await readonlyPage.click('#server-tab-login')
      await readonlyPage.waitForSelector('section[aria-label="登录脚本"]', { timeout: 5000 })
      check(
        '只读用户看不到登录脚本保存按钮',
        (await readonlyPage.locator('#login-script-save').count()) === 0,
      )
      check(
        '只读用户看不到登录脚本清空按钮',
        (await readonlyPage.locator('#login-script-clear').count()) === 0,
      )
      check(
        '只读用户看不到登录脚本示例按钮',
        (await readonlyPage.locator('#login-script-sample-baota').count()) === 0 &&
          (await readonlyPage.locator('#login-script-sample-onepanel').count()) === 0,
      )
      check('只读用户登录脚本编辑器禁用', await readonlyPage.locator('#login-script-editor').isDisabled())
      check(
        '只读用户零 PATCH（登录脚本保存/清空按钮 count=0）',
        readonlyState.patchBodies.length === 0,
        `${readonlyState.patchBodies.length} 次`,
      )
      check(
        '只读用户零写请求',
        readonlyState.hostCalls === 0 && readonlyState.connectionCalls === 0 && readonlyState.upgradeCalls === 0,
        `${readonlyState.hostCalls}/${readonlyState.connectionCalls}/${readonlyState.upgradeCalls}`,
      )
      check('只读页面无未捕获异常', readonlyErrors.length === 0, readonlyErrors.join(' | ').slice(0, 120))
    } finally {
      await readonlyContext.close().catch(() => {})
    }

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.join(' | ').slice(0, 120))
  } catch (error) {
    console.error(`✗ 冒烟中断：${error && error.message ? error.message.split('\n')[0] : error}`)
    for (const item of results.filter((entry) => !entry.ok)) console.error(`  已失败：${item.name}`)
    throw error
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log('')
  if (failed.length > 0) {
    console.error(`✗ 服务器详情页冒烟失败 ${failed.length} 项：`)
    for (const item of failed) console.error(`  - ${item.name}${item.detail ? `（${item.detail}）` : ''}`)
    process.exitCode = 1
    return
  }
  console.log(`✓ 服务器详情页冒烟全部通过（共 ${results.length} 项，失败 0 项）`)
}

run().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
