import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  api,
  type AppItem,
  type AppProvider,
  type BaotaSiteType,
} from '../lib/api'
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

/** 接管向导回填时使用的预置值。 */
export interface AppPreset {
  name?: string
  root?: string
  deploy_mode?: 'release' | 'inplace' | 'script'
  target_dir?: string
  restart_cmd?: string
  healthcheck_port?: number
}

interface AppDialogProps {
  open: boolean
  app: AppItem | null
  onClose: () => void
  onSaved: () => void
  preset?: AppPreset | null
}

/** env_json（JSON 对象字符串）→ 每行 KEY=VALUE，便于手工编辑。 */
function envJsonToText(envJson: string | null): string {
  if (!envJson) return ''
  try {
    const parsed = JSON.parse(envJson) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return Object.entries(parsed as Record<string, unknown>)
        .map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}`)
        .join('\n')
    }
  } catch {
    /* 非法 JSON 时原样展示，交由提交校验兜底 */
  }
  return envJson
}

/** 每行 KEY=VALUE → JSON 对象字符串；错误返回行号，便于就近展示。 */
function parseEnvText(text: string): { json: string | null; errorLine: number | null } {
  const entries: Record<string, string> = {}
  const lines = text.split('\n')
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].trim()
    if (line === '' || line.startsWith('#')) continue
    const separator = line.indexOf('=')
    if (separator <= 0) return { json: null, errorLine: index + 1 }
    const key = line.slice(0, separator).trim()
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) return { json: null, errorLine: index + 1 }
    entries[key] = line.slice(separator + 1)
  }
  return {
    json: Object.keys(entries).length > 0 ? JSON.stringify(entries) : null,
    errorLine: null,
  }
}

/** 新建 / 编辑应用：部署方式、宝塔站点参数、部署参数（多版本 / 接管既有目录）。 */
export function AppDialog({ open, app, onClose, onSaved, preset }: AppDialogProps) {
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
  const [deployMode, setDeployMode] = useState<'release' | 'inplace' | 'script'>('release')
  const [targetDir, setTargetDir] = useState('')
  const [keepPaths, setKeepPaths] = useState('')
  const [restartCmd, setRestartCmd] = useState('')
  const [healthcheckPort, setHealthcheckPort] = useState('')
  const [initScript, setInitScript] = useState('')
  const [rollbackScript, setRollbackScript] = useState('')
  const [envText, setEnvText] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    const fromPreset = !app && preset ? preset : null
    setName(app?.name ?? fromPreset?.name ?? '')
    setSlug(app?.slug ?? slugify(fromPreset?.name ?? ''))
    setSlugTouched(app !== null)
    setRoot(app?.root ?? fromPreset?.root ?? '')
    setRootTouched(false)
    setDescription(app?.description ?? '')
    setProvider(app?.provider === 'baota' ? 'baota' : 'builtin')
    setSiteName(app?.site_name ?? '')
    setSitePort(app?.site_port ? String(app.site_port) : '')
    setSiteType(app?.site_type === 'php' ? 'php' : 'static')
    setHealthcheckPath(app?.healthcheck_path ?? '')
    setDeployMode(
      app?.deploy_mode === 'inplace' || app?.deploy_mode === 'script'
        ? app.deploy_mode
        : fromPreset?.deploy_mode === 'inplace'
          ? 'inplace'
          : 'release',
    )
    setTargetDir(app?.target_dir ?? fromPreset?.target_dir ?? '')
    setKeepPaths(app?.keep_paths ?? '')
    setRestartCmd(app?.restart_cmd ?? fromPreset?.restart_cmd ?? '')
    setHealthcheckPort(
      app?.healthcheck_port != null
        ? String(app.healthcheck_port)
        : fromPreset?.healthcheck_port != null
          ? String(fromPreset.healthcheck_port)
          : '',
    )
    setInitScript(app?.init_script ?? '')
    setRollbackScript(app?.rollback_script ?? '')
    setEnvText(envJsonToText(app?.env_json ?? null))
    setSaving(false)
    setError(null)
  }, [open, app, preset])

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
    if (deployMode === 'inplace' && !targetDir.trim().startsWith('/')) {
      setError(t('apps.errorTargetDir'))
      return
    }
    let parsedPort: number | null = null
    if (healthcheckPort.trim() !== '') {
      const port = Number(healthcheckPort)
      if (
        !/^\d+$/.test(healthcheckPort.trim()) ||
        !Number.isInteger(port) ||
        port < 0 ||
        port > 65535
      ) {
        setError(t('apps.errorHealthcheckPort'))
        return
      }
      parsedPort = port
    }
    const parsedEnv = parseEnvText(envText)
    if (parsedEnv.errorLine !== null) {
      setError(t('apps.errorEnvLine', { line: parsedEnv.errorLine }))
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
        provider,
        site_name: provider === 'baota' ? siteName.trim() : undefined,
        site_port: provider === 'baota' ? Number(sitePort) : undefined,
        site_type: provider === 'baota' ? siteType : null,
        healthcheck_path:
          provider === 'baota' && healthcheckPath.trim() ? healthcheckPath.trim() : null,
        deploy_mode: deployMode,
        target_dir: deployMode === 'inplace' ? targetDir.trim() : null,
        keep_paths: deployMode === 'inplace' ? keepPaths.trim() || null : null,
        rollback_script: deployMode === 'script' ? rollbackScript : null,
        restart_cmd: restartCmd.trim() || null,
        healthcheck_port: parsedPort,
        init_script: initScript.trim() || null,
        env_json: parsedEnv.json,
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
      size="lg"
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
            {deployMode === 'inplace'
              ? t('apps.rootHintInplace')
              : `${t('s.0248')} ${effectiveSlug || '<slug>'}${t('s.0249')}`}
          </p>
          {!slugTouched && effectiveSlug === '' && name.trim() !== '' ? (
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">{t('s.0250')}</p>
          ) : null}
        </div>

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

        {/* 部署参数 */}
        <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
          <p className="text-sm font-semibold text-slate-800 dark:text-slate-100">
            {t('apps.deploySettings')}
          </p>
          <div className="mt-2 grid gap-2 sm:grid-cols-2">
            {(
              [
                { value: 'release', title: t('apps.modeRelease'), hint: t('apps.modeReleaseHint') },
                { value: 'inplace', title: t('apps.modeInplace'), hint: t('apps.modeInplaceHint') },
                { value: 'script', title: t('apps.modeScript'), hint: t('apps.modeScriptHint') },
              ] as const
            ).map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => setDeployMode(option.value)}
                className={
                  'rounded-lg border px-3 py-2 text-left transition-colors ' +
                  (deployMode === option.value
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

          {deployMode === 'inplace' || deployMode === 'script' ? (
            <div className="mt-3 grid gap-3 sm:grid-cols-2">
              <div className="sm:col-span-2">
                <label htmlFor="app-target-dir" className={labelClass}>
                  {deployMode === 'script' ? t('apps.workDir') : t('apps.targetDir')}
                </label>
                <input
                  id="app-target-dir"
                  value={targetDir}
                  onChange={(event) => setTargetDir(event.target.value)}
                  placeholder={deployMode === 'script' ? '/www/wwwroot/tuoluojiang' : '/opt/nengtan-hub'}
                  className={inputClass + ' mt-1.5 font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {deployMode === 'script' ? t('apps.workDirHint') : t('apps.targetDirHint')}
                </p>
              </div>
              <div className={deployMode === 'script' ? 'hidden' : 'sm:col-span-2'}>
                <label htmlFor="app-keep-paths" className={labelClass}>
                  {t('apps.keepPaths')}
                </label>
                <input
                  id="app-keep-paths"
                  value={keepPaths}
                  onChange={(event) => setKeepPaths(event.target.value)}
                  placeholder="data/, uploads/, storage/logs/*"
                  className={inputClass + ' mt-1.5 font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('apps.keepPathsHint')}
                </p>
              </div>
            </div>
          ) : null}

          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <div className={deployMode === 'script' ? 'hidden' : undefined}>
              <label htmlFor="app-restart-cmd" className={labelClass}>
                {t('apps.restartCmd')}
              </label>
              <input
                id="app-restart-cmd"
                value={restartCmd}
                onChange={(event) => setRestartCmd(event.target.value)}
                placeholder="systemctl restart nengtan-hub"
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('apps.restartCmdHint')}
              </p>
            </div>
            <div>
              <label htmlFor="app-healthcheck-port" className={labelClass}>
                {t('apps.healthcheckPort')}
              </label>
              <input
                id="app-healthcheck-port"
                value={healthcheckPort}
                onChange={(event) => setHealthcheckPort(event.target.value)}
                inputMode="numeric"
                placeholder="8080"
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('apps.healthcheckPortHint')}
              </p>
            </div>
          </div>

          {deployMode === 'inplace' ? (
            <p className="mt-2 text-xs leading-relaxed text-amber-700 dark:text-amber-300">
              {t('apps.inplaceHint')}
            </p>
          ) : null}
          {deployMode === 'script' ? (
            <p className="mt-2 text-xs leading-relaxed text-amber-700 dark:text-amber-300">
              {t('apps.scriptHint')}
            </p>
          ) : null}

          <details className="mt-3 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-950/60">
            <summary className="cursor-pointer text-xs font-medium text-slate-600 dark:text-slate-300">
              {t('apps.advanced')}
            </summary>
            <div className="mt-2 space-y-3">
              <div>
                <label htmlFor="app-init-script" className={labelClass}>
                  {t('apps.initScript')}
                </label>
                <textarea
                  id="app-init-script"
                  value={initScript}
                  onChange={(event) => setInitScript(event.target.value)}
                  rows={4}
                  spellCheck={false}
                  placeholder={'#!/bin/bash\nset -e\ncp .env.example .env'}
                  className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('apps.initScriptHint')}
                </p>
              </div>
              {deployMode === 'script' ? (
                <div>
                  <label htmlFor="app-rollback-script" className={labelClass}>
                    {t('apps.rollbackScript')}
                  </label>
                  <textarea
                    id="app-rollback-script"
                    value={rollbackScript}
                    onChange={(event) => setRollbackScript(event.target.value)}
                    rows={4}
                    spellCheck={false}
                    placeholder={'#!/bin/bash\nset -e\ngit reset --hard HEAD@{1}\nbash start.sh restart'}
                    className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
                  />
                  <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                    {t('apps.rollbackScriptHint')}
                  </p>
                </div>
              ) : null}
              <div>
                <label htmlFor="app-env" className={labelClass}>
                  {t('apps.envVars')}
                </label>
                <textarea
                  id="app-env"
                  value={envText}
                  onChange={(event) => setEnvText(event.target.value)}
                  rows={4}
                  spellCheck={false}
                  placeholder={'DB_HOST=127.0.0.1\nDB_PORT=3306'}
                  className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('apps.envHint')}
                </p>
              </div>
            </div>
          </details>
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
