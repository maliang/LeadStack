import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useReleaseRuns } from '../lib/useReleaseRuns'
import { useI18n } from '../i18n'
import { formatRelativeTime } from '../lib/format'
import { ReleaseStatusBadge } from '../components/DeployBadges'
import { PageHeader } from '../components/PageHeader'
import { InlineLoading } from '../components/Loading'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { PlayIcon, RefreshIcon } from '../components/Icons'

export default function ReleasesPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const releasesQuery = usePolling(() => api.releases({ limit: 100 }), 10_000)
  const releases = releasesQuery.data ?? []
  const runsById = useReleaseRuns(
    useMemo(() => releases.slice(0, 30).map((release) => release.id), [releases]),
  )

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('releases.title')}
        description={t('releases.description')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => void releasesQuery.refresh()}
              loading={releasesQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('common.refresh')}
            </Button>
            <Link to="/apps">
              <Button icon={<PlayIcon className="h-4 w-4" />}>{t('releases.newRelease')}</Button>
            </Link>
          </>
        }
      />

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {releasesQuery.loading && releasesQuery.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : releases.length === 0 ? (
          <EmptyState
            icon={<PlayIcon className="h-6 w-6" />}
            title={t('releases.emptyTitle')}
            description={t('releases.emptyDescription')}
            actionLabel={t('nav.apps')}
            onAction={() => navigate('/apps')}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[980px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('releases.app')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('releases.version')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('common.status')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('releases.progress')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('releases.createdAt')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {releases.map((release) => {
                  const runs = runsById[release.id] ?? release.runs
                  const success = runs.filter((run) => run.status === 'success').length
                  const failed = runs.filter((run) => run.status === 'failed').length
                  const active = runs.filter(
                    (run) => run.status === 'running' || run.status === 'pending',
                  ).length
                  return (
                    <tr
                      key={release.id}
                      className="cursor-pointer hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                      onClick={() => navigate(`/releases/${release.id}`)}
                    >
                      <td className="px-4 py-3 align-top text-sm text-slate-800 dark:text-slate-100">
                        {release.app_name ?? release.app_id}
                      </td>
                      <td className="px-4 py-3 align-top font-mono text-xs text-slate-600 dark:text-slate-300">
                        v{release.version}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <ReleaseStatusBadge status={release.status} />
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                        <span className="text-emerald-600 dark:text-emerald-400">{success}</span>
                        {' / '}
                        <span className="text-rose-600 dark:text-rose-400">{failed}</span>
                        {' / '}
                        <span className="text-blue-600 dark:text-blue-400">{active}</span>
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {formatRelativeTime(release.created_at)}
                      </td>
                      <td className="px-4 py-3 align-top text-right">
                        <span className="text-xs text-indigo-600 dark:text-indigo-400">
                          {t('common.details')}
                        </span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
