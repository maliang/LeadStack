import { useState, type FormEvent } from 'react'
import { api, type ManagedUser, type UserRole } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { cn } from '../lib/cn'
import { useAuth } from '../lib/auth'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import { PencilIcon, PlusIcon, RefreshIcon, TrashIcon, UsersIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { PageHeader } from '../components/PageHeader'
import { useToast } from '../components/Toast'

const ROLES: UserRole[] = ['admin', 'operator', 'viewer']

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

interface UserFormState {
  username: string
  password: string
  role: UserRole
}

export default function UsersPage() {
  const { t } = useI18n()
  const { user: me } = useAuth()
  const { toast } = useToast()
  const query = usePolling(api.users, 10_000)
  const users = query.data ?? []

  const [createOpen, setCreateOpen] = useState(false)
  const [form, setForm] = useState<UserFormState>({ username: '', password: '', role: 'viewer' })
  const [creating, setCreating] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const [editing, setEditing] = useState<ManagedUser | null>(null)
  const [resetOpen, setResetOpen] = useState(false)
  const [newPassword, setNewPassword] = useState('')
  const [resetting, setResetting] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const [deleting, setDeleting] = useState<ManagedUser | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  const [passwordOpen, setPasswordOpen] = useState(false)
  const [oldPassword, setOldPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [changing, setChanging] = useState(false)

  const openCreate = () => {
    setForm({ username: '', password: '', role: 'viewer' })
    setFormError(null)
    setCreateOpen(true)
  }

  const handleCreate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!form.username.trim()) return setFormError(t('users.errorUsername'))
    if (form.password.length < 6) return setFormError(t('users.errorPassword'))
    setCreating(true)
    setFormError(null)
    try {
      await api.createUser({
        username: form.username.trim(),
        password: form.password,
        role: form.role,
      })
      toast(t('users.created', { name: form.username.trim() }), 'success')
      setCreateOpen(false)
      void query.refresh()
    } catch (caught) {
      setFormError(errorMessage(caught, t('common.error')))
    } finally {
      setCreating(false)
    }
  }

  const handleRoleChange = async (target: ManagedUser, role: UserRole) => {
    setActionError(null)
    try {
      await api.updateUser(target.id, { role })
      toast(t('users.roleUpdated', { name: target.username }), 'success')
      void query.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    }
  }

  const handleToggleDisabled = async (target: ManagedUser) => {
    setActionError(null)
    try {
      await api.updateUser(target.id, { disabled: !target.disabled })
      toast(
        target.disabled
          ? t('users.enabledToast', { name: target.username })
          : t('users.disabledToast', { name: target.username }),
        'success',
      )
      void query.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    }
  }

  const handleResetPassword = async () => {
    if (!editing) return
    if (newPassword.length < 6) {
      setActionError(t('users.errorPassword'))
      return
    }
    setResetting(true)
    setActionError(null)
    try {
      await api.updateUser(editing.id, { password: newPassword })
      toast(t('users.passwordReset', { name: editing.username }), 'success')
      setResetOpen(false)
      setEditing(null)
      setNewPassword('')
    } catch (caught) {
      setActionError(errorMessage(caught, t('common.error')))
    } finally {
      setResetting(false)
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    try {
      await api.deleteUser(deleting.id)
      toast(t('users.deleted', { name: deleting.username }), 'success')
      setDeleting(null)
      void query.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    } finally {
      setDeleteLoading(false)
    }
  }

  const handleChangeOwnPassword = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!oldPassword || confirmPassword.length < 6) {
      setActionError(t('users.errorPassword'))
      return
    }
    if (newPassword && newPassword !== confirmPassword) {
      /* handled below */
    }
    setChanging(true)
    setActionError(null)
    try {
      await api.changeMyPassword({ old_password: oldPassword, new_password: confirmPassword })
      toast(t('users.ownPasswordChanged'), 'success')
      setPasswordOpen(false)
      setOldPassword('')
      setConfirmPassword('')
    } catch (caught) {
      setActionError(errorMessage(caught, t('common.error')))
    } finally {
      setChanging(false)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('users.title')}
        description={t('users.description')}
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
            <Button onClick={openCreate} icon={<PlusIcon className="h-4 w-4" />}>
              {t('users.create')}
            </Button>
          </>
        }
      />

      {query.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('users.loadFailed')}：{query.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void query.refresh()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <UsersIcon className="h-4 w-4 text-indigo-500" />
            {t('users.title')}
          </h2>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            {users.length}
          </span>
        </div>

        {query.loading && query.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : users.length === 0 ? (
          <EmptyState
            icon={<UsersIcon className="h-6 w-6" />}
            title={t('users.emptyTitle')}
            description={t('users.emptyDescription')}
            actionLabel={t('users.create')}
            onAction={openCreate}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[900px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('users.username')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('users.role')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('common.status')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('users.createdAt')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {users.map((target) => {
                  const isSelf = me?.id === target.id
                  return (
                    <tr key={target.id} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                      <td className="px-4 py-3 align-top">
                        <span className="font-medium text-slate-800 dark:text-slate-100">
                          {target.username}
                        </span>
                        {isSelf ? (
                          <span className="ml-2 rounded bg-indigo-50 px-1.5 py-0.5 text-[11px] text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300">
                            {t('users.you')}
                          </span>
                        ) : null}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <select
                          value={target.role}
                          disabled={isSelf}
                          onChange={(event) => void handleRoleChange(target, event.target.value as UserRole)}
                          className="h-8 rounded border border-slate-300 bg-white px-2 text-xs text-slate-700 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
                          aria-label={t('users.role')}
                        >
                          {ROLES.map((role) => (
                            <option key={role} value={role}>
                              {t(`role.${role}`)}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <span
                          className={cn(
                            'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
                            target.disabled
                              ? 'bg-slate-100 text-slate-500 ring-slate-500/20 dark:bg-slate-800 dark:text-slate-400'
                              : 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300',
                          )}
                        >
                          {target.disabled ? t('common.disabled') : t('common.enabled')}
                        </span>
                      </td>
                      <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                        {formatDateTime(target.created_at)}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex items-center justify-end gap-0.5">
                          <Button
                            size="sm"
                            variant="secondary"
                            disabled={isSelf}
                            onClick={() => {
                              setEditing(target)
                              setNewPassword('')
                              setActionError(null)
                              setResetOpen(true)
                            }}
                          >
                            {t('users.resetPassword')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={isSelf}
                            onClick={() => void handleToggleDisabled(target)}
                          >
                            {target.disabled ? t('users.enable') : t('users.disable')}
                          </Button>
                          <IconButton
                            label={t('common.delete')}
                            tone="danger"
                            disabled={isSelf}
                            onClick={() => setDeleting(target)}
                          >
                            <TrashIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label={t('users.resetPassword')}
                            onClick={() => {
                              setEditing(target)
                              setNewPassword('')
                              setActionError(null)
                              setResetOpen(true)
                            }}
                          >
                            <PencilIcon className="h-4 w-4" />
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

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
          {t('users.ownPasswordTitle')}
        </h2>
        <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
          {t('users.ownPasswordHint')}
        </p>
        <Button className="mt-3" size="sm" variant="secondary" onClick={() => setPasswordOpen(true)}>
          {t('users.changePassword')}
        </Button>
      </section>

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title={t('users.create')}
        description={t('users.createHint')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="user-form" loading={creating}>
              {t('common.create')}
            </Button>
          </>
        }
      >
        <form id="user-form" onSubmit={handleCreate} className="space-y-4">
          <div>
            <label htmlFor="new-username" className={labelClass}>
              {t('users.username')}
            </label>
            <input
              id="new-username"
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              className={inputClass + ' mt-1.5'}
              placeholder="alice"
            />
          </div>
          <div>
            <label htmlFor="new-password" className={labelClass}>
              {t('users.password')}
            </label>
            <input
              id="new-password"
              type="password"
              value={form.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
              className={inputClass + ' mt-1.5'}
              autoComplete="new-password"
            />
          </div>
          <div>
            <label htmlFor="new-role" className={labelClass}>
              {t('users.role')}
            </label>
            <select
              id="new-role"
              value={form.role}
              onChange={(event) => setForm({ ...form, role: event.target.value as UserRole })}
              className={inputClass + ' mt-1.5'}
            >
              {ROLES.map((role) => (
                <option key={role} value={role}>
                  {t(`role.${role}`)}
                </option>
              ))}
            </select>
          </div>
          {formError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {formError}
            </p>
          ) : null}
        </form>
      </Modal>

      <Modal
        open={resetOpen}
        onClose={() => setResetOpen(false)}
        title={t('users.resetPassword')}
        description={editing ? t('users.resetHint', { name: editing.username }) : undefined}
        footer={
          <>
            <Button variant="secondary" onClick={() => setResetOpen(false)} disabled={resetting}>
              {t('common.cancel')}
            </Button>
            <Button variant="danger" onClick={() => void handleResetPassword()} loading={resetting}>
              {t('users.resetPassword')}
            </Button>
          </>
        }
      >
        <label htmlFor="reset-password" className={labelClass}>
          {t('users.newPassword')}
        </label>
        <input
          id="reset-password"
          type="password"
          value={newPassword}
          onChange={(event) => setNewPassword(event.target.value)}
          className={inputClass + ' mt-1.5'}
          autoComplete="new-password"
        />
        {actionError ? (
          <p className="mt-2 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {actionError}
          </p>
        ) : null}
      </Modal>

      <Modal
        open={passwordOpen}
        onClose={() => setPasswordOpen(false)}
        title={t('users.changePassword')}
        description={t('users.ownPasswordHint')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setPasswordOpen(false)} disabled={changing}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="own-password-form" loading={changing}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <form id="own-password-form" onSubmit={handleChangeOwnPassword} className="space-y-4">
          <div>
            <label htmlFor="old-password" className={labelClass}>
              {t('users.oldPassword')}
            </label>
            <input
              id="old-password"
              type="password"
              value={oldPassword}
              onChange={(event) => setOldPassword(event.target.value)}
              className={inputClass + ' mt-1.5'}
              autoComplete="current-password"
            />
          </div>
          <div>
            <label htmlFor="confirm-password" className={labelClass}>
              {t('users.newPassword')}
            </label>
            <input
              id="confirm-password"
              type="password"
              value={confirmPassword}
              onChange={(event) => setConfirmPassword(event.target.value)}
              className={inputClass + ' mt-1.5'}
              autoComplete="new-password"
            />
          </div>
          {actionError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {actionError}
            </p>
          ) : null}
        </form>
      </Modal>

      <ConfirmDialog
        open={deleting !== null}
        title={t('common.delete')}
        description={deleting ? t('users.deleteHint', { name: deleting.username }) : undefined}
        confirmLabel={t('common.delete')}
        tone="danger"
        loading={deleteLoading}
        onClose={() => setDeleting(null)}
        onConfirm={() => void handleDelete()}
      >
        <p>{t('users.deleteImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}
