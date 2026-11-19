import type { RouteRecordRaw } from 'vue-router'

/**
 * 服务器 / 监控域路由：总览、服务器列表与详情、检测、告警。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 * 新增本域页面改这个文件即可，不要再去 index.ts 抢同一处。
 */
export const serverRoutes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'home',
    component: () => import('@/pages/dashboard/DashboardPage.vue'),
    meta: { titleKey: 'nav.overview', permission: 'server.read' },
  },
  {
    path: '/servers',
    name: 'servers',
    component: () => import('@/pages/ServersPage.vue'),
    meta: { titleKey: 'nav.servers', permission: 'server.read' },
  },
  {
    // 静态段 /servers 先于本参数路由注册也无妨：vue-router 4 按路径特异度打分，静态段优先命中。
    path: '/servers/:id',
    name: 'server-detail',
    component: () => import('@/pages/ServerDetailPage.vue'),
    meta: { titleKey: 'crumb.serverDetail', permission: 'server.read' },
  },
  {
    path: '/checks',
    name: 'checks',
    component: () => import('@/pages/ChecksPage.vue'),
    meta: { titleKey: 'nav.checks', permission: 'check.read' },
  },
  {
    path: '/alerts',
    name: 'alerts',
    component: () => import('@/pages/AlertsPage.vue'),
    meta: { titleKey: 'nav.alerts', permission: 'alert.read' },
  },
]
