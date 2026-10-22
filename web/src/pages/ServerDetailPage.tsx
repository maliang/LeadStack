import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
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
  PackageIcon,
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
import { ServerChecksTab } from '../components/ServerChecksTab'
import { DiscoverDialog } from '../components/DiscoverDialog'
import { AppDialog, type AppPreset } from '../components/AppDialog'
import { MonitorTab } from '../components/server/MonitorTab'
import { TerminalTab } from '../components/server/TerminalTab'
import { FilesTab } from '../components/server/FilesTab'
import { ServerLogsTab } from '../components/server/ServerLogsTab'
import { useI18n } from '../i18n'
import { useAuth } from '../lib/auth'
import { useToast } from '../components/Toast'
import { cn } from '../lib/cn'

import { t } from '../i18n'
const RANGES = [
  { key: '1h', label: t('s.0661'), ms: 3_600_000 },
  { key: '24h', label: t('s.0662'), ms: 86_400_000 },
  { key: '7d', label: t('s.0663'), ms: 604_800_000 },
] as const

const STEPS: { key: MetricsStep; label: string }[] = [
  { key: 'raw', label: t('s.1074') },
  { key: '1m', label: t('s.0183') },
  { key: '1h', label: t('s.0001') },
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

  const { t } = useI18n()
  const { can } = useAuth()
  const navigate = useNavigate()
  const credentialsQuery = usePolling(api.credentials, 60_000)
  const [searchParams, setSearchParams] = useSearchParams()
  const tabParam = searchParams.get('tab')
  const detailTabs = ['overview', 'checks', 'monitor', 'terminal', 'files', 'logs'] as const
  type DetailTab = (typeof detailTabs)[number]
  const tab: DetailTab = detailTabs.includes((tabParam ?? '') as DetailTab)
    ? ((tabParam ?? 'overview') as DetailTab)
    : 'overview'
  const setTab = (next: DetailTab) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'overview') params.delete('tab')
    else params.set('tab', next)
    setSearchParams(params, { replace: true })
  }
  const [step, setStep] = useState<MetricsStep>('raw')
  const [discoverOpen, setDiscoverOpen] = useState(false)
  const [appPreset, setAppPreset] = useState<AppPreset | null>(null)
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
      toast(result.ok ? t('s.1075') : t('s.1076'), result.ok ? 'success' : 'error')
    } catch (caught) {
      setTestError(errorMessage(caught, t('s.1077')))
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
        toast(t('s.1078'), 'success')
      } else {
        setInfoError(t('s.1079'))
      }
      void query.refresh()
    } catch (caught) {
      setInfoError(errorMessage(caught, t('s.1080')))
    } finally {
      setRefreshingInfo(false)
    }
  }

  if (query.loading && !server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label={t('s.1081')} />
      </div>
    )
  }

  if (!server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <p className="text-sm font-medium text-slate-900 dark:text-slate-100">{t('s.1082')}</p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          {query.error ?? t('s.1083')}
        </p>
        <Link to="/" className="mt-4 inline-block">
          <Button variant="secondary" icon={<ArrowLeftIcon className="h-4 w-4" />}>
            {t('s.1084')}
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
          {t('s.1084')}
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
              {serverAddress(server)} {t('s.1085')} {formatRelativeTime(server.last_seen_at, now)}
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
                {t('s.0281')}
              </Button>
            ) : null}
            {isSsh ? (
              <Button
                variant="secondary"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
                icon={<RefreshIcon className="h-4 w-4" />}
              >
                {t('s.0282')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              onClick={() => setDiscoverOpen(true)}
              icon={<PackageIcon className="h-4 w-4" />}
            >
              {t('discover.open')}
            </Button>
            <Button
              variant="secondary"
              onClick={() => void query.refresh()}
              loading={query.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('s.0689')}
            </Button>
          </div>
        </div>

        <div className="mt-4 flex items-center gap-1 border-b border-slate-200 dark:border-slate-800">
          {(
            ([
              { key: 'overview', labelKey: 'serverTab.overview' },
              { key: 'checks', labelKey: 'serverTab.checks' },
              { key: 'monitor', labelKey: 'serverTab.monitor' },
              { key: 'terminal', labelKey: 'serverTab.terminal', permission: 'terminal.use' },
              { key: 'files', labelKey: 'serverTab.files', permission: 'terminal.use' },
              { key: 'logs', labelKey: 'serverTab.logs', permission: 'terminal.use' },
            ] as const).filter((option) => !('permission' in option) || can(option.permission))
          ).map((option) => (
            <button
              key={option.key}
              type="button"
              onClick={() => setTab(option.key)}
              className={cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors',
                tab === option.key
                  ? 'border-indigo-600 text-indigo-700 dark:border-indigo-400 dark:text-indigo-300'
                  : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
              )}
            >
              {t(option.labelKey)}
            </button>
          ))}
        </div>
      </div>

      {tab === 'checks' ? (
        <ServerChecksTab serverId={server.id} />
      ) : tab === 'monitor' ? (
        <MonitorTab serverId={server.id} />
      ) : tab === 'terminal' && can('terminal.use') ? (
        <TerminalTab serverId={server.id} />
      ) : tab === 'files' && can('terminal.use') ? (
        <FilesTab serverId={server.id} />
      ) : tab === 'logs' && can('terminal.use') ? (
        <ServerLogsTab serverId={server.id} />
      ) : (
        <>
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
                {t('s.1075')}
              </span>
              <span>{t('s.1086')} {formatLatency(testResult.latency_ms)}</span>
              {testResult.ssh_banner ? (
                <span className="font-mono text-xs">{testResult.ssh_banner}</span>
              ) : null}
              {testResult.host_key_fingerprint ? (
                <span className="font-mono text-xs" title={testResult.host_key_fingerprint}>
                  {t('s.1087')} {truncateMiddle(testResult.host_key_fingerprint, 20, 8)}
                </span>
              ) : null}
            </>
          ) : (
            <span className="inline-flex items-center gap-1.5 font-medium">
              <AlertTriangleIcon className="h-4 w-4" />
              {t('s.1088')}{testResult.error?.trim() || t('s.1089')}
            </span>
          )}
        </div>
      ) : null}

      {testError ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.1090')}{testError}
        </div>
      ) : null}

      {query.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.1052')}{query.error}{t('s.1091')}
        </div>
      ) : null}

      {server.last_error ? (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          <AlertTriangleIcon className="h-4 w-4 shrink-0" />
          <span>{t('s.1092')}{server.last_error}</span>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1093')}</h2>
          <dl className="mt-2">
            <InfoRow label={t('s.0066')}>{serverAddress(server)}</InfoRow>
            <InfoRow label={t('s.1094')}>
              {server.mode === 'agent' ? t('s.1095') : t('s.0036')}
            </InfoRow>
            <InfoRow label={t('server.panelCredential')}>
              {server.panel_credential_id
                ? (credentialsQuery.data ?? []).find(
                    (credential) => credential.id === server.panel_credential_id,
                  )?.name ?? server.panel_credential_id
                : t('server.panelCredentialNone')}
            </InfoRow>
            <InfoRow label={t('s.0062')}>
              {server.group_names.length > 0 ? (
                <span className="inline-flex flex-wrap justify-end gap-1">
                  {server.group_names.map((name) => (
                    <span
                      key={name}
                      className="rounded bg-indigo-50 px-1.5 py-0.5 text-[11px] font-normal text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300"
                    >
                      {name}
                    </span>
                  ))}
                </span>
              ) : (
                server.group ?? '—'
              )}
            </InfoRow>
            <InfoRow label={t('s.0075')}>
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
            <InfoRow label={t('s.1096')}>
              {[server.os ?? info?.os, server.arch ?? info?.arch].filter(Boolean).join(' / ') || '—'}
            </InfoRow>
            <InfoRow label={t('s.1097')}>{server.kernel ?? info?.kernel ?? '—'}</InfoRow>
            {server.mode === 'agent' ? (
              <InfoRow label={t('s.1098')}>{server.agent_version ?? '—'}</InfoRow>
            ) : null}
            <InfoRow label={t('s.0538')}>{formatDateTime(server.last_seen_at)}</InfoRow>
            <InfoRow label={t('s.0271')}>{formatDateTime(server.created_at)}</InfoRow>
            <InfoRow label={t('s.1099')}>
              <span className="font-mono text-[11px] font-normal text-slate-500 dark:text-slate-400">
                {server.id}
              </span>
            </InfoRow>
          </dl>
        </section>

        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1100')}</h2>
            <span className="inline-flex items-center gap-1 text-[11px] text-slate-400 dark:text-slate-500">
              <ClockIcon className="h-3.5 w-3.5" />
              {formatRelativeTime(server.last_seen_at, now)}
            </span>
          </div>
          <div className="mt-4 space-y-4">
            <ResourceBar label={t('s.0633')} value={metrics?.cpu_pct ?? null} />
            <ResourceBar label={t('s.0634')} value={metrics?.mem_used_pct ?? null} />
            <ResourceBar label={t('s.0636')} value={metrics?.disk_used_pct ?? null} />
          </div>
          <dl className="mt-5 grid grid-cols-2 gap-3">
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0637')}</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatLoad(metrics?.load1)}
              </dd>
            </div>
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.1101')}</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatUptime(metrics?.uptime_s ?? info?.uptime_s ?? null)}
              </dd>
            </div>
          </dl>
          {!metrics ? (
            <p className="mt-4 text-xs text-slate-400 dark:text-slate-500">
              {t('s.1102')}
            </p>
          ) : null}
        </section>
      </div>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0535')}</h2>
          {isSsh ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={handleRefreshHostInfo}
              loading={refreshingInfo}
              icon={<RefreshIcon className="h-3.5 w-3.5" />}
            >
              {t('s.0282')}
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
              {t('s.1103')}
            </p>
            <p className="mt-1 max-w-lg text-xs leading-relaxed text-slate-500 dark:text-slate-400">
              {isSsh
                ? t('s.1104')
                : t('s.1105')}
            </p>
            {isSsh ? (
              <Button
                className="mt-4"
                size="sm"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
              >
                {t('s.1106')}
              </Button>
            ) : null}
          </div>
        ) : (
          <>
            <div className="mt-2 grid grid-cols-1 gap-x-8 sm:grid-cols-2">
              <dl>
                <InfoRow label={t('s.1107')}>{info.hostname ?? '—'}</InfoRow>
                <InfoRow label={t('s.1108')}>{info.os ?? '—'}</InfoRow>
                <InfoRow label={t('s.1109')}>{info.os_version ?? '—'}</InfoRow>
                <InfoRow label={t('s.1097')}>{info.kernel ?? '—'}</InfoRow>
                <InfoRow label={t('s.1110')}>{info.arch ?? '—'}</InfoRow>
                <InfoRow label={t('s.1111')}>{info.timezone ?? '—'}</InfoRow>
              </dl>
              <dl>
                <InfoRow label={t('s.1112')}>{info.cpu_model ?? '—'}</InfoRow>
                <InfoRow label={t('s.1113')}>{info.cpu_cores ?? '—'}</InfoRow>
                <InfoRow label={t('s.1114')}>{formatBytes(info.mem_total_bytes)}</InfoRow>
                <InfoRow label={t('s.1115')}>
                  {formatBytes(info.disk_total_bytes)}
                  {diskUsedPct !== null ? t('s.1116', { '0': diskUsedPct.toFixed(1) }) : ''}
                </InfoRow>
                <InfoRow label="Docker">
                  {info.docker
                    ? t('s.1117', { '0': info.docker_version ? ` · ${info.docker_version}` : '' })
                    : t('s.1118')}
                </InfoRow>
                <InfoRow label={t('s.1119')}>{formatDateTime(info.collected_at)}</InfoRow>
              </dl>
            </div>
            <div className="mt-3 border-t border-slate-100 pt-3 dark:border-slate-800/70">
              <p className="text-xs text-slate-500 dark:text-slate-400">{t('s.1120')}</p>
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
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1121')}</h2>
            <p className="mt-0.5 text-xs text-slate-400 dark:text-slate-500">
              {t('s.1122')}{'{id}'}{t('s.1123')}
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
              {t('s.0689')}
            </Button>
          </div>
        </div>

        {metricsQuery.error ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {t('s.1124')}{metricsQuery.error}
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
                emptyHint={metricsQuery.loading ? t('s.1125') : t('s.1126')}
              />
            </div>
          ))}
        </div>

        <p className="mt-3 text-xs text-slate-400 dark:text-slate-500">
          {t('s.1127')}
        </p>
      </section>

      <section className="rounded-xl border border-dashed border-slate-300 bg-white/60 p-6 text-center dark:border-slate-700 dark:bg-slate-900/40">
        <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
          <TerminalIcon className="h-5 w-5" />
        </div>
        <h2 className="mt-3 text-sm font-semibold text-slate-800 dark:text-slate-200">
          {t('s.1128')}
        </h2>
        <p className="mx-auto mt-1 max-w-xl text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          {t('s.1129')}
        </p>
      </section>
        </>
      )}
      <DiscoverDialog
        open={discoverOpen}
        serverId={server.id}
        serverName={server.name}
        onClose={() => setDiscoverOpen(false)}
        onPick={(candidate) => {
          const gitDetected =
            candidate.suggested.deploy_mode === 'script' || /git\(/.test(candidate.detail)
          setAppPreset({
            name: candidate.name,
            root: candidate.suggested.root,
            deploy_mode:
              candidate.suggested.deploy_mode === 'script'
                ? 'script'
                : candidate.suggested.deploy_mode === 'release'
                  ? 'release'
                  : 'inplace',
            target_dir: candidate.suggested.target_dir,
            restart_cmd: candidate.suggested.restart_cmd,
            healthcheck_port: candidate.suggested.healthcheck_port,
            gitDetected,
          })
        }}
      />

      <AppDialog
        open={appPreset !== null}
        app={null}
        preset={appPreset}
        onClose={() => setAppPreset(null)}
        onSaved={() => {
          setAppPreset(null)
          navigate('/apps')
        }}
      />
    </div>
  )
}
