import { t } from '../i18n'
/** 时间、百分比、系统信息等展示格式化。 */

/** 相对时间：刚刚 / 12 秒前 / 3 分钟前 / 2 小时前 / 3 天前。 */
export function formatRelativeTime(ts: number | null | undefined, now: number = Date.now()): string {
  if (!ts) return t('s.0626')
  const diff = now - ts
  if (diff < 5_000) return t('s.0627')
  const seconds = Math.floor(diff / 1_000)
  if (seconds < 60) return t('s.0628', { '0': seconds })
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return t('s.0629', { '0': minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t('s.0630', { '0': hours })
  const days = Math.floor(hours / 24)
  if (days < 30) return t('s.0631', { '0': days })
  return formatDate(ts)
}

export function formatDate(ts: number | null | undefined): string {
  if (!ts) return '—'
  return new Date(ts).toLocaleDateString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  })
}

export function formatDateTime(ts: number | null | undefined): string {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

export function formatClockTime(ts: number | null | undefined): string {
  if (!ts) return '—'
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}

export function formatPercent(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return `${value.toFixed(digits)}%`
}

export function formatLoad(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return value.toFixed(2)
}

/** 运行时长：2 天 3 小时 / 15 分钟 / 42 秒。 */
export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || seconds < 0) return '—'
  const total = Math.floor(seconds)
  const days = Math.floor(total / 86_400)
  const hours = Math.floor((total % 86_400) / 3_600)
  const minutes = Math.floor((total % 3_600) / 60)
  const secs = total % 60
  if (days > 0) return t('s.0153', { '0': days, '1': hours })
  if (hours > 0) return t('s.0154', { '0': hours, '1': minutes })
  if (minutes > 0) return t('s.0632', { '0': minutes, '1': secs })
  return t('s.0156', { '0': secs })
}

export function formatOs(os: string | null, arch: string | null): string {
  const parts = [os?.trim(), arch?.trim()].filter((part): part is string => !!part)
  return parts.length > 0 ? parts.join(' / ') : '—'
}

export function serverAddress(server: { host: string; port: number | null }): string {
  return server.port ? `${server.host}:${server.port}` : server.host
}

/** 资源占用越低越绿、越高越红。 */
export function usageTone(value: number | null | undefined): 'normal' | 'warn' | 'danger' {
  if (value === null || value === undefined) return 'normal'
  if (value >= 90) return 'danger'
  if (value >= 75) return 'warn'
  return 'normal'
}

/** 字节数：1.5 GB / 512 MB / 8 KB。 */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let value = Math.abs(bytes)
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 ? 0 : digits)} ${units[unit]}`
}

/** 延迟：123 ms / 1.20 s。 */
export function formatLatency(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '—'
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`
  return `${Math.round(ms)} ms`
}

/** 长指纹中段省略：SHA256:AbCdEf…WxYz。 */
export function truncateMiddle(value: string | null | undefined, head = 14, tail = 6): string {
  if (!value) return '—'
  if (value.length <= head + tail + 1) return value
  return `${value.slice(0, head)}…${value.slice(-tail)}`
}

/** 指标 key → 中文名（契约 3.1 的点分命名）。 */
export function metricLabel(metric: string): string {
  switch (metric) {
    case 'cpu.pct':
      return t('s.0633')
    case 'mem.used_pct':
      return t('s.0634')
    case 'mem.used_bytes':
      return t('s.0635')
    case 'disk.used.pct':
    case 'disk.used_pct':
      return t('s.0636')
    case 'load.1':
      return t('s.0637')
    case 'net.rx_bytes':
      return t('s.0638')
    case 'net.tx_bytes':
      return t('s.0639')
    case 'proc.count':
      return t('s.0640')
    case 'uptime_s':
      return t('s.0641')
    default:
      return metric
  }
}

/** 秒数 → 人话时长：立即 / 45 秒 / 5 分钟 / 2 小时 / 1 天 3 小时。 */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return '—'
  const total = Math.max(0, Math.floor(seconds))
  if (total === 0) return t('s.0182')
  if (total < 60) return t('s.0156', { '0': total })
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return t('s.0155', { '0': minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return minutes % 60 === 0 ? t('s.0642', { '0': hours }) : t('s.0154', { '0': hours, '1': minutes % 60 })
  const days = Math.floor(hours / 24)
  return hours % 24 === 0 ? t('s.0643', { '0': days }) : t('s.0153', { '0': days, '1': hours % 24 })
}
