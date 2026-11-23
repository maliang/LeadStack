#!/usr/bin/env node
/**
 * 检测页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表渲染（名称/类型/目标/间隔/启用/最近状态）/ agent 代检标记 /
 * 启用开关 PATCH / 立即执行 POST run 后的状态刷新 / 关键字+类型+状态筛选与空态 /
 * 详情抽屉（targets + server_name）/ 首屏失败重试 / 无 check.read 不发请求且落 /forbidden /
 * 320px 无横向滚动。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-checks.mjs [--headed]
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

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.map': 'application/json; charset=utf-8',
}

const TOKEN = '{{SECRET_che1cks9}}'
const PASSWORD = '{{SECRET_che2ckpw}}'

const SCOPE_ALL = { mode: 'all', group_ids: [], server_ids: [] }

function user(permissions) {
  return {
    id: 'smoke-checks-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions,
    scope: SCOPE_ALL,
  }
}

const ADMIN = user(['check.read', 'check.write'])
const READER = user(['check.read'])
const NO_CHECK_PERM = user(['server.read'])

function checkFixture(overrides) {
  const now = Date.now()
  return {
    id: 'c1',
    name: 'gateway-http',
    kind: 'http',
    target: 'https://gateway.internal/healthz',
    interval_s: 30,
    timeout_s: 10,
    params: { probe_via: 'agent', expect_status: 200 },
    enabled: true,
    created_at: now - 86_400_000,
    target_count: 2,
    server_count: 2,
    server_ids: ['srv-alpha', 'srv-beta'],
    status: 'up',
    state: 'up',
    consecutive_fail: 0,
    availability_24h: 99.95,
    results_24h: 2880,
    last_result: {
      id: 9001,
      check_id: 'c1',
      server_id: 'srv-alpha',
      server_name: 'alpha-node',
      ts: now - 15_000,
      ok: true,
      latency_ms: 42,
      status_code: 200,
      message: '',
    },
    ...overrides,
  }
}

/** 初始三个检测项：一个正常 + agent 代检、一个异常、一个停用且从未跑过。 */
function seedChecks() {
  return [
    checkFixture({}),
    checkFixture({
      id: 'c2',
      name: 'db-tcp',
      kind: 'tcp',
      target: '10.0.0.21:5432',
      interval_s: 60,
      params: { probe_via: 'control' },
      target_count: 1,
      server_count: 1,
      server_ids: ['srv-db'],
      status: 'down',
      state: 'down',
      consecutive_fail: 3,
      availability_24h: 97.2,
      results_24h: 1440,
      last_result: {
        id: 9101,
        check_id: 'c2',
        server_id: 'srv-db',
        server_name: 'db-node',
        ts: Date.now() - 45_000,
        ok: false,
        latency_ms: 3000,
        status_code: null,
        message: 'connection refused',
      },
    }),
    checkFixture({
      id: 'c3',
      name: 'edge-ping',
      kind: 'ping',
      target: '10.0.0.31',
      interval_s: 120,
      params: { probe_via: 'control' },
      enabled: false,
      target_count: 0,
      server_count: 0,
      server_ids: [],
      status: 'unknown',
      state: 'unknown',
      consecutive_fail: 0,
      availability_24h: null,
      results_24h: 0,
      last_result: null,
    }),
  ]
}

/** 详情抽屉要看的结果时间线（按 ts 倒序，含 server_name）。 */
const RESULTS = {
  c1: [
    {
      id: 9002,
      check_id: 'c1',
      server_id: 'srv-alpha',
      server_name: 'alpha-node',
      ts: Date.now() - 15_000,
      ok: true,
      latency_ms: 42,
      status_code: 200,
      message: '',
    },
    {
      id: 9003,
      check_id: 'c1',
      server_id: 'srv-beta',
      server_name: 'beta-node',
      ts: Date.now() - 15_500,
      ok: false,
      latency_ms: 5001,
      status_code: 503,
      message: 'upstream unavailable',
    },
  ],
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

/**
 * 假后端：只覆盖检测页需要的端点，其他一律 404，避免静默放过。
 * state.user 决定权限（换账号即换权限），state.checks 是可变的内存数据库。
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
      return json(200, { token: TOKEN, user: state.user })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: state.user })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }

    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, {
        data: [
          { id: 'srv-alpha', name: 'alpha-node', host: '10.0.0.11', port: 22, mode: 'agent', status: 'online', tags: [] },
          { id: 'srv-beta', name: 'beta-node', host: '10.0.0.12', port: 22, mode: 'agent', status: 'online', tags: [] },
        ],
      })
    }
    if (path === '/api/v1/checks' && method === 'GET') {
      state.listCalls += 1
      if (state.listFailures > 0) {
        state.listFailures -= 1
        return json(500, { error: { code: 'internal', message: 'mock 列表故障', request_id: 'req-mock-1' } })
      }
      return json(200, { data: state.checks })
    }
    if (path === '/api/v1/checks' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      state.creates.push(body)
      const created = checkFixture({
        id: 'c-new',
        name: body.name,
        kind: body.kind,
        target: body.target,
        interval_s: body.interval_s,
        timeout_s: body.timeout_s,
        params: body.params ?? {},
        server_ids: body.server_ids ?? [],
        server_count: (body.server_ids ?? []).length,
        target_count: (body.server_ids ?? []).length,
      })
      state.checks.push(created)
      return json(200, { data: created })
    }

    const detail = /^\/api\/v1\/checks\/([^/]+)$/.exec(path)
    if (detail) {
      const id = decodeURIComponent(detail[1])
      const found = state.checks.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found' } })
      if (method === 'GET') return json(200, { data: found })
      if (method === 'PATCH') {
        const body = JSON.parse(request.postData() || '{}')
        state.patches.push({ id, body })
        Object.assign(found, body)
        if (typeof body.enabled === 'boolean') found.enabled = body.enabled
        return json(200, { data: found })
      }
      if (method === 'DELETE') {
        state.deletes.push(id)
        state.checks = state.checks.filter((item) => item.id !== id)
        return json(200, { data: {} })
      }
    }

    const targets = /^\/api\/v1\/checks\/([^/]+)\/targets$/.exec(path)
    if (targets && method === 'POST') {
      const id = decodeURIComponent(targets[1])
      const body = JSON.parse(request.postData() || '{}')
      state.targetSets.push({ id, body })
      const found = state.checks.find((item) => item.id === id)
      if (found) found.server_ids = body.server_ids ?? body.ids ?? found.server_ids
      return json(200, { data: {} })
    }

    const run = /^\/api\/v1\/checks\/([^/]+)\/run$/.exec(path)
    if (run && method === 'POST') {
      const id = decodeURIComponent(run[1])
      const found = state.checks.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found' } })
      state.runs.push(id)
      // 立即执行会写入 last_result / state / availability_24h：这里连列表一起改，
      // 断言「执行后状态刷新」才有意义（前端 refresh 重新 GET /checks）。
      found.state = 'up'
      found.status = 'up'
      found.consecutive_fail = 0
      found.availability_24h = 100
      found.last_result = {
        id: 9900,
        check_id: id,
        server_id: found.server_ids[0] || 'srv-alpha',
        server_name: 'alpha-node',
        ts: Date.now(),
        ok: true,
        latency_ms: 7,
        status_code: 200,
        message: '',
      }
      return json(200, {
        data: {
          results: [{ server_id: found.server_ids[0] || 'srv-alpha', ok: true, latency_ms: 7, message: '' }],
          summary: { ok: 1, failed: 0 },
        },
      })
    }

    const results = /^\/api\/v1\/checks\/([^/]+)\/results$/.exec(path)
    if (results && method === 'GET') {
      const id = decodeURIComponent(results[1])
      state.resultCalls.push({ id, limit: url.searchParams.get('limit') })
      return json(200, { data: RESULTS[id] ?? [] })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

function newState(userRecord) {
  return {
    user: userRecord,
    checks: seedChecks(),
    listCalls: 0,
    listFailures: 0,
    patches: [],
    runs: [],
    resultCalls: [],
    creates: [],
    deletes: [],
    targetSets: [],
  }
}

/** 登录并停在首页（#/），等首页首屏渲染完：首页自己的预取到此结束。 */
async function loginToHome(page, base) {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 15000 })
  await page.fill('#login-username', 'admin')
  await page.fill('#login-password', PASSWORD)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 15000 })
  await page.waitForSelector('.ls-adminpage__title', { timeout: 15000 })
  await page.waitForTimeout(300)
}

/** 登录并按 hash 进检测页。 */
async function signIn(page, base, hash = '#/checks') {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 15000 })
  await page.fill('#login-username', 'admin')
  await page.fill('#login-password', PASSWORD)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 15000 })
  await page.goto(`${base}${hash}`, { waitUntil: 'domcontentloaded' })
}

const rowText = async (page, id) => (await page.locator(`#check-row-${id}`).innerText()).replace(/\s+/g, ' ')

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
  const state = newState(ADMIN)
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const apiCalls = []
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/')) {
      apiCalls.push(`${request.method()} ${new URL(request.url()).pathname}`)
    }
  })

  let legacyContext = null
  try {
    // 1) 登录后进入 /checks：列表渲染出三个检测项
    await signIn(page, base)
    await page.waitForSelector('.ls-checkspage__title', { timeout: 15000 })
    await page.waitForSelector('.ls-checkspage__row', { timeout: 15000 })
    check('登录后进入检测页', new URL(page.url()).hash === '#/checks', new URL(page.url()).hash)
    check('列表渲染全部检测项', (await page.locator('.ls-checkspage__row').count()) === 3, '3 行')

    // 2) 行内字段：名称 / 类型 / 目标 / 间隔 / 启用 / 最近状态
    const first = await rowText(page, 'c1')
    check(
      '行内展示名称/类型/目标/间隔',
      first.includes('gateway-http') && first.includes('HTTP') && first.includes('gateway.internal') && first.includes('30s'),
      first.slice(0, 80),
    )
    check(
      '正常项显示状态与 24h 可用率',
      first.includes('正常') && first.includes('99.95%'),
      first.slice(0, 80),
    )
    const down = await rowText(page, 'c2')
    check('异常项显示失败结果与延迟', down.includes('异常') && down.includes('失败'), down.slice(0, 80))
    check(
      '停用且无结果的项显示占位',
      (await rowText(page, 'c3')).includes('已停用') && (await rowText(page, 'c3')).includes('暂无结果'),
    )

    // 3) probe_via=agent 才有「agent 代检」标记（旧界面有这个前端标记，不能丢）
    const probeBadges = page.locator('.ls-checkspage__probe')
    check(
      'agent 代检标记只出现在 probe_via=agent 的行',
      (await probeBadges.count()) === 1 && (await rowText(page, 'c1')).includes('agent 代检'),
      `${await probeBadges.count()} 个`,
    )

    // 4) 启用开关的初始 aria-checked 来自后端 enabled
    const c1Checked = await page.locator('#check-enabled-c1').getAttribute('aria-checked')
    const c3Checked = await page.locator('#check-enabled-c3').getAttribute('aria-checked')
    check('启用开关初始状态正确', c1Checked === 'true' && c3Checked === 'false', `${c1Checked}/${c3Checked}`)
    check('少量数据不渲染分页', (await page.locator('#checks-pager-info').count()) === 0)

    // 5) 关键字筛选（名称/目标/服务器名）
    await page.fill('#checks-search-input', 'db')
    await page.waitForTimeout(150)
    check(
      '关键字筛选命中目标地址',
      (await page.locator('.ls-checkspage__row').count()) === 1 &&
        (await page.locator('#check-row-c2').count()) === 1,
    )
    const searchByServer = async () => {
      await page.fill('#checks-search-input', 'db-node')
      await page.waitForTimeout(150)
      return page.locator('.ls-checkspage__row').count()
    }
    check('关键字也能命中服务器名', (await searchByServer()) === 1)

    // 6) 类型筛选
    await page.fill('#checks-search-input', '')
    await page.selectOption('#checks-filter-kind', 'http')
    await page.waitForTimeout(150)
    check(
      '类型筛选只留 http',
      (await page.locator('.ls-checkspage__row').count()) === 1 && (await page.locator('#check-row-c1').count()) === 1,
    )

    // 7) 状态筛选
    await page.selectOption('#checks-filter-kind', '')
    await page.selectOption('#checks-filter-state', 'down')
    await page.waitForTimeout(150)
    check(
      '状态筛选只留异常项',
      (await page.locator('.ls-checkspage__row').count()) === 1 && (await page.locator('#check-row-c2').count()) === 1,
    )

    // 8) 筛选无结果的空态 + 清除筛选
    await page.fill('#checks-search-input', '不存在的检测项')
    await page.waitForTimeout(150)
    check('筛选无结果时给空态提示', (await page.locator('#checks-empty-filtered').count()) === 1)
    await page.click('#checks-empty-filtered .ls-notice__action')
    await page.waitForTimeout(200)
    check(
      '清除筛选后恢复全部检测项',
      (await page.locator('.ls-checkspage__row').count()) === 3 && (await page.locator('#checks-filter-reset').count()) === 0,
    )

    // 9) 立即执行：POST /checks/{id}/run → 刷新列表，异常项变正常
    await page.click('#check-run-c2')
    await page.waitForSelector('#checks-run-feedback', { timeout: 10000 })
    await page.waitForFunction(
      () => (document.querySelector('#check-row-c2')?.textContent || '').includes('正常'),
      undefined,
      { timeout: 10000 },
    )
    const afterRun = await rowText(page, 'c2')
    check('立即执行打到了 run 端点', state.runs.includes('c2'), state.runs.join(','))
    check('立即执行后状态刷新（异常 → 正常）', afterRun.includes('正常'), afterRun.slice(0, 80))
    check(
      '立即执行给出汇总反馈',
      (await page.locator('#checks-run-feedback').innerText()).includes('成功 1 台'),
      (await page.locator('#checks-run-feedback').innerText()).replace(/\s+/g, ' ').slice(0, 60),
    )

    // 10) 启用开关：PATCH /checks/{id} {enabled:true}
    await page.click('#check-enabled-c3')
    await page.waitForFunction(
      () => document.querySelector('#check-enabled-c3')?.getAttribute('aria-checked') === 'true',
      undefined,
      { timeout: 10000 },
    )
    const patch = state.patches.find((item) => item.id === 'c3')
    check('启用开关发出 PATCH 且带 enabled=true', Boolean(patch) && patch.body.enabled === true, JSON.stringify(patch ?? {}))
    check(
      '启用开关的界面状态随后端更新',
      (await page.locator('#check-enabled-c3').getAttribute('aria-checked')) === 'true',
    )

    // 11) 详情抽屉：targets（server_name）+ 最近一次结果
    await page.click('#check-detail-c1')
    await page.waitForSelector('.ls-checkspage-drawer__panel', { timeout: 10000 })
    await page.waitForSelector('.ls-checkspage-drawer__target', { timeout: 10000 })
    const drawerText = (await page.locator('.ls-checkspage-drawer__panel').innerText()).replace(/\s+/g, ' ')
    check(
      '详情抽屉按 server_name 列出目标',
      (await page.locator('.ls-checkspage-drawer__target').count()) === 2 &&
        drawerText.includes('alpha-node') &&
        drawerText.includes('beta-node'),
      drawerText.slice(0, 90),
    )
    check(
      '详情抽屉显示逐台最近结果',
      drawerText.includes('成功') && drawerText.includes('失败') && drawerText.includes('503'),
      drawerText.slice(0, 90),
    )
    check('详情抽屉请求了结果时间线', state.resultCalls.some((item) => item.id === 'c1'), JSON.stringify(state.resultCalls.slice(-1)))

    // 12) Esc 关闭抽屉，焦点回到触发按钮
    await page.keyboard.press('Escape')
    await page.waitForSelector('.ls-checkspage-drawer__panel', { state: 'detached', timeout: 5000 })
    const focusedId = await page.evaluate(() => document.activeElement?.id ?? '')
    check('Esc 关闭抽屉并归还焦点', focusedId === 'check-detail-c1', focusedId)

    // 13) 首屏失败 → 错误态可重试
    state.listFailures = 1
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#checks-retry', { timeout: 15000 })
    check('列表加载失败时给错误态与重试', (await page.locator('#checks-retry').count()) === 1)
    await page.click('#checks-retry')
    await page.waitForSelector('.ls-checkspage__row', { timeout: 15000 })
    check('点重试后列表恢复', (await page.locator('.ls-checkspage__row').count()) === 3)

    // 14) 窄宽（320px）不横向滚动，行内按钮仍可点
    await page.setViewportSize({ width: 320, height: 900 })
    await page.waitForTimeout(200)
    const narrow = await page.evaluate(() => {
      const button = document.querySelector('#check-run-c1')
      const box = button?.getBoundingClientRect()
      return {
        overflow: document.documentElement.scrollWidth - window.innerWidth,
        buttonVisible: Boolean(box && box.width > 0 && box.height >= 24 && box.right <= window.innerWidth + 1),
      }
    })
    check('320px 无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 行内执行按钮可见可点', narrow.buttonVisible)
    await page.setViewportSize({ width: 1280, height: 900 })

    // 15) 新建 / 编辑 / 删除检测项（check.write）
    await page.click('#checks-create')
    await page.waitForSelector('#check-form', { timeout: 5000 })
    await page.fill('#check-form-name', 'new-http')
    await page.fill('#check-form-target', 'https://example.com/healthz')
    check(
      '无 server.read 时目标选择降级为说明（仍可保存）',
      (await page.locator('.ls-check-form__servers').count()) === 0,
    )
    await page.click('#check-form-submit')
    await page.waitForSelector('#checks-action-notice', { timeout: 8000 })
    const createBody = state.creates.at(-1) || {}
    check(
      '新建检测项提交名称/类型/目标（无 server.read 时目标为空）',
      createBody.name === 'new-http' &&
        createBody.kind === 'http' &&
        createBody.target === 'https://example.com/healthz' &&
        Array.isArray(createBody.server_ids) &&
        createBody.server_ids.length === 0,
      JSON.stringify(createBody),
    )
    await page.waitForSelector('#check-row-c-new', { timeout: 8000 })

    await page.click('#check-edit-c-new')
    await page.waitForSelector('#check-form', { timeout: 5000 })
    await page.fill('#check-form-interval', '90')
    await page.click('#check-form-submit')
    await page.waitForSelector('#check-form', { state: 'detached', timeout: 8000 })
    const editBody = state.patches.at(-1)?.body || {}
    check(
      '编辑检测项提交修改后的间隔',
      state.patches.at(-1)?.id === 'c-new' && editBody.interval_s === 90,
      JSON.stringify(editBody),
    )

    await page.click('#check-delete-c-new')
    await page.waitForSelector('#check-delete-confirm', { timeout: 5000 })
    await page.click('#check-delete-confirm')
    await page.waitForSelector('#check-row-c-new', { state: 'detached', timeout: 8000 })
    check(
      '删除检测项发出 DELETE 且行消失',
      state.deletes.includes('c-new') && (await page.locator('.ls-checkspage__row').count()) === 3,
      JSON.stringify(state.deletes),
    )

    // 16) 无 check.read：路由守卫拦到 /forbidden，且一次 /checks 请求都不发
    stop: {
      legacyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
      const denied = newState(NO_CHECK_PERM)
      await installMockApi(legacyContext, denied)
      const deniedPage = await legacyContext.newPage()
      const deniedCalls = []
      deniedPage.on('request', (request) => {
        if (request.url().includes('/api/v1/')) deniedCalls.push(`${request.method()} ${new URL(request.url()).pathname}`)
      })
      // 登录后默认落在 #/（总览要求 server.read，本用例的 denied 用户有）：首页会预取自己的接口。
      // 先等首页首屏渲染完、记基线，再进被守卫拦截的目标页；首页预取照样留在 deniedCalls 里，
      // 不靠过滤抹掉，之后只数「目标页自己」有没有再发 /checks 请求。
      await loginToHome(deniedPage, base)
      const deniedSince = deniedCalls.length
      await deniedPage.goto(`${base}/#/checks`, { waitUntil: 'domcontentloaded' })
      await deniedPage.waitForSelector('.ls-forbidden__title', { timeout: 15000 })
      // 抖动窗口：守卫放行或页面自行轮询都会在基线之后冒出请求。
      await deniedPage.waitForTimeout(800)
      const deniedHash = new URL(deniedPage.url()).hash
      const prefetchCheckCalls = deniedCalls.slice(0, deniedSince).filter((line) => line.includes('/api/v1/checks'))
      const checkCalls = deniedCalls.slice(deniedSince).filter((line) => line.includes('/api/v1/checks'))
      check('无 check.read 时被挡到 /forbidden', deniedHash.startsWith('#/forbidden'), deniedHash)
      check(
        '无 check.read 时一次 /checks 请求都不发',
        checkCalls.length === 0,
        `目标页 ${checkCalls.length} 次；首页预取 ${deniedSince} 条请求已计入原始记录（其中 /checks ${prefetchCheckCalls.length} 条：${prefetchCheckCalls.join(', ') || '无'}）`,
      )
      check('无 check.read 时列表接口未被调用', denied.listCalls === 0, `${denied.listCalls} 次`)
      await legacyContext.close().catch(() => {})
      legacyContext = null
      break stop
    }

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
    log('api', `${apiCalls.length} 次请求`)
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    if (legacyContext) await legacyContext.close().catch(() => {})
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(
    `\n${failed.length === 0 ? '✓ 检测页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
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
