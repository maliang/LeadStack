<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type BackupInfo } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatDateTime, formatRelativeTime, truncateMiddle } from '@/lib/format'

const { t } = useI18n()
const { can } = useCan()
const { scope } = useAuth()

const canRead = computed(() => can('backup.read') && scope.value.mode === 'all')
const canCreate = computed(() => can('backup.write'))
const canDelete = computed(() => can('backup.delete'))

const state = ref<'loading' | 'ready' | 'error'>('loading')
const backups = ref<BackupInfo[]>([])
const error = ref<string | null>(null)
const refreshing = ref(false)

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)

const includeArtifacts = ref(false)
const creating = ref(false)
const downloadingName = ref<string | null>(null)
const deleteTarget = ref<BackupInfo | null>(null)
const deleting = ref(false)

const countLabel = computed(() => t('ops.backups.count', { count: backups.value.length }))

async function load(options: { silent?: boolean } = {}): Promise<void> {
  if (options.silent) refreshing.value = true
  else state.value = 'loading'
  try {
    backups.value = await api.backups()
    state.value = 'ready'
    error.value = null
  } catch (caught) {
    error.value = errorMessage(caught, t('ops.backups.loadFailed'))
    if (options.silent) actionError.value = error.value
    else state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

async function createBackup(): Promise<void> {
  creating.value = true
  actionError.value = null
  try {
    const info = await api.createBackup(includeArtifacts.value)
    actionNotice.value = t('ops.backups.created', { name: info.name })
    await load({ silent: true })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('ops.backups.createFailed'))
  } finally {
    creating.value = false
  }
}

async function downloadBackup(name: string): Promise<void> {
  downloadingName.value = name
  actionError.value = null
  try {
    const { blob, filename } = await api.downloadBackup(name)
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
    actionNotice.value = t('ops.backups.downloadStarted', { name: filename })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('ops.backups.downloadFailed'))
  } finally {
    downloadingName.value = null
  }
}

function openDelete(backup: BackupInfo): void {
  deleteTarget.value = backup
}

function closeDelete(): void {
  if (deleting.value) return
  deleteTarget.value = null
}

async function submitDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await api.deleteBackup(target.name)
    deleteTarget.value = null
    actionNotice.value = t('ops.backups.deleted', { name: target.name })
    await load({ silent: true })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('ops.backups.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  if (canRead.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-backupspage">
    <div v-if="!canRead" id="backups-no-scope" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('backups.needsGlobalScopeTitle') }}</p>
      <p class="ls-notice__text">{{ t('backups.needsGlobalScope') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('ops.backups.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('ops.backups.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="backups-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load({ silent: true })"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </header>

      <div class="ls-adminpage__toolbar">
        <label class="ls-backupspage__create">
          <input
            id="backups-include-artifacts"
            v-model="includeArtifacts"
            type="checkbox"
            :disabled="!canCreate || creating"
          />
          <span class="ls-backupspage__create-label">
            {{ t('ops.backups.includeArtifacts') }}
            <span class="ls-adminpage__hint">{{ t('ops.backups.includeArtifactsHint') }}</span>
          </span>
        </label>
        <button
          id="backups-create"
          class="ls-btn ls-btn--primary"
          type="button"
          :disabled="!canCreate || creating"
          @click="createBackup()"
        >
          <span v-if="creating" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="archive" size="0.875rem" />
          {{ t('ops.backups.create') }}
        </button>
        <span id="backups-count" class="ls-adminpage__count" aria-live="polite">{{ countLabel }}</span>
      </div>

      <p class="ls-adminpage__form-hint">{{ t('ops.backups.securityNote') }}</p>

      <div v-if="actionError" id="backups-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="backups-action-notice" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading'"
        id="backups-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && backups.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('ops.backups.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="backups-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="backups.length === 0" id="backups-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('ops.backups.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('ops.backups.emptyDescription') }}</p>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table">
          <caption class="ls-visually-hidden">{{ t('ops.backups.title') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('ops.backups.name') }}</th>
              <th scope="col">{{ t('ops.common.createdAt') }}</th>
              <th scope="col">{{ t('ops.backups.size') }}</th>
              <th scope="col">{{ t('ops.backups.includesArtifacts') }}</th>
              <th scope="col">{{ t('ops.backups.schemaVersion') }}</th>
              <th scope="col">{{ t('ops.backups.sha256') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="backup in backups"
              :key="backup.name"
              class="ls-adminpage__row ls-backupspage__row"
              :data-backup-name="backup.name"
            >
              <td data-label="">
                <div class="ls-adminpage__primary ls-backupspage__name">{{ backup.name }}</div>
              </td>
              <td :data-label="t('ops.common.createdAt')" class="ls-adminpage__date">
                <div>{{ backup.created_at ? formatDateTime(backup.created_at) : t('common.na') }}</div>
                <div v-if="backup.created_at" class="ls-adminpage__meta">
                  {{ formatRelativeTime(backup.created_at) }}
                </div>
              </td>
              <td :data-label="t('ops.backups.size')">{{ formatBytes(backup.size_bytes) }}</td>
              <td :data-label="t('ops.backups.includesArtifacts')">
                <span class="ls-chip ls-chip--muted">
                  {{ backup.includes_artifacts ? t('ops.backups.artifactsYes') : t('ops.backups.artifactsNo') }}
                </span>
              </td>
              <td :data-label="t('ops.backups.schemaVersion')">
                {{ backup.schema_version ?? t('ops.common.unknown') }}
              </td>
              <td :data-label="t('ops.backups.sha256')" class="ls-adminpage__date">
                <span :title="backup.sha256 ?? ''">{{ truncateMiddle(backup.sha256, 10, 6) }}</span>
              </td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <button
                  class="ls-btn ls-btn--small ls-backupspage__download"
                  type="button"
                  :disabled="downloadingName !== null"
                  @click="downloadBackup(backup.name)"
                >
                  {{ t('ops.backups.download') }}
                </button>
                <button
                  v-if="canDelete"
                  class="ls-btn ls-btn--small ls-adminpage__danger ls-backupspage__delete"
                  type="button"
                  :disabled="deleting"
                  @click="openDelete(backup)"
                >
                  {{ t('common.delete') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal
      v-if="deleteTarget"
      :title="t('ops.backups.deleteTitle')"
      :description="t('ops.backups.deleteImpact', { name: deleteTarget.name })"
      size="sm"
      @close="closeDelete"
    >
      <div class="ls-adminpage__form">
        <p id="backups-delete-target" class="ls-adminpage__form-hint">{{ deleteTarget.name }}</p>
      </div>
      <template #footer>
        <button id="backups-delete-cancel" class="ls-btn" type="button" :disabled="deleting" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="backups-delete-confirm-submit"
          class="ls-btn ls-btn--primary ls-adminpage__danger"
          type="button"
          :disabled="deleting"
          @click="submitDelete"
        >
          {{ t('ops.backups.deleteConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
