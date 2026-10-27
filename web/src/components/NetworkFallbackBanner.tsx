import { Link } from 'react-router-dom'
import { api, type NetworkConfig } from '../lib/api'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { Button } from './Button'
import { AlertTriangleIcon } from './Icons'

/**
 * 启动回退结果警示：fallback_reason 非空时提示用户修正监听端口/环境变量。
 *
 * 门控说明（为什么由组件自己判权限，而不是由调用方传 enabled）：横幅挂在仪表盘
 * （DashboardPage）与设置页（SettingsPage），它读的 GET /system/network 是
 * PermSystemManage + RequireGlobalScope（internal/api/router.go:233）——监听端口/环境变量属于
 * 全局配置，分组/服务器范围的角色本来就不该也看不到这条横幅。以前不管角色一律 30s 轮询，
 * 组范围用户于是每 30 秒吃一次静默 403（控制台持续刷红，而横幅永远不会出现）。
 * 现在在**发请求之前**门控：走 usePolling 的 enabled（web/src/lib/hooks.ts:59-64 在 enabled=false
 * 时既不发起首次请求也不挂定时器），与 BackupsPage 的 isGlobalScope、SystemUpdate 的
 * useUpdateAccess 同一范式。调用方若已把 network 数据传进来，也不必自己再拉一次。
 */
export function NetworkFallbackBanner({ network }: { network?: NetworkConfig | null }) {
  const { t } = useI18n()
  const { can, scope } = useAuth()
  const canReadNetwork = can('system.manage') && scope.mode === 'all'
  const query = usePolling(api.network, 30_000, canReadNetwork && !network)
  const data = network ?? query.data
  const reason = data?.fallback_reason?.trim()
  if (!data || !reason) return null

  return (
    <div className="rounded-xl border border-rose-300 bg-rose-50 px-4 py-3 dark:border-rose-500/40 dark:bg-rose-500/10">
      <div className="flex flex-wrap items-start gap-3">
        <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-rose-100 text-rose-600 dark:bg-rose-500/20 dark:text-rose-300">
          <AlertTriangleIcon className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold text-rose-800 dark:text-rose-200">
            {t('network.fallback.title', { active: data.active_listen })}
          </p>
          <p className="mt-0.5 break-words text-xs leading-relaxed text-rose-700/90 dark:text-rose-200/90">
            {t('network.fallback.reason', { reason })}
          </p>
          <p className="mt-0.5 text-xs text-rose-700/80 dark:text-rose-200/80">
            {t('network.fallback.hint')}
          </p>
        </div>
        <Link to="/settings" className="shrink-0">
          <Button size="sm" variant="danger">
            {t('network.fallback.goSettings')}
          </Button>
        </Link>
      </div>
    </div>
  )
}
