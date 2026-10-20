import type { CheckResult } from '../lib/api'
import { cn } from '../lib/cn'
import { formatDateTime, formatLatency } from '../lib/format'
import { SpinnerIcon } from './Icons'
import { t } from '../i18n'

interface CheckTimelineProps {
  results: CheckResult[]
  loading?: boolean
  /** 时间线最多展示的条数（由近到远取尾部）。 */
  limit?: number
  serverName?: (serverId: string) => string
}

function resultTitle(result: CheckResult, serverName?: (serverId: string) => string): string {
  const parts = [formatDateTime(result.ts)]
  parts.push(result.ok ? t('s.0300') : t('s.0301'))
  if (result.latency_ms !== null) parts.push(formatLatency(result.latency_ms))
  if (result.status_code !== null) parts.push(`HTTP ${result.status_code}`)
  const name = result.server_name ?? (result.server_id && serverName ? serverName(result.server_id) : null)
  if (name) parts.push(name)
  if (result.message) parts.push(result.message)
  return parts.join(' · ')
}

/**
 * 结果时间线：成功/失败色块，最右侧为最新一次。
 * 色块可悬浮查看时间、延迟与失败原因。
 */
export function CheckTimeline({ results, loading = false, limit = 100, serverName }: CheckTimelineProps) {
  if (loading && results.length === 0) {
    return (
      <div className="flex h-6 items-center gap-2 text-xs text-slate-400 dark:text-slate-500">
        <SpinnerIcon className="h-3.5 w-3.5" />
        {t('s.0381')}
      </div>
    )
  }

  if (results.length === 0) {
    return (
      <div className="flex h-6 items-center rounded border border-dashed border-slate-200 px-2 text-xs text-slate-400 dark:border-slate-800 dark:text-slate-500">
        {t('s.0382')}
      </div>
    )
  }

  const shown = results.slice(-limit)
  const failed = shown.filter((result) => !result.ok).length

  return (
    <div>
      <div className="flex h-6 items-stretch gap-[2px] overflow-hidden rounded bg-slate-50 p-[2px] dark:bg-slate-950/60">
        {shown.map((result, index) => (
          <span
            key={`${result.ts ?? index}-${index}`}
            title={resultTitle(result, serverName)}
            className={cn(
              'min-w-[2px] flex-1 rounded-[2px] transition-opacity hover:opacity-70',
              result.ok ? 'bg-emerald-500/80' : 'bg-rose-500/85',
            )}
          />
        ))}
      </div>
      <p className="mt-1 text-[11px] text-slate-400 dark:text-slate-500">
        {t('s.0383')} {shown.length} {t('s.0384')} {failed} {t('s.0385')}
      </p>
    </div>
  )
}
