<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import AppFormDialog from '@/components/deploy/AppFormDialog.vue'
import RollbackDialog from '@/components/deploy/RollbackDialog.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type AppItem, type CreateAppPayload, type Release } from '@/lib/api'
import {
  appScenario,
  appScenarioLabelKey,
  releaseDurationSeconds,
  releaseStats,
  releaseStatusLabelKey,
  releaseStatusTone,
} from '@/lib/deploy-domain'
import { errorMessage } from '@/lib/errors'
import { formatDateTime, formatRelativeTime } from '@/lib/format'

/**
 * 应用详情页：应用档案 + 最近发布 + 回滚/编辑入口。
 * 本页是详情与发布列表的唯一数据 owner（一次读取、刷新按钮重拉）；
 * 回滚弹窗自己拉 rollback-targets，提交后由本页重拉发布列表。
 */
const route = useRoute()
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('deploy.read'))
const canWrite = computed(() => can('deploy.write'))
const appId = computed(() => String(route.params.id ?? ''))

const app = ref<AppItem | null>(null)
const releases = ref<Release[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref<string | null>(null)
const loaded = ref(false)
const refreshing = ref(false)
const notFound = ref(false)

const editing = ref(false)
const rollbackOpen = ref(false)
const actionBusy = ref(false)
const actionError = ref<string | null>(null)
const notice = ref('')

const title = computed(() => app.value?.name || appId.value)
const latest = computed<Release | null>(() => releases.value[0] ?? null)
const stats = computed(() => (latest.value ? releaseStats(latest.value) : null))

async function load(keep = false): Promise<void> {
  if (!canRead.value || !appId.value) return
  if (keep) refreshing.value = true
  else state.value = 'loading'
  error.value = null
  notFound.value = false
  try {
    const [detail, list] = await Promise.all([
      api.app(appId.value),
      api.releases({ app_id: appId.value, limit: 50 }),
    ])
    app.value = detail
    releases.value = list
    state.value = 'ready'
    loaded.value = true
  } catch (caught) {
    const message = errorMessage(caught, t('appDetail.loadFailed'))
    notFound.value = /404|not found|不存在/i.test(message)
    error.value = message
    state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

onMounted(() => void load())
watch(appId, () => {
  app.value = null
  releases.value = []
  void load()
})

async function submitEdit(payload: CreateAppPayload): Promise<void> {
  if (!app.value) return
  actionBusy.value = true
  actionError.value = null
  try {
    app.value = await api.updateApp(app.value.id, payload)
    editing.value = false
    notice.value = t('apps.saved', { name: payload.name })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('common.error'))
  } finally {
    actionBusy.value = false
  }
}

async function submitRollback(targetReleaseId: string): Promise<void> {
  const release = latest.value
  if (!release) return
  actionBusy.value = true
  actionError.value = null
  try {
    await api.rollbackRelease(release.id, { target_release_id: targetReleaseId })
    rollbackOpen.value = false
    notice.value = t('appDetail.rollbackDone')
    await load(true)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('appDetail.rollbackFailed'))
  } finally {
    actionBusy.value = false
  }
}

</script>

<template>
  <section class="ls-deploy" aria-labelledby="app-detail-title">
    <RouterLink class="ls-deploy__back" to="/apps">
      <AppIcon name="chevron-right" size="0.875rem" />
      {{ t('appDetail.back') }}
    </RouterLink>

    <header class="ls-deploy__head">
      <div class="ls-deploy__heading">
        <h1 id="app-detail-title" class="ls-deploy__title">{{ title }}</h1>
        <p class="ls-deploy__subtitle">
          <span class="ls-deploy__mono">{{ app?.slug || appId }}</span>
          <span v-if="app"> · {{ t(appScenarioLabelKey(appScenario(app))) }}</span>
        </p>
      </div>
      <div class="ls-deploy__actions">
        <button type="button" class="ls-btn" :disabled="refreshing" @click="load(true)">
          <AppIcon name="refresh" size="1rem" />
          {{ t('common.refresh') }}
        </button>
        <button v-if="canWrite && app" type="button" class="ls-btn" @click="editing = true">
          {{ t('common.edit') }}
        </button>
        <RouterLink
          v-if="canWrite"
          class="ls-btn ls-btn--primary"
          :to="{ path: '/releases', query: { app: appId } }"
        >
          <AppIcon name="play" size="1rem" />
          {{ t('appDetail.releaseNew') }}
        </RouterLink>
      </div>
    </header>

    <div v-if="!canRead" class="ls-notice" role="status">
      <p class="ls-notice__title">{{ t('apps.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('apps.noPermissionText') }}</p>
    </div>

    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>

    <div v-if="state === 'loading' && !loaded" class="ls-skeleton ls-deploy__skeleton-row" aria-busy="true" />

    <div v-else-if="error && !loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ notFound ? t('appDetail.notFoundTitle') : t('appDetail.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="load()">{{ t('common.retry') }}</button>
    </div>

    <template v-else-if="app">
      <div v-if="error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__text">{{ error }}</p>
        <button type="button" class="ls-notice__action" @click="load(true)">{{ t('common.retry') }}</button>
      </div>

      <div class="ls-deploy__grid">
        <article class="ls-card">
          <header class="ls-card__head">
            <div class="ls-card__heading">
              <h2 class="ls-card__title">{{ t('appDetail.profile') }}</h2>
            </div>
          </header>
          <dl class="ls-kv">
            <div class="ls-kv__row">
              <dt>{{ t('apps.form.scenario') }}</dt>
              <dd class="ls-kv__value">{{ t(appScenarioLabelKey(appScenario(app))) }}</dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ app.deploy_mode === 'inplace' ? t('apps.form.targetDir') : t('apps.form.root') }}</dt>
              <dd class="ls-kv__value ls-deploy__mono">{{ app.target_dir || app.root || t('common.na') }}</dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('appDetail.healthcheck') }}</dt>
              <dd class="ls-kv__value ls-deploy__mono">{{ app.healthcheck_path || t('common.none') }}</dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('appDetail.restart') }}</dt>
              <dd class="ls-kv__value ls-deploy__mono">{{ app.restart_cmd || t('common.none') }}</dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('apps.form.note') }}</dt>
              <dd class="ls-kv__value">{{ app.description || t('common.none') }}</dd>
            </div>
          </dl>
        </article>

        <article v-if="latest" class="ls-card">
          <header class="ls-card__head">
            <div class="ls-card__heading">
              <h2 class="ls-card__title">
                {{ t('appDetail.latest') }}
                <span class="ls-badge" :class="`ls-badge--${releaseStatusTone(latest.status)}`">
                  {{ t(releaseStatusLabelKey(latest.status)) }}
                </span>
              </h2>
              <p class="ls-card__subtitle">
                v{{ latest.version }} · {{ formatDateTime(latest.created_at) }}
              </p>
            </div>
          </header>
          <p v-if="stats" class="ls-deploy__progress">
            <span class="ls-tone--success">{{ stats.success }}</span> /
            <span class="ls-tone--danger">{{ stats.failed }}</span> /
            <span class="ls-tone--warning">{{ stats.active }}</span>
            <span class="ls-deploy__muted">（{{ stats.total }}）</span>
          </p>
          <p v-if="releaseDurationSeconds(latest) !== null" class="ls-card__note">
            {{ t('appDetail.duration', { seconds: releaseDurationSeconds(latest) ?? 0 }) }}
          </p>
          <div class="ls-card__actions">
            <RouterLink class="ls-btn ls-btn--small" :to="`/releases/${encodeURIComponent(latest.id)}`">
              {{ t('common.details') }}
            </RouterLink>
            <button
              v-if="canWrite"
              type="button"
              class="ls-btn ls-btn--small ls-btn--danger"
              @click="rollbackOpen = true"
            >
              {{ t('release.rollbackToTarget') }}
            </button>
          </div>
        </article>
      </div>

      <article class="ls-card">
        <header class="ls-card__head">
          <div class="ls-card__heading">
            <h2 class="ls-card__title">{{ t('appDetail.releases') }}</h2>
            <p class="ls-card__subtitle">{{ t('appDetail.releasesHint') }}</p>
          </div>
        </header>
        <div v-if="releases.length === 0" class="ls-empty">
          <p class="ls-empty__title">{{ t('releases.emptyTitle') }}</p>
          <p class="ls-empty__text">{{ t('releases.emptyDescription') }}</p>
        </div>
        <div v-else class="ls-deploy__scroll">
          <table class="ls-deploy__table">
            <thead>
              <tr>
                <th scope="col">{{ t('releases.version') }}</th>
                <th scope="col">{{ t('common.status') }}</th>
                <th scope="col">{{ t('releases.progress') }}</th>
                <th scope="col">{{ t('releases.createdAt') }}</th>
                <th scope="col" class="ls-deploy__actions-col">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="release in releases" :key="release.id" class="ls-deploy__row">
                <td class="ls-deploy__mono">v{{ release.version }}</td>
                <td>
                  <span class="ls-badge" :class="`ls-badge--${releaseStatusTone(release.status)}`">
                    {{ t(releaseStatusLabelKey(release.status)) }}
                  </span>
                </td>
                <td class="ls-deploy__progress">
                  <span class="ls-tone--success">{{ releaseStats(release).success }}</span> /
                  <span class="ls-tone--danger">{{ releaseStats(release).failed }}</span> /
                  <span class="ls-tone--warning">{{ releaseStats(release).active }}</span>
                </td>
                <td>
                  <span :title="formatDateTime(release.created_at)">{{ formatRelativeTime(release.created_at) }}</span>
                </td>
                <td class="ls-deploy__actions-col">
                  <RouterLink class="ls-btn ls-btn--small" :to="`/releases/${encodeURIComponent(release.id)}`">
                    {{ t('common.details') }}
                  </RouterLink>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </article>
    </template>

    <AppFormDialog
      v-if="app"
      :open="editing"
      :app="app"
      :busy="actionBusy"
      :error="actionError"
      @submit="submitEdit"
      @close="editing = false"
    />
    <RollbackDialog
      :open="rollbackOpen"
      :app-id="appId"
      :app-name="title"
      :busy="actionBusy"
      :error="actionError"
      @submit="submitRollback"
      @close="rollbackOpen = false"
    />
  </section>
</template>
