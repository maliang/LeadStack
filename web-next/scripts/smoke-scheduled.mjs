#!/usr/bin/env node
/**
 * 定时任务页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表渲染与空态 / 新建必填校验（名称→cron→命令→目标）/ 新建提交 payload /
 * 编辑回填与 PATCH / 删除二次确认、取消不删、确认删除 / 立即运行 /
 * 运行历史三态与 stdout、stderr / 无 task.write 时写按钮全隐藏且 0 写请求 /
 * 320px 列表与弹窗无横向滚动 / 无未捕获异常。
 * 用法：node scripts/smoke-scheduled.mjs [--headed] [--dist dist] [--port 0]
 */
import { existsSync } from 'node:fs'
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
  writes,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const REQUESTED_PORT = Number(flag('port', 0)) || 0
const { dist: DIST, ready: DIST_READY } = resolveDist(WEB_ROOT, flag)

const SERVERS = [
  { id: 'srv-1', name: 'web-01', host: '10.0.0.11' },
  { id: 'srv-2', name: 'db-01', host: '10.0.0.12' },
]

function taskFixture(overrides) {
  return {
    id: 't-placeholder',
    name: 'placeholder',
    cron: '0 3 * * *',
    command: 'true',
    timeout_s: 60,
    enabled: true,
    concurrency: 4,
    created_at: 1_760_000_000,
    last_run_at: null,
    next_run_at: null,
    server_ids: ['srv-1'],
    ...overrides,
  }
}

const TASKS = [
  taskFixture({
    id: 't-1',
    name: 'nightly-backup',
    cron: '0 3 * * *',
    command: 'tar -czf /tmp/backup.tgz /srv',
    last_run_at: 1_763_000_000,
    next_run_at: 1_763_100_000,
    server_ids: ['srv-1'],
  }),
  taskFixture({
    id: 't-2',
    name: 'disk-check',
    cron: '*/5 * * * *',
    command: 'df -h',
    enabled: false,
    server_ids: ['srv-1', 'srv-2'],
  }),
]

const RUNS = [
  {
    id: 11,
    task_id: 't-1',
    server_id: 'srv-1',
    ts: 1_763_000_000,
    status: 'success',
    exit_code: 0,
    duration_ms: 1830,
    stdout: 'backup written to /tmp/backup.tgz',
    stderr: null,
    message: null,
  },
  {
    id: 12,
    task_id: 't-1',
    server_id: 'srv-1',
    ts: 1_762_999_000,
    status: 'failed',
    exit_code: 1,
    duration_ms: 420,
    stdout: null,
    stderr: 'tar: /srv: Cannot open: Permission denied',
    message: 'exit 1',
  },
]

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

async function installMockApi(context, state) {
  const currentUser = () => ({
    id: 'u-admin',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: state.permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  })

  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const noContent = () => route.fulfill({ status: 204, body: '' })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: '{{SECRET_mock_token}}', user: currentUser() })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: currentUser() })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, { data: state.servers.map((item) => ({ ...item })) })
    }
    if (path === '/api/v1/scheduled-tasks' && method === 'GET') {
      return json(200, { data: state.tasks.map((item) => ({ ...item })) })
    }
    if (path === '/api/v1/scheduled-tasks' && method === 'POST') {
      const body = request.postDataJSON() || {}
      state.createCalls.push(body)
      const created = taskFixture({ ...body, id: `t-new-${state.createCalls.length}` })
      state.tasks.push(created)
      return json(200, { data: { ...created } })
    }

    const detail = /^\/api\/v1\/scheduled-tasks\/([^/]+)$/.exec(path)
    if (detail && method === 'PATCH') {
      const id = decodeURIComponent(detail[1])
      const body = request.postDataJSON() || {}
      state.patchCalls.push({ id, body })
      const found = state.tasks.find((item) => item.id === id)
      if (!found) return json(404, { error: { code: 'not_found', message: '任务不存在' } })
      Object.assign(found, body)
      return json(200, { data: { ...found } })
    }
    if (detail && method === 'DELETE') {
      const id = decodeURIComponent(detail[1])
      state.deleteCalls.push(id)
      state.tasks = state.tasks.filter((item) => item.id !== id)
      return noContent()
    }

    const run = /^\/api\/v1\/scheduled-tasks\/([^/]+)\/run$/.exec(path)
    if (run && method === 'POST') {
      const id = decodeURIComponent(run[1])
      state.runCalls.push(id)
      return json(200, { data: { id, status: 'pending' } })
    }
    const runs = /^\/api\/v1\/scheduled-tasks\/([^/]+)\/runs$/.exec(path)
    if (runs && method === 'GET') {
      state.runsCalls += 1
      return json(200, { data: state.runs.filter((item) => item.task_id === decodeURIComponent(runs[1])) })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
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

  const baseline = TASKS.map((item) => ({ ...item }))
  const state = {
    permissions: ['task.read', 'task.write', 'server.read'],
    tasks: TASKS.map((item) => ({ ...item })),
    servers: SERVERS.map((item) => ({ ...item })),
    runs: RUNS.map((item) => ({ ...item })),
    createCalls: [],
    patchCalls: [],
    deleteCalls: [],
    runCalls: [],
    runsCalls: 0,
  }

  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const apiCalls = watchApiCalls(page)

  try {
    // 1) 登录并进入定时任务页
    await login(page, base)
    await page.goto(`${base}/#/scheduled-tasks`, { waitUntil: 'domcontentloaded' })
    const heading = await readHeading(page, '定时任务')
    await page.waitForSelector('#scheduled-refresh', { timeout: 15000 })
    await page.waitForSelector('.ls-scheduledpage__row', { timeout: 15000 })
    check('进入定时任务页并渲染标题', heading === '定时任务', heading)
    check('列表渲染 2 行', (await page.locator('.ls-scheduledpage__row').count()) === 2)
    check(
      '首行展示名称与命令',
      (await page.locator('[data-task-id="t-1"] .ls-adminpage__primary').innerText()).trim() === 'nightly-backup' &&
        (await page.locator('[data-task-id="t-1"] .ls-scheduledpage__command').innerText()).includes('tar -czf'),
    )
    check(
      '目标列把 server_ids 映射为服务器名',
      (await page.locator('[data-task-id="t-1"] .ls-scheduledpage__targets').innerText()).trim() === 'web-01',
    )
    check(
      '停用任务显示停用文案',
      (await page.locator('[data-task-id="t-2"] .ls-scheduledpage__toggle').innerText()).trim() === '停用',
    )

    // 2) 新建：必填校验按名称→cron→命令→目标的顺序逐项拦截
    await page.click('#scheduled-create-open')
    await page.waitForSelector('#scheduled-form', { timeout: 5000 })
    await page.click('#scheduled-save')
    check(
      '空表单先提示任务名称必填',
      (await page.locator('#scheduled-form-error').innerText()).trim() === '请填写任务名称',
      await page.locator('#scheduled-form-error').innerText(),
    )
    await page.fill('#scheduled-name', 'weekly-report')
    await page.fill('#scheduled-cron', '')
    await page.click('#scheduled-save')
    check(
      'cron 留空非法时提示 cron',
      (await page.locator('#scheduled-form-error').innerText()).trim() === '请填写合法的 cron 表达式',
    )
    await page.fill('#scheduled-cron', '0 3 * * *')
    await page.click('#scheduled-save')
    check(
      '命令为空时提示命令必填',
      (await page.locator('#scheduled-form-error').innerText()).trim() === '请填写要执行的命令',
    )
    await page.fill('#scheduled-command', 'echo weekly')
    await page.click('#scheduled-save')
    check(
      '未选目标时提示至少一台服务器',
      (await page.locator('#scheduled-form-error').innerText()).trim() === '请至少选择一台目标服务器',
    )
    check('校验失败期间未发出写请求', state.createCalls.length === 0 && state.patchCalls.length === 0)

    await page.locator('.ls-scheduledpage__server input').first().check()
    await page.click('#scheduled-save')
    await page.waitForSelector('#scheduled-form', { state: 'detached', timeout: 8000 })
    await page.waitForSelector('[data-task-id="t-new-1"]', { timeout: 8000 })
    const created = state.createCalls[0] || {}
    check('新建写入一次 POST /scheduled-tasks', state.createCalls.length === 1, `${state.createCalls.length} 次`)
    check(
      '新建 payload 字段与默认值正确',
      created.name === 'weekly-report' &&
        created.cron === '0 3 * * *' &&
        created.command === 'echo weekly' &&
        created.timeout_s === 60 &&
        created.concurrency === 4 &&
        created.enabled === true &&
        Array.isArray(created.server_ids) &&
        created.server_ids.length === 1 &&
        created.server_ids[0] === 'srv-1',
      JSON.stringify(created),
    )

    // 3) 编辑：回填后改名称，PATCH 只带该 id
    await page.click('[data-task-id="t-1"] .ls-scheduledpage__edit')
    await page.waitForSelector('#scheduled-form', { timeout: 5000 })
    check('编辑表单回填既有名称', (await page.inputValue('#scheduled-name')) === 'nightly-backup')
    check('编辑表单回填目标服务器', (await page.locator('.ls-scheduledpage__server input:checked').count()) === 1)
    await page.fill('#scheduled-name', 'nightly-backup-v2')
    await page.click('#scheduled-save')
    await page.waitForSelector('#scheduled-form', { state: 'detached', timeout: 8000 })
    await page.waitForFunction(
      () => (document.querySelector('[data-task-id="t-1"] .ls-adminpage__primary')?.textContent || '').includes('v2'),
      undefined,
      { timeout: 8000 },
    )
    check(
      '编辑发出 PATCH 且只带目标 id',
      state.patchCalls.length === 1 && state.patchCalls[0].id === 't-1',
      JSON.stringify(state.patchCalls.map((call) => call.id)),
    )
    check('编辑 payload 带上新名称', state.patchCalls[0]?.body?.name === 'nightly-backup-v2')

    // 4) 删除：二次确认弹窗展示影响；取消不发请求；确认后行消失
    await page.click('[data-task-id="t-2"] .ls-scheduledpage__delete')
    await page.waitForSelector('#scheduled-delete-confirm', { timeout: 5000 })
    check(
      '删除弹窗展示影响范围说明',
      (await page.locator('#scheduled-delete').innerText()).includes('不再触发'),
      await page.locator('#scheduled-delete').innerText(),
    )
    await page.click('#scheduled-delete-cancel')
    await page.waitForSelector('#scheduled-delete-confirm', { state: 'detached', timeout: 5000 })
    check('取消删除不发 DELETE 请求', state.deleteCalls.length === 0, state.deleteCalls.join(','))
    check('取消删除后目标行仍在', (await page.locator('[data-task-id="t-2"]').count()) === 1)

    await page.click('[data-task-id="t-2"] .ls-scheduledpage__delete')
    await page.waitForSelector('#scheduled-delete-confirm', { timeout: 5000 })
    await page.click('#scheduled-delete-confirm')
    await page.waitForSelector('[data-task-id="t-2"]', { state: 'detached', timeout: 8000 })
    check('确认删除发出 DELETE 请求', state.deleteCalls[0] === 't-2', state.deleteCalls.join(','))

    // 5) 立即运行
    await page.click('[data-task-id="t-1"] .ls-scheduledpage__run')
    await page.waitForFunction(() => document.querySelectorAll('#scheduled-action-notice').length === 1, undefined, {
      timeout: 8000,
    })
    check('立即运行调用 /run 并给出提示', state.runCalls[0] === 't-1' && (await page.locator('#scheduled-action-notice').count()) === 1)

    // 6) 运行历史：成功与失败各一条，stdout/stderr 可见
    await page.click('[data-task-id="t-1"] .ls-scheduledpage__history')
    await page.waitForSelector('#scheduled-runs', { timeout: 5000 })
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-scheduledpage__run-item').length === 2,
      undefined,
      { timeout: 8000 },
    )
    const runText = await page.locator('#scheduled-runs').innerText()
    check('历史弹窗渲染 2 条运行记录', (await page.locator('.ls-scheduledpage__run-item').count()) === 2)
    check('历史记录本地化显示成功/失败状态', runText.includes('成功') && runText.includes('失败'), runText.slice(0, 80))
    check(
      '失败记录逐字回显 stderr 与退出码',
      runText.includes('Permission denied') && runText.includes('退出码 1'),
      runText.slice(0, 120),
    )
    await page.keyboard.press('Escape')
    await page.waitForSelector('#scheduled-runs', { state: 'detached', timeout: 5000 })

    // 7) 空态：refresh 后 0 条走空态，不渲染表格
    state.tasks = []
    await page.click('#scheduled-refresh')
    await page.waitForSelector('#scheduled-empty', { timeout: 8000 })
    check('无任务时显示空态', (await page.locator('#scheduled-empty').count()) === 1)
    check('空态下不渲染任务行', (await page.locator('.ls-scheduledpage__row').count()) === 0)

    state.tasks = baseline.map((item) => ({ ...item }))
    await page.click('#scheduled-refresh')
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-scheduledpage__row').length === 2,
      undefined,
      { timeout: 8000 },
    )

    // 8) 320px：列表与新建弹窗都不得横向溢出，主按钮命中区足够
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForFunction(
      () =>
        Boolean(document.querySelector('.ls-scheduledpage')) &&
        document.querySelectorAll('.ls-scheduledpage__row').length > 0,
      undefined,
      { timeout: 8000 },
    )
    const narrow = await page.evaluate(() => ({
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      createVisible: (() => {
        const button = document.querySelector('#scheduled-create-open')
        if (!button) return false
        const rect = button.getBoundingClientRect()
        return rect.width > 0 && rect.height >= 24 && rect.right <= window.innerWidth + 1
      })(),
    }))
    check('320px 列表无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 新建按钮可见且命中区足够', narrow.createVisible)

    await page.click('#scheduled-create-open')
    await page.waitForSelector('#scheduled-form', { timeout: 5000 })
    const modalOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    check('320px 新建弹窗无横向滚动', modalOverflow <= 0, `溢出 ${modalOverflow}px`)
    await page.keyboard.press('Escape')
    await page.waitForSelector('#scheduled-form', { state: 'detached', timeout: 5000 })

    // 9) 无 task.write：写入口全部隐藏，重载页面 0 写请求
    const writesBefore = writes(apiCalls).length
    const readsBefore = apiCalls.length
    state.permissions = ['task.read', 'server.read']
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await readHeading(page, '定时任务')
    await page.waitForSelector('.ls-scheduledpage__row', { timeout: 15000 })
    await page.waitForSelector('#scheduled-readonly', { timeout: 8000 })
    check(
      '无 task.write 时只读提示可见',
      (await page.locator('#scheduled-readonly').innerText()).includes('task.write'),
      await page.locator('#scheduled-readonly').innerText(),
    )
    check('无 task.write 时新建按钮隐藏', (await page.locator('#scheduled-create-open').count()) === 0)
    check(
      '无 task.write 时编辑/删除/立即运行按钮全部隐藏',
      (await page.locator('.ls-scheduledpage__edit').count()) === 0 &&
        (await page.locator('.ls-scheduledpage__delete').count()) === 0 &&
        (await page.locator('.ls-scheduledpage__run').count()) === 0,
    )
    check('无 task.write 时启用开关不渲染', (await page.locator('.ls-scheduledpage__toggle').count()) === 0)
    check('无 task.write 时仍可查看运行历史', (await page.locator('.ls-scheduledpage__history').count()) === 2)
    check(
      '无权限重载后没有新增写请求',
      writes(apiCalls).length === writesBefore,
      `${writesBefore} → ${writes(apiCalls).length}`,
    )
    check('无 task.write 时仍会请求只读列表', apiCalls.length > readsBefore, `${readsBefore} → ${apiCalls.length}`)

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
