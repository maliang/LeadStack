import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { api, type AlertItem } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { t } from '@/i18n'
import { ALERTS_FETCH_LIMIT } from '@/lib/alerts'

/**
 * 告警列表的数据 owner：列表数据、轮询、三态、行内「确认」都收在这里。
 *
 * 页面只负责渲染与 UI 状态（筛选、页码、抽屉开合），组件不各自发请求——
 * 一个状态只能有一个 owner，否则轮询与行内操作会互相打脸。
 *
 * 权限闸：enabled 为 false 时**一次请求都不发**（无 alert.read 的账号不该在
 * 网络面板里看到 /alerts 的 403 回环），轮询定时器也不起。
 */

/** 列表轮询间隔：与旧版 React 页面一致（10s）。 */
export const ALERTS_POLL_MS = 10_000

export type AlertsLoadState = 'idle' | 'loading' | 'ready' | 'error'

export interface UseAlertsOptions {
  /** 是否允许发请求（通常是 can('alert.read')）；false 时零请求、零定时器。 */
  enabled: () => boolean
  /** 轮询间隔，测试里可调小；<= 0 表示不轮询。 */
  intervalMs?: number
}

export interface UseAlerts {
  alerts: Ref<AlertItem[]>
  state: Ref<AlertsLoadState>
  /** 首屏/重试失败文案（已带 requestId），成功刷新后清空 */
  error: Ref<string>
  /** 至少成功取到过一次数据（用于区分骨架屏与刷新中） */
  loaded: Ref<boolean>
  refreshing: Ref<boolean>
  /** 最近一次成功刷新的时间戳（ms），null = 还没成功过 */
  updatedAt: Ref<number | null>
  /** 行内「确认」的失败文案，不影响列表数据 */
  actionError: Ref<string>
  ackingId: Ref<string | null>
  refresh: () => Promise<void>
  ack: (alert: AlertItem) => Promise<boolean>
  clearActionError: () => void
}

export function useAlerts(options: UseAlertsOptions): UseAlerts {
  const intervalMs = options.intervalMs ?? ALERTS_POLL_MS

  const alerts = ref<AlertItem[]>([])
  const state = ref<AlertsLoadState>('idle')
  const error = ref('')
  const loaded = ref(false)
  const refreshing = ref(false)
  const updatedAt = ref<number | null>(null)
  const actionError = ref('')
  const ackingId = ref<string | null>(null)

  let timer: ReturnType<typeof setInterval> | null = null
  let inflight: Promise<void> | null = null

  async function load(): Promise<void> {
    if (!options.enabled()) return
    if (inflight) return inflight
    if (!loaded.value) state.value = 'loading'
    refreshing.value = true
    inflight = (async () => {
      try {
        const data = await api.alerts({ limit: ALERTS_FETCH_LIMIT })
        alerts.value = data
        loaded.value = true
        updatedAt.value = Date.now()
        state.value = 'ready'
        error.value = ''
      } catch (caught) {
        // 首屏失败才把整页打回错误态；已有数据时保留列表，只更新错误文案
        if (!loaded.value) state.value = 'error'
        error.value = errorMessage(caught, t('alerts.loadFailed'))
      } finally {
        refreshing.value = false
        inflight = null
      }
    })()
    return inflight
  }

  /** 强制刷新（写操作后调用）：即使已有轮询在飞也再取一次，保证界面立刻反映新状态。 */
  async function refresh(): Promise<void> {
    if (inflight) await inflight
    await load()
  }

  function stopTimer(): void {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  function startTimer(): void {
    stopTimer()
    if (intervalMs <= 0) return
    timer = setInterval(() => {
      // 页面在后台时不轮询：省一次无意义的请求与流量
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
      void load()
    }, intervalMs)
  }

  watch(
    () => options.enabled(),
    (allowed) => {
      if (allowed) {
        void load()
        startTimer()
      } else {
        stopTimer()
        alerts.value = []
        loaded.value = false
        state.value = 'idle'
        error.value = ''
      }
    },
    { immediate: true },
  )

  onScopeDispose(stopTimer)

  function applyItem(updated: AlertItem): void {
    const index = alerts.value.findIndex((alert) => alert.id === updated.id)
    if (index === -1) return
    const next = alerts.value.slice()
    next[index] = updated
    alerts.value = next
  }

  async function ack(alert: AlertItem): Promise<boolean> {
    if (ackingId.value !== null) return false
    actionError.value = ''
    ackingId.value = alert.id
    try {
      // 后端返回确认后的最新告警（真实后端可能落到 acked，mock/规则命中可能直接 resolved）：
      // 以响应为准先落到列表，随后 refresh 再取一次全量，避免与轮询互相覆盖。
      const updated = await api.ackAlert(alert.id)
      applyItem(updated)
      return true
    } catch (caught) {
      actionError.value = errorMessage(caught, t('alerts.ackFailed'))
      return false
    } finally {
      ackingId.value = null
      await refresh()
    }
  }

  function clearActionError(): void {
    actionError.value = ''
  }

  return {
    alerts,
    state,
    error,
    loaded,
    refreshing,
    updatedAt,
    actionError,
    ackingId,
    refresh,
    ack,
    clearActionError,
  }
}
