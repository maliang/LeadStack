import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type UpdatePhase, type UpdateStatus } from '../lib/api'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button } from './Button'
import { ConfirmDialog } from './ConfirmDialog'
import { CheckCircleIcon, AlertTriangleIcon, ArrowUpCircleIcon, DownloadIcon, PackageIcon, RefreshIcon, SpinnerIcon } from './Icons'
import { Modal } from './Modal'
import { useToast } from './Toast'

const ACTIVE_PHASES: UpdatePhase[] = ['downloading', 'backing_up', 'replacing', 'restarting']
const PHASE_ORDER: UpdatePhase[] = ['downloading', 'backing_up', 'replacing', 'restarting']

export function isUpdateRunning(phase: UpdatePhase | undefined): boolean {
  return Boolean(phase && ACTIVE_PHASES.includes(phase))
}

export function isUpdateFinished(phase: UpdatePhase | undefined): boolean {
  return phase === 'success' || phase === 'failed' || phase === 'rolled_back'
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

function PhaseStepper({ status }: { status: UpdateStatus | null }) {
  const { t } = useI18n()
  const phase = status?.phase
  const currentIndex = PHASE_ORDER.indexOf(phase as UpdatePhase)
  return (
    <ol className="grid gap-2 sm:grid-cols-4">
      {PHASE_ORDER.map((step, index) => {
        const done =
          (currentIndex > index && currentIndex !== -1) || isUpdateFinished(phase) || phase === 'success'
        const active = phase === step
        return (
          <li
            key={step}
            className={cn(
              'rounded-lg border px-3 py-2 text-xs transition-colors',
              done
                ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300'
                : active
                  ? 'border-indigo-300 bg-indigo-50 text-indigo-700 dark:border-indigo-500/40 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 bg-slate-50 text-slate-400 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-500',
            )}
          >
            <span className="flex items-center gap-1.5 font-medium">
              {done ? (
                <CheckCircleIcon className="h-3.5 w-3.5" />
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
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [applying, setApplying] = useState(false)
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
        if (isUpdateRunning(current.phase)) setRunning(true)
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

  const current = info?.current ?? status?.current_version ?? '—'
  const latest = info?.latest ?? status?.target_version ?? '—'
  const phase = status?.phase
  const failed = phase === 'failed' || phase === 'rolled_back'
  const succeeded = phase === 'success' || Boolean(recoveredVersion)
  const disabled = upgradeDisabled || info?.upgrade_enabled === false
  const notUpgradable = info?.upgradable === false
  const canApply = Boolean(info?.has_update && info.latest && !disabled && !notUpgradable && !running && !succeeded)

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
          <PhaseStepper status={status} />
          {status?.message ? (
            <p className="text-xs text-slate-500 dark:text-slate-400">{status.message}</p>
          ) : null}
          {status?.backup_file ? (
            <p className="break-all font-mono text-[11px] text-slate-400 dark:text-slate-500">
              {t('update.backupFile')}：{status.backup_file}
            </p>
          ) : null}
          {waiting ? (
            <div className="flex items-start gap-2 rounded-md border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-900 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
              <SpinnerIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" />
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
            {status?.rolled_back || phase === 'rolled_back' ? t('update.rollbackTitle') : t('update.failedTitle')}
          </p>
          <p className="mt-1 break-words">{status?.error ?? status?.message ?? t('update.failedNoReason')}</p>
          {status?.rolled_back || phase === 'rolled_back' ? (
            <p className="mt-1 font-medium">
              {t('update.rollbackHint', { version: status?.current_version ?? current })}
            </p>
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
