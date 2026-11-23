import type { UpdatePhase, UpdateStatus } from './api'

/**
 * 版本与升级面板的纯函数：阶段映射、终态判定、上传文件校验与格式化。
 * 放在 lib 里而不是组件内，便于单测与在别处复用（阶段文案只有这一份 owner）。
 */

/** 后端上传上限：internal/api/update_upload.go:30 upgradeUploadMaxBytes = 200 << 20。 */
export const UPLOAD_MAX_BYTES = 200 * 1024 * 1024

/** 终态：必须停止 2s 轮询，否则升级结束后面板会永远转圈。 */
const TERMINAL_PHASES: readonly string[] = ['success', 'failed', 'rolled_back', 'stalled']

const PHASE_LABEL_KEYS: Record<string, string> = {
  validating: 'upgrade.phase.validating',
  backup: 'upgrade.phase.backing_up',
  backing_up: 'upgrade.phase.backing_up',
  download: 'upgrade.phase.downloading',
  downloading: 'upgrade.phase.downloading',
  verify_checksum: 'upgrade.phase.verifyChecksum',
  extract: 'upgrade.phase.extract',
  prepare: 'upgrade.phase.prepare',
  swap: 'upgrade.phase.swap',
  replacing: 'upgrade.phase.replacing',
  spawn_watchdog: 'upgrade.phase.spawnWatchdog',
  exec: 'upgrade.phase.exec',
  restarting: 'upgrade.phase.restarting',
  success: 'upgrade.phase.success',
  failed: 'upgrade.phase.failed',
  rolled_back: 'upgrade.phase.rolled_back',
  stalled: 'upgrade.phase.stalled',
}

/** 升级步骤顺序：进度条与“详细步骤”共用这一份，避免两处各写一套阶段顺序。 */
export const UPGRADE_STEPS = [
  { phase: 'validating', labelKey: 'upgrade.phase.validating' },
  { phase: 'backup', labelKey: 'upgrade.phase.backing_up' },
  { phase: 'download', labelKey: 'upgrade.phase.downloading' },
  { phase: 'verify_checksum', labelKey: 'upgrade.phase.verifyChecksum' },
  { phase: 'extract', labelKey: 'upgrade.phase.extract' },
  { phase: 'prepare', labelKey: 'upgrade.phase.prepare' },
  { phase: 'swap', labelKey: 'upgrade.phase.swap' },
  { phase: 'spawn_watchdog', labelKey: 'upgrade.phase.spawnWatchdog' },
  { phase: 'exec', labelKey: 'upgrade.phase.exec' },
] as const

/** 各阶段对应的进度百分比（阶段结束时到达该值）。 */
const PHASE_PROGRESS: Record<string, number> = {
  validating: 4,
  backup: 18,
  download: 38,
  verify_checksum: 52,
  extract: 64,
  prepare: 76,
  swap: 88,
  spawn_watchdog: 94,
  exec: 98,
  restarting: 99,
  success: 100,
  failed: 100,
  rolled_back: 100,
  stalled: 100,
}

/** 当前阶段 → 进度百分比；未知/空阶段给 0。 */
export function phaseProgress(phase: UpdatePhase | null | undefined): number {
  if (!phase) return 0
  return PHASE_PROGRESS[phase] ?? 0
}

/** 当前阶段在步骤列表中的索引；终态时返回最后一步。 */
export function phaseStepIndex(phase: UpdatePhase | null | undefined): number {
  if (!phase) return -1
  if (phase === 'success' || phase === 'failed' || phase === 'rolled_back' || phase === 'stalled') {
    return UPGRADE_STEPS.length - 1
  }
  return UPGRADE_STEPS.findIndex((step) => step.phase === phase)
}

export function isTerminalPhase(phase: UpdatePhase | null | undefined): boolean {
  return typeof phase === 'string' && TERMINAL_PHASES.includes(phase)
}

/** 重启后内存 job 丢失时可判定的终态。 */
export type LostJobOutcome = 'success' | 'rolled_back'

/**
 * 自升级重启后 status 无 job 时，用持久化证据判定这次升级的终态。
 * 证据优先级：history[0]（最新一条）→ current_version 与本次提交目标的比对。
 * 返回 null 表示证据不足，调用方应继续轮询（进程可能还在重启）。
 */
export function resolveLostJobOutcome(
  status: Pick<UpdateStatus, 'current_version' | 'history'>,
  target: string | null,
): LostJobOutcome | null {
  if (!target) return null
  const latest = status.history?.[0]
  if (latest) {
    // 成功：历史目标已等于当前版本，说明新版本确实生效了。
    if (latest.outcome === 'success' && latest.target_version === status.current_version) return 'success'
    // 回退必须匹配本次提交目标，避免把历史里的旧回退误判成本次结果。
    if (latest.outcome === 'rollback' && latest.target_version === target) return 'rolled_back'
    return null
  }
  // history 缺失（老数据目录 / 写历史失败）：退化为版本号比对。
  return status.current_version === target ? 'success' : null
}

/** 阶段 → i18n key。服务端可能返回未知阶段（UpdatePhase 含 `| string`），统一落到 unknown。 */
export function phaseLabelKey(phase: UpdatePhase | null | undefined): string {
  if (!phase) return 'upgrade.phase.starting'
  return PHASE_LABEL_KEYS[phase] ?? 'upgrade.phase.unknown'
}

export type PhaseTone = 'success' | 'danger' | 'warning' | 'normal'

/** 颜色语义固定：成功 / 错误 / 警示，其余中性。 */
export function phaseTone(phase: UpdatePhase | null | undefined): PhaseTone {
  if (phase === 'success') return 'success'
  if (phase === 'failed') return 'danger'
  if (phase === 'rolled_back' || phase === 'stalled') return 'warning'
  return 'normal'
}

export interface UpdateFileLike {
  name: string
  size: number
}

export type UpdateFileCheck =
  | { ok: true }
  | { ok: false; reason: 'empty' | 'too_large'; size: number }

/** 上传前的前端校验：>0 字节且 ≤200 MiB（超限直接拦下，不把 200 MiB 花在网线上）。 */
export function validateUpdateFile(file: UpdateFileLike | null | undefined): UpdateFileCheck {
  const size = file?.size ?? 0
  if (!Number.isFinite(size) || size <= 0) return { ok: false, reason: 'empty', size: 0 }
  if (size > UPLOAD_MAX_BYTES) return { ok: false, reason: 'too_large', size }
  return { ok: true }
}

const KIB = 1024
const MIB = 1024 * 1024
const GIB = 1024 * 1024 * 1024

function formatUnit(value: number, unit: string): string {
  const rounded = Math.round(value * 10) / 10
  return `${Number.isInteger(rounded) ? rounded : rounded.toFixed(1)} ${unit}`
}

/** 人类可读大小（1024 进制）。超限提示里显示的是文件真实大小，不是上限。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < KIB) return `${Math.round(bytes)} B`
  if (bytes < MIB) return formatUnit(bytes / KIB, 'KiB')
  if (bytes < GIB) return formatUnit(bytes / MIB, 'MiB')
  return formatUnit(bytes / GIB, 'GiB')
}

/** 发布时间：`YYYY-MM-DD HH:mm`（本地时区），非法/缺省返回空串由调用方兜底。 */
export function formatDateTime(ms: number | null | undefined): string {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms <= 0) return ''
  const date = new Date(ms)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (value: number): string => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(
    date.getMinutes(),
  )}`
}
