<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/deploy/ConfirmDialog.vue'
import RollbackDialog from '@/components/deploy/RollbackDialog.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type Release, type ReleaseLogEntry } from '@/lib/api'
import {
  buildAppNameMap,
  releaseAppLabel,
  releaseDurationSeconds,
  releaseStats,
  releaseStatusLabelKey,
  releaseStatusTone,
} from '@/lib/deploy-domain'
import { errorMessage } from '@/lib/errors'
import { formatDateTime, formatRelativeTime } from '@/lib/format'

/**
 * 发布详情页：统计 + 动作（重试 / 回滚到指定版本 / 中止）+ 每台服务器的步骤时间线。
 *
 * 数据 owner 是本页（详情 + 逐 run 日志）；日志按需拉取，切服务器不重复拉已加载的那台。
 * 危险动作全部走二次确认：重试 / 中止用 ConfirmDialog，回滚用 RollbackDialog（需选目标版本）。
 */
const route = useRoute()
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('deploy.read'))
const canWrite = computed(() => can('deploy.write'))
const releaseId = computed(() => String(route.params.id ?? ''))

const release = ref<Release | null>(null)
/** 详情接口不返回 app_name/app_slug 时，用 api.apps() 建的表按 app_id 兜底查到的应用名。 */
const resolvedAppNames = ref<Map<string, string> | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref<string | null>(null)
const loaded = ref(false)
const refreshing = ref(false)
const notFound = ref(false)

const actionBusy = ref(false)
const actionError = ref<string | null>(null)
const notice = ref('')
const confirming = ref<'retry' | 'cancel' | null>(null)
const rollbackOpen = ref(false)

const logsByServer = ref<Record<string, ReleaseLogEntry[]>>({})
const logsLoading = ref<Record<string, boolean>>({})
const logsError = ref<Record<string, string | null>>({})
const openServer = ref<string | null>(null)

const stats = computed(() => (release.value ? releaseStats(release.value) : null))
const duration = computed(() => (release.value ? releaseDurationSeconds(release.value) : null))
/**
 * 标题里的应用名：优先详情自带的 app_name/app_slug；真后端只给 app_id，
 * 因此拿不到名字时按 app_id 查应用列表；仍查不到才回退显示 app_id。
 * 与应用列表页共用 deploy-domain 的 releaseAppLabel，避免两套口径。
 */
const appLabel = computed(() => {
  if (!release.value) return releaseId.value
  return releaseAppLabel(release.value, resolvedAppNames.value)
})
const title = computed(() =>
  release.value ? `${appLabel.value} · v${release.value.version}` : releaseId.value,
)
const runs = computed(() => {
  const list = release.value?.runs ?? []
  return list.slice().sort((a, b) => (a.started_at ?? 0) - (b.started_at ?? 0))
})

async function resolveAppNames(target: Release): Promise<void> {
  if (target.app_name || target.app_slug) return
  try {
    const apps = await api.apps()
    // 期间切换到别的发布就丢弃这次结果，避免串号。
    if (release.value?.app_id !== target.app_id) return
    resolvedAppNames.value = buildAppNameMap(apps)
  } catch {
    // 应用列表不可读（如仅 deploy.read）不影响详情展示，标题回退到 app_id。
    if (release.value?.app_id === target.app_id) resolvedAppNames.value = null
  }
}

async function load(keep = false): Promise<void> {
  if (!canRead.value || !releaseId.value) return
  if (keep) refreshing.value = true
  else state.value = 'loading'
  error.value = null
  notFound.value = false
  try {
    const next = await api.release(releaseId.value)
    release.value = next
    resolvedAppNames.value = null
    await resolveAppNames(next)
    state.value = 'ready'
    loaded.value = true
  } catch (caught) {
    const message = errorMessage(caught, t('releaseDetail.loadFailed'))
    notFound.value = /404|not found|不存在/i.test(message)
    error.value = message
    state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

onMounted(() => void load())
watch(releaseId, () => {
  release.value = null
  resolvedAppNames.value = null
  logsByServer.value = {}
  void load()
})

async function loadLogs(serverId: string): Promise<void> {
  if (openServer.value === serverId) {
    openServer.value = null
    return
  }
  openServer.value = serverId
  if (logsByServer.value[serverId]) return
  logsLoading.value = { ...logsLoading.value, [serverId]: true }
  logsError.value = { ...logsError.value, [serverId]: null }
  try {
    const entries = await api.releaseLogs(releaseId.value, serverId, 0)
    logsByServer.value = { ...logsByServer.value, [serverId]: entries }
  } catch (caught) {
    logsError.value = { ...logsError.value, [serverId]: errorMessage(caught, t('releaseDetail.logsFailed')) }
  } finally {
    logsLoading.value = { ...logsLoading.value, [serverId]: false }
  }
}

async function runAction(kind: 'retry' | 'cancel' | 'rollback', targetReleaseId = ''): Promise<void> {
  if (!release.value) return
  actionBusy.value = true
  actionError.value = null
  try {
    if (kind === 'retry') {
      await api.retryRelease(release.value.id)
      notice.value = t('releaseDetail.retryDone')
    } else if (kind === 'cancel') {
      await api.cancelRelease(release.value.id)
      notice.value = t('release.abortDone', { count: stats.value?.active ?? 0 })
    } else {
      await api.rollbackRelease(release.value.id, { target_release_id: targetReleaseId })
      notice.value = t('release.rollbackToTargetDone')
    }
    confirming.value = null
    rollbackOpen.value = false
    await load(true)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('common.error'))
  } finally {
    actionBusy.value = false
  }
}

function runDuration(started: number | null, finished: number | null): string {
  if (!started || !finished) return t('releaseDetail.running')
  return t('appDetail.duration', { seconds: Math.max(0, Math.round((finished - started) / 1000)) })
}
</script>

<template>
  <section class="ls-deploy" aria-labelledby="release-detail-title">
    <RouterLink class="ls-deploy__back" :to="release ? `/apps/${encodeURIComponent(release.app_id)}` : '/releases'">
      <AppIcon name="chevron-right" size="0.875rem" />
      {{ release ? t('appDetail.back') : t('releases.title') }}
    </RouterLink>

    <header class="ls-deploy__head">
      <div class="ls-deploy__heading">
        <h1 id="release-detail-title" class="ls-deploy__title">
          {{ title }}
          <span v-if="release" class="ls-badge" :class="`ls-badge--${releaseStatusTone(release.status)}`">
            {{ t(releaseStatusLabelKey(release.status)) }}
          </span>
        </h1>
        <p v-if="release" class="ls-deploy__subtitle">
          {{ t('releases.createdAt') }} {{ formatDateTime(release.created_at) }}（{{
            formatRelativeTime(release.created_at)
          }}）<span v-if="duration !== null"> · {{ t('appDetail.duration', { seconds: duration }) }}</span>
        </p>
        <p class="ls-deploy__mono ls-deploy__muted">release {{ releaseId }}</p>
      </div>
      <div class="ls-deploy__actions">
        <button type="button" class="ls-btn" :disabled="refreshing" @click="load(true)">
          <AppIcon name="refresh" size="1rem" />
          {{ t('common.refresh') }}
        </button>
        <button
          v-if="canWrite && stats && stats.failed > 0"
          type="button"
          class="ls-btn"
          @click="confirming = 'retry'"
        >
          {{ t('releaseDetail.retry') }}
        </button>
        <button v-if="canWrite && stats && stats.success > 0" type="button" class="ls-btn" @click="rollbackOpen = true">
          {{ t('release.rollbackToTarget') }}
        </button>
        <button
          v-if="canWrite && stats && stats.active > 0"
          type="button"
          class="ls-btn ls-btn--danger"
          @click="confirming = 'cancel'"
        >
          {{ t('release.abort') }}
        </button>
      </div>
    </header>

    <div v-if="!canRead" class="ls-notice" role="status">
      <p class="ls-notice__title">{{ t('releases.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('releases.noPermissionText') }}</p>
    </div>

    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>
    <p v-if="actionError" class="ls-inline-error" role="alert">{{ actionError }}</p>

    <div v-if="state === 'loading' && !loaded" class="ls-skeleton ls-deploy__skeleton-row" aria-busy="true" />

    <div v-else-if="error && !loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ notFound ? t('releaseDetail.notFoundTitle') : t('releaseDetail.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="load()">{{ t('common.retry') }}</button>
    </div>

    <template v-else-if="release">
      <div v-if="stats" class="ls-deploy__stats">
        <div class="ls-deploy__stat">
          <span class="ls-deploy__stat-label">{{ t('apps.progress.success') }}</span>
          <span class="ls-deploy__stat-value ls-tone--success">{{ stats.success }}</span>
        </div>
        <div class="ls-deploy__stat">
          <span class="ls-deploy__stat-label">{{ t('apps.progress.failed') }}</span>
          <span class="ls-deploy__stat-value ls-tone--danger">{{ stats.failed }}</span>
        </div>
        <div class="ls-deploy__stat">
          <span class="ls-deploy__stat-label">{{ t('apps.progress.active') }}</span>
          <span class="ls-deploy__stat-value ls-tone--warning">{{ stats.active }}</span>
        </div>
        <div class="ls-deploy__stat">
          <span class="ls-deploy__stat-label">{{ t('releaseDetail.total') }}</span>
          <span class="ls-deploy__stat-value">{{ stats.total }}</span>
        </div>
      </div>

      <article class="ls-card">
        <header class="ls-card__head">
          <div class="ls-card__heading">
            <h2 class="ls-card__title">{{ t('releaseDetail.timeline') }}</h2>
            <p class="ls-card__subtitle">{{ t('releaseDetail.timelineHint') }}</p>
          </div>
        </header>
        <div v-if="runs.length === 0" class="ls-empty">
          <p class="ls-empty__title">{{ t('releaseDetail.noRunsTitle') }}</p>
          <p class="ls-empty__text">{{ t('releaseDetail.noRunsText') }}</p>
        </div>
        <ol v-else class="ls-timeline" data-testid="release-timeline">
          <li
            v-for="run in runs"
            :key="run.id"
            class="ls-timeline__item"
            :class="`ls-timeline__item--${releaseStatusTone(run.status)}`"
            :data-run-server="run.server_id"
          >
            <div class="ls-timeline__marker" aria-hidden="true" />
            <div class="ls-timeline__body">
              <div class="ls-timeline__head">
                <span class="ls-timeline__server">{{ run.server_name || run.server_id }}</span>
                <span class="ls-badge" :class="`ls-badge--${releaseStatusTone(run.status)}`">
                  {{ t(releaseStatusLabelKey(run.status)) }}
                </span>
                <span class="ls-timeline__time">{{ formatRelativeTime(run.started_at ?? run.finished_at) }}</span>
                <span class="ls-timeline__duration">{{ runDuration(run.started_at, run.finished_at) }}</span>
              </div>
              <p v-if="run.failed_step" class="ls-timeline__fail">
                {{ t('releaseDetail.failedStep', { step: run.failed_step }) }}
              </p>
              <p v-if="run.message" class="ls-timeline__message">{{ run.message }}</p>
              <button
                type="button"
                class="ls-btn ls-btn--small"
                :aria-expanded="openServer === run.server_id"
                :aria-controls="`run-logs-${run.server_id}`"
                @click="loadLogs(run.server_id)"
              >
                {{ openServer === run.server_id ? t('releaseDetail.hideLogs') : t('releaseDetail.showLogs') }}
              </button>
              <div v-if="openServer === run.server_id" :id="`run-logs-${run.server_id}`" class="ls-timeline__logs">
                <p v-if="logsLoading[run.server_id]" class="ls-timeline__hint">{{ t('common.loading') }}</p>
                <p v-else-if="logsError[run.server_id]" class="ls-inline-error" role="alert">
                  {{ logsError[run.server_id] }}
                </p>
                <p v-else-if="(logsByServer[run.server_id] ?? []).length === 0" class="ls-timeline__hint">
                  {{ t('releaseDetail.noLogs') }}
                </p>
                <pre v-else class="ls-timeline__pre">{{ (logsByServer[run.server_id] ?? []).map((entry) => entry.chunk).join('') }}</pre>
              </div>
            </div>
          </li>
        </ol>
      </article>
    </template>

    <ConfirmDialog
      :open="confirming === 'retry'"
      :title="t('releaseDetail.retryTitle')"
      :description="t('releaseDetail.retryDescription')"
      :details="release && stats ? [appLabel, `v${release.version}`, String(stats.failed)] : []"
      :confirm-label="t('releaseDetail.retry')"
      tone="default"
      :busy="actionBusy"
      :error="actionError"
      test-id="release-retry-dialog"
      @confirm="runAction('retry')"
      @close="confirming = null"
    />
    <ConfirmDialog
      :open="confirming === 'cancel'"
      :title="t('release.abortConfirmTitle')"
      :description="release && stats ? t('release.abortConfirmBody', { count: stats.active }) : ''"
      :details="release ? [appLabel, `v${release.version}`] : []"
      :confirm-label="t('release.abort')"
      :busy="actionBusy"
      :error="actionError"
      test-id="release-cancel-dialog"
      @confirm="runAction('cancel')"
      @close="confirming = null"
    />
    <RollbackDialog
      v-if="release"
      :open="rollbackOpen"
      :app-id="release.app_id"
      :app-name="appLabel"
      :busy="actionBusy"
      :error="actionError"
      @submit="(target) => runAction('rollback', target)"
      @close="rollbackOpen = false"
    />
  </section>
</template>
