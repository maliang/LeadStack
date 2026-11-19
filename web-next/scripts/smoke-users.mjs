#!/usr/bin/env node
/**
 * 用户管理页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表分页 / 搜索与无命中 / 启用停用 / 解锁 / 重置密码弹窗（明文不落 DOM）/
 * 删除二次确认与取消 / Esc 关闭与焦点回归 / 无 user.manage 时路由守卫拦截且不请求列表 /
 * 320px 无横向滚动 / 无未捕获异常。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-users.mjs [--headed]
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

const TOKEN = 'smoke-servers-token'
const LOGIN_PASSWORD = 'smoke-servers-password'
const RESET_SECRET = 'smoke-reset-newpass123'

const ROLES = [
  { key: 'admin', name: '管理员', description: '内置管理员', builtin: true, permissions: [], scope: { mode: 'all', group_ids: [], server_ids: [] }, user_count: 1, created_at: Date.now() - 86_400_000, updated_at: null },
  { key: 'operator', name: '运维', description: '内置运维', builtin: true, permissions: [], scope: { mode: 'all', group_ids: [], server_ids: [] }, user_count: 0, created_at: Date.now() - 86_400_000, updated_at: null },
  { key: 'viewer', name: '只读', description: '内置只读', builtin: true, permissions: [], scope: { mode: 'all', group_ids: [], server_ids: [] }, user_count: 0, created_at: Date.now() - 86_400_000, updated_at: null },
]

function userFixture(overrides) {
  return {
    id: 'u-placeholder',
    username: 'placeholder',
    role: 'viewer',
    role_name: '只读',
    disabled: false,
    locked: false,
    locked_until: null,
    failed_attempts: 0,
    created_at: Date.now() - 3_600_000,
    updated_at: null,
    ...overrides,
  }
}

const USERS = [
  userFixture({ id: 'u-admin', username: 'admin', role: 'admin', role_name: '管理员' }),
  userFixture({ id: 'u-alice', username: 'alice', role: 'operator', role_name: '运维' }),
  userFixture({ id: 'u-bob', username: 'bob', role: 'viewer', role_name: '只读', disabled: true }),
  userFixture({ id: 'u-carol', username: 'carol', role: 'viewer', role_name: '只读' }),
  userFixture({ id: 'u-dave', username: 'dave', role: 'viewer', role_name: '只读' }),
  userFixture({ id: 'u-erin', username: 'erin', role: 'viewer', role_name: '只读' }),
  userFixture({ id: 'u-frank', username: 'frank', role: 'viewer', role_name: '只读' }),
  userFixture({ id: 'u-grace', username: 'grace', role: 'viewer', role_name: '只读' }),
  userFixture({ id: 'u-heidi', username: 'heidi', role: 'viewer', role_name: '只读', locked: true, failed_attempts: 5 }),
  userFixture({ id: 'u-ivan', username: 'ivan', role: 'viewer', role_name: '只读' }),
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
    error: ['找不到 playwright —— 这是环境缺失，不是断言失败。', ...errors.map((line) => `  尝试过 ${line}`)].join('\n'),
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

/** 假后端：只覆盖用户页需要的端点，其他一律 404，避免静默放过。 */
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
      return json(200, { token: TOKEN, user: currentUser() })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: currentUser() })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }
    if (path === '/api/v1/roles' && method === 'GET') {
      return json(200, { data: ROLES.map((role) => ({ ...role, user_count: role.key === 'admin' ? 1 : 0 })) })
    }
    if (path === '/api/v1/users' && method === 'GET') {
      state.userListCalls += 1
      return json(200, { data: state.users.map((item) => ({ ...item })) })
    }
    if (path === '/api/v1/users' && method === 'POST') {
      const body = request.postDataJSON() || {}
      const created = userFixture({
        id: `u-${body.username}`,
        username: String(body.username || ''),
        role: String(body.role || 'viewer'),
        role_name: ROLES.find((role) => role.key === body.role)?.name ?? '只读',
        created_at: Date.now(),
      })
      state.users.push(created)
      state.createdUsers.push(body)
      return json(200, { data: created })
    }

    const detail = /^\/api\/v1\/users\/([^/]+)$/.exec(path)
    if (detail && method === 'PATCH') {
      const id = decodeURIComponent(detail[1])
      const body = request.postDataJSON() || {}
      const found = state.users.find((item) => item.id === id)
      state.patchCalls.push({ id, body })
      if (!found) return json(404, { error: { code: 'not_found', message: '用户不存在' } })
      if (body.role) {
        found.role = String(body.role)
        found.role_name = ROLES.find((role) => role.key === body.role)?.name ?? found.role_name
      }
      if (typeof body.disabled === 'boolean') found.disabled = body.disabled
      if (typeof body.password === 'string') found.password_touched = true
      found.updated_at = Date.now()
      return json(200, { data: { ...found } })
    }
    if (detail && method === 'DELETE') {
      const id = decodeURIComponent(detail[1])
      state.deleteCalls.push(id)
      state.users = state.users.filter((item) => item.id !== id)
      return noContent()
    }
    const unlock = /^\/api\/v1\/users\/([^/]+)\/unlock$/.exec(path)
    if (unlock && method === 'POST') {
      const id = decodeURIComponent(unlock[1])
      state.unlockCalls += 1
      const found = state.users.find((item) => item.id === id)
      if (found) {
        found.locked = false
        found.failed_attempts = 0
        found.locked_until = null
      }
      return json(200, { data: found ? { ...found } : null })
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
    permissions: ['user.manage', 'role.manage', 'server.read'],
    users: USERS.map((item) => ({ ...item })),
    userListCalls: 0,
    patchCalls: [],
    deleteCalls: [],
    createdUsers: [],
    unlockCalls: 0,
  }

  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const apiCalls = []
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/')) {
      apiCalls.push(`${request.method()} ${new URL(request.url()).pathname}`)
    }
  })

  try {
    // 1) 登录后进入 /users，确认列表与分页
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', LOGIN_PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.goto(`${base}/#/users`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#users-search-input', { timeout: 15000 })
    await page.waitForSelector('[data-user-id="u-alice"]', { timeout: 15000 })

    check('登录后进入用户页', new URL(page.url()).hash === '#/users', new URL(page.url()).hash)
    check('列表接口只拉一次', state.userListCalls === 1, `${state.userListCalls} 次`)
    check('首屏渲染 8 行（分页 pageSize=8）', (await page.locator('.ls-userspage__row').count()) === 8)
    check(
      '计数文案显示过滤/总数',
      (await page.locator('#users-count').innerText()).replace(/\s+/g, ' ').trim() === '10 / 10 个用户',
      await page.locator('#users-count').innerText(),
    )
    check(
      '当前账号行不显示操作按钮，只给说明',
      (await page.locator('[data-user-id="u-admin"] .ls-userspage__delete').count()) === 0 &&
        (await page.locator('[data-user-id="u-admin"] .ls-adminpage__hint').count()) === 1,
    )

    // 2) 搜索：命中一条、无命中文案、清空恢复
    await page.fill('#users-search-input', 'alice')
    await page.waitForTimeout(150)
    check('搜索命中 1 行', (await page.locator('.ls-userspage__row').count()) === 1)
    check('命中行为 alice', (await page.locator('[data-user-id="u-alice"]').count()) === 1)
    await page.fill('#users-search-input', 'zzz')
    await page.waitForTimeout(150)
    check('无命中时显示空态', (await page.locator('#users-empty-filtered').count()) === 1)
    check('无命中时不渲染表格行', (await page.locator('.ls-userspage__row').count()) === 0)
    await page.fill('#users-search-input', '')
    await page.waitForTimeout(150)

    // 3) 翻页：第 2 页 2 行
    await page.click('#users-next-page')
    await page.waitForTimeout(150)
    check('第 2 页渲染 2 行', (await page.locator('.ls-userspage__row').count()) === 2)
    check(
      '分页信息为第 2 / 2 页',
      /2\s*\/\s*2/.test(await page.locator('#users-pager-info').innerText()),
      await page.locator('#users-pager-info').innerText(),
    )
    check('最后一页禁用下一页', await page.locator('#users-next-page').isDisabled())
    await page.click('#users-prev-page')
    await page.waitForTimeout(150)

    // 4) 启用/停用：bob 从停用变启用
    const bobToggle = page.locator('[data-user-id="u-bob"] .ls-userspage__toggle')
    check('停用用户按钮文案为「启用」', (await bobToggle.innerText()).trim() === '启用', await bobToggle.innerText())
    await bobToggle.click()
    await page.waitForTimeout(300)
    check(
      '启用后状态 chip 变为启用',
      (await page.locator('[data-user-id="u-bob"] .ls-adminpage__status').innerText()).trim() === '启用',
      await page.locator('[data-user-id="u-bob"] .ls-adminpage__status').innerText(),
    )
    const bobPatch = state.patchCalls.find((call) => call.id === 'u-bob')
    check('启用请求 PATCH 带 disabled=false', bobPatch?.body?.disabled === false, JSON.stringify(bobPatch?.body))

    // 5) 解锁：heidi 在第二页，先搜索到它；解锁后失败次数提示消失
    await page.fill('#users-search-input', 'heidi')
    await page.waitForTimeout(150)
    check('锁定用户有解锁按钮', (await page.locator('[data-user-id="u-heidi"] .ls-userspage__unlock').count()) === 1)
    await page.click('[data-user-id="u-heidi"] .ls-userspage__unlock')
    await page.waitForTimeout(300)
    check('解锁后不再显示锁定态', (await page.locator('[data-user-id="u-heidi"] .ls-adminpage__status--locked').count()) === 0)
    check('解锁接口被调用一次', state.unlockCalls === 1, `${state.unlockCalls} 次`)
    await page.fill('#users-search-input', '')
    await page.waitForTimeout(150)

    // 6) 重置密码：弹窗输入明文，成功后明文不留在 DOM
    await page.click('[data-user-id="u-carol"] .ls-userspage__reset')
    await page.waitForSelector('#users-reset-password', { timeout: 5000 })
    check('重置密码弹窗带用户说明', (await page.locator('.ls-modal__desc').innerText()).includes('carol'))
    await page.fill('#users-reset-password', RESET_SECRET)
    await page.click('#users-reset-confirm')
    await page.waitForSelector('#users-reset-form', { state: 'detached', timeout: 5000 })
    const carolPatch = state.patchCalls.filter((call) => call.id === 'u-carol').at(-1)
    check('重置请求 PATCH 携带新密码字段', carolPatch?.body?.password === RESET_SECRET, JSON.stringify(carolPatch?.body))
    const domAfterReset = `${await page.content()}\n${await page.evaluate(() => document.body.innerText)}`
    check('明文密码未写入 DOM', !domAfterReset.includes(RESET_SECRET), domAfterReset.includes(RESET_SECRET) ? 'DOM 中检索到明文' : '')
    check('重置成功后有可见提示', (await page.locator('#users-action-notice').count()) === 1)

    // 7) 删除：取消不发请求；确认后行消失
    await page.click('[data-user-id="u-alice"] .ls-userspage__delete')
    await page.waitForSelector('#users-delete-confirm', { timeout: 5000 })
    check('删除弹窗展示影响说明', (await page.locator('.ls-modal__body').innerText()).includes('不可恢复'))
    await page.click('#users-delete-cancel')
    await page.waitForSelector('#users-delete-confirm', { state: 'detached', timeout: 5000 })
    check('取消删除不发 DELETE 请求', state.deleteCalls.length === 0, state.deleteCalls.join(','))
    check('取消删除后行仍在', (await page.locator('[data-user-id="u-alice"]').count()) === 1)

    await page.click('[data-user-id="u-alice"] .ls-userspage__delete')
    await page.waitForSelector('#users-delete-confirm', { timeout: 5000 })
    await page.click('#users-delete-confirm')
    await page.waitForSelector('[data-user-id="u-alice"]', { state: 'detached', timeout: 5000 })
    check('确认删除发出 DELETE 请求', state.deleteCalls[0] === 'u-alice', state.deleteCalls.join(','))

    // 8) Esc 关闭创建弹窗并归还焦点
    await page.click('#users-create-open')
    await page.waitForSelector('#users-create-username', { timeout: 5000 })
    await page.keyboard.press('Escape')
    await page.waitForSelector('#users-create-form', { state: 'detached', timeout: 5000 })
    const focusBack = await page.evaluate(() => document.activeElement && document.activeElement.id)
    check('Esc 关闭弹窗后焦点回到触发按钮', focusBack === 'users-create-open', String(focusBack))

    // 9) 320px：列表与弹窗都不得横向溢出
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(150)
    const narrow = await page.evaluate(() => ({
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      deleteVisible: (() => {
        const button = document.querySelector('[data-user-id="u-bob"] .ls-userspage__delete')
        if (!button) return false
        const rect = button.getBoundingClientRect()
        return rect.width > 0 && rect.height >= 24 && rect.right <= window.innerWidth + 1
      })(),
    }))
    check('320px 列表无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 操作按钮可见且命中区足够', narrow.deleteVisible)

    await page.click('#users-create-open')
    await page.waitForSelector('#users-create-username', { timeout: 5000 })
    const modalOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    check('320px 弹窗无横向滚动', modalOverflow <= 0, `溢出 ${modalOverflow}px`)
    await page.keyboard.press('Escape')

    // 10) 无 user.manage：路由守卫拦截，不请求用户列表
    const callsBeforeForbidden = state.userListCalls
    state.permissions = ['server.read']
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(800)
    check(
      '无 user.manage 被重定向到 /forbidden',
      new URL(page.url()).hash.startsWith('#/forbidden'),
      new URL(page.url()).hash,
    )
    check(
      '无权限时不请求用户列表',
      state.userListCalls === callsBeforeForbidden,
      `${callsBeforeForbidden} → ${state.userListCalls}`,
    )
    check('无权限时不渲染用户表格', (await page.locator('.ls-userspage__row').count()) === 0)

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
    `\n${failed.length === 0 ? '✓ 用户页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
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
