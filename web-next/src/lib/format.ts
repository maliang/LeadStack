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

/**
 * 服务器列表用的系统名称：优先使用 host_info.os_version（通常是 PRETTY_NAME，
 * 自带 CentOS/Debian/Ubuntu 等发行版名）；如果只有版本号，则补上大类名称。
 */
const OS_ID_NAMES: Record<string, string> = {
  ubuntu: 'Ubuntu',
  debian: 'Debian',
  raspbian: 'Raspbian',
  centos: 'CentOS',
  rocky: 'Rocky Linux',
  almalinux: 'AlmaLinux',
  fedora: 'Fedora',
  rhel: 'Red Hat Enterprise Linux',
  opensuse: 'openSUSE',
  sles: 'SUSE Linux Enterprise',
  alpine: 'Alpine Linux',
  arch: 'Arch Linux',
  manjaro: 'Manjaro',
  gentoo: 'Gentoo',
  nixos: 'NixOS',
  kali: 'Kali Linux',
  linuxmint: 'Linux Mint',
  macos: 'macOS',
  windows: 'Windows',
}

export function formatServerOs(server: {
  os: string | null
  host_info: { os: string | null; os_id?: string | null; os_name: string | null; os_version: string | null } | null
}): string {
  const rawVersion = server.host_info?.os_version?.trim() ?? ''
  // 旧数据里偶尔把 os_version 写成 linux/unknown；这类泛化值不能盖掉发行版名称。
  const version = /^(linux|unknown|n\/a|darwin|windows)$/i.test(rawVersion) ? '' : rawVersion
  const os = (server.host_info?.os ?? server.os ?? '').trim().toLowerCase()
  const distro =
    server.host_info?.os_name?.trim() ||
    OS_ID_NAMES[(server.host_info?.os_id ?? '').trim().toLowerCase()] ||
    ''
  const osName = distro || (os === 'linux' ? 'Linux' : os === 'darwin' ? 'macOS' : os === 'windows' ? 'Windows' : os)
  if (version !== '' && osName !== '') {
    // PRETTY_NAME 通常已经包含发行版名称；只有纯版本号（如 22.04.3 LTS）时才补前缀。
    return version.toLowerCase().includes(osName.toLowerCase()) ? version : `${osName} ${version}`
  }
  return version || osName || '—'
}

/** 把 host_info 的 os_id 归一化成前端图标键；旧 agent 没有 os_id 时按名称兜底。 */
export function serverOsId(server: {
  os: string | null
  host_info: { os: string | null; os_id?: string | null; os_name: string | null; os_version: string | null } | null
}): string {
  const raw = (server.host_info?.os_id ?? '').trim().toLowerCase()
  const text = `${server.host_info?.os_name ?? ''} ${server.host_info?.os_version ?? ''} ${server.host_info?.os ?? server.os ?? ''}`.toLowerCase()
  const source = raw || text
  if (source.includes('ubuntu')) return 'ubuntu'
  if (source.includes('linuxmint') || source.includes('mint')) return 'linuxmint'
  if (source.includes('kali')) return 'kali'
  if (source.includes('raspbian')) return 'raspbian'
  if (source.includes('debian')) return 'debian'
  if (source.includes('centos')) return 'centos'
  if (source.includes('rocky')) return 'rocky'
  if (source.includes('alma')) return 'almalinux'
  if (source.includes('fedora')) return 'fedora'
  if (source.includes('rhel') || source.includes('red hat') || source.includes('redhat')) return 'rhel'
  if (source.includes('opensuse') || source.includes('suse') || source.includes('sles')) return 'opensuse'
  if (source.includes('alpine')) return 'alpine'
  if (source.includes('arch')) return 'arch'
  if (source.includes('manjaro')) return 'manjaro'
  if (source.includes('gentoo')) return 'gentoo'
  if (source.includes('nixos')) return 'nixos'
  if (source.includes('amazon') || source.includes('alinux') || source.includes('oracle') || source.includes('opencloudos') || source.includes('tencent') || source.includes('openeuler')) return 'rhel'
  if (source.includes('darwin') || source.includes('macos') || source.includes('mac os')) return 'macos'
  if (source.includes('windows')) return 'windows'
  return 'linux'
}
export function formatOs(os: string | null, arch: string | null): string {
  const parts = [os?.trim(), arch?.trim()].filter((part): part is string => !!part)
  return parts.length > 0 ? parts.join(' / ') : '—'
}

export function serverAddress(server: { host: string; port: number | null }): string {
  return server.port ? `${server.host}:${server.port}` : server.host
}

/** 资源占用越低越绿、越高越红（列表/卡片的小指标着色）。 */
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

/**
 * 后端 Go duration（24h0m0s / 1h30m0s / 90m0s / 45s）→ 备份策略输入框可编辑的等价短串。
 *
 * 备份策略的 interval 输入只接受 `\d+[smhd]`（如 24h、30m），但后端 PATCH 后会以
 * time.Duration.String() 回填（24h0m0s）。这里换成同值短串，否则「保存一次就再也存不回去」。
 * 无法用单个单位整除表示（如 1.5s）时原样返回，交给输入校验提示用户改。
 */
export function toEditableInterval(raw: string | null | undefined): string {
  const value = (raw ?? '').trim()
  if (value === '') return '24h'
  if (/^\d+[smhd]$/.test(value)) return value
  const pattern = /(\d+(?:\.\d+)?)(ms|h|m|s)/g
  const unitSeconds: Record<string, number> = { h: 3600, m: 60, s: 1, ms: 0.001 }
  let totalSeconds = 0
  let matched = false
  for (let match = pattern.exec(value); match; match = pattern.exec(value)) {
    matched = true
    totalSeconds += Number(match[1]) * unitSeconds[match[2]]
  }
  if (!matched || !Number.isFinite(totalSeconds) || totalSeconds <= 0) return value
  if (!Number.isInteger(totalSeconds)) return value
  if (totalSeconds % 3600 === 0) return `${totalSeconds / 3600}h`
  if (totalSeconds % 60 === 0) return `${totalSeconds / 60}m`
  return `${totalSeconds}s`
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
