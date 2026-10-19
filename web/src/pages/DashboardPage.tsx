import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type BatchAction,
  type BatchResponse,
  type Server,
  type ServerFilters,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { formatClockTime, formatUptime } from '../lib/format'
import { AddServerWizard } from '../components/AddServerWizard'
import { BatchActionBar } from '../components/BatchActionBar'
import { BatchResultDialog } from '../components/BatchResultDialog'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { OfflineIcon, PlusIcon, PulseIcon, RefreshIcon, ServerIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ServerFiltersBar } from '../components/ServerFiltersBar'
import { DeleteServerDialog, RenameServerDialog } from '../components/ServerDialogs'
import { ServerTable } from '../components/ServerTable'
import { StatCard } from '../components/StatCard'
import { TagsDialog } from '../components/TagsDialog'
import { useToast } from '../components/Toast'

interface BatchState {
  open: boolean
  title: string
  loading: boolean
  error: string | null
  response: BatchResponse | null
}

export default function DashboardPage() {
  const { toast } = useToast()
  const [filters, setFilters] = useState<ServerFilters>({})
  const filterKey = JSON.stringify(filters)

  const serversQuery = usePolling(() => api.servers(filters), 10_000, true, filterKey)
  const infoQuery = usePolling(api.systemInfo, 10_000)
  const groupsQuery = usePolling(api.groups, 10_000)
  const now = useNow()

  const [wizardOpen, setWizardOpen] = useState(false)
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
  const info = infoQuery.data
  const totalCount = info?.server_count ?? servers.length
  const onlineCount = info?.online_count ?? servers.filter((s) => s.status === 'online').length
  const offlineCount = Math.max(totalCount - onlineCount, 0)
  const onlineRate =
    totalCount > 0 ? `${Math.round((onlineCount / totalCount) * 100)}%` : '暂无服务器'
  const hasFilters = Boolean(filters.group || filters.tag || filters.status || filters.mode)

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
      await Promise.all([serversQuery.refresh(), infoQuery.refresh(), groupsQuery.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  const handleChanged = () => {
    void serversQuery.refresh()
    void infoQuery.refresh()
    void groupsQuery.refresh()
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
          ? { ...current, loading: false, error: errorMessage(caught, '批量操作失败') }
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
      toast(`「${tagEditing.name}」的标签已更新`, 'success')
      setTagEditing(null)
      handleChanged()
    } catch (caught) {
      setTagError(errorMessage(caught, '标签保存失败'))
    } finally {
      setTagSaving(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            总览
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            所有服务器的实时状态，每 10 秒自动刷新
            {info ? ` · 控制端 v${info.version}` : ''}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={refreshAll}
            loading={refreshing}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            刷新
          </Button>
          <Button onClick={() => setWizardOpen(true)} icon={<PlusIcon className="h-4 w-4" />}>
            添加服务器
          </Button>
        </div>
      </div>

      {serversQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>
            加载服务器列表失败：{serversQuery.error}
            {servers.length > 0 ? '（下方为上次成功获取的数据）' : ''}
          </span>
          <Button size="sm" variant="secondary" onClick={refreshAll}>
            重试
          </Button>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <StatCard
          label="服务器总数"
          value={totalCount}
          hint={info ? `控制端已运行 ${formatUptime(info.uptime_s)}` : '控制端信息加载中'}
          icon={<ServerIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
        <StatCard
          label="在线"
          value={onlineCount}
          tone="success"
          hint={`在线率 ${onlineRate}`}
          icon={<PulseIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
        <StatCard
          label="离线"
          value={offlineCount}
          tone="danger"
          hint="含 SSH 连续采集失败与心跳超时"
          icon={<OfflineIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
      </div>

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">服务器</h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {servers.length}
                {hasFilters ? ' / 已筛选' : ''}
              </span>
            </div>
            <ServerFiltersBar
              filters={filters}
              groups={groupsQuery.data ?? []}
              tags={tagOptions}
              onChange={setFilters}
            />
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {serversQuery.updatedAt
              ? `更新于 ${formatClockTime(serversQuery.updatedAt)}`
              : '加载中…'}
          </p>
        </div>

        <BatchActionBar
          selected={selectedServers}
          pending={batch?.loading ?? false}
          groupOptions={groupOptions}
          onClear={() => setSelected(new Set())}
          onTest={() => void runBatch('测试连通性', 'test-connection')}
          onRefreshInfo={() => void runBatch('刷新主机信息', 'refresh-host-info')}
          onAddTags={(tags) => {
            if (tags.length === 0) {
              toast('请先输入至少一个标签', 'error')
              return
            }
            void runBatch('添加标签', 'add-tags', { tags })
          }}
          onSetGroup={(group) => void runBatch('设置分组', 'set-group', { group })}
          onDelete={() => void runBatch('删除服务器', 'delete')}
        />

        {serversQuery.loading && serversQuery.data === null ? (
          <InlineLoading label="正在加载服务器列表…" />
        ) : servers.length === 0 ? (
          hasFilters ? (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title="没有匹配的服务器"
              description="当前筛选条件下没有服务器，试试清除筛选或更换条件"
              actionLabel="清除筛选"
              onAction={() => setFilters({})}
            />
          ) : (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title="还没有服务器"
              description="还没有服务器，点击右上角添加第一台机器"
              actionLabel="添加第一台服务器"
              onAction={() => setWizardOpen(true)}
            />
          )
        ) : (
          <ServerTable
            servers={servers}
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
              setSelected(checked ? new Set(servers.map((server) => server.id)) : new Set())
            }
            onRename={(server) => setRenaming(server)}
            onDelete={(server) => setDeleting(server)}
            onEditTags={(server) => {
              setTagError(null)
              setTagEditing(server)
            }}
          />
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        状态判定：agent 心跳 45 秒超时判离线；SSH 模式连续 3 次采集失败判离线。鼠标悬停名称后的警示图标可查看最后错误。
      </p>

      <AddServerWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onFinished={handleChanged}
      />
      <RenameServerDialog
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
        title="编辑标签"
        description={tagEditing ? `服务器：${tagEditing.name}` : undefined}
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
