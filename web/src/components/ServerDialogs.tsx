import { useEffect, useState, type FormEvent } from 'react'
import { api, type Server } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

interface RenameDialogProps {
  server: Server | null
  onClose: () => void
  onSaved: () => void
}

export function RenameServerDialog({ server, onClose, onSaved }: RenameDialogProps) {
  const { toast } = useToast()
  const [name, setName] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!server) return
    setName(server.name)
    setError(null)
    setSaving(false)
  }, [server])

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!server) return
    const trimmed = name.trim()
    if (!trimmed) {
      setError(t('s.0504'))
      return
    }
    setSaving(true)
    setError(null)
    try {
      await api.updateServer(server.id, { name: trimmed })
      toast(t('s.0505'), 'success')
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0506')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={server !== null}
      onClose={onClose}
      title={t('s.0507')}
      description={server ? t('s.0508', { '0': server.name }) : undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="rename-server-form" loading={saving}>
            {t('s.0509')}
          </Button>
        </>
      }
    >
      <form id="rename-server-form" onSubmit={handleSubmit} className="space-y-3">
        <label htmlFor="server-name" className="block text-sm font-medium text-slate-700 dark:text-slate-200">
          {t('s.0064')}
        </label>
        <input
          id="server-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={64}
          autoFocus
          placeholder={t('s.0510')}
          className="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
        />
        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
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
        <p className="text-xs text-slate-400 dark:text-slate-500">
          {t('s.0516')}
        </p>
        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
