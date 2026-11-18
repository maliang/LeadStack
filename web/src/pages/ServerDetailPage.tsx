import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  METRIC_KEYS,
  api,
  type MetricSeries,
  type MetricsStep,
  type Server,
  type ServerAccess,
  type ServerLoginResult,
  type TestConnectionResult,
  type UpdateAsset,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import {
  formatBytes,
  formatDateTime,
  formatLatency,
  formatLoad,
  formatPercent,
  formatRelativeTime,
  formatUptime,
  metricLabel,
  serverAddress,
  truncateMiddle,
  usageTone,
} from '../lib/format'
import { Button } from '../components/Button'
import { CodeEditor } from '../components/CodeEditor'
import { CopyButton } from '../components/CopyButton'
import { Modal } from '../components/Modal'
import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  ArrowUpCircleIcon,
  PackageIcon,
  CheckIcon,
  ClockIcon,
  InfoIcon,
  LayoutDashboardIcon,
  RefreshIcon,
  TerminalIcon,
  WifiIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { MetricChart } from '../components/MetricChart'
import { ModeBadge, StatusBadge } from '../components/StatusBadge'
import { ServerChecksTab } from '../components/ServerChecksTab'
import { DiscoverDialog } from '../components/DiscoverDialog'
import { AppDialog, type AppPreset } from '../components/AppDialog'
import { MonitorTab } from '../components/server/MonitorTab'
import { TerminalTab } from '../components/server/TerminalTab'
import { FilesTab } from '../components/server/FilesTab'
import { ServerLogsTab } from '../components/server/ServerLogsTab'
import { useI18n } from '../i18n'
import { useAuth } from '../lib/auth'
import { useToast } from '../components/Toast'
import { useUpdateAccess } from '../components/SystemUpdate'
import { cn } from '../lib/cn'

import { t } from '../i18n'
const RANGES = [
  { key: '1h', label: t('s.0661'), ms: 3_600_000 },
  { key: '24h', label: t('s.0662'), ms: 86_400_000 },
  { key: '7d', label: t('s.0663'), ms: 604_800_000 },
] as const

const STEPS: { key: MetricsStep; label: string }[] = [
  { key: 'raw', label: t('s.1074') },
  { key: '1m', label: t('s.0183') },
  { key: '1h', label: t('s.0001') },
]

type RangeKey = (typeof RANGES)[number]['key']

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-slate-100 py-2.5 last:border-b-0 dark:border-slate-800/70">
      <dt className="shrink-0 text-xs text-slate-500 dark:text-slate-400">{label}</dt>
      <dd className="min-w-0 break-all text-right text-xs font-medium text-slate-700 dark:text-slate-200">
        {children}
      </dd>
    </div>
  )
}

function ResourceBar({ label, value }: { label: string; value: number | null }) {
  const tone = usageTone(value)
  const barColor =
    tone === 'danger' ? 'bg-rose-500' : tone === 'warn' ? 'bg-amber-500' : 'bg-emerald-500'
  const textColor =
    tone === 'danger'
      ? 'text-rose-600 dark:text-rose-400'
      : tone === 'warn'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-slate-800 dark:text-slate-100'

  return (
    <div>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-xs text-slate-500 dark:text-slate-400">{label}</span>
        <span className={cn('text-sm font-semibold tabular-nums', textColor)}>
          {formatPercent(value)}
        </span>
      </div>
      <div className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
        <div
          className={cn('h-1.5 rounded-full transition-all', barColor)}
          style={{ width: `${Math.max(0, Math.min(100, value ?? 0))}%` }}
        />
      </div>
    </div>
  )
}

function pickSeries(series: MetricSeries[], aliases: readonly string[]): MetricSeries[] {
  for (const alias of aliases) {
    const found = series.find((item) => item.metric === alias)
    if (found) return [found]
  }
  return []
}

/**
 * 从 agent 产物清单里按当前服务器的 os/arch 精确匹配。
 *
 * 归档名形如 `leadstack-agent_<ver>_<os>_<arch>.tar.gz`，故先按
 * `_<os>_<arch>` 后缀比对（去掉扩展名后 endsWith，避免 linux_amd64 命中
 * linux_arm64 之类的近似项）；老版本控制端可能不带 name，再退回 os/arch 字段精确比对。
 */
function pickAgentAsset(
  assets: UpdateAsset[] | undefined,
  os: string | null,
  arch: string | null,
): UpdateAsset | null {
  const wantOs = (os ?? '').trim().toLowerCase()
  const wantArch = (arch ?? '').trim().toLowerCase()
  if (!assets || assets.length === 0 || !wantOs || !wantArch) return null
  const suffix = `_${wantOs}_${wantArch}`
  for (const asset of assets) {
    const base = (asset.name ?? '')
      .trim()
      .toLowerCase()
      .replace(/\.(?:tar\.gz|tgz|zip)$/, '')
    if (base !== '' && base.endsWith(suffix)) return asset
  }
  return (
    assets.find(
      (asset) =>
        asset.os.trim().toLowerCase() === wantOs && asset.arch.trim().toLowerCase() === wantArch,
    ) ?? null
  )
}

const upgradeInputClass =
  'mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const upgradeLabelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

/**
 * 「升级 Agent」弹窗（T-14）。
 *
 * 打开时调 POST /system/update/check（api.refreshUpdateCheck），从返回的 agent_assets 中
 * 按当前服务器 os/arch 预填 version/url/sha256；取不到（离线 / 无该平台产物）就留空让
 * 用户手填，不阻断提交。提交走 api.agentUpgrade。
 *
 * 注意预填不是必达条件：`/system/update` 系列端点是 PermSystemManage（真实键名
 * `system.manage`）+ 全局范围（internal/api/router.go:229-232），所以只有
 * `can('system.manage') && scope.mode === 'all'` 的角色才去请求（复用
 * components/SystemUpdate.tsx 里的 useUpdateAccess）；否则直接跳过预填，避免
 * 「打开弹窗就静默 403」，并给一句可理解的说明让用户手填。
 */
function AgentUpgradeDialog({
  open,
  server,
  onClose,
  onDone,
}: {
  open: boolean
  server: Server
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useI18n()
  const { toast } = useToast()
  const [version, setVersion] = useState('')
  const [url, setUrl] = useState('')
  const [sha256, setSha256] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const serverOs = server.os ?? server.host_info?.os ?? null
  const serverArch = server.arch ?? server.host_info?.arch ?? null
  // 读取 agent 产物的前提是能看 /system/update（system.manage + 全局范围）；不满足时
  // 连请求都不发，直接走「手填」路径（A30 要求手填可用）。
  const canPrefill = useUpdateAccess()

  useEffect(() => {
    if (!open) return
    setVersion('')
    setUrl('')
    setSha256('')
    setError(null)
    if (!canPrefill) return
    let cancelled = false
    api
      .refreshUpdateCheck()
      .then((info) => {
        if (cancelled) return
        const asset = pickAgentAsset(info.agent_assets, serverOs, serverArch)
        if (!asset) return
        // 只填还空着的字段：预填返回较晚时不能覆盖用户已经手输的内容。
        const nextVersion = (info.latest ?? '').trim()
        setVersion((prev) => (prev.trim() === '' ? nextVersion : prev))
        setUrl((prev) => (prev.trim() === '' ? asset.url : prev))
        setSha256((prev) => (prev.trim() === '' ? (asset.sha256 ?? '') : prev))
      })
      // 预填只是便利，不算操作失败：离线 / 老控制端都按「取不到」处理。
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [open, canPrefill, server.id, serverOs, serverArch])

  const canSubmit =
    !submitting && version.trim() !== '' && url.trim() !== '' && sha256.trim() !== ''

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!canSubmit) return
    setSubmitting(true)
    setError(null)
    try {
      const result = await api.agentUpgrade(server.id, {
        version: version.trim(),
        url: url.trim(),
        sha256: sha256.trim().toLowerCase(),
      })
      if (!result.ok) {
        // 202 只代表指令已排队；ok=false 时沿用后端给出的原因（已是本地化文案）。
        setError(result.error?.trim() || t('s.1077'))
        return
      }
      toast(t('server.agentUpgradeDone', { version: version.trim() }), 'success')
      onClose()
      onDone()
    } catch (caught) {
      // 403/404（含范围外服务器的 404 + not_found）统一走 errorMessage：
      // 文案取自后端本地化 message，并自动附带 request_id（lib/errors.ts）。
      setError(errorMessage(caught, t('s.1077')))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('server.agentUpgradeTitle')}
      description={server.name}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={submitting}>
            {t('s.0024')}
          </Button>
          <Button
            type="submit"
            form="agent-upgrade-form"
            loading={submitting}
            disabled={!canSubmit}
          >
            {t('server.agentUpgradeSubmit')}
          </Button>
        </>
      }
    >
      <form id="agent-upgrade-form" onSubmit={handleSubmit} className="space-y-3">
        <p className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-xs leading-relaxed text-slate-600 dark:border-slate-700 dark:bg-slate-950/60 dark:text-slate-300">
          {t('server.agentUpgradeHint')}
        </p>

        {canPrefill ? null : (
          <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
            {t('server.agentUpgradeManualHint')}
          </p>
        )}

        {error ? (
          <p
            role="alert"
            className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
          >
            {error}
          </p>
        ) : null}

        <div>
          <label htmlFor="agent-upgrade-version" className={upgradeLabelClass}>
            {t('releases.version')}
          </label>
          <input
            id="agent-upgrade-version"
            value={version}
            onChange={(event) => setVersion(event.target.value)}
            placeholder="0.1.4"
            autoComplete="off"
            className={upgradeInputClass}
          />
        </div>

        <div>
          <label htmlFor="agent-upgrade-url" className={upgradeLabelClass}>
            {t('s.0470')}
          </label>
          <input
            id="agent-upgrade-url"
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder="https://example.com/leadstack-agent_0.1.4_linux_amd64.tar.gz"
            autoComplete="off"
            className={upgradeInputClass + ' font-mono text-xs'}
          />
        </div>

        <div>
          <label htmlFor="agent-upgrade-sha256" className={upgradeLabelClass}>
            {t('s.0471')}
          </label>
          <input
            id="agent-upgrade-sha256"
            value={sha256}
            onChange={(event) => setSha256(event.target.value)}
            placeholder="e3b0c442…"
            autoComplete="off"
            className={upgradeInputClass + ' font-mono text-xs'}
          />
          <p className="mt-1 text-[11px] leading-relaxed text-slate-400 dark:text-slate-500">
            {t('s.0472')}
          </p>
        </div>

        <p className="text-xs text-slate-400 dark:text-slate-500">
          {[serverOs, serverArch].filter(Boolean).join(' / ') || '—'}
        </p>
      </form>
    </Modal>
  )
}

/** 密码掩码占位：只在界面上替换明文，复制时仍取真实值。 */
const ACCESS_PASSWORD_MASK = '••••••••'

export interface ServerAccessLabels {
  name: string
  host: string
  port: string
  username: string
  password: string
}

/**
 * 把登录信息拼成「字段: 值」多行文本，供「一键复制」使用（纯函数，便于单测）。
 * 数字字段（端口）不带单位后缀，与字段标签分列。
 */
export function formatServerAccess(access: ServerAccess, labels: ServerAccessLabels): string {
  return [
    `${labels.name}: ${access.name}`,
    `${labels.host}: ${access.host}`,
    `${labels.port}: ${access.port === null ? '' : access.port}`,
    `${labels.username}: ${access.username}`,
    `${labels.password}: ${access.password}`,
  ].join('\n')
}

interface AccessRowProps {
  label: string
  value: string
  copyValue?: string
  mono?: boolean
  children?: ReactNode
}

/** 单条登录信息：左标签右值 + 复制；窄宽下值截断，hover 用 title 兜底。 */
function AccessRow({ label, value, copyValue, mono = false, children }: AccessRowProps) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 border-b border-slate-100 py-2 last:border-b-0 dark:border-slate-800/70">
      <dt className="shrink-0 text-xs text-slate-500 dark:text-slate-400">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center justify-end gap-1.5">
        <span
          className={cn(
            'min-w-0 flex-1 truncate text-right text-xs font-medium text-slate-700 dark:text-slate-200',
            mono && 'font-mono text-[11px]',
          )}
          title={value}
        >
          {value}
        </span>
        {children}
        {copyValue === undefined ? null : (
          <CopyButton value={copyValue} label={t('server.access.copy')} className="h-7 px-2" />
        )}
      </dd>
    </div>
  )
}

/** 登录脚本示例：宝塔 / 1Panel 各一段，覆盖 output 与 terminal 两种分派形态。 */
const LOGIN_SCRIPT_SAMPLES: Array<{ key: string; labelKey: string; script: string }> = [
  {
    key: 'baota',
    labelKey: 'server.login.sampleBaota',
    script: `#!/usr/bin/env bash
# 宝塔面板 v14 起可用「bt 14」打印面板地址（含安全入口路径）；只提取其中的完整 URL。
# 注意：不要把含初始密码的整段输出写进脚本，脚本输出会被记录到任务日志里。
PREFIX=$(bt 14 2>/dev/null || /etc/init.d/bt 14 2>/dev/null)
# bt 14 的输出常带颜色控制码，不清洗会把转义序列吃进 URL；部分系统输出是 CRLF，\r 也会被 grep 吃进 URL 尾部。
PREFIX=$(printf '%s' "$PREFIX" | tr -d '\\r' | sed -E $'s/\\x1B\\[[0-9;]*[A-Za-z]//g')
PANEL_URL=$(printf '%s' "$PREFIX" | grep -oE 'https?://[^ ]+' | head -n 1)
if [ -n "$PANEL_URL" ]; then
  printf '{"url":"%s"}' "$PANEL_URL"
else
  printf '{"output":"没有从 bt 14 的输出里找到面板地址，请到「面板设置」确认面板端口与安全入口"}'
fi`,
  },
  {
    key: 'onepanel',
    labelKey: 'server.login.sampleOnePanel',
    script: `#!/usr/bin/env bash
# 1Panel 只有 API Key 鉴权，没有免密网页登录；这里输出一条 SSH 命令交给网页终端。
# 把 host/port 换成这台服务器的实际值（脚本里不要写密码）。
printf '{"terminal":"ssh -p %s %s@%s"}' '22' 'root' '1.2.3.4'`,
  },
]

/**
 * 「登录脚本」配置对话框。
 *
 * 脚本在目标机上执行（POST /servers/{id}/login 由发布执行器下发单步任务），约定输出 JSON：
 * {"url":…} / {"terminal":…} / {"output":…}。保存走 PATCH /servers/{id} 的 login_script。
 */
function LoginScriptDialog({
  open,
  server,
  onClose,
  onSaved,
}: {
  open: boolean
  server: Server
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const { toast } = useToast()
  const [script, setScript] = useState(server.login_script)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setScript(server.login_script)
    setError(null)
  }, [open, server.login_script])

  const save = async (next: string) => {
    setSaving(true)
    setError(null)
    try {
      await api.updateServer(server.id, { login_script: next })
      toast(t('server.login.scriptSaved'), 'success')
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('server.login.scriptSaveFailed')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      size="lg"
      title={t('server.login.scriptTitle')}
      description={t('server.login.scriptHint')}
      onClose={onClose}
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button
            variant="ghost"
            size="sm"
            disabled={saving || script.trim() === ''}
            onClick={() => void save('')}
          >
            {t('server.login.scriptClear')}
          </Button>
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" onClick={onClose} disabled={saving}>
              {t('s.0024')}
            </Button>
            <Button size="sm" loading={saving} onClick={() => void save(script)}>
              {t('server.login.scriptSave')}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-slate-500 dark:text-slate-400">
            {t('server.login.sampleLabel')}
          </span>
          {LOGIN_SCRIPT_SAMPLES.map((sample) => (
            <Button
              key={sample.key}
              variant="ghost"
              size="sm"
              onClick={() => setScript(sample.script)}
            >
              {t(sample.labelKey)}
            </Button>
          ))}
        </div>
        <CodeEditor
          value={script}
          onChange={setScript}
          rows={14}
          placeholder={t('server.login.scriptPlaceholder')}
          ariaLabel={t('server.login.scriptTitle')}
        />
        <p className="text-[11px] leading-relaxed text-slate-400 dark:text-slate-500">
          {t('server.login.scriptRisk')}
        </p>
        {error ? (
          <p className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}

/** 一键登录结果对话框：terminal 形态给命令 + 跳终端，output 形态原样展示。 */
function LoginResultDialog({
  result,
  serverId,
  onClose,
}: {
  result: ServerLoginResult | null
  serverId: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const terminal = result?.terminal ?? ''
  const output = result?.output ?? ''

  return (
    <Modal
      open={result !== null}
      size="lg"
      title={terminal ? t('server.login.terminalTitle') : t('server.login.outputTitle')}
      description={terminal ? t('server.login.terminalHint') : t('server.login.outputHint')}
      onClose={onClose}
      footer={
        terminal ? (
          <div className="flex items-center justify-end gap-2">
            <CopyButton value={terminal} label={t('server.login.copyCommand')} />
            <Button size="sm" onClick={() => navigate(`/servers/${serverId}?tab=terminal`)}>
              {t('server.login.openTerminal')}
            </Button>
          </div>
        ) : null
      }
    >
      <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-all rounded-lg bg-slate-900 px-3 py-2 font-mono text-[11px] leading-relaxed text-slate-100">
        {terminal || output || t('server.login.resultEmpty')}
      </pre>
    </Modal>
  )
}

interface ServerAccessCardProps {
  server: Server
  onRefreshServer: () => void
}

/**
 * 服务器详情页「登录信息」卡片。
 *
 * 数据源 GET /servers/{id}/access：服务端每次读取都写一条 server.access 审计，
 * 所以卡片默认折叠——用户点「展开查看」才请求一次，收起即丢弃明文；不做轮询。
 * 同一张卡片是一键登录入口：未配置脚本给配置入口，已配置则执行脚本并按返回分派。
 */
function ServerAccessCard({ server, onRefreshServer }: ServerAccessCardProps) {
  const { t } = useI18n()
  const { toast } = useToast()
  const [expanded, setExpanded] = useState(false)
  const [access, setAccess] = useState<ServerAccess | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const [passwordVisible, setPasswordVisible] = useState(false)
  const [scriptOpen, setScriptOpen] = useState(false)
  const [loginBusy, setLoginBusy] = useState(false)
  const [loginResult, setLoginResult] = useState<ServerLoginResult | null>(null)
  const [panelBusy, setPanelBusy] = useState(false)

  const serverId = server.id
  const hasScript = server.login_script.trim() !== ''
  // 只有绑定了面板凭证才显示入口：未绑定时后端会返回 409，按钮出现也只是噪音。
  const hasPanel = (server.panel_credential_id ?? '') !== ''

  useEffect(() => {
    // 折叠状态一律不请求：否则「打开详情页」就等于「导出凭据」（契约 §67.6）。
    if (!expanded) return
    let cancelled = false
    setLoading(true)
    setError(null)
    api
      .serverAccess(serverId)
      .then((next) => {
        if (cancelled) return
        setAccess(next)
      })
      .catch((caught) => {
        if (cancelled) return
        setAccess(null)
        setError(errorMessage(caught, t('server.access.loadFailed')))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [expanded, serverId, reloadKey, t])

  // 标签每次渲染现取：useI18n 的 t 是稳定引用，memo 住会在切换语言后留下旧文案。
  const labels: ServerAccessLabels = {
    name: t('server.access.fieldName'),
    host: t('server.access.fieldHost'),
    port: t('server.access.fieldPort'),
    username: t('server.access.fieldUsername'),
    password: t('server.access.fieldPassword'),
  }

  const retry = () => setReloadKey((key) => key + 1)
  const collapse = () => {
    setExpanded(false)
    // 收起即丢弃：明文不留在内存里，下次展开重新读取（并重新记一次审计）。
    setAccess(null)
    setError(null)
    setPasswordVisible(false)
  }
  const toggleExpanded = () => {
    if (expanded) collapse()
    else setExpanded(true)
  }

  const runLogin = async () => {
    if (!hasScript) {
      setScriptOpen(true)
      return
    }
    setLoginBusy(true)
    try {
      const result = await api.serverLogin(serverId)
      if (result.url) {
        const opened = window.open(result.url, '_blank', 'noopener,noreferrer')
        toast(
          opened ? t('server.login.urlOpened') : t('server.login.urlBlocked'),
          opened ? 'success' : 'error',
        )
        return
      }
      if (result.terminal || result.output) {
        setLoginResult(result)
        return
      }
      toast(t('server.login.resultEmpty'), 'error')
    } catch (caught) {
      toast(errorMessage(caught, t('server.login.failed')), 'error')
    } finally {
      setLoginBusy(false)
    }
  }

  /**
   * 打开面板：先向后端要一次面板地址（写 server.panel 审计），再新标签打开。
   * 地址里含安全入口路径，等同口令，所以不落进 toast / 页面文案，只在回调里直接用掉。
   */
  const openPanel = async () => {
    setPanelBusy(true)
    try {
      const panel = await api.serverPanel(serverId)
      if (!panel.panel_url) {
        toast(t('server.panel.failed'), 'error')
        return
      }
      window.open(panel.panel_url, '_blank', 'noopener')
    } catch (caught) {
      toast(errorMessage(caught, t('server.panel.failed')), 'error')
    } finally {
      setPanelBusy(false)
    }
  }

  const hasPassword = access !== null && access.password !== ''
  const passwordText = !access
    ? '—'
    : !hasPassword
      ? '—'
      : passwordVisible
        ? access.password
        : ACCESS_PASSWORD_MASK
  const wholeText = access ? formatServerAccess(access, labels) : ''
  const keyAuth = access !== null && access.auth_type === 'key'

  return (
    <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
            {t('server.access.title')}
          </h2>
          <p className="mt-1 text-[11px] leading-relaxed text-slate-400 dark:text-slate-500">
            {t('server.access.hint')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {hasPanel ? (
            <Button
              size="sm"
              variant="secondary"
              loading={panelBusy}
              onClick={() => void openPanel()}
              icon={<LayoutDashboardIcon className="h-4 w-4" />}
            >
              {panelBusy ? t('server.panel.opening') : t('server.panel.open')}
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="secondary"
            loading={loginBusy}
            onClick={() => void runLogin()}
            icon={<TerminalIcon className="h-4 w-4" />}
          >
            {hasScript ? t('server.login.action') : t('server.login.configure')}
          </Button>
          {hasScript ? (
            <Button size="sm" variant="ghost" onClick={() => setScriptOpen(true)}>
              {t('server.login.scriptEdit')}
            </Button>
          ) : null}
          <Button size="sm" variant="secondary" onClick={toggleExpanded} loading={loading}>
            {expanded ? t('server.access.collapse') : t('server.access.expand')}
          </Button>
        </div>
      </div>

      {!expanded ? null : loading && !access ? (
        <div className="mt-3">
          <InlineLoading label={t('server.access.loading')} />
        </div>
      ) : null}

      {expanded && error ? (
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span className="min-w-0 break-all">{error}</span>
          <Button
            size="sm"
            variant="secondary"
            onClick={retry}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('server.access.retry')}
          </Button>
        </div>
      ) : null}

      {expanded && access ? (
        <>
          <div className="mt-2 flex flex-wrap items-center justify-end gap-1.5">
            <CopyButton value={wholeText} label={t('server.access.copyAll')} />
            <Button
              size="sm"
              variant="ghost"
              onClick={retry}
              loading={loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('server.access.refresh')}
            </Button>
          </div>
          <dl className="mt-1">
            <AccessRow
              label={labels.name}
              value={access.name || '—'}
              copyValue={access.name}
            />
            <AccessRow
              label={labels.host}
              value={access.host || '—'}
              copyValue={access.host}
              mono
            />
            <AccessRow
              label={labels.port}
              value={access.port === null ? '—' : String(access.port)}
              copyValue={access.port === null ? undefined : String(access.port)}
              mono
            />
            <AccessRow
              label={labels.username}
              value={access.username || '—'}
              copyValue={access.username}
              mono
            />
            <AccessRow
              label={labels.password}
              value={passwordText}
              copyValue={hasPassword ? access.password : undefined}
              mono
            >
              {hasPassword ? (
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2"
                  aria-pressed={passwordVisible}
                  aria-label={
                    passwordVisible
                      ? t('server.access.hidePassword')
                      : t('server.access.showPassword')
                  }
                  onClick={() => setPasswordVisible((visible) => !visible)}
                >
                  {passwordVisible
                    ? t('server.access.hidePassword')
                    : t('server.access.showPassword')}
                </Button>
              ) : null}
            </AccessRow>
          </dl>
          <p className="mt-3 text-[11px] leading-relaxed text-slate-400 dark:text-slate-500">
            {keyAuth ? t('server.access.keyAuthNote') : t('server.access.copyAllHint')}
          </p>
        </>
      ) : null}

      <LoginScriptDialog
        open={scriptOpen}
        server={server}
        onClose={() => setScriptOpen(false)}
        onSaved={onRefreshServer}
      />
      <LoginResultDialog
        result={loginResult}
        serverId={serverId}
        onClose={() => setLoginResult(null)}
      />
    </section>
  )
}

export default function ServerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { toast } = useToast()
  const now = useNow()
  const query = usePolling(() => api.server(id ?? ''), 10_000, Boolean(id))

  const { t } = useI18n()
  const { can } = useAuth()
  const navigate = useNavigate()
  const credentialsQuery = usePolling(api.credentials, 60_000)
  const [searchParams, setSearchParams] = useSearchParams()
  const tabParam = searchParams.get('tab')
  const detailTabs = ['overview', 'checks', 'monitor', 'terminal', 'files', 'logs'] as const
  type DetailTab = (typeof detailTabs)[number]
  const tab: DetailTab = detailTabs.includes((tabParam ?? '') as DetailTab)
    ? ((tabParam ?? 'overview') as DetailTab)
    : 'overview'
  const setTab = (next: DetailTab) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'overview') params.delete('tab')
    else params.set('tab', next)
    setSearchParams(params, { replace: true })
  }
  const [step, setStep] = useState<MetricsStep>('raw')
  const [discoverOpen, setDiscoverOpen] = useState(false)
  const [appPreset, setAppPreset] = useState<AppPreset | null>(null)
  const [agentUpgradeOpen, setAgentUpgradeOpen] = useState(false)
  const canAgentUpgrade = can('agent.upgrade')
  const [rangeKey, setRangeKey] = useState<RangeKey>('1h')
  const range = RANGES.find((item) => item.key === rangeKey) ?? RANGES[0]

  const metricsQuery = usePolling(
    () =>
      api.metrics(id ?? '', {
        from: Date.now() - range.ms,
        to: Date.now(),
        step,
      }),
    30_000,
    Boolean(id),
    `${id ?? ''}|${step}|${rangeKey}`,
  )

  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<TestConnectionResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)

  const [refreshingInfo, setRefreshingInfo] = useState(false)
  const [infoError, setInfoError] = useState<string | null>(null)

  const server = query.data

  const chartSeries = useMemo(
    () => ({
      cpu: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.cpu),
      mem: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.mem),
      disk: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.disk),
      load: pickSeries(metricsQuery.data?.series ?? [], METRIC_KEYS.load),
    }),
    [metricsQuery.data],
  )

  const handleTestConnection = async () => {
    if (!id) return
    setTesting(true)
    setTestError(null)
    setTestResult(null)
    try {
      const result = await api.testConnection(id)
      setTestResult(result)
      toast(result.ok ? t('s.1075') : t('s.1076'), result.ok ? 'success' : 'error')
    } catch (caught) {
      setTestError(errorMessage(caught, t('s.1077')))
    } finally {
      setTesting(false)
    }
  }

  const handleRefreshHostInfo = async () => {
    if (!id) return
    setRefreshingInfo(true)
    setInfoError(null)
    try {
      const info = await api.refreshHostInfo(id)
      if (info) {
        toast(t('s.1078'), 'success')
      } else {
        setInfoError(t('s.1079'))
      }
      void query.refresh()
    } catch (caught) {
      setInfoError(errorMessage(caught, t('s.1080')))
    } finally {
      setRefreshingInfo(false)
    }
  }

  if (query.loading && !server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <InlineLoading label={t('s.1081')} />
      </div>
    )
  }

  if (!server) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-8 text-center shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <p className="text-sm font-medium text-slate-900 dark:text-slate-100">{t('s.1082')}</p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          {query.error ?? t('s.1083')}
        </p>
        <Link to="/" className="mt-4 inline-block">
          <Button variant="secondary" icon={<ArrowLeftIcon className="h-4 w-4" />}>
            {t('s.1084')}
          </Button>
        </Link>
      </div>
    )
  }

  const metrics = server.metrics_summary
  const info = server.host_info
  const isSsh = server.mode === 'ssh'
  const diskUsedPct =
    info?.disk_total_bytes && info.disk_used_bytes !== null
      ? (info.disk_used_bytes / info.disk_total_bytes) * 100
      : null

  return (
    <div className="space-y-5">
      <div>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500 transition-colors hover:text-indigo-600 dark:text-slate-400 dark:hover:text-indigo-400"
        >
          <ArrowLeftIcon className="h-3.5 w-3.5" />
          {t('s.1084')}
        </Link>

        <div className="mt-3 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="truncate text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
                {server.name}
              </h1>
              <StatusBadge status={server.status} />
              <ModeBadge mode={server.mode} />
              {server.group ? (
                <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                  {server.group}
                </span>
              ) : null}
            </div>
            <p className="mt-1 font-mono text-xs text-slate-500 dark:text-slate-400">
              {serverAddress(server)} {t('s.1085')} {formatRelativeTime(server.last_seen_at, now)}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {isSsh ? (
              <Button
                variant="secondary"
                onClick={handleTestConnection}
                loading={testing}
                icon={<WifiIcon className="h-4 w-4" />}
              >
                {t('s.0281')}
              </Button>
            ) : null}
            {isSsh ? (
              <Button
                variant="secondary"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
                icon={<RefreshIcon className="h-4 w-4" />}
              >
                {t('s.0282')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              onClick={() => setDiscoverOpen(true)}
              icon={<PackageIcon className="h-4 w-4" />}
            >
              {t('discover.open')}
            </Button>
            <Button
              variant="secondary"
              onClick={() => void query.refresh()}
              loading={query.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('s.0689')}
            </Button>
          </div>
        </div>

        <div className="mt-4 flex items-center gap-1 border-b border-slate-200 dark:border-slate-800">
          {(
            ([
              { key: 'overview', labelKey: 'serverTab.overview' },
              { key: 'checks', labelKey: 'serverTab.checks' },
              { key: 'monitor', labelKey: 'serverTab.monitor' },
              { key: 'terminal', labelKey: 'serverTab.terminal', permission: 'terminal.use' },
              { key: 'files', labelKey: 'serverTab.files', permission: 'terminal.use' },
              { key: 'logs', labelKey: 'serverTab.logs', permission: 'terminal.use' },
            ] as const).filter((option) => !('permission' in option) || can(option.permission))
          ).map((option) => (
            <button
              key={option.key}
              type="button"
              onClick={() => setTab(option.key)}
              className={cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors',
                tab === option.key
                  ? 'border-indigo-600 text-indigo-700 dark:border-indigo-400 dark:text-indigo-300'
                  : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
              )}
            >
              {t(option.labelKey)}
            </button>
          ))}
        </div>
      </div>

      {tab === 'checks' ? (
        <ServerChecksTab serverId={server.id} />
      ) : tab === 'monitor' ? (
        <MonitorTab serverId={server.id} />
      ) : tab === 'terminal' && can('terminal.use') ? (
        <TerminalTab serverId={server.id} />
      ) : tab === 'files' && can('terminal.use') ? (
        <FilesTab serverId={server.id} />
      ) : tab === 'logs' && can('terminal.use') ? (
        <ServerLogsTab serverId={server.id} />
      ) : (
        <>
      {testResult ? (
        <div
          className={cn(
            'flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border px-4 py-3 text-sm',
            testResult.ok
              ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200'
              : 'border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300',
          )}
        >
          {testResult.ok ? (
            <>
              <span className="inline-flex items-center gap-1.5 font-medium">
                <CheckIcon className="h-4 w-4" />
                {t('s.1075')}
              </span>
              <span>{t('s.1086')} {formatLatency(testResult.latency_ms)}</span>
              {testResult.ssh_banner ? (
                <span className="font-mono text-xs">{testResult.ssh_banner}</span>
              ) : null}
              {testResult.host_key_fingerprint ? (
                <span className="font-mono text-xs" title={testResult.host_key_fingerprint}>
                  {t('s.1087')} {truncateMiddle(testResult.host_key_fingerprint, 20, 8)}
                </span>
              ) : null}
            </>
          ) : (
            <span className="inline-flex items-center gap-1.5 font-medium">
              <AlertTriangleIcon className="h-4 w-4" />
              {t('s.1088')}{testResult.error?.trim() || t('s.1089')}
            </span>
          )}
        </div>
      ) : null}

      {testError ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.1090')}{testError}
        </div>
      ) : null}

      {query.error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.1052')}{query.error}{t('s.1091')}
        </div>
      ) : null}

      {server.last_error ? (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          <AlertTriangleIcon className="h-4 w-4 shrink-0" />
          <span>{t('s.1092')}{server.last_error}</span>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1093')}</h2>
          <dl className="mt-2">
            <InfoRow label={t('s.0066')}>{serverAddress(server)}</InfoRow>
            <InfoRow label={t('s.1094')}>
              {server.mode === 'agent' ? t('s.1095') : t('s.0036')}
            </InfoRow>
            <InfoRow label={t('server.panelCredential')}>
              {server.panel_credential_id
                ? (credentialsQuery.data ?? []).find(
                    (credential) => credential.id === server.panel_credential_id,
                  )?.name ?? server.panel_credential_id
                : t('server.panelCredentialNone')}
            </InfoRow>
            <InfoRow label={t('s.0062')}>
              {server.group_names.length > 0 ? (
                <span className="inline-flex flex-wrap justify-end gap-1">
                  {server.group_names.map((name) => (
                    <span
                      key={name}
                      className="rounded bg-indigo-50 px-1.5 py-0.5 text-[11px] font-normal text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300"
                    >
                      {name}
                    </span>
                  ))}
                </span>
              ) : (
                server.group ?? '—'
              )}
            </InfoRow>
            <InfoRow label={t('s.0075')}>
              {server.tags.length > 0 ? (
                <span className="inline-flex flex-wrap justify-end gap-1">
                  {server.tags.map((tag) => (
                    <span
                      key={tag}
                      className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] font-normal text-slate-500 dark:bg-slate-800 dark:text-slate-400"
                    >
                      {tag}
                    </span>
                  ))}
                </span>
              ) : (
                '—'
              )}
            </InfoRow>
            <InfoRow label={t('s.1096')}>
              {[server.os ?? info?.os, server.arch ?? info?.arch].filter(Boolean).join(' / ') || '—'}
            </InfoRow>
            <InfoRow label={t('s.1097')}>{server.kernel ?? info?.kernel ?? '—'}</InfoRow>
            {server.mode === 'agent' ? (
              <InfoRow label={t('s.1098')}>
                <span className="inline-flex flex-wrap items-center justify-end gap-2">
                  <span className="font-mono text-[11px] font-normal text-slate-500 dark:text-slate-400">
                    {server.agent_version ?? '—'}
                  </span>
                  <Button
                    size="sm"
                    variant="secondary"
                    icon={<ArrowUpCircleIcon className="h-4 w-4" />}
                    disabled={!canAgentUpgrade}
                    title={canAgentUpgrade ? undefined : t('server.agentUpgradeNoPerm')}
                    onClick={() => setAgentUpgradeOpen(true)}
                  >
                    {t('server.agentUpgrade')}
                  </Button>
                </span>
                {canAgentUpgrade ? null : (
                  <p className="mt-1 text-[11px] leading-relaxed text-amber-600 dark:text-amber-400">
                    {t('server.agentUpgradeNoPerm')}
                  </p>
                )}
              </InfoRow>
            ) : null}
            <InfoRow label={t('s.0538')}>{formatDateTime(server.last_seen_at)}</InfoRow>
            <InfoRow label={t('s.0271')}>{formatDateTime(server.created_at)}</InfoRow>
            <InfoRow label={t('s.1099')}>
              <span className="font-mono text-[11px] font-normal text-slate-500 dark:text-slate-400">
                {server.id}
              </span>
            </InfoRow>
          </dl>
        </section>

        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1100')}</h2>
            <span className="inline-flex items-center gap-1 text-[11px] text-slate-400 dark:text-slate-500">
              <ClockIcon className="h-3.5 w-3.5" />
              {formatRelativeTime(server.last_seen_at, now)}
            </span>
          </div>
          <div className="mt-4 space-y-4">
            <ResourceBar label={t('s.0633')} value={metrics?.cpu_pct ?? null} />
            <ResourceBar label={t('s.0634')} value={metrics?.mem_used_pct ?? null} />
            <ResourceBar label={t('s.0636')} value={metrics?.disk_used_pct ?? null} />
          </div>
          <dl className="mt-5 grid grid-cols-2 gap-3">
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.0637')}</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatLoad(metrics?.load1)}
              </dd>
            </div>
            <div className="rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
              <dt className="text-[11px] text-slate-500 dark:text-slate-400">{t('s.1101')}</dt>
              <dd className="mt-0.5 text-base font-semibold tabular-nums text-slate-800 dark:text-slate-100">
                {formatUptime(metrics?.uptime_s ?? info?.uptime_s ?? null)}
              </dd>
            </div>
          </dl>
          {!metrics ? (
            <p className="mt-4 text-xs text-slate-400 dark:text-slate-500">
              {t('s.1102')}
            </p>
          ) : null}
        </section>
      </div>

      <ServerAccessCard server={server} onRefreshServer={() => void query.refresh()} />

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0535')}</h2>
          {isSsh ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={handleRefreshHostInfo}
              loading={refreshingInfo}
              icon={<RefreshIcon className="h-3.5 w-3.5" />}
            >
              {t('s.0282')}
            </Button>
          ) : null}
        </div>

        {infoError ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {infoError}
          </p>
        ) : null}

        {!info ? (
          <div className="mt-4 flex flex-col items-center justify-center rounded-lg border border-dashed border-slate-300 px-6 py-10 text-center dark:border-slate-700">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
              <InfoIcon className="h-5 w-5" />
            </div>
            <p className="mt-3 text-sm font-medium text-slate-800 dark:text-slate-200">
              {t('s.1103')}
            </p>
            <p className="mt-1 max-w-lg text-xs leading-relaxed text-slate-500 dark:text-slate-400">
              {isSsh
                ? t('s.1104')
                : t('s.1105')}
            </p>
            {isSsh ? (
              <Button
                className="mt-4"
                size="sm"
                onClick={handleRefreshHostInfo}
                loading={refreshingInfo}
              >
                {t('s.1106')}
              </Button>
            ) : null}
          </div>
        ) : (
          <>
            <div className="mt-2 grid grid-cols-1 gap-x-8 sm:grid-cols-2">
              <dl>
                <InfoRow label={t('s.1107')}>{info.hostname ?? '—'}</InfoRow>
                <InfoRow label={t('s.1108')}>{info.os ?? '—'}</InfoRow>
                <InfoRow label={t('s.1109')}>{info.os_version ?? '—'}</InfoRow>
                <InfoRow label={t('s.1097')}>{info.kernel ?? '—'}</InfoRow>
                <InfoRow label={t('s.1110')}>{info.arch ?? '—'}</InfoRow>
                <InfoRow label={t('s.1111')}>{info.timezone ?? '—'}</InfoRow>
              </dl>
              <dl>
                <InfoRow label={t('s.1112')}>{info.cpu_model ?? '—'}</InfoRow>
                <InfoRow label={t('s.1113')}>{info.cpu_cores ?? '—'}</InfoRow>
                <InfoRow label={t('s.1114')}>{formatBytes(info.mem_total_bytes)}</InfoRow>
                <InfoRow label={t('s.1115')}>
                  {formatBytes(info.disk_total_bytes)}
                  {diskUsedPct !== null ? t('s.1116', { '0': diskUsedPct.toFixed(1) }) : ''}
                </InfoRow>
                <InfoRow label="Docker">
                  {info.docker
                    ? t('s.1117', { '0': info.docker_version ? ` · ${info.docker_version}` : '' })
                    : t('s.1118')}
                </InfoRow>
                <InfoRow label={t('s.1119')}>{formatDateTime(info.collected_at)}</InfoRow>
              </dl>
            </div>
            <div className="mt-3 border-t border-slate-100 pt-3 dark:border-slate-800/70">
              <p className="text-xs text-slate-500 dark:text-slate-400">{t('s.1120')}</p>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {info.ip_addrs.length > 0 ? (
                  info.ip_addrs.map((ip) => (
                    <span
                      key={ip}
                      className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
                    >
                      {ip}
                    </span>
                  ))
                ) : (
                  <span className="text-xs text-slate-400 dark:text-slate-500">—</span>
                )}
              </div>
            </div>
          </>
        )}
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.1121')}</h2>
            <p className="mt-0.5 text-xs text-slate-400 dark:text-slate-500">
              {t('s.1122')}{'{id}'}{t('s.1123')}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <div className="inline-flex overflow-hidden rounded-md border border-slate-200 dark:border-slate-700">
              {STEPS.map((option) => (
                <button
                  key={option.key}
                  type="button"
                  onClick={() => setStep(option.key)}
                  className={cn(
                    'px-2.5 py-1.5 text-xs font-medium transition-colors',
                    step === option.key
                      ? 'bg-indigo-600 text-white'
                      : 'bg-white text-slate-600 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800',
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <div className="inline-flex overflow-hidden rounded-md border border-slate-200 dark:border-slate-700">
              {RANGES.map((option) => (
                <button
                  key={option.key}
                  type="button"
                  onClick={() => setRangeKey(option.key)}
                  className={cn(
                    'px-2.5 py-1.5 text-xs font-medium transition-colors',
                    rangeKey === option.key
                      ? 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900'
                      : 'bg-white text-slate-600 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800',
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void metricsQuery.refresh()}
              loading={metricsQuery.loading}
              icon={<RefreshIcon className="h-3.5 w-3.5" />}
            >
              {t('s.0689')}
            </Button>
          </div>
        </div>

        {metricsQuery.error ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {t('s.1124')}{metricsQuery.error}
          </p>
        ) : null}

        <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-2">
          {[
            {
              key: 'cpu',
              title: metricLabel('cpu.pct'),
              series: chartSeries.cpu,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'mem',
              title: metricLabel('mem.used_pct'),
              series: chartSeries.mem,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'disk',
              title: metricLabel('disk.used.pct'),
              series: chartSeries.disk,
              format: (value: number) => `${value.toFixed(1)}%`,
            },
            {
              key: 'load',
              title: metricLabel('load.1'),
              series: chartSeries.load,
              format: (value: number) => value.toFixed(2),
            },
          ].map((chart) => (
            <div
              key={chart.key}
              className="rounded-lg border border-slate-100 p-3 dark:border-slate-800"
            >
              <p className="mb-2 text-xs font-medium text-slate-600 dark:text-slate-300">
                {chart.title}
              </p>
              <MetricChart
                series={chart.series}
                height={200}
                formatValue={chart.format}
                emptyHint={metricsQuery.loading ? t('s.1125') : t('s.1126')}
              />
            </div>
          ))}
        </div>

        <p className="mt-3 text-xs text-slate-400 dark:text-slate-500">
          {t('s.1127')}
        </p>
      </section>

      <section className="rounded-xl border border-dashed border-slate-300 bg-white/60 p-6 text-center dark:border-slate-700 dark:bg-slate-900/40">
        <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
          <TerminalIcon className="h-5 w-5" />
        </div>
        <h2 className="mt-3 text-sm font-semibold text-slate-800 dark:text-slate-200">
          {t('s.1128')}
        </h2>
        <p className="mx-auto mt-1 max-w-xl text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          {t('s.1129')}
        </p>
      </section>
        </>
      )}
      <DiscoverDialog
        open={discoverOpen}
        serverId={server.id}
        serverName={server.name}
        onClose={() => setDiscoverOpen(false)}
        onPick={(candidate, diagnosis) => {
          const rec = diagnosis?.recommendation
          const suggestedMode = rec?.deploy_mode ?? candidate.suggested.deploy_mode
          const gitDetected =
            suggestedMode === 'script' || /git\(/.test(candidate.detail)
          setAppPreset({
            name: candidate.name,
            root: candidate.suggested.root,
            deploy_mode:
              suggestedMode === 'script'
                ? 'script'
                : suggestedMode === 'release'
                  ? 'release'
                  : 'inplace',
            target_dir: rec?.target_dir ?? candidate.suggested.target_dir,
            restart_cmd: rec?.restart_cmd ?? candidate.suggested.restart_cmd,
            keep_paths: rec?.keep_paths,
            healthcheck_port: rec?.healthcheck_port ?? candidate.suggested.healthcheck_port,
            scriptTemplate: rec?.script,
            gitDetected,
          })
        }}
      />

      <AgentUpgradeDialog
        open={agentUpgradeOpen}
        server={server}
        onClose={() => setAgentUpgradeOpen(false)}
        onDone={() => void query.refresh()}
      />

      <AppDialog
        open={appPreset !== null}
        app={null}
        preset={appPreset}
        onClose={() => setAppPreset(null)}
        onSaved={() => {
          setAppPreset(null)
          navigate('/apps')
        }}
      />
    </div>
  )
}
