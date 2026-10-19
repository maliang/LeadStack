import type { ReactNode } from 'react'
import { cn } from '../lib/cn'

type Tone = 'default' | 'success' | 'danger' | 'warning'

interface StatCardProps {
  label: string
  value: ReactNode
  hint?: ReactNode
  icon: ReactNode
  tone?: Tone
  loading?: boolean
}

const toneStyles: Record<Tone, { icon: string; value: string }> = {
  default: {
    icon: 'bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300',
    value: 'text-slate-900 dark:text-slate-100',
  },
  success: {
    icon: 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10 dark:text-emerald-300',
    value: 'text-emerald-700 dark:text-emerald-300',
  },
  danger: {
    icon: 'bg-rose-50 text-rose-600 dark:bg-rose-500/10 dark:text-rose-300',
    value: 'text-rose-700 dark:text-rose-300',
  },
  warning: {
    icon: 'bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-300',
    value: 'text-amber-700 dark:text-amber-300',
  },
}

export function StatCard({ label, value, hint, icon, tone = 'default', loading = false }: StatCardProps) {
  const styles = toneStyles[tone]
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm font-medium text-slate-500 dark:text-slate-400">{label}</p>
          <p className={cn('mt-1.5 text-2xl font-semibold tabular-nums', styles.value)}>
            {loading ? <span className="inline-block h-6 w-10 animate-pulse rounded bg-slate-200 dark:bg-slate-800" /> : value}
          </p>
          {hint ? <p className="mt-1 truncate text-xs text-slate-400 dark:text-slate-500">{hint}</p> : null}
        </div>
        <div className={cn('flex h-9 w-9 shrink-0 items-center justify-center rounded-lg', styles.icon)}>
          {icon}
        </div>
      </div>
    </div>
  )
}
