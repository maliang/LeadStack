import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, type AppItem, type AppProvider, type BaotaSiteType } from '../lib/api'
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
  const [provider, setProvider] = useState<AppProvider>('builtin')
  const [siteName, setSiteName] = useState('')
  const [sitePort, setSitePort] = useState('')
  const [siteType, setSiteType] = useState<BaotaSiteType>('static')
  const [healthcheckPath, setHealthcheckPath] = useState('')
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
    setProvider(app?.provider === 'baota' ? 'baota' : 'builtin')
    setSiteName(app?.site_name ?? '')
    setSitePort(app?.site_port ? String(app.site_port) : '')
    setSiteType(app?.site_type === 'php' ? 'php' : 'static')
    setHealthcheckPath(app?.healthcheck_path ?? '')
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
    if (provider === 'baota') {
      if (!siteName.trim()) {
        setError(t('apps.errorSiteName'))
        return
      }
      const port = Number(sitePort)
      if (!/^\d+$/.test(sitePort.trim()) || !Number.isInteger(port) || port < 1 || port > 65535) {
        setError(t('apps.errorSitePort'))
        return
      }
      if (healthcheckPath.trim() !== '' && !healthcheckPath.trim().startsWith('/')) {
        setError(t('apps.errorHealthcheck'))
        return
      }
    }
    setSaving(true)
    setError(null)
    try {
      const payload = {
        name: name.trim(),
        slug: effectiveSlug,
        root: effectiveRoot.trim() || undefined,
        description: description.trim() || undefined,
        provider,
        site_name: provider === 'baota' ? siteName.trim() : undefined,
        site_port: provider === 'baota' ? Number(sitePort) : undefined,
        site_type: provider === 'baota' ? siteType : null,
        healthcheck_path:
          provider === 'baota' && healthcheckPath.trim() ? healthcheckPath.trim() : null,
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
      description={provider === 'baota' ? t('apps.baotaDeployHint') : t('s.0241')}
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

        {provider === 'baota' ? null : (
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
        )}

        <div>
          <span className={labelClass}>{t('apps.provider')}</span>
          <div className="mt-1.5 grid gap-2 sm:grid-cols-2">
            {(
              [
                { value: 'builtin', title: t('apps.providerBuiltin'), hint: t('apps.providerBuiltinHint') },
                { value: 'baota', title: t('apps.providerBaota'), hint: t('apps.providerBaotaHint') },
              ] as const
            ).map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => setProvider(option.value)}
                className={
                  'rounded-lg border px-3 py-2 text-left transition-colors ' +
                  (provider === option.value
                    ? 'border-indigo-500 bg-indigo-50/70 dark:border-indigo-400 dark:bg-indigo-500/10'
                    : 'border-slate-200 hover:border-slate-300 dark:border-slate-700 dark:hover:border-slate-600')
                }
              >
                <span className="block text-sm font-medium text-slate-800 dark:text-slate-100">
                  {option.title}
                </span>
                <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
                  {option.hint}
                </span>
              </button>
            ))}
          </div>

          {provider === 'baota' ? (
            <div className="mt-3 space-y-3 rounded-lg border border-amber-200 bg-amber-50/60 p-3 dark:border-amber-500/30 dark:bg-amber-500/10">
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label htmlFor="app-site-name" className={labelClass}>
                    {t('apps.siteName')}
                  </label>
                  <input
                    id="app-site-name"
                    value={siteName}
                    onChange={(event) => setSiteName(event.target.value)}
                    placeholder={effectiveSlug || 'order-service'}
                    className={inputClass + ' mt-1.5'}
                  />
                </div>
                <div>
                  <label htmlFor="app-site-port" className={labelClass}>
                    {t('apps.sitePort')}
                  </label>
                  <input
                    id="app-site-port"
                    value={sitePort}
                    onChange={(event) => setSitePort(event.target.value)}
                    inputMode="numeric"
                    placeholder="80"
                    className={inputClass + ' mt-1.5'}
                  />
                </div>
                <div>
                  <label htmlFor="app-site-type" className={labelClass}>
                    {t('apps.siteType')}
                  </label>
                  <select
                    id="app-site-type"
                    value={siteType}
                    onChange={(event) => setSiteType(event.target.value as BaotaSiteType)}
                    className={inputClass + ' mt-1.5'}
                  >
                    <option value="static">{t('apps.siteTypeStatic')}</option>
                    <option value="php">{t('apps.siteTypePhp')}</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="app-healthcheck" className={labelClass}>
                    {t('apps.healthcheckPath')}
                  </label>
                  <input
                    id="app-healthcheck"
                    value={healthcheckPath}
                    onChange={(event) => setHealthcheckPath(event.target.value)}
                    placeholder="/healthz"
                    className={inputClass + ' mt-1.5 font-mono text-xs'}
                  />
                </div>
              </div>
              <p className="text-xs leading-relaxed text-amber-900/90 dark:text-amber-200/90">
                {t('apps.baotaDeployHint')}
              </p>
            </div>
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
