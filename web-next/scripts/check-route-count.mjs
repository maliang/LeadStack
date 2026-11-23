#!/usr/bin/env node
/**
 * 路由表回归校验（无依赖、无需构建）。
 *
 * 背景：路由从 src/router/index.ts 拆到 src/router/routes/{servers,tools,deploy,system,misc}.ts 后，
 * 「总数 / 每条 name+path+meta / 懒加载 / 守卫」这些跨模块契约没有类型能兜住，必须逐条断言。
 *
 * 直接 import 汇总模块 src/router/routes/index.ts（Node 原生 TS 类型擦除）取真实 routes 数组，
 * 不是正则数文件，避免「模块写了但没拼进 routes」这类假通过。
 *
 * 口径：
 * - 业务路由 = 菜单可达页 + 详情页 + /templates + /settings（22 条，与 src/lib/nav.ts 的 NAV_GROUPS 条目数一致）；
 * - 独立页/兜底 = /login /forbidden /about /:pathMatch(.*)*（4 条）；
 * - 注册总数 26 条。
 *
 * 用法：
 *   node scripts/check-route-count.mjs          # 断言业务路由 22 条
 *   node scripts/check-route-count.mjs 23       # 显式给预期业务路由数（验收命令用）
 *
 * 退出码：0 全部一致 / 1 任一断言失败。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const root = resolve(scriptDir, '..')
const ROUTES_DIR = resolve(root, 'src/router/routes')
const ROUTER_FILE = resolve(root, 'src/router/index.ts')

/** 注册总数：26 条（22 业务 + 4 独立页/兜底）。 */
const EXPECTED_TOTAL = 26
/** 独立页与兜底：不挂菜单、不计入业务路由。 */
const MISC_PATHS = new Set(['/login', '/forbidden', '/about', '/:pathMatch(.*)*'])

/** 逐条基线：[path, name, meta.titleKey, meta.permission, meta.toolKind]。 */
const BASELINE = [
  ['/', 'home', 'nav.overview', 'server.read', null],
  ['/servers', 'servers', 'nav.servers', 'server.read', null],
  ['/servers/:id', 'server-detail', 'crumb.serverDetail', 'server.read', null],
  ['/checks', 'checks', 'nav.checks', 'check.read', null],
  ['/alerts', 'alerts', 'nav.alerts', 'alert.read', null],
  ['/terminal', 'terminal', 'nav.terminal', 'terminal.use', 'terminal'],
  ['/files', 'files', 'nav.files', 'terminal.use', 'files'],
  ['/logs', 'logs', 'nav.logs', 'terminal.use', 'logs'],
  ['/scheduled-tasks', 'scheduled-tasks', 'nav.scheduledTasks', 'task.read', null],
  ['/import', 'import', 'nav.import', 'server.write', null],
  ['/apps', 'apps', 'nav.apps', 'deploy.read', null],
  ['/apps/:id', 'app-detail', 'crumb.appDetail', 'deploy.read', null],
  ['/releases', 'releases', 'nav.releases', 'deploy.read', null],
  ['/releases/:id', 'release-detail', 'crumb.releaseDetail', 'deploy.read', null],
  ['/templates', 'templates', 'nav.templates', 'deploy.read', null],
  ['/users', 'users', 'nav.users', 'user.manage', null],
  ['/roles', 'roles', 'nav.roles', 'role.manage', null],
  ['/groups', 'groups', 'nav.groups', 'server.read', null],
  ['/tokens', 'tokens', 'nav.tokens', 'token.manage', null],
  ['/audit', 'audit', 'nav.audit', 'audit.read', null],
  ['/backups', 'backups', 'nav.backups', 'backup.read', null],
  ['/settings', 'settings', 'nav.settings', null, null],
  ['/login', 'login', 'shell.title.login', null, null],
  ['/forbidden', 'forbidden', 'shell.title.forbidden', null, null],
  ['/about', 'about', 'shell.title.about', null, null],
  ['/:pathMatch(.*)*', 'not-found', 'shell.title.notFound', null, null],
]

const failures = []
function check(ok, message) {
  if (!ok) failures.push(message)
}

const arg = process.argv[2]
const expectedBusiness = arg === undefined ? 22 : Number(arg)
if (!Number.isInteger(expectedBusiness) || expectedBusiness <= 0) {
  console.error(`check:routes 参数错误：预期业务路由数必须是正整数，收到 ${JSON.stringify(arg)}`)
  process.exit(1)
}

const { routes } = await import(pathToFileURL(resolve(ROUTES_DIR, 'index.ts')).href)

if (!Array.isArray(routes)) {
  console.error('check:routes 解析失败：src/router/routes/index.ts 没有导出 routes 数组')
  process.exit(1)
}

const business = routes.filter((route) => !MISC_PATHS.has(route.path))
const misc = routes.filter((route) => MISC_PATHS.has(route.path))

check(
  routes.length === EXPECTED_TOTAL,
  `注册总数应为 ${EXPECTED_TOTAL}，实际 ${routes.length}（业务 ${business.length} + 独立页/兜底 ${misc.length}）`,
)
check(
  business.length === expectedBusiness,
  `业务路由应为 ${expectedBusiness}，实际 ${business.length}`,
)
check(misc.length === MISC_PATHS.size, `独立页/兜底应为 ${MISC_PATHS.size}，实际 ${misc.length}`)

// ① 逐条基线：path / name / meta.titleKey / meta.permission / meta.toolKind 全等。
const actualByPath = new Map()
for (const route of routes) {
  if (actualByPath.has(route.path)) failures.push(`path 重复：${route.path}`)
  actualByPath.set(route.path, route)
}
const baselinePaths = new Set(BASELINE.map(([path]) => path))
for (const [path] of BASELINE) {
  const route = actualByPath.get(path)
  if (!route) {
    failures.push(`路由缺失：${path}`)
    continue
  }
  const meta = route.meta ?? {}
  const actual = [route.path, route.name, meta.titleKey, meta.permission ?? null, meta.toolKind ?? null]
  const expected = BASELINE.find(([p]) => p === path)
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    failures.push(`路由字段漂移：期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`)
  }
}
for (const route of routes) {
  if (!baselinePaths.has(route.path)) failures.push(`多出未登记路由：${route.path}（改契约需同步本脚本基线）`)
}

const names = new Set()
for (const route of routes) {
  if (names.has(route.name)) failures.push(`name 重复：${String(route.name)}`)
  names.add(route.name)
}

// ② /settings 必须仍然没有路由级权限门（页内自判 system.manage）。
const settings = actualByPath.get('/settings')
check(Boolean(settings), '/settings 路由缺失')
if (settings) {
  check(
    settings.meta?.permission === undefined,
    `/settings 不应有 meta.permission，实际 ${JSON.stringify(settings.meta?.permission)}`,
  )
}

// ③ 懒加载不得退化为同步 import：组件必须是返回动态 import 的函数。
const notLazy = routes.filter((route) => typeof route.component !== 'function').map((route) => route.path)
check(notLazy.length === 0, `组件不是懒加载函数：${notLazy.join(', ')}（不得退化为同步 import）`)

const moduleFiles = readdirSync(ROUTES_DIR).filter((name) => name.endsWith('.ts'))
const moduleSources = new Map(
  moduleFiles.map((name) => [name, readFileSync(resolve(ROUTES_DIR, name), 'utf8')]),
)
const staticPageImports = []
for (const [name, source] of moduleSources) {
  for (const line of source.split('\n')) {
    if (/^\s*import\s.+\sfrom\s+'@\/(pages|views)\//.test(line)) staticPageImports.push(`${name}: ${line.trim()}`)
  }
}
check(
  staticPageImports.length === 0,
  `发现同步页面 import（必须改回 () => import(...)）：\n    ${staticPageImports.join('\n    ')}`,
)

// ④ index.ts 只留守卫与实例：不得重新内联 route 记录，守卫关键行为不得丢。
const routerSource = readFileSync(ROUTER_FILE, 'utf8')
check(
  !/const\s+routes\s*:\s*RouteRecordRaw\[\]/.test(routerSource),
  'src/router/index.ts 又内联了 routes 数组（应只做 import 与拼装）',
)
check(routerSource.includes('await ensureSession()'), '守卫缺少 await ensureSession()（刷新直达目标页会失效）')
check(
  routerSource.includes("name: 'forbidden', query: { from: to.fullPath }"),
  '守卫缺少 /forbidden?from= 回填',
)
check(
  routerSource.includes("name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath }"),
  '守卫缺少未登录 redirect 回填',
)
check(
  routerSource.includes("typeof required === 'string' && required !== '' && !can(required)"),
  '守卫的 meta.permission 判定被改写',
)

console.log('check:routes')
console.log(`  业务路由 ${business.length} 条（预期 ${expectedBusiness}）`)
console.log(`  独立页/兜底 ${misc.length} 条：${[...MISC_PATHS].join(' ')}`)
console.log(`  注册总数 ${routes.length} 条（预期 ${EXPECTED_TOTAL}）`)
console.log(
  `  逐条基线 ${BASELINE.length} 条 / 懒加载 ${routes.length - notLazy.length}/${routes.length}`,
)
console.log(`  /settings meta.permission = ${JSON.stringify(settings?.meta?.permission ?? null)}（必须为 null）`)

if (failures.length > 0) {
  console.error(`发现 ${failures.length} 处不一致：`)
  for (const failure of failures) console.error(`  - ${failure}`)
  process.exit(1)
}

console.log('  全部断言通过，退出码 0')
