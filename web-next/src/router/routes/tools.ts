import type { RouteRecordRaw } from 'vue-router'

/**
 * 运维工具域路由：终端 / 文件 / 日志三个入口页共用 ToolsEntryPage，靠 meta.toolKind 区分；
 * 另含定时任务（ScheduledTasksPage）与导入主机（ImportPage）两个真实页面。
 *
 * 只放路由记录；实例、守卫与权限判定在 src/router/index.ts。
 */
export const toolRoutes: RouteRecordRaw[] = [
  {
    path: '/terminal',
    name: 'terminal',
    component: () => import('@/pages/ToolsEntryPage.vue'),
    meta: { title: '终端', permission: 'terminal.use', toolKind: 'terminal' },
  },
  {
    path: '/files',
    name: 'files',
    component: () => import('@/pages/ToolsEntryPage.vue'),
    meta: { title: '文件', permission: 'terminal.use', toolKind: 'files' },
  },
  {
    path: '/logs',
    name: 'logs',
    component: () => import('@/pages/ToolsEntryPage.vue'),
    meta: { title: '日志', permission: 'terminal.use', toolKind: 'logs' },
  },
  {
    path: '/scheduled-tasks',
    name: 'scheduled-tasks',
    component: () => import('@/pages/tools/ScheduledTasksPage.vue'),
    meta: { title: '定时任务', permission: 'task.read' },
  },
  {
    path: '/import',
    name: 'import',
    component: () => import('@/pages/tools/ImportPage.vue'),
    meta: { title: '导入主机', permission: 'server.write' },
  },
]
