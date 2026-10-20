import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, type Release } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useReleaseRuns } from '../lib/useReleaseRuns'
import { errorMessage } from '../lib/errors'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { AppDialog } from '../components/AppDialog'
import { Button } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ReleaseStatusBadge } from '../components/DeployBadges'
import { EmptyState } from '../components/EmptyState'
import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  FolderIcon,
  PencilIcon,
  PlayIcon,
  RefreshIcon,
  RotateCcwIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ReleaseWizard } from '../components/ReleaseWizard'
import { useToast } from '../components/Toast'

interface DeployedRow {
  serverId: string
  version: string
  releaseId: string
  status: string
  finishedAt: number | null
}

function aggregateRelease(release: Release) {
  const success = release.runs.filter((run) => run.status === 'success').length
  const failed = release.runs.filter((run) => run.status === 'failed').length
  const active = release.runs.filter(
    (run) => run.status === 'running' || run.status === 'pending',
  ).length
  return { success, failed, active, total: release.runs.length }
}

export default function AppDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { toast } = useToast()
  const navigate = useNavigate()

  const appQuery = usePolling(() => api.app(id ?? ''), 10_000, Boolean(id), id ?? '')
  const releasesQuery = usePolling(
    () => api.releases({ app_id: id ?? '', limit: 50 }),
    10_000,
    Boolean(id),
    id ?? '',
  )
  const serversQuery = usePolling(api.servers, 30_000)
  const templatesQuery = usePolling(api.templates, 10_000)
  const groupsQuery = usePolling(api.groups, 30_000)

  const [editOpen, setEditOpen] = useState(false)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [actionRelease, setActionRelease] = useState<Release | null>(null)
  const [actionKind, setActionKind] = useState<'retry' | 'rollback' | 'cancel' | null>(null)
  const [actionLoading, setActionLoading] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const app = appQuery.data
  const releases = releasesQuery.data ?? []
  const runsById = useReleaseRuns(useMemo(() => releases.slice(0, 20).map((release) => release.id), [releases]))
  const releasesWithRuns = useMemo(
    () => releases.map((release) => ({ ...release, runs: runsById[release.id] ?? release.runs })),
    [releases, runsById],
  )
  const servers = serversQuery.data ?? []
  const serverNameOf = useMemo(() => {
    const names = new Map(servers.map((server) => [server.id, server.name]))
    return (serverId: string) => names.get(serverId) ?? serverId
  }, [servers])

  const deployedRows = useMemo<DeployedRow[]>(() => {
    const map = new Map<string, DeployedRow>()
    const sorted = [...releasesWithRuns].sort((a, b) => (b.created_at ?? 0) - (a.created_at ?? 0))
    for (const release of sorted) {
      for (const run of release.runs) {
        if (run.status !== 'success') continue
        if (map.has(run.server_id)) continue
        map.set(run.server_id, {
          serverId: run.server_id,
          version: release.version,
          releaseId: release.id,
          status: run.status,
          finishedAt: run.finished_at ?? release.finished_at ?? release.created_at,
        })
      }
    }
    return [...map.values()]
  }, [releasesWithRuns])

  const handleAction = async () => {
    if (!actionRelease || !actionKind) return
    setActionLoading(true)
    setActionError(null)
    try {
      if (actionKind === 'retry') {
        await api.retryRelease(actionRelease.id)
        toast('已重试失败的服务器', 'success')
      } else if (actionKind === 'rollback') {
        await api.rollbackRelease(actionRelease.id)
        toast('已发起回滚', 'success')
      } else {
        await api.cancelRelease(actionRelease.id)
        toast('已取消未完成的 run', 'success')
      }
      setActionRelease(null)
      setActionKind(null)
      void releasesQuery.refresh()
    } catch (caught) {
      setActionError(errorMessage(caught, '操作失败'))
    } finally {
      setActionLoading(false)
    }
  }

  if (appQuery.loading && !app) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label="正在加载应用详情…" />
      </div>
    )
  }

  if (!app) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <p className="text-sm font-medium text-slate-900 dark:text-slate-100">应用不存在或已被删除</p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          {appQuery.error ?? '请返回应用列表检查。'}
        </p>
        <Link to="/apps" className="mt-4 inline-block">
          <Button variant="secondary" icon={<ArrowLeftIcon className="h-4 w-4" />}>
            返回应用列表
          </Button>
        </Link>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <Link
          to="/apps"
          className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500 transition-colors hover:text-indigo-600 dark:text-slate-400 dark:hover:text-indigo-400"
        >
          <ArrowLeftIcon className="h-3.5 w-3.5" />
          返回应用列表
        </Link>

        <div className="mt-3 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="truncate text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
                {app.name}
              </h1>
              <span className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                {app.slug}
              </span>
            </div>
            <p className="mt-1 font-mono text-xs text-slate-500 dark:text-slate-400">
              {app.root ?? '—'}
            </p>
            {app.description ? (
              <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{app.description}</p>
            ) : null}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => setEditOpen(true)}
              icon={<PencilIcon className="h-4 w-4" />}
            >
              编辑
            </Button>
            <Button
              variant="secondary"
              onClick={() => void releasesQuery.refresh()}
              loading={releasesQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              刷新
            </Button>
            <Button onClick={() => setWizardOpen(true)} icon={<PlayIcon className="h-4 w-4" />}>
              发起发布
            </Button>
          </div>
        </div>
      </div>

      {releasesQuery.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          加载发布历史失败：{releasesQuery.error}
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">发布历史</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {releases.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            回滚 = 把 current 指回上一个成功版本（秒级，不需重新下载）
          </p>
        </div>

        {releasesQuery.loading && releasesQuery.data === null ? (
          <InlineLoading label="正在加载发布记录…" />
        ) : releases.length === 0 ? (
          <EmptyState
            icon={<FolderIcon className="h-6 w-6" />}
            title="这个应用还没有发布记录"
            description="点击右上角「发起发布」，选择模板与目标服务器即可开始第一次发布"
            actionLabel="发起发布"
            onAction={() => setWizardOpen(true)}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1000px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">版本</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">状态</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">进度</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">创建时间</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">耗时</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {releasesWithRuns.map((release) => {
                  const stats = aggregateRelease(release)
                  const duration =
                    release.started_at && release.finished_at
                      ? Math.round((release.finished_at - release.started_at) / 1000)
                      : null
                  return (
                    <tr
                      key={release.id}
                      className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                    >
                      <td className="px-4 py-3 align-top">
                        <Link
                          to={`/releases/${encodeURIComponent(release.id)}`}
                          className="font-mono text-xs font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                        >
                          v{release.version}
                        </Link>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <ReleaseStatusBadge status={release.status} />
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                        <span className="text-emerald-600 dark:text-emerald-400">成功 {stats.success}</span>
                        {' · '}
                        <span className={stats.failed > 0 ? 'text-rose-600 dark:text-rose-400' : ''}>
                          失败 {stats.failed}
                        </span>
                        {' · '}
                        <span className="text-blue-600 dark:text-blue-400">进行中 {stats.active}</span>
                        <span className="text-slate-400 dark:text-slate-500"> / 共 {stats.total}</span>
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {formatDateTime(release.created_at)}
                        <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                          {formatRelativeTime(release.created_at)}
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                        {duration === null ? '—' : `${duration} 秒`}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex flex-wrap items-center justify-end gap-1">
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => navigate(`/releases/${release.id}`)}
                          >
                            详情与日志
                          </Button>
                          {stats.failed > 0 ? (
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => {
                                setActionError(null)
                                setActionRelease(release)
                                setActionKind('retry')
                              }}
                              icon={<RotateCcwIcon className="h-3.5 w-3.5" />}
                            >
                              重试失败
                            </Button>
                          ) : null}
                          {stats.success > 0 ? (
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => {
                                setActionError(null)
                                setActionRelease(release)
                                setActionKind('rollback')
                              }}
                            >
                              回滚
                            </Button>
                          ) : null}
                          {stats.active > 0 ? (
                            <Button
                              size="sm"
                              variant="danger"
                              onClick={() => {
                                setActionError(null)
                                setActionRelease(release)
                                setActionKind('cancel')
                              }}
                            >
                              取消
                            </Button>
                          ) : null}
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

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
            当前部署到哪些服务器
          </h2>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            按每台机器最近一次成功的 run 折算
          </p>
        </div>
        {deployedRows.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-slate-400 dark:text-slate-500">
            还没有任何服务器成功部署过这个应用
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">服务器</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">当前版本</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">部署时间</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">来源发布</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {deployedRows.map((row) => (
                  <tr key={row.serverId}>
                    <td className="px-4 py-3 align-top text-sm text-slate-800 dark:text-slate-100">
                      {serverNameOf(row.serverId)}
                    </td>
                    <td className="px-4 py-3 align-top font-mono text-xs text-slate-700 dark:text-slate-200">
                      v{row.version}
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(row.finishedAt)}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <Link
                        to={`/releases/${encodeURIComponent(row.releaseId)}`}
                        className="text-xs text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                      >
                        查看发布
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <AppDialog
        open={editOpen}
        app={app}
        onClose={() => setEditOpen(false)}
        onSaved={() => void appQuery.refresh()}
      />

      <ReleaseWizard
        open={wizardOpen}
        apps={[app]}
        templates={templatesQuery.data ?? []}
        servers={servers}
        groups={groupsQuery.data ?? []}
        defaultAppId={app.id}
        onClose={() => setWizardOpen(false)}
        onCreated={(release) => navigate(`/releases/${encodeURIComponent(release.id)}`)}
      />

      <ConfirmDialog
        open={actionKind !== null}
        title={
          actionKind === 'retry'
            ? '重试失败的服务器'
            : actionKind === 'rollback'
              ? '回滚这次发布'
              : '取消未完成的 run'
        }
        description={
          actionRelease ? `发布：v${actionRelease.version}` : undefined
        }
        confirmLabel={actionKind === 'cancel' ? '确认取消' : '确认执行'}
        tone={actionKind === 'cancel' ? 'danger' : 'primary'}
        loading={actionLoading}
        error={actionError}
        onClose={() => {
          setActionRelease(null)
          setActionKind(null)
          setActionError(null)
        }}
        onConfirm={() => void handleAction()}
      >
        {actionKind === 'retry' ? (
          <p>只会重跑状态为 failed 的服务器，成功的机器不会被重复执行。</p>
        ) : actionKind === 'rollback' ? (
          <p>
            将把各服务器的 current 指回上一个成功版本并重启服务。回滚是秒级的，不会重新下载制品。
          </p>
        ) : (
          <p>会终止仍在 pending / running 的 run，已经成功的机器不受影响。</p>
        )}
      </ConfirmDialog>

      <p className="flex items-center gap-1.5 text-xs text-slate-400 dark:text-slate-500">
        <AlertTriangleIcon className="h-3.5 w-3.5" />
        失败时控制端会按逆序执行回滚步骤；日志可在发布详情页实时查看。
      </p>
    </div>
  )
}
