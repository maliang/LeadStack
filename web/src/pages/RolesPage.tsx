import { useMemo, useState } from 'react'
import {
  api,
  type Permission,
  type Role,
  type RolePayload,
  type RoleScope,
  type ScopeMode,
  type Server,
  type ServerGroup,
} from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useAuth } from '../lib/auth'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import { PencilIcon, PlusIcon, RefreshIcon, ShieldCheckIcon, TrashIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { PageHeader } from '../components/PageHeader'
import { SearchMultiSelect } from '../components/SearchMultiSelect'
import { useToast } from '../components/Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const DOMAIN_ORDER = [
  'server',
  'check',
  'alert',
  'deploy',
  'backup',
  'task',
  'group',
  'agent',
  'user',
  'role',
  'token',
  'enroll',
  'system',
  'audit',
]

interface RoleForm {
  name: string
  description: string
  permissions: Set<string>
  mode: ScopeMode
  groupIds: string[]
  serverIds: string[]
}

const EMPTY_FORM: RoleForm = {
  name: '',
  description: '',
  permissions: new Set(),
  mode: 'all',
  groupIds: [],
  serverIds: [],
}

function scopeSummary(role: Role, t: (key: string, vars?: Record<string, string | number>) => string): string {
  const scope = role.scope
  if (scope.mode === 'all') return t('roles.scopeAll')
  const groups = scope.group_ids.length
  const servers = scope.server_ids.length
  if (scope.mode === 'groups') {
    return t('roles.scopeGroups', { groups, servers })
  }
  return t('roles.scopeServers', { servers })
}

export default function RolesPage() {
  const { can } = useAuth()
  const { t } = useI18n()
  const { toast } = useToast()
  const rolesQuery = usePolling(api.roles, 15_000)
  const permissionsQuery = usePolling(api.permissions, 60_000)
  const groupsQuery = usePolling(api.serverGroups, 30_000)
  const serversQuery = usePolling(api.servers, 30_000)

  const roles = rolesQuery.data ?? []
  const permissions = permissionsQuery.data ?? []
  const groups = groupsQuery.data ?? []
  const servers = serversQuery.data ?? []

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<Role | null>(null)
  const [form, setForm] = useState<RoleForm>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const [deleting, setDeleting] = useState<Role | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const groupOptions = useMemo(
    () => groups.map((group: ServerGroup) => ({ id: group.id, label: group.name })),
    [groups],
  )
  const serverOptions = useMemo(
    () =>
      servers.map((server: Server) => ({
        id: server.id,
        label: server.name,
        hint: `${server.host}:${server.port ?? ''}`,
      })),
    [servers],
  )

  const permissionGroups = useMemo(() => {
    const map = new Map<string, Permission[]>()
    for (const permission of permissions) {
      const domain = permission.key.split('.')[0] ?? 'other'
      if (!map.has(domain)) map.set(domain, [])
      map.get(domain)!.push(permission)
    }
    return [...map.entries()].sort(
      (a, b) =>
        (DOMAIN_ORDER.indexOf(a[0]) === -1 ? 99 : DOMAIN_ORDER.indexOf(a[0])) -
        (DOMAIN_ORDER.indexOf(b[0]) === -1 ? 99 : DOMAIN_ORDER.indexOf(b[0])),
    )
  }, [permissions])

  const openCreate = () => {
    setEditing(null)
    setForm(EMPTY_FORM)
    setFormError(null)
    setDialogOpen(true)
  }

  const openEdit = (role: Role) => {
    setEditing(role)
    setForm({
      name: role.name,
      description: role.description,
      permissions: new Set(role.permissions),
      mode: role.scope.mode,
      groupIds: role.scope.group_ids,
      serverIds: role.scope.server_ids,
    })
    setFormError(null)
    setDialogOpen(true)
  }

  const togglePermission = (key: string) => {
    setForm((current) => {
      const next = new Set(current.permissions)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return { ...current, permissions: next }
    })
  }

  const handleSave = async () => {
    if (!form.name.trim()) {
      setFormError(t('roles.errorName'))
      return
    }
    const assignable = permissions.filter((permission) => !permission.admin_only)
    const selected = [...form.permissions].filter((key) =>
      assignable.some((permission) => permission.key === key),
    )
    if (selected.length === 0) {
      setFormError(t('roles.errorPermissions'))
      return
    }
    if (form.mode !== 'all' && form.groupIds.length === 0 && form.serverIds.length === 0) {
      setFormError(t('roles.errorScope'))
      return
    }
    const scope: RoleScope = {
      mode: form.mode,
      group_ids: form.mode === 'all' ? [] : form.groupIds,
      server_ids: form.mode === 'all' ? [] : form.serverIds,
    }
    const payload: RolePayload = {
      name: form.name.trim(),
      description: form.description.trim(),
      permissions: selected,
      scope,
    }
    setSaving(true)
    setFormError(null)
    try {
      if (editing) {
        await api.updateRole(editing.key, payload)
        toast(t('roles.updated', { name: payload.name }), 'success')
      } else {
        await api.createRole(payload)
        toast(t('roles.created', { name: payload.name }), 'success')
      }
      setDialogOpen(false)
      void rolesQuery.refresh()
    } catch (caught) {
      setFormError(errorMessage(caught, t('common.error')))
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    setDeleteError(null)
    try {
      await api.deleteRole(deleting.key)
      toast(t('roles.deleted', { name: deleting.name }), 'success')
      setDeleting(null)
      void rolesQuery.refresh()
    } catch (caught) {
      setDeleteError(errorMessage(caught, t('common.error')))
    } finally {
      setDeleteLoading(false)
    }
  }

  if (!can('role.manage')) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center text-sm text-slate-500 shadow-sm dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
        {t('authz.noPermission')}
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('roles.title')}
        description={t('roles.description')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => void Promise.all([rolesQuery.refresh(), permissionsQuery.refresh()])}
              loading={rolesQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('common.refresh')}
            </Button>
            <Button onClick={openCreate} icon={<PlusIcon className="h-4 w-4" />}>
              {t('roles.create')}
            </Button>
          </>
        }
      />

      {rolesQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>
            {t('roles.loadFailed')}：{rolesQuery.error}
          </span>
          <Button size="sm" variant="secondary" onClick={() => void rolesQuery.refresh()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <ShieldCheckIcon className="h-4 w-4 text-indigo-500" />
            {t('roles.title')}
          </h2>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            {roles.length}
          </span>
        </div>

        {rolesQuery.loading && rolesQuery.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : roles.length === 0 ? (
          <EmptyState
            icon={<ShieldCheckIcon className="h-6 w-6" />}
            title={t('roles.emptyTitle')}
            description={t('roles.emptyDescription')}
            actionLabel={t('roles.create')}
            onAction={openCreate}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[980px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('common.name')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('roles.type')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('roles.permissions')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('roles.scope')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('roles.userCount')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {roles.map((role) => (
                  <tr key={role.key} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                    <td className="max-w-[280px] px-4 py-3 align-top">
                      <span className="block truncate font-medium text-slate-800 dark:text-slate-100">
                        {role.name}
                      </span>
                      <span className="mt-0.5 block truncate font-mono text-[11px] text-slate-400 dark:text-slate-500">
                        {role.key}
                      </span>
                      {role.description ? (
                        <span className="mt-0.5 block truncate text-[11px] text-slate-500 dark:text-slate-400">
                          {role.description}
                        </span>
                      ) : null}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <span
                        className={cn(
                          'inline-flex rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
                          role.builtin
                            ? 'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-800 dark:text-slate-300 dark:ring-slate-400/30'
                            : 'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300',
                        )}
                      >
                        {role.builtin ? t('roles.builtin') : t('roles.custom')}
                      </span>
                      <span className="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">
                        {role.created_at ? formatDateTime(role.created_at) : '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                      <span className="tabular-nums">
                        {t('roles.permissionCount', { count: role.permissions.length })}
                      </span>
                    </td>
                    <td className="max-w-[220px] px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                      {scopeSummary(role, t)}
                    </td>
                    <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                      {role.user_count}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <div className="flex items-center justify-end gap-0.5">
                        <IconButton
                          label={t('common.edit')}
                          disabled={role.builtin}
                          onClick={() => openEdit(role)}
                        >
                          <PencilIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton
                          label={t('common.delete')}
                          tone="danger"
                          disabled={role.builtin}
                          onClick={() => {
                            setDeleteError(null)
                            setDeleting(role)
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

      <Modal
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={editing ? t('roles.edit') : t('roles.create')}
        description={t('roles.dialogHint')}
        size="lg"
        footer={
          <>
            <Button variant="secondary" onClick={() => setDialogOpen(false)} disabled={saving}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void handleSave()} loading={saving}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <label htmlFor="role-name" className={labelClass}>
                {t('roles.name')}
              </label>
              <input
                id="role-name"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                maxLength={32}
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <div>
              <label htmlFor="role-description" className={labelClass}>
                {t('roles.descriptionLabel')}
              </label>
              <input
                id="role-description"
                value={form.description}
                onChange={(event) => setForm({ ...form, description: event.target.value })}
                maxLength={200}
                className={inputClass + ' mt-1.5'}
              />
            </div>
          </div>

          <div>
            <p className={labelClass}>{t('roles.scope')}</p>
            <div className="mt-1.5 grid gap-2 sm:grid-cols-3">
              {(
                [
                  { mode: 'all', titleKey: 'roles.scopeMode.all', hintKey: 'roles.scopeMode.allHint' },
                  { mode: 'groups', titleKey: 'roles.scopeMode.groups', hintKey: 'roles.scopeMode.groupsHint' },
                  { mode: 'servers', titleKey: 'roles.scopeMode.servers', hintKey: 'roles.scopeMode.serversHint' },
                ] as const
              ).map((item) => (
                <button
                  key={item.mode}
                  type="button"
                  onClick={() => setForm({ ...form, mode: item.mode })}
                  className={cn(
                    'rounded-lg border px-3 py-2 text-left text-xs transition-colors',
                    form.mode === item.mode
                      ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                      : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
                  )}
                >
                  <span className="block text-sm font-medium">{t(item.titleKey)}</span>
                  <span className="mt-0.5 block text-[11px] opacity-80">{t(item.hintKey)}</span>
                </button>
              ))}
            </div>

            {form.mode !== 'all' ? (
              <div className="mt-2 space-y-3 rounded-lg border border-slate-200 p-2.5 dark:border-slate-800">
                {form.mode === 'groups' || form.groupIds.length > 0 ? (
                  <div>
                    <p className="text-[11px] font-medium text-slate-600 dark:text-slate-300">
                      {t('roles.groups')}
                    </p>
                    <div className="mt-1">
                      <SearchMultiSelect
                        options={groupOptions}
                        value={form.groupIds}
                        onChange={(groupIds) => setForm({ ...form, groupIds })}
                        placeholder={t('roles.searchGroups')}
                        emptyText={t('roles.noGroups')}
                      />
                    </div>
                  </div>
                ) : null}
                <div>
                  <p className="text-[11px] font-medium text-slate-600 dark:text-slate-300">
                    {t('roles.servers')}
                  </p>
                  <div className="mt-1">
                    <SearchMultiSelect
                      options={serverOptions}
                      value={form.serverIds}
                      onChange={(serverIds) => setForm({ ...form, serverIds })}
                      placeholder={t('roles.searchServers')}
                      emptyText={t('roles.noServers')}
                    />
                  </div>
                </div>
              </div>
            ) : null}
          </div>

          <div>
            <p className={labelClass}>{t('roles.permissions')}</p>
            <p className="mt-0.5 text-[11px] text-slate-400 dark:text-slate-500">
              {t('roles.permissionsHint')}
            </p>
            <div className="mt-2 space-y-3">
              {permissionGroups.map(([domain, items]) => (
                <div key={domain}>
                  <p className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
                    {t(`perm.domain.${domain}`)}
                  </p>
                  <div className="mt-1 grid gap-1 sm:grid-cols-2">
                    {items.map((permission) => {
                      const checked = form.permissions.has(permission.key)
                      return (
                        <label
                          key={permission.key}
                          className={cn(
                            'flex items-start gap-2 rounded-md border px-2.5 py-1.5 text-xs',
                            permission.admin_only
                              ? 'cursor-not-allowed border-slate-100 bg-slate-50 opacity-70 dark:border-slate-800/60 dark:bg-slate-950/40'
                              : 'cursor-pointer border-slate-200 hover:border-slate-300 dark:border-slate-800',
                          )}
                          title={permission.description || permission.key}
                        >
                          <input
                            type="checkbox"
                            checked={checked}
                            disabled={permission.admin_only}
                            onChange={() => togglePermission(permission.key)}
                            className="mt-0.5 h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 dark:border-slate-600 dark:bg-slate-800"
                          />
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center gap-1.5 font-medium text-slate-700 dark:text-slate-200">
                              {permission.name || permission.key}
                              {permission.admin_only ? (
                                <span className="rounded bg-slate-200 px-1 py-0.5 text-[10px] text-slate-600 dark:bg-slate-700 dark:text-slate-300">
                                  {t('roles.adminOnly')}
                                </span>
                              ) : null}
                            </span>
                            <span className="mt-0.5 block truncate font-mono text-[10px] text-slate-400 dark:text-slate-500">
                              {permission.key}
                            </span>
                          </span>
                        </label>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>
          </div>

          {formError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {formError}
            </p>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={deleting !== null}
        title={t('roles.deleteTitle')}
        description={deleting ? deleting.name : undefined}
        confirmLabel={t('common.delete')}
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => setDeleting(null)}
        onConfirm={() => void handleDelete()}
      >
        <p>{t('roles.deleteImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}

/** 供路由层判断：角色管理权限。 */
export function rolesPagePermission(): string {
  return 'role.manage'
}
