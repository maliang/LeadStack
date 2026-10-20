import type { ReactNode } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from '../lib/auth'
import { PageLoading } from './Loading'
import { t } from '../i18n'

/** 路由守卫：未登录访问控制台一律跳登录页；会话校验中显示加载态。 */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { status } = useAuth()
  if (status === 'loading') return <PageLoading label={t('s.0487')} />
  if (status === 'anonymous') return <Navigate to="/login" replace />
  return <>{children}</>
}
