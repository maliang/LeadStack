import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import {
  formatDateTime,
  formatLoad,
  formatPercent,
  formatRelativeTime,
  formatUptime,
  serverAddress,
  usageTone,
} from '../lib/format'
import { Button } from '../components/Button'
import { ArrowLeftIcon, ClockIcon, RefreshIcon, TerminalIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { StatusBadge } from '../components/StatusBadge'
import { cn } from '../lib/cn'

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-slate-100 py-2.5 last:border-b-0 dark:border-slate-800/70">
      <dt className="shrink-0 text-xs text-slate-500 dark:text-slate-400">{label}</dt>
      <dd className="min-w-0 break-all text-right text-xs font-medium text-slate-700 dark:text-slate-200">
        {children}
      </dd>
    </div>
  )
}

function ResourceBar({ label, value }: { label: string; value: number | null }) {
  const tone = usageTone(value)
  const barColor =
    tone === 'danger' ? 'bg-rose-500' : tone === 'warn' ? 'bg-amber-500' : 'bg-emerald-500'
  const textColor =
    tone === 'danger'
      ? 'text-rose-600 dark:text-rose-400'
      : tone === 'warn'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-slate-800 dark:text-slate-100'

  return (
    <div>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-xs text-slate-500 dark:text-slate-400">{label}</span>
        <span className={cn('text-sm font-semibold tabular-nums', textColor)}>
          {formatPercent(value)}
        </span>
      </div>
      <div className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
        <div
          className={cn('h-1.5 rounded-full transition-all', barColor)}
          style={{ width: `${Math.max(0, Math.min(100, value ?? 0))}%` }}
        />
      </div>
    </div>
  )
}

export default function ServerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const query = usePolling(() => api.server(id ?? ''), 10_000, Boolean(id))
  const now = useNow()

  const server = query.data

  if (query.loading && !server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label="正在加载服务器详情…" />
      </div>
    )
  }

  if (!server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <p className="text-sm font-medium text-slate-900 dark:text-slate-100">服务器不存在或已被删除</p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          {query.error ?? '请返回总览页检查服务器列表。'}
        </p>
        <Link to="/" className="mt-4 inline-block">
          <Button variant="secondary" icon={<ArrowLeftIcon className="h-4 w-4" />}>
            返回总览
          </Button>
        </Link>
      </div>
    )
  }

  const metrics = server.metrics_summary

  return (
    <div className="space-y-5">
      <div>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500 transition-colors hover:text-indigo-600 dark:text-slate-400 dark:hover:text-indigo-400"
        >
          <ArrowLeftIcon className="h-3.5 w-3.5" />
          返回总览
        </Link>

        <div className="mt-3 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="truncate text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
                {server.name}
              </h1>
              <StatusBadge status={server.status} />
            </div>
            <p className="mt-1 font-mono text-xs text-slate-500 dark:text-slate-400">
              {serverAddress(server)} · 最后心跳 {formatRelativeTime(server.last_seen_at, now)}
            </p>
          </div>
          <Button
            variant="secondary"
            onClick={() => void query.refresh()}
            loading={query.loading}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            刷新
          </Button>
        </div>
      </div>

      {query.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          刷新失败：{query.error}
          {server ? '（当前为上次成功获取的数据）' : ''}
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">基础信息</h2>
          <dl className="mt-2">
            <InfoRow label="主机地址">{serverAddress(server)}</InfoRow>
            <InfoRow label="接入模式">{server.mode === 'agent' ? 'Agent 模式' : server.mode}</InfoRow>
            <InfoRow label="操作系统">{server.os ?? '—'}</InfoRow>
            <InfoRow label="架构">{server.arch ?? '—'}</InfoRow>
            <InfoRow label="内核">{server.kernel ?? '—'}</InfoRow>
            <InfoRow label="Agent 版本">{server.agent_version ?? '—'}</InfoRow>
            <InfoRow label="最后心跳">{formatDateTime(server.last_seen_at)}</InfoRow>
            <InfoRow label="创建时间">{formatDateTime(server.created_at)}</InfoRow>
            <InfoRow label="标签">
              {server.tags.length > 0 ? (
                <span className="inline-flex flex-wrap justify-end gap-1">
                  {server.tags.map((tag) => (
                    <span
                      key={tag}
                      className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] font-normal text-slate-500 dark:bg-slate-800 dark:text-slate-400"
                    >
                      {tag}
                    </span>
                  ))}
                </span>
              ) : (
                '—'
              )}
            </InfoRow>
            <InfoRow label="服务器 ID">
              <span className="font-mono text-[11px] font-normal text-slate-500 dark:text-slate-400">
                {server.id}
              </span>
            </InfoRow>
          </dl>
        </section>

        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">最新资源快照</h2>
            <span className="inline-flex items-center gap-1 text-[11px] text-slate-400 dark:text-slate-500">
              <ClockIcon className="h-3.5 w-3.5" />
              {formatRelativeTime(server.last_seen_at, now)}
            </span>
          </div>
          <div className="mt-4 space-y-4">
            <ResourceBar label="CPU 使用率" value={metrics?.cpu_pct ?? null} />
            <ResourceBar label="内存使用率" value={metrics?.mem_used_pct ?? null} />
            <ResourceBar label="磁盘使用率" value={metrics?.disk_used_pct ?? null} />
          </div>
          <dl className="mt-5 grid grid-cols-2 gap-3">
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">系统负载（1 分钟）</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatLoad(metrics?.load1)}
              </dd>
            </div>
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">本次运行时长</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatUptime(metrics?.uptime_s)}
              </dd>
            </div>
          </dl>
          {!metrics ? (
            <p className="mt-4 text-xs text-slate-400 dark:text-slate-500">
              暂无指标数据，agent 上报后会自动显示。
            </p>
          ) : null}
        </section>
      </div>

      <section className="rounded-xl border border-dashed border-slate-300 bg-white/60 p-6 text-center dark:border-slate-700 dark:bg-slate-900/40">
        <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
          <TerminalIcon className="h-5 w-5" />
        </div>
        <h2 className="mt-3 text-sm font-semibold text-slate-800 dark:text-slate-200">
          监控曲线与远程终端将在 M1 提供
        </h2>
        <p className="mx-auto mt-1 max-w-xl text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          M0 先提供服务器基础信息与实时状态；历史指标图表、Web 终端、批量执行与部署等能力将在后续版本开放。
        </p>
      </section>
    </div>
  )
}
