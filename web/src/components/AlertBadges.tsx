import type { AlertSeverity, AlertSource, AlertStatus, ChannelKind } from '../lib/api'
import { cn } from '../lib/cn'
import { t } from '../i18n'

const statusConfig: Record<AlertStatus, { label: string; container: string; dot: string }> = {
  firing: {
    label: t('s.0078'),
    container:
      'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-400/30',
    dot: 'bg-rose-500',
  },
  acked: {
    label: t('s.0079'),
    container:
      'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-500/10 dark:text-blue-300 dark:ring-blue-400/30',
    dot: 'bg-blue-500',
  },
  resolved: {
    label: t('s.0080'),
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
    dot: 'bg-emerald-500',
  },
}

/** 告警状态徽标：firing 红 / acked 蓝 / resolved 绿。 */
export function AlertStatusBadge({ status, className }: { status: AlertStatus; className?: string }) {
  const config = statusConfig[status] ?? statusConfig.firing
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={t('s.0081', { '0': status })}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', config.dot)} aria-hidden="true" />
      {config.label}
    </span>
  )
}

const severityConfig: Record<AlertSeverity, { label: string; container: string }> = {
  critical: {
    label: t('s.0082'),
    container:
      'bg-rose-100 text-rose-800 ring-rose-600/20 dark:bg-rose-500/15 dark:text-rose-200 dark:ring-rose-400/30',
  },
  warning: {
    label: t('s.0083'),
    container:
      'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/30',
  },
  info: {
    label: t('s.0084'),
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
  },
}

/** 严重级别徽标。 */
export function SeverityBadge({ severity, className }: { severity: AlertSeverity; className?: string }) {
  const config = severityConfig[severity] ?? severityConfig.warning
  return (
    <span
      className={cn(
        'inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={t('s.0085', { '0': severity })}
    >
      {config.label}
    </span>
  )
}

const sourceConfig: Record<string, { label: string; container: string }> = {
  check: {
    label: t('s.0086'),
    container:
      'bg-sky-50 text-sky-700 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/30',
  },
  server: {
    label: t('s.0087'),
    container:
      'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-400/30',
  },
  metric: {
    label: t('s.0088'),
    container:
      'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30',
  },
}

export function AlertSourceBadge({ source, className }: { source: AlertSource | string; className?: string }) {
  const config = sourceConfig[source] ?? {
    label: source || t('s.0089'),
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
  }
  return (
    <span
      className={cn(
        'inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={t('s.0090', { '0': source })}
    >
      {config.label}
    </span>
  )
}

const channelKindConfig: Record<string, { label: string; container: string }> = {
  webhook: {
    label: 'Webhook',
    container:
      'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-400/30',
  },
  email: {
    label: t('s.0091'),
    container:
      'bg-sky-50 text-sky-700 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/30',
  },
  dingtalk: {
    label: t('s.0092'),
    container:
      'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-500/10 dark:text-blue-300 dark:ring-blue-400/30',
  },
  wecom: {
    label: t('s.0093'),
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
  },
  feishu: {
    label: t('s.0094'),
    container:
      'bg-cyan-50 text-cyan-700 ring-cyan-600/20 dark:bg-cyan-500/10 dark:text-cyan-300 dark:ring-cyan-400/30',
  },
  slack: {
    label: 'Slack',
    container:
      'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30',
  },
  telegram: {
    label: 'Telegram',
    container:
      'bg-cyan-50 text-cyan-700 ring-cyan-600/20 dark:bg-cyan-500/10 dark:text-cyan-300 dark:ring-cyan-400/30',
  },
}

export function ChannelKindBadge({ kind, className }: { kind: ChannelKind | string; className?: string }) {
  const config = channelKindConfig[kind] ?? {
    label: kind || t('s.0089'),
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
  }
  return (
    <span
      className={cn(
        'inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
    >
      {config.label}
    </span>
  )
}
