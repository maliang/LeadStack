#!/usr/bin/env node
/**
 * 批量导入主机页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：凭证/网络配置加载 / 空预览 / 粘贴解析（有效、重复、非法）/ 文件选择读入并逐行预览 /
 * 超过 500 行报错 / 必选凭证校验 / 提交中禁用态 / 逐行成功与失败结果 / 失败行回显后端错误 /
 * 凭证加载失败与重试 / 320px 无横向滚动 / 无权限时路由守卫拦截且不写请求 / 无未捕获异常。
 * 用法：node scripts/smoke-import.mjs [--headed] [--dist dist] [--port 0]
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
  writes,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const REQUESTED_PORT = Number(flag('port', 0)) || 0
const { dist: DIST, ready: DIST_READY } = resolveDist(WEB_ROOT, flag)

const CREDENTIALS = [
  { id: 'cred-1', name: 'root-key', kind: 'ssh_key' },
  { id: 'cred-2', name: 'ops-password', kind: 'password' },
]

const HOST_TEXT = '10.0.0.11\n10.0.0.12:2200\n10.0.0.11\nbad host\n# comment\n'
const FILE_TEXT = '10.0.0.21\n10.0.0.22:2222\n10.0.0.23\n'
const TOO_MANY_TEXT = Array.from({ length: 501 }, (_, index) => `10.8.0.${index + 1}`).join('\n')

const IMPORT_RESULT = {
  total: 2,
  created: [{ host: '10.0.0.21', server_id: 'srv-21' }],
  failed: [{ host: '10.0.0.22', error: 'SSH 连接超时' }],
}

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
    if (path === '/api/v1/credentials' && method === 'GET') {
      state.credentialCalls += 1
      if (state.failCredentials) {
        return json(500, { error: { code: 'internal', message: '凭证服务不可用' } })
      }
      return json(200, { data: state.credentials.map((item) => ({ ...item })) })
    }
    if (path === '/api/v1/system/network' && method === 'GET') {
      return json(200, { data: { default_ssh_port: 2222 } })
    }
    if (path === '/api/v1/servers/import' && method === 'POST') {
      const body = request.postDataJSON() || {}
      state.importCalls.push(body)
      if (state.failImport) {
        return json(500, { error: { code: 'internal', message: '导入失败，请稍后重试' } })
      }
      if (state.importDelayMs > 0) await new Promise((done) => setTimeout(done, state.importDelayMs))
      return json(200, { data: { ...IMPORT_RESULT, created: IMPORT_RESULT.created.map((item) => ({ ...item })), failed: IMPORT_RESULT.failed.map((item) => ({ ...item })) } })
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

  const state = {
    permissions: ['server.write', 'server.read'],
    credentials: CREDENTIALS.map((item) => ({ ...item })),
    credentialCalls: 0,
    failCredentials: false,
    failImport: false,
    importDelayMs: 0,
    importCalls: [],
  }

  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const apiCalls = watchApiCalls(page)

  try {
    // 1) 登录并进入导入页
    await login(page, base)
    await page.goto(`${base}/#/import`, { waitUntil: 'domcontentloaded' })
    const heading = await readHeading(page, '批量导入主机')
    await page.waitForSelector('#import-credential', { timeout: 15000 })
    await page.waitForFunction(
      () => document.querySelectorAll('#import-credential option').length === 3,
      undefined,
      { timeout: 8000 },
    )
    check('进入批量导入主机页并渲染标题', heading === '批量导入主机', heading)
    check('凭证下拉加载 2 个凭证（含占位共 3 项）', (await page.locator('#import-credential option').count()) === 3)
    check('默认端口取网络配置的 2222', (await page.inputValue('#import-port')) === '2222')
    check('初始预览显示空态', (await page.locator('#import-preview-empty').count()) === 1)
    check(
      '初始解析计数为 0',
      (await page.locator('#import-parsed').innerText()).replace(/\s+/g, ' ').trim() === '解析：0 条有效 / 共 0 行',
      await page.locator('#import-parsed').innerText(),
    )

    // 2) 粘贴解析：有效 / 重复 / 非法逐行标记
    await page.fill('#import-text', HOST_TEXT)
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-importpage__preview-row').length === 4,
      undefined,
      { timeout: 5000 },
    )
    check('解析出 4 行预览', (await page.locator('.ls-importpage__preview-row').count()) === 4)
    check(
      '状态列区分可导入/重复/格式有误',
      (await page.locator('.ls-importpage__preview-row[data-note="ok"]').count()) === 2 &&
        (await page.locator('.ls-importpage__preview-row[data-note="duplicate"]').count()) === 1 &&
        (await page.locator('.ls-importpage__preview-row[data-note="invalid"]').count()) === 1,
    )
    check(
      '解析计数排除重复与非法行',
      (await page.locator('#import-parsed').innerText()).replace(/\s+/g, ' ').trim() === '解析：2 条有效 / 共 4 行',
      await page.locator('#import-parsed').innerText(),
    )

    // 3) 文件选择：读入文本域并展示文件名与行数
    await page.setInputFiles('#import-file', {
      name: 'hosts.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from(FILE_TEXT, 'utf8'),
    })
    await page.waitForFunction(
      () => (document.querySelector('#import-text')?.value || '').includes('10.0.0.21'),
      undefined,
      { timeout: 5000 },
    )
    const fileNameText = (await page.locator('#import-file-name').innerText()).replace(/\s+/g, ' ').trim()
    check('选择文件后文本域被读入', (await page.inputValue('#import-text')).includes('10.0.0.23'))
    check('文件名与行数可见', fileNameText.includes('hosts.txt') && fileNameText.includes('3 行'), fileNameText)
    check('文件内容替换预览为 3 行', (await page.locator('.ls-importpage__preview-row').count()) === 3)

    // 4) 超过 500 行：前端拦截并给出可读错误
    await page.setInputFiles('#import-file', {
      name: 'too-many.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from(TOO_MANY_TEXT, 'utf8'),
    })
    await page.waitForSelector('#import-file-error', { timeout: 5000 })
    check(
      '超过 500 行的文件被拒绝',
      (await page.locator('#import-file-error').innerText()).includes('500'),
      await page.locator('#import-file-error').innerText(),
    )

    await page.setInputFiles('#import-file', {
      name: 'hosts.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from(FILE_TEXT, 'utf8'),
    })
    await page.waitForSelector('#import-file-error', { state: 'detached', timeout: 5000 })
    check('重新选择合法文件后错误消失', (await page.locator('#import-file-error').count()) === 0)

    // 5) 未选凭证时提交被拦截，不发导入请求
    await page.click('#import-submit')
    await page.waitForSelector('#import-error', { timeout: 5000 })
    check(
      '未选凭证时提示选择凭证',
      (await page.locator('#import-error').innerText()).trim() === '请选择用于连接的凭证',
      await page.locator('#import-error').innerText(),
    )
    check('校验失败时没有发导入请求', state.importCalls.length === 0)

    // 6) 提交中：按钮禁用 + aria-busy
    state.importDelayMs = 600
    await page.selectOption('#import-credential', 'cred-1')
    await page.click('#import-submit')
    const submitting = await page.evaluate(() => {
      const button = document.querySelector('#import-submit')
      return {
        disabled: Boolean(button && button.disabled),
        busy: button?.getAttribute('aria-busy') === 'true',
        text: (button?.textContent || '').trim(),
      }
    })
    check(
      '提交中按钮禁用并标记 aria-busy',
      submitting.disabled && submitting.busy,
      JSON.stringify(submitting),
    )
    check('提交中按钮文案切换为正在导入', submitting.text.includes('正在导入'), submitting.text)

    await page.waitForSelector('#import-result', { timeout: 10000 })
    state.importDelayMs = 0
    const payload = state.importCalls[0] || {}
    check('导入发出一次 POST /servers/import', state.importCalls.length === 1, `${state.importCalls.length} 次`)
    check(
      '导入 payload 字段正确',
      payload.default_port === 2222 &&
        payload.credential_id === 'cred-1' &&
        payload.mode === 'ssh' &&
        Array.isArray(payload.tags) &&
        payload.tags.length === 0 &&
        typeof payload.text === 'string' &&
        payload.text.includes('10.0.0.21'),
      JSON.stringify({ ...payload, text: String(payload.text).slice(0, 30) }),
    )

    // 7) 逐行结果：成功行带服务器链接，失败行回显后端错误
    check(
      '结果汇总展示总数/成功/失败',
      (await page.locator('#import-result-summary').innerText()).replace(/\s+/g, ' ').trim() ===
        '共 2 条，成功 1 条，失败 1 条。',
      await page.locator('#import-result-summary').innerText(),
    )
    check('成功行逐条渲染', (await page.locator('.ls-importpage__created').count()) === 1)
    check('失败行逐条渲染', (await page.locator('.ls-importpage__failed').count()) === 1)
    check(
      '成功行提供服务器详情链接',
      (await page.locator('.ls-importpage__created .ls-importpage__link').getAttribute('href'))?.includes('srv-21'),
    )
    check(
      '失败行回显后端 error 文案',
      (await page.locator('.ls-importpage__failed').innerText()).includes('SSH 连接超时'),
      await page.locator('.ls-importpage__failed').innerText(),
    )
    check('导入成功有可见提示', (await page.locator('#import-notice').count()) === 1)

    // 8) 导入失败：错误可读，页面不崩
    state.failImport = true
    await page.click('#import-submit')
    await page.waitForFunction(
      () => (document.querySelector('#import-error')?.textContent || '').includes('导入失败'),
      undefined,
      { timeout: 8000 },
    )
    check(
      '导入失败逐字展示后端错误',
      (await page.locator('#import-error').innerText()).includes('导入失败，请稍后重试'),
      await page.locator('#import-error').innerText(),
    )
    state.failImport = false

    // 9) 凭证加载失败 → 错误横幅；重试后恢复下拉
    state.failCredentials = true
    await page.reload({ waitUntil: 'domcontentloaded' })
    await readHeading(page, '批量导入主机')
    await page.waitForSelector('#import-load-error', { timeout: 15000 })
    check(
      '凭证加载失败显示错误与重试入口',
      (await page.locator('#import-load-error').innerText()).includes('读取凭证列表失败') &&
        (await page.locator('#import-load-error .ls-btn').count()) === 1,
      await page.locator('#import-load-error').innerText(),
    )
    state.failCredentials = false
    await page.click('#import-load-error .ls-btn')
    await page.waitForFunction(
      () => document.querySelectorAll('#import-credential option').length === 3,
      undefined,
      { timeout: 8000 },
    )
    check('重试后凭证下拉恢复', (await page.locator('#import-load-error').count()) === 0)

    await page.fill('#import-text', HOST_TEXT)
    await page.waitForFunction(
      () => document.querySelectorAll('.ls-importpage__preview-row').length === 4,
      undefined,
      { timeout: 5000 },
    )

    // 10) 320px：两栏降单栏且无横向滚动
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForFunction(
      () =>
        Boolean(document.querySelector('.ls-importpage')) &&
        document.querySelectorAll('.ls-importpage__preview-row').length > 0,
      undefined,
      { timeout: 8000 },
    )
    const narrow = await page.evaluate(() => ({
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      submitVisible: (() => {
        const button = document.querySelector('#import-submit')
        if (!button) return false
        const rect = button.getBoundingClientRect()
        return rect.width > 0 && rect.height >= 24 && rect.right <= window.innerWidth + 1
      })(),
    }))
    check('320px 导入页无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 提交按钮可见且命中区足够', narrow.submitVisible)

    // 11) 无 server.write：路由守卫拦截，不请求凭证、不发导入请求
    const importPostsBefore = apiCalls.filter((call) => call.path === '/api/v1/servers/import').length
    const credentialCallsBefore = state.credentialCalls
    state.permissions = ['server.read']
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForFunction(
      () => window.location.hash.startsWith('#/forbidden'),
      undefined,
      { timeout: 15000 },
    )
    check('无 server.write 被重定向到 /forbidden', new URL(page.url()).hash.startsWith('#/forbidden'), new URL(page.url()).hash)
    check(
      '无权限时不请求凭证列表',
      state.credentialCalls === credentialCallsBefore,
      `${credentialCallsBefore} → ${state.credentialCalls}`,
    )
    check(
      '无权限时没有发导入请求',
      apiCalls.filter((call) => call.path === '/api/v1/servers/import').length === importPostsBefore,
    )
    check('无权限时不渲染导入表单', (await page.locator('#import-submit').count()) === 0)

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
