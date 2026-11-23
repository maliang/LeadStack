#!/usr/bin/env node
/**
 * 「版本与升级」面板 Hermetic 冒烟（本地静态服务 + Playwright context.route 内存 mock，不碰真后端）。
 *
 * 覆盖 web-next/src/components/system/UpgradeCard.vue：
 *   ① 无 system.manage：整张升级卡不存在，且 /api/v1/system/update* 请求数 0；
 *   ② admin 首屏 GET /system/update 恰 1 次；点「立即检查更新」后 update 请求 1→2（新增 POST /system/update/check）；
 *   ③ 有更新：current/latest、发布时间、说明、迁移徽标、「立即升级」可点；无更新：「已是最新」且不可点；
 *   ④ upgradable=false + upgrade_note：原文展示、一键升级禁用，但本地上传 input 仍可交互；
 *   ⑤ 检查失败（info.error 非空）：错误原文展示，上传仍可用；
 *   ⑥ 一键升级：2s 轮询 downloading → backing_up → replacing → restarting → success，阶段文案跟随，终态停轮询；
 *   ⑦ rolled_back：显示「已自动回退到升级前版本」；⑧ stalled：中断文案 + 「重新升级」按钮 + 停止轮询；
 *   ⑨ 上传 1024 字节：accept 保留 * / * 且提示「解压后」二进制、multipart POST /system/update/upload、显示进度百分比、202 后切同一套 job 轮询；
 *   ⑩ 201 MiB 假文件：前端直接拦下、0 请求；⑪ 320px 无横向滚动（先等根类就绪）；⑫ 全程 pageerror 0；
 *   ⑬⑭ 重启后 status 无 job（只剩 history）：success 要停表 + 刷新当前版本 + 释放上传，rollback 要显示自动回退并停表。
 *   ⑮ 重启后 status 无 job 且无 history、无 pending_state：无证据不得显示「升级成功」，版本与 idle 状态保持原样。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-upgrade.mjs [--headed] [--shots dir]
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
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const SHOTS = typeof flag('shots', null) === 'string' ? resolve(String(flag('shots'))) : null

const TOKEN = '{{SECRET_xpfxtgql}}'
const PASSWORD = '{{SECRET_pn4deyln}}'
const NOW = Date.now()

const ALL_PERMISSIONS = [
  'server.read',
  'server.write',
  'group.write',
  'token.manage',
  'audit.read',
  'backup.read',
  'backup.write',
  'backup.delete',
  'system.manage',
  'user.manage',
  'role.manage',
]

/** 缺少 system.manage：设置页仍可进，但升级卡必须整块不渲染。 */
const LIMITED_PERMISSIONS = ['server.read', 'audit.read']

function userFixture(permissions) {
  return {
    id: 'u-admin',
    username: 'admin',
    role: permissions.includes('system.manage') ? 'admin' : 'operator',
    role_name: permissions.includes('system.manage') ? '管理员' : '运维',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

function infoFixture(overrides = {}) {
  return {
    current: '1.8.0',
    latest: '2.0.0',
    has_update: true,
    published_at: NOW - 3_600_000,
    release_url: 'https://example.com/releases/2.0.0',
    notes: '## 更新内容\n\n* 新增离线升级包上传\n* 升级前自动备份当前版本并支持失败回退\n\n> 这是引用说明\n\n```sh\ncurl -fsSL https://example.com/install.sh | sh\n```\n',
    schema_changed: true,
    assets: [],
    upgradable: true,
    upgrade_note: null,
    checked_at: NOW,
    error: null,
    upgrade_enabled: true,
    check_interval: '24h',
    ...overrides,
  }
}

function statusFixture(overrides = {}) {
  return {
    job_id: 'job-1',
    phase: 'idle',
    target_version: '2.0.0',
    current_version: null,
    message: null,
    error: null,
    rolled_back: false,
    backup_file: null,
    progress: null,
    started_at: null,
    finished_at: null,
    stalled: false,
    phase_started_at: null,
    phase_elapsed_s: null,
    ...overrides,
  }
}

function makeState(permissions = ALL_PERMISSIONS) {
  return {
    permissions,
    info: infoFixture(),
    /** POST /system/update/check 若设置则返回它（模拟「检查失败但 HTTP 200 + info.error」）。 */
    checkInfo: null,
    currentVersion: '1.8.0',
    getCalls: 0,
    checkCalls: 0,
    statusCalls: 0,
    applyCalls: [],
    uploadCalls: [],
    /** 上传 mock 的应答延迟：留出窗口观察「上传中 n%」进度态。 */
    uploadDelay: 0,
    /** 依次回放的 job 快照；最后一项会一直重复，模拟 job 卡在某个阶段。 */
    statusQueue: [statusFixture({ phase: 'idle' })],
  }
}

/** 把一串阶段压进 state，pollStatus 按顺序取。 */
function queueStatus(state, list, currentVersion = '1.8.0') {
  state.statusQueue = list.map((item) => statusFixture(item))
  state.currentVersion = currentVersion
}

/** 假后端：只覆盖升级面板与设置页用到的端点，其余一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const noContent = () => route.fulfill({ status: 204, body: '' })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: userFixture(state.permissions) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: userFixture(state.permissions) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }

    /* 设置页其它卡片依赖的端点（缺失会让页面进失败态，干扰断言）。 */
    if (path === '/api/v1/system/info' && method === 'GET') {
      return json(200, { data: { version: '1.8.0', uptime_s: 3_600, server_count: 1, online_count: 1 } })
    }
    if (path === '/api/v1/system/network' && method === 'GET') {
      return json(200, {
        data: {
          listen: '127.0.0.1:8443',
          active_listen: '127.0.0.1:8443',
          public_url: '',
          default_ssh_port: 22,
          listen_source: 'default',
        },
      })
    }

    /* ---------------- 升级 ---------------- */
    if (path === '/api/v1/system/update' && method === 'GET') {
      state.getCalls += 1
      return json(200, { data: state.info })
    }
    if (path === '/api/v1/system/update/check' && method === 'POST') {
      state.checkCalls += 1
      await new Promise((done) => setTimeout(done, 120))
      return json(200, { data: state.checkInfo ?? state.info })
    }
    if (path === '/api/v1/system/update/apply' && method === 'POST') {
      let payload = {}
      try {
        payload = request.postDataJSON() ?? {}
      } catch {
        payload = {}
      }
      state.applyCalls.push(payload)
      return json(202, { job_id: 'job-apply' })
    }
    if (path === '/api/v1/system/update/status' && method === 'GET') {
      state.statusCalls += 1
      const queue = state.statusQueue
      const next = queue.length > 1 ? queue.shift() : queue[0] ?? statusFixture({ phase: 'idle' })
      if (next.current_version) state.currentVersion = next.current_version
      // 自升级重启后进程内存里的 job 丢失：真实后端只返回 current_version/running/history，
      // 没有 job 键（internal/api/update_apply.go:167-192）。history 是唯一可用的终态证据。
      if (next.restart_without_job) {
        return json(200, {
          data: {
            current_version: state.currentVersion,
            running: false,
            upgrade_dir: '/tmp/leadstack/upgrade',
            stall_timeout_s: 600,
            history: next.history ?? [],
          },
        })
      }
      // 最简 idle 快照：无 job、无 history、无 pending_state。升级从未真正发起时，
      // 页面不得把 idle 猜成 success（e9 反向断言，对应 e8 报告「建议修改 M1」）。
      if (next.bare_idle) {
        return json(200, { data: { current_version: state.currentVersion, running: false, history: [] } })
      }
      return json(200, { data: { current_version: state.currentVersion, job: next } })
    }
    if (path === '/api/v1/system/update/upload' && method === 'POST') {
      const raw = request.postData() ?? ''
      state.uploadCalls.push({
        contentType: request.headers()['content-type'] ?? '',
        allowMajor: /name="allow_major"\r?\n\r?\n(true|false)/.exec(raw)?.[1] ?? null,
        bytes: raw.length,
      })
      if (state.uploadDelay > 0) await new Promise((done) => setTimeout(done, state.uploadDelay))
      return json(202, {
        data: {
          job_id: 'job-upload',
          status: statusFixture({ job_id: 'job-upload', phase: 'replacing', target_version: '2.0.0' }),
        },
      })
    }

    return json(404, { error: { code: 'not_found', message: `${method} ${path} 未在冒烟 mock 中实现` } })
  })
}

/** 覆盖 input.files 注入「大小已知但内容为空」的假文件，绕过 input 无法设置超大文件的问题。 */
async function injectFakeFile(page, name, size) {
  await page.evaluate(
    ({ name: fileName, size: sizeBytes }) => {
      const input = document.querySelector('#upgrade-file')
      if (!input) throw new Error('#upgrade-file 不存在')
      const file = new File([new Uint8Array(0)], fileName, { type: 'application/octet-stream' })
      Object.defineProperty(file, 'size', { value: sizeBytes })
      const transfer = new DataTransfer()
      transfer.items.add(file)
      input.files = transfer.files
      input.dispatchEvent(new Event('change', { bubbles: true }))
    },
    { name, size },
  )
}

function updateRequests(calls, sliceFrom = 0) {
  return calls.slice(sliceFrom).filter((call) => call.path.startsWith('/api/v1/system/update'))
}

/** 等待 #upgrade-phase 文案切到期望值（轮询 2s 一次，留足 15s）。 */
async function waitPhase(page, expected) {
  await page.waitForFunction(
    (want) => document.querySelector('#upgrade-phase')?.textContent?.trim() === want,
    expected,
    { timeout: 15000 },
  )
}

/** 终态必须停表：等两个轮询周期，计数不能增长。 */
async function expectPollingStopped(checker, page, state, label) {
  const before = state.statusCalls
  await page.waitForTimeout(3200)
  checker({
    check: `${label}：终态后停止轮询（再等 3s 请求数不增长）`,
    ok: state.statusCalls === before,
    detail: `statusCalls ${before} → ${state.statusCalls}`,
  })
}

async function layoutFacts(page) {
  return page.evaluate(() => {
    const overflow = document.documentElement.scrollWidth - window.innerWidth
    const measure = (selector) => {
      const element = document.querySelector(selector)
      if (!element) return null
      const box = element.getBoundingClientRect()
      return {
        height: Math.round(box.height),
        right: Math.round(box.right),
        ok: box.height >= 24 && box.right <= window.innerWidth + 1,
      }
    }
    return {
      overflow,
      width: window.innerWidth,
      rootReady: Boolean(
        document.querySelector('.ls-shell') &&
          document.querySelector('.ls-settingspage') &&
          document.querySelector('#upgrade-card'),
      ),
      check: measure('#upgrade-check'),
      picker: measure('.ls-upgrade__picker'),
    }
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

  const checker = makeChecker()
  /** 每条断言都写成 { check: '中文字标', ok, detail }：字标可被 grep，控制台逐条打印。 */
  const verify = ({ check, ok, detail = '' }) => checker.check(check, ok, detail)

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

  try {
    /* ============ ① 无 system.manage：整块不渲染、零请求 ============ */
    {
      const state = makeState(LIMITED_PERMISSIONS)
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
      await installMockApi(context, state)
      const page = await context.newPage()
      page.on('pageerror', (error) => pageErrors.push(`no-perm: ${error.message}`))
      const calls = watchApiCalls(page)
      await login(page, base, { password: PASSWORD })
      const before = calls.length
      await page.goto(`${base}/#/settings`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.ls-settingspage', { timeout: 15000 })
      await page.waitForTimeout(600)
      verify({ check: '无 system.manage 时整个升级卡不存在', ok: (await page.locator('#upgrade-card').count()) === 0 })
      const touched = updateRequests(calls, before)
      verify({
        check: '无 system.manage 时 /api/v1/system/update* 请求数 0',
        ok: touched.length === 0,
        detail: JSON.stringify(touched.slice(0, 3)),
      })
      verify({
        check: '无 system.manage 时 mock 未收到任何 update 调用',
        ok: state.getCalls + state.checkCalls + state.statusCalls + state.applyCalls.length + state.uploadCalls.length === 0,
        detail: `get=${state.getCalls} check=${state.checkCalls} status=${state.statusCalls}`,
      })
      await context.close()
    }

    /* ============ ② 有权限：首屏 GET 恰 1 次 ============ */
    const state = makeState()
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)
    await login(page, base, { password: PASSWORD })

    const navStart = calls.length
    await page.goto(`${base}/#/settings`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-card', { timeout: 15000 })
    await page.waitForSelector('#upgrade-current', { timeout: 15000 })

    const initialUpdateCalls = updateRequests(calls, navStart)
    const initialGets = initialUpdateCalls.filter(
      (call) => call.method === 'GET' && call.path === '/api/v1/system/update',
    )
    verify({
      check: 'admin 首屏 GET /system/update 恰 1 次',
      ok: initialGets.length === 1,
      detail: `实际 ${initialGets.length} 次`,
    })
    verify({
      check: '有更新时渲染 current/latest',
      ok:
        (await page.locator('#upgrade-current').innerText()).trim() === '1.8.0' &&
        (await page.locator('#upgrade-latest').innerText()).trim() === '2.0.0',
      detail: `${(await page.locator('#upgrade-current').innerText()).trim()} → ${(await page.locator('#upgrade-latest').innerText()).trim()}`,
    })
    const published = (await page.locator('#upgrade-published').innerText()).trim()
    verify({
      check: '发布时间格式化为本地 YYYY-MM-DD HH:mm',
      ok: /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/.test(published),
      detail: published,
    })
    const notes = (await page.locator('#upgrade-notes').innerText()).trim()
    verify({ check: '更新说明渲染', ok: notes.includes('离线升级包上传'), detail: notes.slice(0, 24) })
    verify({
      check: '更新说明按 Markdown 渲染标题/列表/引用/代码块',
      ok:
        (await page.locator('#upgrade-notes h2').count()) === 1 &&
        (await page.locator('#upgrade-notes ul li').count()) === 2 &&
        (await page.locator('#upgrade-notes blockquote').count()) === 1 &&
        (await page.locator('#upgrade-notes pre code').innerText()).includes('install.sh') &&
        !notes.includes('##') &&
        !notes.includes('```'),
      detail: notes.replace(/\s+/g, ' ').slice(0, 100),
    })
    verify({
      check: 'schema_changed 时显示「含数据库迁移」徽标',
      ok: (await page.locator('#upgrade-schema-badge').innerText()).trim() === '含数据库迁移',
    })
    verify({
      check: '有更新时「立即升级」按钮存在且可点',
      ok: (await page.locator('#upgrade-apply').count()) === 1 && (await page.locator('#upgrade-apply').isEnabled()),
      detail: `count=${await page.locator('#upgrade-apply').count()}`,
    })
    verify({
      check: '有更新时不显示「已是最新」',
      ok: (await page.locator('#upgrade-up-to-date').count()) === 0,
    })

    /* ============ ② 点击「立即检查更新」：update 请求 1→2（POST /check） ============ */
    await page.click('#upgrade-check')
    await page.waitForFunction(() => document.querySelector('#upgrade-check')?.disabled === false, { timeout: 10000 })
    const afterCheck = updateRequests(calls, navStart)
    const afterCheckGets = afterCheck.filter(
      (call) => call.method === 'GET' && call.path === '/api/v1/system/update',
    )
    const afterCheckPosts = afterCheck.filter(
      (call) => call.method === 'POST' && call.path === '/api/v1/system/update/check',
    )
    verify({
      check: '点「立即检查更新」后 /system/update 请求数从 1 变 2',
      ok: afterCheck.length === 2,
      detail: afterCheck.map((call) => `${call.method} ${call.path}`).join(' | '),
    })
    verify({
      check: '点击后新增的是 POST /system/update/check，GET 仍为 1 次',
      ok: afterCheckPosts.length === 1 && afterCheckGets.length === 1,
      detail: `GET=${afterCheckGets.length} POST/check=${afterCheckPosts.length}`,
    })

    /* ============ ⑤ 检查失败：info.error 原文展示、上传仍可用 ============ */
    const CHECK_ERROR = '连接 GitHub 失败：i/o timeout'
    state.checkInfo = infoFixture({ error: CHECK_ERROR })
    await page.click('#upgrade-check')
    await page.waitForSelector('#upgrade-check-error-text', { timeout: 10000 })
    const errorText = (await page.locator('#upgrade-check-error-text').innerText()).trim()
    verify({ check: '检查失败时原样展示 info.error', ok: errorText === CHECK_ERROR, detail: errorText })
    verify({
      check: '检查失败时 POST /system/update/check 计数为 2',
      ok: state.checkCalls === 2,
      detail: `checkCalls=${state.checkCalls}`,
    })
    verify({
      check: '检查失败后本地上传 input 仍可交互',
      ok: (await page.locator('#upgrade-file').isEnabled()) && (await page.locator('#upgrade-check').isEnabled()),
    })
    /* 真选一个文件，证明失败态下上传入口不是只剩一个灰控件。 */
    await page.setInputFiles('#upgrade-file', {
      name: 'check-failed-still-upload.tar.gz',
      mimeType: 'application/gzip',
      buffer: Buffer.alloc(1024, 3),
    })
    await page.waitForSelector('#upgrade-selected-file', { timeout: 5000 })
    verify({
      check: '检查失败后仍能选中本地升级包',
      ok: (await page.locator('#upgrade-selected-file').innerText()).includes('check-failed-still-upload.tar.gz'),
    })
    await page.setInputFiles('#upgrade-file', [])
    state.checkInfo = null
    state.info = infoFixture()

    /* ============ ④ upgradable=false：note、一键升级禁用、本地仍可交互 ============ */
    const NOT_UPGRADABLE = '当前平台（darwin/arm64）没有可用的一键升级产物，请用本地升级包。'
    state.info = infoFixture({ upgradable: false, upgrade_note: NOT_UPGRADABLE })
    queueStatus(state, [{ phase: 'idle' }])
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-note', { timeout: 15000 })
    verify({
      check: 'upgradable=false 时展示 upgrade_note 原文',
      ok: (await page.locator('#upgrade-note').innerText()).includes(NOT_UPGRADABLE),
    })
    verify({
      check: 'upgradable=false 时一键升级被禁用',
      ok: (await page.locator('#upgrade-apply').count()) === 1 && (await page.locator('#upgrade-apply').isDisabled()),
    })
    verify({
      check: 'upgradable=false 时本地上传 input 仍可交互',
      ok: await page.locator('#upgrade-file').isEnabled(),
    })
    await page.setInputFiles('#upgrade-file', {
      name: 'offline-2.0.0.tar.gz',
      mimeType: 'application/gzip',
      buffer: Buffer.alloc(1024, 5),
    })
    await page.waitForSelector('#upgrade-selected-file', { timeout: 5000 })
    verify({
      check: 'upgradable=false 时仍能选中本地升级包',
      ok: (await page.locator('#upgrade-selected-file').innerText()).includes('offline-2.0.0.tar.gz'),
    })
    await page.setInputFiles('#upgrade-file', [])

    /* ============ ③ 无更新：「已是最新」且一键升级不可点 ============ */
    state.info = infoFixture({ has_update: false, latest: '1.8.0', published_at: null, notes: null })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-up-to-date', { timeout: 15000 })
    verify({
      check: '无更新时显示「已是最新」',
      ok: (await page.locator('#upgrade-up-to-date').innerText()).includes('已是最新'),
      detail: (await page.locator('#upgrade-up-to-date').innerText()).trim(),
    })
    const noUpdateApplyCount = await page.locator('#upgrade-apply').count()
    const noUpdateApplyDisabled = noUpdateApplyCount === 1 ? await page.locator('#upgrade-apply').isDisabled() : true
    verify({
      check: '无更新时一键升级按钮不可点（禁用或不渲染）',
      ok: noUpdateApplyDisabled,
      detail: noUpdateApplyCount === 0 ? '按钮未渲染' : '按钮已禁用',
    })
    state.info = infoFixture()

    /* ============ ⑥ 一键升级：2s 轮询四阶段 → success，终态停轮询 ============ */
    queueStatus(state, [
      { phase: 'downloading', target_version: '2.0.0', phase_elapsed_s: 3 },
      { phase: 'backing_up', target_version: '2.0.0', phase_elapsed_s: 12 },
      { phase: 'replacing', target_version: '2.0.0', phase_elapsed_s: 40 },
      { phase: 'restarting', target_version: '2.0.0', phase_elapsed_s: 55 },
      { phase: 'success', target_version: '2.0.0', current_version: '2.0.0' },
    ])
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm-panel', { timeout: 5000 })
    const confirmText = (await page.locator('#upgrade-confirm-panel').innerText()).trim()
    verify({ check: '升级二次确认说明备份当前版本', ok: confirmText.includes('备份') })
    verify({ check: '升级二次确认说明会重启服务', ok: confirmText.includes('重启') })

    const applyStart = Date.now()
    await page.click('#upgrade-confirm')
    const phaseTimeline = [
      ['downloading', '正在下载升级包'],
      ['backing_up', '正在备份当前版本'],
      ['replacing', '正在替换二进制文件'],
      ['restarting', '正在重启控制端'],
    ]
    for (const [phase, label] of phaseTimeline) {
      await waitPhase(page, label)
      verify({ check: `一键升级阶段 ${phase} 文案跟随为「${label}」`, ok: true })
    }
    await waitPhase(page, '升级成功')
    const applyElapsed = Date.now() - applyStart
    verify({
      check: '阶段推进符合 2s 轮询节奏（4 次阶段切换 ≥6s）',
      ok: applyElapsed >= 6000,
      detail: `${applyElapsed}ms`,
    })
    verify({
      check: '一键升级 apply 请求带 version=2.0.0',
      ok: state.applyCalls.length === 1 && state.applyCalls[0].version === '2.0.0',
      detail: JSON.stringify(state.applyCalls),
    })
    await page.waitForFunction(
      () => document.querySelector('#upgrade-current')?.textContent?.trim() === '2.0.0',
      { timeout: 5000 },
    )
    verify({
      check: '升级成功后当前版本刷新为 2.0.0',
      ok: (await page.locator('#upgrade-current').innerText()).trim() === '2.0.0',
    })
    verify({
      check: '升级成功后显示完成提示',
      ok: (await page.locator('#upgrade-status').innerText()).includes('升级已完成'),
    })
    await expectPollingStopped(verify, page, state, 'success')

    if (SHOTS) await page.screenshot({ path: resolve(SHOTS, '01-upgrade-success.png'), fullPage: true }).catch(() => {})

    /* ============ ⑦ rolled_back：自动回退文案 + 停轮询 ============ */
    state.info = infoFixture()
    queueStatus(state, [
      { phase: 'replacing', target_version: '2.0.0', phase_elapsed_s: 20 },
      { phase: 'rolled_back', target_version: '2.0.0', rolled_back: true, phase_elapsed_s: 45 },
    ])
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    await waitPhase(page, '已自动回退到升级前版本')
    verify({
      check: 'rolled_back 时显示「已自动回退到升级前版本」',
      ok: (await page.locator('#upgrade-phase').innerText()).trim() === '已自动回退到升级前版本',
    })
    await expectPollingStopped(verify, page, state, 'rolled_back')

    /* ============ ⑧ stalled：中断文案 + 重新升级按钮 + 停轮询 ============ */
    state.info = infoFixture()
    queueStatus(state, [{ phase: 'stalled', target_version: '2.0.0', stalled: true, phase_elapsed_s: 120 }])
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    await waitPhase(page, '升级可能已中断')
    verify({
      check: 'stalled 时显示「升级可能已中断」',
      ok: (await page.locator('#upgrade-phase').innerText()).trim() === '升级可能已中断',
    })
    verify({
      check: 'stalled 时出现「重新升级」按钮',
      ok: (await page.locator('#upgrade-retry').count()) === 1,
    })
    await expectPollingStopped(verify, page, state, 'stalled')
    await page.click('#upgrade-retry')
    await page.waitForSelector('#upgrade-confirm-panel', { timeout: 5000 })
    verify({
      check: '「重新升级」重新打开确认面板',
      ok: (await page.locator('#upgrade-confirm').count()) === 1,
    })
    await page.click('#upgrade-cancel')

    /* ============ ⑧.5 失败但 done=true：phase 仍是 swap，必须收口为 failed ============ */
    state.info = infoFixture()
    queueStatus(
      state,
      [{ phase: 'swap', done: true, error: '替换可执行文件失败（已回滚）', target_version: '2.0.0' }],
      '1.8.0',
    )
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    await waitPhase(page, '升级失败')
    verify({
      check: 'phase=swap 但 done=true 时收口为「升级失败」并展示后端错误',
      ok:
        (await page.locator('#upgrade-phase').innerText()).trim() === '升级失败' &&
        (await page.locator('#upgrade-job-error').innerText()).includes('替换可执行文件失败'),
    })
    await expectPollingStopped(verify, page, state, 'swap-done')

    /* ============ ⑨ 本地上传 1024 字节：multipart + 进度百分比 + 202 切轮询 ============ */
    /* 后端只收解压后的裸二进制；选择器必须留 * / *，否则 macOS 会把无扩展名二进制灰掉。 */
    const acceptHint = (await page.locator('#upgrade-accept-hint').innerText()).trim()
    verify({
      check: '选文件控件下方提示「解压后」的 leadstack 二进制（含「解压」字样）',
      ok: acceptHint.includes('解压') && acceptHint.includes('leadstack'),
      detail: acceptHint.slice(0, 60),
    })
    verify({
      check: '文件选择器 accept 为 .bin,application/octet-stream,*/*（保留 */*）',
      ok: (await page.locator('#upgrade-file').getAttribute('accept')) === '.bin,application/octet-stream,*/*',
      detail: await page.locator('#upgrade-file').getAttribute('accept'),
    })
    state.info = infoFixture()
    state.uploadDelay = 700
    queueStatus(state, [
      { job_id: 'job-upload', phase: 'replacing', target_version: '2.0.0', phase_elapsed_s: 4 },
      { job_id: 'job-upload', phase: 'success', target_version: '2.0.0', current_version: '2.0.0' },
    ])
    const uploadsBefore = state.uploadCalls.length
    const statusBeforeUpload = state.statusCalls
    await page.setInputFiles('#upgrade-file', {
      name: 'leadstack-2.0.0.tar.gz',
      mimeType: 'application/gzip',
      buffer: Buffer.alloc(1024, 9),
    })
    await page.waitForSelector('#upgrade-selected-file', { timeout: 5000 })
    verify({
      check: '选中 1024 字节升级包后显示文件名与大小',
      ok:
        (await page.locator('#upgrade-selected-file').innerText()).includes('leadstack-2.0.0.tar.gz') &&
        (await page.locator('#upgrade-selected-file').innerText()).includes('1'),
      detail: (await page.locator('#upgrade-selected-file').innerText()).trim(),
    })
    await page.click('#upgrade-upload-submit')
    await page.waitForSelector('#upgrade-upload-progress', { timeout: 5000 })
    const progressText = (await page.locator('#upgrade-upload-progress').innerText()).trim()
    verify({
      check: '上传中显示进度百分比（上传中 n%）',
      ok: /上传中 \d+%/.test(progressText),
      detail: progressText.replace(/\s+/g, ' ').slice(0, 40),
    })
    await page.waitForFunction(
      () => document.querySelector('#upgrade-phase')?.textContent?.trim() === '正在替换二进制文件',
      { timeout: 10000 },
    )
    verify({
      check: '上传发出 multipart POST /system/update/upload',
      ok:
        state.uploadCalls.length === uploadsBefore + 1 &&
        state.uploadCalls[uploadsBefore].contentType.includes('multipart/form-data'),
      detail: JSON.stringify(state.uploadCalls[uploadsBefore]),
    })
    verify({
      check: '上传请求显式带 allow_major=false 且体积 ≥1024 字节',
      ok: state.uploadCalls[uploadsBefore].allowMajor === 'false' && state.uploadCalls[uploadsBefore].bytes >= 1024,
      detail: `bytes=${state.uploadCalls[uploadsBefore].bytes}`,
    })
    verify({
      check: '上传 202 后切到同一套 job 轮询',
      ok: state.statusCalls > statusBeforeUpload,
      detail: `statusCalls ${statusBeforeUpload} → ${state.statusCalls}`,
    })
    await waitPhase(page, '升级成功')
    await expectPollingStopped(verify, page, state, 'upload-success')
    state.uploadDelay = 0

    /* ============ ⑩ 201 MiB 假文件：前端直接拦下、0 请求 ============ */
    const uploadsAfterUpload = state.uploadCalls.length
    await injectFakeFile(page, 'huge-201.tar.gz', 201 * 1024 * 1024)
    await page.waitForFunction(
      () => (document.querySelector('#upgrade-file-error')?.textContent ?? '').includes('201 MiB'),
      { timeout: 5000 },
    )
    const hugeText = (await page.locator('#upgrade-file-error').innerText()).trim()
    verify({
      check: '201 MiB 假文件被前端拦下并提示真实大小',
      ok: hugeText.includes('201 MiB') && hugeText.includes('200 MiB'),
      detail: hugeText,
    })
    verify({
      check: '201 MiB 被拦下时不发上传请求',
      ok: state.uploadCalls.length === uploadsAfterUpload,
      detail: `uploads=${state.uploadCalls.length - uploadsAfterUpload}`,
    })
    verify({
      check: '201 MiB 被拦下后不出现已选文件',
      ok: (await page.locator('#upgrade-selected-file').count()) === 0,
    })
    await injectFakeFile(page, 'empty.tar.gz', 0)
    await page.waitForFunction(
      () => (document.querySelector('#upgrade-file-error')?.textContent ?? '').includes('0 字节'),
      { timeout: 5000 },
    )
    verify({
      check: '0 字节文件也被前端拦下',
      ok: (await page.locator('#upgrade-file-error').innerText()).includes('0 字节'),
    })

    /* ============ ⑬ 重启后 status 无 job（success）：history 是唯一证据，必须停表 + 刷新版本 ============ */
    state.info = infoFixture({ current: '1.8.0', latest: '2.0.0' })
    queueStatus(
      state,
      [
        { phase: 'restarting', target_version: '2.0.0', phase_elapsed_s: 41 },
        {
          restart_without_job: true,
          current_version: '2.0.0',
          history: [{ ts: NOW, outcome: 'success', from_version: '1.8.0', target_version: '2.0.0' }],
        },
      ],
      '1.8.0',
    )
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    // 第一轮 restarting；第二轮起进程已重启、内存 job 消失，只剩 history。
    await page.waitForTimeout(5200)
    const restartSuccessPhase = (await page.locator('#upgrade-phase').innerText()).trim()
    verify({
      check: '重启后 status 无 job（success）：阶段落定为「升级成功」',
      ok: restartSuccessPhase === '升级成功',
      detail: restartSuccessPhase,
    })
    const restartSuccessCurrent = (await page.locator('#upgrade-current').innerText()).trim()
    verify({
      check: '重启后 status 无 job（success）：#upgrade-current 刷新为新版本 2.0.0',
      ok: restartSuccessCurrent === '2.0.0',
      detail: restartSuccessCurrent,
    })
    verify({
      check: '重启后 status 无 job（success）：上传入口恢复可用（jobBusy 已释放）',
      ok: await page.locator('#upgrade-file').isEnabled(),
    })
    await expectPollingStopped(verify, page, state, '重启后 status 无 job（success）')

    /* ============ ⑭ 重启后 status 无 job（rollback）：显示自动回退 + 停表，不卡在「升级进行中」 ============ */
    state.info = infoFixture({ current: '1.8.0', latest: '2.0.0' })
    queueStatus(
      state,
      [
        { phase: 'restarting', target_version: '2.0.0', phase_elapsed_s: 41 },
        {
          restart_without_job: true,
          current_version: '1.8.0',
          history: [
            {
              ts: NOW,
              outcome: 'rollback',
              from_version: '1.8.0',
              target_version: '2.0.0',
              reason: '新进程未在 120s 内就绪',
            },
          ],
        },
      ],
      '1.8.0',
    )
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    await page.waitForTimeout(5200)
    const restartRollbackPhase = (await page.locator('#upgrade-phase').innerText()).trim()
    verify({
      check: '重启后 status 无 job（rollback）：显示「已自动回退到升级前版本」',
      ok: restartRollbackPhase === '已自动回退到升级前版本',
      detail: restartRollbackPhase,
    })
    const restartRollbackCurrent = (await page.locator('#upgrade-current').innerText()).trim()
    verify({
      check: '重启后 status 无 job（rollback）：当前版本保持 1.8.0 未被误刷',
      ok: restartRollbackCurrent === '1.8.0',
      detail: restartRollbackCurrent,
    })
    await expectPollingStopped(verify, page, state, '重启后 status 无 job（rollback）')

    /* ============ ⑮ 无条件 idle → success 的反向断言 ============
     * 无 job、无 history、无 pending_state 时没有任何「升级已完成」的证据：
     * 页面必须停在 idle，不得把 idle 猜成「升级成功」。
     * 该用例用于捕获 resolveLostJobOutcome 被改成无条件返回 'success' 的变异。
     */
    state.info = infoFixture({ current: '1.8.0', latest: '2.0.0' })
    queueStatus(state, [{ bare_idle: true, current_version: '1.8.0' }], '1.8.0')
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#upgrade-apply', { timeout: 15000 })
    await page.click('#upgrade-apply')
    await page.waitForSelector('#upgrade-confirm', { timeout: 5000 })
    await page.click('#upgrade-confirm')
    await page.waitForTimeout(5200)
    const noHistoryStatus = (await page.locator('#upgrade-status').innerText()).trim()
    const noHistoryPhase = (await page.locator('#upgrade-phase').innerText()).trim()
    verify({
      check: '重启后 status 无 job 且无 history（无 history 不得显示成功）：页面不得出现「升级成功」',
      ok: noHistoryPhase !== '升级成功' && !noHistoryStatus.includes('升级成功') && !noHistoryStatus.includes('升级已完成'),
      detail: noHistoryPhase,
    })
    const noHistoryCurrent = (await page.locator('#upgrade-current').innerText()).trim()
    verify({
      check: '重启后 status 无 job 且无 history（无 history 不得显示成功）：#upgrade-current 仍为 1.8.0',
      ok: noHistoryCurrent === '1.8.0',
      detail: noHistoryCurrent,
    })
    const noHistoryPhaseClass = (await page.locator('#upgrade-status .ls-upgrade__phase').getAttribute('class')) ?? ''
    verify({
      check: '重启后 status 无 job 且无 history（无 history 不得显示成功）：状态停在 idle（未升级）',
      ok: noHistoryPhaseClass.includes('ls-upgrade__phase--normal') && !noHistoryPhaseClass.includes('ls-upgrade__phase--success'),
      detail: `${noHistoryPhase} / ${noHistoryPhaseClass}`,
    })

    /* ============ ⑪ 320px：先等根类就绪，再断言无横向滚动 ============ */
    await page.setViewportSize({ width: 320, height: 900 })
    await page.waitForSelector('.ls-settingspage', { timeout: 10000 })
    await page.waitForTimeout(200)
    const narrow = await layoutFacts(page)
    verify({
      check: '320px 下根类就绪且升级卡无横向滚动',
      ok: narrow.rootReady && narrow.overflow <= 0,
      detail: `rootReady=${narrow.rootReady} overflow=${narrow.overflow}`,
    })
    verify({
      check: '320px 下「立即检查更新」命中区 ≥24px 且在视口内',
      ok: Boolean(narrow.check && narrow.check.ok),
      detail: JSON.stringify(narrow.check),
    })
    verify({
      check: '320px 下选择升级包命中区 ≥24px 且在视口内',
      ok: Boolean(narrow.picker && narrow.picker.ok),
      detail: JSON.stringify(narrow.picker),
    })
    if (SHOTS) await page.screenshot({ path: resolve(SHOTS, '02-upgrade-320.png'), fullPage: true }).catch(() => {})
    await page.setViewportSize({ width: 1280, height: 900 })

    /* ============ ⑫ 全程 pageerror 0 ============ */
    verify({
      check: '全程 pageerror 为 0',
      ok: pageErrors.length === 0,
      detail: pageErrors.slice(0, 2).join(' | '),
    })

    await context.close()
  } catch (error) {
    verify({ check: '冒烟执行未抛异常', ok: false, detail: error && error.message ? error.message : String(error) })
  } finally {
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  process.exitCode = checker.finish()
}

run().catch((error) => {
  console.error(error && error.stack ? error.stack : error)
  process.exitCode = 1
})
