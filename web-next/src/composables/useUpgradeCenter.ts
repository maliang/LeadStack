import { computed, ref, watch } from 'vue'
import { api, type UpdateInfo, type UpdateStatus } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { isTerminalPhase, phaseProgress, phaseLabelKey, resolveLostJobOutcome } from '@/lib/upgrade'
import {
  clearUpgradeSession,
  markUpgradeActive,
  onUpgradeSessionChange,
  readUpgradeSession,
} from '@/lib/upgrade-session'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'

/**
 * 顶栏升级中心（单例）：检查新版本、确认升级、在右上角展示小进度。
 *
 * 与设置页 UpgradeCard 独立：设置页负责完整面板与离线包上传，顶栏只做
 * 「有更新就提示、确认后显示 2 秒粒度的进度」。两边都调同一组接口，不新增后端端点。
 */
const info = ref<UpdateInfo | null>(null)
const status = ref<UpdateStatus | null>(null)
const active = ref(false)
const applying = ref(false)
const error = ref<string | null>(null)
const loaded = ref(false)

let statusTimer: ReturnType<typeof setInterval> | null = null
let statusAbort: AbortController | null = null
let statusInFlight = false
let pendingTarget: string | null = null
let started = false

export function useUpgradeCenter() {
  const { can } = useCan()
  const { t } = useI18n()
  const allowed = computed(() => can('system.manage'))

  const latest = computed(() => info.value?.latest ?? null)
  const hasUpdate = computed(
    () =>
      Boolean(info.value?.has_update && latest.value) &&
      info.value?.upgradable !== false &&
      info.value?.upgrade_enabled !== false,
  )
  const progress = computed(() => phaseProgress(status.value?.phase, status.value?.phase_elapsed_s ?? 0))
  const phaseLabel = computed(() => t(phaseLabelKey(status.value?.phase)))

  async function loadInfo(force = false): Promise<void> {
    if (!allowed.value) return
    try {
      info.value = force ? await api.refreshUpdateCheck() : await api.checkForUpdate()
      error.value = null
    } catch (caught) {
      error.value = errorMessage(caught, t('upgrade.checkFailedTitle'))
    } finally {
      loaded.value = true
    }
  }

  async function loadStatus(): Promise<void> {
    if (!allowed.value || statusInFlight) return
    statusInFlight = true
    const controller = new AbortController()
    statusAbort = controller
    const timeout = setTimeout(() => controller.abort(), 6_000)
    try {
      const data = await api.updateStatus({ signal: controller.signal })
      status.value = data
      if (active.value && data.phase === 'idle' && pendingTarget) {
        const outcome = resolveLostJobOutcome(data, pendingTarget)
        if (outcome) {
          status.value = {
            ...data,
            phase: outcome === 'success' ? 'success' : 'rolled_back',
            target_version: pendingTarget,
            rolled_back: outcome === 'rolled_back',
          }
          pendingTarget = null
          active.value = false
          clearUpgradeSession()
          stopFastPoll()
          void loadInfo(true)
          return
        }
      }
      if (active.value && isTerminalPhase(data.phase)) {
        active.value = false
        pendingTarget = null
        clearUpgradeSession()
        stopFastPoll()
        if (data.phase === 'success') {
          const next = data.current_version ?? data.target_version
          if (info.value && next) info.value = { ...info.value, current: next, has_update: info.value.latest !== next }
        }
        void loadInfo(true)
      }
    } catch {
      // 重启期间状态接口短暂不可达：保持上一帧进度，不把升级误判为失败。
      // 主动 6 秒超时，避免卡住的 fetch 让 statusInFlight 永久为 true。
    } finally {
      clearTimeout(timeout)
      statusAbort = null
      statusInFlight = false
    }
  }

  function startFastPoll(): void {
    if (statusTimer !== null) clearInterval(statusTimer)
    statusTimer = setInterval(() => void loadStatus(), 2_000)
  }

  function stopFastPoll(): void {
    if (statusTimer !== null) {
      clearInterval(statusTimer)
      statusTimer = null
    }
    statusAbort?.abort()
    statusAbort = null
  }

  async function checkNow(): Promise<void> {
    await loadInfo(true)
  }

  async function apply(): Promise<void> {
    const version = latest.value
    if (!version || applying.value || active.value) return
    applying.value = true
    error.value = null
    try {
      await api.applyUpdate({ version })
      markUpgradeActive(version)
      pendingTarget = version
      active.value = true
      startFastPoll()
      await loadStatus()
    } catch (caught) {
      error.value = errorMessage(caught, t('upgrade.applyFailedTitle'))
      throw caught
    } finally {
      applying.value = false
    }
  }

  function onSettingsPage(): boolean {
    return typeof window !== 'undefined' && window.location.hash.startsWith('#/settings')
  }

  function start(): void {
    if (started || !allowed.value) return
    started = true
    void loadInfo()
    setInterval(() => void loadInfo(), 5 * 60_000)
    // 刷新后若 localStorage 里仍有升级会话，立即恢复进度轮询；设置页也会读同一标记。
    const session = readUpgradeSession()
    // 设置页由 UpgradeCard 负责状态轮询；顶栏不再重复请求同一接口，避免两边抢进度。
    if (session && !onSettingsPage()) {
      pendingTarget = session.target
      active.value = true
      startFastPoll()
    }
    onUpgradeSessionChange((next) => {
      if (!next) {
        active.value = false
        pendingTarget = null
        status.value = null
        stopFastPoll()
        return
      }
      if (!active.value && !onSettingsPage()) {
        pendingTarget = next.target
        active.value = true
        startFastPoll()
      }
    })
    // 设置页 UpgradeCard 轮询到的状态通过事件同步给顶栏；两边显示同一帧。
    window.addEventListener('leadstack:upgrade-status', (event) => {
      const data = (event as CustomEvent<UpdateStatus>).detail
      if (!data) return
      status.value = data
      if (isTerminalPhase(data.phase)) {
        active.value = false
        pendingTarget = null
        stopFastPoll()
        return
      }
      active.value = true
    })
  }

  watch(
    allowed,
    (value) => {
      if (value) start()
    },
    { immediate: true },
  )

  return {
    allowed,
    loaded,
    info,
    status,
    active,
    applying,
    error,
    latest,
    hasUpdate,
    progress,
    phaseLabel,
    start,
    checkNow,
    apply,
  }
}
