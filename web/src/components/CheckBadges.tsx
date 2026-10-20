import type { CheckState } from '../lib/api'
import { cn } from '../lib/cn'
import { t } from '../i18n'

const kindConfig: Record<string, { label: string; container: string }> = {
  tcp: {
    label: 'TCP',
    container:
      'bg-sky-50 text-sky-700 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/30',
  },
  http: {
    label: 'HTTP',
    container:
      'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-400/30',
  },
  ping: {
    label: 'Ping',
    container:
      'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30',
  },
  process: {
    label: t('s.0314'),
    container:
      'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/30',
  },
  script: {
    label: t('s.0315'),
    container:
      'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-400/30',
  },
}

/** 检测类型徽标：TCP / HTTP / Ping / 进程 / 脚本。 */
export function CheckKindBadge({ kind, className }: { kind: string; className?: string }) {
  const config = kindConfig[kind] ?? {
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
      title={t('s.0316', { '0': kind })}
    >
      {config.label}
    </span>
  )
}

const stateConfig: Record<CheckState, { label: string; container: string; dot: string }> = {
  up: {
    label: t('s.0317'),
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
    dot: 'bg-emerald-500',
  },
  down: {
    label: t('s.0318'),
    container:
      'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-400/30',
    dot: 'bg-rose-500',
  },
  unknown: {
    label: t('s.0089'),
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
    dot: 'bg-slate-400 dark:bg-slate-500',
  },
}

/** 检测状态徽标：up 绿 / down 红 / unknown 灰（title 里保留英文口径）。 */
export function CheckStateBadge({ state, className }: { state: CheckState; className?: string }) {
  const config = stateConfig[state] ?? stateConfig.unknown
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={t('s.0081', { '0': state })}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', config.dot)} aria-hidden="true" />
      {config.label}
    </span>
  )
}

/** 24h 可用率文案与配色。 */
export function availabilityTone(value: number | null): { text: string; className: string } {
  if (value === null) return { text: '—', className: 'text-slate-400 dark:text-slate-500' }
  const text = `${value.toFixed(value >= 99.95 ? 1 : 2)}%`
  if (value >= 99) return { text, className: 'text-emerald-600 dark:text-emerald-300' }
  if (value >= 95) return { text, className: 'text-amber-600 dark:text-amber-400' }
  return { text, className: 'text-rose-600 dark:text-rose-400' }
}
