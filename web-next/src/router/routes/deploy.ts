import type { RouteRecordRaw } from 'vue-router'

/**
 * 发布域路由：应用与发布列表 + 详情。四页均为真实实现（见 src/pages/apps、src/pages/releases）。
 * 但 path/name/meta 已定，后续换成真实页面时保持不变。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const deployRoutes: RouteRecordRaw[] = [
  {
    path: '/apps',
    name: 'apps',
    component: () => import('@/pages/apps/AppsPage.vue'),
    meta: { title: '应用', permission: 'deploy.read' },
  },
  {
    path: '/apps/:id',
    name: 'app-detail',
    component: () => import('@/pages/apps/AppDetailPage.vue'),
    meta: { title: '应用详情', permission: 'deploy.read' },
  },
  {
    path: '/releases',
    name: 'releases',
    component: () => import('@/pages/releases/ReleasesPage.vue'),
    meta: { title: '发布', permission: 'deploy.read' },
  },
  {
    path: '/releases/:id',
    name: 'release-detail',
    component: () => import('@/pages/releases/ReleaseDetailPage.vue'),
    meta: { title: '发布详情', permission: 'deploy.read' },
  },
]
