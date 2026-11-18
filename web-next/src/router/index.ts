import { createRouter, createWebHashHistory, type RouteLocationNormalized, type RouteRecordRaw } from 'vue-router'
import HomeView from '@/views/HomeView.vue'
import { useAuth } from '@/composables/useAuth'

/**
 * 路由表 + 会话/权限守卫。
 *
 * 约定（与旧版 web/src/App.tsx 的 PrivateRoute + PermissionGate 对齐）：
 * - 默认所有页面都需要登录；公开页显式写 meta.public。
 * - 页面级权限点写 meta.permission，缺权限时进入 /forbidden（不白屏、不静默跳首页）。
 * - 守卫在导航前 await ensureSession()，保证刷新后带 token 也能直接落到目标页。
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
  {
    path: '/',
    name: 'home',
    component: HomeView,
    meta: { title: '概览' },
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
  {
    path: '/:pathMatch(.*)*',
    redirect: '/',
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
