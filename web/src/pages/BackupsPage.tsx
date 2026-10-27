import { useEffect, useMemo, useRef, useState } from 'react'
import {
  api,
  ApiError,
  type BackupInfo,
  type BackupSettings,
  type BackupVerifyResult,
  type RestoreStagedResult,
  type SystemRestoreStatus,
} from '../lib/api'
import { usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import {
  classifyBackupError,
  formatBackupInterval,
  isValidGoDuration,
  parseGoDurationSeconds,
  type BackupErrorInsight,
} from '../lib/backup'
import { cn } from '../lib/cn'
import { formatBytes, formatDateTime, formatRelativeTime, truncateMiddle } from '../lib/format'
import { BackupErrorCard } from '../components/BackupErrorCard'
import { BackupVerifyDialog } from '../components/BackupVerifyDialog'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CopyButton } from '../components/CopyButton'
import { EmptyState } from '../components/EmptyState'
import { EnableSwitch } from '../components/EnableSwitch'
import {
  ArchiveIcon,
  CheckCircleIcon,
  DownloadIcon,
  KeyRoundIcon,
  RefreshIcon,
  ShieldCheckIcon,
  TrashIcon,
  UploadIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'

import { t } from '../i18n'
const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const INTERVAL_PRESETS = [
  { value: '6h', label: t('s.0838') },
  { value: '12h', label: t('s.0839') },
  { value: '24h', label: t('s.0002') },
]

const DEFAULT_SETTINGS: BackupSettings = {
  enabled: false,
  interval: '24h',
  retention: 7,
  include_artifacts: false,
}

/**
 * 重启控制端的三种安装方式（T-12）：并列展示、各带复制按钮，
 * 由用户按自己的安装方式选择，不用 navigator.platform 猜平台（契约 S-18）。
 */
const RESTART_COMMANDS = [
  { labelKey: 'backups.restartLabelLinux', commandKey: 'backups.restartCmdLinux' },
  { labelKey: 'backups.restartLabelDarwin', commandKey: 'backups.restartCmdDarwin' },
  { labelKey: 'backups.restartLabelDocker', commandKey: 'backups.restartCmdDocker' },
]

interface VerifyState {
  info: BackupInfo
  loading: boolean
  result: BackupVerifyResult | null
  insight: BackupErrorInsight | null
}

export default function BackupsPage() {
  const { toast } = useToast()
  const { can, scope } = useAuth()
  const canWrite = can('backup.write')
  const canDelete = can('backup.delete')
  const canRestore = can('backup.restore')
  /**
   * 备份与恢复整页都是全局操作：后端 /backups、/backup-settings、/system/backup/restore-status
   * 与恢复上传全部挂了 RequireGlobalScope（internal/api/router.go:107/109/180/181/186）。
   * 分组/服务器范围的角色即便有 backup.read / backup.restore 也只会拿到 403，所以这里把范围
   * 一起判：非全局范围不轮询、不渲染上传区，只给一条明确说明。否则页面会每 5s/10s/30s 静默
   * 打一次 403（用户看不到，只有网络面板一直红），而「已暂存」卡片也不会自动转成「已完成」。
   */
  const isGlobalScope = scope.mode === 'all'
  const canRestoreGlobal = canRestore && isGlobalScope
  // 备份列表 / 备份设置同样挂了 RequireGlobalScope（internal/api/router.go:107/109），
  // 非全局范围角色一进页面就会连续 403（列表 10s、设置 30s），所以一并停掉轮询。
  const backupsQuery = usePolling(api.backups, 10_000, isGlobalScope)
  const settingsQuery = usePolling(api.backupSettings, 30_000, isGlobalScope)

  const backups = backupsQuery.data ?? []

  // ---------- 立即备份 ----------
  const [createIncludeArtifacts, setCreateIncludeArtifacts] = useState(false)
  const [creating, setCreating] = useState(false)
  const [lastCreated, setLastCreated] = useState<BackupInfo | null>(null)

  // ---------- 列表操作 ----------
  const [downloading, setDownloading] = useState<string | null>(null)
  const [verifyState, setVerifyState] = useState<VerifyState | null>(null)
  const [deleting, setDeleting] = useState<BackupInfo | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  // ---------- 恢复 ----------
  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [restoreStage, setRestoreStage] = useState<'idle' | 'risk' | 'staged'>('idle')
  const [restoreLoading, setRestoreLoading] = useState(false)
  const [restoreInsight, setRestoreInsight] = useState<BackupErrorInsight | null>(null)
  const [restoreStaged, setRestoreStaged] = useState<RestoreStagedResult | null>(null)
  const [restoreConfirmOpen, setRestoreConfirmOpen] = useState(false)
  const [riskAcknowledged, setRiskAcknowledged] = useState(false)

  // ---------- 恢复状态回查（T-12 恢复闭环） ----------
  // 挂载后每 5s 轮询 GET /system/backup/restore-status（需 backup.restore 且全局范围）：
  // usePolling 在卸载时 clearInterval、页面不可见时跳过，pending=true→false 即「重启后已应用」。
  // 非全局范围角色后端必定 403，这里直接不启用，避免每 5 秒一次静默失败。
  const restoreStatusQuery = usePolling(api.systemRestoreStatus, 5_000, canRestoreGlobal)
  const restoreStatus = restoreStatusQuery.data
  // 归档 sha256 前 8 位（pending 时接口才返回，完成后需用暂存时的值回显）
  const [restoreDoneSha, setRestoreDoneSha] = useState<string | null>(null)
  const lastPendingRef = useRef<SystemRestoreStatus | null>(null)
  const wasPendingRef = useRef(false)

  useEffect(() => {
    if (!restoreStatus) return
    if (restoreStatus.pending) lastPendingRef.current = restoreStatus
    const wasPending = wasPendingRef.current
    wasPendingRef.current = restoreStatus.pending
    if (!wasPending || restoreStatus.pending) return

    // pending 由 true→false：控制端重启后已应用恢复，用归档 sha 前 8 位做完成提示
    const sha = (
      lastPendingRef.current?.archive_sha256_prefix ??
      restoreStaged?.sha256 ??
      ''
    ).slice(0, 8)
    lastPendingRef.current = null
    setRestoreDoneSha(sha)
    setRestoreStaged(null)
    setRestoreStage('idle')
    setRestoreFile(null)
    setRestoreInsight(null)
    toast(t('backups.restoreDone', { sha: sha || '—' }), 'success')
  }, [restoreStatus, restoreStaged, toast])

  // 待应用的恢复：以轮询结果为准，本地刚上传时先按本地状态出卡片，不必等下一次轮询
  const restorePending = restoreStage === 'staged' || restoreStatus?.pending === true

  const stagedSummary = useMemo(() => {
    if (restoreStaged) {
      return [
        restoreStaged.archive_name
          ? t('s.0897', { '0': restoreStaged.archive_name })
          : t('s.0898'),
        restoreStaged.size_bytes ? `（${formatBytes(restoreStaged.size_bytes)}）` : '',
        restoreStaged.schema_version !== null ? ` · schema ${restoreStaged.schema_version}` : '',
        restoreStaged.sha256 ? ` · sha256 ${truncateMiddle(restoreStaged.sha256, 14, 6)}` : '',
        restoreStaged.uploaded_at
          ? t('s.0899', { '0': formatDateTime(restoreStaged.uploaded_at) })
          : '',
        restoreStaged.staged_path ? t('s.0900', { '0': restoreStaged.staged_path }) : '',
      ].join('')
    }
    if (!restoreStatus) return ''
    return [
      t('s.0898'),
      restoreStatus.schema_version !== undefined ? ` · schema ${restoreStatus.schema_version}` : '',
      restoreStatus.archive_sha256_prefix ? ` · sha256 ${restoreStatus.archive_sha256_prefix}` : '',
      restoreStatus.staged_at ? t('s.0899', { '0': formatDateTime(restoreStatus.staged_at) }) : '',
    ].join('')
  }, [restoreStaged, restoreStatus])

  // ---------- 设置 ----------
  const [settingsForm, setSettingsForm] = useState<BackupSettings>(DEFAULT_SETTINGS)
  const [settingsDirty, setSettingsDirty] = useState(false)
  const [settingsError, setSettingsError] = useState<string | null>(null)
  const [savingSettings, setSavingSettings] = useState(false)

  useEffect(() => {
    if (!settingsQuery.data || settingsDirty) return
    setSettingsForm(settingsQuery.data)
  }, [settingsQuery.data, settingsDirty])

  const intervalSeconds = useMemo(
    () => parseGoDurationSeconds(settingsForm.interval),
    [settingsForm.interval],
  )
  const intervalIsCustom = useMemo(
    () =>
      !INTERVAL_PRESETS.some(
        (preset) => parseGoDurationSeconds(preset.value) === intervalSeconds,
      ),
    [intervalSeconds],
  )

  const updateSettings = (patch: Partial<BackupSettings>) => {
    setSettingsForm((current) => ({ ...current, ...patch }))
    setSettingsDirty(true)
    setSettingsError(null)
  }

  const refreshAll = async () => {
    await Promise.all([backupsQuery.refresh(), settingsQuery.refresh()])
  }

  const handleCreate = async () => {
    setCreating(true)
    try {
      const created = await api.createBackup(createIncludeArtifacts)
      setLastCreated(created)
      toast(
        t('s.0840', { '0': created.name, '1': formatBytes(created.size_bytes), '2': created.includes_artifacts ? t('backups.withArtifacts') : '' }),
        'success',
      )
      await backupsQuery.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('s.0841')), 'error')
    } finally {
      setCreating(false)
    }
  }

  const handleDownload = async (backup: BackupInfo) => {
    setDownloading(backup.name)
    try {
      const { blob, filename } = await api.downloadBackup(backup.name)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = filename
      document.body.appendChild(anchor)
      anchor.click()
      document.body.removeChild(anchor)
      window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
      toast(t('s.0842', { '0': filename }), 'success')
    } catch (caught) {
      toast(errorMessage(caught, t('s.0843')), 'error')
    } finally {
      setDownloading(null)
    }
  }

  const handleVerify = async (backup: BackupInfo) => {
    setVerifyState({ info: backup, loading: true, result: null, insight: null })
    try {
      const result = await api.verifyBackup(backup.name)
      if (result.ok) {
        setVerifyState({ info: backup, loading: false, result, insight: null })
      } else {
        setVerifyState({
          info: backup,
          loading: false,
          result: null,
          insight: classifyBackupError(result.error ?? t('s.0844')),
        })
      }
    } catch (caught) {
      setVerifyState({
        info: backup,
        loading: false,
        result: null,
        insight: classifyBackupError(caught),
      })
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    setDeleteError(null)
    try {
      await api.deleteBackup(deleting.name)
      toast(t('s.0845', { '0': deleting.name }), 'success')
      setDeleting(null)
      await backupsQuery.refresh()
    } catch (caught) {
      setDeleteError(errorMessage(caught, t('s.0846')))
    } finally {
      setDeleteLoading(false)
    }
  }

  const handleRestoreFile = (file: File | null) => {
    setRestoreFile(file)
    setRestoreInsight(null)
    setRestoreStaged(null)
    setRestoreDoneSha(null)
    setRestoreStage(file ? 'risk' : 'idle')
    setRiskAcknowledged(false)
  }

  const handleRestoreSubmit = async () => {
    if (!restoreFile) return
    setRestoreLoading(true)
    setRestoreInsight(null)
    try {
      const staged = await api.restoreBackup(restoreFile)
      setRestoreStaged(staged)
      setRestoreStage('staged')
      setRestoreConfirmOpen(false)
      setRestoreDoneSha(null)
      toast(t('s.0847'), 'success')
      // 立刻回查一次状态，让「待应用 + 重启命令」卡片马上出现
      void restoreStatusQuery.refresh()
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        // 409 = 已有待应用的恢复（后端 message 已说明原因），按错误 toast 展示
        toast(errorMessage(caught, t('s.0847')), 'error')
        // 清掉本次选择，页面回到「待应用」单卡片状态，避免半截风险提示与待应用提示并存
        setRestoreStage('idle')
        setRestoreFile(null)
        setRestoreConfirmOpen(false)
        void restoreStatusQuery.refresh()
      } else {
        setRestoreInsight(classifyBackupError(caught))
        setRestoreStage('risk')
        setRestoreConfirmOpen(false)
      }
    } finally {
      setRestoreLoading(false)
    }
  }

  const handleSaveSettings = async () => {
    const interval = settingsForm.interval.trim()
    if (!isValidGoDuration(interval)) {
      setSettingsError(t('s.0848'))
      return
    }
    if (!Number.isInteger(settingsForm.retention) || settingsForm.retention < 1) {
      setSettingsError(t('s.0849'))
      return
    }
    setSavingSettings(true)
    setSettingsError(null)
    try {
      const saved = await api.updateBackupSettings({
        ...settingsForm,
        interval,
        retention: settingsForm.retention,
      })
      setSettingsForm(saved)
      setSettingsDirty(false)
      toast(t('s.0850'), 'success')
      void settingsQuery.refresh()
    } catch (caught) {
      setSettingsError(errorMessage(caught, t('s.0851')))
    } finally {
      setSavingSettings(false)
    }
  }

  // 非全局范围：整页的备份/恢复接口都不可用（见 isGlobalScope 注释），只给一条明确说明，
  // 不渲染备份列表/设置/上传区，也完全不发请求 —— 避免一串静默 403。
  if (!isGlobalScope) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('s.0858')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{t('s.0859')}</p>
        </div>
        <section className="rounded-xl border border-amber-300 bg-amber-50/70 p-4 shadow-sm dark:border-amber-500/40 dark:bg-amber-500/10">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-amber-900 dark:text-amber-200">
            <ShieldCheckIcon className="h-4 w-4 text-amber-600 dark:text-amber-300" />
            {t('backups.needsGlobalScopeTitle')}
          </h2>
          <p className="mt-1.5 text-xs leading-relaxed text-amber-800 dark:text-amber-200/90">
            {t('backups.needsGlobalScope')}
          </p>
        </section>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* 口令提示：固定在页面顶部，必须是显眼位置 */}
      <div className="sticky top-14 z-30 -mx-4 border-y border-amber-300 bg-amber-50/95 px-4 py-3 backdrop-blur sm:-mx-6 sm:px-6 dark:border-amber-500/40 dark:bg-amber-500/10">
        <div className="mx-auto flex w-full max-w-7xl items-start gap-3">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300">
            <KeyRoundIcon className="h-5 w-5" />
          </span>
          <div className="min-w-0">
            <p className="text-sm font-semibold text-amber-900 dark:text-amber-100">
              {t('s.0852')}
            </p>
            <p className="mt-0.5 text-xs leading-relaxed text-amber-800/90 dark:text-amber-200/90">
              {t('s.0853')}{' '}
              <code className="rounded bg-amber-100 px-1 py-0.5 font-mono text-[11px] text-amber-900 dark:bg-amber-500/20 dark:text-amber-100">
                backup.pass
              </code>
              {t('s.0854')} <code className="font-mono text-[11px]">LEADSTACK_BACKUP_PASSPHRASE</code>
              {t('s.0855')}<strong className="font-semibold">{t('s.0856')}</strong>{t('s.0857')}
            </p>
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('s.0858')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {t('s.0859')}
          </p>
        </div>
        <Button
          variant="secondary"
          onClick={() => void refreshAll()}
          loading={backupsQuery.loading || settingsQuery.loading}
          icon={<RefreshIcon className="h-4 w-4" />}
        >
          {t('s.0689')}
        </Button>
      </div>

      {backupsQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('s.0860')}{backupsQuery.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void backupsQuery.refresh()}>
            {t('s.0053')}
          </Button>
        </div>
      ) : null}

      {/* 立即备份 */}
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
              <ArchiveIcon className="h-4 w-4 text-indigo-500" />
              {t('s.0861')}
            </h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {t('s.0862')}
            </p>
          </div>
          <Button
            onClick={() => void handleCreate()}
            loading={creating}
            disabled={!canWrite}
            icon={<ArchiveIcon className="h-4 w-4" />}
          >
            {t('s.0861')}
          </Button>
        </div>

        <label className="mt-3 flex items-start gap-2.5 rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
          <input
            type="checkbox"
            checked={createIncludeArtifacts}
            onChange={(event) => setCreateIncludeArtifacts(event.target.checked)}
            className="mt-0.5 h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
          />
          <span>
            <span className="block text-sm text-slate-700 dark:text-slate-200">{t('s.0863')}</span>
            <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
              {t('s.0864')}
            </span>
          </span>
        </label>

        {lastCreated ? (
          <p className="mt-2 flex flex-wrap items-center gap-2 text-xs text-emerald-700 dark:text-emerald-300">
            <CheckCircleIcon className="h-3.5 w-3.5" />
            {t('s.0865')}{lastCreated.name}（{formatBytes(lastCreated.size_bytes)} ·{' '}
            {formatDateTime(lastCreated.created_at)}）
          </p>
        ) : null}
      </section>

      {/* 备份列表 */}
      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0866')}</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {backups.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.0867')}
          </p>
        </div>

        {backupsQuery.loading && backupsQuery.data === null ? (
          <InlineLoading label={t('s.0868')} />
        ) : backups.length === 0 ? (
          <EmptyState
            icon={<ArchiveIcon className="h-6 w-6" />}
            title={t('s.0869')}
            description={t('s.0870')}
            actionLabel={canWrite ? t('s.0861') : undefined}
            onAction={canWrite ? () => void handleCreate() : undefined}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1040px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0871')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0271')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0278')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0872')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">{t('s.0270')}</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">SHA256</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">{t('s.0539')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {backups.map((backup) => (
                  <tr
                    key={backup.name}
                    className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                  >
                    <td className="max-w-[260px] px-4 py-3 align-top">
                      <span className="block truncate font-mono text-xs font-medium text-slate-800 dark:text-slate-100">
                        {backup.name}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(backup.created_at)}
                      <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                        {formatRelativeTime(backup.created_at)}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                      {formatBytes(backup.size_bytes)}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <span
                        className={cn(
                          'inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
                          backup.includes_artifacts
                            ? 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/30'
                            : 'bg-slate-100 text-slate-500 ring-slate-500/20 dark:bg-slate-800 dark:text-slate-400 dark:ring-slate-400/30',
                        )}
                      >
                        {backup.includes_artifacts ? t('s.0872') : t('s.0873')}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                      {backup.schema_version ?? '—'}
                    </td>
                    <td className="max-w-[180px] px-4 py-3 align-top font-mono text-[11px] text-slate-500 dark:text-slate-400">
                      <span title={backup.sha256 ?? undefined}>
                        {backup.sha256 ? truncateMiddle(backup.sha256, 14, 6) : '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top">
                      <div className="flex items-center justify-end gap-0.5">
                        <IconButton
                          label={t('s.0874')}
                          onClick={() => void handleDownload(backup)}
                          disabled={downloading === backup.name}
                        >
                          <DownloadIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton label={t('s.0875')}
                          disabled={!canWrite} onClick={() => void handleVerify(backup)}>
                          <ShieldCheckIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton
                          label={t('s.0876')}
                          disabled={!canDelete}
                          tone="danger"
                          onClick={() => {
                            setDeleteError(null)
                            setDeleting(backup)
                          }}
                        >
                          <TrashIcon className="h-4 w-4" />
                        </IconButton>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* 从备份恢复：整库恢复要求全局范围（后端 RequireGlobalScope），见 isGlobalScope 注释 */}
      {canRestoreGlobal ? (
      <section className="rounded-xl border border-rose-200 bg-white p-4 shadow-sm dark:border-rose-500/30 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
              <UploadIcon className="h-4 w-4 text-rose-500" />
              {t('s.0877')}
            </h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {t('s.0419')} <code className="font-mono">.lsbak</code> {t('s.0878')}
            </p>
          </div>
          {restoreStage !== 'idle' ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                handleRestoreFile(null)
                setRestoreConfirmOpen(false)
              }}
            >
              {t('s.0879')}
            </Button>
          ) : null}
        </div>

        <div className="mt-3">
          <label htmlFor="restore-file" className={labelClass}>
            {t('s.0880')}
          </label>
          <input
            id="restore-file"
            type="file"
            accept=".lsbak,application/octet-stream"
            onChange={(event) => handleRestoreFile(event.target.files?.[0] ?? null)}
            className="mt-1.5 block w-full text-sm text-slate-600 file:mr-3 file:rounded-md file:border-0 file:bg-rose-50 file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-rose-700 dark:text-slate-300 dark:file:bg-rose-500/10 dark:file:text-rose-300"
          />
          {restoreFile ? (
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {t('s.0881')}<span className="font-mono">{restoreFile.name}</span>（
              {formatBytes(restoreFile.size)}）
            </p>
          ) : null}
        </div>

        {restoreStage === 'risk' && restoreFile ? (
          <div className="mt-3 space-y-3">
            <div className="rounded-lg border border-rose-200 bg-rose-50 p-3.5 dark:border-rose-500/30 dark:bg-rose-500/10">
              <p className="text-sm font-semibold text-rose-800 dark:text-rose-200">
                {t('s.0882')}
              </p>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-xs leading-relaxed text-rose-800/90 dark:text-rose-200/90">
                <li>
                  <strong className="font-semibold">{t('s.0883')}</strong>{t('s.0884')}<strong className="font-semibold">{t('s.0885')}</strong>{t('s.0886')}
                </li>
                <li>
                  {t('s.0887')}{' '}
                  <code className="font-mono">{t('s.0888')}</code>{' '}
                  {t('s.0889')}
                </li>
                <li>
                  {t('s.0890')}
                </li>
                <li>
                  {t('s.0891')}
                </li>
              </ul>
            </div>

            {restoreInsight ? <BackupErrorCard insight={restoreInsight} /> : null}

            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="danger"
                onClick={() => {
                  setRiskAcknowledged(false)
                  setRestoreConfirmOpen(true)
                }}
                icon={<UploadIcon className="h-4 w-4" />}
              >
                {t('s.0892')}
              </Button>
              <Button variant="secondary" onClick={() => handleRestoreFile(null)}>
                {t('s.0893')}
              </Button>
            </div>
          </div>
        ) : null}

        {restorePending ? (
          <div className="mt-3 rounded-lg border border-emerald-200 bg-emerald-50 p-3.5 dark:border-emerald-500/30 dark:bg-emerald-500/10">
            <p className="flex items-center gap-2 text-sm font-semibold text-emerald-800 dark:text-emerald-200">
              <CheckCircleIcon className="h-4 w-4" />
              {t('backups.restorePending')}
            </p>
            <p className="mt-1 text-xs leading-relaxed text-emerald-800/90 dark:text-emerald-200/90">
              {t('backups.restartCmdHint')}
            </p>
            <div className="mt-2 grid gap-1.5 sm:grid-cols-3">
              {RESTART_COMMANDS.map((item) => (
                <div
                  key={item.labelKey}
                  className="rounded-md border border-emerald-200 bg-white/70 p-2 dark:border-emerald-500/30 dark:bg-slate-950/40"
                >
                  <p className="text-[11px] font-medium text-emerald-900/80 dark:text-emerald-200/80">
                    {t(item.labelKey)}
                  </p>
                  <div className="mt-1 flex items-center gap-1">
                    <code className="min-w-0 flex-1 truncate font-mono text-[11px] text-slate-700 dark:text-slate-200">
                      {t(item.commandKey)}
                    </code>
                    <CopyButton value={t(item.commandKey)} label={t('backups.copyCmd')} />
                  </div>
                </div>
              ))}
            </div>
            {stagedSummary ? (
              <p className="mt-2 text-[11px] text-emerald-700/80 dark:text-emerald-300/80">
                {stagedSummary}
              </p>
            ) : null}
            <p className="mt-2 text-[11px] text-emerald-700/70 dark:text-emerald-300/70">
              {t('s.0901')}
            </p>
          </div>
        ) : null}

        {/* pending 由 true→false（重启后已应用）时的完成回执 */}
        {!restorePending && restoreDoneSha !== null ? (
          <div className="mt-3 flex items-start gap-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3.5 dark:border-emerald-500/30 dark:bg-emerald-500/10">
            <CheckCircleIcon className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-300" />
            <p className="text-sm font-semibold text-emerald-800 dark:text-emerald-200">
              {t('backups.restoreDone', { sha: restoreDoneSha || '—' })}
            </p>
          </div>
        ) : null}
      </section>

      ) : null}

      {/* 备份设置 */}
      {canWrite ? (
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('s.0902')}</h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {t('s.0903')}{settingsQuery.data ? formatBackupInterval(settingsQuery.data.interval) : t('s.0425')}
              {settingsQuery.data
                ? t('s.0904', { '0': settingsQuery.data.enabled ? t('common.enabled') : t('common.disabled'), '1': settingsQuery.data.retention })
                : ''}
            </p>
          </div>
          <Button
            onClick={() => void handleSaveSettings()}
            loading={savingSettings}
            disabled={!settingsDirty}
          >
            {t('s.0905')}
          </Button>
        </div>

        {settingsQuery.error ? (
          <p className="mt-2 text-xs text-rose-600 dark:text-rose-400">
            {t('s.0906')}{settingsQuery.error}{t('s.0907')}
          </p>
        ) : null}

        <div className="mt-3 space-y-3">
          <label className="flex items-start gap-2.5 rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
            <EnableSwitch
              checked={settingsForm.enabled}
              label={settingsForm.enabled ? t('s.0908') : t('s.0909')}
              onChange={() => updateSettings({ enabled: !settingsForm.enabled })}
            />
            <span>
              <span className="block text-sm text-slate-700 dark:text-slate-200">{t('s.0909')}</span>
              <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
                {t('s.0910')}
              </span>
            </span>
          </label>

          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <label htmlFor="backup-interval" className={labelClass}>
                {t('s.0911')}
              </label>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {INTERVAL_PRESETS.map((preset) => (
                  <button
                    key={preset.value}
                    type="button"
                    onClick={() => updateSettings({ interval: preset.value })}
                    className={cn(
                      'rounded border px-2 py-1 text-xs font-medium transition-colors',
                      parseGoDurationSeconds(preset.value) === intervalSeconds
                        ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                        : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
                    )}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
              <input
                id="backup-interval"
                value={settingsForm.interval}
                onChange={(event) => updateSettings({ interval: event.target.value })}
                placeholder={t('s.0912')}
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <p
                className={cn(
                  'mt-1 text-xs',
                  isValidGoDuration(settingsForm.interval)
                    ? 'text-slate-400 dark:text-slate-500'
                    : 'text-rose-600 dark:text-rose-400',
                )}
              >
                {isValidGoDuration(settingsForm.interval)
                  ? t('s.0913', { '0': formatBackupInterval(settingsForm.interval), '1': intervalIsCustom ? t('backups.custom') : '' })
                  : t('s.0914')}
              </p>
            </div>

            <div>
              <label htmlFor="backup-retention" className={labelClass}>
                {t('s.0915')}
              </label>
              <input
                id="backup-retention"
                type="number"
                min={1}
                value={settingsForm.retention}
                onChange={(event) =>
                  updateSettings({ retention: Number(event.target.value) || 0 })
                }
                className={inputClass + ' mt-1.5'}
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('s.0916')}
              </p>
            </div>

            <div>
              <span className={labelClass}>{t('s.0917')}</span>
              <label className="mt-2 flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200">
                <input
                  type="checkbox"
                  checked={settingsForm.include_artifacts}
                  onChange={(event) => updateSettings({ include_artifacts: event.target.checked })}
                  className="mt-0.5 h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                />
                <span className="text-xs text-slate-500 dark:text-slate-400">
                  {t('s.0918')}
                </span>
              </label>
            </div>
          </div>

          {settingsError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {settingsError}
            </p>
          ) : null}

          {settingsDirty ? (
            <p className="text-xs text-amber-600 dark:text-amber-400">
              {t('s.0919')}
            </p>
          ) : null}
        </div>
      </section>
      ) : null}

      <p className="text-xs text-slate-400 dark:text-slate-500">
        {t('s.0920')}{' '}
        <code className="font-mono">{t('s.0921')}</code>{' '}
        {t('s.0922')}
      </p>

      <BackupVerifyDialog
        open={verifyState !== null}
        info={verifyState?.info ?? null}
        loading={verifyState?.loading ?? false}
        result={verifyState?.result ?? null}
        insight={verifyState?.insight ?? null}
        onClose={() => setVerifyState(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('s.0876')}
        description={deleting ? t('s.0263', { '0': deleting.name }) : undefined}
        confirmLabel={t('s.0295')}
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => {
          setDeleting(null)
          setDeleteError(null)
        }}
        onConfirm={() => void handleDelete()}
      >
        <p>
          {t('s.0923')}
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deleting?.name}」
          </span>
          {t('s.0924')}
        </p>
      </ConfirmDialog>

      <Modal
        open={restoreConfirmOpen}
        onClose={() => {
          if (!restoreLoading) setRestoreConfirmOpen(false)
        }}
        title={t('s.0925')}
        description={restoreFile ? t('s.0263', { '0': restoreFile.name }) : undefined}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => setRestoreConfirmOpen(false)}
              disabled={restoreLoading}
            >
              {t('s.0024')}
            </Button>
            <Button
              variant="danger"
              onClick={() => void handleRestoreSubmit()}
              loading={restoreLoading}
              disabled={!riskAcknowledged}
            >
              {t('s.0926')}
            </Button>
          </>
        }
      >
        <div className="space-y-3 text-sm">
          <div className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2.5 text-xs leading-relaxed text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
            {t('s.0927')}<strong className="font-semibold">{t('s.0928')}</strong>
            {t('s.0929')}
          </div>
          <label className="flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200">
            <input
              type="checkbox"
              checked={riskAcknowledged}
              onChange={(event) => setRiskAcknowledged(event.target.checked)}
              className="mt-0.5 h-4 w-4 rounded border-slate-300 text-rose-600 focus:ring-rose-500 dark:border-slate-600 dark:bg-slate-800"
            />
            <span>
              {t('s.0930')}<strong className="font-semibold">{t('s.0931')}</strong>{t('s.0932')}
            </span>
          </label>
        </div>
      </Modal>
    </div>
  )
}
