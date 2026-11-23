import type { RouteRecordRaw } from 'vue-router'

/**
 * 系统管理域路由：用户 / 角色 / 分组 / 令牌 / 审计 / 备份 / 设置。
 *
 * 凭证不再单独成页：SSH 凭证在服务器表单内维护，宝塔面板在服务器详情内维护。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const systemRoutes: RouteRecordRaw[] = [
  {
    path: '/users',
    name: 'users',
    component: () => import('@/pages/users/UsersPage.vue'),
    meta: { titleKey: 'nav.users', permission: 'user.manage' },
  },
  {
    path: '/roles',
    name: 'roles',
    component: () => import('@/pages/roles/RolesPage.vue'),
    meta: { titleKey: 'nav.roles', permission: 'role.manage' },
  },
  {
    path: '/groups',
    name: 'groups',
    component: () => import('@/pages/ops/GroupsPage.vue'),
    meta: { titleKey: 'nav.groups', permission: 'server.read' },
  },
  {
    path: '/tokens',
    name: 'tokens',
    component: () => import('@/pages/ops/TokensPage.vue'),
    meta: { titleKey: 'nav.tokens', permission: 'token.manage' },
  },
  {
    path: '/audit',
    name: 'audit',
    component: () => import('@/pages/ops/AuditPage.vue'),
    meta: { titleKey: 'nav.audit', permission: 'audit.read' },
  },
  {
    path: '/backups',
    name: 'backups',
    component: () => import('@/pages/ops/BackupsPage.vue'),
    meta: { titleKey: 'nav.backups', permission: 'backup.read' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/pages/ops/SettingsPage.vue'),
    // 这里刻意不加路由级权限门：设置页对所有人可见（nav.ts 的设置项也没带 permission），
    // 页内自己判 can('system.manage') 并给「需要系统管理权限」提示，与旧 React 界面一致。
    // 不要在这里加 permission，否则无系统管理权限的角色会被直接踢到 /forbidden。
    meta: { titleKey: 'nav.settings' },
  },
]
