import { createRouter, createWebHashHistory, type RouteLocationNormalized } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { routes } from './routes'

/**
 * 路由实例 + 会话/权限守卫。
 *
 * 拆分约定（为并行开发解锁）：路由记录按领域放在 src/router/routes/*.ts
 * （servers / tools / deploy / system / misc），本文件只负责：
 * RouteMeta 契约、路由实例、登录与权限守卫、文档标题。
 * 新增页面只改对应领域文件，不要在这里再堆 route 记录。
 *
 * 守卫约定（与旧版 web/src/App.tsx 的 PrivateRoute + PermissionGate 对齐）：
 * - 默认所有页面都需要登录；公开页显式写 meta.public。
 * - 页面级权限点写 meta.permission，缺权限时进入 /forbidden（不白屏、不静默跳首页）。
 * - 守卫在导航前 await ensureSession()，保证刷新后带 token 也能直接落到目标页。
 * - 全部页面组件走 `() => import(...)` 懒加载：首屏不再背整包（此前单块 571 kB）。
 * - meta.permission 与 src/lib/nav.ts 的 NAV_GROUPS 一一对应：
 *   菜单按同一权限点过滤，看得见的菜单一定进得去，不会出现「点了落 /forbidden」。
 */
declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    /** 公开页：不要求登录（登录页、错误页） */
    public?: boolean
    /** 页面级权限点，缺失时进入 /forbidden */
    permission?: string
    /** 版式：blank 不打外壳（登录页 / 独立错误页），缺省进 AppShell */
    layout?: 'shell' | 'blank'
    /** 运维工具入口页的 kind：/terminal /files /logs 共用 ToolsEntryPage */
    toolKind?: ServerToolKind
  }
}

/** 终端 / 文件 / 日志三个入口页共用的工具类型（页面从 meta.toolKind 读取，不写三份页）。 */
export type ServerToolKind = 'terminal' | 'files' | 'logs'

/** 汇总后的路由表：领域模块拼装结果，供测试/校验脚本与外部消费。 */
export { routes }

/** 服务器详情页面板契约：页面仍从 '@/router' 引入，实现移到独立模块。 */
export * from './serverDetailTab'

const router = createRouter({
  // 构建产物由 Go 二进制在子路径下托管，hash 路由不依赖服务端 rewrite。
  history: createWebHashHistory(),
  routes,
})

/** 站内相对路径，防止 ?redirect=//evil.com 造成开放跳转。 */
function safeRedirect(raw: unknown, fallback = '/'): string {
  const target = typeof raw === 'string' ? raw : ''
  if (!target.startsWith('/') || target.startsWith('//')) return fallback
  const path = target.split('#')[0]
  if (path === '/login' || path.startsWith('/login?') || path === '/forbidden') return fallback
  return target
}

router.beforeEach(async (to: RouteLocationNormalized) => {
  const { status, ensureSession, can } = useAuth()
  const isPublic = to.meta.public === true

  if (!isPublic) {
    await ensureSession()
  }

  const authenticated = status.value === 'authenticated'

  if (to.name === 'login') {
    if (!authenticated) return true
    // 已登录还来登录页：回到本来要去的页面
    return { path: safeRedirect(to.query.redirect) }
  }

  if (isPublic) return true

  if (!authenticated) {
    return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }

  const required = to.meta.permission
  if (typeof required === 'string' && required !== '' && !can(required)) {
    return { name: 'forbidden', query: { from: to.fullPath } }
  }

  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title ? `${title} · LeadStack 领栈` : 'LeadStack 领栈'
})

export default router
