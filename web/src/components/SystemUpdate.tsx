import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type UpdatePhase, type UpdateStatus } from '../lib/api'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatBytes, formatDateTime, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button } from './Button'
import { ConfirmDialog } from './ConfirmDialog'
import { CheckCircleIcon, AlertTriangleIcon, ArrowUpCircleIcon, DownloadIcon, PackageIcon, RefreshIcon, SpinnerIcon, UploadIcon } from './Icons'
import { Modal } from './Modal'
import { useToast } from './Toast'

const ACTIVE_PHASES: UpdatePhase[] = ['downloading', 'backing_up', 'replacing', 'restarting']
const PHASE_ORDER: UpdatePhase[] = ['downloading', 'backing_up', 'replacing', 'restarting']

/**
 * 本地上传的大小闸门（前端先拦，提示后不发请求）。
 * 与后端 t6 的校验矩阵同口径（<1MB 或 >200MB → 413），前端这层只是即时反馈，
 * 真正的权威仍是后端——所以文案里不承诺「已经校验通过」。
 */
const MIN_UPLOAD_BYTES = 1 * 1024 * 1024
const MAX_UPLOAD_BYTES = 200 * 1024 * 1024

/** 取消上传以 AbortError 拒绝；据此区分「用户取消」与「真失败」（前者不该显示红色报错）。 */
function isAbortError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { name?: unknown }).name === 'AbortError'
}

export function isUpdateRunning(phase: UpdatePhase | undefined): boolean {
  return Boolean(phase && ACTIVE_PHASES.includes(phase))
}

export function isUpdateFinished(phase: UpdatePhase | undefined): boolean {
  // stalled 是服务端停滞看门狗（`upgrader.failUpgrade`，见 internal/api/update_apply.go）判定出的**终态**。
  // 不把它算作结束，界面就会对着一个早已失败的任务永远转圈——本次修复的核心。
  return phase === 'success' || phase === 'failed' || phase === 'rolled_back' || phase === 'stalled'
}

/**
 * 本地停滞阈值：20 分钟没有新的进度就不再转圈，改为提示 + 重试入口。
 * 服务端 stalled 是半程预警，但状态接口不可达（重启期间）时只有本地计时还能兜底。
 */
export const LOCAL_STALL_MS = 20 * 60 * 1000

/**
 * 进度指纹：只有「阶段 / 百分比 / 文案 / 备份文件」变化才算真的往前走。
 * phase_elapsed_s 每次轮询都在涨，绝不能进指纹，否则永远判不出停滞。
 */
export function updateProgressKey(status: UpdateStatus | null): string {
  if (!status) return ''
  return [status.phase, status.progress ?? '', status.message ?? '', status.backup_file ?? ''].join('|')
}

/** 停滞提示里的「已多久没进展」：本地计时与服务端阶段耗时取大者，向上取整且至少 1 分钟。 */
export function stallMinutes(noProgressSec: number, phaseElapsedSec: number | null | undefined): number {
  return Math.max(1, Math.ceil(Math.max(noProgressSec, phaseElapsedSec ?? 0) / 60))
}

/**
 * 阶段文案映射：既覆盖前端归一化后的阶段，也覆盖服务端原始阶段名
 * （update_apply.go 里的 verify_checksum / extract / prepare / swap / exec 等）。
 * 停滞提示要告诉用户「卡在哪个阶段」，直接吐内部英文名等于没说。
 */
const PHASE_LABEL_KEYS: Record<string, string> = {
  validating: 'update.phase.validating',
  downloading: 'update.phase.downloading',
  verify_checksum: 'update.phase.verify_checksum',
  extract: 'update.phase.extract',
  backing_up: 'update.phase.backing_up',
  prepare: 'update.phase.prepare',
  replacing: 'update.phase.replacing',
  swap: 'update.phase.swap',
  restarting: 'update.phase.restarting',
  spawn_watchdog: 'update.phase.spawn_watchdog',
  exec: 'update.phase.exec',
  stalled: 'update.phase.stalled',
}

export function updatePhaseLabel(
  phase: UpdatePhase | undefined,
  t: (key: string, vars?: Record<string, string | number>) => string,
): string {
  if (!phase || phase === 'idle') return '—'
  const key = PHASE_LABEL_KEYS[phase]
  return key ? t(key) : phase
}

/**
 * 能否读控制端自升级信息（顶栏「版本与升级」入口 + 设置页卡片 + 升级弹窗共用）。
 *
 * GET /system/update 与 /system/update/check|apply|status 全部是 permSystemManage + RequireGlobalScope
 * （internal/api/router.go:229-232），非全局范围或没有 system.manage 的角色请求必定 403。顶栏是每个
 * 页面都在的常驻组件，所以这里把「权限 + 范围」一起判，不发起请求（与 BackupsPage 的 isGlobalScope
 * 同范式：走 usePolling 的 enabled，既不请求也不挂定时器），而不是拿到 403 之后再吞掉。
 *
 * 注意权限点叫 system.manage（internal/auth/perm.go:36），没有 system.read。
 */
export function useUpdateAccess(): boolean {
  const { can, scope } = useAuth()
  return can('system.manage') && scope.mode === 'all'
}

/** 顶部/设置页共用的更新检查（缓存结果，5 分钟轮询；无权限/非全局范围时不请求）。 */
export function useUpdateNotice() {
  const allowed = useUpdateAccess()
  const query = usePolling(api.checkForUpdate, 300_000, allowed)
  return {
    info: query.data,
    loading: query.loading,
    error: query.error,
    refresh: query.refresh,
  }
}

function PhaseStepper({ status, stalled = false }: { status: UpdateStatus | null; stalled?: boolean }) {
  const { t } = useI18n()
  const phase = status?.phase
  const stalledPhase = phase === 'stalled'
  const currentIndex = PHASE_ORDER.indexOf(phase as UpdatePhase)
  return (
    <ol className="grid gap-2 sm:grid-cols-4">
      {PHASE_ORDER.map((step, index) => {
        // 停滞时既不算完成、也不让活动步继续转圈：转圈代表「还在进展」，与停滞提示自相矛盾。
        const done =
          !stalled &&
          !stalledPhase &&
          ((currentIndex > index && currentIndex !== -1) || isUpdateFinished(phase) || phase === 'success')
        const active = phase === step
        const activeStalled = active && stalled
        return (
          <li
            key={step}
            className={cn(
              'rounded-lg border px-3 py-2 text-xs transition-colors',
              done
                ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300'
                : activeStalled
                  ? 'border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-200'
                  : active
                    ? 'border-indigo-300 bg-indigo-50 text-indigo-700 dark:border-indigo-500/40 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'border-slate-200 bg-slate-50 text-slate-400 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-500',
            )}
          >
            <span className="flex items-center gap-1.5 font-medium">
              {done ? (
                <CheckCircleIcon className="h-3.5 w-3.5" />
              ) : activeStalled ? (
                <AlertTriangleIcon className="h-3.5 w-3.5" />
              ) : active ? (
                <SpinnerIcon className="h-3.5 w-3.5" />
              ) : (
                <span className="inline-flex h-3.5 w-3.5 items-center justify-center rounded-full border border-current text-[9px]">
                  {index + 1}
                </span>
              )}
              {t(`update.phase.${step}`)}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

export function UpdatePanel({ compact = false }: { compact?: boolean }) {
  const { t } = useI18n()
  const { toast } = useToast()
  // 升级面板的所有接口同样是 permSystemManage + RequireGlobalScope（router.go:229-232），
  // 无权限/非全局范围时连轮询和「恢复进行中升级」的状态请求都不发，避免打开弹窗就刷 403。
  const allowed = useUpdateAccess()
  const query = usePolling(api.checkForUpdate, 600_000, allowed)
  const info = query.data
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [status, setStatus] = useState<UpdateStatus | null>(null)
  const [running, setRunning] = useState(false)
  const [waiting, setWaiting] = useState(false)
  const [waitSeconds, setWaitSeconds] = useState(0)
  const [recoveredVersion, setRecoveredVersion] = useState<string | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [upgradeDisabled, setUpgradeDisabled] = useState(false)
  // 本地停滞计时：距上一次「真实进度变化」多少秒（与 2s 轮询、服务可达性都解耦）。
  const [noProgressSec, setNoProgressSec] = useState(0)
  const [retryChecking, setRetryChecking] = useState(false)
  const lastProgressAtRef = useRef(Date.now())
  const progressKeyRef = useRef('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [applying, setApplying] = useState(false)
  // 「从本地文件升级」的独立状态机：选文件 → 二次确认 → 上传（带进度/可取消）→ 202 后交给既有轮询
  const [pendingFile, setPendingFile] = useState<File | null>(null)
  const [uploadConfirmOpen, setUploadConfirmOpen] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [uploadPct, setUploadPct] = useState(0)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const uploadAbortRef = useRef<AbortController | null>(null)
  const startedRef = useRef(false)

  // 面板打开时恢复进行中的升级（例如刷新页面后）
  useEffect(() => {
    if (!allowed) return
    if (startedRef.current) return
    startedRef.current = true
    api
      .updateStatus()
      .then((current) => {
        if (!current.phase || current.phase === 'idle') return
        setStatus(current)
        if (isUpdateRunning(current.phase)) {
          progressKeyRef.current = updateProgressKey(current)
          lastProgressAtRef.current = Date.now()
          setNoProgressSec(0)
          setRunning(true)
        }
      })
      .catch(() => {
        /* 升级接口未就绪时忽略 */
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [allowed])

  // 2s 轮询升级状态
  useEffect(() => {
    if (!running) return
    let cancelled = false
    const tick = async () => {
      try {
        const current = await api.updateStatus()
        if (cancelled) return
        setStatus(current)
        // 只有阶段/百分比/文案/备份文件真的变了才算「有进展」；重复轮询同一快照不刷新计时。
        const progressKey = updateProgressKey(current)
        if (progressKey !== progressKeyRef.current) {
          progressKeyRef.current = progressKey
          lastProgressAtRef.current = Date.now()
          setNoProgressSec(0)
        }
        if (isUpdateFinished(current.phase)) {
          setRunning(false)
          setWaiting(false)
          void query.refresh()
          if (current.phase === 'success') {
            toast(t('update.successToast', { version: current.target_version ?? info?.latest ?? '' }), 'success')
          }
        }
      } catch {
        if (cancelled) return
        // 重启期间状态接口不可达：切到等待重连
        setWaiting(true)
      }
    }
    void tick()
    const timer = window.setInterval(tick, 2_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running])

  // 本地停滞计时：running 期间每秒重算「距上次真实进展多久」。独立于状态轮询，
  // 这样状态接口不可达（waiting）时计时仍然走着，20 分钟后照样能停止无限转圈。
  useEffect(() => {
    if (!running) {
      setNoProgressSec(0)
      return
    }
    const timer = window.setInterval(() => {
      setNoProgressSec(Math.max(0, Math.floor((Date.now() - lastProgressAtRef.current) / 1_000)))
    }, 1_000)
    return () => window.clearInterval(timer)
  }, [running])

  // 等待服务重启：轮询 /healthz，恢复后刷新版本
  useEffect(() => {
    if (!waiting) return
    let cancelled = false
    setWaitSeconds(0)
    const timer = window.setInterval(async () => {
      if (cancelled) return
      setWaitSeconds((seconds) => seconds + 2)
      try {
        const health = await api.health()
        if (cancelled) return
        setWaiting(false)
        setRunning(false)
        setRecoveredVersion(health.version || null)
        void query.refresh()
        try {
          const current = await api.updateStatus()
          setStatus(current)
        } catch {
          /* ignore */
        }
      } catch {
        /* 服务还没起来，继续等待 */
      }
    }, 2_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [waiting])

  const handleCheck = async () => {
    setChecking(true)
    setCheckError(null)
    try {
      await api.refreshUpdateCheck()
      await query.refresh()
    } catch (caught) {
      setCheckError(errorMessage(caught, t('update.checkFailed')))
    } finally {
      setChecking(false)
    }
  }

  const handleApply = async () => {
    if (!info?.latest) return
    setApplying(true)
    setApplyError(null)
    try {
      await api.applyUpdate({ version: info.latest })
      setConfirmOpen(false)
      setRunning(true)
      setWaiting(false)
      setRecoveredVersion(null)
      progressKeyRef.current = ''
      lastProgressAtRef.current = Date.now()
      setNoProgressSec(0)
      setStatus({
        job_id: null,
        phase: 'downloading',
        target_version: info.latest,
        current_version: info.current,
        message: null,
        error: null,
        rolled_back: false,
        backup_file: null,
        progress: null,
        started_at: Date.now(),
        finished_at: null,
        stalled: false,
        phase_started_at: Date.now(),
        phase_elapsed_s: 0,
      })
      toast(t('update.startedToast', { version: info.latest }), 'info')
    } catch (caught) {
      const message = errorMessage(caught, t('update.applyFailed'))
      setApplyError(message)
      if (message.includes('403') || /disabled|未开启|禁用/i.test(message)) setUpgradeDisabled(true)
      setConfirmOpen(false)
    } finally {
      setApplying(false)
    }
  }

  /**
   * 选中本地文件：先做大小拦截（<1MB / >200MB 直接提示、不发请求），
   * 再立刻弹二次确认——用户点「确认升级」才真正上传，避免误选一个大文件就开始替换二进制。
   */
  const handleFilePicked = (event: React.ChangeEvent<HTMLInputElement>) => {
    const picked = event.target.files?.[0] ?? null
    // 清空 value：否则「取消上传后又选同一个文件」不会触发 onChange（浏览器认为值没变）。
    event.target.value = ''
    if (!picked) return
    if (picked.size < MIN_UPLOAD_BYTES) {
      toast(
        t('update.uploadTooSmall', {
          size: formatBytes(picked.size),
          min: formatBytes(MIN_UPLOAD_BYTES, 0),
        }),
        'error',
      )
      return
    }
    if (picked.size > MAX_UPLOAD_BYTES) {
      toast(
        t('update.uploadTooLarge', {
          size: formatBytes(picked.size),
          max: formatBytes(MAX_UPLOAD_BYTES, 0),
        }),
        'error',
      )
      return
    }
    setUploadError(null)
    setPendingFile(picked)
    setUploadConfirmOpen(true)
  }

  const handleUpload = async () => {
    const file = pendingFile
    if (!file) return
    const controller = new AbortController()
    uploadAbortRef.current = controller
    setUploading(true)
    setUploadPct(0)
    setUploadError(null)
    setApplyError(null)
    // 先关确认弹窗再上传：Modal 会盖住页面，进度条与「取消上传」必须露出来才谈得上可取消。
    setUploadConfirmOpen(false)
    try {
      const result = await api.uploadUpdate(file, {
        signal: controller.signal,
        onProgress: (pct) => setUploadPct(pct),
      })
      setPendingFile(null)
      // 202 = job 已入队：立刻切到既有的 job phase 视图，之后由上面的 2s 轮询接管展示。
      const started = result.status
      setStatus({
        ...started,
        phase: started.phase && started.phase !== 'idle' ? started.phase : 'downloading',
        // 上传的版本号要等后端读完 --version 才知道；先用文件名占位，下一次轮询就会被真实值覆盖。
        target_version: started.target_version ?? file.name,
      })
      setRunning(true)
      setWaiting(false)
      setRecoveredVersion(null)
      progressKeyRef.current = updateProgressKey(started)
      lastProgressAtRef.current = Date.now()
      setNoProgressSec(0)
      toast(t('update.startedToast', { version: started.target_version ?? file.name }), 'info')
    } catch (caught) {
      setUploadConfirmOpen(false)
      if (isAbortError(caught)) {
        // 用户主动取消：不轮询（running 从未置 true），也不报错。
        setPendingFile(null)
        setUploadPct(0)
        toast(t('update.uploadCancelled'), 'info')
        return
      }
      setUploadError(errorMessage(caught, t('update.applyFailed')))
    } finally {
      setUploading(false)
      uploadAbortRef.current = null
    }
  }

  const handleCancelUpload = () => {
    uploadAbortRef.current?.abort()
  }

  /**
   * 停滞后的手动重试：只做一次权威状态查询，不盲目重新发起升级。
   * - 已终态（含服务端 stalled）→ 停止轮询，交给失败/成功面板；
   * - 仍在跑 → 保留轮询并重置本地计时（用户已看到告警，再卡 20 分钟才重新提示）；
   * - idle/未知 → 任务已不在内存，停止转圈；
   * - 接口不可达 → 走既有「等待服务重启」探活链路。
   */
  const handleRetryStall = async () => {
    setRetryChecking(true)
    try {
      const current = await api.updateStatus()
      setStatus(current)
      progressKeyRef.current = updateProgressKey(current)
      lastProgressAtRef.current = Date.now()
      setNoProgressSec(0)
      if (isUpdateFinished(current.phase)) {
        setRunning(false)
        setWaiting(false)
        void query.refresh()
        if (current.phase === 'success') {
          toast(t('update.successToast', { version: current.target_version ?? info?.latest ?? '' }), 'success')
        }
      } else if (isUpdateRunning(current.phase)) {
        setWaiting(false)
        toast(t('update.stalledStillRunning'), 'info')
      } else {
        setRunning(false)
        setWaiting(false)
        void query.refresh()
      }
    } catch {
      // 失败不许静默：用户点了重试，至少要知道这次查询没成功。
      // 状态接口不可达：交给 /healthz 探活，恢复后会刷新版本并停止转圈。
      toast(t('update.stalledRetryFailed'), 'error')
      setWaiting(true)
    } finally {
      setRetryChecking(false)
    }
  }

  const current = info?.current ?? status?.current_version ?? '—'
  const latest = info?.latest ?? status?.target_version ?? '—'
  const phase = status?.phase
  const stalledPhase = phase === 'stalled'
  const failed = phase === 'failed' || phase === 'rolled_back' || stalledPhase
  // 服务端半程预警（stalled）或本地 20 分钟无进展，都算「疑似停滞」：提示 + 重试，不再只转圈。
  const stalled = running && (Boolean(status?.stalled) || noProgressSec * 1_000 >= LOCAL_STALL_MS)
  const stalledMinutes = stallMinutes(noProgressSec, status?.phase_elapsed_s)
  const stalledPhaseLabel = updatePhaseLabel(phase, t)
  const succeeded = phase === 'success' || Boolean(recoveredVersion)
  const disabled = upgradeDisabled || info?.upgrade_enabled === false
  // 「本平台没有可用升级包」只有在「检查本身成功」时才是事实：
  // 检查失败（受限网络 / GitHub 不可达）时后端返回的是零值 UpdateInfo（assets 为空、
  // upgradable=false、error 非空），把它当成「平台不支持」会把唯一可用的通道——
  // 本地上传——一起关掉（线上实测缺陷：内网机器点不了「从本地文件升级」）。
  // upgrade_note 非空才是平台层面的硬阻断（如 Windows 无法替换运行中的可执行文件）。
  const platformBlocked = Boolean(info?.upgrade_note)
  const checkFailed = Boolean(info?.error)
  const notUpgradable = !platformBlocked && !checkFailed && info?.upgradable === false
  // uploading 也算「进行中」：上传途中再点「立即升级」会并发起第二条升级链，后端虽然会 409，但前端不该给这个机会。
  const canApply = Boolean(
    info?.has_update && info.latest && !disabled && !notUpgradable && !running && !succeeded && !uploading,
  )
  /**
   * 本地上传是否可用：与「立即升级」同一套门控（权限/全局范围由外层 allowed 决定，
   * 平台不支持或服务端禁用则禁用并显示既有 upgrade_note）。
   * 注意这里**不**要求 info.has_update——恰恰相反，受限网络下查不到更新时，
   * 本地上传才是唯一可用的升级通道，所以 info 为空也放行，让后端做权威校验。
   */
  const canUpload = allowed && !platformBlocked && !disabled && !running && !succeeded && !applying && !uploading

  const intervalText = useMemo(() => {
    if (!info?.check_interval) return t('update.intervalDefault')
    return t('update.intervalValue', { interval: info.check_interval })
  }, [info?.check_interval, t])

  return (
    <div className={cn('space-y-4', compact ? 'text-sm' : '')}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-slate-800 dark:text-slate-100">
            <ArrowUpCircleIcon className="h-4 w-4 text-indigo-500" />
            {t('update.currentVersion')}
            <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:bg-slate-800 dark:text-slate-200">
              {current}
            </code>
            {info?.has_update ? (
              <>
                <span className="text-slate-400">→</span>
                <code className="rounded bg-indigo-50 px-1.5 py-0.5 font-mono text-xs font-semibold text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300">
                  {latest}
                </code>
                <span className="rounded-full bg-indigo-600 px-2 py-0.5 text-[11px] font-medium text-white">
                  {t('update.available')}
                </span>
              </>
            ) : null}
          </p>
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            {t('update.intervalLabel')}：{intervalText}
            {info?.checked_at
              ? ` · ${t('update.lastChecked', { time: formatRelativeTime(info.checked_at) })}`
              : ''}
          </p>
        </div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => void handleCheck()}
          loading={checking}
          icon={<RefreshIcon className="h-3.5 w-3.5" />}
        >
          {t('update.checkNow')}
        </Button>
      </div>

      {info?.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          <span className="break-words">{t('update.checkFailedInline', { error: info.error })}</span>
          <Button size="sm" variant="secondary" onClick={() => void handleCheck()} loading={checking}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      {checkError ? (
        <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {t('update.checkFailedInline', { error: checkError })}
        </p>
      ) : null}

      {disabled ? (
        <div className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-xs text-slate-600 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-300">
          <p className="font-medium">{t('update.disabledTitle')}</p>
          <p className="mt-0.5">{t('update.disabledReason')}</p>
        </div>
      ) : null}

      {!info && query.loading ? (
        <p className="flex items-center gap-2 py-4 text-xs text-slate-400 dark:text-slate-500">
          <SpinnerIcon className="h-3.5 w-3.5" />
          {t('common.loading')}
        </p>
      ) : info && !info.has_update && !info.error ? (
        <div className="flex items-center gap-2 rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300">
          <CheckCircleIcon className="h-4 w-4" />
          {t('update.upToDate', { version: current })}
        </div>
      ) : null}

      {info?.has_update && !running && !succeeded ? (
        <div className="space-y-3">
          <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-slate-500 dark:text-slate-400">
              <span>
                {t('update.releasedAt')}：
                {info.published_at ? formatDateTime(info.published_at) : '—'}
              </span>
              {info.release_url ? (
                <a
                  href={info.release_url}
                  target="_blank"
                  rel="noreferrer"
                  className="font-medium text-indigo-600 hover:underline dark:text-indigo-400"
                >
                  {t('update.viewRelease')} ↗
                </a>
              ) : null}
            </div>
            <p className="mt-2 text-xs font-medium text-slate-700 dark:text-slate-200">
              {t('update.releaseNotes')}
            </p>
            <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md bg-slate-950 p-3 font-mono text-[11px] leading-relaxed text-slate-100">
              {info.notes?.trim() || t('update.noNotes')}
            </pre>
          </div>

          <div className="rounded-lg border border-indigo-200 bg-indigo-50/70 p-3 text-xs leading-relaxed text-indigo-900 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
            <p className="font-semibold">{t('update.riskTitle')}</p>
            <p className="mt-1">{t('update.riskBody')}</p>
          </div>

          {info.schema_changed ? (
            <div className="rounded-lg border border-rose-300 bg-rose-50 p-3 text-xs leading-relaxed text-rose-900 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200">
              <p className="flex items-center gap-1.5 font-semibold">
                <AlertTriangleIcon className="h-4 w-4" />
                {t('update.schemaWarningTitle')}
              </p>
              <p className="mt-1">{t('update.schemaWarningBody')}</p>
            </div>
          ) : null}

          {notUpgradable ? (
            <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
              {info?.upgrade_note || t('update.notUpgradable')}
            </p>
          ) : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              onClick={() => setConfirmOpen(true)}
              disabled={!canApply}
              icon={<DownloadIcon className="h-4 w-4" />}
            >
              {t('update.apply')}
            </Button>
            <span className="text-[11px] text-slate-400 dark:text-slate-500">{t('update.applyHint')}</span>
          </div>
        </div>
      ) : null}

      {/* 从本地文件升级：不受「是否有新版本」限制——查不到更新（受限网络）时它才是可用的升级通道。 */}
      {allowed && !running && !succeeded && !uploading ? (
        <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => fileInputRef.current?.click()}
              disabled={!canUpload}
              icon={<UploadIcon className="h-4 w-4" />}
            >
              {t('update.uploadAction')}
            </Button>
            <span className="text-[11px] text-slate-400 dark:text-slate-500">
              {t('update.uploadFileHint')}
            </span>
          </div>
          {platformBlocked ? (
            <p className="mt-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
              {info?.upgrade_note || t('update.notUpgradable')}
            </p>
          ) : null}
          {checkFailed && !platformBlocked ? (
            <p className="mt-2 text-[11px] text-slate-500 dark:text-slate-400">{t('update.uploadWorksOffline')}</p>
          ) : null}
          {/*
            不把 accept 设成一两个 MIME：解压后的 leadstack 二进制在 macOS/Linux 上往往没有扩展名
            （分发出来的归档才是 .tar.gz），设死会让文件选择器把它整个灰掉、用户以为功能没做。
            所以 accept 里带上通配（见下方 input），.bin 与 octet-stream 只是顺带给出提示。
          */}
          <input
            ref={fileInputRef}
            type="file"
            className="hidden"
            accept=".bin,application/octet-stream,*/*"
            onChange={handleFilePicked}
          />
        </div>
      ) : null}

      {uploading ? (
        <div className="space-y-2 rounded-lg border border-indigo-200 bg-indigo-50/70 p-3 text-xs dark:border-indigo-500/30 dark:bg-indigo-500/10">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-1.5 font-medium text-indigo-900 dark:text-indigo-200">
              <SpinnerIcon className="h-3.5 w-3.5" />
              {t('update.uploadProgress', { pct: uploadPct })}
            </span>
            <Button size="sm" variant="secondary" onClick={handleCancelUpload}>
              {t('common.cancel')}
            </Button>
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-indigo-200 dark:bg-indigo-500/30">
            <div
              className="h-full rounded-full bg-indigo-600 transition-[width] duration-200 dark:bg-indigo-400"
              style={{ width: `${uploadPct}%` }}
            />
          </div>
          {pendingFile ? (
            <p className="break-all text-indigo-900/80 dark:text-indigo-200/80">
              {pendingFile.name}（{formatBytes(pendingFile.size)}）
            </p>
          ) : null}
        </div>
      ) : null}

      {uploadError ? (
        <p className="break-words rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {uploadError}
        </p>
      ) : null}

      {applyError ? (
        <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {applyError}
        </p>
      ) : null}

      {running ? (
        <div className="space-y-3 rounded-lg border border-slate-200 p-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm font-medium text-slate-800 dark:text-slate-100">
              {t('update.progressTitle', { version: status?.target_version ?? latest })}
            </p>
            <span className="text-[11px] text-slate-400 dark:text-slate-500">
              {t('update.progressHint')}
            </span>
          </div>
          <PhaseStepper status={status} stalled={stalled} />
          {status?.message ? (
            <p className="text-xs text-slate-500 dark:text-slate-400">{status.message}</p>
          ) : null}
          {status?.backup_file ? (
            <p className="break-all font-mono text-[11px] text-slate-400 dark:text-slate-500">
              {t('update.backupFile')}：{status.backup_file}
            </p>
          ) : null}
          {stalled ? (
            <div
              role="alert"
              className="flex flex-wrap items-start justify-between gap-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-200"
            >
              <div className="flex min-w-0 items-start gap-2">
                <AlertTriangleIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <div className="min-w-0">
                  <p className="font-medium">{t('update.stalledTitle')}</p>
                  <p className="mt-0.5 break-words leading-relaxed">
                    {t('update.stalledHint', { minutes: stalledMinutes, phase: stalledPhaseLabel })}
                  </p>
                </div>
              </div>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void handleRetryStall()}
                loading={retryChecking}
                icon={<RefreshIcon className="h-3.5 w-3.5" />}
              >
                {t('update.stalledRetry')}
              </Button>
            </div>
          ) : null}
          {waiting ? (
            <div className="flex items-start gap-2 rounded-md border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-900 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
              {stalled ? (
                <AlertTriangleIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              ) : (
                <SpinnerIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              )}
              <div>
                <p className="font-medium">{t('update.waitingTitle')}</p>
                <p className="mt-0.5 leading-relaxed">{t('update.waitingHint', { seconds: waitSeconds })}</p>
              </div>
            </div>
          ) : null}
        </div>
      ) : null}

      {waiting && !running ? (
        <div className="rounded-md border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-900 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
          <p className="font-medium">{t('update.waitingTitle')}</p>
          <p className="mt-0.5">{t('update.waitingHint', { seconds: waitSeconds })}</p>
        </div>
      ) : null}

      {failed ? (
        <div className="rounded-lg border border-rose-300 bg-rose-50 p-3 text-xs leading-relaxed text-rose-900 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200">
          <p className="flex items-center gap-1.5 font-semibold">
            <AlertTriangleIcon className="h-4 w-4" />
            {status?.rolled_back || phase === 'rolled_back'
              ? t('update.rollbackTitle')
              : stalledPhase
                ? t('update.stalledFailedTitle')
                : t('update.failedTitle')}
          </p>
          <p className="mt-1 break-words">{status?.error ?? status?.message ?? t('update.failedNoReason')}</p>
          {status?.rolled_back || phase === 'rolled_back' ? (
            <p className="mt-1 font-medium">
              {t('update.rollbackHint', { version: status?.current_version ?? current })}
            </p>
          ) : null}
          {stalledPhase ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <p>{t('update.stalledFailedHint')}</p>
              {canApply ? (
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => setConfirmOpen(true)}
                  icon={<DownloadIcon className="h-3.5 w-3.5" />}
                >
                  {t('update.stalledRetryApply')}
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      {succeeded ? (
        <div className="rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-xs text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200">
          <p className="flex items-center gap-1.5 font-semibold">
            <CheckCircleIcon className="h-4 w-4" />
            {t('update.successTitle', { version: recoveredVersion ?? status?.target_version ?? latest })}
          </p>
          <p className="mt-1">{t('update.successHint')}</p>
        </div>
      ) : null}

      <ConfirmDialog
        open={uploadConfirmOpen}
        title={t('update.uploadConfirmTitle')}
        description={t('update.uploadConfirmBody', {
          file: pendingFile?.name ?? '—',
          size: formatBytes(pendingFile?.size),
          current,
        })}
        confirmLabel={t('update.confirmApply')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        onClose={() => {
          if (uploading) return
          setUploadConfirmOpen(false)
          setPendingFile(null)
        }}
        onConfirm={() => void handleUpload()}
      >
        <div className="space-y-1.5">
          <p className="break-all">
            {t('update.confirmTarget')}：
            <span className="font-mono font-semibold">{pendingFile?.name ?? '—'}</span>
          </p>
          <p>
            {formatBytes(pendingFile?.size)}
            {' · '}
            {t('update.confirmBackup')}
          </p>
          <p>{t('update.confirmRollback')}</p>
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={confirmOpen}
        title={t('update.confirmTitle')}
        description={t('update.confirmDescription', { version: latest })}
        confirmLabel={t('update.confirmApply')}
        tone="danger"
        loading={applying}
        onClose={() => setConfirmOpen(false)}
        onConfirm={() => void handleApply()}
      >
        <div className="space-y-1.5">
          <p>
            {t('update.confirmTarget')}：<span className="font-mono font-semibold">{latest}</span>
          </p>
          <p>{t('update.confirmBackup')}</p>
          <p>{t('update.confirmRollback')}</p>
          {info?.schema_changed ? (
            <p className="font-medium text-rose-700 dark:text-rose-300">
              {t('update.confirmSchema')}
            </p>
          ) : null}
        </div>
      </ConfirmDialog>
    </div>
  )
}

export function UpdateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('update.title')}
      description={t('update.description')}
      size="lg"
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <UpdatePanel />
    </Modal>
  )
}

/** 设置页「版本与升级」卡片：检查间隔、立即检查、升级入口、禁用原因。 */
export function UpdateSettingsCard({ onOpenDetails }: { onOpenDetails: () => void }) {
  const { t } = useI18n()
  const update = useUpdateNotice()
  const [checking, setChecking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const info = update.info
  const disabled = info?.upgrade_enabled === false

  const handleCheck = async () => {
    setChecking(true)
    setError(null)
    try {
      await api.refreshUpdateCheck()
      await update.refresh()
    } catch (caught) {
      setError(errorMessage(caught, t('update.checkFailed')))
    } finally {
      setChecking(false)
    }
  }

  return (
    <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <PackageIcon className="h-4 w-4 text-indigo-500" />
            {t('update.settingsTitle')}
          </h2>
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            {t('update.settingsDescription')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void handleCheck()}
            loading={checking}
            icon={<RefreshIcon className="h-3.5 w-3.5" />}
          >
            {t('update.checkNow')}
          </Button>
          <Button size="sm" onClick={onOpenDetails} disabled={disabled}>
            {t('update.openDetails')}
          </Button>
        </div>
      </div>

      <dl className="mt-3 grid gap-2 text-xs sm:grid-cols-3">
        <div className="rounded-md bg-slate-50 px-3 py-2 dark:bg-slate-950/60">
          <dt className="text-slate-500 dark:text-slate-400">{t('update.currentVersion')}</dt>
          <dd className="mt-0.5 font-mono font-medium text-slate-800 dark:text-slate-100">
            {info?.current ?? '—'}
          </dd>
        </div>
        <div className="rounded-md bg-slate-50 px-3 py-2 dark:bg-slate-950/60">
          <dt className="text-slate-500 dark:text-slate-400">{t('update.latestVersion')}</dt>
          <dd className="mt-0.5 flex items-center gap-1.5 font-mono font-medium text-slate-800 dark:text-slate-100">
            {info?.latest ?? '—'}
            {info?.has_update ? (
              <span className="rounded-full bg-indigo-600 px-1.5 py-0.5 text-[10px] font-medium text-white">
                {t('update.available')}
              </span>
            ) : null}
          </dd>
        </div>
        <div className="rounded-md bg-slate-50 px-3 py-2 dark:bg-slate-950/60">
          <dt className="text-slate-500 dark:text-slate-400">{t('update.checkInterval')}</dt>
          <dd className="mt-0.5 text-slate-800 dark:text-slate-100">
            {info?.check_interval || t('update.intervalDefault')}
          </dd>
        </div>
      </dl>

      {info?.checked_at ? (
        <p className="mt-2 text-[11px] text-slate-400 dark:text-slate-500">
          {t('update.lastChecked', { time: formatRelativeTime(info.checked_at) })}
        </p>
      ) : null}

      {info?.error ? (
        <p className="mt-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {t('update.checkFailedInline', { error: info.error })}
        </p>
      ) : null}
      {error ? (
        <p className="mt-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {t('update.checkFailedInline', { error })}
        </p>
      ) : null}

      {disabled ? (
        <p className="mt-2 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-xs text-slate-600 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-300">
          <span className="font-medium">{t('update.disabledTitle')}</span>
          {' · '}
          {t('update.disabledReason')}
        </p>
      ) : null}
    </section>
  )
}
