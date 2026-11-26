#!/usr/bin/env node
/**
 * 发布页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表渲染与进度 / 搜索与状态筛选 / 发起发布（表单必填校验、计划预览、提交后可见） /
 * 详情步骤时间线与日志 / 回滚二次确认（取消不发请求、确认才发） / 中止二次确认 /
 * 无 deploy.write 时写按钮隐藏且 0 写请求 / 320px 无横向滚动 / 无未捕获异常。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-releases.mjs [--headed]
 */
import { dirname, resolve } from 'node:path'
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
  writes,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const TOKEN = 'smoke-password-smoke-token'
const PASSWORD = 'smoke-servers-password'

const APPS = [
  { id: 'app-shop', name: 'shop-web-v2', slug: 'shop-web', root: '/srv/shop-web', deploy_mode: 'release', server_count: 2 },
  { id: 'app-api', name: 'api-service', slug: 'api-service', root: '/srv/api', deploy_mode: 'inplace', server_count: 1 },
]

const TEMPLATES = [
  { id: 'tpl-1', name: '二进制发布', kind: 'binary', content: '#!/bin/sh', created_at: 1, updated_at: 1 },
  { id: 'tpl-2', name: '静态站点', kind: 'static', content: '#!/bin/sh', created_at: 1, updated_at: 1 },
]

const SERVERS = [
  { id: 'srv-alpha', name: 'alpha-node', host: '10.0.0.11', port: 22, mode: 'agent', status: 'online', tags: [] },
  { id: 'srv-beta', name: 'beta-node', host: '10.0.0.12', port: 22, mode: 'agent', status: 'online', tags: [] },
]

function makeReleases() {
  const now = Date.now()
  return [
    {
      id: 'rel-success',
      app_id: 'app-shop',
      template_id: 'tpl-1',
      template_name: '二进制发布',
      version: '1.4.2',
      status: 'success',
      strategy: 'all',
      canary_count: 0,
      hold_seconds: 0,
      concurrency: 2,
      created_at: now - 7_200_000,
      started_at: now - 7_200_000,
      finished_at: now - 7_140_000,
      runs: [
        {
          id: 'run-1',
          release_id: 'rel-success',
          server_id: 'srv-alpha',
          server_name: 'alpha-node',
          status: 'success',
          started_at: now - 7_200_000,
          finished_at: now - 7_140_000,
          message: '',
          failed_step: null,
        },
      ],
    },
    {
      id: 'rel-canary',
      app_id: 'app-shop',
      template_id: 'tpl-1',
      template_name: '二进制发布',
      version: '1.5.0',
      status: 'running',
      strategy: 'canary',
      canary_count: 1,
      hold_seconds: 300,
      concurrency: 1,
      created_at: now - 600_000,
      started_at: now - 600_000,
      runs: [
        {
          id: 'run-2',
          release_id: 'rel-canary',
          server_id: 'srv-alpha',
          server_name: 'alpha-node',
          status: 'success',
          started_at: now - 600_000,
          finished_at: now - 540_000,
          message: '',
          failed_step: null,
        },
        {
          id: 'run-3',
          release_id: 'rel-canary',
          server_id: 'srv-beta',
          server_name: 'beta-node',
          status: 'running',
          started_at: now - 30_000,
          finished_at: null,
          message: '',
          failed_step: null,
        },
      ],
    },
    {
      id: 'rel-failed',
      app_id: 'app-api',
      template_id: 'tpl-1',
      template_name: '二进制发布',
      version: '2.1.0',
      status: 'failed',
      strategy: 'all',
      canary_count: 0,
      hold_seconds: 0,
      concurrency: 1,
      created_at: now - 86_400_000,
      runs: [
        {
          id: 'run-4',
          release_id: 'rel-failed',
          server_id: 'srv-alpha',
          server_name: 'alpha-node',
          status: 'failed',
          started_at: now - 86_400_000,
          finished_at: now - 86_340_000,
          message: 'health check timeout',
          failed_step: 'healthcheck',
        },
      ],
    },
  ]
}

function mockUser(state) {
  const permissions = state.permissions ?? (state.readOnly
    ? ['deploy.read', 'server.read']
    : ['deploy.read', 'deploy.write', 'server.read'])
  return {
    id: 'user-smoke',
    username: state.readOnly ? 'viewer' : 'admin',
    role: state.readOnly ? 'viewer' : 'admin',
    role_name: state.readOnly ? '只读' : '管理员',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

/** 假后端：只覆盖发布域需要的端点，其他一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: mockUser(state) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: mockUser(state) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/apps' && method === 'GET') {
      if (state.appsFail) return json(403, { error: { code: 'forbidden', message: 'forbidden' } })
      return json(200, { data: APPS })
    }
    if (path === '/api/v1/templates' && method === 'GET') {
      return json(200, { data: TEMPLATES })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, { data: SERVERS })
    }
    if (path === '/api/v1/releases' && method === 'GET') {
      state.releaseListCalls += 1
      return json(200, { data: state.releases })
    }
    if (path === '/api/v1/releases/preview' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      return json(200, {
        data: {
          app_id: body.app_id,
          app_slug: APPS.find((app) => app.id === body.app_id)?.slug ?? '',
          version: body.version,
          provider: 'builtin',
          deploy_mode: 'release',
          servers: (body.server_ids ?? []).map((id) => ({
            server_id: id,
            server_name: SERVERS.find((server) => server.id === id)?.name ?? id,
            plan_root: '/srv/shop-web/releases/r1',
            steps: [{ name: 'sync', command: 'rsync', timeout_s: 60 }],
            rollback_steps: [],
            notes: [],
          })),
        },
      })
    }
    if (path === '/api/v1/releases' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      const release = {
        id: `rel-new-${state.releases.length + 1}`,
        app_id: body.app_id,
        template_id: body.template_id,
        template_name: TEMPLATES.find((template) => template.id === body.template_id)?.name ?? '',
        version: body.version,
        status: 'pending',
        strategy: body.strategy ?? 'all',
        canary_count: body.canary_count ?? 0,
        hold_seconds: body.hold_seconds ?? 0,
        concurrency: body.concurrency ?? 1,
        created_at: Date.now(),
        runs: (body.server_ids ?? []).map((id, index) => ({
          id: `run-new-${index + 1}`,
          release_id: 'pending',
          server_id: id,
          server_name: SERVERS.find((server) => server.id === id)?.name ?? id,
          status: 'pending',
          started_at: null,
          finished_at: null,
          message: 'queued',
          failed_step: null,
        })),
      }
      state.releases.unshift(release)
      return json(200, { data: { release, runs: release.runs } })
    }

    const logs = /^\/api\/v1\/releases\/([^/]+)\/runs\/([^/]+)\/logs$/.exec(path)
    if (logs && method === 'GET') {
      return json(200, {
        data: { logs: [{ seq: 1, ts: Date.now(), stream: 'stdout', chunk: 'deploy ok' }] },
      })
    }

    const targets = /^\/api\/v1\/apps\/([^/]+)\/rollback-targets$/.exec(path)
    if (targets && method === 'GET') {
      return json(200, {
        data: [
          {
            release_id: 'rel-old',
            version: '1.3.0',
            created_at: Date.now() - 86_400_000,
            finished_at: Date.now() - 86_340_000,
            server_ids: ['srv-alpha'],
          },
        ],
      })
    }

    const detail = /^\/api\/v1\/releases\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      const found = state.releases.find((item) => item.id === decodeURIComponent(detail[1]))
      if (!found) return json(404, { error: { code: 'not_found' } })
      const { runs, ...release } = found
      return json(200, { data: { release, runs } })
    }

    const action = /^\/api\/v1\/releases\/([^/]+)\/(retry|rollback|cancel)$/.exec(path)
    if (action && method === 'POST') {
      const id = decodeURIComponent(action[1])
      state.actions.push(`${action[2]} ${id}`)
      const found = state.releases.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found' } })
      if (action[2] === 'cancel') return route.fulfill({ status: 204, body: '' })
      return json(200, { data: { release: found, runs: found.runs } })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

async function run() {
  const { dist, ready } = resolveDist(WEB_ROOT, flag)
  if (!ready) {
    console.error(`✗ 没有构建产物：${resolve(dist, 'index.html')}\n  先跑：pnpm --dir web-next run build`)
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
  const { server, base } = await startStaticServer(dist, Number(flag('port', 0)) || 0)
  log('serve', `${dist} → ${base}`)

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
  const state = {
    releases: makeReleases(),
    actions: [],
    releaseListCalls: 0,
    readOnly: false,
    permissions: null,
    appsFail: false,
  }

  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)
    const posts = () => writes(calls).filter((call) => call.method === 'POST')

    await login(page, base, { password: PASSWORD })
    // 登录后默认落在 #/：首页总览会预取 /apps 等列表（照样留在 calls 里，不靠过滤抹掉）。
    // 等首页首屏（总览标题）渲染完记基线，之后只数发布列表页自己发的 /apps。
    await page.waitForSelector('.ls-adminpage__title', { timeout: 15000 })
    const callsSince = calls.length
    await page.goto(`${base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#releases-title', { timeout: 15000 })

    // 1) 列表渲染：三行 + 状态 + 进度 + 灰度徽标
    await page.waitForSelector('.ls-deploy__row[data-release-id="rel-canary"]', { timeout: 15000 })
    check('发布列表渲染三行', (await page.locator('.ls-deploy__row').count()) === 3)
    const canaryRow = await page.locator('.ls-deploy__row[data-release-id="rel-canary"]').innerText()
    check('运行中发布显示状态徽标', canaryRow.includes('发布中'))
    check('灰度发布显示 canary 进度', canaryRow.includes('灰度 1/2'))
    const failedRow = await page.locator('.ls-deploy__row[data-release-id="rel-failed"]').innerText()
    check('失败发布透出失败计数', failedRow.includes('失败'))

    // 1b) 应用列：列表接口只给 app_id，页面须用一次 /apps 的映射显示应用名
    await page.waitForFunction(
      () => (document.querySelector('.ls-deploy__row[data-release-id="rel-success"]')?.textContent ?? '').includes('shop-web-v2'),
      null,
      { timeout: 5000 },
    )
    const shopRow = await page.locator('.ls-deploy__row[data-release-id="rel-success"]').innerText()
    const apiRow = await page.locator('.ls-deploy__row[data-release-id="rel-failed"]').innerText()
    check(
      '列表应用列显示应用名而非 app_id',
      shopRow.includes('shop-web-v2') &&
        !shopRow.includes('app-shop') &&
        apiRow.includes('api-service') &&
        !apiRow.includes('app-api'),
      `${shopRow.replace(/\s+/g, ' ')} | ${apiRow.replace(/\s+/g, ' ')}`,
    )
    const listText = await page.locator('body').innerText()
    check('列表页不出现 app_id 形态文本', !/app-shop|app-api/.test(listText))
    const appsGets = (list) => list.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps')
    // 抖动窗口：行渲染完再等一拍，任何轮询/按行补请求都会把增量推成 >1。
    await page.waitForTimeout(800)
    const appListGetsSettled = appsGets(calls.slice(callsSince))
    check(
      '列表只拉一次应用列表（不按行请求）',
      appListGetsSettled.length === 1,
      `目标页 ${appListGetsSettled.length} 次（首页预取 ${appsGets(calls.slice(0, callsSince)).length} 次，总计 ${calls.length} 条）`,
    )

    // 2) 搜索与状态筛选
    await page.fill('#releases-search', '2.1.0')
    await page.waitForTimeout(150)
    check('按版本搜索只剩一行', (await page.locator('.ls-deploy__row').count()) === 1)
    await page.fill('#releases-search', '')
    await page.selectOption('#releases-status', 'failed')
    await page.waitForTimeout(150)
    check('按状态筛选只剩失败行', (await page.locator('.ls-deploy__row').count()) === 1)
    check('筛选后出现清空入口', (await page.locator('#releases-filter-reset').count()) === 1)
    await page.click('#releases-filter-reset')
    await page.waitForTimeout(150)
    check('清空筛选恢复三行', (await page.locator('.ls-deploy__row').count()) === 3)

    // 3) 发起发布：必填校验 → 预览 → 提交 → 列表可见
    await page.click('#releases-new')
    await page.waitForSelector('[data-testid="release-form-dialog"]', { timeout: 5000 })
    await page.waitForFunction(
      () => document.querySelectorAll('#release-form-template option').length > 1,
      null,
      { timeout: 5000 },
    )
    check('打开弹窗后按需拉到模板与服务器', (await page.locator('#release-form-template option').count()) === 3)
    await page.click('#release-form-submit')
    await page.waitForTimeout(150)
    check('空表单出现字段错误', (await page.locator('[data-testid="release-form-dialog"] .ls-field__error').count()) >= 2)
    check('空表单不发 POST /releases', posts().length === 0, `${posts().length} 次`)

    await page.selectOption('#release-form-app', 'app-shop')
    await page.fill('#release-form-version', '2.0.0')
    await page.selectOption('#release-form-template', 'tpl-1')
    await page.locator('[data-testid="release-targets"] input[type="checkbox"]').first().check()
    await page.click('#release-form-preview')
    await page.waitForSelector('[data-testid="release-preview"]', { timeout: 5000 })
    const previewText = await page.locator('[data-testid="release-preview"]').innerText()
    check('发布计划预览展示目标与步数', previewText.includes('2.0.0') && previewText.includes('1 步'), previewText.replace(/\s+/g, ' '))

    await page.click('#release-form-submit')
    await page.waitForSelector('[data-testid="release-form-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(300)
    check('提交发出 1 次 POST /releases', posts().filter((call) => call.path === '/api/v1/releases').length === 1)
    check('新发布出现在列表', (await page.locator('.ls-deploy__row').count()) === 4)
    const createdNotice = await page.locator('.ls-ok-hint').innerText()
    check(
      '创建成功提示展示应用名而非 app_id',
      createdNotice.includes('shop-web-v2') && !createdNotice.includes('app-shop'),
      createdNotice.replace(/\s+/g, ' '),
    )

    // 4) 详情：步骤时间线 + 日志按需展开
    await page.goto(`${base}/#/releases/rel-success`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('[data-testid="release-timeline"]', { timeout: 15000 })
    check('详情渲染步骤时间线', (await page.locator('.ls-timeline__item').count()) === 1)
    const successTitle = await page.locator('#release-detail-title').innerText()
    check(
      '详情标题展示应用名而非 app_id',
      successTitle.includes('shop-web-v2') && !successTitle.includes('app-shop'),
      successTitle.replace(/\s+/g, ' '),
    )
    check(
      '时间线按服务器展示成功态',
      (await page.locator('[data-run-server="srv-alpha"]').innerText()).includes('alpha-node'),
    )
    check('统计区展示成功/失败/进行中', (await page.locator('.ls-deploy__stats').innerText()).includes('成功'))
    await page.locator('.ls-timeline__item .ls-btn').first().click()
    await page.waitForSelector('.ls-timeline__pre', { timeout: 5000 })
    check('查看日志按钮按需拉取并渲染', (await page.locator('.ls-timeline__pre').innerText()).includes('deploy ok'))

    // 5) 回滚二次确认：取消不发请求，确认才发
    const rollbackPosts = () => state.actions.filter((item) => item.startsWith('rollback')).length
    await page.locator('header .ls-btn', { hasText: '回滚' }).first().click()
    await page.waitForSelector('[data-testid="rollback-dialog"]', { timeout: 5000 })
    await page.waitForSelector('[data-testid="rollback-dialog"] input[type="radio"]', { timeout: 5000 })
    check('回滚弹窗列出可回滚版本', (await page.locator('[data-testid="rollback-dialog"] input[type="radio"]').count()) === 1)
    await page.locator('[data-testid="rollback-dialog"] input[type="radio"]').first().check()
    const rollbackText = await page.locator('[data-testid="rollback-dialog"]').innerText()
    check(
      '回滚弹窗写明应用名/目标版本/影响服务器',
      rollbackText.includes('shop-web') && rollbackText.includes('1.3.0') && rollbackText.includes('1 台'),
      rollbackText.replace(/\s+/g, ' ').slice(0, 80),
    )
    await page.locator('[data-testid="rollback-dialog"] .ls-modal__foot .ls-btn:not(.ls-btn--danger)').click()
    await page.waitForSelector('[data-testid="rollback-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(200)
    check('取消回滚不发请求', rollbackPosts() === 0, `${rollbackPosts()} 次`)
    await page.locator('header .ls-btn', { hasText: '回滚' }).first().click()
    await page.waitForSelector('[data-testid="rollback-dialog"] input[type="radio"]', { timeout: 5000 })
    await page.locator('[data-testid="rollback-dialog"] input[type="radio"]').first().check()
    await page.locator('[data-testid="rollback-dialog"] .ls-modal__foot .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="rollback-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(200)
    check('确认回滚发出一次请求', rollbackPosts() === 1, `${rollbackPosts()} 次`)

    // 6) 详情只给 app_id 时也能用 api.apps() 兜底出应用名
    await page.goto(`${base}/#/releases/rel-failed`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('[data-testid="release-timeline"]', { timeout: 15000 })
    const fallbackTitle = await page.locator('#release-detail-title').innerText()
    check(
      '详情只给 app_id 时仍显示应用名（api.apps 兜底）',
      fallbackTitle.includes('api-service') && !fallbackTitle.includes('app-api'),
      fallbackTitle.replace(/\s+/g, ' '),
    )

    // 7) 中止进行中的发布：二次确认 + 取消不中止
    await page.goto(`${base}/#/releases/rel-canary`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('[data-testid="release-timeline"]', { timeout: 15000 })
    check('灰度发布时间线两台服务器', (await page.locator('.ls-timeline__item').count()) === 2)
    await page.locator('header .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="release-cancel-dialog"]', { timeout: 5000 })
    check(
      '中止弹窗写明影响台数',
      (await page.locator('[data-testid="release-cancel-dialog"]').innerText()).includes('1'),
    )
    await page.locator('[data-testid="release-cancel-dialog"] .ls-modal__foot .ls-btn:not(.ls-btn--danger)').click()
    await page.waitForSelector('[data-testid="release-cancel-dialog"]', { state: 'detached', timeout: 5000 })
    check('取消中止不发请求', state.actions.filter((item) => item.startsWith('cancel')).length === 0)
    await page.locator('header .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="release-cancel-dialog"]', { timeout: 5000 })
    await page.locator('[data-testid="release-cancel-dialog"] .ls-modal__foot .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="release-cancel-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(200)
    check(
      '确认中止发出一次请求',
      state.actions.filter((item) => item.startsWith('cancel')).length === 1,
      state.actions.join(','),
    )

    // 8) 320px：列表页与详情页都不横向滚动
    await page.setViewportSize({ width: 320, height: 800 })
    await page.goto(`${base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-deploy__row', { timeout: 15000 })
    await page.waitForTimeout(200)
    const listOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    check('320px 发布列表无横向滚动', listOverflow <= 1, `溢出 ${listOverflow}px`)
    await page.goto(`${base}/#/releases/rel-canary`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('[data-testid="release-timeline"]', { timeout: 15000 })
    await page.waitForTimeout(200)
    const detailOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    check('320px 发布详情无横向滚动', detailOverflow <= 1, `溢出 ${detailOverflow}px`)
    await page.setViewportSize({ width: 1280, height: 900 })

    // 9) 无 deploy.write：写按钮隐藏且 0 写请求
    state.readOnly = true
    const viewerContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(viewerContext, state)
    const viewer = await viewerContext.newPage()
    viewer.on('pageerror', (error) => pageErrors.push(error.message))
    const viewerCalls = watchApiCalls(viewer)
    await login(viewer, base, { username: 'viewer', password: PASSWORD })
    await viewer.goto(`${base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await viewer.waitForSelector('.ls-deploy__row', { timeout: 15000 })
    check('只读账号能看到发布列表', (await viewer.locator('.ls-deploy__row').count()) >= 3)
    check('只读账号隐藏发起发布按钮', (await viewer.locator('#releases-new').count()) === 0)
    check('只读账号 0 写请求', writes(viewerCalls).length === 0, `${writes(viewerCalls).length} 次`)
    await viewerContext.close()

    // 10) 权限一致性：详情页与列表页同点 deploy.read；仅 server.read 不得绕过
    state.permissions = ['deploy.read']
    const deployOnlyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(deployOnlyContext, state)
    const deployOnly = await deployOnlyContext.newPage()
    deployOnly.on('pageerror', (error) => pageErrors.push(error.message))
    const deployOnlyCalls = watchApiCalls(deployOnly)
    await login(deployOnly, base, { username: 'deploy-only', password: PASSWORD })
    await deployOnly.goto(`${base}/#/releases/rel-success`, { waitUntil: 'domcontentloaded' })
    await deployOnly.waitForSelector('[data-testid="release-timeline"]', { timeout: 15000 })
    await deployOnly.waitForFunction(
      () => (document.querySelector('#release-detail-title')?.textContent ?? '').includes('1.4.2'),
      null,
      { timeout: 15000 },
    )
    check('仅 deploy.read 可进发布详情（未被重定向）', !deployOnly.url().includes('/forbidden'), deployOnly.url())
    check(
      '仅 deploy.read 发布详情渲染版本',
      (await deployOnly.locator('#release-detail-title').innerText()).includes('1.4.2'),
    )
    const releaseDetailGets = deployOnlyCalls.filter(
      (call) => call.method === 'GET' && call.path === '/api/v1/releases/rel-success',
    )
    check(
      '仅 deploy.read 发布详情发出 1 次 GET /releases/rel-success',
      releaseDetailGets.length === 1,
      `${releaseDetailGets.length} 次`,
    )
    await deployOnlyContext.close()

    state.permissions = ['server.read']
    const serverOnlyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(serverOnlyContext, state)
    const serverOnly = await serverOnlyContext.newPage()
    serverOnly.on('pageerror', (error) => pageErrors.push(error.message))
    const serverOnlyCalls = watchApiCalls(serverOnly)
    await login(serverOnly, base, { username: 'server-only', password: PASSWORD })
    await serverOnly.goto(`${base}/#/releases/rel-success`, { waitUntil: 'domcontentloaded' })
    await serverOnly.waitForFunction(() => window.location.hash.startsWith('#/forbidden'), null, { timeout: 15000 })
    check('仅 server.read 进发布详情被重定向到 /forbidden', serverOnly.url().includes('/forbidden'), serverOnly.url())
    check(
      '仅 server.read 不请求 GET /releases/rel-success',
      serverOnlyCalls.every((call) => !(call.method === 'GET' && call.path === '/api/v1/releases/rel-success')),
      serverOnlyCalls.map((call) => call.path).join(','),
    )
    await serverOnlyContext.close()
    state.permissions = null

    // 11) 应用列表不可读（如仅 deploy.read）：列表主流程不受影响，应用列回退 app_id
    state.appsFail = true
    const noAppsContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(noAppsContext, state)
    const noApps = await noAppsContext.newPage()
    noApps.on('pageerror', (error) => pageErrors.push(error.message))
    await login(noApps, base, { username: 'deploy-only', password: PASSWORD })
    await noApps.goto(`${base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await noApps.waitForSelector('.ls-deploy__row[data-release-id="rel-success"]', { timeout: 15000 })
    check('应用列表不可读时发布列表仍渲染', (await noApps.locator('.ls-deploy__row').count()) === 4)
    const noAppsRow = await noApps.locator('.ls-deploy__row[data-release-id="rel-success"]').innerText()
    check('应用列表不可读时应用列回退 app_id', noAppsRow.includes('app-shop'), noAppsRow.replace(/\s+/g, ' '))
    check(
      '应用列表不可读时列表不显示加载失败',
      (await noApps.locator('.ls-notice--error').count()) === 0,
    )
    await noAppsContext.close()
    state.appsFail = false

    check('无未捕获异常', pageErrors.length === 0, pageErrors.join(' / ').slice(0, 160))
    await context.close()
  } catch (error) {
    console.error(`✗ 冒烟中断：${error && error.stack ? error.stack.split('\n').slice(0, 4).join('\n') : error}`)
    process.exitCode = 1
  } finally {
    await browser.close()
    await new Promise((done) => server.close(done))
  }

  if (process.exitCode !== 1) process.exitCode = finish()
}

await run()
