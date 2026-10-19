import { useEffect, useState, type FormEvent } from 'react'
import { api, type Server } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'

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
      setError('服务器名称不能为空')
      return
    }
    setSaving(true)
    setError(null)
    try {
      await api.updateServer(server.id, { name: trimmed })
      toast('服务器名称已更新', 'success')
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, '重命名失败，请稍后重试'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={server !== null}
      onClose={onClose}
      title="重命名服务器"
      description={server ? `当前名称：${server.name}` : undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button type="submit" form="rename-server-form" loading={saving}>
            保存
          </Button>
        </>
      }
    >
      <form id="rename-server-form" onSubmit={handleSubmit} className="space-y-3">
        <label htmlFor="server-name" className="block text-sm font-medium text-slate-700 dark:text-slate-200">
          服务器名称
        </label>
        <input
          id="server-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={64}
          autoFocus
          placeholder="例如：生产环境 · 东京 01"
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
      toast(`已删除服务器「${server.name}」`, 'success')
      onDeleted(server)
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, '删除失败，请稍后重试'))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Modal
      open={server !== null}
      onClose={onClose}
      title="删除服务器"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={deleting}>
            取消
          </Button>
          <Button variant="danger" onClick={handleDelete} loading={deleting}>
            确认删除
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-sm text-slate-600 dark:text-slate-300">
        <p>
          确定要删除服务器{' '}
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{server?.name}」
          </span>{' '}
          吗？该操作不可撤销。
        </p>
        <p className="text-xs text-slate-400 dark:text-slate-500">
          删除后 agent 将无法继续上报；如需重新接入，请生成新的接入令牌。
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
