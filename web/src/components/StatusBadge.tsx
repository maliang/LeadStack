import type { ServerStatus } from '../lib/api'
import { cn } from '../lib/cn'

const statusConfig: Record<
  ServerStatus,
  { label: string; container: string; dot: string }
> = {
  online: {
    label: '在线',
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
    dot: 'bg-emerald-500',
  },
  offline: {
    label: '离线',
    container:
      'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-400/30',
    dot: 'bg-rose-500',
  },
  unknown: {
    label: '未知',
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
    dot: 'bg-slate-400 dark:bg-slate-500',
  },
}

export function StatusBadge({ status, className }: { status: ServerStatus; className?: string }) {
  const config = statusConfig[status] ?? statusConfig.unknown
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', config.dot)} aria-hidden="true" />
      {config.label}
    </span>
  )
}
