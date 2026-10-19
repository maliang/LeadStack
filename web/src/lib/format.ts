/** 时间、百分比、系统信息等展示格式化。 */

/** 相对时间：刚刚 / 12 秒前 / 3 分钟前 / 2 小时前 / 3 天前。 */
export function formatRelativeTime(ts: number | null | undefined, now: number = Date.now()): string {
  if (!ts) return '从未'
  const diff = now - ts
  if (diff < 5_000) return '刚刚'
  const seconds = Math.floor(diff / 1_000)
  if (seconds < 60) return `${seconds} 秒前`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} 天前`
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
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分钟`
  if (minutes > 0) return `${minutes} 分 ${secs} 秒`
  return `${secs} 秒`
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
