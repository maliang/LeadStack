import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { api, type CheckItem, type RunCheckResponse } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { t } from '@/i18n'

/**
 * 检测列表的数据 owner：列表数据、轮询、三态与两个写操作（立即执行 / 启用开关）都收在这里。
 *
 * 页面只负责渲染与 UI 状态（筛选关键字、页码、抽屉开合），组件不各自发请求——
 * 一个状态只能有一个 owner，否则轮询与行内操作会互相打脸。
 *
 * 权限闸：options.enabled 为 false 时**一次请求都不发**（无 check.read 的账号不该在
 * 网络面板里看到 /checks 的 403 回环），轮询定时器也不起。
 */

/** 列表轮询间隔：与旧版 React 页面一致（10s）。 */
export const CHECKS_POLL_MS = 10_000

export type ChecksLoadState = 'idle' | 'loading' | 'ready' | 'error'

export interface UseChecksOptions {
  /** 是否允许发请求（通常是 can('check.read')）；为 false 时零请求、零定时器。 */
  enabled: () => boolean
  /** 轮询间隔，测试里可调小；<= 0 表示不轮询。 */
  intervalMs?: number
}

export interface UseChecks {
  checks: Ref<CheckItem[]>
  state: Ref<ChecksLoadState>
  /** 首屏/重试失败文案（已带 requestId），成功刷新后清空 */
  error: Ref<string>
  /** 至少成功取到过一次数据（用于区分「骨架屏」与「刷新中」） */
  loaded: Ref<boolean>
  refreshing: Ref<boolean>
  /** 最近一次成功刷新的时间戳（ms），null = 还没成功过 */
  updatedAt: Ref<number | null>
  /** 行内写操作的失败文案（立即执行/启用开关），不影响列表数据 */
  actionError: Ref<string>
  runningId: Ref<string | null>
  togglingId: Ref<string | null>
  /** 立即执行的结果（供页面展示逐台结果），null = 尚未执行 */
  lastRun: Ref<RunCheckResponse | null>
  refresh: () => Promise<void>
  runCheck: (check: CheckItem) => Promise<RunCheckResponse | null>
  setEnabled: (check: CheckItem, enabled: boolean) => Promise<boolean>
  clearActionError: () => void
}

export function useChecks(options: UseChecksOptions): UseChecks {
  const intervalMs = options.intervalMs ?? CHECKS_POLL_MS

  const checks = ref<CheckItem[]>([])
  const state = ref<ChecksLoadState>('idle')
  const error = ref('')
  const loaded = ref(false)
  const refreshing = ref(false)
  const updatedAt = ref<number | null>(null)
  const actionError = ref('')
  const runningId = ref<string | null>(null)
  const togglingId = ref<string | null>(null)
  const lastRun = ref<RunCheckResponse | null>(null)

  let timer: ReturnType<typeof setInterval> | null = null
  let inflight: Promise<void> | null = null

  async function load(): Promise<void> {
    if (!options.enabled()) return
    if (inflight) return inflight
    // 首屏与重试用 'loading'（骨架屏），后续轮询只用 refreshing（不清数据）
    if (!loaded.value) state.value = 'loading'
    refreshing.value = true
    inflight = (async () => {
      try {
        const data = await api.checks()
        checks.value = data
        loaded.value = true
        updatedAt.value = Date.now()
        state.value = 'ready'
        error.value = ''
      } catch (caught) {
        // 首屏失败才把整页打回错误态；已有数据时保留列表，只更新错误文案
        if (!loaded.value) state.value = 'error'
        error.value = errorMessage(caught, t('checks.loadFailed'))
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
      // 页面在后台时不轮询：省一次无意义的 401/403 与流量
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
        checks.value = []
        loaded.value = false
        state.value = 'idle'
        error.value = ''
      }
    },
    { immediate: true },
  )

  onScopeDispose(stopTimer)

  async function runCheck(check: CheckItem): Promise<RunCheckResponse | null> {
    if (runningId.value) return null
    actionError.value = ''
    runningId.value = check.id
    lastRun.value = null
    try {
      const response = await api.runCheck(check.id)
      lastRun.value = response
      return response
    } catch (caught) {
      actionError.value = errorMessage(caught, t('checks.runFailed'))
      return null
    } finally {
      runningId.value = null
      // 无论成功失败都取一次最新状态：run 会写入 last_result/state/availability_24h
      await refresh()
    }
  }

  async function setEnabled(check: CheckItem, enabled: boolean): Promise<boolean> {
    if (togglingId.value) return false
    actionError.value = ''
    togglingId.value = check.id
    try {
      await api.updateCheck(check.id, { enabled })
      return true
    } catch (caught) {
      actionError.value = errorMessage(caught, t('checks.toggleFailed'))
      return false
    } finally {
      togglingId.value = null
      await refresh()
    }
  }

  function clearActionError(): void {
    actionError.value = ''
  }

  return {
    checks,
    state,
    error,
    loaded,
    refreshing,
    updatedAt,
    actionError,
    runningId,
    togglingId,
    lastRun,
    refresh,
    runCheck,
    setEnabled,
    clearActionError,
  }
}
