import { useEffect, useState } from 'react'
import { api, type DeployPreset } from '../lib/api'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { EmptyState } from './EmptyState'
import { PackageIcon, PlayIcon, RefreshIcon } from './Icons'
import { InlineLoading } from './Loading'
import { Modal } from './Modal'

interface PresetDialogProps {
  open: boolean
  canWrite: boolean
  onClose: () => void
  onApply: (preset: DeployPreset) => Promise<void>
}

/** 从内置部署预设创建：先落模板，再回填新建应用。 */
export function PresetDialog({ open, canWrite, onClose, onApply }: PresetDialogProps) {
  const { t } = useI18n()
  const [loading, setLoading] = useState(false)
  const [presets, setPresets] = useState<DeployPreset[]>([])
  const [error, setError] = useState<string | null>(null)
  const [applyingKey, setApplyingKey] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setPresets(await api.listDeployPresets())
    } catch (caught) {
      setPresets([])
      setError(errorMessage(caught, t('preset.loadFailed')))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!open) return
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const apply = async (preset: DeployPreset) => {
    setApplyingKey(preset.key)
    setError(null)
    try {
      await onApply(preset)
    } catch (caught) {
      setError(errorMessage(caught, t('preset.applyFailed')))
    } finally {
      setApplyingKey(null)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('preset.title')}
      description={t('preset.description')}
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button variant="secondary" onClick={() => void load()} loading={loading}>
            {t('preset.reload')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
        {loading && presets.length === 0 ? <InlineLoading label={t('common.loading')} /> : null}
        {!loading && presets.length === 0 && !error ? (
          <EmptyState
            icon={<PackageIcon className="h-6 w-6" />}
            title={t('preset.empty')}
            description={t('preset.emptyHint')}
          />
        ) : null}
        <div className="grid gap-2 sm:grid-cols-2">
          {presets.map((preset) => (
            <div
              key={preset.key}
              className="flex flex-col rounded-lg border border-slate-200 p-3 dark:border-slate-800"
            >
              <div className="flex items-start justify-between gap-2">
                <p className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {preset.name}
                </p>
                <span className="shrink-0 rounded bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                  {preset.template.kind}
                </span>
              </div>
              <p className="mt-1 text-xs leading-relaxed text-slate-500 dark:text-slate-400">
                {preset.description}
              </p>
              {preset.hints.length > 0 ? (
                <ul className="mt-1.5 space-y-0.5 text-[11px] leading-relaxed text-amber-700 dark:text-amber-300">
                  {preset.hints.map((hint) => (
                    <li key={hint}>· {hint}</li>
                  ))}
                </ul>
              ) : null}
              <div className="mt-1.5 flex flex-wrap gap-1 text-[10px] text-slate-400 dark:text-slate-500">
                {preset.defaults.deploy_mode ? (
                  <span className="rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
                    {preset.defaults.deploy_mode}
                  </span>
                ) : null}
                {preset.defaults.provider ? (
                  <span className="rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
                    {preset.defaults.provider}
                  </span>
                ) : null}
                {preset.defaults.keep_paths ? (
                  <span className="max-w-[200px] truncate rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
                    keep: {preset.defaults.keep_paths}
                  </span>
                ) : null}
              </div>
              <div className="mt-2 flex justify-end">
                <Button
                  size="sm"
                  disabled={!canWrite}
                  loading={applyingKey === preset.key}
                  onClick={() => void apply(preset)}
                  icon={<PlayIcon className="h-3.5 w-3.5" />}
                >
                  {t('preset.use')}
                </Button>
              </div>
            </div>
          ))}
        </div>
        <p className="flex items-center gap-1.5 text-[11px] text-slate-400 dark:text-slate-500">
          <RefreshIcon className="h-3 w-3" />
          {t('preset.footnote')}
        </p>
      </div>
    </Modal>
  )
}
