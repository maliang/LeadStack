import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, type AppItem } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

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
      setError(t('s.0234'))
      return
    }
    if (!/^[a-z0-9-]{1,40}$/.test(effectiveSlug)) {
      setError(t('s.0235'))
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
        toast(t('s.0236', { '0': name.trim() }), 'success')
      } else {
        await api.createApp(payload)
        toast(t('s.0237', { '0': name.trim() }), 'success')
      }
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0238')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('s.0239') : t('s.0240')}
      description={t('s.0241')}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="app-form" loading={saving}>
            {editing ? t('s.0128') : t('s.0242')}
          </Button>
        </>
      }
    >
      <form id="app-form" onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="app-name" className={labelClass}>
              {t('s.0243')}
            </label>
            <input
              id="app-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0244')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="app-slug" className={labelClass}>
              {t('s.0245')}
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
            {t('s.0246')}
          </label>
          <input
            id="app-root"
            value={effectiveRoot}
            onChange={(event) => {
              setRootTouched(true)
              setRoot(event.target.value)
            }}
            placeholder={t('s.0247')}
            className={inputClass + ' mt-1.5 font-mono text-xs'}
          />
          <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
            {t('s.0248')} {effectiveSlug || '<slug>'}{t('s.0249')}
          </p>
          {!slugTouched && effectiveSlug === '' && name.trim() !== '' ? (
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
              {t('s.0250')}
            </p>
          ) : null}
        </div>

        <div>
          <label htmlFor="app-description" className={labelClass}>
            {t('s.0251')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
          </label>
          <textarea
            id="app-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            rows={2}
            maxLength={200}
            placeholder={t('s.0252')}
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
