import { getCurrentScope, onScopeDispose, ref, watch, type Ref } from 'vue'
import { api, type CreateReleasePayload, type Release } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { t } from '@/i18n'
import type { DeployLoadState } from './useApps'

/**
 * 发布单列表的数据 owner：拉取（limit 200）+ 三态 + 发起发布 / 回滚 / 重试 / 中止。
 * 所有写操作都走同一个 busy/error 通道，页面不再各自维护一套「提交中/失败」。
 */

export interface ReleasesApi {
  releases: Ref<Release[]>
  state: Ref<DeployLoadState>
  error: Ref<string | null>
  loaded: Ref<boolean>
  refreshing: Ref<boolean>
  refresh: () => Promise<void>
  create: (payload: CreateReleasePayload) => Promise<Release | null>
  rollback: (releaseId: string, targetReleaseId: string) => Promise<boolean>
  retry: (releaseId: string) => Promise<boolean>
  cancel: (releaseId: string) => Promise<boolean>
  writeBusy: Ref<boolean>
  writeError: Ref<string | null>
  clearWriteError: () => void
}

export function useReleases(options: { enabled?: () => boolean; limit?: number } = {}): ReleasesApi {
  const enabled = options.enabled ?? (() => true)
  const limit = options.limit ?? 200
  const releases = ref<Release[]>([])
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
        releases.value = await api.releases({ limit })
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

  async function write<T>(action: () => Promise<T>): Promise<T | null> {
    writeBusy.value = true
    writeError.value = null
    try {
      const result = await action()
      await load(true)
      return result
    } catch (caught) {
      writeError.value = errorMessage(caught, t('common.error'))
      return null
    } finally {
      writeBusy.value = false
    }
  }

  if (enabled()) void load()
  const stop = watch(enabled, (allowed) => {
    if (allowed && !loaded.value) void load()
  })
  if (getCurrentScope()) onScopeDispose(() => stop())

  return {
    releases,
    state,
    error,
    loaded,
    refreshing,
    refresh: () => load(true),
    create: (payload) => write(async () => (await api.createRelease(payload)).release),
    rollback: (releaseId, targetReleaseId) =>
      write(async () => {
        await api.rollbackRelease(releaseId, { target_release_id: targetReleaseId })
        return true
      }).then((result) => result === true),
    retry: (releaseId) =>
      write(async () => {
        await api.retryRelease(releaseId)
        return true
      }).then((result) => result === true),
    cancel: (releaseId) =>
      write(async () => {
        await api.cancelRelease(releaseId)
        return true
      }).then((result) => result === true),
    writeBusy,
    writeError,
    clearWriteError: () => {
      writeError.value = null
    },
  }
}
