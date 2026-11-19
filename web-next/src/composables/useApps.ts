import { computed, getCurrentScope, onScopeDispose, ref, watch, type Ref } from 'vue'
import { api, type AppItem, type CreateAppPayload } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { t } from '@/i18n'

/**
 * 应用列表的数据 owner：列表 + 三态 + 写操作（新建 / 编辑 / 删除）。
 *
 * 页面只渲染并派发动作，不自己 setState；写操作失败把可读文案留给 dialog，
 * 成功后统一重拉列表（后端返回的统计字段前端不自行推导）。
 */

export type DeployLoadState = 'idle' | 'loading' | 'ready' | 'error'

export interface AppsApi {
  apps: Ref<AppItem[]>
  state: Ref<DeployLoadState>
  error: Ref<string | null>
  loaded: Ref<boolean>
  loading: Ref<boolean>
  refresh: () => Promise<void>
  create: (payload: CreateAppPayload) => Promise<boolean>
  update: (id: string, payload: Partial<CreateAppPayload>) => Promise<boolean>
  remove: (id: string) => Promise<boolean>
  writeBusy: Ref<boolean>
  writeError: Ref<string | null>
  clearWriteError: () => void
}

export function useApps(options: { enabled?: () => boolean } = {}): AppsApi {
  const enabled = options.enabled ?? (() => true)
  const apps = ref<AppItem[]>([])
  const state = ref<DeployLoadState>('idle')
  const error = ref<string | null>(null)
  const loaded = ref(false)
  const refreshing = ref(false)
  const writeBusy = ref(false)
  const writeError = ref<string | null>(null)
  let inflight: Promise<void> | null = null

  async function load(keep = false): Promise<void> {
    if (!enabled()) return
    if (inflight) return inflight
    if (keep) refreshing.value = true
    else state.value = 'loading'
    error.value = null
    inflight = (async () => {
      try {
        apps.value = await api.apps()
        state.value = 'ready'
        loaded.value = true
      } catch (caught) {
        error.value = errorMessage(caught, t('common.error'))
        state.value = 'error'
      } finally {
        refreshing.value = false
        inflight = null
      }
    })()
    return inflight
  }

  async function write(action: () => Promise<unknown>): Promise<boolean> {
    writeBusy.value = true
    writeError.value = null
    try {
      await action()
      await load(true)
      return true
    } catch (caught) {
      writeError.value = errorMessage(caught, t('common.error'))
      return false
    } finally {
      writeBusy.value = false
    }
  }

  function clearWriteError(): void {
    writeError.value = null
  }

  if (enabled()) void load()
  const stop = watch(enabled, (allowed) => {
    if (allowed && !loaded.value) void load()
  })
  if (getCurrentScope()) onScopeDispose(() => stop())

  return {
    apps,
    state,
    error,
    loaded,
    loading: computed(() => state.value === 'loading'),
    refresh: () => load(true),
    create: (payload) => write(() => api.createApp(payload)),
    update: (id, payload) => write(() => api.updateApp(id, payload)),
    remove: (id) => write(() => api.deleteApp(id)),
    writeBusy,
    writeError,
    clearWriteError,
  }
}
