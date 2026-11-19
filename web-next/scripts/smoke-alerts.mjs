#!/usr/bin/env node
/**
 * 告警列表页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表渲染（含规则级告警）/ 状态·级别·服务器筛选 / 分页 / 详情抽屉 / 行内确认
 * （成功变 acked（不代表恢复）、resolved 由假数据单独覆盖、失败带 requestId）/
 * 无 alert.read 时 0 请求并落到 /forbidden /
 * 320px 无横向滚动。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-alerts.mjs [--headed]
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

const TOKEN = '{{SECRET_jf1n8w2r}}'
const PASSWORD = '{{SECRET_obr7o0o4}}'
const NOW = Date.now()

function user(permissions) {
  return {
    id: 'smoke-alerts-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

/** 单条告警的默认字段：后端并行开发中字段可能缺失，前端必须扛得住。 */
function alertFixture(overrides) {
  return {
    id: 'a1',
    rule_id: 'rule-cpu',
    dedup_key: 'dedup-cpu-alpha',
    server_id: 'srv-alpha',
    check_id: 'chk-cpu',
    status: 'firing',
    severity: 'critical',
    title: 'CPU 使用率过高',
    message: 'cpu.pct = 96.4，持续 5 分钟',
    value: 96.4,
    started_at: NOW - 5 * 60_000,
    updated_at: NOW - 60_000,
    resolved_at: null,
    acked_at: null,
    acked_by: null,
    notify_count: 2,
    last_notify_at: NOW - 4 * 60_000,
    silence_until: null,
    rule_name: 'CPU 使用率过高',
    server_name: 'alpha-node',
    check_name: 'cpu.check',
    events: [],
    ...overrides,
  }
}

/**
 * 23 条：a1 是首个可确认的严重告警，a2 已恢复，a3 是规则级（server_id 为空），
 * a4 用于「确认失败」分支，其余补足分页（20 条/页 → 2 页）。
 */
function buildAlerts() {
  const alerts = [
    alertFixture({
      id: 'a1',
      events: [
        { id: 1, kind: 'firing', ts: NOW - 5 * 60_000, detail: 'cpu.pct=96.4 超过阈值 90' },
        { id: 2, kind: 'notified', ts: NOW - 4 * 60_000, detail: '已推送到默认渠道' },
      ],
    }),
    alertFixture({
      id: 'a2',
      rule_id: 'rule-disk',
      rule_name: '磁盘空间不足',
      server_id: 'srv-beta',
      server_name: 'beta-node',
      check_id: 'chk-disk',
      status: 'resolved',
      severity: 'warning',
      title: '磁盘空间不足',
      message: 'disk.used.pct = 91',
      value: 91,
      started_at: NOW - 2 * 3_600_000,
      updated_at: NOW - 3_600_000,
      resolved_at: NOW - 3_600_000,
      events: [
        { id: 3, kind: 'firing', ts: NOW - 2 * 3_600_000, detail: 'disk.used.pct=91' },
        { id: 4, kind: 'resolved', ts: NOW - 3_600_000, detail: 'disk.used.pct=72' },
      ],
    }),
    // 规则级告警：没有服务器维度，界面必须显示「规则级」而不是空白/报错
    alertFixture({
      id: 'a3',
      rule_id: 'rule-disk-readonly',
      rule_name: '全局规则：磁盘只读',
      server_id: null,
      server_name: null,
      check_id: null,
      check_name: null,
      status: 'firing',
      severity: 'warning',
      title: '磁盘进入只读模式',
      message: 'mount / 只读',
      value: null,
      notify_count: 1,
      events: [{ id: 5, kind: 'firing', ts: NOW - 10 * 60_000, detail: '检测到只读挂载' }],
    }),
    alertFixture({
      id: 'a4',
      rule_id: 'rule-heartbeat',
      rule_name: '心跳缺失',
      status: 'firing',
      severity: 'info',
      title: '心跳缺失',
      message: 'agent 心跳超时',
      value: null,
      started_at: NOW - 15 * 60_000,
      events: [],
    }),
  ]
  for (let index = 5; index <= 23; index += 1) {
    alerts.push(
      alertFixture({
        id: `a${index}`,
        rule_id: `rule-bulk-${index}`,
        dedup_key: `dedup-bulk-${index}`,
        rule_name: `批量告警 ${index}`,
        title: `批量告警 ${index}`,
        status: index % 5 === 0 ? 'resolved' : 'firing',
        severity: 'info',
        started_at: NOW - index * 60_000,
      }),
    )
  }
  return alerts
}

/**
 * 规则 mock：覆盖 check（两项检测）与 metric 两种来源，行内按钮齐全。
 * 字段名严格按 api.ts / handlers_alerts.go，不凭猜。
 */
function alertRuleFixture(overrides) {
  return {
    id: 'rule-cpu',
    name: 'CPU 持续高位',
    enabled: true,
    source: 'check',
    check_ids: ['chk-cpu', 'chk-load'],
    server_ids: [],
    groups: [],
    tags: [],
    metric: null,
    comparator: null,
    threshold: null,
    for_duration_s: 300,
    severity: 'warning',
    dedup_window_s: 300,
    channel_ids: ['ch-ok'],
    silence_until: null,
    created_at: NOW - 86_400_000,
    ...overrides,
  }
}

function buildAlertRules() {
  return [
    alertRuleFixture(),
    alertRuleFixture({
      id: 'rule-disk',
      name: '磁盘空间不足',
      enabled: false,
      source: 'metric',
      check_ids: [],
      metric: 'disk.used.pct',
      comparator: '>',
      threshold: 90,
      for_duration_s: 0,
      severity: 'critical',
      dedup_window_s: 0,
      channel_ids: [],
      created_at: NOW - 172_800_000,
    }),
  ]
}

/** 渠道 mock：config 一律是后端脱敏后的形态（含 ***），用来验证前端不回显。 */
function alertChannelFixture(overrides) {
  return {
    id: 'ch-ok',
    name: '运维 Webhook',
    kind: 'webhook',
    enabled: true,
    config: { url: 'https://hooks.example.com/services/T000/***' },
    last_error: null,
    last_ok_at: NOW - 600_000,
    created_at: NOW - 2 * 86_400_000,
    ...overrides,
  }
}

function buildAlertChannels() {
  return [
    alertChannelFixture(),
    alertChannelFixture({
      id: 'ch-bad',
      name: 'Telegram 值班群',
      kind: 'telegram',
      config: { chat_id: '-1001234567', bot_token: '***' },
      last_error: '401 Unauthorized: bot token invalid',
      last_ok_at: null,
    }),
  ]
}

function buildChecks() {
  return [
    { id: 'chk-cpu', name: 'CPU 使用率', kind: 'metric', target: 'cpu.pct', interval_s: 60, timeout_s: 5, server_ids: [], enabled: true },
    { id: 'chk-load', name: '系统负载', kind: 'metric', target: 'load1', interval_s: 60, timeout_s: 5, server_ids: [], enabled: true },
    { id: 'chk-disk', name: '磁盘使用率', kind: 'metric', target: 'disk.used.pct', interval_s: 60, timeout_s: 5, server_ids: [], enabled: true },
  ]
}

/** 服务器 mock：metrics_summary 必须非空，否则 api.servers() 会补拉详情（mock 未覆盖）。 */
function buildServers() {
  return [
    {
      id: 'srv-alpha',
      name: 'alpha-node',
      host: '10.0.0.11',
      port: 9700,
      mode: 'agent',
      status: 'online',
      tags: [],
      metrics_summary: { cpu_pct: 12, mem_used_pct: 40, disk_used_pct: 55, load1: 0.4, uptime_s: 86400 },
    },
  ]
}

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
  return {
    error: ['找不到 playwright —— 这是环境缺失，不是断言失败。', ...errors.map((line) => `  尝试过 ${line}`)].join(
      '\n',
    ),
  }
}

async function resolvePlaywright() {
  const loaded = loadPlaywright()
  if (loaded && loaded.error) throw new Error(loaded.error)
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

/**
 * 假后端：只覆盖告警页需要的端点，其他一律 404，避免静默放过。
 *
 * a4 的确认请求固定 500 并带 request_id，用来验证失败文案会带上请求编号。
 */
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

    if (path === '/api/v1/alerts' && method === 'GET') {
      state.listCalls += 1
      return json(200, { data: state.alerts })
    }

    const ack = /^\/api\/v1\/alerts\/([^/]+)\/ack$/.exec(path)
    if (ack && method === 'POST') {
      const id = decodeURIComponent(ack[1])
      state.ackCalls.push(id)
      const found = state.alerts.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '告警不存在' } })
      if (id === 'a4') {
        return json(500, {
          error: { code: 'internal', message: '确认失败：后端暂时不可用', request_id: 'req-smoke-ack-1' },
        })
      }
      // 与后端 store.AckAlert 一致：确认只把状态置 acked（不代表恢复），
      // resolved 只由 ResolveAlert 产生；mock 不得自己发明「确认即恢复」。
      if (found.status === 'resolved') {
        return json(409, { error: { code: 'conflict', message: '告警已恢复，无需确认' } })
      }
      found.status = 'acked'
      found.acked_at = Date.now()
      found.acked_by = 'admin'
      found.updated_at = Date.now()
      found.events = [
        ...(found.events ?? []),
        { id: 99, kind: 'acked', ts: Date.now(), detail: '由 admin 确认' },
      ]
      return json(200, { data: found })
    }

    const detail = /^\/api\/v1\/alerts\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      const found = state.alerts.find((item) => item.id === decodeURIComponent(detail[1]))
      return found ? json(200, { data: found }) : json(404, { error: { code: 'not_found' } })
    }

    /* ---------------- 告警规则 / 通知渠道（h2 起） ---------------- */
    // 任何 /alert-rules* 与 /alert-channels* 的非 GET 请求都记账，
    // 「缺 alert.write 时零写请求」直接用它做断言。
    const isAlertWrite =
      method !== 'GET' &&
      (path.startsWith('/api/v1/alert-rules') || path.startsWith('/api/v1/alert-channels'))
    if (isAlertWrite) state.writeCalls.push(`${method} ${path}`)

    if (path === '/api/v1/checks' && method === 'GET') return json(200, { data: state.checks })
    if (path === '/api/v1/servers' && method === 'GET') return json(200, { data: state.servers })

    if (path === '/api/v1/alert-rules' && method === 'GET') return json(200, { data: state.rules })
    if (path === '/api/v1/alert-rules' && method === 'POST') {
      const body = request.postDataJSON()
      state.ruleCreateCalls.push(body)
      const created = {
        ...body,
        id: `rule-new-${state.ruleCreateCalls.length}`,
        silence_until: null,
        created_at: Date.now(),
      }
      state.rules.unshift(created)
      return json(201, { data: created })
    }

    const ruleSilence = /^\/api\/v1\/alert-rules\/([^/]+)\/silence$/.exec(path)
    if (ruleSilence && method === 'POST') {
      const id = decodeURIComponent(ruleSilence[1])
      const body = request.postDataJSON()
      state.silenceCalls.push({ id, body })
      const found = state.rules.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '规则不存在' } })
      // 与后端一致：duration_s 秒后解除，0 表示取消静默
      found.silence_until = body.duration_s > 0 ? Date.now() + body.duration_s * 1000 : null
      return json(200, { data: found })
    }

    const ruleDetail = /^\/api\/v1\/alert-rules\/([^/]+)$/.exec(path)
    if (ruleDetail && method === 'PATCH') {
      const id = decodeURIComponent(ruleDetail[1])
      const body = request.postDataJSON()
      state.rulePatchCalls.push({ id, body })
      const found = state.rules.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '规则不存在' } })
      Object.assign(found, body)
      return json(200, { data: found })
    }
    if (ruleDetail && method === 'DELETE') {
      const id = decodeURIComponent(ruleDetail[1])
      state.ruleDeleteCalls.push(id)
      state.rules = state.rules.filter((item) => item.id !== id)
      return route.fulfill({ status: 204, body: '' })
    }

    if (path === '/api/v1/alert-channels' && method === 'GET') return json(200, { data: state.channels })
    if (path === '/api/v1/alert-channels' && method === 'POST') {
      const body = request.postDataJSON()
      state.channelCreateCalls.push(body)
      const created = {
        ...body,
        id: `ch-new-${state.channelCreateCalls.length}`,
        last_error: null,
        last_ok_at: null,
        created_at: Date.now(),
      }
      state.channels.unshift(created)
      return json(201, { data: created })
    }

    const channelTest = /^\/api\/v1\/alert-channels\/([^/]+)\/test$/.exec(path)
    if (channelTest && method === 'POST') {
      const id = decodeURIComponent(channelTest[1])
      state.channelTestCalls.push(id)
      const found = state.channels.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '渠道不存在' } })
      // ch-bad 固定失败：前端必须原样显示后端 error，不能自己编文案
      if (id === 'ch-bad') {
        return json(200, { data: { ok: false, error: '401 Unauthorized: bot token invalid' } })
      }
      return json(200, { data: { ok: true } })
    }

    const channelDetail = /^\/api\/v1\/alert-channels\/([^/]+)$/.exec(path)
    if (channelDetail && method === 'PATCH') {
      const id = decodeURIComponent(channelDetail[1])
      const body = request.postDataJSON()
      state.channelPatchCalls.push({ id, body })
      const found = state.channels.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '渠道不存在' } })
      Object.assign(found, body)
      return json(200, { data: found })
    }
    if (channelDetail && method === 'DELETE') {
      const id = decodeURIComponent(channelDetail[1])
      state.channelDeleteCalls.push(id)
      state.channels = state.channels.filter((item) => item.id !== id)
      return route.fulfill({ status: 204, body: '' })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

async function login(page, base) {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 15000 })
  await page.fill('#login-username', 'admin')
  await page.fill('#login-password', PASSWORD)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 15000 })
}

const rows = (page) => page.locator('.ls-alertspage__row')
const rowById = (page, id) => page.locator(`.ls-alertspage__row[data-alert-id="${id}"]`)

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

  const pageErrors = []
  try {
    /* ------------------------------------------------------------------ */
    /* 场景 1：有 alert.read + alert.write                                 */
    /* ------------------------------------------------------------------ */
    const state = {
      permissions: ['server.read', 'alert.read', 'alert.write'],
      alerts: buildAlerts(),
      listCalls: 0,
      ackCalls: [],
      writeCalls: [],
    }
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))

    await login(page, base)
    await page.goto(`${base}/#/alerts`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-alertspage__title', { timeout: 15000 })
    await page.waitForSelector('.ls-alertspage__row', { timeout: 15000 })

    check('登录后进入告警真实页面（不是占位页）', (await page.locator('.ls-alertspage__title').innerText()).trim() === '告警')
    check('告警列表接口只拉一次', state.listCalls === 1, `${state.listCalls} 次`)
    check('首屏按每页 20 条分页渲染', (await rows(page).count()) === 20, `${await rows(page).count()} 行`)
    await shot(page, 'alerts-1280')
    check(
      '计数文案给出总数',
      (await page.locator('#alerts-count').innerText()).includes('23'),
      (await page.locator('#alerts-count').innerText()).replace(/\s+/g, ' '),
    )

    // 规则级告警：server_id 为空不能显示空白或报错
    const ruleRow = rowById(page, 'a3')
    check(
      '规则级告警显示「规则级」且不空白',
      (await ruleRow.locator('.ls-alertspage__server').innerText()).trim() === '规则级' &&
        (await ruleRow.locator('.ls-alertspage__server--rule').count()) === 1,
      (await ruleRow.locator('.ls-alertspage__server').innerText()).trim(),
    )

    // 级别/状态徽标
    const firstRow = rowById(page, 'a1')
    const badgeTexts = (await firstRow.locator('.ls-alertspage__badge').allInnerTexts()).map((text) => text.trim())
    check('级别与状态徽标文案正确', badgeTexts[0] === '严重' && badgeTexts[1] === '触发中', badgeTexts.join(' / '))

    // 时间：本地时区绝对时间 + 相对时间
    const timeText = (await firstRow.locator('.ls-alertspage__time').innerText()).trim()
    const relativeText = (await firstRow.locator('.ls-alertspage__relative').innerText()).trim()
    check(
      '开始时间为本地时区并带相对时间',
      /^\d{4}\/\d{1,2}\/\d{1,2} \d{1,2}:\d{2}:\d{2}$/.test(timeText) && /前|刚刚/.test(relativeText),
      `${timeText} · ${relativeText}`,
    )

    // 分页
    check('首页分页信息为 1 / 2', (await page.locator('#alerts-pager-info').innerText()).replace(/\s+/g, '') === '第1/2页')
    log('step', '分页：下一页')
    await page.click('#alerts-next-page')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 3)
    check('下一页显示剩余 3 条并包含 a23', (await rows(page).count()) === 3 && (await rowById(page, 'a23').count()) === 1)
    log('step', '分页：上一页')
    await page.click('#alerts-prev-page')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 20)

    // 筛选：状态 / 级别 / 服务器
    log('step', '筛选：状态 resolved')
    await page.selectOption('#alerts-filter-status', 'resolved')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 5)
    check(
      '状态筛选 resolved 只剩已恢复告警',
      (await rowById(page, 'a2').count()) === 1 &&
        (await rowById(page, 'a1').count()) === 0 &&
        (await page.locator('.ls-alertspage__row[data-alert-status="resolved"]').count()) === 5,
      `${await rows(page).count()} 行`,
    )
    check('有筛选时出现清除入口', (await page.locator('#alerts-filter-reset').count()) === 1)
    await page.click('#alerts-filter-reset')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 20)

    log('step', '筛选：级别 critical')
    await page.selectOption('#alerts-filter-severity', 'critical')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 1)
    check('级别筛选 critical 只剩 a1', (await rowById(page, 'a1').count()) === 1)
    await page.click('#alerts-filter-reset')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 20)

    log('step', '筛选：服务器 rule')
    await page.selectOption('#alerts-filter-server', 'rule')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 1)
    check('服务器筛选「规则级」只剩 a3', (await rowById(page, 'a3').count()) === 1)
    await page.click('#alerts-filter-reset')
    await page.waitForFunction(() => document.querySelectorAll('.ls-alertspage__row').length === 20)

    // 详情抽屉：规则信息 + 最近事件
    log('step', '打开详情抽屉')
    await page.click('#alert-view-a1')
    await page.waitForSelector('[data-testid="alert-drawer"]', { timeout: 10000 })
    await page.waitForSelector('.ls-alertspage-drawer__event', { timeout: 10000 })
    const drawerText = (await page.locator('.ls-alertspage-drawer__panel').innerText()).replace(/\s+/g, ' ')
    check(
      '详情抽屉展示规则名、服务器与 2 条最近事件',
      drawerText.includes('CPU 使用率过高') &&
        drawerText.includes('alpha-node') &&
        (await page.locator('.ls-alertspage-drawer__event').count()) === 2,
    )
    check('抽屉事件类型走文案映射', (await page.locator('.ls-alertspage-drawer__event-kind').first().innerText()).trim() === '触发')
    await page.keyboard.press('Escape')
    await page.waitForSelector('[data-testid="alert-drawer"]', { state: 'detached', timeout: 5000 })
    const focusId = await page.evaluate(() => document.activeElement?.id ?? '')
    check('Esc 关闭抽屉且焦点回到触发按钮', focusId === 'alert-view-a1', focusId)

    // 已恢复路径：resolved 由后端 ResolveAlert 产生，不能靠确认推出来。
    // 用假数据 a2（status: 'resolved'）单独覆盖徽标与文案。
    log('step', '校验已恢复告警展示')
    const resolvedRow = rowById(page, 'a2')
    const resolvedBadge = (await resolvedRow.locator('.ls-alertspage__badge').nth(1).innerText()).trim()
    const resolvedAck = resolvedRow.locator('#alert-ack-a2')
    const resolvedAckText = (await resolvedAck.innerText()).trim()
    check(
      '已恢复告警展示「已恢复」徽标且不可确认',
      (await resolvedRow.getAttribute('data-alert-status')) === 'resolved' &&
        resolvedBadge === '已恢复' &&
        (await resolvedAck.isDisabled()) &&
        resolvedAckText === '已恢复',
      resolvedBadge + ' / ' + resolvedAckText,
    )

    // 行内确认：成功 → 响应把该行置为 acked（不是 resolved）
    log('step', '确认 a1')
    await page.click('#alert-ack-a1')
    await page.waitForFunction(
      () => document.querySelector('.ls-alertspage__row[data-alert-id="a1"]')?.getAttribute('data-alert-status') === 'acked',
      undefined,
      { timeout: 10000 },
    )
    const ackedRow = rowById(page, 'a1')
    const ackedBadge = (await ackedRow.locator('.ls-alertspage__badge').nth(1).innerText()).trim()
    const ackedButton = ackedRow.locator('#alert-ack-a1')
    const ackedButtonText = (await ackedButton.innerText()).trim()
    check(
      '确认后该行状态变 acked 且徽标为已确认',
      (await ackedRow.getAttribute('data-alert-status')) === 'acked' && ackedBadge === '已确认',
      ackedBadge,
    )
    check('确认请求打到 /alerts/a1/ack', state.ackCalls.includes('a1'), state.ackCalls.join(','))
    check(
      '已确认的行不再显示确认按钮（按钮变为已确认文案且禁用）',
      (await ackedButton.isDisabled()) && ackedButtonText === '已确认',
      ackedButtonText,
    )

    // 行内确认失败：错误文案带 requestId，列表不被清空
    log('step', '确认 a4（失败分支）')
    await page.click('#alert-ack-a4')
    await page.waitForSelector('#alerts-action-error', { timeout: 10000 })
    const actionError = (await page.locator('#alerts-action-error').innerText()).replace(/\s+/g, ' ')
    check(
      '确认失败展示 errorMessage 且带 requestId',
      actionError.includes('req-smoke-ack-1') && actionError.includes('确认失败'),
      actionError.slice(0, 80),
    )

    // 320px：不横向滚动，确认按钮仍可见可点
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(250)
    const narrow = await page.evaluate(() => {
      const button = document.querySelector('#alert-ack-a4')
      const rect = button ? button.getBoundingClientRect() : null
      return {
        overflow: document.documentElement.scrollWidth - window.innerWidth,
        buttonVisible: Boolean(rect && rect.width > 0 && rect.right <= window.innerWidth + 1),
      }
    })
    check('320px 无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 行内确认按钮可见可点', narrow.buttonVisible)
    await shot(page, 'alerts-320')

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
    await context.close()

    /* ------------------------------------------------------------------ */
    /* 场景 2：规则 tab —— 深链 / 新建 / 编辑 / 静默 / 删除 / 320px        */
    /* ------------------------------------------------------------------ */
    const ruleState = {
      permissions: ['server.read', 'check.read', 'alert.read', 'alert.write'],
      alerts: buildAlerts(),
      listCalls: 0,
      ackCalls: [],
      rules: buildAlertRules(),
      channels: buildAlertChannels(),
      checks: buildChecks(),
      servers: buildServers(),
      ruleCreateCalls: [],
      rulePatchCalls: [],
      ruleDeleteCalls: [],
      silenceCalls: [],
      channelCreateCalls: [],
      channelPatchCalls: [],
      channelDeleteCalls: [],
      channelTestCalls: [],
      writeCalls: [],
    }
    const adminContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(adminContext, ruleState)
    const adminPage = await adminContext.newPage()
    adminPage.on('pageerror', (error) => pageErrors.push(`rules: ${error.message}`))

    await login(adminPage, base)
    log('step', '深链 #/alerts?tab=rules')
    await adminPage.goto(`${base}/#/alerts?tab=rules`, { waitUntil: 'domcontentloaded' })
    await adminPage.waitForSelector('#alerts-panel-rules .ls-alerts__panel-title', { timeout: 15000 })
    await adminPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 15000 },
    )
    const ruleRows = adminPage.locator('#alerts-panel-rules .ls-alertspage__row')
    check(
      '深链 ?tab=rules 直达规则表',
      (await ruleRows.count()) === 2 &&
        (await adminPage.locator('#alerts-tab-rules').getAttribute('aria-selected')) === 'true' &&
        /2 条规则/.test(await adminPage.locator('#alerts-panel-rules .ls-alerts__count').innerText()),
      `${await ruleRows.count()} 行 / ${(await adminPage.locator('#alerts-panel-rules .ls-alerts__count').innerText()).trim()}`,
    )

    const historyBefore = await adminPage.evaluate(() => window.history.length)
    await adminPage.click('#alerts-tab-events')
    await adminPage.waitForSelector('#alerts-count', { timeout: 10000 })
    await adminPage.click('#alerts-tab-channels')
    await adminPage.waitForSelector('#alerts-panel-channels .ls-alerts__panel-title', { timeout: 10000 })
    await adminPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-channels .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 10000 },
    )
    await adminPage.click('#alerts-tab-rules')
    await adminPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 10000 },
    )
    const historyAfter = await adminPage.evaluate(() => window.history.length)
    check(
      'tab 切换用 replace 不新增历史',
      historyAfter === historyBefore && new URL(adminPage.url()).hash.includes('tab=rules'),
      `history ${historyBefore} → ${historyAfter} / ${new URL(adminPage.url()).hash}`,
    )

    // ---- 新建 check 规则：两项检测 + warning + 关联一个渠道 ----
    log('step', '新建 check 规则')
    await adminPage.click('#alerts-rule-create')
    await adminPage.waitForSelector('#alerts-rule-form', { timeout: 10000 })
    await adminPage.waitForFunction(
      () => document.querySelectorAll('#alerts-rule-form fieldset')[0]?.querySelectorAll('input[type="checkbox"]').length === 3,
      undefined,
      { timeout: 10000 },
    )
    await adminPage.fill('#alerts-rule-name', '磁盘增长过快')
    await adminPage.selectOption('#alerts-rule-severity', 'warning')
    await adminPage.fill('#alerts-rule-duration', '120')
    const checkBoxes = adminPage.locator('#alerts-rule-form fieldset').nth(0).locator('input[type="checkbox"]')
    await checkBoxes.nth(0).check()
    await checkBoxes.nth(1).check()
    await adminPage
      .locator('#alerts-rule-form fieldset')
      .nth(1)
      .locator('input[type="checkbox"]')
      .nth(0)
      .check()
    await adminPage.click('#alerts-rule-save')
    await adminPage.waitForFunction(
      () =>
        [...document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row')].some((row) =>
          row.textContent.includes('磁盘增长过快'),
        ),
      undefined,
      { timeout: 10000 },
    )
    const expectedRuleCreate = {
      name: '磁盘增长过快',
      enabled: true,
      source: 'check',
      for_duration_s: 120,
      severity: 'warning',
      dedup_window_s: 300,
      channel_ids: ['ch-ok'],
      check_ids: ['chk-cpu', 'chk-load'],
      server_ids: [],
    }
    check(
      '新建规则 POST /alert-rules 恰 1 次且请求体与表单一致',
      ruleState.ruleCreateCalls.length === 1 &&
        JSON.stringify(ruleState.ruleCreateCalls[0]) === JSON.stringify(expectedRuleCreate),
      JSON.stringify(ruleState.ruleCreateCalls[0]),
    )
    check(
      '新建成功后规则表出现新行',
      (await adminPage
        .locator('#alerts-panel-rules .ls-alertspage__row')
        .filter({ hasText: '磁盘增长过快' })
        .count()) === 1,
    )

    // ---- 编辑：只发 PATCH，体只含变更字段 ----
    log('step', '编辑规则：只改名称')
    await adminPage.locator('.ls-alertspage__row[data-rule-id="rule-cpu"]').locator('#alerts-rule-edit-rule-cpu').click()
    await adminPage.waitForSelector('#alerts-rule-form', { timeout: 10000 })
    await adminPage.waitForFunction(() => document.querySelector('#alerts-rule-name')?.value === 'CPU 持续高位')
    await adminPage.fill('#alerts-rule-name', 'CPU 持续高位（已调整）')
    await adminPage.click('#alerts-rule-save')
    await adminPage.waitForFunction(
      () =>
        [...document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row')].some((row) =>
          row.textContent.includes('CPU 持续高位（已调整）'),
        ),
      undefined,
      { timeout: 10000 },
    )
    const patchCall = ruleState.rulePatchCalls[0]
    check(
      '编辑只发 1 次 PATCH 且体只含变更字段 name',
      ruleState.rulePatchCalls.length === 1 &&
        patchCall.id === 'rule-cpu' &&
        JSON.stringify(patchCall.body) === JSON.stringify({ name: 'CPU 持续高位（已调整）' }),
      JSON.stringify(patchCall?.body),
    )
    check(
      '编辑未触发 POST / DELETE / 静默请求',
      ruleState.ruleCreateCalls.length === 1 &&
        ruleState.ruleDeleteCalls.length === 0 &&
        ruleState.silenceCalls.length === 0,
    )

    // ---- 删除：二次确认 ----
    log('step', '删除规则：先取消再确认')
    await adminPage.click('#alerts-rule-delete-rule-disk')
    await adminPage.waitForSelector('#alerts-delete-confirm', { timeout: 10000 })
    await adminPage.click('#alerts-delete-cancel')
    await adminPage.waitForSelector('#alerts-delete-confirm', { state: 'detached', timeout: 10000 })
    check(
      '规则删除二次确认：取消后 0 次 DELETE 且行还在',
      ruleState.ruleDeleteCalls.length === 0 &&
        (await adminPage.locator('.ls-alertspage__row[data-rule-id="rule-disk"]').count()) === 1,
      `${ruleState.ruleDeleteCalls.length} 次 DELETE`,
    )
    await adminPage.click('#alerts-rule-delete-rule-disk')
    await adminPage.waitForSelector('#alerts-delete-confirm', { timeout: 10000 })
    await adminPage.click('#alerts-delete-confirm')
    await adminPage.waitForSelector('.ls-alertspage__row[data-rule-id="rule-disk"]', {
      state: 'detached',
      timeout: 10000,
    })
    check(
      '规则删除二次确认：确认 1 次 DELETE 且行消失',
      ruleState.ruleDeleteCalls.length === 1 && ruleState.ruleDeleteCalls[0] === 'rule-disk',
      ruleState.ruleDeleteCalls.join(','),
    )

    // ---- 静默 1h ----
    log('step', '静默 1h')
    await adminPage.click('#alerts-rule-silence-rule-new-1')
    await adminPage.waitForSelector('#alerts-silence-3600', { timeout: 10000 })
    await adminPage.click('#alerts-silence-3600')
    await adminPage.waitForSelector('#alerts-rule-unsilence-rule-new-1', { timeout: 10000 })
    check(
      '静默体为秒：{"duration_s":3600}',
      ruleState.silenceCalls.length === 1 &&
        JSON.stringify(ruleState.silenceCalls[0].body) === '{"duration_s":3600}',
      `共 ${ruleState.silenceCalls.length} 次静默请求`,
    )
    check('静默 1h 后行内出现取消静默按钮', (await adminPage.locator('#alerts-rule-unsilence-rule-new-1').count()) === 1)

    // ---- 320px：规则 tab ----
    log('step', '320px 规则 tab')
    await adminPage.setViewportSize({ width: 320, height: 800 })
    await adminPage.waitForTimeout(250)
    const rulesNarrow = await adminPage.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      rows: document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row').length,
    }))
    check(
      '320px 规则 tab 无横向滚动',
      rulesNarrow.rows >= 2 && rulesNarrow.scrollWidth <= rulesNarrow.innerWidth + 1,
      `scrollWidth=${rulesNarrow.scrollWidth}`,
    )
    await shot(adminPage, 'alerts-rules-320')

    /* ------------------------------------------------------------------ */
    /* 场景 3：渠道 tab —— 新建 / 测试 ok·fail / 密钥不回显 / 320px        */
    /* ------------------------------------------------------------------ */
    log('step', '320px 渠道 tab')
    await adminPage.click('#alerts-tab-channels')
    await adminPage.waitForSelector('#alerts-panel-channels .ls-alerts__panel-title', { timeout: 10000 })
    await adminPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-channels .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 10000 },
    )
    const channelsNarrow = await adminPage.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      rows: document.querySelectorAll('#alerts-panel-channels .ls-alertspage__row').length,
    }))
    check(
      '320px 渠道 tab 无横向滚动',
      channelsNarrow.rows >= 2 && channelsNarrow.scrollWidth <= channelsNarrow.innerWidth + 1,
      `scrollWidth=${channelsNarrow.scrollWidth}`,
    )
    await shot(adminPage, 'alerts-channels-320')
    await adminPage.setViewportSize({ width: 1280, height: 900 })

    log('step', '新建 webhook 渠道')
    await adminPage.click('#alerts-channel-create')
    await adminPage.waitForSelector('#alerts-channel-form', { timeout: 10000 })
    await adminPage.fill('#alerts-channel-name', '值班 Webhook')
    await adminPage.fill('#alerts-channel-url', 'https://hooks.example.com/services/T111/B222')
    await adminPage.click('#alerts-channel-save')
    await adminPage.waitForFunction(
      () =>
        [...document.querySelectorAll('#alerts-panel-channels .ls-alertspage__row')].some((row) =>
          row.textContent.includes('值班 Webhook'),
        ),
      undefined,
      { timeout: 10000 },
    )
    const expectedChannelCreate = {
      name: '值班 Webhook',
      kind: 'webhook',
      enabled: true,
      config: { url: 'https://hooks.example.com/services/T111/B222' },
    }
    check(
      '新建 webhook 渠道 POST /alert-channels 恰 1 次',
      ruleState.channelCreateCalls.length === 1 &&
        JSON.stringify(ruleState.channelCreateCalls[0]) === JSON.stringify(expectedChannelCreate),
      JSON.stringify(ruleState.channelCreateCalls[0]),
    )
    check(
      '新建成功后渠道表出现新行',
      (await adminPage
        .locator('#alerts-panel-channels .ls-alertspage__row')
        .filter({ hasText: '值班 Webhook' })
        .count()) === 1,
    )

    log('step', '测试渠道：ok=true')
    await adminPage.click('#alerts-channel-test-ch-ok')
    await adminPage.waitForFunction(
      () => document.querySelector('#alerts-channel-test-result')?.textContent.includes('测试消息已发送'),
      undefined,
      { timeout: 10000 },
    )
    const okResult = adminPage.locator('#alerts-channel-test-result')
    check(
      '渠道测试 ok=true 显示成功文案',
      (await okResult.getAttribute('class')).includes('ls-alerts__notice--ok') &&
        (await okResult.innerText()).includes('运维 Webhook'),
      (await okResult.innerText()).replace(/\s+/g, ' '),
    )

    log('step', '测试渠道：ok=false')
    await adminPage.click('#alerts-channel-test-ch-bad')
    await adminPage.waitForFunction(
      () =>
        document
          .querySelector('#alerts-channel-test-result')
          ?.textContent.includes('401 Unauthorized: bot token invalid'),
      undefined,
      { timeout: 10000 },
    )
    const badResult = adminPage.locator('#alerts-channel-test-result')
    check(
      '渠道测试 ok=false 原样显示后端 error',
      (await badResult.getAttribute('class')).includes('ls-alerts__notice--error') &&
        (await badResult.innerText()).includes('401 Unauthorized: bot token invalid'),
      (await badResult.innerText()).replace(/\s+/g, ' '),
    )
    check(
      '两次测试分别打到各自渠道',
      JSON.stringify(ruleState.channelTestCalls) === JSON.stringify(['ch-ok', 'ch-bad']),
      ruleState.channelTestCalls.join(','),
    )

    const channelsText = await adminPage.locator('#alerts-panel-channels').innerText()
    await adminPage.click('#alerts-channel-edit-ch-ok')
    await adminPage.waitForSelector('#alerts-channel-url', { timeout: 10000 })
    const maskedInput = await adminPage.locator('#alerts-channel-url').inputValue()
    const maskedPlaceholder = await adminPage.locator('#alerts-channel-url').getAttribute('placeholder')
    const channelModalText = await adminPage.locator('#alerts-channel-form').innerText()
    await adminPage.click('#alerts-channel-cancel')
    await adminPage.waitForSelector('#alerts-channel-form', { state: 'detached', timeout: 10000 })
    check(
      '渠道密钥不回显：列表与弹窗都不含 *** 或明文',
      !channelsText.includes('***') &&
        !channelsText.includes('hooks.example.com') &&
        maskedInput === '' &&
        Boolean(maskedPlaceholder && maskedPlaceholder.length > 0) &&
        !channelModalText.includes('***') &&
        !channelModalText.includes('hooks.example.com'),
      `列表 ${channelsText.length} 字 / 弹窗输入 "${maskedInput}" / placeholder "${maskedPlaceholder}"`,
    )
    check('打开渠道编辑弹窗不做写入（取消后 0 次 PATCH）', ruleState.channelPatchCalls.length === 0)

    check('规则/渠道场景无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
    await adminContext.close()

    /* ------------------------------------------------------------------ */
    /* 场景 4：有 alert.read 缺 alert.write —— 只读，零写请求               */
    /* ------------------------------------------------------------------ */
    const readOnly = {
      permissions: ['server.read', 'check.read', 'alert.read'],
      alerts: buildAlerts(),
      listCalls: 0,
      ackCalls: [],
      rules: buildAlertRules(),
      channels: buildAlertChannels(),
      checks: buildChecks(),
      servers: buildServers(),
      ruleCreateCalls: [],
      rulePatchCalls: [],
      ruleDeleteCalls: [],
      silenceCalls: [],
      channelCreateCalls: [],
      channelPatchCalls: [],
      channelDeleteCalls: [],
      channelTestCalls: [],
      writeCalls: [],
    }
    const readOnlyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(readOnlyContext, readOnly)
    const readOnlyPage = await readOnlyContext.newPage()
    readOnlyPage.on('pageerror', (error) => pageErrors.push(`readonly: ${error.message}`))
    await login(readOnlyPage, base)
    await readOnlyPage.goto(`${base}/#/alerts?tab=rules`, { waitUntil: 'domcontentloaded' })
    await readOnlyPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-rules .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 15000 },
    )
    const ruleWriteIds = [
      '#alerts-rule-create',
      '#alerts-rule-edit-rule-cpu',
      '#alerts-rule-silence-rule-cpu',
      '#alerts-rule-delete-rule-cpu',
      '#alerts-rule-enabled-rule-cpu',
    ]
    let ruleWriteButtons = 0
    for (const selector of ruleWriteIds) ruleWriteButtons += await readOnlyPage.locator(selector).count()
    check(
      '缺 alert.write 时规则侧新建/编辑/静默/删除/开关均不渲染',
      ruleWriteButtons === 0 && (await readOnlyPage.locator('#alerts-panel-rules').innerText()).includes('无写入权限'),
      `${ruleWriteButtons} 个写按钮`,
    )

    // 零写请求断言的可分辨化：有写控件就逐个点（触发 onClick 全链路），
    // 点完等 300ms 再关掉可能弹出的弹窗，然后才断言零写请求。
    // 守卫有效时写控件数为 0 → controls=0、clicked=0，断言照常通过；
    // 守卫失效时控件会渲染 → 点击必然产生写请求，断言变红。
    // 点击用 DOM click 直接命中控件本身，避免弹窗遮罩吞掉真实点击造成假绿。
    const writeClickStats = { controls: 0, clicked: 0, selectors: [] }
    const dismissAlertModal = async () => {
      for (const cancel of [
        '#alerts-rule-cancel',
        '#alerts-silence-cancel',
        '#alerts-delete-cancel',
        '#alerts-channel-cancel',
      ]) {
        const button = await readOnlyPage.$(cancel)
        if (button) {
          await button.evaluate((el) => el.click()).catch(() => {})
          await readOnlyPage.waitForTimeout(100)
        }
      }
    }
    const clickWriteControls = async (selectors) => {
      for (const selector of selectors) {
        const matched = await readOnlyPage.$$(selector)
        writeClickStats.selectors.push(`${selector}=${matched.length}`)
        writeClickStats.controls += matched.length
        for (let index = 0; index < matched.length; index += 1) {
          const current = await readOnlyPage.$$(selector)
          const handle = current[index]
          if (!handle) continue
          try {
            await handle.evaluate((el) => el.click())
          } catch {
            continue
          }
          writeClickStats.clicked += 1
          await readOnlyPage.waitForTimeout(300)
          await dismissAlertModal()
        }
      }
    }
    await clickWriteControls([
      '#alerts-rule-create',
      'button[id^="alerts-rule-edit-"]',
      'button[id^="alerts-rule-silence-"]',
      'button[id^="alerts-rule-unsilence-"]',
      'button[id^="alerts-rule-delete-"]',
      'input[id^="alerts-rule-enabled-"]',
    ])

    await readOnlyPage.click('#alerts-tab-channels')
    await readOnlyPage.waitForFunction(
      () => document.querySelectorAll('#alerts-panel-channels .ls-alertspage__row').length === 2,
      undefined,
      { timeout: 10000 },
    )
    const channelWriteIds = [
      '#alerts-channel-create',
      '#alerts-channel-test-ch-ok',
      '#alerts-channel-edit-ch-ok',
      '#alerts-channel-delete-ch-ok',
    ]
    let channelWriteButtons = 0
    for (const selector of channelWriteIds) channelWriteButtons += await readOnlyPage.locator(selector).count()
    check(
      '缺 alert.write 时渠道侧新建/测试/编辑/删除均不渲染',
      channelWriteButtons === 0 && (await readOnlyPage.locator('#alerts-panel-channels').innerText()).includes('无写入权限'),
      `${channelWriteButtons} 个写按钮`,
    )
    await clickWriteControls([
      '#alerts-channel-create',
      'button[id^="alerts-channel-test-"]',
      'button[id^="alerts-channel-edit-"]',
      'button[id^="alerts-channel-delete-"]',
    ])
    const readOnlyWriteCalls = readOnly.writeCalls.length
    check(
      `零写请求断言可分辨：写控件 ${writeClickStats.controls} 个、已点击 ${writeClickStats.clicked} 次、writeCalls=${readOnlyWriteCalls}`,
      readOnlyWriteCalls === 0,
      readOnlyWriteCalls === 0
        ? ''
        : `${readOnly.writeCalls.slice(0, 3).join(' | ')}；${writeClickStats.selectors.join('、')}`,
    )
    await readOnlyContext.close()

    /* ------------------------------------------------------------------ */
    /* 场景 5：无 alert.read —— 0 请求 + /forbidden                        */
    /* ------------------------------------------------------------------ */
    const denied = {
      permissions: ['server.read'],
      alerts: buildAlerts(),
      listCalls: 0,
      ackCalls: [],
      writeCalls: [],
    }
    const deniedContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(deniedContext, denied)
    const deniedPage = await deniedContext.newPage()
    deniedPage.on('pageerror', (error) => pageErrors.push(`denied: ${error.message}`))

    await login(deniedPage, base)
    await deniedPage.goto(`${base}/#/alerts`, { waitUntil: 'domcontentloaded' })
    await deniedPage.waitForSelector('.ls-forbidden', { timeout: 15000 })
    check('无 alert.read 时进入 /forbidden', new URL(deniedPage.url()).hash.startsWith('#/forbidden'), new URL(deniedPage.url()).hash)
    check('无 alert.read 时 0 次告警请求', denied.listCalls === 0, `${denied.listCalls} 次`)
    check('无权限页面不渲染告警列表', (await deniedPage.locator('.ls-alertspage__row').count()) === 0)
    await deniedContext.close()
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(
    `\n${failed.length === 0 ? '✓ 告警页冒烟全部通过' : '✗ 告警页冒烟存在失败'}：共 ${results.length} 项，失败 ${failed.length} 项`,
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
