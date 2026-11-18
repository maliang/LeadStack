import { computed, type ComputedRef } from 'vue'
import { permissionMatches } from '@/lib/nav'
import { useAuth } from './useAuth'

/**
 * 权限判定入口（替代 web/src/lib/auth.tsx 里的 `can()`）。
 *
 * 只读：真正的会话状态由 composables/useAuth.ts 持有，这里不保存任何副本。
 * 组件里请用 `const { can } = useCan()`，不要直接读 user.permissions 自己判。
 */

/** 任一枚举权限命中即通过（空列表为 false，避免误放行）。 */
export function hasAnyPermission(
  permissions: readonly string[],
  required: readonly string[],
  authenticated: boolean,
): boolean {
  if (required.length === 0) return false
  return required.some((permission) => permissionMatches(permissions, permission, authenticated))
}

/** 全部权限命中才通过（空列表为 false）。 */
export function hasAllPermissions(
  permissions: readonly string[],
  required: readonly string[],
  authenticated: boolean,
): boolean {
  if (required.length === 0) return false
  return required.every((permission) => permissionMatches(permissions, permission, authenticated))
}

export interface CanApi {
  /** 单个权限点；不传表示「已登录即可」 */
  can: (permission?: string) => boolean
  /** 任一命中 */
  canAny: (permission: string[]) => boolean
  /** 全部命中 */
  canAll: (permission: string[]) => boolean
  permissions: ComputedRef<string[]>
  isAuthenticated: ComputedRef<boolean>
}

export function useCan(): CanApi {
  const { permissions, isAuthenticated } = useAuth()

  const can = (permission?: string): boolean =>
    permissionMatches(permissions.value, permission, isAuthenticated.value)

  const canAny = (required: string[]): boolean =>
    hasAnyPermission(permissions.value, required, isAuthenticated.value)

  const canAll = (required: string[]): boolean =>
    hasAllPermissions(permissions.value, required, isAuthenticated.value)

  return {
    can,
    canAny,
    canAll,
    permissions,
    // 与 can() 同源：不是 computed 也保证读到同一个 status
    isAuthenticated: computed(() => isAuthenticated.value),
  }
}
