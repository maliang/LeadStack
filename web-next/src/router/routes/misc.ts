import type { RouteRecordRaw } from 'vue-router'

/**
 * 独立页与兜底：登录 / 无权限 / 关于 / 404。
 *
 * 这些页面不挂左侧菜单，靠 meta.public 或 layout 决定外壳；404 是显式兜底，
 * 不再静默 redirect 到首页。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const miscRoutes: RouteRecordRaw[] = [
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
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { title: '页面不存在' },
  },
]
