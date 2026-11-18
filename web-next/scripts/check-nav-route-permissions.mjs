#!/usr/bin/env node
/**
 * 菜单权限 × 路由权限一致性校验（静态解析，无依赖，不需要构建）。
 *
 * 背景：左侧菜单权限点写在 src/lib/nav.ts 的 NAV_GROUPS[].permission，
 * 路由权限门写在 src/router/index.ts 的 route.meta.permission，两份定义各自维护、
 * 没有任何一致性断言。漂移会变成两种线上故障：
 *   ① 菜单有 permission、路由没门  → 菜单看不到，但直接输地址 / 分享链接就能进；
 *   ② 路由有 permission、菜单没有  → 菜单看得见，点进去被踢到 /forbidden。
 * 本脚本逐条比对，两个方向都判，覆盖率 100%（菜单每一项、路由每一条都必须归类）。
 *
 * 唯一白名单：/settings 刻意不设路由门（设置页对所有人可见，页内自判
 * can('system.manage')），见 src/router/index.ts 该条目的注释。脚本显式输出这条例外，
 * 且只在「菜单与路由都没有 permission」时放过；一旦某一侧改了口径仍会报出来。
 *
 * 用法：
 *   node scripts/check-nav-route-permissions.mjs
 *   node scripts/check-nav-route-permissions.mjs --root /path/to/web-next   # 变异测试用
 *
 * 退出码：0 一致 / 1 有 mismatch（含解析失败与覆盖缺口）。
 */
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const argRoot = process.argv.indexOf('--root')
const root = argRoot !== -1 && process.argv[argRoot + 1]
  ? resolve(process.argv[argRoot + 1])
  : resolve(scriptDir, '..')

const NAV_FILE = resolve(root, 'src/lib/nav.ts')
const ROUTER_FILE = resolve(root, 'src/router/index.ts')

/** 只有这一条允许「菜单与路由都不设权限门」，且必须两侧一致。 */
const KNOWN_UNGUARDED = new Map([
  ['/settings', '设置页刻意不设路由门（页内自判 can(\'system.manage\')），菜单项同样不带 permission'],
])

/** 不在菜单里、也不该有权限门的独立页面。 */
const NON_MENU_ROUTES = new Set(['/login', '/forbidden', '/about'])

function fail(message) {
  console.error(`check:nav 解析失败：${message}`)
  process.exit(1)
}

function readSource(file, label) {
  try {
    return readFileSync(file, 'utf8')
  } catch (error) {
    fail(`读不到 ${label}（${file}）：${error.message}`)
  }
}

/** 取 `[` 起始的数组/对象字面量到 `stopMarker` 之前的片段，避免扫到文件其余部分。 */
function sliceBlock(source, startMarker, stopMarker, label) {
  const start = source.indexOf(startMarker)
  if (start === -1) fail(`${label} 中找不到起始标记 ${JSON.stringify(startMarker)}`)
  const stop = source.indexOf(stopMarker, start)
  if (stop === -1) fail(`${label} 中找不到结束标记 ${JSON.stringify(stopMarker)}`)
  return source.slice(start, stop)
}

function parseNav(source) {
  const block = sliceBlock(source, 'export const NAV_GROUPS', '\n]\n', 'src/lib/nav.ts')
  const items = []
  // 菜单项对象没有嵌套花括号，取 `{ ... }` 即可；多行写法（/scheduled-tasks）同样命中。
  for (const match of block.matchAll(/\{\s*to:\s*'([^']+)'[\s\S]*?\}/g)) {
    const to = match[1]
    const body = match[0]
    const permission = body.match(/permission:\s*'([^']+)'/)
    items.push({ to, permission: permission ? permission[1] : null })
  }
  if (items.length === 0) fail('src/lib/nav.ts 没有解析出任何菜单项')
  return items
}

function parseRoutes(source) {
  const block = sliceBlock(source, 'const routes: RouteRecordRaw[] = [', '\n]\n', 'src/router/index.ts')
  const routes = []
  const pathMatches = [...block.matchAll(/path:\s*'([^']+)'/g)]
  pathMatches.forEach((match, index) => {
    const next = pathMatches[index + 1]
    // 路由对象内 permission 只出现一次；下一处 path 之前都属于本条路由。
    const body = block.slice(match.index, next ? next.index : block.length)
    const permission = body.match(/permission:\s*'([^']+)'/)
    const isPublic = /public:\s*true/.test(body)
    routes.push({ path: match[1], permission: permission ? permission[1] : null, isPublic })
  })
  if (routes.length === 0) fail('src/router/index.ts 没有解析出任何路由')
  return routes
}

const navItems = parseNav(readSource(NAV_FILE, '菜单定义'))
const routes = parseRoutes(readSource(ROUTER_FILE, '路由定义'))

const navByPath = new Map()
for (const item of navItems) {
  if (navByPath.has(item.to)) fail(`菜单项 ${item.to} 重复定义`)
  navByPath.set(item.to, item)
}

const routeByPath = new Map()
for (const route of routes) {
  if (routeByPath.has(route.path)) fail(`路由 ${route.path} 重复定义（静态比对需要唯一 path）`)
  routeByPath.set(route.path, route)
}

const mismatches = []
const accountedRoutes = new Set()

/** 覆盖统计：菜单 / 详情 / 独立页 / 兜底。 */
const stats = { menu: 0, detail: 0, standalone: 0, catchAll: 0 }

for (const item of navItems) {
  stats.menu += 1
  const route = routeByPath.get(item.to)
  if (!route) {
    mismatches.push(`[缺失] 菜单项 ${item.to}（permission=${item.permission ?? '无'}）在路由表中不存在`)
    continue
  }
  accountedRoutes.add(route.path)

  const navPerm = item.permission
  const routePerm = route.permission
  const known = KNOWN_UNGUARDED.get(item.to)

  if (navPerm === routePerm) continue

  if (known && !navPerm && !routePerm) continue // 白名单仅覆盖「两侧都没有门」

  // ① 菜单有权限点、路由没有 → 权限门缺失，任何人直达都能进。
  if (navPerm && !routePerm) {
    mismatches.push(
      `[①权限门缺失] ${item.to}：nav.permission=${navPerm}，route.meta.permission=无 → 任何人都能直接输地址进入`,
    )
    continue
  }

  // ② 路由有权限点、菜单没有 → 菜单可见性与准入不一致（点进去被踢 /forbidden）。
  if (!navPerm && routePerm) {
    mismatches.push(
      `[②菜单与门不一致] ${item.to}：route.meta.permission=${routePerm}，nav.permission=无 → 菜单不按该权限过滤，无权限用户点进去会被踢`,
    )
    continue
  }

  mismatches.push(
    `[权限点不一致] ${item.to}：nav.permission=${navPerm}，route.meta.permission=${routePerm} → 菜单可见性与准入判定使用不同权限点`,
  )
}

for (const route of routes) {
  if (accountedRoutes.has(route.path)) continue

  if (route.path.includes('pathMatch')) {
    stats.catchAll += 1
    if (route.permission) {
      mismatches.push(`[②菜单与门不一致] ${route.path}：兜底路由不应带 permission（实际 ${route.permission}）`)
    }
    continue
  }

  // 详情页（带 :param）没有独立菜单项，但必须挂在一个菜单可见的父路径下。
  if (route.path.includes(':')) {
    stats.detail += 1
    const parent = route.path.split('/:')[0] || '/'
    if (!navByPath.has(parent)) {
      mismatches.push(`[覆盖缺口] 详情路由 ${route.path} 的父路径 ${parent} 不在菜单中，权限语义无法对照`)
    }
    continue
  }

  if (NON_MENU_ROUTES.has(route.path)) {
    stats.standalone += 1
    if (route.permission) {
      mismatches.push(`[②菜单与门不一致] ${route.path}：独立页不该带 permission（实际 ${route.permission}）`)
    }
    continue
  }

  // 有菜单语义的路由却查不到菜单项：双向覆盖失败。
  stats.standalone += 1
  if (route.permission) {
    mismatches.push(
      `[②菜单与门不一致] ${route.path}：route.meta.permission=${route.permission}，菜单中没有对应项 → 菜单与门不一致`,
    )
  } else {
    mismatches.push(`[覆盖缺口] 路由 ${route.path} 既没有菜单项，也不属于独立页/详情页/白名单`)
  }
}

for (const [path, reason] of KNOWN_UNGUARDED) {
  const route = routeByPath.get(path)
  const item = navByPath.get(path)
  const bothUnguarded = Boolean(route) && Boolean(item) && !route.permission && !item.permission
  // 白名单只在「两侧确实都没有门」时成立；条件变了就走正常比对（上面的循环会报出来）。
  if (!bothUnguarded) {
    mismatches.push(`[白名单失效] ${path} 已不满足「两侧都无 permission」，请移除白名单并修复：${reason}`)
  }
}

const compared = navItems.length + (routes.length - accountedRoutes.size)
const totalChecked = routes.length

console.log(`check:nav root=${root}`)
console.log(`  菜单 ${navItems.length} 条 / 路由 ${routes.length} 条`)
console.log(
  `  覆盖：菜单命中路由 ${stats.menu} 条；路由归类 详情页 ${stats.detail} / 独立页 ${stats.standalone} / 兜底 ${stats.catchAll}`,
)
console.log(
  `  已知例外 /settings：${KNOWN_UNGUARDED.get('/settings')}（两侧均不带 permission，已显式放行，不是忽略差异）`,
)

if (mismatches.length > 0) {
  console.error('  发现不一致：')
  for (const item of mismatches) console.error(`  - ${item}`)
}

console.log(`比对 ${compared} 条，mismatch ${mismatches.length}（路由条目 ${totalChecked} 条已全部归类）`)
process.exit(mismatches.length === 0 ? 0 : 1)
