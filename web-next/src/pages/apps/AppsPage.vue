<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import AppFormDialog from '@/components/deploy/AppFormDialog.vue'
import ConfirmDialog from '@/components/deploy/ConfirmDialog.vue'
import { useApps } from '@/composables/useApps'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import type { AppItem, CreateAppPayload } from '@/lib/api'
import { deployModeLabelKey, filterApps, releaseStatusLabelKey, releaseStatusTone } from '@/lib/deploy-domain'
import { formatRelativeTime } from '@/lib/format'
import { pushToast } from '@/lib/toast'

/**
 * 应用列表页（挂载在 AppShell 默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是 useApps（列表 + 三态 + 新建/编辑/删除），本页只持
 * 「搜索关键字 / 开了哪个弹窗 / 成功提示」这三项 UI 状态。
 *
 * 权限：无 deploy.read 不发任何列表请求（composable 的 enabled 闸）；
 * 无 deploy.write 时新建/编辑/删除按钮整排不渲染，页面不会发出写请求。
 * 删除必须过二次确认，弹窗里写明应用名与后果。
 */
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('deploy.read'))
const canWrite = computed(() => can('deploy.write'))

const { apps, state, error, loaded, refresh, create, update, remove, writeBusy, writeError, clearWriteError } = useApps({
  enabled: () => canRead.value,
})

const keyword = ref('')
const visibleApps = computed(() => filterApps(apps.value, keyword.value))
const loading = computed(() => state.value === 'loading')

const dialog = ref<'create' | 'edit' | null>(null)
const editing = ref<AppItem | null>(null)
const deleting = ref<AppItem | null>(null)
const notice = ref('')


function openCreate(): void {
  clearWriteError()
  editing.value = null
  dialog.value = 'create'
}

function openEdit(app: AppItem): void {
  clearWriteError()
  editing.value = app
  dialog.value = 'edit'
}

function openDelete(app: AppItem): void {
  clearWriteError()
  deleting.value = app
}

async function submitApp(payload: CreateAppPayload): Promise<void> {
  const ok = editing.value ? await update(editing.value.id, payload) : await create(payload)
  if (!ok) return
  notice.value = editing.value
    ? t('apps.saved', { name: payload.name })
    : t('apps.created', { name: payload.name })
  pushToast({ tone: 'success', title: notice.value })
  dialog.value = null
  editing.value = null
}

async function confirmDelete(): Promise<void> {
  const target = deleting.value
  if (!target) return
  const ok = await remove(target.id)
  if (!ok) return
  notice.value = t('apps.deleted', { name: target.name })
  pushToast({ tone: 'success', title: notice.value })
  deleting.value = null
}

const deleteDetails = computed(() => {
  const target = deleting.value
  if (!target) return []
  return [t('apps.deleteDetailName', { name: target.name }), t('apps.deleteDetailImpact', { count: target.release_count ?? 0 })]
})
</script>

<template>
  <section class="ls-deploy" aria-labelledby="apps-title">
    <header class="ls-deploy__head">
      <div>
        <h1 id="apps-title" class="ls-deploy__title">{{ t('apps.title') }}</h1>
        <p class="ls-deploy__subtitle">{{ t('apps.description') }}</p>
      </div>
      <div class="ls-deploy__actions">
        <button id="apps-refresh" type="button" class="ls-btn" :disabled="state === 'loading'" @click="refresh">
          <AppIcon name="refresh" size="1rem" />
          {{ t('common.refresh') }}
        </button>
        <button v-if="canWrite" id="apps-new" type="button" class="ls-btn ls-btn--primary" @click="openCreate">
          <AppIcon name="plus" size="1rem" />
          {{ t('apps.new') }}
        </button>
      </div>
    </header>

    <div v-if="!canRead" class="ls-notice" role="status">
      <p class="ls-notice__title">{{ t('apps.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('apps.noPermissionText') }}</p>
    </div>

    <div v-if="error && loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('apps.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="refresh">{{ t('common.retry') }}</button>
    </div>

    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>

    <div class="ls-deploy__bar">
      <label class="ls-field ls-deploy__search" for="apps-search">
        <span class="ls-visually-hidden">{{ t('common.search') }}</span>
        <input
          id="apps-search"
          v-model="keyword"
          class="ls-input"
          type="search"
          :placeholder="t('apps.searchPlaceholder')"
          :aria-label="t('common.search')"
        />
      </label>
      <p class="ls-deploy__count" aria-live="polite">
        {{ t('apps.count', { shown: visibleApps.length, total: apps.length }) }}
      </p>
    </div>

    <div v-if="loading && !loaded" class="ls-deploy__skeleton" aria-busy="true">
      <div v-for="n in 3" :key="n" class="ls-skeleton ls-deploy__skeleton-row" />
    </div>

    <div v-else-if="error && !loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('apps.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="refresh">{{ t('common.retry') }}</button>
    </div>

    <div v-else-if="apps.length === 0" class="ls-empty">
      <p class="ls-empty__title">{{ t('apps.emptyTitle') }}</p>
      <p class="ls-empty__text">{{ t('apps.emptyText') }}</p>
      <button v-if="canWrite" type="button" class="ls-btn ls-btn--primary" @click="openCreate">
        {{ t('apps.new') }}
      </button>
    </div>

    <div v-else-if="visibleApps.length === 0" class="ls-empty">
      <p class="ls-empty__title">{{ t('apps.noMatchTitle') }}</p>
      <p class="ls-empty__text">{{ t('apps.noMatchText', { keyword }) }}</p>
      <button type="button" class="ls-btn" @click="keyword = ''">{{ t('apps.clearSearch') }}</button>
    </div>

    <div v-else class="ls-deploy__scroll">
      <table class="ls-deploy__table">
        <caption class="ls-visually-hidden">{{ t('apps.tableCaption') }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t('apps.col.name') }}</th>
            <th scope="col">{{ t('apps.col.root') }}</th>
            <th scope="col">{{ t('apps.col.mode') }}</th>
            <th scope="col">{{ t('apps.col.lastRelease') }}</th>
            <th scope="col" class="ls-deploy__num">{{ t('apps.col.servers') }}</th>
            <th scope="col" class="ls-deploy__actions-col">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="app in visibleApps" :key="app.id" class="ls-deploy__row" :data-app-id="app.id">
            <td>
              <RouterLink class="ls-deploy__name" :to="`/apps/${encodeURIComponent(app.id)}`">
                {{ app.name }}
              </RouterLink>
              <span class="ls-deploy__slug">{{ app.slug }}</span>
            </td>
            <td class="ls-deploy__mono" :title="app.root ?? ''">{{ app.root || t('common.na') }}</td>
            <td>{{ t(deployModeLabelKey(app.deploy_mode)) }}</td>
            <td>
              <span
                v-if="app.last_release"
                class="ls-badge"
                :class="`ls-badge--${releaseStatusTone(app.last_release.status)}`"
              >
                v{{ app.last_release.version }} · {{ t(releaseStatusLabelKey(app.last_release.status)) }}
              </span>
              <span v-else class="ls-deploy__muted">{{ t('apps.noRelease') }}</span>
              <span v-if="app.last_release && app.last_release.created_at" class="ls-deploy__ago">
                {{ formatRelativeTime(app.last_release.created_at) }}
              </span>
            </td>
            <td class="ls-deploy__num">{{ app.server_count ?? 0 }}</td>
            <td class="ls-deploy__actions-col">
              <div v-if="canWrite" class="ls-deploy__row-actions">
                <button
                  type="button"
                  class="ls-btn ls-btn--small"
                  :aria-label="t('apps.editNamed', { name: app.name })"
                  @click="openEdit(app)"
                >
                  {{ t('common.edit') }}
                </button>
                <button
                  type="button"
                  class="ls-btn ls-btn--small ls-btn--danger"
                  :aria-label="t('apps.deleteNamed', { name: app.name })"
                  @click="openDelete(app)"
                >
                  {{ t('common.delete') }}
                </button>
              </div>
              <span v-else class="ls-deploy__muted">{{ t('apps.readOnly') }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <AppFormDialog
      :open="dialog !== null"
      :app="editing"
      :busy="writeBusy"
      :error="writeError"
      @submit="submitApp"
      @close="dialog = null"
    />
    <ConfirmDialog
      :open="deleting !== null"
      :title="t('apps.deleteTitle')"
      :description="t('apps.deleteDescription')"
      :details="deleteDetails"
      :confirm-label="t('apps.deleteConfirm')"
      :busy="writeBusy"
      :error="writeError"
      test-id="app-delete-dialog"
      @confirm="confirmDelete"
      @close="deleting = null"
    />
  </section>
</template>
