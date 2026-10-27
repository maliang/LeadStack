import { useEffect, useRef, useState } from 'react'
import { api, type NetworkConfig, type NetworkSource } from '../lib/api'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { parsePort, validateListenAddress, validatePublicUrl } from '../lib/network'
import { cn } from '../lib/cn'
import { Button } from './Button'
import { ConfirmDialog } from './ConfirmDialog'
import { CopyButton } from './CopyButton'
import {
  AlertTriangleIcon,
  CheckCircleIcon,
  GlobeIcon,
  RefreshIcon,
  ServerIcon,
  TerminalIcon,
} from './Icons'
import { InlineLoading } from './Loading'
import { useToast } from './Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

interface FormState {
  listen: string
  publicUrl: string
  defaultSshPort: string
}

interface FieldErrors {
  listen?: string
  publicUrl?: string
  defaultSshPort?: string
  form?: string
}

function SourceBadge({ source }: { source: NetworkSource }) {
  const { t } = useI18n()
  const normalized = source === 'settings' || source === 'env' ? source : 'default'
  const styles: Record<string, string> = {
    settings:
      'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-400/30',
    env: 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/30',
    default:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-800 dark:text-slate-300 dark:ring-slate-400/30',
  }
  return (
    <span
      className={cn(
        'inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        styles[normalized],
      )}
    >
      {t(`network.source.${normalized}`)}
    </span>
  )
}

/** 设置页「网络与端口」：监听地址（需重启）、对外地址、目标机默认 SSH 端口。 */
export function NetworkSettingsCard() {
  const { t } = useI18n()
  const { toast } = useToast()
  // GET /system/network（以及 PUT 与 reset）都要求 system.manage + 全局范围
  // （internal/api/router.go:233-235）——网络配置是控制端全局设置，分组/服务器范围的角色
  // 既读不到也不该改。门控放在卡片自身而不是依赖调用页面的包裹：这样卡片被挪到别处也不会
  // 重新开始每 15 秒吃一次静默 403。判定表达式与 NetworkFallbackBanner 保持一致。
  const { can, scope } = useAuth()
  const canManageNetwork = can('system.manage') && scope.mode === 'all'
  const query = usePolling(api.network, 15_000, canManageNetwork)
  const syncedRef = useRef(false)
  const [form, setForm] = useState<FormState>({ listen: '', publicUrl: '', defaultSshPort: '22' })
  const [errors, setErrors] = useState<FieldErrors>({})
  const [saving, setSaving] = useState(false)
  const [altHint, setAltHint] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const [resetting, setResetting] = useState(false)

  const syncFrom = (data: NetworkConfig) => {
    setForm({
      listen: data.listen ?? data.active_listen,
      publicUrl: data.public_url ?? '',
      defaultSshPort: String(data.default_ssh_port),
    })
  }

  useEffect(() => {
    if (!query.data || syncedRef.current) return
    syncedRef.current = true
    syncFrom(query.data)
  }, [query.data])

  const data = query.data
  const listenChanged = data ? form.listen.trim() !== (data.listen ?? data.active_listen) : false
  const needRestart = Boolean(data?.restart_required) || listenChanged

  const handleSave = async () => {
    const next: FieldErrors = {}
    const listenError = validateListenAddress(form.listen)
    if (listenError) next.listen = listenError
    const publicUrlError = validatePublicUrl(form.publicUrl)
    if (publicUrlError) next.publicUrl = publicUrlError
    const port = parsePort(form.defaultSshPort)
    if (port === null) next.defaultSshPort = 'network.error.portRange'
    const trimmedPublicUrl = form.publicUrl.trim()
    if (data?.public_url && trimmedPublicUrl === '') {
      next.publicUrl = 'network.error.publicUrlCannotClear'
    }
    setErrors(next)
    if (Object.keys(next).length > 0) return

    setSaving(true)
    setAltHint(false)
    try {
      // 只有真正改动监听地址时才提交：未改动时后端会 probe 当前（已占用）端口而报 400
      const listenChangedOnServer = data ? form.listen.trim() !== (data.listen ?? '') : true
      const saved = await api.updateNetwork({
        listen: listenChangedOnServer ? form.listen.trim() : undefined,
        public_url: trimmedPublicUrl !== (data?.public_url ?? '') ? trimmedPublicUrl : undefined,
        default_ssh_port: port ?? 22,
      })
      syncFrom(saved)
      setErrors({})
      toast(t('network.saved'), 'success')
      void query.refresh()
    } catch (caught) {
      const message = errorMessage(caught, t('common.error'))
      const lower = message.toLowerCase()
      if (
        lower.includes('listen') ||
        message.includes('监听') ||
        message.includes('绑定') ||
        message.includes('占用')
      ) {
        setErrors({ listen: 'network.error.serverRejected' })
        setAltHint(true)
      } else if (lower.includes('public_url') || message.includes('对外')) {
        setErrors({ publicUrl: 'network.error.serverRejected' })
      } else if (lower.includes('default_ssh_port') || message.includes('端口')) {
        setErrors({ defaultSshPort: 'network.error.serverRejected' })
      } else {
        setErrors({ form: message })
      }
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    setResetting(true)
    try {
      const reset = await api.resetNetwork()
      syncFrom(reset)
      setErrors({})
      setResetOpen(false)
      toast(t('network.resetDone'), 'success')
      void query.refresh()
    } catch (caught) {
      setErrors({ form: errorMessage(caught, t('common.error')) })
    } finally {
      setResetting(false)
    }
  }

  const effectiveListen = data?.active_listen ?? t('common.loading')
  const restartCommands = [
    { key: 'network.restart.systemd', command: 'sudo systemctl restart leadstack' },
    { key: 'network.restart.docker', command: 'docker restart leadstack' },
    { key: 'network.restart.direct', command: 'sudo ./leadstack' },
  ]

  // 没有全局范围就不渲染表单：保存/重置同样会被后端拒绝，给出空表单只会让人白填一遍。
  if (!canManageNetwork) {
    return (
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
          <GlobeIcon className="h-4 w-4 text-indigo-500" />
          {t('network.needsGlobalScopeTitle')}
        </h2>
        <p className="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {t('network.needsGlobalScope')}
        </p>
      </section>
    )
  }

  return (
    <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <GlobeIcon className="h-4 w-4 text-indigo-500" />
            {t('network.title')}
          </h2>
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">{t('network.description')}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void query.refresh()}
            loading={query.loading}
            icon={<RefreshIcon className="h-3.5 w-3.5" />}
          >
            {t('common.refresh')}
          </Button>
          <Button variant="secondary" size="sm" onClick={() => setResetOpen(true)} disabled={saving}>
            {t('network.reset')}
          </Button>
          <Button size="sm" onClick={() => void handleSave()} loading={saving}>
            {t('common.save')}
          </Button>
        </div>
      </div>

      {query.error ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('network.loadFailed')}：{query.error}
        </p>
      ) : null}

      {query.loading && query.data === null ? <InlineLoading label={t('common.loading')} /> : null}

      {data ? (
        <div className="mt-4 space-y-5">
          <div className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2.5 text-xs leading-relaxed text-sky-900 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-100">
            {t('network.honestNotice')}
          </div>

          {/* 监听地址 */}
          <div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label htmlFor="network-listen" className={labelClass}>
                {t('network.listen')}
                <span className="ml-1.5 font-normal text-slate-400">{t('network.listenHint')}</span>
              </label>
              <SourceBadge source={data.listen_source} />
            </div>
            <div className="mt-2 grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
              <input
                id="network-listen"
                value={form.listen}
                onChange={(event) => {
                  setForm({ ...form, listen: event.target.value })
                  if (errors.listen) setErrors({ ...errors, listen: undefined })
                }}
                placeholder="127.0.0.1:8080"
                className={cn(inputClass, 'font-mono text-xs', errors.listen && 'border-rose-400 dark:border-rose-500')}
              />
              <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                <span>{t('network.effectiveNow')}</span>
                <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-700 dark:bg-slate-800 dark:text-slate-200">
                  {effectiveListen}
                </code>
              </div>
            </div>
            {errors.listen ? (
              <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.listen)}</p>
            ) : (
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('network.listenDefaultHint')}
              </p>
            )}
          </div>

          {needRestart ? (
            <div className="rounded-lg border border-amber-300 bg-amber-50 p-3 dark:border-amber-500/40 dark:bg-amber-500/10">
              <p className="flex items-center gap-2 text-sm font-semibold text-amber-900 dark:text-amber-100">
                <AlertTriangleIcon className="h-4 w-4" />
                {t('network.restartRequiredTitle')}
              </p>
              <p className="mt-1 text-xs leading-relaxed text-amber-800/90 dark:text-amber-200/90">
                {t('network.restartRequiredHint', {
                  active: data.active_listen,
                  configured: form.listen.trim() || data.listen || '—',
                })}
              </p>
              <div className="mt-2 grid gap-1.5 sm:grid-cols-3">
                {restartCommands.map((item) => (
                  <div
                    key={item.key}
                    className="rounded-md border border-amber-200 bg-white/70 p-2 dark:border-amber-500/30 dark:bg-slate-950/40"
                  >
                    <p className="text-[11px] font-medium text-amber-900/80 dark:text-amber-200/80">
                      {t(item.key)}
                    </p>
                    <div className="mt-1 flex items-center gap-1">
                      <code className="min-w-0 flex-1 truncate font-mono text-[11px] text-slate-700 dark:text-slate-200">
                        {item.command}
                      </code>
                      <CopyButton value={item.command} />
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ) : (
            <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <CheckCircleIcon className="h-3.5 w-3.5" />
              {t('network.noRestart')}
            </p>
          )}

          {/* 对外地址 */}
          <div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label htmlFor="network-public-url" className={labelClass}>
                {t('network.publicUrl')}
              </label>
              <SourceBadge source={data.public_url_source} />
            </div>
            <input
              id="network-public-url"
              value={form.publicUrl}
              onChange={(event) => {
                setForm({ ...form, publicUrl: event.target.value })
                if (errors.publicUrl) setErrors({ ...errors, publicUrl: undefined })
              }}
              placeholder="https://leadstack.example.com"
              className={cn(inputClass, 'mt-2 font-mono text-xs', errors.publicUrl && 'border-rose-400 dark:border-rose-500')}
            />
            {errors.publicUrl ? (
              <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.publicUrl)}</p>
            ) : (
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">{t('network.publicUrlHint')}</p>
            )}
            {data.public_url ? (
              <p className="mt-1 text-[11px] text-slate-400 dark:text-slate-500">
                {t('network.currentPublicUrl', { url: data.public_url })}
              </p>
            ) : null}
          </div>

          {/* 默认 SSH 端口 */}
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <label htmlFor="network-default-ssh-port" className={labelClass}>
                  {t('network.defaultSshPort')}
                </label>
                <SourceBadge source={data.default_ssh_port_source} />
              </div>
              <input
                id="network-default-ssh-port"
                value={form.defaultSshPort}
                onChange={(event) => {
                  setForm({ ...form, defaultSshPort: event.target.value })
                  if (errors.defaultSshPort) setErrors({ ...errors, defaultSshPort: undefined })
                }}
                inputMode="numeric"
                className={cn(inputClass, 'mt-2 max-w-[160px]', errors.defaultSshPort && 'border-rose-400 dark:border-rose-500')}
              />
              {errors.defaultSshPort ? (
                <p className="mt-1 text-xs text-rose-600 dark:text-rose-400">{t(errors.defaultSshPort)}</p>
              ) : (
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">{t('network.defaultSshPortHint')}</p>
              )}
            </div>
            <div className="flex items-start gap-2 rounded-md bg-slate-50 px-3 py-2 text-xs text-slate-500 dark:bg-slate-950/60 dark:text-slate-400 sm:mt-6">
              <ServerIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{t('network.defaultSshPortUsage')}</span>
            </div>
          </div>

          <details className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2.5 dark:border-slate-800 dark:bg-slate-950/60">
            <summary className="cursor-pointer text-sm font-medium text-slate-700 dark:text-slate-200">
              {t('network.escape.title')}
            </summary>
            <p className="mt-1.5 text-xs leading-relaxed text-slate-500 dark:text-slate-400">
              {t('network.escape.description')}
            </p>
            <div className="mt-2 space-y-2">
              <div>
                <p className="text-[11px] font-medium text-slate-600 dark:text-slate-300">
                  {t('network.escape.offlineLabel')}
                </p>
                <div className="mt-1 flex items-center gap-2 rounded-md border border-slate-200 bg-white px-2.5 py-1.5 dark:border-slate-700 dark:bg-slate-900">
                  <code className="min-w-0 flex-1 overflow-x-auto font-mono text-[11px] text-slate-700 dark:text-slate-200">
                    leadstack network --listen 127.0.0.1:9090
                  </code>
                  <CopyButton value="leadstack network --listen 127.0.0.1:9090" />
                </div>
              </div>
              <div>
                <p className="text-[11px] font-medium text-slate-600 dark:text-slate-300">
                  {t('network.escape.temporaryLabel')}
                </p>
                <div className="mt-1 flex items-center gap-2 rounded-md border border-slate-200 bg-white px-2.5 py-1.5 dark:border-slate-700 dark:bg-slate-900">
                  <code className="min-w-0 flex-1 overflow-x-auto font-mono text-[11px] text-slate-700 dark:text-slate-200">
                    leadstack --listen 127.0.0.1:9090
                  </code>
                  <CopyButton value="leadstack --listen 127.0.0.1:9090" />
                </div>
              </div>
            </div>
            <p className="mt-2 text-[11px] text-slate-400 dark:text-slate-500">
              {t('network.escape.note')}
            </p>
          </details>

          {altHint ? (
            <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
              {t('network.saveFailedHint')}
            </p>
          ) : null}

          {errors.form ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {errors.form}
            </p>
          ) : null}

          <p className="flex items-center gap-1.5 text-[11px] text-slate-400 dark:text-slate-500">
            <TerminalIcon className="h-3.5 w-3.5" />
            {t('network.restartCommandHint')}
          </p>
        </div>
      ) : null}

      <ConfirmDialog
        open={resetOpen}
        title={t('network.resetTitle')}
        description={t('network.resetDescription')}
        confirmLabel={t('network.reset')}
        tone="danger"
        loading={resetting}
        onClose={() => setResetOpen(false)}
        onConfirm={() => void handleReset()}
      >
        <p>{t('network.resetImpact')}</p>
      </ConfirmDialog>
    </section>
  )
}
