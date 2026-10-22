import { useEffect, useState, type FormEvent } from 'react'
import { api, type Credential, type Server, type ServerGroup } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { validateServerHost, parsePort } from '../lib/network'
import { formatLatency } from '../lib/format'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { AlertTriangleIcon } from './Icons'
import { SearchMultiSelect } from './SearchMultiSelect'
import { t } from '../i18n'

interface EditServerDialogProps {
  server: Server | null
  onClose: () => void
  onSaved: () => void
}

interface FieldErrors {
  name?: string
  host?: string
  port?: string
  form?: string
}

/** 编辑服务器：名称、主机地址、端口；改地址后自动重新测试连通性。 */
export function EditServerDialog({ server, onClose, onSaved }: EditServerDialogProps) {
  const { toast } = useToast()
  const [name, setName] = useState('')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('')
  const [groupIds, setGroupIds] = useState<string[]>([])
  const [serverGroups, setServerGroups] = useState<ServerGroup[]>([])
  const [panelCredentials, setPanelCredentials] = useState<Credential[]>([])
  const [panelCredentialId, setPanelCredentialId] = useState('')
  const [saving, setSaving] = useState(false)
  const [errors, setErrors] = useState<FieldErrors>({})

  useEffect(() => {
    if (!server) return
    setName(server.name)
    setHost(server.host)
    setPort(server.port === null ? '' : String(server.port))
    setGroupIds(server.group_ids)
    setPanelCredentialId(server.panel_credential_id ?? '')
    setErrors({})
    setSaving(false)
  }, [server])

  // 授权分组候选：每次打开时加载（避免长期缓存陈旧）
  useEffect(() => {
    if (!server) return
    let cancelled = false
    Promise.allSettled([api.serverGroups(), api.credentials()]).then(([groupResult, credentialResult]) => {
      if (cancelled) return
      if (groupResult.status === 'fulfilled') setServerGroups(groupResult.value)
      if (credentialResult.status === 'fulfilled') {
        setPanelCredentials(credentialResult.value.filter((credential) => credential.kind === 'baota'))
      }
    })
    return () => {
      cancelled = true
    }
  }, [server])

  const addressChanged =
    server !== null &&
    (host.trim() !== server.host || (parsePort(port) ?? null) !== server.port)

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!server) return
    const next: FieldErrors = {}
    const trimmedName = name.trim()
    if (!trimmedName) next.name = 'server.edit.errorNameRequired'
    const hostError = validateServerHost(host)
    if (hostError) next.host = hostError
    const parsedPort = parsePort(port)
    if (parsedPort === null) next.port = 'network.error.portRange'
    setErrors(next)
    if (Object.keys(next).length > 0) return

    setSaving(true)
    try {
      await api.updateServer(server.id, {
        name: trimmedName,
        host: host.trim(),
        port: parsedPort ?? 22,
        group_ids: groupIds,
        panel_credential_id: panelCredentialId,
      })
      toast(t('server.edit.saved'), 'success')
      onSaved()
      onClose()
      if (addressChanged) {
        toast(t('server.edit.retesting'), 'info')
        void (async () => {
          try {
            const result = await api.testConnection(server.id)
            if (result.ok) {
              toast(
                t('server.edit.testOk', {
                  latency: formatLatency(result.latency_ms),
                }),
                'success',
              )
            } else {
              toast(
                t('server.edit.testFailed', { error: result.error ?? t('server.edit.noReason') }),
                'error',
              )
            }
          } catch (caught) {
            toast(errorMessage(caught, t('server.edit.testFailedGeneric')), 'error')
          }
        })()
      }
    } catch (caught) {
      setErrors({ form: errorMessage(caught, t('server.edit.saveFailed')) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={server !== null}
      onClose={onClose}
      title={t('server.edit.title')}
      description={server ? t('server.edit.description', { name: server.name }) : undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="edit-server-form" loading={saving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <form id="edit-server-form" onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label
            htmlFor="server-name"
            className="block text-sm font-medium text-slate-700 dark:text-slate-200"
          >
            {t('server.edit.name')}
          </label>
          <input
            id="server-name"
            value={name}
            onChange={(event) => {
              setName(event.target.value)
              if (errors.name) setErrors({ ...errors, name: undefined })
            }}
            maxLength={64}
            autoFocus
            placeholder={t('s.0510')}
            className={
              'mt-1 w-full rounded-md border bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:ring-2 dark:bg-slate-950 dark:text-slate-100 ' +
              (errors.name
                ? 'border-rose-400 focus:border-rose-500 focus:ring-rose-500/20 dark:border-rose-500'
                : 'border-slate-300 focus:border-indigo-500 focus:ring-indigo-500/20 dark:border-slate-700')
            }
          />
          {errors.name ? (
            <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.name)}</p>
          ) : null}
        </div>

        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_140px]">
          <div>
            <label
              htmlFor="server-host"
              className="block text-sm font-medium text-slate-700 dark:text-slate-200"
            >
              {t('server.edit.host')}
            </label>
            <input
              id="server-host"
              value={host}
              onChange={(event) => {
                setHost(event.target.value)
                if (errors.host) setErrors({ ...errors, host: undefined })
              }}
              placeholder="10.0.0.12"
              className={
                'mt-1 w-full rounded-md border bg-white px-3 py-2 font-mono text-xs text-slate-900 shadow-sm outline-none transition focus:ring-2 dark:bg-slate-950 dark:text-slate-100 ' +
                (errors.host
                  ? 'border-rose-400 focus:border-rose-500 focus:ring-rose-500/20 dark:border-rose-500'
                  : 'border-slate-300 focus:border-indigo-500 focus:ring-indigo-500/20 dark:border-slate-700')
              }
            />
            {errors.host ? (
              <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.host)}</p>
            ) : null}
          </div>
          <div>
            <label
              htmlFor="server-port"
              className="block text-sm font-medium text-slate-700 dark:text-slate-200"
            >
              {t('server.edit.port')}
            </label>
            <input
              id="server-port"
              value={port}
              onChange={(event) => {
                setPort(event.target.value)
                if (errors.port) setErrors({ ...errors, port: undefined })
              }}
              inputMode="numeric"
              className={
                'mt-1 w-full rounded-md border bg-white px-3 py-2 font-mono text-xs text-slate-900 shadow-sm outline-none transition focus:ring-2 dark:bg-slate-950 dark:text-slate-100 ' +
                (errors.port
                  ? 'border-rose-400 focus:border-rose-500 focus:ring-rose-500/20 dark:border-rose-500'
                  : 'border-slate-300 focus:border-indigo-500 focus:ring-indigo-500/20 dark:border-slate-700')
              }
            />
            {errors.port ? (
              <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.port)}</p>
            ) : null}
          </div>
        </div>

        <div>
          <label className="block text-sm font-medium text-slate-700 dark:text-slate-200">
            {t('groups.authorizedGroups')}
          </label>
          <p className="mt-0.5 text-[11px] text-slate-400 dark:text-slate-500">
            {t('groups.authorizedGroupsHint')}
          </p>
          <div className="mt-1.5">
            <SearchMultiSelect
              options={serverGroups.map((group) => ({ id: group.id, label: group.name }))}
              value={groupIds}
              onChange={setGroupIds}
              placeholder={t('groups.searchGroups')}
              emptyText={t('groups.noGroups')}
            />
          </div>
        </div>

        <div>
          <label htmlFor="server-panel-credential" className="block text-sm font-medium text-slate-700 dark:text-slate-200">
            {t('server.panelCredential')}
          </label>
          <select
            id="server-panel-credential"
            value={panelCredentialId}
            onChange={(event) => setPanelCredentialId(event.target.value)}
            className="mt-1 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          >
            <option value="">{t('server.panelCredentialNone')}</option>
            {panelCredentials.map((credential) => (
              <option key={credential.id} value={credential.id}>
                {credential.name}
                {credential.panel_url ? `（${credential.panel_url}）` : ''}
              </option>
            ))}
          </select>
          <p className="mt-1 text-[11px] text-slate-400 dark:text-slate-500">
            {t('server.panelCredentialHint')}
          </p>
        </div>

        {addressChanged ? (
          <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 dark:border-amber-500/30 dark:bg-amber-500/10">
            <p className="flex items-center gap-1.5 text-xs font-medium text-amber-900 dark:text-amber-200">
              <AlertTriangleIcon className="h-3.5 w-3.5" />
              {t('server.edit.addressWarning')}
            </p>
            <p className="mt-0.5 text-[11px] leading-relaxed text-amber-800/90 dark:text-amber-200/80">
              {t('server.edit.addressWarningHint')}
            </p>
          </div>
        ) : null}

        {errors.form ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {errors.form}
          </p>
        ) : null}
      </form>
    </Modal>
  )
}

interface DeleteDialogProps {
  server: Server | null
  onClose: () => void
  onDeleted: (server: Server) => void
}

export function DeleteServerDialog({ server, onClose, onDeleted }: DeleteDialogProps) {
  const { toast } = useToast()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!server) return
    setError(null)
    setDeleting(false)
  }, [server])

  const handleDelete = async () => {
    if (!server) return
    setDeleting(true)
    setError(null)
    try {
      await api.deleteServer(server.id)
      toast(t('s.0511', { '0': server.name }), 'success')
      onDeleted(server)
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0512')))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Modal
      open={server !== null}
      onClose={onClose}
      title={t('s.0513')}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={deleting}>
            {t('s.0024')}
          </Button>
          <Button variant="danger" onClick={handleDelete} loading={deleting}>
            {t('s.0295')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-sm text-slate-600 dark:text-slate-300">
        <p>
          {t('s.0514')}{' '}
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{server?.name}」
          </span>{' '}
          {t('s.0515')}
        </p>
        <p className="text-xs text-slate-400 dark:text-slate-500">{t('s.0516')}</p>
        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
