import { useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'
import type { Server } from '../lib/api'
import { cn } from '../lib/cn'
import {
  formatBytes,
  formatDateTime,
  formatLoad,
  formatOs,
  formatPercent,
  formatRelativeTime,
  serverAddress,
  usageTone,
} from '../lib/format'
import { ModeBadge, StatusBadge } from './StatusBadge'
import { IconButton } from './Button'
import { AlertTriangleIcon, FolderIcon, PencilIcon, TagIcon, TrashIcon } from './Icons'

import { t } from '../i18n'
interface ServerTableProps {
  servers: Server[]
  now: number
  selected: Set<string>
  onToggle: (id: string) => void
  onToggleAll: (checked: boolean) => void
  onRename: (server: Server) => void
  onDelete: (server: Server) => void
  onEditTags: (server: Server) => void
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

/** 主机信息摘要：核数 · 内存 · 磁盘使用率（未采集时给引导文案）。 */
function HostSummary({ server }: { server: Server }) {
  const info = server.host_info
  if (!info) {
    return <span className="text-xs text-slate-400 dark:text-slate-500">{t('s.0526')}</span>
  }
  const diskPct =
    info.disk_total_bytes && info.disk_used_bytes
      ? `${((info.disk_used_bytes / info.disk_total_bytes) * 100).toFixed(0)}%`
      : null
  const text = [
    info.cpu_cores ? t('s.0527', { '0': info.cpu_cores }) : null,
    info.mem_total_bytes ? formatBytes(info.mem_total_bytes, 0) : null,
    diskPct ? t('s.0528', { '0': diskPct }) : null,
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <span
      className="text-xs text-slate-600 dark:text-slate-300"
      title={[info.cpu_model, info.os_version, info.timezone].filter(Boolean).join(' · ')}
    >
      {text || t('s.0529')}
    </span>
  )
}

export function ServerTable({
  servers,
  now,
  selected,
  onToggle,
  onToggleAll,
  onRename,
  onDelete,
  onEditTags,
}: ServerTableProps) {
  const headerCheckboxRef = useRef<HTMLInputElement | null>(null)
  const allSelected = servers.length > 0 && servers.every((server) => selected.has(server.id))
  const someSelected = servers.some((server) => selected.has(server.id))

  useEffect(() => {
    if (headerCheckboxRef.current) {
      headerCheckboxRef.current.indeterminate = someSelected && !allSelected
    }
  }, [someSelected, allSelected])

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[1240px] border-collapse text-sm">
        <thead>
          <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
            <th scope="col" className="w-10 px-3 py-2.5">
              <input
                ref={headerCheckboxRef}
                type="checkbox"
                aria-label={t('s.0530')}
                checked={allSelected}
                onChange={(event) => onToggleAll(event.target.checked)}
                className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
              />
            </th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0531')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0060')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0532')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0533')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0062')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0534')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0535')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">CPU</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0536')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0537')}</th>
            <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0538')}</th>
            <th scope="col" className="px-4 py-2.5 text-right font-medium">{t('s.0539')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
          {servers.map((server) => {
            const metrics = server.metrics_summary
            const info = server.host_info
            return (
              <tr
                key={server.id}
                className={cn(
                  'transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60',
                  selected.has(server.id) && 'bg-indigo-50/40 dark:bg-indigo-500/5',
                )}
              >
                <td className="px-3 py-3 align-top">
                  <input
                    type="checkbox"
                    aria-label={t('s.0540', { '0': server.name })}
                    checked={selected.has(server.id)}
                    onChange={() => onToggle(server.id)}
                    className="mt-0.5 h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                  />
                </td>
                <td className="max-w-[220px] px-4 py-3 align-top">
                  <div className="flex items-center gap-1.5">
                    <Link
                      to={`/servers/${encodeURIComponent(server.id)}`}
                      className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                    >
                      {server.name}
                    </Link>
                    {server.last_error ? (
                      <span
                        className="shrink-0 text-amber-500 dark:text-amber-400"
                        title={t('s.0541', { '0': server.last_error })}
                      >
                        <AlertTriangleIcon className="h-3.5 w-3.5" />
                      </span>
                    ) : null}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    {server.tags.slice(0, 3).map((tag) => (
                      <span
                        key={tag}
                        className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500 dark:bg-slate-800 dark:text-slate-400"
                      >
                        {tag}
                      </span>
                    ))}
                    {server.mode === 'agent' && server.agent_version ? (
                      <span className="text-[11px] text-slate-400 dark:text-slate-500">
                        agent {server.agent_version}
                      </span>
                    ) : null}
                  </div>
                </td>
                <td className="px-4 py-3 align-top font-mono text-xs text-slate-600 dark:text-slate-300">
                  {serverAddress(server)}
                </td>
                <td className="px-4 py-3 align-top">
                  <ModeBadge mode={server.mode} />
                </td>
                <td className="px-4 py-3 align-top">
                  <StatusBadge status={server.status} />
                </td>
                <td className="px-4 py-3 align-top">
                  {server.group ? (
                    <span className="inline-flex max-w-[140px] items-center gap-1 text-xs text-slate-600 dark:text-slate-300">
                      <FolderIcon className="h-3.5 w-3.5 shrink-0 text-slate-400" />
                      <span className="truncate">{server.group}</span>
                    </span>
                  ) : (
                    <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                  )}
                </td>
                <td className="px-4 py-3 align-top">
                  <span
                    className="text-xs text-slate-600 dark:text-slate-300"
                    title={server.kernel ? t('s.0542', { '0': server.kernel }) : undefined}
                  >
                    {formatOs(server.os ?? info?.os ?? null, server.arch ?? info?.arch ?? null)}
                  </span>
                </td>
                <td className="px-4 py-3 align-top">
                  <HostSummary server={server} />
                </td>
                <td className="px-4 py-3 align-top">
                  <UsageBar value={metrics?.cpu_pct ?? null} />
                </td>
                <td className="px-4 py-3 align-top">
                  <UsageBar value={metrics?.mem_used_pct ?? null} />
                </td>
                <td className="px-4 py-3 align-top tabular-nums text-xs text-slate-600 dark:text-slate-300">
                  {formatLoad(metrics?.load1)}
                </td>
                <td className="px-4 py-3 align-top">
                  <span
                    className="text-xs text-slate-500 dark:text-slate-400"
                    title={server.last_seen_at ? formatDateTime(server.last_seen_at) : t('s.0543')}
                  >
                    {formatRelativeTime(server.last_seen_at, now)}
                  </span>
                </td>
                <td className="px-4 py-3 align-top">
                  <div className="flex items-center justify-end gap-0.5">
                    <IconButton label={t('s.0544')} onClick={() => onEditTags(server)}>
                      <TagIcon className="h-4 w-4" />
                    </IconButton>
                    <IconButton label={t('s.0545')} onClick={() => onRename(server)}>
                      <PencilIcon className="h-4 w-4" />
                    </IconButton>
                    <IconButton label={t('s.0513')} tone="danger" onClick={() => onDelete(server)}>
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
