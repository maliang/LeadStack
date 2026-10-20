import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, type AppItem } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

export function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
}

interface AppDialogProps {
  open: boolean
  app: AppItem | null
  onClose: () => void
  onSaved: () => void
}

/** 新建 / 编辑应用（slug 唯一，部署根目录默认 /opt/leadstack/apps/<slug>）。 */
export function AppDialog({ open, app, onClose, onSaved }: AppDialogProps) {
  const { toast } = useToast()
  const editing = app !== null

  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [root, setRoot] = useState('')
  const [rootTouched, setRootTouched] = useState(false)
  const [description, setDescription] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(app?.name ?? '')
    setSlug(app?.slug ?? '')
    setSlugTouched(app !== null)
    setRoot(app?.root ?? '')
    setRootTouched(false)
    setDescription(app?.description ?? '')
    setSaving(false)
    setError(null)
  }, [open, app])

  const effectiveSlug = useMemo(() => (slugTouched ? slug : slugify(name)), [slug, slugTouched, name])
  const effectiveRoot = useMemo(() => (rootTouched ? root : ''), [root, rootTouched])

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!name.trim()) {
      setError('请填写应用名称')
      return
    }
    if (!/^[a-z0-9-]{1,40}$/.test(effectiveSlug)) {
      setError('slug 需为 1-40 位小写字母、数字或短横线（例如 my-app）')
      return
    }
    setSaving(true)
    setError(null)
    try {
      const payload = {
        name: name.trim(),
        slug: effectiveSlug,
        root: effectiveRoot.trim() || undefined,
        description: description.trim() || undefined,
      }
      if (editing && app) {
        await api.updateApp(app.id, payload)
        toast(`应用「${name.trim()}」已更新`, 'success')
      } else {
        await api.createApp(payload)
        toast(`应用「${name.trim()}」已创建`, 'success')
      }
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, '保存应用失败'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? '编辑应用' : '新建应用'}
      description="应用是被部署的业务单元；每台机器上的部署目录为 <root>/releases/<release_id>，current 符号链接指向当前版本。"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button type="submit" form="app-form" loading={saving}>
            {editing ? '保存修改' : '创建应用'}
          </Button>
        </>
      }
    >
      <form id="app-form" onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="app-name" className={labelClass}>
              应用名称
            </label>
            <input
              id="app-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder="例如：订单服务"
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="app-slug" className={labelClass}>
              slug（唯一，用于目录名）
            </label>
            <input
              id="app-slug"
              value={effectiveSlug}
              onChange={(event) => {
                setSlugTouched(true)
                setSlug(slugify(event.target.value))
              }}
              maxLength={40}
              placeholder="order-service"
              className={inputClass + ' mt-1.5 font-mono text-xs'}
            />
          </div>
        </div>

        <div>
          <label htmlFor="app-root" className={labelClass}>
            部署根目录
          </label>
          <input
            id="app-root"
            value={effectiveRoot}
            onChange={(event) => {
              setRootTouched(true)
              setRoot(event.target.value)
            }}
            placeholder="留空 = 使用控制端 LEADSTACK_DEPLOY_ROOT（默认 /opt/leadstack/apps）"
            className={inputClass + ' mt-1.5 font-mono text-xs'}
          />
          <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
            留空使用控制端部署根目录；填了则作为基础目录，发布时会再拼 /&lt;slug&gt;（如 {effectiveSlug || '<slug>'}），
            并创建 releases/&lt;release_id&gt; 与 current 符号链接。
          </p>
          {!slugTouched && effectiveSlug === '' && name.trim() !== '' ? (
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
              中文名称无法自动生成 slug，请手动填写（小写字母、数字、短横线）。
            </p>
          ) : null}
        </div>

        <div>
          <label htmlFor="app-description" className={labelClass}>
            描述 <span className="font-normal text-slate-400">（可选）</span>
          </label>
          <textarea
            id="app-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            rows={2}
            maxLength={200}
            placeholder="这个应用部署的是什么，方便同事识别"
            className={inputClass + ' mt-1.5'}
          />
        </div>

        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </form>
    </Modal>
  )
}
