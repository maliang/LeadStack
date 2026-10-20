import { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { useI18n } from '../../i18n'
import { errorMessage } from '../../lib/errors'
import { cn } from '../../lib/cn'
import { Button } from '../Button'
import { RefreshIcon } from '../Icons'

const inputClass =
  'h-9 rounded-md border border-slate-300 bg-white px-3 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

/** 服务器详情「日志」标签：路径 + 行数 + grep，服务端过滤，5s 自动刷新可关。 */
export function ServerLogsTab({ serverId }: { serverId: string }) {
  const { t } = useI18n()
  const [path, setPath] = useState('/var/log/syslog')
  const [lines, setLines] = useState('200')
  const [grep, setGrep] = useState('')
  const [auto, setAuto] = useState(true)
  const [output, setOutput] = useState<string[]>([])
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    if (!path.trim()) return
    setLoading(true)
    setError(null)
    try {
      const result = await api.serverLogs(serverId, {
        path: path.trim(),
        lines: Number(lines) || 200,
        grep: grep.trim() || undefined,
      })
      setOutput(result.lines)
      setTruncated(result.truncated)
    } catch (caught) {
      setError(errorMessage(caught, t('logs.loadFailed')))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverId])

  useEffect(() => {
    if (!auto) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 5_000)
    return () => window.clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, serverId, path, lines, grep])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-2 rounded-lg border border-slate-200 bg-white px-3 py-2.5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="min-w-[200px] flex-1">
          <label htmlFor="log-path" className="block text-[11px] text-slate-500 dark:text-slate-400">
            {t('logs.path')}
          </label>
          <input
            id="log-path"
            value={path}
            onChange={(event) => setPath(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void load()
            }}
            placeholder="/var/log/app.log"
            className={inputClass + ' mt-0.5 w-full font-mono text-xs'}
          />
        </div>
        <div>
          <label htmlFor="log-lines" className="block text-[11px] text-slate-500 dark:text-slate-400">
            {t('logs.lines')}
          </label>
          <select
            id="log-lines"
            value={lines}
            onChange={(event) => setLines(event.target.value)}
            className={inputClass + ' mt-0.5'}
          >
            {['100', '200', '500', '1000'].map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </div>
        <div className="min-w-[140px]">
          <label htmlFor="log-grep" className="block text-[11px] text-slate-500 dark:text-slate-400">
            grep
          </label>
          <input
            id="log-grep"
            value={grep}
            onChange={(event) => setGrep(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void load()
            }}
            placeholder={t('logs.grepPlaceholder')}
            className={inputClass + ' mt-0.5 w-full font-mono text-xs'}
          />
        </div>
        <Button size="sm" variant="secondary" onClick={() => void load()} loading={loading} icon={<RefreshIcon className="h-3.5 w-3.5" />}>
          {t('common.refresh')}
        </Button>
        <label className="flex items-center gap-1.5 pb-1.5 text-xs text-slate-500 dark:text-slate-400">
          <input
            type="checkbox"
            checked={auto}
            onChange={(event) => setAuto(event.target.checked)}
            className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 dark:border-slate-600 dark:bg-slate-800"
          />
          {t('logs.autoRefresh')}
        </label>
      </div>

      {error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{error}</span>
          <Button size="sm" variant="secondary" onClick={() => void load()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <div className="overflow-hidden rounded-lg border border-slate-800 bg-slate-950">
        <div className="flex items-center justify-between border-b border-slate-800 px-3 py-1.5 text-[11px] text-slate-400">
          <span className="font-mono">{path}</span>
          <span className="flex items-center gap-2">
            {truncated ? (
              <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-300">
                {t('logs.truncated')}
              </span>
            ) : null}
            <span className="tabular-nums">{t('logs.lineCount', { count: output.length })}</span>
          </span>
        </div>
        <pre className="max-h-[520px] overflow-auto p-3 font-mono text-[11px] leading-relaxed text-slate-100">
          {output.length === 0 ? (
            <span className={cn('text-slate-500')}>{loading ? t('common.loading') : t('logs.empty')}</span>
          ) : (
            output.join('\n')
          )}
        </pre>
      </div>
      <p className="text-xs text-slate-400 dark:text-slate-500">{t('logs.footerHint')}</p>
    </div>
  )
}
