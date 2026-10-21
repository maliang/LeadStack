import { useMemo, useState } from 'react'
import { api, type Server, type ServerGroup } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useAuth } from '../lib/auth'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import { FolderIcon, PencilIcon, PlusIcon, RefreshIcon, TrashIcon, UsersIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { PageHeader } from '../components/PageHeader'
import { SearchMultiSelect } from '../components/SearchMultiSelect'
import { useToast } from '../components/Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

export default function GroupsPage() {
  const { can, scope } = useAuth()
  const { t } = useI18n()
  const { toast } = useToast()
  const groupsQuery = usePolling(api.serverGroups, 15_000)
  const serversQuery = usePolling(api.servers, 30_000)
  const groups = groupsQuery.data ?? []
  const servers = serversQuery.data ?? []
  const canRead = can('server.read')
  const canWrite = can('group.write') && scope.mode === 'all'

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<ServerGroup | null>(null)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const [deleting, setDeleting] = useState<ServerGroup | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const [membersGroup, setMembersGroup] = useState<ServerGroup | null>(null)
  const [memberIds, setMemberIds] = useState<string[]>([])
  const [membersLoading, setMembersLoading] = useState(false)
  const [membersSaving, setMembersSaving] = useState(false)
  const [membersError, setMembersError] = useState<string | null>(null)

  const serverOptions = useMemo(
    () =>
      servers.map((server: Server) => ({
        id: server.id,
        label: server.name,
        hint: `${server.host}:${server.port ?? ''}`,
      })),
    [servers],
  )

  const openCreate = () => {
    setEditing(null)
    setName('')
    setDescription('')
    setFormError(null)
    setDialogOpen(true)
  }

  const openEdit = (group: ServerGroup) => {
    setEditing(group)
    setName(group.name)
    setDescription(group.description)
    setFormError(null)
    setDialogOpen(true)
  }

  const handleSave = async () => {
    if (!name.trim()) {
      setFormError(t('groups.errorName'))
      return
    }
    setSaving(true)
    setFormError(null)
    try {
      if (editing) {
        await api.updateServerGroup(editing.id, {
          name: name.trim(),
          description: description.trim(),
        })
        toast(t('groups.updated', { name: name.trim() }), 'success')
      } else {
        await api.createServerGroup({ name: name.trim(), description: description.trim() })
        toast(t('groups.created', { name: name.trim() }), 'success')
      }
      setDialogOpen(false)
      void groupsQuery.refresh()
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
      await api.deleteServerGroup(deleting.id)
      toast(t('groups.deleted', { name: deleting.name }), 'success')
      setDeleting(null)
      void groupsQuery.refresh()
    } catch (caught) {
      setDeleteError(errorMessage(caught, t('common.error')))
    } finally {
      setDeleteLoading(false)
    }
  }

  const openMembers = async (group: ServerGroup) => {
    setMembersGroup(group)
    setMembersLoading(true)
    setMembersError(null)
    setMemberIds([])
    try {
      const list = await api.serverGroupServers(group.id)
      setMemberIds(list.map((server) => server.id))
    } catch (caught) {
      setMembersError(errorMessage(caught, t('common.error')))
    } finally {
      setMembersLoading(false)
    }
  }

  const handleSaveMembers = async () => {
    if (!membersGroup) return
    setMembersSaving(true)
    setMembersError(null)
    try {
      await api.setServerGroupMembers(membersGroup.id, memberIds)
      toast(t('groups.membersSaved', { name: membersGroup.name }), 'success')
      setMembersGroup(null)
      void groupsQuery.refresh()
    } catch (caught) {
      setMembersError(errorMessage(caught, t('common.error')))
    } finally {
      setMembersSaving(false)
    }
  }

  if (!canRead) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center text-sm text-slate-500 shadow-sm dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
        {t('authz.noPermission')}
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('groups.title')}
        description={t('groups.description')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => void groupsQuery.refresh()}
              loading={groupsQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('common.refresh')}
            </Button>
            {canWrite ? (
              <Button onClick={openCreate} icon={<PlusIcon className="h-4 w-4" />}>
                {t('groups.create')}
              </Button>
            ) : null}
          </>
        }
      />

      {!canWrite ? (
        <p className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {t('groups.readOnlyHint')}
        </p>
      ) : null}

      {groupsQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>
            {t('groups.loadFailed')}：{groupsQuery.error}
          </span>
          <Button size="sm" variant="secondary" onClick={() => void groupsQuery.refresh()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <FolderIcon className="h-4 w-4 text-indigo-500" />
            {t('groups.title')}
          </h2>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            {groups.length}
          </span>
        </div>

        {groupsQuery.loading && groupsQuery.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : groups.length === 0 ? (
          <EmptyState
            icon={<FolderIcon className="h-6 w-6" />}
            title={t('groups.emptyTitle')}
            description={t('groups.emptyDescription')}
            actionLabel={canWrite ? t('groups.create') : undefined}
            onAction={canWrite ? openCreate : undefined}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[860px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('common.name')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('groups.descriptionLabel')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('groups.members')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('groups.updatedAt')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {groups.map((group) => (
                  <tr key={group.id} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                    <td className="px-4 py-3 align-top">
                      <span className="font-medium text-slate-800 dark:text-slate-100">{group.name}</span>
                      <span className="mt-0.5 block font-mono text-[11px] text-slate-400 dark:text-slate-500">
                        {group.id}
                      </span>
                    </td>
                    <td className="max-w-[320px] px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {group.description || '—'}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <button
                        type="button"
                        onClick={() => void openMembers(group)}
                        className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:underline dark:text-indigo-400"
                      >
                        <UsersIcon className="h-3.5 w-3.5" />
                        {t('groups.memberCount', { count: group.member_count })}
                      </button>
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(group.updated_at)}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <div className="flex items-center justify-end gap-0.5">
                        <IconButton label={t('groups.manageMembers')} onClick={() => void openMembers(group)}>
                          <UsersIcon className="h-4 w-4" />
                        </IconButton>
                        {canWrite ? (
                          <>
                            <IconButton label={t('common.edit')} onClick={() => openEdit(group)}>
                              <PencilIcon className="h-4 w-4" />
                            </IconButton>
                            <IconButton
                              label={t('common.delete')}
                              tone="danger"
                              onClick={() => {
                                setDeleteError(null)
                                setDeleting(group)
                              }}
                            >
                              <TrashIcon className="h-4 w-4" />
                            </IconButton>
                          </>
                        ) : null}
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
        title={editing ? t('groups.edit') : t('groups.create')}
        description={t('groups.dialogHint')}
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
        <div className="space-y-3">
          <div>
            <label htmlFor="group-name" className={labelClass}>
              {t('groups.name')}
            </label>
            <input
              id="group-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={32}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="group-description" className={labelClass}>
              {t('groups.descriptionLabel')}
            </label>
            <input
              id="group-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              maxLength={200}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          {formError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {formError}
            </p>
          ) : null}
        </div>
      </Modal>

      <Modal
        open={membersGroup !== null}
        onClose={() => setMembersGroup(null)}
        title={t('groups.manageMembers')}
        description={membersGroup ? t('groups.membersHint', { name: membersGroup.name }) : undefined}
        footer={
          <>
            <Button variant="secondary" onClick={() => setMembersGroup(null)} disabled={membersSaving}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => void handleSaveMembers()}
              loading={membersSaving}
              disabled={!canWrite || membersLoading}
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        {membersLoading ? (
          <InlineLoading label={t('common.loading')} />
        ) : (
          <div className="space-y-2">
            <SearchMultiSelect
              options={serverOptions}
              value={memberIds}
              onChange={canWrite ? setMemberIds : () => {}}
              placeholder={t('groups.searchServers')}
              emptyText={t('groups.noServers')}
              listClassName="max-h-56"
            />
            <p className="text-[11px] text-slate-400 dark:text-slate-500">{t('groups.membersImpact')}</p>
            {membersError ? (
              <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                {membersError}
              </p>
            ) : null}
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={deleting !== null}
        title={t('groups.deleteTitle')}
        description={deleting ? deleting.name : undefined}
        confirmLabel={t('common.delete')}
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => setDeleting(null)}
        onConfirm={() => void handleDelete()}
      >
        <p>{t('groups.deleteImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}
