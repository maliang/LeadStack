import { Fragment, useEffect, useMemo, useState } from 'react'
import { api, type AuditLogEntry, type Paged } from '../lib/api'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { ChevronRightIcon, RefreshIcon, ShieldCheckIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { PageHeader } from '../components/PageHeader'

const ACTION_OPTIONS = [
  'auth.login',
  'auth.logout',
  'server.create',
  'server.update',
  'server.delete',
  'server.batch',
  'credential.create',
  'credential.delete',
  'check.create',
  'check.update',
  'check.delete',
  'check.run',
  'alert_rule.create',
  'alert_rule.update',
  'alert_rule.delete',
  'alert_rule.silence',
  'alert.ack',
  'app.create',
  'template.create',
  'release.create',
  'release.retry',
  'release.rollback',
  'release.cancel',
  'backup.create',
  'backup.delete',
  'backup.restore',
  'user.create',
  'user.update',
  'user.delete',
  'token.create',
  'token.delete',
  'terminal.open',
  'terminal.close',
  'agent.upgrade',
  'import.servers',
]

const RANGES = [
  { key: '1h', ms: 3_600_000 },
  { key: '24h', ms: 86_400_000 },
  { key: '7d', ms: 604_800_000 },
  { key: '30d', ms: 2_592_000_000 },
  { key: 'all', ms: 0 },
] as const

const selectClass =
  'h-9 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

const inputClass =
  'h-9 rounded-md border border-slate-300 bg-white px-3 text-sm text-slate-700 shadow-sm outline-none transition placeholder:text-slate-400 focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

export default function AuditPage() {
  const { t } = useI18n()
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const [range, setRange] = useState<(typeof RANGES)[number]['key']>('24h')
  const [items, setItems] = useState<AuditLogEntry[]>([])
  const [page, setPage] = useState<Paged<AuditLogEntry>['page']>(null)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<number | null>(null)

  const filters = useMemo(() => {
    const selected = RANGES.find((item) => item.key === range) ?? RANGES[1]
    return {
      actor: actor.trim() || undefined,
      action: action || undefined,
      from: selected.ms > 0 ? Date.now() - selected.ms : undefined,
      limit: 50,
    }
  }, [actor, action, range])

  const load = async (cursor?: string) => {
    if (cursor) setLoadingMore(true)
    else setLoading(true)
    setError(null)
    try {
      const result = await api.auditLogs({ ...filters, cursor })
      setItems((current) => (cursor ? [...current, ...result.items] : result.items))
      setPage(result.page)
    } catch (caught) {
      setError(errorMessage(caught, t('audit.loadFailed')))
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(filters)])

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 10_000)
    return () => window.clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(filters)])

  const formatDetail = (entry: AuditLogEntry): string => {
    if (!entry.detail) return ''
    try {
      return JSON.stringify(JSON.parse(entry.detail), null, 2)
    } catch {
      return entry.detail
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('audit.title')}
        description={t('audit.description')}
        actions={
          <Button
            variant="secondary"
            onClick={() => void load()}
            loading={loading}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('common.refresh')}
          </Button>
        }
      />

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <input
            value={actor}
            onChange={(event) => setActor(event.target.value)}
            placeholder={t('audit.filterActor')}
            className={inputClass + ' w-40'}
            aria-label={t('audit.filterActor')}
          />
          <select
            value={action}
            onChange={(event) => setAction(event.target.value)}
            className={selectClass}
            aria-label={t('audit.filterAction')}
          >
            <option value="">{t('audit.allActions')}</option>
            {ACTION_OPTIONS.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
          <select
            value={range}
            onChange={(event) => setRange(event.target.value as typeof range)}
            className={selectClass}
            aria-label={t('audit.filterTime')}
          >
            {RANGES.map((option) => (
              <option key={option.key} value={option.key}>
                {t(`audit.range.${option.key}`)}
              </option>
            ))}
          </select>
          <span className="ml-auto text-xs text-slate-400 dark:text-slate-500">
            {t('audit.count', { count: items.length })}
          </span>
        </div>

        {error ? (
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            <span>{error}</span>
            <Button size="sm" variant="secondary" onClick={() => void load()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : null}

        {loading && items.length === 0 ? (
          <InlineLoading label={t('common.loading')} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={<ShieldCheckIcon className="h-6 w-6" />}
            title={t('audit.emptyTitle')}
            description={t('audit.emptyDescription')}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1020px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="w-8 px-2 py-2.5" />
                  <th className="px-4 py-2.5 font-medium">{t('audit.time')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('audit.actor')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('audit.action')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('audit.resource')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('audit.result')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('audit.ip')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {items.map((entry) => {
                  const isOpen = expanded === entry.id
                  const ok = entry.status_code !== null && entry.status_code < 400
                  return (
                    <Fragment key={entry.id}>
                      <tr
                        onClick={() => setExpanded(isOpen ? null : entry.id)}
                        className={cn(
                          'cursor-pointer transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60',
                          isOpen && 'bg-slate-50/80 dark:bg-slate-900/60',
                        )}
                      >
                        <td className="px-2 py-3 align-top">
                          <span
                            className={cn(
                              'inline-flex h-5 w-5 items-center justify-center text-slate-400 transition-transform',
                              isOpen && 'rotate-90 text-indigo-500',
                            )}
                          >
                            <ChevronRightIcon className="h-4 w-4" />
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                          {formatRelativeTime(entry.ts)}
                          <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                            {formatDateTime(entry.ts)}
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top text-xs text-slate-700 dark:text-slate-200">
                          {entry.actor}
                          {entry.actor_role ? (
                            <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                              {entry.actor_role}
                            </span>
                          ) : null}
                        </td>
                        <td className="px-4 py-3 align-top">
                          <span className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                            {entry.action}
                          </span>
                        </td>
                        <td className="max-w-[220px] px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                          {entry.resource_type ?? '—'}
                          {entry.resource_id ? (
                            <span className="mt-0.5 block truncate font-mono text-[11px] text-slate-400 dark:text-slate-500">
                              {entry.resource_id}
                            </span>
                          ) : null}
                        </td>
                        <td className="px-4 py-3 align-top text-xs">
                          <span
                            className={cn(
                              'font-mono tabular-nums',
                              entry.status_code === null
                                ? 'text-slate-400'
                                : ok
                                  ? 'text-emerald-600 dark:text-emerald-400'
                                  : 'text-rose-600 dark:text-rose-400',
                            )}
                          >
                            {entry.status_code ?? '—'}
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top font-mono text-[11px] text-slate-500 dark:text-slate-400">
                          {entry.ip ?? '—'}
                        </td>
                      </tr>
                      {isOpen ? (
                        <tr>
                          <td colSpan={7} className="bg-slate-50/70 px-6 py-3 dark:bg-slate-950/40">
                            <div className="grid gap-2 text-xs text-slate-600 dark:text-slate-300 sm:grid-cols-2">
                              <p>
                                <span className="text-slate-400">{t('audit.methodPath')}：</span>
                                <span className="font-mono">
                                  {entry.method ?? '—'} {entry.path ?? ''}
                                </span>
                              </p>
                              <p className="truncate" title={entry.user_agent ?? ''}>
                                <span className="text-slate-400">{t('audit.userAgent')}：</span>
                                {entry.user_agent ?? '—'}
                              </p>
                            </div>
                            <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-slate-950 p-3 font-mono text-[11px] leading-relaxed text-slate-100">
                              {formatDetail(entry) || t('audit.noDetail')}
                            </pre>
                          </td>
                        </tr>
                      ) : null}
                    </Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        {page?.has_more ? (
          <div className="border-t border-slate-200 px-4 py-3 text-center dark:border-slate-800">
            <Button
              variant="secondary"
              size="sm"
              loading={loadingMore}
              onClick={() => void load(page.next_cursor ?? undefined)}
            >
              {t('audit.loadMore')}
            </Button>
          </div>
        ) : null}
      </section>
    </div>
  )
}
