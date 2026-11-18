import { createRouter, createWebHashHistory, type RouteLocationNormalized, type RouteRecordRaw } from 'vue-router'
import { useAuth } from '@/composables/useAuth'

/**
 * 路由表 + 会话/权限守卫。
 *
 * 约定（与旧版 web/src/App.tsx 的 PrivateRoute + PermissionGate 对齐）：
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
  }
}

const routes: RouteRecordRaw[] = [
  // ── 监控 ────────────────────────────────────────────────────────────────
  {
    path: '/',
    name: 'home',
    component: () => import('@/views/HomeView.vue'),
    meta: { title: '总览', permission: 'server.read' },
  },
  {
    path: '/servers/:id',
    name: 'server-detail',
    component: () => import('@/pages/ServerDetailPage.vue'),
    meta: { title: '服务器详情', permission: 'server.read' },
  },
  {
    path: '/checks',
    name: 'checks',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '检测', permission: 'check.read' },
  },
  {
    path: '/alerts',
    name: 'alerts',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '告警', permission: 'alert.read' },
  },

  // ── 运维 ────────────────────────────────────────────────────────────────
  {
    path: '/terminal',
    name: 'terminal',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '终端', permission: 'terminal.use' },
  },
  {
    path: '/files',
    name: 'files',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '文件', permission: 'terminal.use' },
  },
  {
    path: '/logs',
    name: 'logs',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '日志', permission: 'terminal.use' },
  },
  {
    path: '/scheduled-tasks',
    name: 'scheduled-tasks',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '定时任务', permission: 'task.read' },
  },
  {
    path: '/import',
    name: 'import',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '导入主机', permission: 'server.write' },
  },

  // ── 发布 ────────────────────────────────────────────────────────────────
  {
    path: '/apps',
    name: 'apps',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '应用', permission: 'deploy.read' },
  },
  {
    path: '/apps/:id',
    name: 'app-detail',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '应用详情', permission: 'server.read' },
  },
  {
    path: '/releases',
    name: 'releases',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '发布', permission: 'deploy.read' },
  },
  {
    path: '/releases/:id',
    name: 'release-detail',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '发布详情', permission: 'server.read' },
  },

  // ── 系统 ────────────────────────────────────────────────────────────────
  {
    path: '/credentials',
    name: 'credentials',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '凭证', permission: 'server.write' },
  },
  {
    path: '/users',
    name: 'users',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '用户', permission: 'user.manage' },
  },
  {
    path: '/roles',
    name: 'roles',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '角色与权限', permission: 'role.manage' },
  },
  {
    path: '/groups',
    name: 'groups',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '服务器分组', permission: 'server.read' },
  },
  {
    path: '/tokens',
    name: 'tokens',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: 'API 令牌', permission: 'token.manage' },
  },
  {
    path: '/audit',
    name: 'audit',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '审计', permission: 'audit.read' },
  },
  {
    path: '/backups',
    name: 'backups',
    component: () => import('@/views/NotYetView.vue'),
    meta: { title: '备份', permission: 'backup.read' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/views/NotYetView.vue'),
    // 这里刻意不加路由级权限门：设置页对所有人可见（nav.ts 的设置项也没带 permission），
    // 页内自己判 can('system.manage') 并给「需要系统管理权限」提示，与旧 React 界面一致。
    // 不要在这里加 permission，否则无系统管理权限的角色会被直接踢到 /forbidden。
    meta: { title: '设置' },
  },

  // ── 独立页（meta 保持不变）─────────────────────────────────────────────
  {
    path: '/servers',
    name: 'servers',
    component: () => import('@/pages/ServersPage.vue'),
    meta: { title: '服务器', permission: 'server.read' },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { title: '登录', public: true, layout: 'blank' },
  },
  {
    path: '/forbidden',
    name: 'forbidden',
    component: () => import('@/pages/ForbiddenPage.vue'),
    meta: { title: '无权限', public: true },
  },
  {
    path: '/about',
    name: 'about',
    component: () => import('@/views/AboutView.vue'),
    meta: { title: '关于' },
  },

  // ── 兜底：显式 404，不再静默 redirect 到 / ─────────────────────────────
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { title: '页面不存在' },
  },
]

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
