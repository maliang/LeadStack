<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import ReleaseFormDialog from '@/components/deploy/ReleaseFormDialog.vue'
import { useApps } from '@/composables/useApps'
import { useCan } from '@/composables/useCan'
import { useReleases } from '@/composables/useReleases'
import { useI18n } from '@/i18n'
import { api, type DeployTemplate, type Server } from '@/lib/api'
import {
  buildAppNameMap,
  filterReleases,
  releaseAppLabel,
  releaseStats,
  releaseStatusLabelKey,
  releaseStatusTone,
} from '@/lib/deploy-domain'
import { errorMessage } from '@/lib/errors'
import { formatRelativeTime } from '@/lib/format'

/**
 * 发布列表页：全量发布单 + 筛选 + 发起发布（含计划预览）。
 *
 * 数据 owner：useReleases（列表 / 三态 / 创建）；应用下拉复用 useApps，
 * 模板与服务器列表只在打开「发起发布」弹窗时按需拉取（读页面不发多余请求）。
 */
const route = useRoute()
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('deploy.read'))
const canWrite = computed(() => can('deploy.write'))

const { releases, state, error, loaded, refreshing, refresh, create, writeBusy, writeError, clearWriteError } =
  useReleases({ enabled: () => canRead.value })
const { apps } = useApps({ enabled: () => canRead.value })
/** 列表接口只返回 app_id：复用应用下拉这一次请求建表，绝不按行再拉。 */
const appNames = computed(() => buildAppNameMap(apps.value))

const keyword = ref('')
const statusFilter = ref('')
const appFilter = ref('')
const loading = computed(() => state.value === 'loading')

const visible = computed(() =>
  filterReleases(releases.value, {
    keyword: keyword.value,
    status: statusFilter.value,
    appId: appFilter.value,
    appNames: appNames.value,
  }),
)

const dialogOpen = ref(false)
const templateOptions = ref<DeployTemplate[]>([])
const serverOptions = ref<Server[]>([])
const optionsError = ref<string | null>(null)
const notice = ref('')


async function openDialog(): Promise<void> {
  clearWriteError()
  optionsError.value = null
  dialogOpen.value = true
  try {
    const [templates, servers] = await Promise.all([api.templates(), api.servers()])
    templateOptions.value = templates
    serverOptions.value = servers
  } catch (caught) {
    optionsError.value = errorMessage(caught, t('apps.releaseForm.optionsFailed'))
  }
}

async function submitRelease(payload: Parameters<typeof create>[0]): Promise<void> {
  const created = await create(payload)
  if (!created) return
  dialogOpen.value = false
  notice.value = t('apps.releaseForm.created', {
    app: releaseAppLabel(created, appNames.value),
    version: created.version,
  })
}

onMounted(() => {
  const preset = route.query.app
  if (typeof preset === 'string' && preset !== '' && canWrite.value) {
    appFilter.value = preset
    void openDialog()
  }
})

const statusOptions = [
  { value: '', key: 'common.all' },
  { value: 'pending', key: 'releases.status.pending' },
  { value: 'running', key: 'releases.status.running' },
  { value: 'success', key: 'releases.status.success' },
  { value: 'failed', key: 'releases.status.failed' },
  { value: 'canceled', key: 'releases.status.canceled' },
]

const filtersActive = computed(() => keyword.value !== '' || statusFilter.value !== '' || appFilter.value !== '')

function resetFilters(): void {
  keyword.value = ''
  statusFilter.value = ''
  appFilter.value = ''
}
</script>

<template>
  <section class="ls-deploy" aria-labelledby="releases-title">
    <header class="ls-deploy__head">
      <div>
        <h1 id="releases-title" class="ls-deploy__title">{{ t('releases.title') }}</h1>
        <p class="ls-deploy__subtitle">{{ t('releases.description') }}</p>
      </div>
      <div class="ls-deploy__actions">
        <button id="releases-refresh" type="button" class="ls-btn" :disabled="loading || refreshing" @click="refresh">
          <AppIcon name="refresh" size="1rem" />
          {{ t('common.refresh') }}
        </button>
        <button v-if="canWrite" id="releases-new" type="button" class="ls-btn ls-btn--primary" @click="openDialog">
          <AppIcon name="play" size="1rem" />
          {{ t('releases.newRelease') }}
        </button>
      </div>
    </header>

    <div v-if="!canRead" class="ls-notice" role="status">
      <p class="ls-notice__title">{{ t('releases.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('releases.noPermissionText') }}</p>
    </div>

    <div v-if="error && loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('releases.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="refresh">{{ t('common.retry') }}</button>
    </div>

    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>

    <div class="ls-deploy__bar">
      <label class="ls-field ls-deploy__search" for="releases-search">
        <span class="ls-visually-hidden">{{ t('common.search') }}</span>
        <input
          id="releases-search"
          v-model="keyword"
          class="ls-input"
          type="search"
          :placeholder="t('releases.searchPlaceholder')"
          :aria-label="t('common.search')"
        />
      </label>
      <label class="ls-field ls-deploy__select" for="releases-status">
        <span class="ls-visually-hidden">{{ t('common.status') }}</span>
        <AppSelect id="releases-status" v-model="statusFilter" class="ls-input">
          <option v-for="option in statusOptions" :key="option.value" :value="option.value">
            {{ t(option.key) }}
          </option>
        </AppSelect>
      </label>
      <label class="ls-field ls-deploy__select" for="releases-app">
        <span class="ls-visually-hidden">{{ t('releases.app') }}</span>
        <AppSelect id="releases-app" v-model="appFilter" class="ls-input">
          <option value="">{{ t('releases.allApps') }}</option>
          <option v-for="app in apps" :key="app.id" :value="app.id">{{ app.name }}</option>
        </AppSelect>
      </label>
      <button v-if="filtersActive" id="releases-filter-reset" type="button" class="ls-btn ls-btn--small" @click="resetFilters">
        {{ t('releases.clearFilters') }}
      </button>
      <p class="ls-deploy__count" aria-live="polite">
        {{ t('apps.count', { shown: visible.length, total: releases.length }) }}
      </p>
    </div>

    <div v-if="loading && !loaded" class="ls-deploy__skeleton" aria-busy="true">
      <div v-for="n in 3" :key="n" class="ls-skeleton ls-deploy__skeleton-row" />
    </div>

    <div v-else-if="error && !loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('releases.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="refresh">{{ t('common.retry') }}</button>
    </div>

    <div v-else-if="releases.length === 0" class="ls-empty">
      <p class="ls-empty__title">{{ t('releases.emptyTitle') }}</p>
      <p class="ls-empty__text">{{ t('releases.emptyDescription') }}</p>
      <button v-if="canWrite" type="button" class="ls-btn ls-btn--primary" @click="openDialog">
        {{ t('releases.newRelease') }}
      </button>
    </div>

    <div v-else-if="visible.length === 0" class="ls-empty">
      <p class="ls-empty__title">{{ t('releases.noMatchTitle') }}</p>
      <p class="ls-empty__text">{{ t('releases.noMatchText') }}</p>
      <button type="button" class="ls-btn" @click="resetFilters">{{ t('releases.clearFilters') }}</button>
    </div>

    <div v-else class="ls-deploy__scroll">
      <table class="ls-deploy__table">
        <caption class="ls-visually-hidden">{{ t('releases.tableCaption') }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t('releases.app') }}</th>
            <th scope="col">{{ t('releases.version') }}</th>
            <th scope="col">{{ t('common.status') }}</th>
            <th scope="col">{{ t('releases.progress') }}</th>
            <th scope="col">{{ t('releases.createdAt') }}</th>
            <th scope="col" class="ls-deploy__actions-col">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="release in visible" :key="release.id" class="ls-deploy__row" :data-release-id="release.id">
            <td>
              <RouterLink class="ls-deploy__name" :to="`/apps/${encodeURIComponent(release.app_id)}`">
                {{ releaseAppLabel(release, appNames) }}
              </RouterLink>
            </td>
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
              <span v-if="release.strategy === 'canary'" class="ls-badge ls-badge--muted">
                {{ t('release.canaryBadge', { count: release.canary_count ?? 0, total: release.runs?.length ?? 0 }) }}
              </span>
            </td>
            <td>{{ formatRelativeTime(release.created_at) }}</td>
            <td class="ls-deploy__actions-col">
              <RouterLink class="ls-btn ls-btn--small" :to="`/releases/${encodeURIComponent(release.id)}`">
                {{ t('common.details') }}
              </RouterLink>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="optionsError" class="ls-inline-error" role="alert">{{ optionsError }}</p>

    <ReleaseFormDialog
      :open="dialogOpen"
      :apps="apps"
      :templates="templateOptions"
      :servers="serverOptions"
      :initial-app-id="typeof route.query.app === 'string' ? route.query.app : ''"
      :busy="writeBusy"
      :error="writeError"
      @submit="submitRelease"
      @close="dialogOpen = false"
    />
  </section>
</template>
