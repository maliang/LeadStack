import type { RouteRecordRaw } from 'vue-router'

/**
 * 系统管理域路由：凭证 / 用户 / 角色 / 分组 / 令牌 / 审计 / 备份 / 设置。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const systemRoutes: RouteRecordRaw[] = [
  {
    path: '/credentials',
    name: 'credentials',
    component: () => import('@/pages/ops/CredentialsPage.vue'),
    meta: { title: '凭证', permission: 'server.write' },
  },
  {
    path: '/users',
    name: 'users',
    component: () => import('@/pages/users/UsersPage.vue'),
    meta: { title: '用户', permission: 'user.manage' },
  },
  {
    path: '/roles',
    name: 'roles',
    component: () => import('@/pages/roles/RolesPage.vue'),
    meta: { title: '角色与权限', permission: 'role.manage' },
  },
  {
    path: '/groups',
    name: 'groups',
    component: () => import('@/pages/ops/GroupsPage.vue'),
    meta: { title: '服务器分组', permission: 'server.read' },
  },
  {
    path: '/tokens',
    name: 'tokens',
    component: () => import('@/pages/ops/TokensPage.vue'),
    meta: { title: 'API 令牌', permission: 'token.manage' },
  },
  {
    path: '/audit',
    name: 'audit',
    component: () => import('@/pages/ops/AuditPage.vue'),
    meta: { title: '审计', permission: 'audit.read' },
  },
  {
    path: '/backups',
    name: 'backups',
    component: () => import('@/pages/ops/BackupsPage.vue'),
    meta: { title: '备份', permission: 'backup.read' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/pages/ops/SettingsPage.vue'),
    // 这里刻意不加路由级权限门：设置页对所有人可见（nav.ts 的设置项也没带 permission），
    // 页内自己判 can('system.manage') 并给「需要系统管理权限」提示，与旧 React 界面一致。
    // 不要在这里加 permission，否则无系统管理权限的角色会被直接踢到 /forbidden。
    meta: { title: '设置' },
  },
]
