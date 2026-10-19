import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { api, clearToken, getToken, onUnauthorized, setToken, type User } from './api'

export type AuthStatus = 'loading' | 'authenticated' | 'anonymous'

interface AuthContextValue {
  status: AuthStatus
  user: User | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

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

  const value = useMemo<AuthContextValue>(
    () => ({ status, user, login, logout }),
    [status, user, login, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext)
  if (!context) throw new Error('useAuth 必须在 <AuthProvider> 内使用')
  return context
}
