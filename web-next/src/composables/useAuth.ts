import { computed, reactive } from 'vue'
import {
  api,
  clearToken,
  getToken,
  onUnauthorized,
  setToken,
  ApiError,
  type RoleScope,
  type User,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { permissionMatches } from '@/lib/nav'

/**
 * 会话状态的唯一 owner（对应 web/src/lib/auth.tsx 的 AuthProvider）。
 *
 * 状态是模块级单例：任何组件通过 useAuth()/useCan() 读的都是同一份，
 * 不允许多处各自持有一份 user/permissions（历史高频缺陷）。
 */

export type AuthStatus = 'loading' | 'authenticated' | 'anonymous'

const EMPTY_SCOPE: RoleScope = { mode: 'all', group_ids: [], server_ids: [] }

interface AuthState {
  status: AuthStatus
  user: User | null
  /** 非 401 的会话校验失败（网络 / 5xx）；401 由 onUnauthorized 统一清态，这里不重复报错 */
  error: string | null
  /** 是否至少跑过一次会话校验：区分「还没校验」与「校验后未登录」 */
  checked: boolean
}

function safeGetToken(): string | null {
  if (typeof window === 'undefined') return null
  try {
    return getToken()
  } catch {
    return null
  }
}

const state = reactive<AuthState>({
  status: safeGetToken() ? 'loading' : 'anonymous',
  user: null,
  error: null,
  checked: false,
})

let unsubscribe: (() => void) | null = null
let inflight: Promise<void> | null = null

function resetAuth(message: string | null): void {
  state.status = 'anonymous'
  state.user = null
  state.error = message
  state.checked = true
}

function subscribeUnauthorized(): void {
  if (unsubscribe) return
  unsubscribe = onUnauthorized(() => {
    clearToken()
    resetAuth(null)
  })
}

/** 拉一次会话（/auth/me）。并发调用合并成同一个请求。 */
export async function refreshSession(): Promise<void> {
  subscribeUnauthorized()
  if (inflight) return inflight
  const token = safeGetToken()
  if (!token) {
    resetAuth(null)
    return
  }
  state.status = 'loading'
  state.error = null
  inflight = api
    .me()
    .then((currentUser) => {
      state.user = currentUser
      state.status = 'authenticated'
      state.error = null
    })
    .catch((error: unknown) => {
      const unauthorized = error instanceof ApiError && error.status === 401
      // 401：token 已失效，清掉；网络错误：保留 token，允许界面重试
      if (unauthorized) clearToken()
      resetAuth(unauthorized ? null : errorMessage(error))
    })
    .finally(() => {
      state.checked = true
      inflight = null
    })
  return inflight
}

/** 首屏挂载时调用；已经校验过就不重复请求。 */
export function ensureSession(): Promise<void> {
  subscribeUnauthorized()
  if (state.checked && state.status !== 'loading') return Promise.resolve()
  return refreshSession()
}

async function login(username: string, password: string): Promise<void> {
  const result = await api.login(username, password)
  setToken(result.token)
  if (result.user) {
    state.user = result.user
    state.status = 'authenticated'
    state.error = null
    state.checked = true
    return
  }
  // 兼容登录响应不带 user 的实现：再取一次 /auth/me
  try {
    const currentUser = await api.me()
    state.user = currentUser
    state.status = 'authenticated'
    state.error = null
  } catch (error) {
    clearToken()
    resetAuth(errorMessage(error))
  } finally {
    state.checked = true
  }
}

async function logout(): Promise<void> {
  try {
    await api.logout()
  } catch {
    /* 登出失败不阻塞前端清态 */
  }
  clearToken()
  resetAuth(null)
}

const permissions = computed<string[]>(() => state.user?.permissions ?? [])
const scope = computed<RoleScope>(() => state.user?.scope ?? EMPTY_SCOPE)
const status = computed<AuthStatus>(() => state.status)
const user = computed<User | null>(() => state.user)
const error = computed<string | null>(() => state.error)
const checked = computed<boolean>(() => state.checked)
const isAuthenticated = computed(() => state.status === 'authenticated')
const isLoading = computed(() => state.status === 'loading')

/** 权限判定：只看后端返回的 permissions，不按角色名硬编码。 */
function can(permission?: string): boolean {
  return permissionMatches(permissions.value, permission, state.status === 'authenticated')
}

export function useAuth() {
  return {
    status,
    user,
    permissions,
    scope,
    error,
    checked,
    isAuthenticated,
    isLoading,
    can,
    login,
    logout,
    refreshSession,
    ensureSession,
  }
}
