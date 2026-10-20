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
        : '未知错误'
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
      title: '备份来自更新版本，当前控制端无法恢复',
      reason: '归档里的数据库 schema 版本比当前控制端支持的最高版本更高。',
      guidance:
        '请先把控制端升级到与备份相同（或更高）的版本再恢复；不要用旧版本强行恢复新版本备份，否则可能损坏数据。',
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
      title: '备份口令错误',
      reason:
        '控制端当前使用的解密口令与创建这份备份时的口令不一致（AES-GCM 认证失败通常就是口令不对）。',
      guidance:
        '请确认数据目录里的 backup.pass（或环境变量 LEADSTACK_BACKUP_PASSPHRASE）与创建备份时一致。口令文件丢失后该备份无法解开，只能改用更早的、仍记得口令的备份。',
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
      title: '归档损坏或格式不符',
      reason: '文件可能在上传/下载/拷贝过程中损坏，或被意外修改，或根本不是 LeadStack 备份归档。',
      guidance:
        '请重新下载或重新拷贝该归档后再校验一次（大文件传输中断很常见）。若仍然失败，说明源文件已损坏，请改用其他备份；建议保留多份不同时间的备份。',
      raw,
    }
  }

  return {
    kind: 'unknown',
    title: '校验 / 恢复失败',
    reason: '控制端返回了未归类的错误，请结合原始错误信息排查。',
    guidance:
      '可以先在控制端所在机器上用命令行 `leadstack backup verify --file <归档>` 复现；若涉及权限或磁盘空间，请检查数据目录权限与剩余空间。',
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
  if (seconds % 86_400 === 0 && seconds >= 86_400) return `每 ${seconds / 86_400} 天`
  if (seconds % 3_600 === 0 && seconds >= 3_600) return `每 ${seconds / 3_600} 小时`
  if (seconds % 60 === 0 && seconds >= 60) return `每 ${seconds / 60} 分钟`
  return `每 ${seconds} 秒`
}
