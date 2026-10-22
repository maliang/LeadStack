import { useEffect, useMemo, useState } from 'react'
import { api, type DiscoverCandidate, type DiscoverResult } from '../lib/api'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { AlertTriangleIcon, FolderIcon, PackageIcon, ServerIcon } from './Icons'
import { InlineLoading } from './Loading'
import { Modal } from './Modal'

interface DiscoverDialogProps {
  open: boolean
  serverId: string
  serverName: string
  onClose: () => void
  onPick: (candidate: DiscoverCandidate) => void
}

const KIND_ORDER = ['systemd', 'docker', 'site']
const KIND_ICONS: Record<string, typeof ServerIcon> = {
  systemd: ServerIcon,
  docker: PackageIcon,
  site: FolderIcon,
}

/** 接管向导：发现目标机上已有应用，点击后回填新建应用表单。 */
export function DiscoverDialog({ open, serverId, serverName, onClose, onPick }: DiscoverDialogProps) {
  const { t } = useI18n()
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<DiscoverResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      const discovered = await api.discoverServer(serverId)
      setResult(discovered)
    } catch (caught) {
      setResult(null)
      setError(errorMessage(caught, t('discover.failed')))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!open) return
    setResult(null)
    setError(null)
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, serverId])

  const grouped = useMemo(() => {
    const map = new Map<string, DiscoverCandidate[]>()
    for (const candidate of result?.candidates ?? []) {
      if (!map.has(candidate.kind)) map.set(candidate.kind, [])
      map.get(candidate.kind)!.push(candidate)
    }
    return [...map.entries()].sort(
      (a, b) =>
        (KIND_ORDER.indexOf(a[0]) === -1 ? 99 : KIND_ORDER.indexOf(a[0])) -
        (KIND_ORDER.indexOf(b[0]) === -1 ? 99 : KIND_ORDER.indexOf(b[0])),
    )
  }, [result?.candidates])

  const rawEntries = Object.entries(result?.raw ?? {})

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('discover.title')}
      description={t('discover.description', { server: serverName })}
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button variant="secondary" onClick={() => void load()} loading={loading}>
            {t('discover.rescan')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {loading ? <InlineLoading label={t('discover.loading')} /> : null}

        {error ? (
          <div className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-relaxed text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            <p className="flex items-center gap-1.5 font-medium">
              <AlertTriangleIcon className="h-3.5 w-3.5" />
              {t('discover.failed')}
            </p>
            <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px]">
              {error}
            </pre>
          </div>
        ) : null}

        {!loading && !error && result && result.candidates.length === 0 ? (
          <p className="rounded-md border border-slate-200 bg-slate-50 px-3 py-6 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
            {t('discover.empty')}
          </p>
        ) : null}

        {!loading && !error
          ? grouped.map(([kind, candidates]) => {
              const Icon = KIND_ICONS[kind] ?? ServerIcon
              return (
                <div key={kind}>
                  <p className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
                    {t(`discover.kind.${kind}`)}（{candidates.length}）
                  </p>
                  <div className="mt-1.5 space-y-1.5">
                    {candidates.map((candidate, index) => (
                      <div
                        key={`${candidate.kind}-${candidate.name}-${index}`}
                        className="flex flex-wrap items-start gap-3 rounded-lg border border-slate-200 px-3 py-2.5 dark:border-slate-800"
                      >
                        <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-300">
                          <Icon className="h-4 w-4" />
                        </span>
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-medium text-slate-800 dark:text-slate-100">
                            {candidate.name}
                          </p>
                          <p className="mt-0.5 break-words text-xs text-slate-500 dark:text-slate-400">
                            {candidate.detail || '—'}
                          </p>
                          <div className="mt-1 flex flex-wrap gap-1.5 text-[11px] text-slate-400 dark:text-slate-500">
                            {candidate.suggested.deploy_mode ? (
                              <span className="rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
                                {candidate.suggested.deploy_mode}
                              </span>
                            ) : null}
                            {candidate.suggested.target_dir ? (
                              <span className="max-w-[260px] truncate rounded bg-slate-100 px-1.5 py-0.5 font-mono dark:bg-slate-800">
                                {candidate.suggested.target_dir}
                              </span>
                            ) : null}
                            {candidate.suggested.restart_cmd ? (
                              <span className="max-w-[260px] truncate rounded bg-slate-100 px-1.5 py-0.5 font-mono dark:bg-slate-800">
                                {candidate.suggested.restart_cmd}
                              </span>
                            ) : null}
                            {candidate.suggested.healthcheck_port ? (
                              <span className="rounded bg-slate-100 px-1.5 py-0.5 tabular-nums dark:bg-slate-800">
                                :{candidate.suggested.healthcheck_port}
                              </span>
                            ) : null}
                          </div>
                        </div>
                        <Button
                          size="sm"
                          onClick={() => {
                            onPick(candidate)
                            onClose()
                          }}
                        >
                          {t('discover.adopt')}
                        </Button>
                      </div>
                    ))}
                  </div>
                </div>
              )
            })
          : null}

        {rawEntries.length > 0 ? (
          <details className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-950/60">
            <summary className="cursor-pointer text-xs font-medium text-slate-600 dark:text-slate-300">
              {t('discover.raw')}
            </summary>
            <pre className="mt-2 max-h-56 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-950 p-2.5 font-mono text-[11px] text-slate-100">
              {rawEntries.map(([key, value]) => `${key}:\n${value}`).join('\n\n')}
            </pre>
          </details>
        ) : null}
      </div>
    </Modal>
  )
}
