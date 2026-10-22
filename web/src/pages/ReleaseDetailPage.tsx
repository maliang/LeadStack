import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, type Release, type RollbackTarget } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { Button } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Modal } from '../components/Modal'
import { ReleaseStatusBadge } from '../components/DeployBadges'
import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  ClockIcon,
  RefreshIcon,
  RotateCcwIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ReleaseLogPanel } from '../components/ReleaseLogPanel'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'

import { t } from '../i18n'
function summarize(release: Release) {
  const success = release.runs.filter((run) => run.status === 'success').length
  const failed = release.runs.filter((run) => run.status === 'failed').length
  const active = release.runs.filter(
    (run) => run.status === 'running' || run.status === 'pending',
  ).length
  const canceled = release.runs.filter((run) => run.status === 'canceled').length
  return { success, failed, active, canceled, total: release.runs.length }
}

export default function ReleaseDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { toast } = useToast()
  const { can } = useAuth()
  const canWrite = can('deploy.write')

  const [activeServerId, setActiveServerId] = useState<string>('')
  const [actionKind, setActionKind] = useState<'retry' | 'rollback' | 'cancel' | null>(null)
  const [actionLoading, setActionLoading] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [targetOpen, setTargetOpen] = useState(false)
  const [targets, setTargets] = useState<RollbackTarget[]>([])
  const [targetsLoading, setTargetsLoading] = useState(false)
  const [targetsError, setTargetsError] = useState<string | null>(null)
  const [selectedTarget, setSelectedTarget] = useState('')

  const releaseQuery = usePolling(() => api.release(id ?? ''), 3_000, Boolean(id), id ?? '')
  const serversQuery = usePolling(api.servers, 30_000)
  const appsQuery = usePolling(api.apps, 30_000)

  const release = releaseQuery.data
  const streaming = release?.status === 'pending' || release?.status === 'running'
  const appName = useMemo(() => {
    if (!release) return t('s.0461')
    if (release.app_name) return release.app_name
    const app = (appsQuery.data ?? []).find((item) => item.id === release.app_id)
    return app?.name ?? t('s.0461')
  }, [release, appsQuery.data])
  const stats = release ? summarize(release) : null
  const appMode = useMemo(() => {
    if (!release) return 'release'
    const app = (appsQuery.data ?? []).find((item) => item.id === release.app_id)
    return app?.deploy_mode ?? 'release'
  }, [release, appsQuery.data])

  const openRollbackTargets = async () => {
    if (!release) return
    setTargetOpen(true)
    setTargetsLoading(true)
    setTargetsError(null)
    setSelectedTarget('')
    try {
      setTargets(await api.rollbackTargets(release.app_id))
    } catch (caught) {
      setTargets([])
      setTargetsError(errorMessage(caught, t('release.rollbackTargetsFailed')))
    } finally {
      setTargetsLoading(false)
    }
  }

  const handleTargetRollback = async () => {
    if (!release || !selectedTarget) return
    setActionLoading(true)
    setActionError(null)
    try {
      await api.rollbackRelease(release.id, { target_release_id: selectedTarget })
      toast(t('release.rollbackToTargetDone'), 'success')
      setTargetOpen(false)
      await releaseQuery.refresh()
    } catch (caught) {
      setTargetsError(errorMessage(caught, t('s.0769')))
    } finally {
      setActionLoading(false)
    }
  }

  const serverNameOf = useMemo(() => {
    const names = new Map((serversQuery.data ?? []).map((server) => [server.id, server.name]))
    return (serverId: string) => names.get(serverId) ?? serverId
  }, [serversQuery.data])

  const handleAction = async () => {
    if (!release || !actionKind) return
    setActionLoading(true)
    setActionError(null)
    try {
      if (actionKind === 'retry') {
        await api.retryRelease(release.id)
        toast(t('s.1044'), 'success')
      } else if (actionKind === 'rollback') {
        await api.rollbackRelease(release.id)
        toast(t('s.0767'), 'success')
      } else {
        await api.cancelRelease(release.id)
        toast(t('s.0768'), 'success')
      }
      setActionKind(null)
      await releaseQuery.refresh()
    } catch (caught) {
      setActionError(errorMessage(caught, t('s.0769')))
    } finally {
      setActionLoading(false)
    }
  }

  if (releaseQuery.loading && !release) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label={t('s.1045')} />
      </div>
    )
  }

  if (!release) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <p className="text-sm font-medium text-slate-900 dark:text-slate-100">{t('s.1046')}</p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          {releaseQuery.error ?? t('s.0772')}
        </p>
        <Link to="/apps" className="mt-4 inline-block">
          <Button variant="secondary" icon={<ArrowLeftIcon className="h-4 w-4" />}>
            {t('s.0773')}
          </Button>
        </Link>
      </div>
    )
  }

  const duration =
    release.started_at && release.finished_at
      ? Math.round((release.finished_at - release.started_at) / 1000)
      : null

  return (
    <div className="space-y-5">
      <div>
        <Link
          to={release.app_id ? `/apps/${encodeURIComponent(release.app_id)}` : '/apps'}
          className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500 transition-colors hover:text-indigo-600 dark:text-slate-400 dark:hover:text-indigo-400"
        >
          <ArrowLeftIcon className="h-3.5 w-3.5" />
          {t('s.1047')}
        </Link>

        <div className="mt-3 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="truncate text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
                {appName} · v{release.version}
              </h1>
              <ReleaseStatusBadge status={release.status} />
            </div>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {t('s.1048')} {formatDateTime(release.created_at)}（{formatRelativeTime(release.created_at)}）
              {duration !== null ? t('s.1049', { '0': duration }) : ''}
              {release.created_by ? t('s.1050', { '0': release.created_by }) : ''}
            </p>
            <p className="mt-0.5 font-mono text-[11px] text-slate-400 dark:text-slate-500">
              release {release.id}
              {release.artifact_id ? ` · artifact ${release.artifact_id}` : ''}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => void releaseQuery.refresh()}
              loading={releaseQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('s.0689')}
            </Button>
            {stats && stats.failed > 0 ? (
              <Button
                variant="secondary"
                disabled={!canWrite}
                onClick={() => {
                  setActionError(null)
                  setActionKind('retry')
                }}
                icon={<RotateCcwIcon className="h-4 w-4" />}
              >
                {t('s.1051')}{stats.failed}）
              </Button>
            ) : null}
            {stats && stats.success > 0 ? (
              <>
              <Button
                variant="secondary"
                disabled={!canWrite}
                onClick={() => {
                  setActionError(null)
                  setActionKind('rollback')
                }}
              >
                {t('s.0786')}
              </Button>
              <Button
                variant="secondary"
                disabled={!canWrite || appMode === 'script'}
                title={appMode === 'script' ? t('release.rollbackScriptUnsupported') : undefined}
                onClick={() => void openRollbackTargets()}
                icon={<ClockIcon className="h-4 w-4" />}
              >
                {t('release.rollbackToTarget')}
              </Button>
              </>
            ) : null}
            {stats && stats.active > 0 ? (
              <Button
                variant="danger"
                disabled={!canWrite}
                onClick={() => {
                  setActionError(null)
                  setActionKind('cancel')
                }}

              >
                {t('s.0024')}
              </Button>
            ) : null}
          </div>
        </div>
        {appMode === 'script' ? (
          <p className="mt-2 text-xs text-amber-700 dark:text-amber-300">
            {t('release.rollbackScriptUnsupported')}
          </p>
        ) : null}
      </div>

      {releaseQuery.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.1052')}{releaseQuery.error}{t('s.0999')}
        </div>
      ) : null}

      {streaming ? (
        <div className="flex items-center gap-2 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-700 dark:border-blue-500/30 dark:bg-blue-500/10 dark:text-blue-200">
          <span className="h-2 w-2 animate-pulse rounded-full bg-blue-500" />
          {t('s.1053')}
        </div>
      ) : null}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: t('s.0300'), value: stats?.success ?? 0, tone: 'text-emerald-600 dark:text-emerald-300' },
          { label: t('s.0301'), value: stats?.failed ?? 0, tone: 'text-rose-600 dark:text-rose-300' },
          { label: t('s.0415'), value: stats?.active ?? 0, tone: 'text-blue-600 dark:text-blue-300' },
          {
            label: t('s.1054'),
            value: t('s.1055', { '0': stats?.total ?? 0, '1': stats && stats.canceled > 0 ? t('releaseDetail.canceledPart', { '0': stats.canceled }) : '' }),
            tone: 'text-slate-700 dark:text-slate-200',
          },
        ].map((card) => (
          <div
            key={card.label}
            className="rounded-xl border border-slate-200 bg-white p-3.5 shadow-sm dark:border-slate-800 dark:bg-slate-900"
          >
            <span className="block text-xs text-slate-500 dark:text-slate-400">{card.label}</span>
            <span className={cn('mt-1 block text-xl font-semibold tabular-nums', card.tone)}>
              {card.value}
            </span>
          </div>
        ))}
      </div>

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1056')}</h2>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.1057')}
          </p>
        </div>

        {release.runs.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-slate-400 dark:text-slate-500">
            {t('s.1058')}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1080px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0087')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0533')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0432')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.1059')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.1060')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0782')}</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">{t('s.0539')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {release.runs.map((run) => {
                  const runDuration =
                    run.started_at && run.finished_at
                      ? Math.round((run.finished_at - run.started_at) / 1000)
                      : null
                  const isActive = activeServerId === run.server_id
                  return (
                    <tr
                      key={run.server_id}
                      onClick={() => setActiveServerId(run.server_id)}
                      className={cn(
                        'cursor-pointer transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60',
                        isActive && 'bg-indigo-50/50 dark:bg-indigo-500/5',
                      )}
                    >
                      <td className="px-4 py-3 align-top">
                        <span className="text-sm font-medium text-slate-800 dark:text-slate-100">
                          {run.server_name ?? serverNameOf(run.server_id)}
                        </span>
                        <span className="mt-0.5 block font-mono text-[11px] text-slate-400 dark:text-slate-500">
                          {run.server_id}
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <ReleaseStatusBadge status={run.status} />
                      </td>
                      <td className="px-4 py-3 align-top">
                        {run.exit_code === null ? (
                          <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                        ) : (
                          <span
                            className={cn(
                              'font-mono text-xs',
                              run.exit_code === 0
                                ? 'text-emerald-600 dark:text-emerald-400'
                                : 'text-rose-600 dark:text-rose-400',
                            )}
                          >
                            {run.exit_code}
                          </span>
                        )}
                      </td>
                      <td className="max-w-[180px] px-4 py-3 align-top">
                        {run.failed_step ? (
                          <span className="inline-flex items-center gap-1 rounded bg-rose-50 px-1.5 py-0.5 text-[11px] font-medium text-rose-700 dark:bg-rose-500/10 dark:text-rose-300">
                            <AlertTriangleIcon className="h-3 w-3" />
                            {run.failed_step}
                          </span>
                        ) : (
                          <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                        )}
                      </td>
                      <td className="max-w-[320px] px-4 py-3 align-top">
                        {run.message ? (
                          <span
                            className={cn(
                              'block truncate text-xs',
                              run.status === 'failed'
                                ? 'text-rose-600 dark:text-rose-400'
                                : 'text-slate-600 dark:text-slate-300',
                            )}
                            title={run.message}
                          >
                            {run.message}
                          </span>
                        ) : (
                          <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                        )}
                        {run.prev_release_id ? (
                          <span className="mt-0.5 block font-mono text-[11px] text-slate-400 dark:text-slate-500">
                            {t('s.1061')} {run.prev_release_id}
                          </span>
                        ) : null}
                      </td>
                      <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                        {runDuration === null ? '—' : t('s.0156', { '0': runDuration })}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex justify-end">
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={(event) => {
                              event.stopPropagation()
                              setActiveServerId(run.server_id)
                              document
                                .getElementById('release-log-panel')
                                ?.scrollIntoView({ behavior: 'smooth', block: 'start' })
                            }}
                          >
                            {t('s.1062')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section
        id="release-log-panel"
        className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1063')}</h2>
            <p className="mt-0.5 text-xs text-slate-400 dark:text-slate-500">
              {t('s.1064')}
            </p>
          </div>
          <span className="inline-flex items-center gap-1.5 text-xs text-slate-400 dark:text-slate-500">
            <ClockIcon className="h-3.5 w-3.5" />
            {release.finished_at
              ? t('s.1065', { '0': formatDateTime(release.finished_at) })
              : t('s.1066')}
          </span>
        </div>
        <div className="mt-3">
          <ReleaseLogPanel
            releaseId={release.id}
            runs={release.runs}
            streaming={streaming}
            activeServerId={activeServerId || undefined}
            onSelectServer={setActiveServerId}
          />
        </div>
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        {t('s.1067')}
      </p>

      <ConfirmDialog
        open={actionKind !== null}
        title={
          actionKind === 'retry'
            ? t('s.0794')
            : actionKind === 'rollback'
              ? t('s.0795')
              : t('s.0796')
        }
        description={t('s.0797', { '0': release.version })}
        confirmLabel={actionKind === 'cancel' ? t('s.0798') : t('s.0799')}
        tone={actionKind === 'cancel' ? 'danger' : 'primary'}
        loading={actionLoading}
        error={actionError}
        onClose={() => {
          setActionKind(null)
          setActionError(null)
        }}
        onConfirm={() => void handleAction()}
      >
        {actionKind === 'retry' ? (
          <p>{t('s.1068')} {stats?.failed ?? 0} {t('s.1069')}</p>
        ) : actionKind === 'rollback' ? (
          <p>{t('s.1070')}</p>
        ) : (
          <p>{t('s.1071')} {stats?.active ?? 0} {t('s.1072')}</p>
        )}
      </ConfirmDialog>
      <Modal
        open={targetOpen}
        onClose={() => setTargetOpen(false)}
        title={t('release.rollbackTargetTitle')}
        description={t('release.rollbackTargetDescription')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setTargetOpen(false)}>
              {t('common.close')}
            </Button>
            <Button
              disabled={!selectedTarget || actionLoading}
              loading={actionLoading}
              onClick={() => void handleTargetRollback()}
            >
              {t('release.rollbackTargetConfirm')}
            </Button>
          </>
        }
      >
        <div className="space-y-2">
          {targetsError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {targetsError}
            </p>
          ) : null}
          {targetsLoading ? (
            <InlineLoading label={t('release.rollbackTargetsLoading')} />
          ) : targets.length === 0 && !targetsError ? (
            <p className="rounded-md border border-slate-200 bg-slate-50 px-3 py-6 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
              {t('release.rollbackTargetsEmpty')}
            </p>
          ) : (
            targets.map((target) => (
              <label
                key={target.release_id}
                className={cn(
                  'flex cursor-pointer items-start gap-2 rounded-lg border px-3 py-2',
                  selectedTarget === target.release_id
                    ? 'border-indigo-500 bg-indigo-50/60 dark:border-indigo-400 dark:bg-indigo-500/10'
                    : 'border-slate-200 hover:border-slate-300 dark:border-slate-800 dark:hover:border-slate-700',
                )}
              >
                <input
                  type="radio"
                  name="rollback-target"
                  checked={selectedTarget === target.release_id}
                  onChange={() => setSelectedTarget(target.release_id)}
                  className="mt-1 h-3.5 w-3.5 text-indigo-600"
                />
                <span className="min-w-0 flex-1">
                  <span className="block font-mono text-sm text-slate-800 dark:text-slate-100">
                    {target.version}
                  </span>
                  <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
                    {t('release.rollbackTargetMeta', {
                      time: formatDateTime(target.finished_at ?? target.created_at),
                      count: target.server_ids.length,
                    })}
                  </span>
                </span>
              </label>
            ))
          )}
        </div>
      </Modal>
    </div>
  )
}
