/**
 * 升级会话：把「正在升级」这一事实写到 localStorage。
 *
 * 升级会重启控制端并刷新页面；内存里的 jobStarted 会丢，但服务端 job 可能仍在。
 * 右上角与设置页都读同一份标记，刷新后各自恢复 2s 状态轮询，两边显示同一份
 * `/system/update/status`，进度自然同步。
 */
const KEY = 'leadstack.upgrade.active'
const EVENT = 'leadstack:upgrade-session'

export interface UpgradeSession {
  target: string
  startedAt: number
}

export function readUpgradeSession(): UpgradeSession | null {
  try {
    const raw = window.localStorage.getItem(KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<UpgradeSession>
    if (typeof parsed.target !== 'string' || typeof parsed.startedAt !== 'number') return null
    // 超过 30 分钟视为陈旧标记，避免永远显示进度。
    if (Date.now() - parsed.startedAt > 30 * 60_000) {
      window.localStorage.removeItem(KEY)
      return null
    }
    return { target: parsed.target, startedAt: parsed.startedAt }
  } catch {
    return null
  }
}

export function markUpgradeActive(target: string): void {
  try {
    window.localStorage.setItem(KEY, JSON.stringify({ target, startedAt: Date.now() }))
  } catch {
    /* ignore */
  }
  window.dispatchEvent(new CustomEvent(EVENT, { detail: { target } }))
}

export function clearUpgradeSession(): void {
  try {
    window.localStorage.removeItem(KEY)
  } catch {
    /* ignore */
  }
}

export function onUpgradeSessionChange(handler: (session: UpgradeSession | null) => void): () => void {
  const onCustom = (): void => handler(readUpgradeSession())
  const onStorage = (event: StorageEvent): void => {
    if (event.key === KEY) handler(readUpgradeSession())
  }
  window.addEventListener(EVENT, onCustom)
  window.addEventListener('storage', onStorage)
  return () => {
    window.removeEventListener(EVENT, onCustom)
    window.removeEventListener('storage', onStorage)
  }
}
