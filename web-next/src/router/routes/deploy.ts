import type { RouteRecordRaw } from 'vue-router'

/**
 * 发布域路由：应用与发布列表 + 详情。当前详情页均为占位页（NotYetView），
 * 但 path/name/meta 已定，后续换成真实页面时保持不变。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const deployRoutes: RouteRecordRaw[] = [
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
]
