import type { ComponentType, SVGProps } from 'react'
import {
  ArchiveIcon,
  BellIcon,
  FileTextIcon,
  FolderIcon,
  KeyIcon,
  LayoutDashboardIcon,
  ListChecksIcon,
  PulseIcon,
  ServerIcon,
  SettingsIcon,
  ShieldCheckIcon,
  TerminalIcon,
  ClockIcon,
  UsersIcon,
  PlusIcon,
  KeyRoundIcon,
  PlayIcon,
} from '../components/Icons'

export interface NavItem {
  to: string
  labelKey: string
  icon: ComponentType<SVGProps<SVGSVGElement>>
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

/** 左侧导航分组（监控 / 运维 / 发布 / 系统），契约第 46 节。 */
export const NAV_GROUPS: NavGroup[] = [
  {
    key: 'monitoring',
    labelKey: 'nav.group.monitoring',
    items: [
      { to: '/', labelKey: 'nav.overview', icon: LayoutDashboardIcon, end: true, permission: 'server.read' },
      { to: '/servers', labelKey: 'nav.servers', icon: ServerIcon, permission: 'server.read' },
      { to: '/checks', labelKey: 'nav.checks', icon: PulseIcon, permission: 'check.read' },
      { to: '/alerts', labelKey: 'nav.alerts', icon: BellIcon, permission: 'alert.read' },
    ],
  },
  {
    key: 'ops',
    labelKey: 'nav.group.ops',
    items: [
      { to: '/terminal', labelKey: 'nav.terminal', icon: TerminalIcon, permission: 'terminal.use' },
      { to: '/files', labelKey: 'nav.files', icon: FolderIcon, permission: 'terminal.use' },
      { to: '/scheduled-tasks', labelKey: 'nav.scheduledTasks', icon: ClockIcon, permission: 'task.read' },
      { to: '/logs', labelKey: 'nav.logs', icon: FileTextIcon, permission: 'terminal.use' },
      { to: '/import', labelKey: 'nav.import', icon: PlusIcon, permission: 'server.write' },
    ],
  },
  {
    key: 'release',
    labelKey: 'nav.group.release',
    items: [
      { to: '/apps', labelKey: 'nav.apps', icon: ListChecksIcon, permission: 'deploy.read' },
      { to: '/releases', labelKey: 'nav.releases', icon: PlayIcon, permission: 'deploy.read' },
    ],
  },
  {
    key: 'system',
    labelKey: 'nav.group.system',
    items: [
      { to: '/credentials', labelKey: 'nav.credentials', icon: KeyIcon, permission: 'server.write' },
      { to: '/users', labelKey: 'nav.users', icon: UsersIcon, permission: 'user.manage' },
      { to: '/tokens', labelKey: 'nav.tokens', icon: KeyRoundIcon, permission: 'token.manage' },
      { to: '/roles', labelKey: 'nav.roles', icon: ShieldCheckIcon, permission: 'role.manage' },
      { to: '/groups', labelKey: 'nav.groups', icon: FolderIcon, permission: 'server.read' },
      { to: '/audit', labelKey: 'nav.audit', icon: ShieldCheckIcon, permission: 'audit.read' },
      { to: '/backups', labelKey: 'nav.backups', icon: ArchiveIcon, permission: 'backup.read' },
      { to: '/settings', labelKey: 'nav.settings', icon: SettingsIcon },
    ],
  },
]

interface Crumb {
  labelKey: string
  to?: string
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
  '/credentials': { labelKey: 'nav.credentials' },
  '/users': { labelKey: 'nav.users' },
  '/tokens': { labelKey: 'nav.tokens' },
  '/roles': { labelKey: 'nav.roles' },
  '/groups': { labelKey: 'nav.groups' },
  '/audit': { labelKey: 'nav.audit' },
  '/backups': { labelKey: 'nav.backups' },
  '/settings': { labelKey: 'nav.settings' },
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
  if (pathname === '/setup') return [{ labelKey: 'crumb.setup' }]
  const crumb = SIMPLE_CRUMBS[pathname] ?? SIMPLE_CRUMBS['/']
  return [crumb]
}
