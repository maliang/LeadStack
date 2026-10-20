import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, type AppItem, type DeployTemplate, type Release } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useReleaseRuns } from '../lib/useReleaseRuns'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { AppDialog } from '../components/AppDialog'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ReleaseStatusBadge, TemplateKindBadge } from '../components/DeployBadges'
import { EmptyState } from '../components/EmptyState'
import {
  ChevronRightIcon,
  FolderIcon,
  ListChecksIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshIcon,
  ServerIcon,
  TrashIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ReleaseWizard } from '../components/ReleaseWizard'
import { TemplateDialog } from '../components/TemplateDialog'
import { useToast } from '../components/Toast'

type Tab = 'apps' | 'templates'

export default function AppsPage() {
  const { toast } = useToast()
  const navigate = useNavigate()

  const [tab, setTab] = useState<Tab>('apps')
  const appsQuery = usePolling(api.apps, 10_000)
  const templatesQuery = usePolling(api.templates, 10_000)
  const releasesQuery = usePolling(() => api.releases({ limit: 100 }), 10_000)
  const serversQuery = usePolling(api.servers, 30_000)
  const groupsQuery = usePolling(api.groups, 30_000)

  const [appDialogOpen, setAppDialogOpen] = useState(false)
  const [editingApp, setEditingApp] = useState<AppItem | null>(null)
  const [deletingApp, setDeletingApp] = useState<AppItem | null>(null)
  const [deleteAppLoading, setDeleteAppLoading] = useState(false)
  const [deleteAppError, setDeleteAppError] = useState<string | null>(null)

  const [templateDialogOpen, setTemplateDialogOpen] = useState(false)
  const [editingTemplate, setEditingTemplate] = useState<DeployTemplate | null>(null)
  const [deletingTemplate, setDeletingTemplate] = useState<DeployTemplate | null>(null)
  const [deleteTemplateLoading, setDeleteTemplateLoading] = useState(false)
  const [deleteTemplateError, setDeleteTemplateError] = useState<string | null>(null)

  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardAppId, setWizardAppId] = useState<string | undefined>(undefined)
  const [refreshing, setRefreshing] = useState(false)

  const templates = templatesQuery.data ?? []
  const releases = releasesQuery.data ?? []
  const servers = serversQuery.data ?? []
  const apps = appsQuery.data ?? []
  const latestReleaseIds = useMemo(() => {
    const sorted = [...releases].sort((a, b) => (b.created_at ?? 0) - (a.created_at ?? 0))
    const seen = new Set<string>()
    const ids: string[] = []
    for (const release of sorted) {
      if (seen.has(release.app_id)) continue
      seen.add(release.app_id)
      ids.push(release.id)
    }
    return ids
  }, [releases])
  const runsById = useReleaseRuns(latestReleaseIds)
  /** 按应用聚合：最近一次发布、发布次数、涉及服务器数 */
  const appStats = useMemo(() => {
    const map = new Map<
      string,
      { last: Release | null; count: number; serverIds: Set<string> }
    >()
    for (const app of apps) map.set(app.id, { last: null, count: 0, serverIds: new Set() })
    const sorted = [...releases].sort((a, b) => (b.created_at ?? 0) - (a.created_at ?? 0))
    for (const release of sorted) {
      const stat = map.get(release.app_id)
      if (!stat) continue
      if (!stat.last) stat.last = release
      stat.count += 1
      for (const run of release.runs) stat.serverIds.add(run.server_id)
      for (const run of runsById[release.id] ?? []) stat.serverIds.add(run.server_id)
    }
    return map
  }, [apps, releases, runsById])

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([appsQuery.refresh(), templatesQuery.refresh(), releasesQuery.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  const handleDeleteApp = async () => {
    if (!deletingApp) return
    setDeleteAppLoading(true)
    setDeleteAppError(null)
    try {
      await api.deleteApp(deletingApp.id)
      toast(`已删除应用「${deletingApp.name}」`, 'success')
      setDeletingApp(null)
      await refreshAll()
    } catch (caught) {
      setDeleteAppError(errorMessage(caught, '删除应用失败'))
    } finally {
      setDeleteAppLoading(false)
    }
  }

  const handleDeleteTemplate = async () => {
    if (!deletingTemplate) return
    setDeleteTemplateLoading(true)
    setDeleteTemplateError(null)
    try {
      await api.deleteTemplate(deletingTemplate.id)
      toast(`已删除模板「${deletingTemplate.name}」`, 'success')
      setDeletingTemplate(null)
      await refreshAll()
    } catch (caught) {
      setDeleteTemplateError(errorMessage(caught, '删除模板失败'))
    } finally {
      setDeleteTemplateLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            应用与发布
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            应用 = 被部署的业务单元；模板定义怎么部署；每次发布都会冻结模板快照，每 10 秒自动刷新
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            onClick={refreshAll}
            loading={refreshing}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            刷新
          </Button>
          {tab === 'apps' ? (
            <>
              <Button
                variant="secondary"
                icon={<PlusIcon className="h-4 w-4" />}
                onClick={() => {
                  setEditingApp(null)
                  setAppDialogOpen(true)
                }}
              >
                新建应用
              </Button>
              <Button
                icon={<PlayIcon className="h-4 w-4" />}
                onClick={() => {
                  setWizardAppId(undefined)
                  setWizardOpen(true)
                }}
              >
                发起发布
              </Button>
            </>
          ) : (
            <Button
              icon={<PlusIcon className="h-4 w-4" />}
              onClick={() => {
                setEditingTemplate(null)
                setTemplateDialogOpen(true)
              }}
            >
              新建模板
            </Button>
          )}
        </div>
      </div>

      <div className="flex items-center gap-1 border-b border-slate-200 dark:border-slate-800">
        {(
          [
            { key: 'apps' as const, label: '应用', count: apps.length },
            { key: 'templates' as const, label: '模板', count: templates.length },
          ]
        ).map((option) => (
          <button
            key={option.key}
            type="button"
            onClick={() => setTab(option.key)}
            className={cn(
              '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors',
              tab === option.key
                ? 'border-indigo-600 text-indigo-700 dark:border-indigo-400 dark:text-indigo-300'
                : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
            )}
          >
            {option.label}
            <span className="rounded-full bg-slate-100 px-1.5 py-0.5 text-[11px] tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {option.count}
            </span>
          </button>
        ))}
      </div>

      {tab === 'apps' ? (
        <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">应用列表</h2>
            {appsQuery.error ? (
              <span className="text-xs text-rose-600 dark:text-rose-400">
                加载失败：{appsQuery.error}
              </span>
            ) : (
              <p className="text-xs text-slate-400 dark:text-slate-500">
                部署目录：&lt;root&gt;/releases/&lt;release_id&gt;，current 指向当前版本
              </p>
            )}
          </div>

          {appsQuery.loading && appsQuery.data === null ? (
            <InlineLoading label="正在加载应用…" />
          ) : apps.length === 0 ? (
            <EmptyState
              icon={<FolderIcon className="h-6 w-6" />}
              title="还没有应用"
              description="先创建一个应用（例如 order-service），再配合模板发布到服务器"
              actionLabel="新建应用"
              onAction={() => {
                setEditingApp(null)
                setAppDialogOpen(true)
              }}
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[1000px] border-collapse text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                    <th scope="col" className="px-4 py-2.5 font-medium">应用</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">部署根目录</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">最近发布</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">发布次数</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">服务器数</th>
                    <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                  {apps.map((app) => {
                    const stat = appStats.get(app.id)
                    const last = stat?.last ?? app.last_release
                    const serverCount = stat?.serverIds.size ?? app.server_count
                    return (
                      <tr
                        key={app.id}
                        className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                      >
                        <td className="max-w-[220px] px-4 py-3 align-top">
                          <Link
                            to={`/apps/${encodeURIComponent(app.id)}`}
                            className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                          >
                            {app.name}
                          </Link>
                          <span className="mt-0.5 block font-mono text-[11px] text-slate-400 dark:text-slate-500">
                            {app.slug}
                          </span>
                        </td>
                        <td className="max-w-[220px] px-4 py-3 align-top">
                          <span
                            className="block truncate font-mono text-[11px] text-slate-600 dark:text-slate-300"
                            title={app.root ?? undefined}
                          >
                            {app.root ?? '—'}
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top">
                          {last ? (
                            <span className="block">
                              <span className="flex items-center gap-2">
                                <ReleaseStatusBadge status={last.status} />
                                <span className="font-mono text-xs text-slate-600 dark:text-slate-300">
                                  v{last.version}
                                </span>
                              </span>
                              <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                                {formatRelativeTime(last.created_at)}
                              </span>
                            </span>
                          ) : (
                            <span className="text-xs text-slate-400 dark:text-slate-500">尚未发布</span>
                          )}
                        </td>
                        <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                          {stat?.count ?? app.release_count}
                        </td>
                        <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                          <span className="inline-flex items-center gap-1">
                            <ServerIcon className="h-3.5 w-3.5 text-slate-400" />
                            {serverCount}
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top">
                          <div className="flex items-center justify-end gap-0.5">
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => {
                                setWizardAppId(app.id)
                                setWizardOpen(true)
                              }}
                              icon={<PlayIcon className="h-3.5 w-3.5" />}
                            >
                              发布
                            </Button>
                            <IconButton label="应用详情" onClick={() => navigate(`/apps/${app.id}`)}>
                              <ChevronRightIcon className="h-4 w-4" />
                            </IconButton>
                            <IconButton
                              label="编辑应用"
                              onClick={() => {
                                setEditingApp(app)
                                setAppDialogOpen(true)
                              }}
                            >
                              <PencilIcon className="h-4 w-4" />
                            </IconButton>
                            <IconButton
                              label="删除应用"
                              tone="danger"
                              onClick={() => {
                                setDeleteAppError(null)
                                setDeletingApp(app)
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
      ) : (
        <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">部署模板</h2>
            <p className="text-xs text-slate-400 dark:text-slate-500">
              支持 compose / systemd / binary；模板是版本化文本，发布时冻结快照
            </p>
          </div>

          {templatesQuery.loading && templatesQuery.data === null ? (
            <InlineLoading label="正在加载模板…" />
          ) : templates.length === 0 ? (
            <EmptyState
              icon={<ListChecksIcon className="h-6 w-6" />}
              title="还没有部署模板"
              description="新建模板，或用「填充示例模板」从三种类型的示例开始改"
              actionLabel="新建模板"
              onAction={() => {
                setEditingTemplate(null)
                setTemplateDialogOpen(true)
              }}
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[880px] border-collapse text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                    <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">类型</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">内容</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">更新时间</th>
                    <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                  {templates.map((template) => (
                    <tr
                      key={template.id}
                      className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                    >
                      <td className="max-w-[240px] px-4 py-3 align-top">
                        <button
                          type="button"
                          onClick={() => {
                            setEditingTemplate(template)
                            setTemplateDialogOpen(true)
                          }}
                          className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                        >
                          {template.name}
                        </button>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <TemplateKindBadge kind={template.kind} />
                      </td>
                      <td className="max-w-[320px] px-4 py-3 align-top">
                        <span className="block truncate font-mono text-[11px] text-slate-500 dark:text-slate-400">
                          {template.content.split('\n').find((line) => line.trim() !== '') ?? '（空）'}
                        </span>
                        <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                          {template.content.split('\n').length} 行 · {template.content.length} 字符
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {formatDateTime(template.updated_at ?? template.created_at)}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex items-center justify-end gap-0.5">
                          <IconButton
                            label="编辑模板"
                            onClick={() => {
                              setEditingTemplate(template)
                              setTemplateDialogOpen(true)
                            }}
                          >
                            <PencilIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label="删除模板"
                            tone="danger"
                            onClick={() => {
                              setDeleteTemplateError(null)
                              setDeletingTemplate(template)
                            }}
                          >
                            <TrashIcon className="h-4 w-4" />
                          </IconButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      <AppDialog
        open={appDialogOpen}
        app={editingApp}
        onClose={() => {
          setAppDialogOpen(false)
          setEditingApp(null)
        }}
        onSaved={() => void refreshAll()}
      />

      <TemplateDialog
        open={templateDialogOpen}
        template={editingTemplate}
        onClose={() => {
          setTemplateDialogOpen(false)
          setEditingTemplate(null)
        }}
        onSaved={() => void refreshAll()}
      />

      <ReleaseWizard
        open={wizardOpen}
        apps={apps}
        templates={templates}
        servers={servers}
        groups={groupsQuery.data ?? []}
        defaultAppId={wizardAppId}
        onClose={() => setWizardOpen(false)}
        onCreated={(release) => {
          void refreshAll()
          navigate(`/releases/${encodeURIComponent(release.id)}`)
        }}
      />

      <ConfirmDialog
        open={deletingApp !== null}
        title="删除应用"
        description={deletingApp ? `应用：${deletingApp.name}（${deletingApp.slug}）` : undefined}
        confirmLabel="确认删除"
        tone="danger"
        loading={deleteAppLoading}
        error={deleteAppError}
        onClose={() => {
          setDeletingApp(null)
          setDeleteAppError(null)
        }}
        onConfirm={() => void handleDeleteApp()}
      >
        <p>
          确定要删除应用
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deletingApp?.name}」
          </span>
          吗？若该应用已有发布记录，控制端会拒绝删除（409）。
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={deletingTemplate !== null}
        title="删除模板"
        description={deletingTemplate ? `模板：${deletingTemplate.name}` : undefined}
        confirmLabel="确认删除"
        tone="danger"
        loading={deleteTemplateLoading}
        error={deleteTemplateError}
        onClose={() => {
          setDeletingTemplate(null)
          setDeleteTemplateError(null)
        }}
        onConfirm={() => void handleDeleteTemplate()}
      >
        <p>
          确定要删除模板
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deletingTemplate?.name}」
          </span>
          吗？已发布的历史使用的是快照，不受影响。
        </p>
      </ConfirmDialog>
    </div>
  )
}
