import { Link } from 'react-router-dom'
import { api, type NetworkConfig } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { Button } from './Button'
import { AlertTriangleIcon } from './Icons'

/** 启动回退结果警示：fallback_reason 非空时提示用户修正监听端口/环境变量。 */
export function NetworkFallbackBanner({ network }: { network?: NetworkConfig | null }) {
  const { t } = useI18n()
  const query = usePolling(api.network, 30_000)
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
