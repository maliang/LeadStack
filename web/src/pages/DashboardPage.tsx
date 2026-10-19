import { useState } from 'react'
import { api, type Server } from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { formatClockTime, formatUptime } from '../lib/format'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { EnrollWizard } from '../components/EnrollWizard'
import {
  OfflineIcon,
  PlusIcon,
  PulseIcon,
  RefreshIcon,
  ServerIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { DeleteServerDialog, RenameServerDialog } from '../components/ServerDialogs'
import { ServerTable } from '../components/ServerTable'
import { StatCard } from '../components/StatCard'

export default function DashboardPage() {
  const serversQuery = usePolling(api.servers, 10_000)
  const infoQuery = usePolling(api.systemInfo, 10_000)
  const now = useNow()

  const [wizardOpen, setWizardOpen] = useState(false)
  const [renaming, setRenaming] = useState<Server | null>(null)
  const [deleting, setDeleting] = useState<Server | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const servers = serversQuery.data ?? []
  const info = infoQuery.data
  const onlineCount = servers.filter((server) => server.status === 'online').length
  const offlineCount = servers.filter((server) => server.status === 'offline').length
  const unknownCount = servers.filter((server) => server.status === 'unknown').length
  const totalCount = info?.server_count ?? servers.length
  const onlineRate =
    totalCount > 0 ? `${Math.round((onlineCount / totalCount) * 100)}%` : '暂无服务器'

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([serversQuery.refresh(), infoQuery.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  const handleChanged = () => {
    void serversQuery.refresh()
    void infoQuery.refresh()
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
            接入服务器
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
          hint={unknownCount > 0 ? `另有 ${unknownCount} 台状态未知（从未接入）` : '所有服务器均已接入'}
          icon={<OfflineIcon className="h-5 w-5" />}
          loading={serversQuery.loading && serversQuery.data === null}
        />
      </div>

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">服务器</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {servers.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {serversQuery.updatedAt
              ? `更新于 ${formatClockTime(serversQuery.updatedAt)}`
              : '加载中…'}
          </p>
        </div>

        {serversQuery.loading && serversQuery.data === null ? (
          <InlineLoading label="正在加载服务器列表…" />
        ) : servers.length === 0 ? (
          <EmptyState
            icon={<ServerIcon className="h-6 w-6" />}
            title="还没有服务器"
            description="还没有服务器，点击右上角接入第一台机器"
            actionLabel="接入第一台机器"
            onAction={() => setWizardOpen(true)}
          />
        ) : (
          <ServerTable
            servers={servers}
            now={now}
            onRename={(server) => setRenaming(server)}
            onDelete={(server) => setDeleting(server)}
          />
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        状态判定：最近 45 秒内有心跳为在线，超过 45 秒为离线，从未接入为未知。
      </p>

      <EnrollWizard
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
    </div>
  )
}
