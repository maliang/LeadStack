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
    /* 场景 2：无 alert.read —— 0 请求 + /forbidden                        */
    /* ------------------------------------------------------------------ */
    const denied = {
      permissions: ['server.read'],
      alerts: buildAlerts(),
      listCalls: 0,
      ackCalls: [],
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
    `\n${failed.length === 0 ? '✓ 告警页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
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
