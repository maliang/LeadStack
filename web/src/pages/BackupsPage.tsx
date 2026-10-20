import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type BackupInfo,
  type BackupSettings,
  type BackupVerifyResult,
  type RestoreStagedResult,
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

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const INTERVAL_PRESETS = [
  { value: '6h', label: '6 小时' },
  { value: '12h', label: '12 小时' },
  { value: '24h', label: '24 小时' },
]

const DEFAULT_SETTINGS: BackupSettings = {
  enabled: false,
  interval: '24h',
  retention: 7,
  include_artifacts: false,
}

interface VerifyState {
  info: BackupInfo
  loading: boolean
  result: BackupVerifyResult | null
  insight: BackupErrorInsight | null
}

export default function BackupsPage() {
  const { toast } = useToast()
  const backupsQuery = usePolling(api.backups, 10_000)
  const settingsQuery = usePolling(api.backupSettings, 30_000)

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
        `备份已创建：${created.name}（${formatBytes(created.size_bytes)}）${
          created.includes_artifacts ? '，含上传制品' : ''
        }`,
        'success',
      )
      await backupsQuery.refresh()
    } catch (caught) {
      toast(errorMessage(caught, '创建备份失败'), 'error')
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
      toast(`已开始下载 ${filename}`, 'success')
    } catch (caught) {
      toast(errorMessage(caught, '下载备份失败'), 'error')
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
          insight: classifyBackupError(result.error ?? '校验失败'),
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
      toast(`已删除备份「${deleting.name}」`, 'success')
      setDeleting(null)
      await backupsQuery.refresh()
    } catch (caught) {
      setDeleteError(errorMessage(caught, '删除备份失败'))
    } finally {
      setDeleteLoading(false)
    }
  }

  const handleRestoreFile = (file: File | null) => {
    setRestoreFile(file)
    setRestoreInsight(null)
    setRestoreStaged(null)
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
      toast('备份已暂存，请重启控制端后生效', 'success')
    } catch (caught) {
      setRestoreInsight(classifyBackupError(caught))
      setRestoreStage('risk')
      setRestoreConfirmOpen(false)
    } finally {
      setRestoreLoading(false)
    }
  }

  const handleSaveSettings = async () => {
    const interval = settingsForm.interval.trim()
    if (!isValidGoDuration(interval)) {
      setSettingsError('间隔需为合法的时间长度，例如 6h、12h、30m、1h30m（单位 h/m/s）')
      return
    }
    if (!Number.isInteger(settingsForm.retention) || settingsForm.retention < 1) {
      setSettingsError('保留份数需为不小于 1 的整数')
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
      toast('备份设置已保存并即时生效', 'success')
      void settingsQuery.refresh()
    } catch (caught) {
      setSettingsError(errorMessage(caught, '保存备份设置失败'))
    } finally {
      setSavingSettings(false)
    }
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
              备份加密口令必须单独保管 —— 丢了就再也解不开
            </p>
            <p className="mt-0.5 text-xs leading-relaxed text-amber-800/90 dark:text-amber-200/90">
              口令存放在数据目录的{' '}
              <code className="rounded bg-amber-100 px-1 py-0.5 font-mono text-[11px] text-amber-900 dark:bg-amber-500/20 dark:text-amber-100">
                backup.pass
              </code>
              （或环境变量 <code className="font-mono text-[11px]">LEADSTACK_BACKUP_PASSPHRASE</code>
              ）。它<strong className="font-semibold">不在备份归档里</strong>，是解密备份的唯一钥匙：
              丢失后所有备份都无法恢复，控制端也无法替你找回。请把 backup.pass 单独保存到与备份文件不同的地方。
            </p>
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            备份与恢复
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            在线加密备份数据目录（数据库快照 / 主密钥 / known_hosts / 可选制品），恢复需重启控制端
          </p>
        </div>
        <Button
          variant="secondary"
          onClick={() => void refreshAll()}
          loading={backupsQuery.loading || settingsQuery.loading}
          icon={<RefreshIcon className="h-4 w-4" />}
        >
          刷新
        </Button>
      </div>

      {backupsQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>加载备份列表失败：{backupsQuery.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void backupsQuery.refresh()}>
            重试
          </Button>
        </div>
      ) : null}

      {/* 立即备份 */}
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
              <ArchiveIcon className="h-4 w-4 text-indigo-500" />
              立即备份
            </h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              在线执行（不停止服务），使用 VACUUM INTO 导出数据库一致快照后加密打包
            </p>
          </div>
          <Button onClick={() => void handleCreate()} loading={creating} icon={<ArchiveIcon className="h-4 w-4" />}>
            立即备份
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
            <span className="block text-sm text-slate-700 dark:text-slate-200">包含上传的制品文件</span>
            <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
              默认关闭：制品可能很大，包含它们会让备份变慢、占用更多磁盘；只有需要连同制品一起迁移时才勾选。
            </span>
          </span>
        </label>

        {lastCreated ? (
          <p className="mt-2 flex flex-wrap items-center gap-2 text-xs text-emerald-700 dark:text-emerald-300">
            <CheckCircleIcon className="h-3.5 w-3.5" />
            最近创建：{lastCreated.name}（{formatBytes(lastCreated.size_bytes)} ·{' '}
            {formatDateTime(lastCreated.created_at)}）
          </p>
        ) : null}
      </section>

      {/* 备份列表 */}
      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">备份列表</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {backups.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            归档保存在数据目录的 backups/ 下；备份文件本身已加密，可安全拷贝到别处
          </p>
        </div>

        {backupsQuery.loading && backupsQuery.data === null ? (
          <InlineLoading label="正在加载备份列表…" />
        ) : backups.length === 0 ? (
          <EmptyState
            icon={<ArchiveIcon className="h-6 w-6" />}
            title="还没有备份"
            description="点击上方「立即备份」创建第一份加密备份；建议同时开启下面的定时备份"
            actionLabel="立即备份"
            onAction={() => void handleCreate()}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1040px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">文件名</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">创建时间</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">大小</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">含制品</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Schema 版本</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">SHA256</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
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
                        {backup.includes_artifacts ? '含制品' : '不含制品'}
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
                          label="下载备份"
                          onClick={() => void handleDownload(backup)}
                          disabled={downloading === backup.name}
                        >
                          <DownloadIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton label="校验备份" onClick={() => void handleVerify(backup)}>
                          <ShieldCheckIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton
                          label="删除备份"
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

      {/* 从备份恢复 */}
      <section className="rounded-xl border border-rose-200 bg-white p-4 shadow-sm dark:border-rose-500/30 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
              <UploadIcon className="h-4 w-4 text-rose-500" />
              从备份恢复
            </h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              上传 <code className="font-mono">.lsbak</code> 归档 → 校验 → 暂存；重启控制端后才会真正生效
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
              取消本次恢复
            </Button>
          ) : null}
        </div>

        <div className="mt-3">
          <label htmlFor="restore-file" className={labelClass}>
            选择备份归档文件
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
              已选择：<span className="font-mono">{restoreFile.name}</span>（
              {formatBytes(restoreFile.size)}）
            </p>
          ) : null}
        </div>

        {restoreStage === 'risk' && restoreFile ? (
          <div className="mt-3 space-y-3">
            <div className="rounded-lg border border-rose-200 bg-rose-50 p-3.5 dark:border-rose-500/30 dark:bg-rose-500/10">
              <p className="text-sm font-semibold text-rose-800 dark:text-rose-200">
                危险操作：恢复会覆盖当前数据，并需要重启控制端
              </p>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-xs leading-relaxed text-rose-800/90 dark:text-rose-200/90">
                <li>
                  <strong className="font-semibold">本次只是暂存</strong>：归档上传并校验后写入数据目录的
                  .restore-pending/，<strong className="font-semibold">必须重启控制端</strong>才会真正恢复。
                </li>
                <li>
                  重启时当前数据会被整体移动到{' '}
                  <code className="font-mono">&lt;data_dir&gt;.pre-restore-&lt;时间戳&gt;/</code>{' '}
                  目录保留（不会直接删除），确认恢复无误后可手动清理。
                </li>
                <li>
                  归档里的 leadstack.db、master.key、known_hosts（以及可选制品）会替换当前文件；
                  恢复后若主密钥不同，需要重新配置 SSH 凭证。
                </li>
                <li>
                  恢复需要正确的解密口令（数据目录里的 backup.pass）；口令错误、归档损坏或备份版本过新都会在暂存前被拒绝。
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
                下一步：二次确认
              </Button>
              <Button variant="secondary" onClick={() => handleRestoreFile(null)}>
                重新选择文件
              </Button>
            </div>
          </div>
        ) : null}

        {restoreStage === 'staged' && restoreStaged ? (
          <div className="mt-3 rounded-lg border border-emerald-200 bg-emerald-50 p-3.5 dark:border-emerald-500/30 dark:bg-emerald-500/10">
            <p className="flex items-center gap-2 text-sm font-semibold text-emerald-800 dark:text-emerald-200">
              <CheckCircleIcon className="h-4 w-4" />
              已暂存，请重启控制端生效
            </p>
            <p className="mt-1 text-xs leading-relaxed text-emerald-800/90 dark:text-emerald-200/90">
              归档已通过校验并暂存到数据目录。现在重启 LeadStack 控制端（例如{' '}
              <code className="font-mono">systemctl restart leadstack</code> 或重新执行启动命令），
              启动时会在打开数据库前应用恢复；恢复前的数据会保留在 pre-restore 目录。
            </p>
            <p className="mt-1 text-[11px] text-emerald-700/80 dark:text-emerald-300/80">
              {restoreStaged.archive_name
                ? `暂存归档：${restoreStaged.archive_name}`
                : '归档已暂存'}
              {restoreStaged.size_bytes ? `（${formatBytes(restoreStaged.size_bytes)}）` : ''}
              {restoreStaged.schema_version !== null ? ` · schema ${restoreStaged.schema_version}` : ''}
              {restoreStaged.uploaded_at ? ` · 上传于 ${formatDateTime(restoreStaged.uploaded_at)}` : ''}
              {restoreStaged.sha256 ? ` · sha256 ${truncateMiddle(restoreStaged.sha256, 14, 6)}` : ''}
              {restoreStaged.staged_path ? ` · 暂存位置 ${restoreStaged.staged_path}` : ''}
            </p>
            <p className="mt-2 text-[11px] text-emerald-700/70 dark:text-emerald-300/70">
              重启完成前请勿再次上传恢复包；若需要放弃本次恢复，可在重启前删除数据目录下的 .restore-pending/。
            </p>
          </div>
        ) : null}
      </section>

      {/* 备份设置 */}
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">定时备份设置</h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
              保存后即时生效，无需重启；当前：{settingsQuery.data ? formatBackupInterval(settingsQuery.data.interval) : '加载中…'}
              {settingsQuery.data
                ? ` · ${settingsQuery.data.enabled ? '已启用' : '未启用'} · 保留 ${settingsQuery.data.retention} 份`
                : ''}
            </p>
          </div>
          <Button
            onClick={() => void handleSaveSettings()}
            loading={savingSettings}
            disabled={!settingsDirty}
          >
            保存设置
          </Button>
        </div>

        {settingsQuery.error ? (
          <p className="mt-2 text-xs text-rose-600 dark:text-rose-400">
            加载设置失败：{settingsQuery.error}（可先直接修改并保存）
          </p>
        ) : null}

        <div className="mt-3 space-y-3">
          <label className="flex items-start gap-2.5 rounded-lg bg-slate-50 px-3 py-2.5 dark:bg-slate-950/60">
            <EnableSwitch
              checked={settingsForm.enabled}
              label={settingsForm.enabled ? '停用定时备份' : '启用定时备份'}
              onChange={() => updateSettings({ enabled: !settingsForm.enabled })}
            />
            <span>
              <span className="block text-sm text-slate-700 dark:text-slate-200">启用定时备份</span>
              <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
                按间隔自动创建备份，并按保留份数清理最旧的备份（避免磁盘被占满）
              </span>
            </span>
          </label>

          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <label htmlFor="backup-interval" className={labelClass}>
                备份间隔
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
                placeholder="例如 6h、12h、24h、30m、1h30m"
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
                  ? `生效值：${formatBackupInterval(settingsForm.interval)}${
                      intervalIsCustom ? '（自定义）' : ''
                    }`
                  : '格式不合法：需为 Go duration（单位 h/m/s，例如 24h、30m）'}
              </p>
            </div>

            <div>
              <label htmlFor="backup-retention" className={labelClass}>
                保留份数
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
                超出后按创建时间删除最旧的备份，建议至少保留 3 份
              </p>
            </div>

            <div>
              <span className={labelClass}>定时备份是否含制品</span>
              <label className="mt-2 flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200">
                <input
                  type="checkbox"
                  checked={settingsForm.include_artifacts}
                  onChange={(event) => updateSettings({ include_artifacts: event.target.checked })}
                  className="mt-0.5 h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                />
                <span className="text-xs text-slate-500 dark:text-slate-400">
                  开启后每份备份都会包含上传制品，会更慢、更占磁盘
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
              有未保存的修改，点击右上角「保存设置」后生效。
            </p>
          ) : null}
        </div>
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        恢复也可以不用控制台：停掉服务后用命令行{' '}
        <code className="font-mono">leadstack restore --file &lt;归档&gt; --data-dir &lt;数据目录&gt;</code>{' '}
        执行，同样会保留 pre-restore 目录。
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
        title="删除备份"
        description={deleting ? `归档：${deleting.name}` : undefined}
        confirmLabel="确认删除"
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
          确定要删除备份
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deleting?.name}」
          </span>
          吗？删除后存档文件与元数据都会移除，且不可恢复。
        </p>
      </ConfirmDialog>

      <Modal
        open={restoreConfirmOpen}
        onClose={() => {
          if (!restoreLoading) setRestoreConfirmOpen(false)
        }}
        title="二次确认：恢复将覆盖当前数据"
        description={restoreFile ? `归档：${restoreFile.name}` : undefined}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => setRestoreConfirmOpen(false)}
              disabled={restoreLoading}
            >
              取消
            </Button>
            <Button
              variant="danger"
              onClick={() => void handleRestoreSubmit()}
              loading={restoreLoading}
              disabled={!riskAcknowledged}
            >
              确认暂存恢复
            </Button>
          </>
        }
      >
        <div className="space-y-3 text-sm">
          <div className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2.5 text-xs leading-relaxed text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
            提交后归档会立即被暂存；<strong className="font-semibold">重启控制端时</strong>
            才会真正覆盖数据。当前数据会被移到 pre-restore 目录保留，但恢复后若发现问题仍需人工处理。
          </div>
          <label className="flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200">
            <input
              type="checkbox"
              checked={riskAcknowledged}
              onChange={(event) => setRiskAcknowledged(event.target.checked)}
              className="mt-0.5 h-4 w-4 rounded border-slate-300 text-rose-600 focus:ring-rose-500 dark:border-slate-600 dark:bg-slate-800"
            />
            <span>
              我已了解：恢复需要<strong className="font-semibold">重启控制端</strong>才会生效，
              且当前数据会被移动到 pre-restore 目录、被归档内容覆盖。
            </span>
          </label>
        </div>
      </Modal>
    </div>
  )
}
