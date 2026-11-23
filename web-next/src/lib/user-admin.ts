import { t } from '../i18n'
import type { ManagedUser, Permission, Role, ScopeMode, Server, ServerGroup } from './api'

/**
 * 用户 / 角色管理页的纯函数层。
 *
 * 页面只持有「选中项 / 表单 / 页码」这些 UI 状态，筛选、分页、范围摘要、
 * 权限分组全部在这里算，便于单测与两页复用；组件里不再各写一套映射。
 */

/** 用户列表每页条数（窄宽卡片化后 8 条仍然一屏可控）。 */
export const USERS_PAGE_SIZE = 8

/** 权限点域的顺序：与后端 internal/auth/perm.go 的注册顺序一致。 */
export const PERMISSION_DOMAIN_ORDER = [
  'server',
  'check',
  'alert',
  'deploy',
  'backup',
  'task',
  'group',
  'agent',
  'user',
  'role',
  'token',
  'enroll',
  'system',
  'audit',
  'other',
] as const

export type PermissionDomain = (typeof PERMISSION_DOMAIN_ORDER)[number]

export interface PermissionGroup {
  domain: PermissionDomain
  label: string
  items: Permission[]
}

/** `server.read` → `server`；未知前缀归入 other，保证不会丢权限点。 */
export function permissionDomain(key: string): PermissionDomain {
  const dot = key.indexOf('.')
  const domain = dot > 0 ? key.slice(0, dot) : key
  return (PERMISSION_DOMAIN_ORDER as readonly string[]).includes(domain)
    ? (domain as PermissionDomain)
    : 'other'
}

export function permissionDomainLabel(domain: PermissionDomain): string {
  return t(`perm.domain.${domain}`)
}

/** 按固定域顺序分组；组内保持后端返回顺序（不重排，管理员看到的顺序稳定）。 */
export function groupPermissionsByDomain(permissions: Permission[]): PermissionGroup[] {
  const buckets = new Map<PermissionDomain, Permission[]>()
  for (const permission of permissions) {
    const domain = permissionDomain(permission.key)
    const list = buckets.get(domain)
    if (list) list.push(permission)
    else buckets.set(domain, [permission])
  }
  return PERMISSION_DOMAIN_ORDER.filter((domain) => buckets.has(domain)).map((domain) => ({
    domain,
    label: permissionDomainLabel(domain),
    items: buckets.get(domain) ?? [],
  }))
}

export function assignablePermissions(permissions: Permission[]): Permission[] {
  return permissions.filter((permission) => !permission.admin_only)
}

/** 已选权限里真正能提交给后端的部分（过滤掉 admin-only，后端会 400）。 */
export function selectedGrantable(permissions: Permission[], selected: string[]): string[] {
  const grantable = new Set(assignablePermissions(permissions).map((permission) => permission.key))
  return selected.filter((key) => grantable.has(key))
}

export function filterUsers(users: ManagedUser[], keyword: string): ManagedUser[] {
  const query = keyword.trim().toLowerCase()
  if (query === '') return users
  return users.filter((user) =>
    [user.username, user.role, user.role_name ?? '', user.id].some((field) =>
      field.toLowerCase().includes(query),
    ),
  )
}

export function pageCount(total: number, size: number): number {
  if (size <= 0) return 1
  return Math.max(1, Math.ceil(total / size))
}

export function clampPage(page: number, total: number, size: number): number {
  return Math.min(Math.max(1, page), pageCount(total, size))
}

export function paginate<T>(items: T[], page: number, size: number): T[] {
  const start = (clampPage(page, items.length, size) - 1) * size
  return items.slice(start, start + size)
}

/** 内置角色排在前面（admin → operator → viewer 的固定顺序由后端顺序保证）。 */
export function sortRoles(roles: Role[]): Role[] {
  return [...roles].sort((left, right) => {
    if (left.builtin !== right.builtin) return left.builtin ? -1 : 1
    return left.name.localeCompare(right.name, 'zh-CN')
  })
}

export function roleNameMap(roles: Role[]): Record<string, string> {
  const map: Record<string, string> = {}
  for (const role of roles) map[role.key] = role.name
  return map
}

/** 范围摘要：全部 / N 个分组（+M 台服务器）/ M 台服务器。 */
export function scopeSummary(role: Role): string {
  const scope = role.scope
  if (scope.mode === 'all') return t('roles.scopeAll')
  const groups = scope.group_ids.length
  const servers = scope.server_ids.length
  if (groups > 0) return t('roles.scopeGroups', { groups, servers })
  if (servers > 0) return t('roles.scopeServers', { servers })
  return t('roles.scopeServers', { servers: 0 })
}

export type UserStatus = 'active' | 'disabled' | 'locked'

export function userStatus(user: ManagedUser): UserStatus {
  if (user.disabled) return 'disabled'
  if (user.locked) return 'locked'
  return 'active'
}

/** 状态 → i18n 键（唯一映射，模板里不再写条件色）。 */
export function userStatusLabelKey(status: UserStatus): string {
  switch (status) {
    case 'disabled':
      return 'common.disabled'
    case 'locked':
      return 'users.locked'
    default:
      return 'common.enabled'
  }
}

/** 范围模式选项：all / groups / servers，文案与说明都走 i18n 键。 */
export const SCOPE_MODES: { value: ScopeMode; labelKey: string; hintKey: string }[] = [
  { value: 'all', labelKey: 'roles.scopeMode.all', hintKey: 'roles.scopeMode.allHint' },
  { value: 'groups', labelKey: 'roles.scopeMode.groups', hintKey: 'roles.scopeMode.groupsHint' },
  {
    value: 'servers',
    labelKey: 'roles.scopeMode.servers',
    hintKey: 'roles.scopeMode.serversHint',
  },
]

/** 可选范围：groups 模式同时允许追加服务器；servers 模式只选服务器。 */
export function showsGroups(mode: ScopeMode): boolean {
  return mode === 'groups'
}

export function showsServers(mode: ScopeMode): boolean {
  return mode === 'groups' || mode === 'servers'
}

export function pickerGroups(groups: ServerGroup[], keyword: string): ServerGroup[] {
  const query = keyword.trim().toLowerCase()
  if (query === '') return groups
  return groups.filter((group) =>
    [group.name, group.description].some((field) => field.toLowerCase().includes(query)),
  )
}

export function pickerServers(servers: Server[], keyword: string): Server[] {
  const query = keyword.trim().toLowerCase()
  if (query === '') return servers
  return servers.filter((server) =>
    [server.name, server.host, server.id].some((field) => field.toLowerCase().includes(query)),
  )
}

/** 表单校验：返回 i18n 键，空串表示通过。 */
export function validateRoleForm(input: {
  name: string
  selected: string[]
  mode: ScopeMode
  groupIds: string[]
  serverIds: string[]
}): string {
  if (input.name.trim() === '') return 'roles.errorName'
  if (input.selected.length === 0) return 'roles.errorPermissions'
  if (input.mode !== 'all' && input.groupIds.length === 0 && input.serverIds.length === 0) {
    return 'roles.errorScope'
  }
  return ''
}

export function toggleSelection(selected: string[], key: string): string[] {
  return selected.includes(key) ? selected.filter((item) => item !== key) : [...selected, key]
}
