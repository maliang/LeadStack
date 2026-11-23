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

/**
 * 各阶段的进度窗口与「预计耗时」。
 *
 * 后端只在阶段切换时更新 phase，若直接映射成固定百分比，进度条会在阶段内
 * 一动不动（原子替换/等新进程接管时尤其明显）。这里用 phase_elapsed_s 做
 * 渐近插值：随着耗时增长缓慢逼近窗口上限，切换阶段时再抬到下一窗口。
 * 预计耗时只影响观感，不参与任何超时判断。
 */
const PHASE_WINDOWS: Record<string, { start: number; end: number; expectedS: number }> = {
  validating: { start: 0, end: 5, expectedS: 2 },
  backup: { start: 5, end: 20, expectedS: 25 },
  download: { start: 20, end: 48, expectedS: 45 },
  verify_checksum: { start: 48, end: 58, expectedS: 8 },
  extract: { start: 58, end: 70, expectedS: 8 },
  prepare: { start: 70, end: 80, expectedS: 3 },
  swap: { start: 80, end: 90, expectedS: 6 },
  spawn_watchdog: { start: 90, end: 95, expectedS: 3 },
  exec: { start: 95, end: 99, expectedS: 6 },
  restarting: { start: 99, end: 100, expectedS: 15 },
}

const TERMINAL_PROGRESS: Record<string, number> = {
  success: 100,
  failed: 100,
  rolled_back: 100,
  stalled: 100,
}

const PHASE_ALIASES: Record<string, string> = {
  backing_up: 'backup',
  downloading: 'download',
  replacing: 'swap',
}

function canonicalPhase(phase: string): string {
  return PHASE_ALIASES[phase] ?? phase
}

/** 当前阶段 → 进度百分比；传入耗时后在该阶段窗口内平滑增长。 */
export function phaseProgress(
  phase: UpdatePhase | null | undefined,
  elapsedS = 0,
): number {
  if (!phase) return 0
  const key = canonicalPhase(phase)
  if (TERMINAL_PROGRESS[key] !== undefined) return TERMINAL_PROGRESS[key]
  const window = PHASE_WINDOWS[key]
  if (!window) return 0
  const elapsed = Number.isFinite(elapsedS) ? Math.max(0, elapsedS) : 0
  if (elapsed <= 0) return window.start
  // elapsed/(elapsed+expected)：0→0、expected→50%、3×expected→75%，永不越过窗口上限。
  const ratio = elapsed / (elapsed + window.expectedS)
  return Math.min(window.end, Math.round(window.start + (window.end - window.start) * ratio))
}

/** 当前阶段在步骤列表中的索引；终态时返回最后一步。 */
export function phaseStepIndex(phase: UpdatePhase | null | undefined): number {
  if (!phase) return -1
  if (phase === 'success' || phase === 'failed' || phase === 'rolled_back' || phase === 'stalled') {
    return UPGRADE_STEPS.length - 1
  }
  return UPGRADE_STEPS.findIndex((step) => step.phase === canonicalPhase(phase))
}

/** 原子替换/等新进程接管的阶段：界面要明确提示「服务会短暂无响应」。 */
export function isHandoffPhase(phase: UpdatePhase | null | undefined): boolean {
  return phase === 'swap' || phase === 'spawn_watchdog' || phase === 'exec' || phase === 'restarting'
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
