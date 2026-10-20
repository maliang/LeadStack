import type { BackupErrorInsight } from '../lib/backup'
import { AlertTriangleIcon, InfoIcon } from './Icons'
import { t } from '../i18n'

const KIND_LABEL: Record<BackupErrorInsight['kind'], string> = {
  bad_passphrase: t('s.0256'),
  corrupt: t('s.0257'),
  schema_too_new: t('s.0258'),
  unknown: t('s.0259'),
}

/** 三类备份错误各自的中文说明与「该怎么办」，避免统一显示一句「失败」。 */
export function BackupErrorCard({ insight }: { insight: BackupErrorInsight }) {
  return (
    <div className="rounded-lg border border-rose-200 bg-rose-50 p-3.5 dark:border-rose-500/30 dark:bg-rose-500/10">
      <div className="flex flex-wrap items-center gap-2">
        <AlertTriangleIcon className="h-4 w-4 shrink-0 text-rose-600 dark:text-rose-300" />
        <p className="text-sm font-semibold text-rose-800 dark:text-rose-200">{insight.title}</p>
        <span className="rounded bg-rose-100 px-1.5 py-0.5 text-[11px] font-medium text-rose-700 dark:bg-rose-500/15 dark:text-rose-200">
          {KIND_LABEL[insight.kind]}
        </span>
      </div>
      <p className="mt-1.5 text-xs leading-relaxed text-rose-700 dark:text-rose-200/90">
        {insight.reason}
      </p>
      <div className="mt-2 rounded-md bg-white/70 px-3 py-2 dark:bg-slate-950/40">
        <p className="flex items-center gap-1.5 text-xs font-medium text-slate-700 dark:text-slate-200">
          <InfoIcon className="h-3.5 w-3.5" />
          {t('s.0260')}
        </p>
        <p className="mt-1 whitespace-pre-wrap text-xs leading-relaxed text-slate-600 dark:text-slate-300">
          {insight.guidance}
        </p>
      </div>
      <details className="mt-2">
        <summary className="cursor-pointer text-[11px] text-rose-600/80 hover:text-rose-700 dark:text-rose-300/80">
          {t('s.0261')}
        </summary>
        <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-950/5 p-2 font-mono text-[11px] text-slate-600 dark:bg-black/20 dark:text-slate-300">
          {insight.raw}
        </pre>
      </details>
    </div>
  )
}
