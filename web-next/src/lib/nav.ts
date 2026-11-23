import type { IconName } from './icons'

/**
 * 左侧导航与面包屑的数据定义。
 *
 * 与 web/src/lib/nav.ts 保持同构（同一批路由、同一批 labelKey、同一批权限点），
 * 只把 React 组件图标换成图标名，并把「按权限过滤」抽成纯函数，
 * 方便单测与复用：组件只负责渲染，不各写一套过滤逻辑。
 */

export interface NavItem {
  to: string
  labelKey: string
  icon: IconName
  /** 精确匹配（避免 '/' 总是高亮） */
  end?: boolean
  /** 需要的权限点；缺省表示所有已登录用户可见 */
  permission?: string
}

export interface NavGroup {
  key: string
  labelKey: string
  items: NavItem[]
}

export interface Crumb {
  labelKey: string
  to?: string
}

/** 左侧导航分组（监控 / 运维 / 发布 / 系统），契约第 46 节。 */
export const NAV_GROUPS: NavGroup[] = [
  {
    key: 'monitoring',
    labelKey: 'nav.group.monitoring',
    items: [
      { to: '/', labelKey: 'nav.overview', icon: 'dashboard', end: true, permission: 'server.read' },
      { to: '/servers', labelKey: 'nav.servers', icon: 'server', permission: 'server.read' },
      { to: '/checks', labelKey: 'nav.checks', icon: 'pulse', permission: 'check.read' },
      { to: '/alerts', labelKey: 'nav.alerts', icon: 'bell', permission: 'alert.read' },
    ],
  },
  {
    key: 'ops',
    labelKey: 'nav.group.ops',
    items: [
      { to: '/terminal', labelKey: 'nav.terminal', icon: 'terminal', permission: 'terminal.use' },
      { to: '/files', labelKey: 'nav.files', icon: 'folder', permission: 'terminal.use' },
      {
        to: '/scheduled-tasks',
        labelKey: 'nav.scheduledTasks',
        icon: 'clock',
        permission: 'task.read',
      },
      { to: '/logs', labelKey: 'nav.logs', icon: 'file-text', permission: 'terminal.use' },
      { to: '/import', labelKey: 'nav.import', icon: 'plus', permission: 'server.write' },
    ],
  },
  {
    key: 'release',
    labelKey: 'nav.group.release',
    items: [
      { to: '/apps', labelKey: 'nav.apps', icon: 'list-checks', permission: 'deploy.read' },
      { to: '/releases', labelKey: 'nav.releases', icon: 'play', permission: 'deploy.read' },
      { to: '/templates', labelKey: 'nav.templates', icon: 'file-text', permission: 'deploy.read' },
    ],
  },
  {
    key: 'system',
    labelKey: 'nav.group.system',
    items: [
      { to: '/users', labelKey: 'nav.users', icon: 'users', permission: 'user.manage' },
      { to: '/tokens', labelKey: 'nav.tokens', icon: 'key-round', permission: 'token.manage' },
      { to: '/roles', labelKey: 'nav.roles', icon: 'shield-check', permission: 'role.manage' },
      { to: '/groups', labelKey: 'nav.groups', icon: 'folder', permission: 'server.read' },
      { to: '/audit', labelKey: 'nav.audit', icon: 'shield-check', permission: 'audit.read' },
      { to: '/backups', labelKey: 'nav.backups', icon: 'archive', permission: 'backup.read' },
      { to: '/settings', labelKey: 'nav.settings', icon: 'settings' },
    ],
  },
]

/**
 * 单个权限点的判定：只看后端返回的 permissions，不按角色名硬编码。
 * 未声明权限点（`required` 为空）时，只要有会话即视为可见。
 */
export function permissionMatches(
  permissions: readonly string[],
  required: string | undefined,
  authenticated: boolean,
): boolean {
  if (!required) return authenticated
  return permissions.includes(required)
}

/**
 * 按权限过滤导航分组：整组无可见项时整组消失（不留空标题）。
 * 纯函数，组件只需 `visibleNavGroups(NAV_GROUPS, can)`。
 */
export function visibleNavGroups(
  groups: readonly NavGroup[],
  can: (permission?: string) => boolean,
): NavGroup[] {
  return groups
    .map((group) => ({ ...group, items: group.items.filter((item) => can(item.permission)) }))
    .filter((group) => group.items.length > 0)
}

/** 当前路径命中的导航项；`end` 项要求完全相等。 */
export function isNavItemActive(item: NavItem, path: string): boolean {
  if (item.end) return path === item.to
  if (item.to === '/') return path === '/'
  return path === item.to || path.startsWith(`${item.to}/`)
}

const SIMPLE_CRUMBS: Record<string, Crumb> = {
  '/': { labelKey: 'nav.overview' },
  '/servers': { labelKey: 'nav.servers' },
  '/checks': { labelKey: 'nav.checks' },
  '/alerts': { labelKey: 'nav.alerts' },
  '/terminal': { labelKey: 'nav.terminal' },
  '/files': { labelKey: 'nav.files' },
  '/logs': { labelKey: 'nav.logs' },
  '/scheduled-tasks': { labelKey: 'nav.scheduledTasks' },
  '/apps': { labelKey: 'nav.apps' },
  '/releases': { labelKey: 'nav.releases' },
  '/templates': { labelKey: 'nav.templates' },
  '/users': { labelKey: 'nav.users' },
  '/tokens': { labelKey: 'nav.tokens' },
  '/roles': { labelKey: 'nav.roles' },
  '/groups': { labelKey: 'nav.groups' },
  '/audit': { labelKey: 'nav.audit' },
  '/backups': { labelKey: 'nav.backups' },
  '/settings': { labelKey: 'nav.settings' },
  '/about': { labelKey: 'crumb.about' },
  '/forbidden': { labelKey: 'crumb.forbidden' },
}

/** 根据当前路径生成面包屑；详情页补一级。 */
export function breadcrumbsFor(pathname: string): Crumb[] {
  if (pathname.startsWith('/servers/') && pathname !== '/servers') {
    return [{ labelKey: 'nav.servers', to: '/servers' }, { labelKey: 'crumb.serverDetail' }]
  }
  if (pathname.startsWith('/apps/') && pathname !== '/apps') {
    return [{ labelKey: 'nav.apps', to: '/apps' }, { labelKey: 'crumb.appDetail' }]
  }
  if (pathname.startsWith('/releases/')) {
    return [{ labelKey: 'nav.releases', to: '/releases' }, { labelKey: 'crumb.releaseDetail' }]
  }
  const crumb = SIMPLE_CRUMBS[pathname] ?? SIMPLE_CRUMBS['/']
  return [crumb]
}
