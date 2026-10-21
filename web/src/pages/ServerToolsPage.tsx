import { Link } from 'react-router-dom'
import { api } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { useAuth } from '../lib/auth'
import { formatRelativeTime } from '../lib/format'
import { ServerIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { PageHeader } from '../components/PageHeader'
import { StatusBadge, ModeBadge } from '../components/StatusBadge'
import { EmptyState } from '../components/EmptyState'

export type ServerToolKind = 'terminal' | 'files' | 'logs'

const TITLE_KEYS: Record<ServerToolKind, string> = {
  terminal: 'tools.terminalTitle',
  files: 'tools.filesTitle',
  logs: 'tools.logsTitle',
}

const DESC_KEYS: Record<ServerToolKind, string> = {
  terminal: 'tools.terminalDescription',
  files: 'tools.filesDescription',
  logs: 'tools.logsDescription',
}

/** 终端 / 文件 / 日志的入口页：先选服务器，再进入详情页对应标签。 */
export default function ServerToolsPage({ kind }: { kind: ServerToolKind }) {
  const { t } = useI18n()
  const { can } = useAuth()
  const serversQuery = usePolling(api.servers, 10_000)
  const servers = serversQuery.data ?? []

  if (!can('terminal.use')) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center text-sm text-slate-500 shadow-sm dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
        {t('authz.noPermission')}
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <PageHeader title={t(TITLE_KEYS[kind])} description={t(DESC_KEYS[kind])} />

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {serversQuery.loading && serversQuery.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : servers.length === 0 ? (
          <EmptyState
            icon={<ServerIcon className="h-6 w-6" />}
            title={t('tools.emptyTitle')}
            description={t('tools.emptyDescription')}
            actionLabel={t('nav.servers')}
            onAction={() => {
              window.location.hash = '#/'
            }}
          />
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800/70">
            {servers.map((server) => (
              <li key={server.id}>
                <Link
                  to={`/servers/${server.id}?tab=${kind}`}
                  className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 transition-colors hover:bg-slate-50 dark:hover:bg-slate-900/60"
                >
                  <span className="min-w-0 flex-1 truncate text-sm font-medium text-slate-800 dark:text-slate-100">
                    {server.name}
                  </span>
                  <ModeBadge mode={server.mode} />
                  <StatusBadge status={server.status} />
                  <span className="font-mono text-xs text-slate-500 dark:text-slate-400">
                    {server.host}
                  </span>
                  <span className="text-xs text-slate-400 dark:text-slate-500">
                    {formatRelativeTime(server.last_seen_at)}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
