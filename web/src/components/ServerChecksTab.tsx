import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type CheckResult } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { formatLatency, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button } from '../components/Button'
import { CheckKindBadge, CheckStateBadge, availabilityTone } from '../components/CheckBadges'
import { CheckTimeline } from '../components/CheckTimeline'
import { EmptyState } from '../components/EmptyState'
import { AlertTriangleIcon, ShieldCheckIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { t } from '../i18n'

/**
 * 服务器详情页「检测」标签：该机器适用的检测项 + 每项最近 100 条结果时间线。
 * 检测项列表来自 GET /servers/{id}/checks，时间线来自 GET /checks/{id}/results?server_id=。
 */
export function ServerChecksTab({ serverId }: { serverId: string }) {
  const checksQuery = usePolling(() => api.serverChecks(serverId), 10_000, true, serverId)
  const checks = checksQuery.data ?? []
  const [timelines, setTimelines] = useState<Record<string, CheckResult[]>>({})
  const [timelineLoading, setTimelineLoading] = useState(false)

  const idsKey = checks.map((check) => check.id).join(',')

  useEffect(() => {
    if (!idsKey) {
      setTimelines({})
      return
    }
    let cancelled = false
    const load = async () => {
      setTimelineLoading(true)
      const ids = idsKey.split(',')
      const settled = await Promise.allSettled(
        ids.map((id) => api.checkResults(id, { server_id: serverId, limit: 100 })),
      )
      if (cancelled) return
      const next: Record<string, CheckResult[]> = {}
      settled.forEach((result, index) => {
        if (result.status === 'fulfilled') next[ids[index]] = result.value
      })
      setTimelines(next)
      setTimelineLoading(false)
    }
    void load()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 10_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [idsKey, serverId])

  const downCount = checks.filter((check) => check.state === 'down').length
  const unknownCount = checks.filter((check) => check.state === 'unknown').length

  if (checksQuery.loading && checksQuery.data === null) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label={t('s.0490')} />
      </div>
    )
  }

  if (checksQuery.error && checksQuery.data === null) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
        <span>{t('s.0491')}{checksQuery.error}</span>
        <Button size="sm" variant="secondary" onClick={() => void checksQuery.refresh()}>
          {t('s.0053')}
        </Button>
      </div>
    )
  }

  if (checks.length === 0) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <EmptyState
          icon={<ShieldCheckIcon className="h-6 w-6" />}
          title={t('s.0492')}
          description={t('s.0493')}
        />
        <div className="pb-6 text-center">
          <Link to="/checks">
            <Button size="sm" variant="secondary">
              {t('s.0494')}
            </Button>
          </Link>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {downCount > 0 ? (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <AlertTriangleIcon className="h-4 w-4" />
          <span className="font-medium">
            {t('s.0495')} {downCount} {t('s.0496')}
            {unknownCount > 0 ? t('s.0497', { '0': unknownCount }) : ''}
          </span>
        </div>
      ) : null}

      {checks.map((check) => {
        const availability = availabilityTone(check.availability_24h)
        const last = check.last_result
        return (
          <section
            key={check.id}
            className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900"
          >
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="truncate text-sm font-semibold text-slate-900 dark:text-slate-100">
                    {check.name}
                  </h3>
                  <CheckKindBadge kind={check.kind} />
                  <CheckStateBadge state={check.state} />
                  {!check.enabled ? (
                    <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                      {t('s.0232')}
                    </span>
                  ) : null}
                </div>
                <p
                  className="mt-1 truncate font-mono text-[11px] text-slate-500 dark:text-slate-400"
                  title={check.target}
                >
                  {check.target}
                </p>
              </div>
              <div className="text-right text-[11px] text-slate-500 dark:text-slate-400">
                <p>{t('s.0498')} {check.interval_s}{t('s.0499')} {check.timeout_s}s</p>
                <p className={cn('mt-0.5 font-medium tabular-nums', availability.className)}>
                  {t('s.0500')} {availability.text}
                </p>
              </div>
            </div>

            <div className="mt-3">
              <CheckTimeline results={timelines[check.id] ?? []} loading={timelineLoading} />
            </div>

            {last ? (
              <p
                className={cn(
                  'mt-2 text-xs',
                  last.ok
                    ? 'text-slate-500 dark:text-slate-400'
                    : check.state === 'down'
                      ? 'text-rose-600 dark:text-rose-400'
                      : 'text-amber-600 dark:text-amber-400',
                )}
              >
                {t('s.0383')}{last.ok ? t('s.0300') : t('s.0301')} {formatRelativeTime(last.ts)} {t('s.0501')}{' '}
                {formatLatency(last.latency_ms)}
                {last.status_code !== null ? ` · HTTP ${last.status_code}` : ''}
                {!last.ok && check.state !== 'down' ? t('s.0502') : ''}
                {!last.ok && last.message ? ` · ${last.message}` : ''}
              </p>
            ) : (
              <p className="mt-2 text-xs text-slate-400 dark:text-slate-500">
                {t('s.0503')}
              </p>
            )}
          </section>
        )
      })}
    </div>
  )
}
