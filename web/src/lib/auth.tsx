import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { api, clearToken, getToken, onUnauthorized, setToken, type RoleScope, type User } from './api'

import { t } from '../i18n'
export type AuthStatus = 'loading' | 'authenticated' | 'anonymous'

interface AuthContextValue {
  status: AuthStatus
  user: User | null
  permissions: string[]
  scope: RoleScope
  /** 权限判定：只看后端返回的 permissions，不按角色名硬编码 */
  can: (permission: string) => boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const EMPTY_SCOPE: RoleScope = { mode: 'all', group_ids: [], server_ids: [] }

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setTokenState] = useState<string | null>(() => getToken())
  const [user, setUser] = useState<User | null>(null)
  const [status, setStatus] = useState<AuthStatus>(() => (getToken() ? 'loading' : 'anonymous'))

  // 任何接口 401 → 清态，路由层会自动跳转登录页
  useEffect(
    () =>
      onUnauthorized(() => {
        setTokenState(null)
        setUser(null)
        setStatus('anonymous')
      }),
    [],
  )

  // 启动或 token 变化时校验会话
  useEffect(() => {
    if (!token) {
      setUser(null)
      setStatus('anonymous')
      return
    }
    let cancelled = false
    setStatus('loading')
    api
      .me()
      .then((currentUser) => {
        if (cancelled) return
        setUser(currentUser)
        setStatus('authenticated')
      })
      .catch(() => {
        if (cancelled) return
        clearToken()
        setTokenState(null)
        setUser(null)
        setStatus('anonymous')
      })
    return () => {
      cancelled = true
    }
  }, [token])

  const login = useCallback(async (username: string, password: string) => {
    const result = await api.login(username, password)
    setToken(result.token)
    setTokenState(result.token)
    if (result.user) {
      setUser(result.user)
      setStatus('authenticated')
      return
    }
    // 兼容登录响应不带 user 的实现：再取一次 /auth/me
    try {
      const currentUser = await api.me()
      setUser(currentUser)
      setStatus('authenticated')
    } catch {
      setUser(null)
      setStatus('anonymous')
    }
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      /* 登出失败不阻塞前端清态 */
    }
    clearToken()
    setTokenState(null)
    setUser(null)
    setStatus('anonymous')
  }, [])

  const permissions = useMemo(() => user?.permissions ?? [], [user])
  const scope = user?.scope ?? EMPTY_SCOPE

  const can = useCallback((permission: string) => permissions.includes(permission), [permissions])

  const value = useMemo<AuthContextValue>(
    () => ({ status, user, permissions, scope, can, login, logout }),
    [status, user, permissions, scope, can, login, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext)
  if (!context) throw new Error(t('s.0594'))
  return context
}
