import { useState, type FormEvent } from 'react'
import { api, type ApiToken, type CreatedApiToken, type UserRole } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CopyButton } from '../components/CopyButton'
import { EmptyState } from '../components/EmptyState'
import { KeyIcon, PlusIcon, RefreshIcon, TrashIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { PageHeader } from '../components/PageHeader'
import { useToast } from '../components/Toast'

const ROLES: UserRole[] = ['admin', 'operator', 'viewer']
const EXPIRY_OPTIONS = [
  { value: '', labelKey: 'tokens.expiry.never' },
  { value: '720h', labelKey: 'tokens.expiry.30d' },
  { value: '2160h', labelKey: 'tokens.expiry.90d' },
  { value: '8760h', labelKey: 'tokens.expiry.1y' },
]

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

export default function TokensPage() {
  const { t } = useI18n()
  const { toast } = useToast()
  const query = usePolling(api.apiTokens, 15_000)
  const tokens = query.data ?? []

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [role, setRole] = useState<UserRole>('viewer')
  const [expiry, setExpiry] = useState('')
  const [creating, setCreating] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [created, setCreated] = useState<CreatedApiToken | null>(null)

  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const [revokeLoading, setRevokeLoading] = useState(false)

  const handleCreate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!name.trim()) {
      setFormError(t('tokens.errorName'))
      return
    }
    setCreating(true)
    setFormError(null)
    try {
      const result = await api.createApiToken({
        name: name.trim(),
        role,
        expires_in: expiry || undefined,
      })
      setCreated(result)
      setCreateOpen(false)
      setName('')
      setRole('viewer')
      setExpiry('')
      void query.refresh()
    } catch (caught) {
      setFormError(errorMessage(caught, t('common.error')))
    } finally {
      setCreating(false)
    }
  }

  const handleRevoke = async () => {
    if (!revoking) return
    setRevokeLoading(true)
    try {
      await api.deleteApiToken(revoking.id)
      toast(t('tokens.revokedToast', { name: revoking.name }), 'success')
      setRevoking(null)
      void query.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    } finally {
      setRevokeLoading(false)
    }
  }

  const tokenStatus = (token: ApiToken): { labelKey: string; className: string } => {
    if (token.revoked_at) return { labelKey: 'tokens.status.revoked', className: 'text-slate-500 dark:text-slate-400' }
    if (token.expires_at && token.expires_at < Date.now())
      return { labelKey: 'tokens.status.expired', className: 'text-rose-600 dark:text-rose-400' }
    return { labelKey: 'tokens.status.active', className: 'text-emerald-600 dark:text-emerald-400' }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('tokens.title')}
        description={t('tokens.description')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => void query.refresh()}
              loading={query.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('common.refresh')}
            </Button>
            <Button
              onClick={() => {
                setFormError(null)
                setCreateOpen(true)
              }}
              icon={<PlusIcon className="h-4 w-4" />}
            >
              {t('tokens.create')}
            </Button>
          </>
        }
      />

      <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-xs leading-relaxed text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
        {t('tokens.securityNote')}
      </div>

      {query.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('tokens.loadFailed')}：{query.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void query.refresh()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <KeyIcon className="h-4 w-4 text-indigo-500" />
            {t('tokens.title')}
          </h2>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            {tokens.length}
          </span>
        </div>

        {query.loading && query.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : tokens.length === 0 ? (
          <EmptyState
            icon={<KeyIcon className="h-6 w-6" />}
            title={t('tokens.emptyTitle')}
            description={t('tokens.emptyDescription')}
            actionLabel={t('tokens.create')}
            onAction={() => setCreateOpen(true)}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[960px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('common.name')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('users.role')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('tokens.status')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('tokens.lastUsed')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('tokens.expiresAt')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {tokens.map((token) => {
                  const status = tokenStatus(token)
                  return (
                    <tr key={token.id} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                      <td className="px-4 py-3 align-top">
                        <span className="font-medium text-slate-800 dark:text-slate-100">{token.name}</span>
                        {token.created_by ? (
                          <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                            {t('tokens.createdBy', { name: token.created_by })}
                          </span>
                        ) : null}
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                        {t(`role.${token.role}`)}
                      </td>
                      <td className={cn('px-4 py-3 align-top text-xs font-medium', status.className)}>
                        {t(status.labelKey)}
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {token.last_used_at ? formatRelativeTime(token.last_used_at) : t('tokens.neverUsed')}
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {token.expires_at ? formatDateTime(token.expires_at) : t('tokens.expiry.never')}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex justify-end">
                          <IconButton
                            label={t('tokens.revoke')}
                            tone="danger"
                            disabled={Boolean(token.revoked_at)}
                            onClick={() => setRevoking(token)}
                          >
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
        )}
      </section>

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title={t('tokens.create')}
        description={t('tokens.createHint')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="token-form" loading={creating}>
              {t('common.create')}
            </Button>
          </>
        }
      >
        <form id="token-form" onSubmit={handleCreate} className="space-y-4">
          <div>
            <label htmlFor="token-name" className={labelClass}>
              {t('common.name')}
            </label>
            <input
              id="token-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              className={inputClass + ' mt-1.5'}
              placeholder={t('tokens.namePlaceholder')}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor="token-role" className={labelClass}>
                {t('users.role')}
              </label>
              <select
                id="token-role"
                value={role}
                onChange={(event) => setRole(event.target.value as UserRole)}
                className={inputClass + ' mt-1.5'}
              >
                {ROLES.map((item) => (
                  <option key={item} value={item}>
                    {t(`role.${item}`)}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="token-expiry" className={labelClass}>
                {t('tokens.expiresAt')}
              </label>
              <select
                id="token-expiry"
                value={expiry}
                onChange={(event) => setExpiry(event.target.value)}
                className={inputClass + ' mt-1.5'}
              >
                {EXPIRY_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {t(option.labelKey)}
                  </option>
                ))}
              </select>
            </div>
          </div>
          {formError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {formError}
            </p>
          ) : null}
        </form>
      </Modal>

      <Modal
        open={created !== null}
        onClose={() => setCreated(null)}
        title={t('tokens.createdTitle')}
        description={t('tokens.createdHint')}
        footer={
          <Button onClick={() => setCreated(null)}>{t('tokens.savedIt')}</Button>
        }
      >
        <div className="space-y-3">
          <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
            {t('tokens.onceWarning')}
          </p>
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs text-slate-700 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200">
              {created?.token ?? ''}
            </code>
            {created?.token ? <CopyButton value={created.token} /> : null}
          </div>
        </div>
      </Modal>

      <ConfirmDialog
        open={revoking !== null}
        title={t('tokens.revoke')}
        description={revoking ? revoking.name : undefined}
        confirmLabel={t('tokens.revoke')}
        tone="danger"
        loading={revokeLoading}
        onClose={() => setRevoking(null)}
        onConfirm={() => void handleRevoke()}
      >
        <p>{t('tokens.revokeImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}
