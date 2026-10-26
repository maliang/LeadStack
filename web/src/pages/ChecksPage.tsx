import { useMemo, useState } from 'react'
import {
  api,
  type CheckItem,
  type CheckState,
  type RunCheckResponse,
} from '../lib/api'
import { usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { formatLatency, formatRelativeTime } from '../lib/format'
import { Button, IconButton } from '../components/Button'
import { EnableSwitch } from '../components/EnableSwitch'
import { CheckFormDialog } from '../components/CheckFormDialog'
import {
  CheckKindBadge,
  CheckStateBadge,
  ProbeAgentBadge,
  availabilityTone,
} from '../components/CheckBadges'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import {
  AlertTriangleIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshIcon,
  ShieldCheckIcon,
  TrashIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { RunResultsDialog } from '../components/RunResultsDialog'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'

import { t } from '../i18n'
interface RunState {
  check: CheckItem
  loading: boolean
  error: string | null
  response: RunCheckResponse | null
}

const STATE_FILTERS: { value: CheckState | ''; label: string }[] = [
  { value: '', label: t('s.0933') },
  { value: 'up', label: t('s.0317') },
  { value: 'down', label: t('s.0318') },
  { value: 'unknown', label: t('s.0089') },
]

export default function ChecksPage() {
  const { toast } = useToast()
  const { can } = useAuth()
  const canWrite = can('check.write')
  const checksQuery = usePolling(api.checks, 10_000)
  const summaryQuery = usePolling(api.checksSummary, 10_000)
  const serversQuery = usePolling(api.servers, 30_000)
  const groupsQuery = usePolling(api.groups, 30_000)

  const [search, setSearch] = useState('')
  const [stateFilter, setStateFilter] = useState<CheckState | ''>('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<CheckItem | null>(null)
  const [deleting, setDeleting] = useState<CheckItem | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [togglingId, setTogglingId] = useState<string | null>(null)
  const [runState, setRunState] = useState<RunState | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const checks = checksQuery.data ?? []
  const summary = summaryQuery.data
  const servers = serversQuery.data ?? []

  const counts = useMemo(() => {
    const up = checks.filter((check) => check.state === 'up').length
    const down = checks.filter((check) => check.state === 'down').length
    const unknown = checks.filter((check) => check.state === 'unknown').length
    return {
      total: summary?.total ?? checks.length,
      up: summary?.up ?? up,
      down: summary?.down ?? down,
      unknown: summary?.unknown ?? unknown,
    }
  }, [checks, summary])

  const serverNameOf = useMemo(() => {
    const names = new Map(servers.map((server) => [server.id, server.name]))
    return (id: string) => names.get(id) ?? id
  }, [servers])

  const visibleChecks = useMemo(() => {
    const query = search.trim().toLowerCase()
    return checks.filter((check) => {
      if (stateFilter && check.state !== stateFilter) return false
      if (!query) return true
      const serverNames = check.server_ids.map((id) => serverNameOf(id)).join(' ')
      return (
        check.name.toLowerCase().includes(query) ||
        check.target.toLowerCase().includes(query) ||
        check.kind.toLowerCase().includes(query) ||
        serverNames.toLowerCase().includes(query)
      )
    })
  }, [checks, stateFilter, search, serverNameOf])

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([checksQuery.refresh(), summaryQuery.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  const handleToggleEnabled = async (check: CheckItem) => {
    setTogglingId(check.id)
    try {
      await api.updateCheck(check.id, { enabled: !check.enabled })
      toast(t('s.0934', { '0': check.name, '1': check.enabled ? t('common.disabled') : t('common.enabled') }), 'success')
      await refreshAll()
    } catch (caught) {
      toast(errorMessage(caught, t('s.0935')), 'error')
    } finally {
      setTogglingId(null)
    }
  }

  const handleRun = async (check: CheckItem) => {
    setRunState({ check, loading: true, error: null, response: null })
    try {
      const response = await api.runCheck(check.id)
      setRunState({ check, loading: false, error: null, response })
    } catch (caught) {
      setRunState({ check, loading: false, error: errorMessage(caught, t('s.0936')), response: null })
    } finally {
      void refreshAll()
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    setDeleteError(null)
    try {
      await api.deleteCheck(deleting.id)
      toast(t('s.0937', { '0': deleting.name }), 'success')
      setDeleting(null)
      await refreshAll()
    } catch (caught) {
      setDeleteError(errorMessage(caught, t('s.0938')))
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('s.0939')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {t('s.0940')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={refreshAll}
            loading={refreshing}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('s.0689')}
          </Button>
          {canWrite ? (
            <Button
              onClick={() => {
                setEditing(null)
                setFormOpen(true)
              }}
              icon={<PlusIcon className="h-4 w-4" />}
            >
              {t('s.0348')}
            </Button>
          ) : null}
        </div>
      </div>

      {counts.down > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span className="inline-flex items-center gap-2 font-medium">
            <AlertTriangleIcon className="h-4 w-4" />
            {t('s.0435')} {counts.down} {t('s.0941')}
          </span>
          <button
            type="button"
            onClick={() => setStateFilter('down')}
            className="text-xs font-medium underline-offset-2 hover:underline"
          >
            {t('s.0942')}
          </button>
        </div>
      ) : null}

      {checksQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('s.0491')}{checksQuery.error}</span>
          <Button size="sm" variant="secondary" onClick={refreshAll}>
            {t('s.0053')}
          </Button>
        </div>
      ) : null}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { key: '' as const, label: t('s.0943'), value: counts.total, tone: 'text-slate-900 dark:text-slate-100' },
          { key: 'up' as const, label: t('s.0944'), value: counts.up, tone: 'text-emerald-600 dark:text-emerald-300' },
          { key: 'down' as const, label: t('s.0945'), value: counts.down, tone: 'text-rose-600 dark:text-rose-300' },
          { key: 'unknown' as const, label: t('s.0946'), value: counts.unknown, tone: 'text-slate-500 dark:text-slate-400' },
        ].map((card) => (
          <button
            key={card.label}
            type="button"
            onClick={() => setStateFilter(stateFilter === card.key ? '' : card.key)}
            className={cn(
              'rounded-xl border bg-white p-3.5 text-left shadow-sm transition-colors dark:bg-slate-900',
              stateFilter === card.key && card.key !== ''
                ? 'border-indigo-400 dark:border-indigo-500/60'
                : 'border-slate-200 hover:border-slate-300 dark:border-slate-800 dark:hover:border-slate-700',
            )}
          >
            <span className="block text-xs text-slate-500 dark:text-slate-400">{card.label}</span>
            <span className={cn('mt-1 block text-xl font-semibold tabular-nums', card.tone)}>
              {card.value}
            </span>
          </button>
        ))}
      </div>

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0086')}</h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {visibleChecks.length}
                {visibleChecks.length !== checks.length ? ` / ${checks.length}` : ''}
              </span>
            </div>
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('s.0947')}
              className="h-9 w-52 rounded-md border border-slate-300 bg-white px-3 text-sm text-slate-700 shadow-sm outline-none transition placeholder:text-slate-400 focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
            />
            <select
              aria-label={t('s.0948')}
              value={stateFilter}
              onChange={(event) => setStateFilter(event.target.value as CheckState | '')}
              className="h-9 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
            >
              {STATE_FILTERS.map((option) => (
                <option key={option.label} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {checksQuery.updatedAt
              ? t('s.0949', { '0': new Date(checksQuery.updatedAt).toLocaleTimeString('zh-CN', { hour12: false }) })
              : t('s.0425')}
          </p>
        </div>

        {checksQuery.loading && checksQuery.data === null ? (
          <InlineLoading label={t('s.0950')} />
        ) : checks.length === 0 ? (
          <EmptyState
            icon={<ShieldCheckIcon className="h-6 w-6" />}
            title={t('s.0951')}
            description={t('s.0952')}
            actionLabel={canWrite ? t('s.0348') : undefined}
            onAction={
              canWrite
                ? () => {
                    setEditing(null)
                    setFormOpen(true)
                  }
                : undefined
            }
          />
        ) : visibleChecks.length === 0 ? (
          <EmptyState
            icon={<ShieldCheckIcon className="h-6 w-6" />}
            title={t('s.0953')}
            description={t('s.0954')}
            actionLabel={t('s.0955')}
            onAction={() => {
              setSearch('')
              setStateFilter('')
            }}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1180px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0531')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0399')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0956')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0498')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0212')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0533')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0500')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0746')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0422')}</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">{t('s.0539')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {visibleChecks.map((check) => {
                  const availability = availabilityTone(check.availability_24h)
                  const last = check.last_result
                  return (
                    <tr
                      key={check.id}
                      className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                    >
                      <td className="max-w-[220px] px-4 py-3 align-top">
                        <button
                          type="button"
                          disabled={!canWrite}
                          onClick={() => {
                            if (!canWrite) return
                            setEditing(check)
                            setFormOpen(true)
                          }}
                          className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                        >
                          {check.name}
                        </button>
                        <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                          {t('s.0957')} {check.timeout_s}{t('s.0958')}{' '}
                          {String(check.params.fail_threshold ?? 3)} {t('s.0959')}
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex flex-wrap items-center gap-1">
                          <CheckKindBadge kind={check.kind} />
                          {check.params.probe_via === 'agent' ? <ProbeAgentBadge /> : null}
                        </div>
                      </td>
                      <td className="max-w-[240px] px-4 py-3 align-top">
                        <span
                          className="block truncate font-mono text-xs text-slate-600 dark:text-slate-300"
                          title={check.target}
                        >
                          {check.target}
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top tabular-nums text-xs text-slate-600 dark:text-slate-300">
                        {check.interval_s}s
                      </td>
                      <td className="max-w-[200px] px-4 py-3 align-top">
                        {check.server_ids.length === 0 ? (
                          <span className="text-xs text-slate-400 dark:text-slate-500">
                            {check.server_count > 0 ? t('s.0960', { '0': check.server_count }) : t('s.0961')}
                          </span>
                        ) : (
                          <span
                            className="text-xs text-slate-600 dark:text-slate-300"
                            title={check.server_ids.map((id) => serverNameOf(id)).join('、')}
                          >
                            {check.server_ids.length} {t('s.0962')}{' '}
                            {check.server_ids
                              .slice(0, 2)
                              .map((id) => serverNameOf(id))
                              .join('、')}
                            {check.server_ids.length > 2 ? t('s.0963') : ''}
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <CheckStateBadge state={check.state} />
                      </td>
                      <td className={cn('px-4 py-3 align-top text-xs font-medium tabular-nums', availability.className)}>
                        {availability.text}
                      </td>
                      <td className="max-w-[220px] px-4 py-3 align-top">
                        {last ? (
                          <span className="block">
                            <span
                              className={cn(
                                'text-xs',
                                last.ok
                                  ? 'text-slate-600 dark:text-slate-300'
                                  : check.state === 'down'
                                    ? 'text-rose-600 dark:text-rose-400'
                                    : 'text-amber-600 dark:text-amber-400',
                              )}
                            >
                              {last.ok ? t('s.0300') : t('s.0301')} · {formatLatency(last.latency_ms)}
                              {!last.ok && check.state !== 'down' ? t('s.0964') : ''}
                            </span>
                            <span className="block text-[11px] text-slate-400 dark:text-slate-500">
                              {formatRelativeTime(last.ts)}
                              {last.message ? ` · ${last.message.slice(0, 28)}` : ''}
                            </span>
                          </span>
                        ) : (
                          <span className="text-xs text-slate-400 dark:text-slate-500">{t('s.0965')}</span>
                        )}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <EnableSwitch
                          checked={check.enabled}
                          loading={togglingId === check.id}
                          onChange={() => {
                            if (canWrite) void handleToggleEnabled(check)
                          }}
                        />
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex items-center justify-end gap-0.5">
                          <IconButton
                            label={t('s.0966')}
                            onClick={() => void handleRun(check)}
                            disabled={!canWrite || (runState?.loading ?? false)}
                          >
                            <PlayIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label={t('s.0347')}
                            disabled={!canWrite}
                            onClick={() => {
                              setEditing(check)
                              setFormOpen(true)
                            }}
                          >
                            <PencilIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label={t('s.0967')}
                            tone="danger"
                            disabled={!canWrite}
                            onClick={() => {
                              setDeleteError(null)
                              setDeleting(check)
                            }}
                          >
                            <TrashIcon className="h-4 w-4" />
                          </IconButton>
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

      <p className="text-xs text-slate-400 dark:text-slate-500">
        {t('s.0968')}
      </p>

      <CheckFormDialog
        open={formOpen}
        check={editing}
        servers={servers}
        groups={groupsQuery.data ?? []}
        onClose={() => {
          setFormOpen(false)
          setEditing(null)
        }}
        onSaved={() => void refreshAll()}
      />

      <RunResultsDialog
        open={runState !== null}
        title={runState ? t('s.0969', { '0': runState.check.name }) : t('s.0966')}
        loading={runState?.loading ?? false}
        error={runState?.error ?? null}
        response={runState?.response ?? null}
        serverNameOf={serverNameOf}
        onClose={() => setRunState(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('s.0967')}
        description={deleting ? t('s.0970', { '0': deleting.name }) : undefined}
        confirmLabel={t('s.0295')}
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => {
          setDeleting(null)
          setDeleteError(null)
        }}
        onConfirm={() => void handleDelete()}
      >
        <p>
          {t('s.0971')}
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deleting?.name}」
          </span>
          {t('s.0972')}
        </p>
      </ConfirmDialog>
    </div>
  )
}
