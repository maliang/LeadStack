import { computed, ref, watch } from 'vue'
import { api, type UpdateInfo, type UpdateStatus } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { isTerminalPhase, phaseProgress, phaseLabelKey } from '@/lib/upgrade'
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
  const progress = computed(() => phaseProgress(status.value?.phase))
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
    if (!allowed.value) return
    try {
      const data = await api.updateStatus()
      status.value = data
      if (active.value && isTerminalPhase(data.phase)) {
        active.value = false
        stopFastPoll()
        if (data.phase === 'success') {
          const next = data.current_version ?? data.target_version
          if (info.value && next) info.value = { ...info.value, current: next, has_update: info.value.latest !== next }
        }
        void loadInfo(true)
      }
    } catch {
      // 重启期间状态接口短暂不可达：保持上一帧进度，不把升级误判为失败。
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

  function start(): void {
    if (started || !allowed.value) return
    started = true
    // 顶栏只负责「有更新提示」；升级状态仅在用户从顶栏确认后轮询，
    // 避免与设置页 UpgradeCard 的 status mock/轮询互相抢答。
    void loadInfo()
    setInterval(() => void loadInfo(), 5 * 60_000)
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
