import type { AlertEvent, AlertItem } from '../lib/api'
import { cn } from '../lib/cn'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { AlertStatusBadge, SeverityBadge } from './AlertBadges'
import { BellIcon, CheckCircleIcon, ClockIcon, HistoryIcon, SpinnerIcon } from './Icons'
import { useNow } from '../lib/hooks'

const EVENT_LABELS: Record<string, string> = {
  firing: '触发告警',
  trigger: '触发告警',
  triggered: '触发告警',
  alert: '触发告警',
  notify: '发送通知',
  notified: '发送通知',
  notification: '发送通知',
  renotify: '再次通知（超出静默去重窗口）',
  renotified: '再次通知（超出静默去重窗口）',
  dedup: '重复触发（去重，不新建告警）',
  retrigger: '重复触发（去重，不新建告警）',
  resolved: '异常恢复',
  resolve: '异常恢复',
  recovery: '异常恢复',
  recovered: '异常恢复',
  ack: '人工确认',
  acked: '人工确认',
  silence: '静默生效',
  silenced: '静默生效',
  unsilence: '静默结束',
}

const EVENT_DOT: Record<string, string> = {
  firing: 'bg-rose-500',
  notify: 'bg-indigo-500',
  renotify: 'bg-amber-500',
  dedup: 'bg-amber-400',
  resolved: 'bg-emerald-500',
  ack: 'bg-blue-500',
  silence: 'bg-slate-400',
}

function eventLabel(kind: string): string {
  return EVENT_LABELS[kind] ?? kind
}

function eventDot(kind: string): string {
  const normalized = kind.toLowerCase()
  for (const [key, color] of Object.entries(EVENT_DOT)) {
    if (normalized.includes(key)) return color
  }
  return 'bg-slate-400'
}

function FactCard({
  icon,
  label,
  value,
  hint,
  tone = 'default',
}: {
  icon: React.ReactNode
  label: string
  value: string
  hint?: string
  tone?: 'default' | 'danger' | 'success' | 'info'
}) {
  const toneClass =
    tone === 'danger'
      ? 'text-rose-700 dark:text-rose-300'
      : tone === 'success'
        ? 'text-emerald-700 dark:text-emerald-300'
        : tone === 'info'
          ? 'text-blue-700 dark:text-blue-300'
          : 'text-slate-800 dark:text-slate-100'
  return (
    <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
      <div className="flex items-center gap-1.5 text-[11px] text-slate-500 dark:text-slate-400">
        {icon}
        {label}
      </div>
      <p className={cn('mt-1 text-sm font-medium', toneClass)}>{value}</p>
      {hint ? <p className="mt-0.5 text-[11px] text-slate-400 dark:text-slate-500">{hint}</p> : null}
    </div>
  )
}

interface AlertDetailPanelProps {
  alert: AlertItem | null
  loading: boolean
  error: string | null
  ruleNameOf: (ruleId: string) => string
  serverNameOf: (serverId: string) => string
}

/**
 * 告警详情：四要素（何时触发 / 通知了哪些渠道 / 何时恢复 / 谁在何时确认）+ 事件轨迹。
 * 排障时最需要的就是这一段。
 */
export function AlertDetailPanel({
  alert,
  loading,
  error,
  ruleNameOf,
  serverNameOf,
}: AlertDetailPanelProps) {
  const now = useNow(1_000)

  if (loading && !alert) {
    return (
      <div className="flex items-center justify-center gap-2 px-4 py-8 text-sm text-slate-500 dark:text-slate-400">
        <SpinnerIcon className="h-4 w-4" />
        正在加载告警详情与事件轨迹…
      </div>
    )
  }

  if (error && !alert) {
    return (
      <p className="m-4 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
        加载告警详情失败：{error}
      </p>
    )
  }

  if (!alert) return null

  const startedAt = alert.started_at ?? null
  const durationEnd = alert.resolved_at ?? now
  const durationText =
    startedAt === null
      ? '—'
      : (() => {
          const seconds = Math.max(0, Math.floor((durationEnd - startedAt) / 1000))
          const hours = Math.floor(seconds / 3600)
          const minutes = Math.floor((seconds % 3600) / 60)
          if (hours >= 24) return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时`
          if (hours > 0) return `${hours} 小时 ${minutes} 分钟`
          if (minutes > 0) return `${minutes} 分钟`
          return `${seconds} 秒`
        })()

  const notifyEvents = alert.events.filter((event) => /notif|notify|通知/i.test(event.kind))
  const ruleLabel = alert.rule_name ?? (alert.rule_id ? ruleNameOf(alert.rule_id) : '—')
  const serverLabel = alert.server_name ?? (alert.server_id ? serverNameOf(alert.server_id) : '—')

  return (
    <div className="space-y-4 border-t border-slate-200 bg-slate-50/60 px-4 py-4 dark:border-slate-800 dark:bg-slate-950/40">
      <div className="flex flex-wrap items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
        <AlertStatusBadge status={alert.status} />
        <SeverityBadge severity={alert.severity} />
        <span>规则：{ruleLabel}</span>
        <span>· 服务器：{serverLabel}</span>
        {alert.silence_until && alert.silence_until > now ? (
          <span className="text-slate-600 dark:text-slate-300">
            · 静默至 {formatDateTime(alert.silence_until)}
          </span>
        ) : null}
        <span className="font-mono text-[11px] text-slate-400 dark:text-slate-500">
          · {alert.dedup_key}
        </span>
      </div>

      {alert.message ? (
        <p className="rounded-md border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-200">
          {alert.message}
        </p>
      ) : null}

      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-4">
        <FactCard
          icon={<ClockIcon className="h-3.5 w-3.5" />}
          label="何时触发"
          value={formatDateTime(startedAt)}
          hint={`已持续 ${durationText}`}
          tone="danger"
        />
        <FactCard
          icon={<BellIcon className="h-3.5 w-3.5" />}
          label="通知情况"
          value={`共 ${alert.notify_count} 次`}
          hint={
            alert.last_notify_at
              ? `最近通知 ${formatRelativeTime(alert.last_notify_at, now)}`
              : '尚未通知（可能命中静默或去重窗口）'
          }
          tone="info"
        />
        <FactCard
          icon={<CheckCircleIcon className="h-3.5 w-3.5" />}
          label="何时恢复"
          value={alert.resolved_at ? formatDateTime(alert.resolved_at) : '尚未恢复'}
          hint={
            alert.resolved_at && startedAt
              ? `从触发到恢复 ${Math.max(
                  0,
                  Math.round((alert.resolved_at - startedAt) / 1000),
                )} 秒`
              : '异常仍在持续'
          }
          tone={alert.resolved_at ? 'success' : 'default'}
        />
        <FactCard
          icon={<HistoryIcon className="h-3.5 w-3.5" />}
          label="确认情况"
          value={alert.acked_at ? formatDateTime(alert.acked_at) : '未确认'}
          hint={
            alert.acked_at
              ? `确认人：${alert.acked_by ?? '人工确认（后端未返回操作人）'}`
              : '确认只代表有人接手，不代表已恢复'
          }
          tone={alert.acked_at ? 'info' : 'default'}
        />
      </div>

      <div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-xs font-semibold text-slate-700 dark:text-slate-200">
            事件轨迹（{alert.events.length} 条）
          </h4>
          {notifyEvents.length > 0 ? (
            <p className="text-[11px] text-slate-400 dark:text-slate-500">
              通知相关 {notifyEvents.length} 条，渠道与失败原因见下方明细
            </p>
          ) : null}
        </div>

        {alert.events.length === 0 ? (
          <p className="mt-2 rounded-md border border-dashed border-slate-300 px-3 py-3 text-xs text-slate-400 dark:border-slate-700 dark:text-slate-500">
            暂无事件记录（后端未返回 events）。触发 / 通知 / 恢复 / 确认都会记录在这里。
          </p>
        ) : (
          <ol className="mt-2 space-y-0">
            {alert.events.map((event: AlertEvent, index) => (
              <li key={`${event.ts ?? index}-${index}`} className="flex gap-3">
                <div className="flex flex-col items-center">
                  <span
                    className={cn('mt-1.5 h-2 w-2 shrink-0 rounded-full', eventDot(event.kind))}
                    aria-hidden="true"
                  />
                  {index < alert.events.length - 1 ? (
                    <span className="w-px flex-1 bg-slate-200 dark:bg-slate-800" />
                  ) : null}
                </div>
                <div className="min-w-0 flex-1 pb-3">
                  <p className="text-xs font-medium text-slate-800 dark:text-slate-100">
                    {eventLabel(event.kind)}
                    <span className="ml-2 font-normal text-slate-400 dark:text-slate-500">
                      {formatDateTime(event.ts)}（{formatRelativeTime(event.ts, now)}）
                    </span>
                  </p>
                  {event.detail ? (
                    <p className="mt-0.5 whitespace-pre-wrap break-words text-xs leading-relaxed text-slate-600 dark:text-slate-300">
                      {event.detail}
                    </p>
                  ) : null}
                </div>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  )
}
