import type { BackupInfo, BackupVerifyResult } from '../lib/api'
import type { BackupErrorInsight } from '../lib/backup'
import { formatBytes, formatDateTime, truncateMiddle } from '../lib/format'
import { BackupErrorCard } from './BackupErrorCard'
import { CheckCircleIcon, SpinnerIcon } from './Icons'
import { Modal } from './Modal'
import { Button } from './Button'
import { t } from '../i18n'

interface BackupVerifyDialogProps {
  open: boolean
  info: BackupInfo | null
  loading: boolean
  result: BackupVerifyResult | null
  insight: BackupErrorInsight | null
  onClose: () => void
}

/** 校验结果：成功展示 manifest 摘要（含文件清单/schema/创建时间），失败按三类错误给中文说明。 */
export function BackupVerifyDialog({
  open,
  info,
  loading,
  result,
  insight,
  onClose,
}: BackupVerifyDialogProps) {
  const manifest = result?.manifest ?? null
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('s.0262')}
      description={info ? t('s.0263', { '0': info.name }) : undefined}
      size="lg"
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('s.0264')}
        </Button>
      }
    >
      {loading ? (
        <div className="flex items-center justify-center gap-2 py-10 text-sm text-slate-500 dark:text-slate-400">
          <SpinnerIcon className="h-4 w-4" />
          {t('s.0265')}
        </div>
      ) : insight ? (
        <BackupErrorCard insight={insight} />
      ) : result?.ok && manifest ? (
        <div className="space-y-4">
          <div className="flex items-start gap-2 rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-3 dark:border-emerald-500/30 dark:bg-emerald-500/10">
            <CheckCircleIcon className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-300" />
            <div>
              <p className="text-sm font-semibold text-emerald-800 dark:text-emerald-200">
                {t('s.0266')}
              </p>
              <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
                {t('s.0267')}
              </p>
            </div>
          </div>

          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border border-slate-200 p-3 text-sm dark:border-slate-800 sm:grid-cols-3">
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0268')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {manifest.format_version ?? '—'}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0269')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {manifest.app_version ?? '—'}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0270')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {manifest.schema_version ?? '—'}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0271')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {formatDateTime(manifest.created_at)}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0272')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {manifest.includes_artifacts ? t('s.0273') : t('s.0274')}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0275')}</dt>
              <dd className="font-medium text-slate-800 dark:text-slate-100">
                {manifest.files.length}
              </dd>
            </div>
          </dl>

          <div>
            <p className="mb-1.5 text-xs font-medium text-slate-700 dark:text-slate-200">
              {t('s.0276')}
            </p>
            <div className="max-h-56 overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
              <table className="w-full min-w-[420px] border-collapse text-xs">
                <thead>
                  <tr className="border-b border-slate-200 bg-slate-50 text-left text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                    <th className="px-3 py-1.5 font-medium">{t('s.0277')}</th>
                    <th className="px-3 py-1.5 font-medium">{t('s.0278')}</th>
                    <th className="px-3 py-1.5 font-medium">SHA256</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                  {manifest.files.map((file) => (
                    <tr key={file.name}>
                      <td className="px-3 py-1.5 font-mono text-slate-700 dark:text-slate-200">
                        {file.name}
                      </td>
                      <td className="px-3 py-1.5 tabular-nums text-slate-600 dark:text-slate-300">
                        {formatBytes(file.size_bytes)}
                      </td>
                      <td className="px-3 py-1.5 font-mono text-slate-500 dark:text-slate-400">
                        <span title={file.sha256 ?? undefined}>
                          {truncateMiddle(file.sha256, 16, 8)}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      ) : (
        <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.0279')}
        </p>
      )}
    </Modal>
  )
}
