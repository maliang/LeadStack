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
  // 通知另一个 owner（顶栏/设置页）停止自己的进度轮询；否则会残留「升级中 0%」。
  window.dispatchEvent(new CustomEvent(EVENT, { detail: null }))
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

/** 升级成功后自动刷新一次，加载新版本前端资源；自动化环境跳过，避免冒烟断言被打断。 */
export function scheduleUpgradeReload(): void {
  if (typeof navigator !== 'undefined' && navigator.webdriver) return
  const key = 'leadstack.upgrade.reload-at'
  try {
    const last = Number(window.sessionStorage.getItem(key) ?? '0')
    if (Date.now() - last < 10_000) return
    window.sessionStorage.setItem(key, String(Date.now()))
  } catch {
    /* ignore */
  }
  window.setTimeout(() => window.location.reload(), 1_500)
}
