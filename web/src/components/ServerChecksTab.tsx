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
        <InlineLoading label="正在加载该服务器的检测项…" />
      </div>
    )
  }

  if (checksQuery.error && checksQuery.data === null) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
        <span>加载检测项失败：{checksQuery.error}</span>
        <Button size="sm" variant="secondary" onClick={() => void checksQuery.refresh()}>
          重试
        </Button>
      </div>
    )
  }

  if (checks.length === 0) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <EmptyState
          icon={<ShieldCheckIcon className="h-6 w-6" />}
          title="该服务器还没有适用的检测项"
          description="到检测页创建检测项并把它应用到这台服务器，结果会在这里形成时间线"
        />
        <div className="pb-6 text-center">
          <Link to="/checks">
            <Button size="sm" variant="secondary">
              去检测页创建
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
            该服务器有 {downCount} 个检测项异常
            {unknownCount > 0 ? `，另有 ${unknownCount} 个暂无结果` : ''}
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
                      已停用
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
                <p>间隔 {check.interval_s}s · 超时 {check.timeout_s}s</p>
                <p className={cn('mt-0.5 font-medium tabular-nums', availability.className)}>
                  24h 可用率 {availability.text}
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
                最近{last.ok ? '成功' : '失败'} {formatRelativeTime(last.ts)} · 延迟{' '}
                {formatLatency(last.latency_ms)}
                {last.status_code !== null ? ` · HTTP ${last.status_code}` : ''}
                {!last.ok && check.state !== 'down' ? ' · 待确认（未达失败阈值）' : ''}
                {!last.ok && last.message ? ` · ${last.message}` : ''}
              </p>
            ) : (
              <p className="mt-2 text-xs text-slate-400 dark:text-slate-500">
                还没有结果，等待下一次调度或到检测页点「立即执行」。
              </p>
            )}
          </section>
        )
      })}
    </div>
  )
}
