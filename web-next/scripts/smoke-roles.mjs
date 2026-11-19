#!/usr/bin/env node
/**
 * 角色与权限页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：内置/自定义角色列表与「内置不可删」/ 新建自定义角色（24 权限点、6 个 admin 专属置灰并标注原因）/
 * 权限与范围随请求体提交 / 编辑回填 / 删除二次确认 / 320px 无横向滚动 /
 * 无 role.manage 时路由守卫拦截且不请求角色列表 / 无未捕获异常。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-roles.mjs [--headed]
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

const ADMIN_ONLY = new Set(['user.manage', 'role.manage', 'token.manage', 'enroll.manage', 'system.manage', 'audit.read'])
const PERMISSION_NAMES = {
  'server.read': '查看服务器',
  'server.write': '编辑服务器',
  'server.delete': '删除服务器',
  'terminal.use': '使用终端',
  'check.read': '查看检查',
  'check.write': '编辑检查',
  'alert.read': '查看告警',
  'alert.write': '编辑告警',
  'deploy.read': '查看部署',
  'deploy.write': '发布部署',
  'backup.read': '查看备份',
  'backup.write': '创建备份',
  'backup.delete': '删除备份',
  'backup.restore': '恢复备份',
  'task.read': '查看任务',
  'task.write': '编辑任务',
  'group.write': '编辑分组',
  'agent.upgrade': '升级 Agent',
  'user.manage': '管理用户',
  'role.manage': '管理角色',
  'token.manage': '管理令牌',
  'enroll.manage': '管理纳管',
  'system.manage': '系统设置',
  'audit.read': '查看审计',
}
const PERMISSIONS = Object.keys(PERMISSION_NAMES).map((key) => ({
  key,
  name: PERMISSION_NAMES[key],
  description: '',
  admin_only: ADMIN_ONLY.has(key),
}))

function roleFixture(overrides) {
  return {
    key: 'custom-placeholder',
    name: '占位角色',
    description: '',
    builtin: false,
    permissions: ['server.read'],
    scope: { mode: 'all', group_ids: [], server_ids: [] },
    user_count: 0,
    created_at: Date.now() - 3_600_000,
    updated_at: null,
    ...overrides,
  }
}

const ROLES = [
  roleFixture({
    key: 'admin',
    name: '管理员',
    description: '内置管理员，拥有全部权限',
    builtin: true,
    permissions: Object.keys(PERMISSION_NAMES),
    scope: { mode: 'all', group_ids: [], server_ids: [] },
    user_count: 1,
    created_at: Date.now() - 86_400_000,
  }),
  roleFixture({
    key: 'operator',
    name: '运维',
    description: '内置运维',
    builtin: true,
    permissions: ['server.read', 'check.read'],
    scope: { mode: 'all', group_ids: [], server_ids: [] },
    user_count: 0,
    created_at: Date.now() - 86_400_000,
  }),
  roleFixture({
    key: 'viewer',
    name: '只读',
    description: '内置只读',
    builtin: true,
    permissions: ['server.read'],
    scope: { mode: 'all', group_ids: [], server_ids: [] },
    user_count: 0,
    created_at: Date.now() - 86_400_000,
  }),
  roleFixture({
    key: 'release-ops',
    name: '发布运维',
    description: '自定义：仅发布与回滚',
    builtin: false,
    permissions: ['deploy.read', 'deploy.write'],
    scope: { mode: 'groups', group_ids: ['grp-default'], server_ids: [] },
    user_count: 2,
    created_at: Date.now() - 7_200_000,
  }),
]

const GROUPS = [
  { id: 'grp-default', name: '默认分组', description: '', member_count: 2, created_at: null, updated_at: null },
  { id: 'grp-test', name: '测试分组', description: '', member_count: 1, created_at: null, updated_at: null },
]

const SERVERS = [
  { id: 'srv-alpha', name: 'alpha-node', host: '10.0.0.11', port: 22, mode: 'agent', status: 'online', group: '默认分组', group_ids: ['grp-default'], group_names: ['默认分组'], tags: [], os: 'linux', arch: 'amd64' },
  { id: 'srv-beta', name: 'beta-node', host: '10.0.0.12', port: 22, mode: 'agent', status: 'offline', group: '测试分组', group_ids: ['grp-test'], group_names: ['测试分组'], tags: [], os: 'linux', arch: 'amd64' },
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

/** 假后端：只覆盖角色页需要的端点，其他一律 404，避免静默放过。 */
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
      state.roleListCalls += 1
      return json(200, { data: state.roles.map((role) => ({ ...role, scope: { ...role.scope } })) })
    }
    if (path === '/api/v1/roles' && method === 'POST') {
      const body = request.postDataJSON() || {}
      state.createBodies.push(body)
      const key = String(body.name || 'role')
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/(^-|-$)/g, '')
      const created = roleFixture({
        key: key || `role-${state.roles.length}`,
        name: String(body.name || ''),
        description: String(body.description || ''),
        builtin: false,
        permissions: Array.isArray(body.permissions) ? body.permissions : [],
        scope: body.scope || { mode: 'all', group_ids: [], server_ids: [] },
        user_count: 0,
        created_at: Date.now(),
      })
      state.roles.push(created)
      return json(200, { data: created })
    }
    const roleDetail = /^\/api\/v1\/roles\/([^/]+)$/.exec(path)
    if (roleDetail && method === 'PATCH') {
      const key = decodeURIComponent(roleDetail[1])
      const body = request.postDataJSON() || {}
      state.patchBodies.push({ key, body })
      const found = state.roles.find((role) => role.key === key)
      if (!found) return json(404, { error: { code: 'not_found', message: '角色不存在' } })
      Object.assign(found, body, { updated_at: Date.now() })
      return json(200, { data: found })
    }
    if (roleDetail && method === 'DELETE') {
      const key = decodeURIComponent(roleDetail[1])
      state.deleteCalls.push(key)
      state.roles = state.roles.filter((role) => role.key !== key)
      return noContent()
    }
    if (path === '/api/v1/permissions' && method === 'GET') {
      return json(200, { data: PERMISSIONS })
    }
    if (path === '/api/v1/server-groups' && method === 'GET') {
      return json(200, { data: GROUPS })
    }
    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, { data: SERVERS })
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
    permissions: ['role.manage', 'user.manage', 'server.read'],
    roles: ROLES.map((role) => ({ ...role, scope: { ...role.scope } })),
    roleListCalls: 0,
    createBodies: [],
    patchBodies: [],
    deleteCalls: [],
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
    // 1) 登录进入 /roles：列表、内置与自定义的行级差异
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', LOGIN_PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.goto(`${base}/#/roles`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#roles-create-open', { timeout: 15000 })
    await page.waitForSelector('[data-role-key="release-ops"]', { timeout: 15000 })

    check('登录后进入角色页', new URL(page.url()).hash === '#/roles', new URL(page.url()).hash)
    check('角色接口只拉一次', state.roleListCalls === 1, `${state.roleListCalls} 次`)
    check('列表渲染 4 个角色', (await page.locator('.ls-rolespage__row').count()) === 4)
    check(
      '计数文案显示总数',
      (await page.locator('#roles-count').innerText()).replace(/\s+/g, ' ').trim() === '共 4 个角色',
      await page.locator('#roles-count').innerText(),
    )
    check('内置角色行标记 builtin', (await page.locator('.ls-rolespage__row--builtin').count()) === 3)
    check(
      '内置角色没有删除/编辑按钮',
      (await page.locator('.ls-rolespage__row--builtin .ls-rolespage__delete').count()) === 0 &&
        (await page.locator('.ls-rolespage__row--builtin .ls-rolespage__edit').count()) === 0,
    )
    check(
      '内置角色展示不可改删说明',
      (await page.locator('[data-role-key="admin"] .ls-rolespage__builtin-hint').innerText()).includes('不可修改'),
      await page.locator('[data-role-key="admin"] .ls-rolespage__builtin-hint').innerText(),
    )
    check(
      '内置角色类型列标注「内置」',
      (await page.locator('[data-role-key="admin"] .ls-chip').innerText()).trim() === '内置',
      await page.locator('[data-role-key="admin"] .ls-chip').innerText(),
    )
    check('自定义角色有编辑与删除按钮', (await page.locator('[data-role-key="release-ops"] .ls-rolespage__delete').count()) === 1)

    // 2) 新建弹窗：24 个权限点、6 个 admin 专属置灰并带原因
    await page.click('#roles-create-open')
    await page.waitForSelector('#roles-form-name', { timeout: 5000 })
    check('权限点总数为 24', (await page.locator('input[data-permission-key]').count()) === 24)
    check(
      'admin 专属权限 6 个且全部 disabled',
      (await page.locator('input[data-admin-only="true"]').count()) === 6 &&
        (await page.locator('input[data-admin-only="true"]:disabled').count()) === 6,
    )
    check(
      '置灰权限标注原因（后端 400）',
      (await page.locator('.ls-adminpage__perm-reason').first().innerText()).includes('400'),
      await page.locator('.ls-adminpage__perm-reason').first().innerText(),
    )
    check('可授予权限计数为 0 / 18', (await page.locator('#roles-permission-count').innerText()).includes('0 / 18'))

    // 3) 选择 scope=servers、两个普通权限与一台服务器后提交
    await page.click('#roles-scope-servers')
    check('servers 模式不显示分组选择器', (await page.locator('#roles-group-search').count()) === 0)
    await page.fill('#roles-form-name', 'Smoke Ops')
    await page.fill('#roles-form-description', '冒烟创建的自定义角色')
    await page.check('input[data-permission-key="server.read"]')
    await page.check('input[data-permission-key="check.read"]')
    await page.check('input[data-server-id="srv-alpha"]')
    check('勾选后已选计数为 2 / 18', (await page.locator('#roles-permission-count').innerText()).includes('2 / 18'))
    await page.click('#roles-save')
    await page.waitForSelector('#roles-form', { state: 'detached', timeout: 5000 })
    await page.waitForSelector('[data-role-key="smoke-ops"]', { timeout: 5000 })

    const created = state.createBodies.at(-1) || {}
    check(
      '新建请求体权限不含 admin 专属项',
      Array.isArray(created.permissions) &&
        created.permissions.length === 2 &&
        created.permissions.every((key) => !ADMIN_ONLY.has(key)),
      JSON.stringify(created.permissions),
    )
    check(
      '新建请求体范围为 servers/srv-alpha',
      created.scope?.mode === 'servers' &&
        Array.isArray(created.scope.server_ids) &&
        created.scope.server_ids.length === 1 &&
        created.scope.server_ids[0] === 'srv-alpha' &&
        Array.isArray(created.scope.group_ids) &&
        created.scope.group_ids.length === 0,
      JSON.stringify(created.scope),
    )
    check('创建后列表出现新角色', (await page.locator('.ls-rolespage__row').count()) === 5)
    check('创建成功后有可见提示', (await page.locator('#roles-action-notice').count()) === 1)

    // 4) 编辑回填：打开自定义角色
    await page.click('[data-role-key="release-ops"] .ls-rolespage__edit')
    await page.waitForSelector('#roles-form-name', { timeout: 5000 })
    check('编辑弹窗回填角色名', (await page.inputValue('#roles-form-name')) === '发布运维')
    check('编辑弹窗回填范围模式', (await page.locator('#roles-scope-groups').getAttribute('aria-pressed')) === 'true')
    await page.click('#roles-cancel')
    await page.waitForSelector('#roles-form', { state: 'detached', timeout: 5000 })

    // 5) 删除自定义角色：取消不发请求，确认才发
    await page.click('[data-role-key="smoke-ops"] .ls-rolespage__delete')
    await page.waitForSelector('#roles-delete-confirm', { timeout: 5000 })
    check('删除弹窗写明影响', (await page.locator('.ls-modal__body').innerText()).includes('用户引用'))
    await page.click('#roles-delete-cancel')
    await page.waitForSelector('#roles-delete-confirm', { state: 'detached', timeout: 5000 })
    check('取消删除不发 DELETE', state.deleteCalls.length === 0, state.deleteCalls.join(','))
    check('取消后行仍在', (await page.locator('[data-role-key="smoke-ops"]').count()) === 1)

    await page.click('[data-role-key="smoke-ops"] .ls-rolespage__delete')
    await page.waitForSelector('#roles-delete-confirm', { timeout: 5000 })
    await page.click('#roles-delete-confirm')
    await page.waitForSelector('[data-role-key="smoke-ops"]', { state: 'detached', timeout: 5000 })
    check('确认删除发出 DELETE', state.deleteCalls[0] === 'smoke-ops', state.deleteCalls.join(','))

    // 6) 320px：列表与新建弹窗都不得横向溢出
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(150)
    const narrow = await page.evaluate(() => ({
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      deleteVisible: (() => {
        const button = document.querySelector('[data-role-key="release-ops"] .ls-rolespage__delete')
        if (!button) return false
        const rect = button.getBoundingClientRect()
        return rect.width > 0 && rect.height >= 24 && rect.right <= window.innerWidth + 1
      })(),
    }))
    check('320px 列表无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 操作按钮可见且命中区足够', narrow.deleteVisible)

    await page.click('#roles-create-open')
    await page.waitForSelector('#roles-form-name', { timeout: 5000 })
    const modalOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    check('320px 新建弹窗无横向滚动', modalOverflow <= 0, `溢出 ${modalOverflow}px`)
    await page.keyboard.press('Escape')
    await page.waitForSelector('#roles-form', { state: 'detached', timeout: 5000 })

    // 7) 无 role.manage：路由守卫拦截，不请求角色列表
    const callsBeforeForbidden = state.roleListCalls
    state.permissions = ['server.read']
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(800)
    check(
      '无 role.manage 被重定向到 /forbidden',
      new URL(page.url()).hash.startsWith('#/forbidden'),
      new URL(page.url()).hash,
    )
    check(
      '无权限时不请求角色列表',
      state.roleListCalls === callsBeforeForbidden,
      `${callsBeforeForbidden} → ${state.roleListCalls}`,
    )
    check('无权限时不渲染角色表格', (await page.locator('.ls-rolespage__row').count()) === 0)

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
    `\n${failed.length === 0 ? '✓ 角色页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
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
