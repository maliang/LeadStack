#!/usr/bin/env node
/**
 * 总览页（/#/）冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：KPI 数字与 online 率 / 「需要关注」只列非 online 且最多 6 条 + 溢出提示 / 全在线提示 /
 * 检测与告警概览数值 / 最近发布行与 runs 统计（应用名走 GET /apps 映射、403 回退 app_id、与 /releases 一致）/
 * 空态 / 无 deploy.read 时只显示无权限且不发 releases、apps 请求 / 无 check.read、alert.read 时零请求且只显示无权限 /
 * 单接口 500 只让该块报错、其它块照常 ready / 错误块重试可恢复 / 慢接口的加载骨架 / 320px 无横向滚动。
 * 用法：node scripts/smoke-dashboard.mjs [--headed] [--dist dist] [--port 0] [--shot out.png]
 */
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  launchChromium,
  loadChromium,
  log,
  login,
  makeChecker,
  parseFlags,
  resolveDist,
  startStaticServer,
  watchApiCalls,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const REQUESTED_PORT = Number(flag('port', 0)) || 0
const { dist: DIST, ready: DIST_READY } = resolveDist(WEB_ROOT, flag)
const SHOT = String(flag('shot', ''))

const NOW = Date.now()

/** 4 在线 + 1 离线 + 1 未知：KPI 总数 6 / 在线 4 / 需要关注 2 / 在线率 67%。 */
function serversFixture() {
  const online = ['srv-a', 'srv-b', 'srv-c', 'srv-d']
  return [
    ...online.map((id, index) => ({
      id,
      name: `web-${index + 1}`,
      host: `10.0.1.${index + 1}`,
      port: 22,
      mode: 'agent',
      status: 'online',
      last_seen_at: NOW - 5000,
      metrics: { cpu_pct: 12.5, mem_pct: 40.1, disk_pct: 55 },
    })),
    {
      id: 'srv-db',
      name: 'db-standby',
      host: '10.0.2.9',
      port: 2222,
      mode: 'ssh',
      status: 'offline',
      last_seen_at: NOW - 3600_000,
      metrics: { cpu_pct: 3, mem_pct: 61, disk_pct: 44 },
    },
    {
      id: 'srv-edge',
      name: 'edge-unknown',
      host: '10.0.3.7',
      port: 22,
      mode: 'agent',
      status: 'unknown',
      last_seen_at: null,
      metrics: { cpu_pct: 0, mem_pct: 0, disk_pct: 0 },
    },
  ]
}

const CHECK_SUMMARY = { total: 8, up: 5, down: 2, unknown: 1, checks: [] }
const ALERT_SUMMARY = {
  firing: 2,
  acked: 1,
  resolved_24h: 4,
  by_severity: { critical: 1, warning: 2, info: 3 },
}

function releasesFixture() {
  return [
    {
      id: 'rel-1',
      app_id: 'app-1',
      version: '1.4.2',
      status: 'success',
      strategy: 'all',
      created_at: NOW - 3600_000,
      runs: [
        { id: 'run-1', server_id: 'srv-a', status: 'success' },
        { id: 'run-2', server_id: 'srv-b', status: 'success' },
        { id: 'run-3', server_id: 'srv-c', status: 'failed' },
      ],
    },
    {
      id: 'rel-2',
      app_id: 'app-2',
      version: '0.9.0',
      status: 'running',
      strategy: 'canary',
      created_at: NOW - 600_000,
      runs: [
        { id: 'run-4', server_id: 'srv-a', status: 'running' },
        { id: 'run-5', server_id: 'srv-d', status: 'pending' },
      ],
    },
    {
      id: 'rel-3',
      app_id: 'app-3',
      version: '2.0.0',
      status: 'failed',
      strategy: 'all',
      created_at: NOW - 86_400_000,
      runs: [],
    },
  ]
}

/**
 * GET /apps：真实 /releases 不返回 app_name，首页必须用这一次应用列表把 app_id 映射成应用名。
 * app-3 故意不在表里，用来验证映射不到时回退 app_id。
 */
const APPS = [
  { id: 'app-1', name: 'shop-web-v2', slug: 'shop-web-v2', provider: 'builtin', created_at: NOW - 86_400_000 },
  { id: 'app-2', name: 'api-service', slug: 'api-service', provider: 'builtin', created_at: NOW - 86_400_000 },
]

/** 每个场景只覆盖它关心的字段，其余沿用默认值。 */
const SCENARIOS = {
  happy: {},
  allOnline: { servers: serversFixture().filter((server) => server.status === 'online'), onlineCount: 4, totalCount: 4 },
  manyAttention: {
    servers: [
      ...serversFixture().filter((server) => server.status === 'online').slice(0, 2),
      ...Array.from({ length: 8 }, (_, index) => ({
        id: `srv-${index + 1}`,
        name: `down-${index + 1}`,
        host: `10.9.0.${index + 1}`,
        port: 22,
        mode: 'agent',
        status: 'offline',
        last_seen_at: NOW - 7200_000,
        metrics: { cpu_pct: 0, mem_pct: 0, disk_pct: 0 },
      })),
    ],
    totalCount: 10,
    onlineCount: 2,
  },
  empty: { servers: [], totalCount: 0, onlineCount: 0, checks: { total: 0, up: 0, down: 0, unknown: 0, checks: [] }, alerts: { firing: 0, acked: 0, resolved_24h: 0, by_severity: { critical: 0, warning: 0, info: 0 } }, releases: [] },
  serverError: { fail: { servers: true } },
  checksError: { fail: { checks: true } },
  kpiError: { fail: { info: true, servers: true } },
  slowChecks: { delay: { checks: 1800 } },
}

function scenarioData(state) {
  const scenario = SCENARIOS[state.active] ?? {}
  return {
    servers: scenario.servers ?? serversFixture(),
    totalCount: scenario.totalCount ?? 6,
    onlineCount: scenario.onlineCount ?? 4,
    checks: scenario.checks ?? CHECK_SUMMARY,
    alerts: scenario.alerts ?? ALERT_SUMMARY,
    releases: scenario.releases ?? releasesFixture(),
    fail: scenario.fail ?? {},
    delay: scenario.delay ?? {},
  }
}

function currentUser(state) {
  return {
    id: 'u-admin',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: state.permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))

async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const data = scenarioData(state)
    const maybeDelay = async (key) => {
      const ms = data.delay[key]
      if (ms) await sleep(ms)
    }
    const maybeFail = (key) => data.fail[key] === true

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: '{{SECRET_mock_token}}', user: currentUser(state) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: currentUser(state) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/system/info' && method === 'GET') {
      if (maybeFail('info')) return json(500, { error: { code: 'internal', message: '系统信息不可用' } })
      return json(200, {
        data: {
          version: '9.9.9',
          uptime_s: 7260,
          server_count: data.totalCount,
          online_count: data.onlineCount,
          hostname: 'control-plane',
        },
      })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      await maybeDelay('servers')
      if (maybeFail('servers')) return json(500, { error: { code: 'internal', message: '服务器列表不可用' } })
      return json(200, { data: data.servers.map((server) => ({ ...server })) })
    }
    if (path === '/api/v1/checks/summary' && method === 'GET') {
      await maybeDelay('checks')
      if (maybeFail('checks')) return json(500, { error: { code: 'internal', message: '检测汇总不可用' } })
      return json(200, { data: { ...data.checks } })
    }
    if (path === '/api/v1/alerts/summary' && method === 'GET') {
      await maybeDelay('alerts')
      if (maybeFail('alerts')) return json(500, { error: { code: 'internal', message: '告警汇总不可用' } })
      return json(200, { data: { ...data.alerts, by_severity: { ...data.alerts.by_severity } } })
    }
    if (path === '/api/v1/apps' && method === 'GET') {
      if (state.appsFail) return json(403, { error: { code: 'forbidden', message: 'forbidden' } })
      return json(200, { data: APPS.map((app) => ({ ...app })) })
    }
    if (path === '/api/v1/releases' && method === 'GET') {
      await maybeDelay('releases')
      if (maybeFail('releases')) return json(500, { error: { code: 'internal', message: '发布列表不可用' } })
      return json(200, { data: data.releases.map((release) => ({ ...release, runs: release.runs.map((run) => ({ ...run })) })) })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

/** 用 waitForFunction 轮询标题文本（禁止 waitForSelector('.ls-adminpage__title')），超时也继续读。 */
async function readHeading(page, expected) {
  await page
    .waitForFunction(
      (title) => {
        const node = document.querySelector('.ls-adminpage__title')
        return Boolean(node) && (node.textContent || '').trim() === title
      },
      expected,
      { timeout: 15000 },
    )
    .catch(() => {})
  return (await page.locator('.ls-adminpage__title').first().innerText()).trim()
}

/** 换权限重新登录：先清 token 再整页 reload，避免内存里的 auth 状态把 /login 又重定向走。 */
async function relogin(page, base, state, permissions) {
  state.permissions = permissions
  await page.evaluate(() => window.localStorage.clear())
  await page.reload({ waitUntil: 'domcontentloaded' })
  await login(page, base, { password: 'smoke-password' })
}

async function gotoDashboard(page, base, state, scenario) {
  state.active = scenario
  // 同一 URL 只改 hash 时浏览器不会重新加载，这里显式 reload 保证拿的是当前 scenario 的数据
  await page.goto(`${base}/#/`, { waitUntil: 'domcontentloaded' })
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#dashboard-page', { timeout: 15000 })
}

async function text(page, selector) {
  return (await page.locator(selector).first().innerText()).trim()
}

async function waitText(page, selector, expected, timeout = 10000) {
  await page
    .waitForFunction(
      ({ selector: target, expected: want }) => {
        const node = document.querySelector(target)
        return Boolean(node) && (node.textContent || '').trim() === want
      },
      { selector, expected },
      { timeout },
    )
    .catch(() => {})
  return await text(page, selector)
}

async function count(page, selector) {
  return await page.locator(selector).count()
}

async function waitFor(page, selector, timeout = 10000) {
  await page.waitForSelector(selector, { timeout })
}

async function run() {
  if (!DIST_READY) {
    console.error(`✗ 没有构建产物：${join(DIST, 'index.html')}\n  先跑：pnpm --dir web-next run build`)
    process.exitCode = 2
    return
  }

  let chromium
  try {
    chromium = await loadChromium()
  } catch (error) {
    console.error(`✗ ${error.message}`)
    process.exitCode = 2
    return
  }

  const { check, finish } = makeChecker()
  const { server, base } = await startStaticServer(DIST, REQUESTED_PORT)
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
    active: 'happy',
    permissions: ['server.read', 'check.read', 'alert.read', 'deploy.read'],
    appsFail: false,
  }
  const pageErrors = []
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  page.on('pageerror', (error) => pageErrors.push(error && error.message ? error.message : String(error)))
  const apiCalls = watchApiCalls(page)

  try {
    await login(page, base, { password: 'smoke-password' })
    log('login', '已登录')

    /* ---------------- 有数据：数字与 mock 对齐 ---------------- */
    await gotoDashboard(page, base, state, 'happy')
    const heading = await readHeading(page, '总览')
    check('进入 #/ 渲染总览标题（不再是脚手架首屏）', heading === '总览', heading)
    check(
      'KPI 服务器总数 = mock 的 server_count 6',
      (await waitText(page, '#dash-kpi-total .ls-dashboard__kpi-value', '6')) === '6',
    )
    check(
      'KPI 在线数 = mock 的 online_count 4',
      (await waitText(page, '#dash-kpi-online .ls-dashboard__kpi-value', '4')) === '4',
    )
    check(
      'KPI 离线数 = 总数 - 在线 = 2',
      (await waitText(page, '#dash-kpi-offline .ls-dashboard__kpi-value', '2')) === '2',
    )
    check('KPI 在线率显示 67%', (await text(page, '#dash-kpi-online .ls-dashboard__kpi-hint')).includes('67%'))
    check(
      'KPI 已运行时长来自 uptime_s（2 小时 1 分钟）',
      (await text(page, '#dash-kpi-total .ls-dashboard__kpi-hint')).includes('2 小时'),
      await text(page, '#dash-kpi-total .ls-dashboard__kpi-hint'),
    )

    await waitFor(page, '#dash-attention-count')
    check('「需要关注」计数 = 非 online 的 2 台', (await text(page, '#dash-attention-count')) === '2')
    const attentionStatuses = await page.$$eval('.ls-dashboard__attention-row', (rows) =>
      rows.map((row) => row.getAttribute('data-status')),
    )
    check(
      '「需要关注」只列非 online（offline / unknown），顺序与 mock 一致',
      JSON.stringify(attentionStatuses) === JSON.stringify(['offline', 'unknown']),
      attentionStatuses.join(','),
    )
    const attentionNames = await page.$$eval('.ls-dashboard__attention-name', (nodes) =>
      nodes.map((node) => (node.textContent || '').trim()),
    )
    check('「需要关注」行名与 mock 对应', attentionNames.join(',') === 'db-standby,edge-unknown', attentionNames.join(','))
    check('有需要关注的机器时不显示「全部在线」提示', (await count(page, '#dash-attention-all-online')) === 0)

    await waitFor(page, '#dash-checks-ready')
    check('检测概览 total 8', (await text(page, '[data-check="total"] .ls-dashboard__tile-value')) === '8')
    check('检测概览 up 5', (await text(page, '[data-check="up"] .ls-dashboard__tile-value')) === '5')
    check('检测概览 down 2', (await text(page, '[data-check="down"] .ls-dashboard__tile-value')) === '2')
    check('检测概览 unknown 1', (await text(page, '[data-check="unknown"] .ls-dashboard__tile-value')) === '1')

    await waitFor(page, '#dash-alerts-ready')
    check('告警概览 firing 2', (await text(page, '[data-alert="firing"] .ls-dashboard__tile-value')) === '2')
    check('告警概览 acked 1', (await text(page, '[data-alert="acked"] .ls-dashboard__tile-value')) === '1')
    check('告警概览 24h 已恢复 4', (await text(page, '[data-alert="resolved"] .ls-dashboard__tile-value')) === '4')
    check('告警按严重度显示 1/2/3', (await text(page, '[data-alert="severity"] .ls-dashboard__tile-value')) === '1/2/3')

    await waitFor(page, '#dash-releases-ready')
    check('最近发布列出 3 条（mock 给 3 条）', (await count(page, '.ls-dashboard__release')) === 3)
    check(
      '最近发布首行版本 v1.4.2',
      (await text(page, '.ls-dashboard__release .ls-dashboard__release-version')) === 'v1.4.2',
    )
    check(
      'release.runs 统计成功 2 / 失败 1 / 进行中 0',
      (await text(page, '.ls-dashboard__release .ls-dashboard__release-runs')).includes('成功 2') &&
        (await text(page, '.ls-dashboard__release .ls-dashboard__release-runs')).includes('失败 1'),
      await text(page, '.ls-dashboard__release .ls-dashboard__release-runs'),
    )
    check('发布成功态徽标使用 success 色调', (await count(page, '#dash-releases-ready .ls-badge--success')) >= 1)
    if (SHOT) {
      await page.screenshot({ path: SHOT.replace(/\.png$/, '-1280.png'), fullPage: true })
    }
    check('有数据时四个错误块都不出现', (await count(page, '#dash-checks-error, #dash-alerts-error, #dash-releases-error, #dash-attention-error')) === 0)

    /* ---------------- 最近发布应用名：真实 /releases 只给 app_id，须走 /apps 映射 ---------------- */
    const firstAppLabel = await text(page, '.ls-dashboard__release .ls-dashboard__release-app')
    check('最近发布应用名来自 GET /apps 映射（不是 app_id）', firstAppLabel === 'shop-web-v2', firstAppLabel)
    check('最近发布不再出现「未命名应用」（真实 /releases 无 app_name）', !firstAppLabel.includes('未命名'), firstAppLabel)
    check(
      '映射不到的 app-3 回退 app_id',
      (await text(page, '#dash-releases-ready > li:nth-child(3) .ls-dashboard__release-app')) === 'app-3',
    )
    // 登录跳转也会挂载一次首页；这里只统计本次整页加载的 /apps 请求数
    const appsBeforeReload = apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length
    await gotoDashboard(page, base, state, 'happy')
    await waitFor(page, '#dash-releases-ready')
    check(
      '最近发布只发一次 /apps（不按行请求）',
      apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length - appsBeforeReload === 1,
      `${apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length - appsBeforeReload} 次`,
    )
    const dashboardFirstApp = await text(page, '.ls-dashboard__release .ls-dashboard__release-app')

    /* 首页首行应用名必须与 /releases 首行一致（同一份 /apps 映射） */
    await page.goto(`${base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await waitFor(page, '.ls-deploy__row[data-release-id="rel-1"]')
    const releasesFirstApp = await text(page, '.ls-deploy__row[data-release-id="rel-1"] .ls-deploy__name')
    check(
      '首页首行应用名与 /releases 首行应用名一致',
      dashboardFirstApp === 'shop-web-v2' && releasesFirstApp === dashboardFirstApp,
      `首页=${dashboardFirstApp} / /releases=${releasesFirstApp}`,
    )
    await gotoDashboard(page, base, state, 'happy')
    await waitFor(page, '#dash-releases-ready')

    /* ---------------- 已有数据后刷新失败：不静默，保留旧值 + 可见提示 ---------------- */
    SCENARIOS.happy.fail = { checks: true }
    await page.click('#dash-refresh')
    await waitFor(page, '#dash-checks-stale')
    check(
      '刷新失败时检测块保留旧数据并提示「上次结果」（失败不静默）',
      (await count(page, '#dash-checks-ready')) === 1 &&
        (await text(page, '[data-check="total"] .ls-dashboard__tile-value')) === '8',
    )
    check('单块刷新失败不拖垮告警/发布块', (await count(page, '#dash-alerts-ready, #dash-releases-ready')) === 2)
    delete SCENARIOS.happy.fail

    /* ---------------- 全在线 ---------------- */
    await gotoDashboard(page, base, state, 'allOnline')
    await waitFor(page, '#dash-attention-all-online')
    check('全部在线时显示「全部在线」提示', (await text(page, '#dash-attention-all-online')).includes('全部服务器都在线'))
    check('全部在线时「需要关注」没有行', (await count(page, '.ls-dashboard__attention-row')) === 0)
    check(
      '全部在线时离线 KPI 为 0',
      (await waitText(page, '#dash-kpi-offline .ls-dashboard__kpi-value', '0')) === '0',
    )

    /* ---------------- 超过 6 条截断 ---------------- */
    await gotoDashboard(page, base, state, 'manyAttention')
    await waitFor(page, '#dash-attention-count')
    check('非 online 有 8 台时「需要关注」最多列 6 行', (await count(page, '.ls-dashboard__attention-row')) === 6)
    check('截断时显示「还有 2 台未显示」', (await text(page, '#dash-attention-more')).includes('2'))

    /* ---------------- 空态 ---------------- */
    await gotoDashboard(page, base, state, 'empty')
    await waitFor(page, '#dash-kpi-empty')
    check('0 台服务器时 KPI 区显示空态文案', (await count(page, '#dash-kpi-empty')) === 1)
    check('空态下「需要关注」显示无服务器', (await count(page, '#dash-attention-empty')) === 1)
    check('空态下检测概览显示空态', (await count(page, '#dash-checks-empty')) === 1)
    check('空态下告警概览显示空态', (await count(page, '#dash-alerts-empty')) === 1)
    check('空态下最近发布显示空态', (await count(page, '#dash-releases-empty')) === 1)

    /* ---------------- 无 deploy.read：不发 releases 请求 ---------------- */
    state.active = 'happy'
    const releasesBefore = apiCalls.filter((call) => call.path === '/api/v1/releases').length
    const appsBeforeDenied = apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length
    const checksBeforeDenied = apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/checks/summary').length
    const alertsBeforeDenied = apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/alerts/summary').length
    await relogin(page, base, state, ['server.read'])
    await gotoDashboard(page, base, state, 'happy')
    await waitFor(page, '#dash-releases-denied')
    await waitFor(page, '#dash-checks-denied')
    await waitFor(page, '#dash-alerts-denied')
    check('无 deploy.read 时最近发布只显示无权限文案', (await text(page, '#dash-releases-denied')).includes('没有发布的查看权限'))
    check(
      '无 deploy.read 时不请求 /releases',
      apiCalls.filter((call) => call.path === '/api/v1/releases').length === releasesBefore,
      `${releasesBefore} → ${apiCalls.filter((call) => call.path === '/api/v1/releases').length}`,
    )
    check(
      '无 deploy.read 时不请求 /apps（与 /releases 同门控）',
      apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length === appsBeforeDenied,
      `${appsBeforeDenied} → ${apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps').length}`,
    )
    check(
      '无 check.read 时检测概览只显示无权文案（不是加载骨架）',
      (await count(page, '#dash-checks-denied')) === 1 &&
        (await count(page, '#dash-checks-loading')) === 0 &&
        (await text(page, '#dash-checks-denied')).includes('check.read'),
      await text(page, '#dash-checks-denied'),
    )
    check(
      '无 alert.read 时告警概览只显示无权文案（不是加载骨架）',
      (await count(page, '#dash-alerts-denied')) === 1 &&
        (await count(page, '#dash-alerts-loading')) === 0 &&
        (await text(page, '#dash-alerts-denied')).includes('alert.read'),
      await text(page, '#dash-alerts-denied'),
    )
    check(
      '仅 server.read 时不请求 /checks/summary',
      apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/checks/summary').length ===
        checksBeforeDenied,
      `${checksBeforeDenied} → ${apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/checks/summary').length}`,
    )
    check(
      '仅 server.read 时不请求 /alerts/summary',
      apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/alerts/summary').length ===
        alertsBeforeDenied,
      `${alertsBeforeDenied} → ${apiCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/alerts/summary').length}`,
    )
    check(
      '仅 server.read 时服务器相关区块照常出数（server.read 未被误门控）',
      (await count(page, '#dash-kpi-total')) === 1 &&
        (await count(page, '#dash-attention-count')) === 1 &&
        (await count(page, '#dash-kpi-denied, #dash-attention-denied')) === 0,
    )
    await relogin(page, base, state, ['server.read', 'check.read', 'alert.read', 'deploy.read'])

    /* ---------------- 单块 500：只有该块报错 ---------------- */
    await gotoDashboard(page, base, state, 'checksError')
    await waitFor(page, '#dash-checks-error')
    check('checks/summary 500 时检测块显示错误与重试入口', (await count(page, '#dash-checks-retry')) === 1)
    check(
      '检测块失败时 KPI 仍出数（6）',
      (await waitText(page, '#dash-kpi-total .ls-dashboard__kpi-value', '6')) === '6',
    )
    check('检测块失败时「需要关注」仍渲染 2 行', (await count(page, '.ls-dashboard__attention-row')) === 2)
    check('检测块失败时告警块仍 ready', (await count(page, '#dash-alerts-ready')) === 1)
    check('检测块失败时最近发布仍 ready', (await count(page, '#dash-releases-ready')) === 1)
    check('检测块失败不影响告警/发布/关注块无错误', (await count(page, '#dash-alerts-error, #dash-releases-error, #dash-attention-error')) === 0)
    state.active = 'happy'
    await page.click('#dash-checks-retry')
    await waitFor(page, '#dash-checks-ready')
    check('检测块重试后恢复 ready（错误块可恢复）', (await text(page, '[data-check="total"] .ls-dashboard__tile-value')) === '8')

    /* ---------------- KPI 双源失败 ---------------- */
    await gotoDashboard(page, base, state, 'kpiError')
    await waitFor(page, '#dash-kpi-error')
    check('system/info 与 servers 都 500 时 KPI 显示错误态', (await count(page, '#dash-kpi-retry')) === 1)
    check('KPI 失败时检测块仍 ready', (await count(page, '#dash-checks-ready')) === 1)

    /* ---------------- 慢接口：加载骨架 + 其它块先出数 ---------------- */
    await gotoDashboard(page, base, state, 'slowChecks')
    await waitFor(page, '#dash-checks-loading', 8000)
    check('checks 响应慢时检测块先显示加载骨架', (await count(page, '#dash-checks-loading')) === 1)
    check(
      '慢接口期间 KPI 已经出数',
      (await waitText(page, '#dash-kpi-total .ls-dashboard__kpi-value', '6', 5000)) === '6',
    )
    await waitFor(page, '#dash-checks-ready', 15000)
    check('慢接口返回后加载骨架被真实数据替换', (await count(page, '#dash-checks-loading')) === 0)

    /* ---------------- /apps 403：应用名回退 app_id，不显示「未命名应用」 ---------------- */
    state.active = 'happy'
    state.appsFail = true
    await gotoDashboard(page, base, state, 'happy')
    await waitFor(page, '#dash-releases-ready')
    const fallbackLabel = await text(page, '.ls-dashboard__release .ls-dashboard__release-app')
    check('/apps 403 时最近发布回退 app_id', fallbackLabel === 'app-1', fallbackLabel)
    check('/apps 403 时也不出现「未命名应用」', !fallbackLabel.includes('未命名'), fallbackLabel)
    state.appsFail = false

    /* ---------------- 320px 无横向滚动 ---------------- */
    await gotoDashboard(page, base, state, 'happy')
    await waitFor(page, '#dash-attention-count')
    await page.setViewportSize({ width: 320, height: 900 })
    await page.waitForFunction(() => document.querySelectorAll('.ls-dashboard__attention-row').length === 2, undefined, {
      timeout: 10000,
    })
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    if (SHOT) await page.screenshot({ path: SHOT.replace(/\.png$/, '-320.png'), fullPage: true })
    check('320px 窄宽下没有横向滚动', overflow <= 0, `overflow=${overflow}px`)

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  process.exitCode = finish()
}

await run()
