import { useMemo, useState, type ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  METRIC_KEYS,
  api,
  type MetricSeries,
  type MetricsStep,
  type TestConnectionResult,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import {
  formatBytes,
  formatDateTime,
  formatLatency,
  formatLoad,
  formatPercent,
  formatRelativeTime,
  formatUptime,
  metricLabel,
  serverAddress,
  truncateMiddle,
  usageTone,
} from '../lib/format'
import { Button } from '../components/Button'
import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  CheckIcon,
  ClockIcon,
  InfoIcon,
  RefreshIcon,
  TerminalIcon,
  WifiIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { MetricChart } from '../components/MetricChart'
import { ModeBadge, StatusBadge } from '../components/StatusBadge'
import { useToast } from '../components/Toast'
import { cn } from '../lib/cn'

const RANGES = [
  { key: '1h', label: '近 1 小时', ms: 3_600_000 },
  { key: '24h', label: '近 24 小时', ms: 86_400_000 },
  { key: '7d', label: '近 7 天', ms: 604_800_000 },
] as const

const STEPS: { key: MetricsStep; label: string }[] = [
  { key: 'raw', label: '原始' },
  { key: '1m', label: '1 分钟' },
  { key: '1h', label: '1 小时' },
]

type RangeKey = (typeof RANGES)[number]['key']

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

function pickSeries(series: MetricSeries[], aliases: readonly string[]): MetricSeries[] {
  for (const alias of aliases) {
    const found = series.find((item) => item.metric === alias)
    if (found) return [found]
  }
  return []
}

export default function ServerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { toast } = useToast()
  const now = useNow()
  const query = usePolling(() => api.server(id ?? ''), 10_000, Boolean(id))

  const [step, setStep] = useState<MetricsStep>('raw')
  const [rangeKey, setRangeKey] = useState<RangeKey>('1h')
  const range = RANGES.find((item) => item.key === rangeKey) ?? RANGES[0]

  const metricsQuery = usePolling(
    () =>
      api.metrics(id ?? '', {
        from: Date.now() - range.ms,
        to: Date.now(),
        step,
      }),
    30_000,
    Boolean(id),
    `${id ?? ''}|${step}|${rangeKey}`,
  )

  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<TestConnectionResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)

  const [refreshingInfo, setRefreshingInfo] = useState(false)
  const [infoError, setInfoError] = useState<string | null>(null)

  const server = query.data

  const chartSeries = useMemo(
    () => ({
      cpu: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.cpu),
      mem: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.mem),
      disk: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.disk),
      load: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.load),
    }),
    [metricsQuery.data],
  )

  const handleTestConnection = async () => {
    if (!id) return
    setTesting(true)
    setTestError(null)
    setTestResult(null)
    try {
      const result = await api.testConnection(id)
      setTestResult(result)
      toast(result.ok ? 'SSH 连接正常' : 'SSH 连接失败', result.ok ? 'success' : 'error')
    } catch (caught) {
      setTestError(errorMessage(caught, '连通性测试失败'))
    } finally {
      setTesting(false)
    }
  }

  const handleRefreshHostInfo = async () => {
    if (!id) return
    setRefreshingInfo(true)
    setInfoError(null)
    try {
      const info = await api.refreshHostInfo(id)
      if (info) {
        toast('主机信息已刷新', 'success')
      } else {
        setInfoError('控制端未返回主机信息，请稍后重试')
      }
      void query.refresh()
    } catch (caught) {
      setInfoError(errorMessage(caught, '刷新主机信息失败'))
    } finally {
      setRefreshingInfo(false)
    }
  }

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
  const info = server.host_info
  const isSsh = server.mode === 'ssh'
  const diskUsedPct =
    info?.disk_total_bytes && info.disk_used_bytes !== null
      ? (info.disk_used_bytes / info.disk_total_bytes) * 100
      : null

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
              <ModeBadge mode={server.mode} />
              {server.group ? (
                <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                  {server.group}
                </span>
              ) : null}
            </div>
            <p className="mt-1 font-mono text-xs text-slate-500 dark:text-slate-400">
              {serverAddress(server)} · 最后心跳 {formatRelativeTime(server.last_seen_at, now)}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {isSsh ? (
              <Button
                variant="secondary"
                onClick={handleTestConnection}
                loading={testing}
                icon={<WifiIcon className="h-4 w-4" />}
              >
                测试连通性
              </Button>
            ) : null}
            {isSsh ? (
              <Button
                variant="secondary"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
                icon={<RefreshIcon className="h-4 w-4" />}
              >
                刷新主机信息
              </Button>
            ) : null}
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
      </div>

      {testResult ? (
        <div
          className={cn(
            'flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border px-4 py-3 text-sm',
            testResult.ok
              ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200'
              : 'border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300',
          )}
        >
          {testResult.ok ? (
            <>
              <span className="inline-flex items-center gap-1.5 font-medium">
                <CheckIcon className="h-4 w-4" />
                SSH 连接正常
              </span>
              <span>延迟 {formatLatency(testResult.latency_ms)}</span>
              {testResult.ssh_banner ? (
                <span className="font-mono text-xs">{testResult.ssh_banner}</span>
              ) : null}
              {testResult.host_key_fingerprint ? (
                <span className="font-mono text-xs" title={testResult.host_key_fingerprint}>
                  主机密钥 {truncateMiddle(testResult.host_key_fingerprint, 20, 8)}
                </span>
              ) : null}
            </>
          ) : (
            <span className="inline-flex items-center gap-1.5 font-medium">
              <AlertTriangleIcon className="h-4 w-4" />
              SSH 连接失败：{testResult.error?.trim() || '控制端未返回具体原因'}
            </span>
          )}
        </div>
      ) : null}

      {testError ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          连通性测试请求失败：{testError}
        </div>
      ) : null}

      {query.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          刷新失败：{query.error}（当前为上次成功获取的数据）
        </div>
      ) : null}

      {server.last_error ? (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          <AlertTriangleIcon className="h-4 w-4 shrink-0" />
          <span>最后错误：{server.last_error}</span>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">基础信息</h2>
          <dl className="mt-2">
            <InfoRow label="主机地址">{serverAddress(server)}</InfoRow>
            <InfoRow label="接入方式">
              {server.mode === 'agent' ? 'Agent 模式' : 'SSH 免安装'}
            </InfoRow>
            <InfoRow label="分组">{server.group ?? '—'}</InfoRow>
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
            <InfoRow label="系统 / 架构">
              {[server.os ?? info?.os, server.arch ?? info?.arch].filter(Boolean).join(' / ') || '—'}
            </InfoRow>
            <InfoRow label="内核">{server.kernel ?? info?.kernel ?? '—'}</InfoRow>
            {server.mode === 'agent' ? (
              <InfoRow label="Agent 版本">{server.agent_version ?? '—'}</InfoRow>
            ) : null}
            <InfoRow label="最后心跳">{formatDateTime(server.last_seen_at)}</InfoRow>
            <InfoRow label="创建时间">{formatDateTime(server.created_at)}</InfoRow>
            <InfoRow label="服务器 ID">
              <span className="font-mono text-[11px] font-normal text-slate-500 dark:text-slate-400">
                {server.id}
              </span>
            </InfoRow>
          </dl>
        </section>

        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">实时指标</h2>
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
                {formatUptime(metrics?.uptime_s ?? info?.uptime_s ?? null)}
              </dd>
            </div>
          </dl>
          {!metrics ? (
            <p className="mt-4 text-xs text-slate-400 dark:text-slate-500">
              暂无指标数据，等待首次采集完成。
            </p>
          ) : null}
        </section>
      </div>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">主机信息</h2>
          {isSsh ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={handleRefreshHostInfo}
              loading={refreshingInfo}
              icon={<RefreshIcon className="h-3.5 w-3.5" />}
            >
              刷新主机信息
            </Button>
          ) : null}
        </div>

        {infoError ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {infoError}
          </p>
        ) : null}

        {!info ? (
          <div className="mt-4 flex flex-col items-center justify-center rounded-lg border border-dashed border-slate-300 px-6 py-10 text-center dark:border-slate-700">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
              <InfoIcon className="h-5 w-5" />
            </div>
            <p className="mt-3 text-sm font-medium text-slate-800 dark:text-slate-200">
              尚未采集，点击刷新主机信息
            </p>
            <p className="mt-1 max-w-lg text-xs leading-relaxed text-slate-500 dark:text-slate-400">
              {isSsh
                ? '控制端会通过 SSH 采集主机名、系统版本、CPU/内存/磁盘等硬件信息，通常在一个采集周期内完成。'
                : 'Agent 上线后立即上报一次主机信息，之后每 6 小时更新一次；稍后刷新本页即可看到。'}
            </p>
            {isSsh ? (
              <Button
                className="mt-4"
                size="sm"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
              >
                立即刷新主机信息
              </Button>
            ) : null}
          </div>
        ) : (
          <>
            <div className="mt-2 grid grid-cols-1 gap-x-8 sm:grid-cols-2">
              <dl>
                <InfoRow label="主机名">{info.hostname ?? '—'}</InfoRow>
                <InfoRow label="操作系统">{info.os ?? '—'}</InfoRow>
                <InfoRow label="系统版本">{info.os_version ?? '—'}</InfoRow>
                <InfoRow label="内核">{info.kernel ?? '—'}</InfoRow>
                <InfoRow label="架构">{info.arch ?? '—'}</InfoRow>
                <InfoRow label="时区">{info.timezone ?? '—'}</InfoRow>
              </dl>
              <dl>
                <InfoRow label="CPU 型号">{info.cpu_model ?? '—'}</InfoRow>
                <InfoRow label="CPU 核数">{info.cpu_cores ?? '—'}</InfoRow>
                <InfoRow label="内存总量">{formatBytes(info.mem_total_bytes)}</InfoRow>
                <InfoRow label="磁盘总量">
                  {formatBytes(info.disk_total_bytes)}
                  {diskUsedPct !== null ? ` · 已用 ${diskUsedPct.toFixed(1)}%` : ''}
                </InfoRow>
                <InfoRow label="Docker">
                  {info.docker
                    ? `已安装${info.docker_version ? ` · ${info.docker_version}` : ''}`
                    : '未检测到'}
                </InfoRow>
                <InfoRow label="采集时间">{formatDateTime(info.collected_at)}</InfoRow>
              </dl>
            </div>
            <div className="mt-3 border-t border-slate-100 pt-3 dark:border-slate-800/70">
              <p className="text-xs text-slate-500 dark:text-slate-400">IP 地址</p>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {info.ip_addrs.length > 0 ? (
                  info.ip_addrs.map((ip) => (
                    <span
                      key={ip}
                      className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
                    >
                      {ip}
                    </span>
                  ))
                ) : (
                  <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                )}
              </div>
            </div>
          </>
        )}
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">指标历史曲线</h2>
            <p className="mt-0.5 text-xs text-slate-400 dark:text-slate-500">
              数据源 GET /servers/{'{id}'}/metrics · 原始保留 7 天 / 1 分钟 30 天 / 1 小时 1 年
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <div className="inline-flex overflow-hidden rounded-md border border-slate-200 dark:border-slate-700">
              {STEPS.map((option) => (
                <button
                  key={option.key}
                  type="button"
                  onClick={() => setStep(option.key)}
                  className={cn(
                    'px-2.5 py-1.5 text-xs font-medium transition-colors',
                    step === option.key
                      ? 'bg-indigo-600 text-white'
                      : 'bg-white text-slate-600 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800',
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <div className="inline-flex overflow-hidden rounded-md border border-slate-200 dark:border-slate-700">
              {RANGES.map((option) => (
                <button
                  key={option.key}
                  type="button"
                  onClick={() => setRangeKey(option.key)}
                  className={cn(
                    'px-2.5 py-1.5 text-xs font-medium transition-colors',
                    rangeKey === option.key
                      ? 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900'
                      : 'bg-white text-slate-600 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800',
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void metricsQuery.refresh()}
              loading={metricsQuery.loading}
              icon={<RefreshIcon className="h-3.5 w-3.5" />}
            >
              刷新
            </Button>
          </div>
        </div>

        {metricsQuery.error ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            指标加载失败：{metricsQuery.error}
          </p>
        ) : null}

        <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-2">
          {[
            {
              key: 'cpu',
              title: metricLabel('cpu.pct'),
              series: chartSeries.cpu,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'mem',
              title: metricLabel('mem.used_pct'),
              series: chartSeries.mem,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'disk',
              title: metricLabel('disk.used.pct'),
              series: chartSeries.disk,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'load',
              title: metricLabel('load.1'),
              series: chartSeries.load,
              format: (value: number) => value.toFixed(2),
            },
          ].map((chart) => (
            <div
              key={chart.key}
              className="rounded-lg border border-slate-100 p-3 dark:border-slate-800"
            >
              <p className="mb-2 text-xs font-medium text-slate-600 dark:text-slate-300">
                {chart.title}
              </p>
              <MetricChart
                series={chart.series}
                height={200}
                formatValue={chart.format}
                emptyHint={metricsQuery.loading ? '正在加载指标…' : '该时间段暂无数据'}
              />
            </div>
          ))}
        </div>

        <p className="mt-3 text-xs text-slate-400 dark:text-slate-500">
          曲线每 30 秒自动刷新；切换「原始 / 1 分钟 / 1 小时」查看不同聚合粒度。
        </p>
      </section>

      <section className="rounded-xl border border-dashed border-slate-300 bg-white/60 p-6 text-center dark:border-slate-700 dark:bg-slate-900/40">
        <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
          <TerminalIcon className="h-5 w-5" />
        </div>
        <h2 className="mt-3 text-sm font-semibold text-slate-800 dark:text-slate-200">
          远程终端与批量部署将在后续版本提供
        </h2>
        <p className="mx-auto mt-1 max-w-xl text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          M1 已支持凭证管理、SSH 连通性测试、主机信息采集与指标历史；Web 终端、任务下发与部署模板将在后续里程碑开放。
        </p>
      </section>
    </div>
  )
}
