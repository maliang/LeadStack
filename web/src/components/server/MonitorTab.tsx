import { useEffect, useState } from 'react'
import { api, type RemotePort, type RemoteProcess } from '../../lib/api'
import { useI18n } from '../../i18n'
import { errorMessage } from '../../lib/errors'
import { formatPercent } from '../../lib/format'
import { cn } from '../../lib/cn'
import { Button } from '../Button'
import { EmptyState } from '../EmptyState'
import { RefreshIcon, ServerIcon } from '../Icons'

/** 服务器详情「监控」标签：进程 / 端口两张表，3 秒刷新。 */
export function MonitorTab({ serverId }: { serverId: string }) {
  const { t } = useI18n()
  const [processes, setProcesses] = useState<RemoteProcess[]>([])
  const [ports, setPorts] = useState<RemotePort[]>([])
  const [sort, setSort] = useState<'cpu' | 'mem'>('cpu')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      setLoading(true)
      const [processResult, portResult] = await Promise.allSettled([
        api.serverProcesses(serverId, { sort, limit: 50 }),
        api.serverPorts(serverId),
      ])
      if (cancelled) return
      if (processResult.status === 'fulfilled') setProcesses(processResult.value)
      if (portResult.status === 'fulfilled') setPorts(portResult.value)
      const firstError =
        processResult.status === 'rejected'
          ? errorMessage(processResult.reason)
          : portResult.status === 'rejected'
            ? errorMessage(portResult.reason)
            : null
      setError(firstError)
      setLoading(false)
    }
    void load()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 3_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [serverId, sort])

  return (
    <div className="space-y-4">
      {error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('monitor.error')}：{error}</span>
          <Button size="sm" variant="secondary" onClick={() => setLoading(true)} icon={<RefreshIcon className="h-3.5 w-3.5" />}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
            {t('monitor.processes')}
          </h2>
          <div className="flex items-center gap-1.5">
            {(['cpu', 'mem'] as const).map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setSort(option)}
                className={cn(
                  'rounded border px-2 py-1 text-xs font-medium',
                  sort === option
                    ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400',
                )}
              >
                {t(`monitor.sort.${option}`)}
              </button>
            ))}
            <span className="ml-1 text-[11px] text-slate-400 dark:text-slate-500">
              {t('monitor.autoRefresh')}
            </span>
          </div>
        </div>
        {loading && processes.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-slate-400">{t('common.loading')}</p>
        ) : processes.length === 0 ? (
          <EmptyState
            icon={<ServerIcon className="h-6 w-6" />}
            title={t('monitor.noProcesses')}
            description={t('monitor.noProcessesHint')}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border-collapse text-xs">
              <thead>
                <tr className="border-b border-slate-200 text-left text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2 font-medium">PID</th>
                  <th className="px-4 py-2 font-medium">{t('monitor.user')}</th>
                  <th className="px-4 py-2 font-medium">CPU</th>
                  <th className="px-4 py-2 font-medium">{t('monitor.memory')}</th>
                  <th className="px-4 py-2 font-medium">RSS</th>
                  <th className="px-4 py-2 font-medium">{t('monitor.command')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {processes.map((process) => (
                  <tr key={process.pid}>
                    <td className="px-4 py-1.5 font-mono tabular-nums text-slate-600 dark:text-slate-300">
                      {process.pid}
                    </td>
                    <td className="px-4 py-1.5 text-slate-600 dark:text-slate-300">{process.user ?? '—'}</td>
                    <td className="px-4 py-1.5 tabular-nums text-slate-600 dark:text-slate-300">
                      {formatPercent(process.cpu_pct)}
                    </td>
                    <td className="px-4 py-1.5 tabular-nums text-slate-600 dark:text-slate-300">
                      {formatPercent(process.mem_pct)}
                    </td>
                    <td className="px-4 py-1.5 tabular-nums text-slate-500 dark:text-slate-400">
                      {process.rss_kb !== null ? `${process.rss_kb} KB` : '—'}
                    </td>
                    <td className="max-w-[420px] truncate px-4 py-1.5 font-mono text-slate-500 dark:text-slate-400" title={process.command}>
                      {process.command}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
            {t('monitor.ports')}
          </h2>
        </div>
        {ports.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-slate-400">{t('monitor.noPorts')}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[640px] border-collapse text-xs">
              <thead>
                <tr className="border-b border-slate-200 text-left text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2 font-medium">{t('monitor.proto')}</th>
                  <th className="px-4 py-2 font-medium">{t('monitor.local')}</th>
                  <th className="px-4 py-2 font-medium">PID</th>
                  <th className="px-4 py-2 font-medium">{t('monitor.process')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {ports.map((port, index) => (
                  <tr key={`${port.proto}-${port.local}-${index}`}>
                    <td className="px-4 py-1.5 text-slate-600 dark:text-slate-300">{port.proto}</td>
                    <td className="px-4 py-1.5 font-mono text-slate-600 dark:text-slate-300">{port.local}</td>
                    <td className="px-4 py-1.5 font-mono tabular-nums text-slate-500 dark:text-slate-400">
                      {port.pid ?? '—'}
                    </td>
                    <td className="px-4 py-1.5 text-slate-500 dark:text-slate-400">{port.process ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
