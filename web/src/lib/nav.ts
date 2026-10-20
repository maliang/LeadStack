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
      { to: '/', labelKey: 'nav.overview', icon: LayoutDashboardIcon, end: true },
      { to: '/servers', labelKey: 'nav.servers', icon: ServerIcon },
      { to: '/checks', labelKey: 'nav.checks', icon: PulseIcon },
      { to: '/alerts', labelKey: 'nav.alerts', icon: BellIcon },
    ],
  },
  {
    key: 'ops',
    labelKey: 'nav.group.ops',
    items: [
      { to: '/terminal', labelKey: 'nav.terminal', icon: TerminalIcon },
      { to: '/files', labelKey: 'nav.files', icon: FolderIcon },
      { to: '/scheduled-tasks', labelKey: 'nav.scheduledTasks', icon: ClockIcon },
      { to: '/logs', labelKey: 'nav.logs', icon: FileTextIcon },
      { to: '/import', labelKey: 'nav.import', icon: PlusIcon },
    ],
  },
  {
    key: 'release',
    labelKey: 'nav.group.release',
    items: [
      { to: '/apps', labelKey: 'nav.apps', icon: ListChecksIcon },
      { to: '/releases', labelKey: 'nav.releases', icon: PlayIcon },
    ],
  },
  {
    key: 'system',
    labelKey: 'nav.group.system',
    items: [
      { to: '/credentials', labelKey: 'nav.credentials', icon: KeyIcon },
      { to: '/users', labelKey: 'nav.users', icon: UsersIcon },
      { to: '/tokens', labelKey: 'nav.tokens', icon: KeyRoundIcon },
      { to: '/audit', labelKey: 'nav.audit', icon: ShieldCheckIcon },
      { to: '/backups', labelKey: 'nav.backups', icon: ArchiveIcon },
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
