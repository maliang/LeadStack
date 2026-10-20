import { t } from '../i18n'
/** 备份相关的错误分类与时长工具（契约第 31/33 节）。 */

export type BackupErrorKind = 'bad_passphrase' | 'corrupt' | 'schema_too_new' | 'unknown'

export interface BackupErrorInsight {
  kind: BackupErrorKind
  title: string
  /** 为什么会这样（一句人话） */
  reason: string
  /** 用户接下来该做什么 */
  guidance: string
  /** 原始错误信息（原样展示，便于排查） */
  raw: string
}

/**
 * 后端会区分「口令错误 / 归档损坏或篡改 / 备份来自更新版本」三类错误。
 * 这里按错误码 + 中文/英文关键字归类，分别给出「该怎么办」，避免统一显示一句「失败」。
 */
export function classifyBackupError(error: unknown): BackupErrorInsight {
  const raw =
    error instanceof Error && error.message.trim() !== ''
      ? error.message
      : typeof error === 'string'
        ? error
        : t('s.0595')
  const code =
    error && typeof error === 'object' && 'code' in error
      ? String((error as { code?: unknown }).code ?? '')
      : ''
  const combined = `${code} ${raw}`.toLowerCase()

  const isSchemaTooNew =
    /schema[_ -]?too[_ -]?new|too[_ -]?new|版本过新|更新的版本|高于当前|schema.*版本/.test(combined) ||
    /format_version.*(new|高)/.test(combined)
  if (isSchemaTooNew) {
    return {
      kind: 'schema_too_new',
      title: t('s.0596'),
      reason: t('s.0597'),
      guidance:
        t('s.0598'),
      raw,
    }
  }

  const isBadPassphrase =
    /bad[_ -]?pass|invalid[_ -]?pass|passphrase|口令|密码|解密失败|认证失败|authentication failed/.test(
      combined,
    )
  if (isBadPassphrase) {
    return {
      kind: 'bad_passphrase',
      title: t('s.0599'),
      reason:
        t('s.0600'),
      guidance:
        t('s.0601'),
      raw,
    }
  }

  const isCorrupt =
    /corrupt|tamper|invalid magic|格式|损坏|篡改|magic|truncat|unexpected eof|checksum|哈希|sha256|gzip|tar/.test(
      combined,
    )
  if (isCorrupt) {
    return {
      kind: 'corrupt',
      title: t('s.0602'),
      reason: t('s.0603'),
      guidance:
        t('s.0604'),
      raw,
    }
  }

  return {
    kind: 'unknown',
    title: t('s.0605'),
    reason: t('s.0606'),
    guidance:
      t('s.0607'),
    raw,
  }
}

/** Go duration 字符串校验（如 6h、30m、1h30m、500ms）。 */
export function isValidGoDuration(value: string): boolean {
  const trimmed = value.trim()
  if (trimmed === '') return false
  return /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(trimmed)
}

/** 把 Go duration 解析成秒数；非法返回 null。 */
export function parseGoDurationSeconds(value: string): number | null {
  const trimmed = value.trim()
  if (!/^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(trimmed)) return null
  const units: Record<string, number> = {
    h: 3600,
    m: 60,
    s: 1,
    ms: 0.001,
    us: 0.000001,
    'µs': 0.000001,
    ns: 0.000000001,
  }
  let total = 0
  const regex = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g
  let match: RegExpExecArray | null
  while ((match = regex.exec(trimmed)) !== null) {
    total += Number(match[1]) * units[match[2]]
  }
  return Number.isFinite(total) ? total : null
}

/**
 * 把 Go duration 转成人话；兼容后端规范化后的 `24h0m0s` / `6h0m0s` 形式。
 * 无法解析时原样返回。
 */
export function formatBackupInterval(value: string): string {
  const total = parseGoDurationSeconds(value)
  if (total === null || total <= 0) return value.trim()
  const seconds = Math.round(total)
  if (seconds % 86_400 === 0 && seconds >= 86_400) return t('s.0608', { '0': seconds / 86_400 })
  if (seconds % 3_600 === 0 && seconds >= 3_600) return t('s.0609', { '0': seconds / 3_600 })
  if (seconds % 60 === 0 && seconds >= 60) return t('s.0610', { '0': seconds / 60 })
  return t('s.0611', { '0': seconds })
}
