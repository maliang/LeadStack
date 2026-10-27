import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  api,
  ApiError,
  getToken,
  normalizeApp,
  type AppItem,
  type AppProvider,
  type BaotaSiteType,
  type CreateAppPayload,
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
  keep_paths?: string
  healthcheck_path?: string
  provider?: string
  site_type?: string
  site_port?: number
  gitDetected?: boolean
  /** 接管体检建议的脚本内容（script 模式），仅展示提示，不落库 */
  scriptTemplate?: string
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

/** 编辑态详情：api.ts 的归一化会丢掉 redacted / env_keys，这里把它们一起带出来。 */
interface AppDetail {
  app: AppItem
  /** true = 调用者看不到 env_json / init_script / restart_cmd 的值（后端已收敛为 null） */
  redacted: boolean
  /** env_json 的键名（字母序，最多 64 个） */
  envKeys: string[]
  envKeysTruncated: boolean
}

/** 服务端统一信封：成功 {"data":...}，失败 {"error":{"code","message","request_id"}}。 */
interface AppEnvelope {
  data?: unknown
  error?: { code?: string; message?: string; request_id?: string }
}

/**
 * 从错误信封/响应头取 request_id（与 api.ts 内部的 pickRequestId 同口径，那个函数未导出）。
 * 拿到后交给 ApiError，errorMessage()（lib/errors.ts）就会在提示后自动追加「请求编号 xxx」，
 * 与其它走 request() 的接口保持一致 —— 用户报障时能直接把这串 id 给后端定位日志。
 */
function requestIdOf(envelope: AppEnvelope | null, response: Response): string | null {
  const fromBody = envelope?.error?.request_id
  const candidate =
    typeof fromBody === 'string' && fromBody.trim() !== ''
      ? fromBody
      : (response.headers.get('X-Request-Id') ?? '')
  const trimmed = candidate.trim()
  return trimmed === '' ? null : trimmed
}

/**
 * GET /api/v1/apps/{id} 的详情（原始响应 + 归一化结果）。
 *
 * 为什么不直接用 api.app(id)：api.ts 是契约层（T-08 定稿、本任务禁止改动），它的
 * normalizeApp 只挑已知字段，服务端返回的 `redacted` / `env_keys` 会被丢掉；而「受限角色
 * 只显示键名」与「未改动的敏感字段不回传」必须靠这两个字段才做得对。请求本身仍走同一套
 * /api/v1 前缀与 Bearer 令牌，模型部分复用已导出的 normalizeApp，不另写一套归一化。
 *
 * 错误契约与 request() 对齐：失败时抛 ApiError（带 status / code / request_id），
 * 这样 errorMessage() 会自动补「请求编号 xxx」；401 则借统一层的请求触发登录失效处理
 * （见下方 void api.apps()），不在组件里另写一套跳登录逻辑。
 *
 * 另外 SPEC R2：列表（GET /apps）在受限角色下对 env_json / init_script / restart_cmd
 * 一律回 null，编辑时直接复用列表数据会把库里已有的值清空，所以编辑态必须再取一次详情。
 */
async function fetchAppDetail(id: string): Promise<AppDetail> {
  const token = getToken()
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  const response = await fetch(`/api/v1/apps/${encodeURIComponent(id)}`, { headers })
  const text = await response.text()
  let envelope: AppEnvelope | null = null
  if (text) {
    try {
      envelope = JSON.parse(text) as AppEnvelope
    } catch {
      envelope = null
    }
  }
  if (!response.ok) {
    // 抛 ApiError（带 request_id）而不是普通 Error：errorMessage() 只在 ApiError 上补请求编号。
    const apiError = new ApiError(
      response.status,
      envelope?.error?.code ?? 'http_error',
      envelope?.error?.message ?? t('s.0579', { '0': response.status }),
      null,
      requestIdOf(envelope, response),
    )
    if (response.status === 401) {
      // 会话失效：借一次统一层的请求触发 unauthorizedHandler（clearToken + 回登录页），
      // 这里不重复实现 401 处理；触发失败也不影响本次错误照常抛出。
      void api.apps().catch(() => {})
    }
    throw apiError
  }
  const payload = envelope?.data
  const data = payload !== null && typeof payload === 'object' ? (payload as Record<string, unknown>) : {}
  return {
    app: normalizeApp(data),
    redacted: data.redacted === true,
    envKeys: Array.isArray(data.env_keys)
      ? data.env_keys.filter((key): key is string => typeof key === 'string')
      : [],
    envKeysTruncated: data.env_keys_truncated === true,
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
  /** 敏感字段已被后端收敛（调用者无权限读值）→ 只展示 env_keys */
  const [redacted, setRedacted] = useState(false)
  const [envKeys, setEnvKeys] = useState<string[]>([])
  const [envKeysTruncated, setEnvKeysTruncated] = useState(false)
  /** 详情还没取回来时先禁用保存，避免用列表的精简值提交（SPEC R2） */
  const [detailLoading, setDetailLoading] = useState(false)
  /**
   * 敏感字段是否被用户改过（diff 提交，PLAN 选型 8）。
   * 用 ref 而不是 state：只需要在提交时读一次，且详情回填的异步回调里要看到最新值。
   */
  const envDirty = useRef(false)
  const initScriptDirty = useRef(false)
  const restartCmdDirty = useRef(false)
  /**
   * 详情请求序号：连续切换应用（或快速关掉再打开）时旧请求的回包可能后到，
   * 用单调递增序号丢弃过期回包 —— cancelled 只覆盖「effect 清理」，序号额外覆盖
   * 「清理前已发出、清理后仍返回」的多次请求，避免旧应用的敏感字段覆盖新应用。
   */
  const detailSeq = useRef(0)

  useEffect(() => {
    if (!open) return
    const fromPreset = !app && preset ? preset : null
    setName(app?.name ?? fromPreset?.name ?? '')
    setSlug(app?.slug ?? slugify(fromPreset?.name ?? ''))
    setSlugTouched(app !== null)
    setRoot(app?.root ?? fromPreset?.root ?? '')
    setRootTouched(false)
    setDescription(app?.description ?? '')
    setProvider(
      app?.provider === 'baota' || fromPreset?.provider === 'baota' ? 'baota' : 'builtin',
    )
    setSiteName(app?.site_name ?? '')
    setSitePort(
      app?.site_port
        ? String(app.site_port)
        : fromPreset?.site_port
          ? String(fromPreset.site_port)
          : '',
    )
    setSiteType(app?.site_type === 'php' || fromPreset?.site_type === 'php' ? 'php' : 'static')
    setHealthcheckPath(app?.healthcheck_path ?? fromPreset?.healthcheck_path ?? '')
    const presetMode = app?.deploy_mode ?? fromPreset?.deploy_mode
    setDeployMode(
      presetMode === 'inplace' ? 'inplace' : presetMode === 'script' ? 'script' : 'release',
    )
    setTargetDir(app?.target_dir ?? fromPreset?.target_dir ?? '')
    setKeepPaths(app?.keep_paths ?? fromPreset?.keep_paths ?? '')
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
    setRedacted(false)
    setEnvKeys([])
    setEnvKeysTruncated(false)
    setDetailLoading(false)
    envDirty.current = false
    initScriptDirty.current = false
    restartCmdDirty.current = false
  }, [open, app, preset])

  // 编辑态先取详情再渲染：列表 payload 在受限角色下不含 env_json / init_script / restart_cmd
  // （SPEC R2），直接复用列表值会在保存时把它们清空；同时拿 redacted / env_keys 决定显示方式。
  useEffect(() => {
    if (!open || !app) return
    let cancelled = false
    const seq = ++detailSeq.current
    const stale = () => cancelled || seq !== detailSeq.current
    setDetailLoading(true)
    fetchAppDetail(app.id)
      .then((detail) => {
        if (stale()) return
        setRedacted(detail.redacted)
        setEnvKeys(detail.envKeys)
        setEnvKeysTruncated(detail.envKeysTruncated)
        // 只回填用户还没动过的字段，避免覆盖刚输入的内容
        if (!envDirty.current) setEnvText(envJsonToText(detail.app.env_json))
        if (!initScriptDirty.current) setInitScript(detail.app.init_script ?? '')
        if (!restartCmdDirty.current) setRestartCmd(detail.app.restart_cmd ?? '')
      })
      .catch((caught: unknown) => {
        if (stale()) return
        // 详情取不到时保留列表值：敏感字段只要没被改动就不会提交，不会清空库里的值。
        setError(errorMessage(caught, t('s.0238')))
      })
      .finally(() => {
        if (!stale()) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
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
    }
    // 健康检查路径对所有部署模式都生效（后端两条通道共用），所以校验也提到 provider 之外。
    if (healthcheckPath.trim() !== '' && !healthcheckPath.trim().startsWith('/')) {
      setError(t('apps.errorHealthcheck'))
      return
    }
    if ((deployMode === 'inplace' || deployMode === 'script') && !targetDir.trim().startsWith('/')) {
      setError(deployMode === 'script' ? t('apps.errorWorkdir') : t('apps.errorTargetDir'))
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
      const payload: CreateAppPayload = {
        name: name.trim(),
        slug: effectiveSlug,
        root: effectiveRoot.trim() || undefined,
        description: description.trim() || undefined,
        provider,
        site_name: provider === 'baota' ? siteName.trim() : undefined,
        site_port: provider === 'baota' ? Number(sitePort) : undefined,
        site_type: provider === 'baota' ? siteType : null,
        // 留空 = 未配置：显式空串才会把库里的值清成 NULL（服务端 PATCH：缺省=不变、""=清空）。
        // 原来只有 baota/script 才提交真实值，release/inplace 一律置 null，路径永远存不进去（A1/A2）。
        healthcheck_path: healthcheckPath.trim(),
        deploy_mode: deployMode,
        // script 模式的工作目录同样是 target_dir（后端 script 分支会回落到 root），不能丢。
        target_dir: deployMode === 'release' ? null : targetDir.trim(),
        keep_paths: deployMode === 'inplace' ? keepPaths.trim() || null : null,
        rollback_script: deployMode === 'script' ? rollbackScript : null,
        // 留空且原本配了端口 → 0 表示清除（后端 healthcheck_port：0 有语义，null 只是「不改」）。
        healthcheck_port: parsedPort ?? (editing && app?.healthcheck_port != null ? 0 : null),
      }
      // 敏感字段走 diff 提交：编辑态只提交用户真正改过的字段。
      // 收敛后（A8）受限角色的 env_json/init_script/restart_cmd 恒为 null，全量提交会把库里的值清空。
      if (!editing || envDirty.current) payload.env_json = parsedEnv.json ?? ''
      if (!editing || initScriptDirty.current) payload.init_script = initScript.trim()
      if (!editing || restartCmdDirty.current) payload.restart_cmd = restartCmd.trim()
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
          <Button type="submit" form="app-form" loading={saving} disabled={detailLoading}>
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
            {deployMode !== 'release'
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
          {preset?.gitDetected ? (
            <p className="mt-1.5 rounded-md border border-sky-200 bg-sky-50 px-2.5 py-1.5 text-xs leading-relaxed text-sky-900 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-200">
              {t('apps.gitDetectedHint')}
            </p>
          ) : null}
          {preset?.scriptTemplate ? (
            <div className="mt-2 rounded-md border border-indigo-200 bg-indigo-50/60 px-2.5 py-1.5 dark:border-indigo-500/30 dark:bg-indigo-500/10">
              <p className="text-xs font-medium text-indigo-900 dark:text-indigo-200">
                {t('apps.suggestedScript')}
              </p>
              <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-950 p-2 font-mono text-[11px] text-slate-100">
                {preset.scriptTemplate}
              </pre>
              <p className="mt-1 text-[11px] text-indigo-800/80 dark:text-indigo-300/80">
                {t('apps.suggestedScriptHint')}
              </p>
            </div>
          ) : null}
          <div className="mt-2 grid gap-2 sm:grid-cols-3">
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
                onChange={(event) => {
                  restartCmdDirty.current = true
                  setRestartCmd(event.target.value)
                }}
                placeholder="systemctl restart nengtan-hub"
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {redacted ? t('apps.envKeysOnly') : t('apps.restartCmdHint')}
              </p>
            </div>
            {/* 健康检查路径与端口对 release / inplace / script 一视同仁（后端两条通道共用，A1） */}
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
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('apps.healthcheckPathHint')}
              </p>
              {deployMode === 'script' ? (
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('apps.scriptHealthcheckHint')}
                </p>
              ) : null}
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
                  onChange={(event) => {
                    initScriptDirty.current = true
                    setInitScript(event.target.value)
                  }}
                  rows={4}
                  spellCheck={false}
                  placeholder={'#!/bin/bash\nset -e\ncp .env.example .env'}
                  className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {redacted ? t('apps.envKeysOnly') : t('apps.initScriptHint')}
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
                {redacted ? (
                  // 受限角色读不到环境变量的值（后端收敛为 null + env_keys），只展示键名，
                  // 且这些字段只要没被改动就不会被提交，保存不会覆盖原有值。
                  <div
                    id="app-env"
                    className="mt-1.5 whitespace-pre-wrap break-all rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs leading-relaxed text-slate-600 shadow-sm dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
                  >
                    {envKeys.length > 0 ? envKeys.join('\n') : '—'}
                    {envKeys.length > 0 && envKeysTruncated ? '\n…' : ''}
                  </div>
                ) : (
                  <textarea
                    id="app-env"
                    value={envText}
                    onChange={(event) => {
                      envDirty.current = true
                      setEnvText(event.target.value)
                    }}
                    rows={4}
                    spellCheck={false}
                    placeholder={'DB_HOST=127.0.0.1\nDB_PORT=3306'}
                    className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
                  />
                )}
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {redacted ? t('apps.envKeysOnly') : t('apps.envHint')}
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
