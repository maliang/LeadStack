import { useState } from 'react'
import { ApiError, api, type Credential } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { formatDateTime, truncateMiddle } from '../lib/format'
import { Button } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CredentialDialog } from '../components/CredentialDialog'
import { EmptyState } from '../components/EmptyState'
import { KeyIcon, PlusIcon, RefreshIcon, TrashIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'
import { IconButton } from '../components/Button'
import { t } from '../i18n'

function KindBadge({ kind }: { kind: string }) {
  const isKey = kind === 'key'
  return (
    <span
      className={
        'inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset ' +
        (isKey
          ? 'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30'
          : 'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30')
      }
    >
      {isKey ? t('s.0072') : t('s.0073')}
    </span>
  )
}

export default function CredentialsPage() {
  const { toast } = useToast()
  const { can } = useAuth()
  const canWrite = can('server.write')
  const query = usePolling(api.credentials, 10_000)
  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<Credential | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  const credentials = query.data ?? []

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    setDeleteError(null)
    try {
      await api.deleteCredential(deleting.id)
      toast(t('s.0973', { '0': deleting.name }), 'success')
      setDeleting(null)
      void query.refresh()
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setDeleteError(
          t('s.0974'),
        )
      } else {
        setDeleteError(errorMessage(caught, t('s.0975')))
      }
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('s.0976')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {t('s.0977')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={() => void query.refresh()}
            loading={query.loading}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('s.0689')}
          </Button>
          <Button disabled={!canWrite} onClick={() => setCreateOpen(true)} icon={<PlusIcon className="h-4 w-4" />}>
            {t('s.0070')}
          </Button>
        </div>
      </div>

      {query.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('s.0978')}{query.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void query.refresh()}>
            {t('s.0053')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0979')}</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {credentials.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {query.updatedAt ? t('s.0949', { '0': formatDateTime(query.updatedAt) }) : t('s.0425')}
          </p>
        </div>

        {query.loading && query.data === null ? (
          <InlineLoading label={t('s.0980')} />
        ) : credentials.length === 0 ? (
          <EmptyState
            icon={<KeyIcon className="h-6 w-6" />}
            title={t('s.0981')}
            description={t('s.0982')}
            actionLabel={t('s.0983')}
            onAction={() => setCreateOpen(true)}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[760px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0531')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0399')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0112')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0984')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0985')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0271')}</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">{t('s.0539')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {credentials.map((credential) => (
                  <tr
                    key={credential.id}
                    className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                  >
                    <td className="px-4 py-3 font-medium text-slate-900 dark:text-slate-100">
                      {credential.name}
                    </td>
                    <td className="px-4 py-3">
                      <KindBadge kind={credential.kind} />
                    </td>
                    <td className="px-4 py-3 text-slate-600 dark:text-slate-300">
                      {credential.username || '—'}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-500 dark:text-slate-400">
                      <span title={credential.fingerprint ?? undefined}>
                        {credential.fingerprint
                          ? truncateMiddle(credential.fingerprint, 20, 10)
                          : '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 tabular-nums text-slate-600 dark:text-slate-300">
                      {credential.server_count > 0 ? t('s.0960', { '0': credential.server_count }) : t('s.0986')}
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(credential.created_at)}
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex justify-end">
                        <IconButton
                          label={t('s.0987')}
                          tone="danger"
                          onClick={() => {
                            setDeleteError(null)
                            setDeleting(credential)
                          }}
                        >
                          <TrashIcon className="h-4 w-4" />
                        </IconButton>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        {t('s.0988')}
      </p>

      <CredentialDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => void query.refresh()}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('s.0987')}
        description={deleting ? t('s.0989', { '0': deleting.name }) : undefined}
        confirmLabel={t('s.0295')}
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => {
          setDeleting(null)
          setDeleteError(null)
        }}
        onConfirm={() => void handleDelete()}
      >
        <p>
          {t('s.0990')}
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deleting?.name}」
          </span>
          {t('s.0515')}
        </p>
      </ConfirmDialog>
    </div>
  )
}
