import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  api,
  type BatchAction,
  type BatchResponse,
  type Server,
  type ServerFilters,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { useReleaseRuns } from '../lib/useReleaseRuns'
import { errorMessage } from '../lib/errors'
import { formatClockTime, formatRelativeTime, formatUptime } from '../lib/format'
import { AddServerWizard } from '../components/AddServerWizard'
import { LocalServerDialog } from '../components/LocalServerDialog'
import { NetworkFallbackBanner } from '../components/NetworkFallbackBanner'
import { BatchActionBar } from '../components/BatchActionBar'
import { BatchResultDialog } from '../components/BatchResultDialog'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import {
  AlertTriangleIcon,
  BellIcon,
  ChevronRightIcon,
  FolderIcon,
  OfflineIcon,
  PlusIcon,
  PulseIcon,
  RefreshIcon,
  ServerIcon,
  ShieldCheckIcon,

} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ServerFiltersBar } from '../components/ServerFiltersBar'
import { DeleteServerDialog, EditServerDialog } from '../components/ServerDialogs'
import { ServerTable } from '../components/ServerTable'
import { ReleaseStatusBadge } from '../components/DeployBadges'
import { StatCard } from '../components/StatCard'
import { TagsDialog } from '../components/TagsDialog'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'

import { t } from '../i18n'
interface BatchState {
  open: boolean
  title: string
  loading: boolean
  error: string | null
  response: BatchResponse | null
}

export default function DashboardPage() {
  const { toast } = useToast()
  const { can, scope } = useAuth()
  const [filters, setFilters] = useState<ServerFilters>({})
  const [groupId, setGroupId] = useState('')
  const filterKey = JSON.stringify(filters)

  const serversQuery = usePolling(() => api.servers(filters), 10_000, true, filterKey)
  const infoQuery = usePolling(api.systemInfo, 10_000)
  const groupsQuery = usePolling(api.groups, 10_000)
  const serverGroupsQuery = usePolling(api.serverGroups, 30_000)
  const checksSummaryQuery = usePolling(api.checksSummary, 10_000)
  const alertsSummaryQuery = usePolling(api.alertsSummary, 10_000)
  // 「最近发布」读 GET /releases（internal/api/router.go:96 只校验 deploy.read，**不要求**全局范围）：
  // 非全局范围的调用由后端按「目标服务器 ∩ 角色范围」过滤（internal/api/handlers_deploy.go:873-893，
  // 契约 §53.3），所以组范围且有 deploy.read 的运维角色能看到自己范围内的发布，前端不该替他挡掉。
  // 真正没有数据的只有「没有 deploy.read」这一种（过去还会每 10 秒吃一次静默 403），只在此时于发请求
  // 之前门控（usePolling 的 enabled：web/src/lib/hooks.ts:59-64 在 false 时既不请求也不挂定时器）。
  const canViewReleases = can('deploy.read')
  // 用 errorMessage 包一层：usePolling 只保留错误文案字符串，裸传会丢掉 ApiError 的 requestId；
  // 包一层后 releasesQuery.error 自带「（请求编号 xxx）」，与错误 toast 同一口径（errors.ts）。
  const releasesQuery = usePolling(
    async () => {
      try {
        return await api.releases({ limit: 5 })
      } catch (caught) {
        throw new Error(errorMessage(caught, t('s.0625')))
      }
    },
    10_000,
    canViewReleases,
  )
  const recentRuns = useReleaseRuns(
    (releasesQuery.data ?? []).slice(0, 5).map((release) => release.id),
    canViewReleases,
    10_000,
  )
  const now = useNow()

  const [wizardOpen, setWizardOpen] = useState(false)
  const [localOpen, setLocalOpen] = useState(false)
  const [renaming, setRenaming] = useState<Server | null>(null)
  const [deleting, setDeleting] = useState<Server | null>(null)
  const [tagEditing, setTagEditing] = useState<Server | null>(null)
  const [tagSaving, setTagSaving] = useState(false)
  const [tagError, setTagError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [batch, setBatch] = useState<BatchState | null>(null)
  const [batchNames, setBatchNames] = useState<Record<string, string>>({})
  const [refreshing, setRefreshing] = useState(false)

  const servers = serversQuery.data ?? []
  const visibleServers = groupId
    ? servers.filter((server) => server.group_ids.includes(groupId))
    : servers
  const info = infoQuery.data
  const totalCount = info?.server_count ?? servers.length
  const onlineCount = info?.online_count ?? servers.filter((s) => s.status === 'online').length
  const offlineCount = Math.max(totalCount - onlineCount, 0)
  const onlineRate =
    totalCount > 0 ? `${Math.round((onlineCount / totalCount) * 100)}%` : t('s.0991')
  const hasFilters = Boolean(
    filters.group || filters.tag || filters.status || filters.mode || groupId,
  )

  const checkSummary = checksSummaryQuery.data
  const checkCounts = {
    total: checkSummary?.total ?? 0,
    up: checkSummary?.up ?? 0,
    down: checkSummary?.down ?? 0,
    unknown: checkSummary?.unknown ?? 0,
  }

  const alertSummary = alertsSummaryQuery.data
  const alertCounts = {
    firing: alertSummary?.firing ?? 0,
    acked: alertSummary?.acked ?? 0,
    resolved24h: alertSummary?.resolved_24h ?? 0,
    critical: alertSummary?.by_severity?.critical ?? 0,
    warning: alertSummary?.by_severity?.warning ?? 0,
    info: alertSummary?.by_severity?.info ?? 0,
  }

  const tagOptions = useMemo(() => {
    const tags = new Set<string>()
    for (const server of servers) for (const tag of server.tags) tags.add(tag)
    return [...tags].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }, [servers])

  const groupOptions = useMemo(
    () => (groupsQuery.data ?? []).map((group) => group.name),
    [groupsQuery.data],
  )

  const selectedServers = useMemo(
    () => servers.filter((server) => selected.has(server.id)),
    [servers, selected],
  )

  // 轮询后自动丢弃已不存在的选中项
  useEffect(() => {
    setSelected((previous) => {
      if (previous.size === 0) return previous
      const ids = new Set(servers.map((server) => server.id))
      const next = new Set([...previous].filter((id) => ids.has(id)))
      return next.size === previous.size ? previous : next
    })
  }, [servers])

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([
        serversQuery.refresh(),
        infoQuery.refresh(),
        groupsQuery.refresh(),
        checksSummaryQuery.refresh(),
        alertsSummaryQuery.refresh(),
        releasesQuery.refresh(),
      ])
    } finally {
      setRefreshing(false)
    }
  }

  const handleChanged = () => {
    void serversQuery.refresh()
    void infoQuery.refresh()
    void groupsQuery.refresh()
    void checksSummaryQuery.refresh()
    void alertsSummaryQuery.refresh()
    void releasesQuery.refresh()
  }

  const runBatch = async (title: string, action: BatchAction, params?: Record<string, unknown>) => {
    const targets = selectedServers
    if (targets.length === 0) return
    setBatchNames(Object.fromEntries(targets.map((server) => [server.id, server.name])))
    setBatch({ open: true, title, loading: true, error: null, response: null })
    try {
      const response = await api.batch({ ids: targets.map((server) => server.id), action, params })
      setBatch((current) => (current ? { ...current, loading: false, response } : current))
    } catch (caught) {
      setBatch((current) =>
        current
          ? { ...current, loading: false, error: errorMessage(caught, t('s.0992')) }
          : current,
      )
    } finally {
      if (action === 'delete') setSelected(new Set())
      handleChanged()
    }
  }

  const handleSaveTags = async (tags: string[]) => {
    if (!tagEditing) return
    setTagSaving(true)
    setTagError(null)
    try {
      await api.updateServer(tagEditing.id, { tags })
      toast(t('s.0993', { '0': tagEditing.name }), 'success')
      setTagEditing(null)
      handleChanged()
    } catch (caught) {
      setTagError(errorMessage(caught, t('s.0994')))
    } finally {
      setTagSaving(false)
    }
  }

  return (
    <div className="space-y-6">
      <NetworkFallbackBanner />

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('s.0995')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {t('s.0996')}
            {info ? t('s.0997', { '0': info.version }) : ''}
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
          {can('enroll.manage') ? (
            <Button
              variant="secondary"
              onClick={() => setLocalOpen(true)}
              icon={<ServerIcon className="h-4 w-4" />}
            >
              {t('localServer.entry')}
            </Button>
          ) : null}
          {can('server.write') ? (
          <Button onClick={() => setWizardOpen(true)} icon={<PlusIcon className="h-4 w-4" />}>
            {t('s.0029')}
          </Button>
          ) : null}
        </div>
      </div>

      {serversQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>
            {t('s.0998')}{serversQuery.error}
            {servers.length > 0 ? t('s.0999') : ''}
          </span>
          <Button size="sm" variant="secondary" onClick={refreshAll}>
            {t('s.0053')}
          </Button>
        </div>
      ) : null}

      {alertCounts.firing > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-400 bg-rose-50 px-4 py-3 text-sm text-rose-900 dark:border-rose-500/50 dark:bg-rose-500/15 dark:text-rose-100">
          <span className="inline-flex items-center gap-2 font-medium">
            <BellIcon className="h-4 w-4" />
            {t('s.1000')} {alertCounts.firing} {t('s.1001')}
            {alertCounts.critical > 0 ? t('s.1002', { '0': alertCounts.critical }) : ''}
          </span>
          <Link
            to="/alerts"
            className="inline-flex items-center gap-1 text-xs font-medium underline-offset-2 hover:underline"
          >
            {t('s.1003')}
            <ChevronRightIcon className="h-3.5 w-3.5" />
          </Link>
        </div>
      ) : null}

      {checkCounts.down > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-300 bg-rose-50 px-4 py-3 text-sm text-rose-800 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200">
          <span className="inline-flex items-center gap-2 font-medium">
            <AlertTriangleIcon className="h-4 w-4" />
            {t('s.1004')} {checkCounts.down} {t('s.1005')}
          </span>
          <Link
            to="/checks"
            className="inline-flex items-center gap-1 text-xs font-medium underline-offset-2 hover:underline"
          >
            {t('s.1006')}
            <ChevronRightIcon className="h-3.5 w-3.5" />
          </Link>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <StatCard
          label={t('s.1007')}
          value={totalCount}
          hint={info ? t('s.1008', { '0': formatUptime(info.uptime_s) }) : t('s.1009')}
          icon={<ServerIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
        <StatCard
          label={t('s.0521')}
          value={onlineCount}
          tone="success"
          hint={t('s.1010', { '0': onlineRate })}
          icon={<PulseIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
        <StatCard
          label={t('s.0522')}
          value={offlineCount}
          tone="danger"
          hint={t('s.1011')}
          icon={<OfflineIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
      </div>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300">
              <ShieldCheckIcon className="h-4.5 w-4.5" />
            </span>
            <div>
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1012')}</h2>
              <p className="text-xs text-slate-400 dark:text-slate-500">
                {t('s.1013')}
              </p>
            </div>
          </div>
          <Link
            to="/checks"
            className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
          >
            {t('s.1014')}
            <ChevronRightIcon className="h-3.5 w-3.5" />
          </Link>
        </div>

        <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
          {[
            {
              label: t('s.0943'),
              value: checkCounts.total,
              className: 'text-slate-900 dark:text-slate-100',
            },
            {
              label: t('s.0944'),
              value: checkCounts.up,
              className: 'text-emerald-600 dark:text-emerald-300',
            },
            {
              label: t('s.0945'),
              value: checkCounts.down,
              className: 'text-rose-600 dark:text-rose-300',
            },
            {
              label: t('s.0946'),
              value: checkCounts.unknown,
              className: 'text-slate-500 dark:text-slate-400',
            },
          ].map((item) => (
            <Link
              key={item.label}
              to="/checks"
              className="rounded-lg bg-slate-50 px-3 py-2.5 transition-colors hover:bg-slate-100 dark:bg-slate-950/60 dark:hover:bg-slate-900"
            >
              <span className="block text-[11px] text-slate-500 dark:text-slate-400">
                {item.label}
              </span>
              <span className={`mt-0.5 block text-lg font-semibold tabular-nums ${item.className}`}>
                {checksSummaryQuery.loading && checksSummaryQuery.data === null ? '—' : item.value}
              </span>
            </Link>
          ))}
        </div>
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-rose-50 text-rose-600 dark:bg-rose-500/10 dark:text-rose-300">
              <BellIcon className="h-4.5 w-4.5" />
            </span>
            <div>
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1015')}</h2>
              <p className="text-xs text-slate-400 dark:text-slate-500">
                {t('s.1016')}
              </p>
            </div>
          </div>
          <Link
            to="/alerts"
            className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
          >
            {t('s.1017')}
            <ChevronRightIcon className="h-3.5 w-3.5" />
          </Link>
        </div>

        <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
          {[
            {
              label: t('s.0698'),
              value: alertCounts.firing,
              className: 'text-rose-600 dark:text-rose-300',
            },
            {
              label: t('s.0699'),
              value: alertCounts.acked,
              className: 'text-blue-600 dark:text-blue-300',
            },
            {
              label: t('s.0700'),
              value: alertCounts.resolved24h,
              className: 'text-emerald-600 dark:text-emerald-300',
            },
            {
              label: t('s.1018'),
              value: `${alertCounts.critical}/${alertCounts.warning}/${alertCounts.info}`,
              className: 'text-slate-700 dark:text-slate-200',
            },
          ].map((item) => (
            <Link
              key={item.label}
              to="/alerts"
              className="rounded-lg bg-slate-50 px-3 py-2.5 transition-colors hover:bg-slate-100 dark:bg-slate-950/60 dark:hover:bg-slate-900"
            >
              <span className="block text-[11px] text-slate-500 dark:text-slate-400">
                {item.label}
              </span>
              <span className={`mt-0.5 block text-lg font-semibold tabular-nums ${item.className}`}>
                {item.value}
              </span>
            </Link>
          ))}
        </div>
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300">
              <FolderIcon className="h-4.5 w-4.5" />
            </span>
            <div>
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0815')}</h2>
              <p className="text-xs text-slate-400 dark:text-slate-500">
                {t('s.1019')}
              </p>
            </div>
          </div>
          {canViewReleases ? (
            <Link
              to="/apps"
              className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
            >
              {t('s.1020')}
              <ChevronRightIcon className="h-3.5 w-3.5" />
            </Link>
          ) : null}
        </div>

        <div className="mt-3 space-y-1.5">
          {!canViewReleases ? (
            <p className="rounded-lg bg-slate-50 px-3 py-3 text-xs text-slate-400 dark:bg-slate-950/60 dark:text-slate-500">
              {t('dashboard.recentReleasesNoPermission')}
            </p>
          ) : releasesQuery.error ? (
            // 三分支且 error 优先：请求失败时 data 还是 null，若继续按「空」渲染，界面会告诉用户
            // 「范围内暂无发布记录」——用户于是去查权限配置，而真实原因是他连不上控制端。
            // 这是本会话同一类问题的第三次（403 冒充范围过滤、LIMIT 冒充没有数据、error 冒充空），
            // 按同一范式在渲染层就把三者分开（同文件 :251 的 serversQuery.error 分支同款写法）。
            <div className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {t('dashboard.recentReleasesFailed')}：{releasesQuery.error}
            </div>
          ) : (releasesQuery.data ?? []).length === 0 ? (
            <p className="rounded-lg bg-slate-50 px-3 py-3 text-xs text-slate-400 dark:bg-slate-950/60 dark:text-slate-500">
              {/* 两种「空」要分开说：全局角色确实没有任何发布；范围受限角色只是自己范围内没有。 */}
              {scope.mode === 'all' ? t('s.1021') : t('dashboard.recentReleasesScopedEmpty')}
            </p>
          ) : (
            (releasesQuery.data ?? []).slice(0, 5).map((release) => {
              const runs = recentRuns[release.id] ?? release.runs
              const success = runs.filter((run) => run.status === 'success').length
              const failed = runs.filter((run) => run.status === 'failed').length
              const active = runs.filter(
                (run) => run.status === 'running' || run.status === 'pending',
              ).length
              return (
                <Link
                  key={release.id}
                  to={`/releases/${encodeURIComponent(release.id)}`}
                  className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg bg-slate-50 px-3 py-2 transition-colors hover:bg-slate-100 dark:bg-slate-950/60 dark:hover:bg-slate-900"
                >
                  <ReleaseStatusBadge status={release.status} />
                  <span className="text-sm text-slate-800 dark:text-slate-100">
                    {release.app_name ?? t('s.0461')}
                  </span>
                  <span className="font-mono text-xs text-slate-500 dark:text-slate-400">
                    v{release.version}
                  </span>
                  <span className="text-xs text-slate-500 dark:text-slate-400">
                    {t('s.0300')} {success} {t('s.1022')} {failed} {t('s.1023')} {active}
                  </span>
                  <span className="ml-auto text-[11px] text-slate-400 dark:text-slate-500">
                    {formatRelativeTime(release.created_at, now)}
                  </span>
                </Link>
              )
            })
          )}
        </div>
      </section>


      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0087')}</h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {visibleServers.length}
                {hasFilters ? t('s.1024') : ''}
              </span>
            </div>
            <ServerFiltersBar
              filters={filters}
              groups={groupsQuery.data ?? []}
              tags={tagOptions}
              onChange={setFilters}
              serverGroups={serverGroupsQuery.data ?? []}
              groupId={groupId}
              onGroupIdChange={setGroupId}
            />
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {serversQuery.updatedAt
              ? t('s.0949', { '0': formatClockTime(serversQuery.updatedAt) })
              : t('s.0425')}
          </p>
        </div>

        {can('server.write') ? (
        <BatchActionBar
          selected={selectedServers}
          pending={batch?.loading ?? false}
          groupOptions={groupOptions}
          onClear={() => setSelected(new Set())}
          onTest={() => void runBatch(t('s.0281'), 'test-connection')}
          onRefreshInfo={() => void runBatch(t('s.0282'), 'refresh-host-info')}
          onAddTags={(tags) => {
            if (tags.length === 0) {
              toast(t('s.1025'), 'error')
              return
            }
            void runBatch(t('s.0547'), 'add-tags', { tags })
          }}
          onSetGroup={(group) => void runBatch(t('s.1026'), 'set-group', { group })}
          onDelete={() => void runBatch(t('s.0513'), 'delete')}
        />
        ) : null}

        {serversQuery.loading && serversQuery.data === null ? (
          <InlineLoading label={t('s.1027')} />
        ) : visibleServers.length === 0 ? (
          hasFilters ? (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title={t('s.1028')}
              description={t('s.1029')}
              actionLabel={t('s.0525')}
              onAction={() => {
                setFilters({})
                setGroupId('')
              }}
            />
          ) : (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title={t('s.1030')}
              description={t('s.1031')}
              actionLabel={t('s.1032')}
              onAction={() => setWizardOpen(true)}
            />
          )
        ) : (
          <ServerTable
            canEdit={can('server.write')}
            canDelete={can('server.delete')}
            servers={visibleServers}
            now={now}
            selected={selected}
            onToggle={(id) =>
              setSelected((previous) => {
                const next = new Set(previous)
                if (next.has(id)) next.delete(id)
                else next.add(id)
                return next
              })
            }
            onToggleAll={(checked) =>
              setSelected(checked ? new Set(visibleServers.map((server) => server.id)) : new Set())
            }
            onEdit={(server) => setRenaming(server)}
            onDelete={(server) => setDeleting(server)}
            onEditTags={(server) => {
              setTagError(null)
              setTagEditing(server)
            }}
          />
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        {t('s.1033')}
      </p>

      <AddServerWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onFinished={handleChanged}
        onUseLocal={() => {
          setWizardOpen(false)
          setLocalOpen(true)
        }}
      />
      <LocalServerDialog
        open={localOpen}
        onClose={() => setLocalOpen(false)}
        onChanged={handleChanged}
      />
      <EditServerDialog
        server={renaming}
        onClose={() => setRenaming(null)}
        onSaved={handleChanged}
      />
      <DeleteServerDialog
        server={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={handleChanged}
      />
      <TagsDialog
        open={tagEditing !== null}
        title={t('s.0544')}
        description={tagEditing ? t('s.1034', { '0': tagEditing.name }) : undefined}
        initialTags={tagEditing?.tags ?? []}
        loading={tagSaving}
        error={tagError}
        onClose={() => {
          setTagEditing(null)
          setTagError(null)
        }}
        onSubmit={(tags) => void handleSaveTags(tags)}
      />
      <BatchResultDialog
        open={batch?.open ?? false}
        title={batch?.title ?? ''}
        loading={batch?.loading ?? false}
        error={batch?.error ?? null}
        response={batch?.response ?? null}
        nameOf={(id) => batchNames[id] ?? id}
        onClose={() => setBatch(null)}
      />
    </div>
  )
}
