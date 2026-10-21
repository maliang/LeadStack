import type { ReactNode } from 'react'
import { useAuth } from '../lib/auth'
import { useI18n } from '../i18n'

/** 路由级权限门：没有权限点时不渲染页面，直接给出无权限提示。 */
export function PermissionGate({
  permission,
  children,
}: {
  permission: string
  children: ReactNode
}) {
  const { can } = useAuth()
  const { t } = useI18n()
  if (!can(permission)) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center text-sm text-slate-500 shadow-sm dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
        {t('authz.noPermission')}
      </div>
    )
  }
  return <>{children}</>
}
