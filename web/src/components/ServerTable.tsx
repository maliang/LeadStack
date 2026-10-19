import { Link } from 'react-router-dom'
import type { Server } from '../lib/api'
import { cn } from '../lib/cn'
import {
  formatDateTime,
  formatLoad,
  formatOs,
  formatPercent,
  formatRelativeTime,
  serverAddress,
  usageTone,
} from '../lib/format'
import { StatusBadge } from './StatusBadge'
import { IconButton } from './Button'
import { PencilIcon, TrashIcon } from './Icons'

interface ServerTableProps {
  servers: Server[]
  now: number
  onRename: (server: Server) => void
  onDelete: (server: Server) => void
}

function UsageBar({ value }: { value: number | null }) {
  const tone = usageTone(value)
  const barColor =
    tone === 'danger' ? 'bg-rose-500' : tone === 'warn' ? 'bg-amber-500' : 'bg-emerald-500'
  const textColor =
    tone === 'danger'
      ? 'text-rose-600 dark:text-rose-400'
      : tone === 'warn'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-slate-700 dark:text-slate-200'

  return (
    <div className="min-w-[72px]">
      <span className={cn('text-xs font-medium tabular-nums', textColor)}>{formatPercent(value)}</span>
      {value !== null && value !== undefined ? (
        <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
          <div
            className={cn('h-1 rounded-full', barColor)}
            style={{ width: `${Math.max(0, Math.min(100, value))}%` }}
          />
        </div>
      ) : null}
    </div>
  )
}

export function ServerTable({ servers, now, onRename, onDelete }: ServerTableProps) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[980px] border-collapse text-sm">
        <thead>
          <tr className="border-b border-slate-200 text-left text-xs font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400">
            <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
            <th scope="col" className="px-4 py-2.5 font-medium">主机</th>
            <th scope="col" className="px-4 py-2.5 font-medium">状态</th>
            <th scope="col" className="px-4 py-2.5 font-medium">系统</th>
            <th scope="col" className="px-4 py-2.5 font-medium">CPU</th>
            <th scope="col" className="px-4 py-2.5 font-medium">内存</th>
            <th scope="col" className="px-4 py-2.5 font-medium">负载</th>
            <th scope="col" className="px-4 py-2.5 font-medium">最后心跳</th>
            <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
          {servers.map((server) => {
            const metrics = server.metrics_summary
            return (
              <tr
                key={server.id}
                className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
              >
                <td className="max-w-[220px] px-4 py-3">
                  <Link
                    to={`/servers/${encodeURIComponent(server.id)}`}
                    className="font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                  >
                    {server.name}
                  </Link>
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    {server.tags.slice(0, 2).map((tag) => (
                      <span
                        key={tag}
                        className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500 dark:bg-slate-800 dark:text-slate-400"
                      >
                        {tag}
                      </span>
                    ))}
                    {server.agent_version ? (
                      <span className="text-[11px] text-slate-400 dark:text-slate-500">
                        agent {server.agent_version}
                      </span>
                    ) : null}
                  </div>
                </td>
                <td className="px-4 py-3 font-mono text-xs text-slate-600 dark:text-slate-300">
                  {serverAddress(server)}
                </td>
                <td className="px-4 py-3">
                  <StatusBadge status={server.status} />
                </td>
                <td className="px-4 py-3">
                  <span
                    className="text-xs text-slate-600 dark:text-slate-300"
                    title={server.kernel ? `内核 ${server.kernel}` : undefined}
                  >
                    {formatOs(server.os, server.arch)}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <UsageBar value={metrics?.cpu_pct ?? null} />
                </td>
                <td className="px-4 py-3">
                  <UsageBar value={metrics?.mem_used_pct ?? null} />
                </td>
                <td className="px-4 py-3 tabular-nums text-xs text-slate-600 dark:text-slate-300">
                  {formatLoad(metrics?.load1)}
                </td>
                <td className="px-4 py-3">
                  <span
                    className="text-xs text-slate-500 dark:text-slate-400"
                    title={server.last_seen_at ? formatDateTime(server.last_seen_at) : '从未上报'}
                  >
                    {formatRelativeTime(server.last_seen_at, now)}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-0.5">
                    <IconButton label="重命名" onClick={() => onRename(server)}>
                      <PencilIcon className="h-4 w-4" />
                    </IconButton>
                    <IconButton label="删除服务器" tone="danger" onClick={() => onDelete(server)}>
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
  )
}
