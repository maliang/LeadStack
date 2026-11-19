<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  api,
  type BackupInfo,
  type BackupManifest,
  type BackupSettings,
  type RestoreStagedResult,
  type SystemRestoreStatus,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import {
  formatBytes,
  formatDateTime,
  formatRelativeTime,
  toEditableInterval,
  truncateMiddle,
} from '@/lib/format'

const { t } = useI18n()
const { can } = useCan()
const { scope } = useAuth()

const canRead = computed(() => can('backup.read') && scope.value.mode === 'all')
const canCreate = computed(() => can('backup.write'))
const canDelete = computed(() => can('backup.delete'))
// 校验与恢复共用 backup.restore（internal/api/router.go:183-189），且都要求全局范围。
const canRestore = computed(() => can('backup.restore') && scope.value.mode === 'all')
const canWriteSettings = computed(() => can('backup.write'))

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
  if (!canRead.value) return
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
  if (!canCreate.value) return
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

/* ---------------------------- 归档校验 ---------------------------- */

interface VerifyOutcome {
  ok: boolean
  manifest: BackupManifest | null
  error: string | null
}

const verifyResults = ref<Record<string, VerifyOutcome>>({})
const verifyingName = ref<string | null>(null)

const verifyRows = computed(() =>
  backups.value.map((backup) => ({ backup, result: verifyResults.value[backup.name] ?? null })),
)

function verifyChipLabel(ok: boolean): string {
  return ok ? 'ok=true' : 'ok=false'
}

/** manifest → 摘要行（纯函数，模板只负责排版）。 */
function manifestRows(manifest: BackupManifest): { label: string; value: string }[] {
  return [
    { label: t('backups.manifestFormat'), value: String(manifest.format_version ?? t('ops.common.unknown')) },
    { label: t('backups.manifestAppVersion'), value: manifest.app_version ?? t('ops.common.unknown') },
    { label: t('backups.manifestSchema'), value: String(manifest.schema_version ?? t('ops.common.unknown')) },
    {
      label: t('backups.manifestCreatedAt'),
      value: manifest.created_at ? formatDateTime(manifest.created_at) : t('ops.common.unknown'),
    },
    {
      label: t('backups.manifestArtifacts'),
      value: manifest.includes_artifacts ? t('ops.backups.artifactsYes') : t('ops.backups.artifactsNo'),
    },
  ]
}

async function verifyBackup(name: string): Promise<void> {
  if (!canRestore.value || verifyingName.value !== null) return
  verifyingName.value = name
  try {
    const result = await api.verifyBackup(name)
    verifyResults.value = { ...verifyResults.value, [name]: result }
  } catch (caught) {
    // 400 的可读原因（归档损坏、schema 过新等）原样展示，不吞成通用文案。
    verifyResults.value = {
      ...verifyResults.value,
      [name]: { ok: false, manifest: null, error: errorMessage(caught, t('backups.verifyFailed')) },
    }
  } finally {
    verifyingName.value = null
  }
}

function clearVerify(name: string): void {
  const next = { ...verifyResults.value }
  delete next[name]
  verifyResults.value = next
}

/* ---------------------------- 从归档恢复 ---------------------------- */

const RESTORE_POLL_INTERVAL_MS = 3_000
const RESTORE_POLL_MAX_ATTEMPTS = 20

const restoreFileInput = ref<HTMLInputElement | null>(null)
const restoreFile = ref<File | null>(null)
const restoreFileError = ref<string | null>(null)
const restoreConfirmOpen = ref(false)
const restoreStaging = ref(false)
const restoreError = ref<string | null>(null)
const restoreStaged = ref<RestoreStagedResult | null>(null)
const restoreNotice = ref<string | null>(null)
const restoreStatus = ref<SystemRestoreStatus | null>(null)
const restoreStatusError = ref<string | null>(null)
const restorePollActive = ref(false)
const restorePollAttempts = ref(0)
const restorePollExhausted = ref(false)
let restorePollTimer: ReturnType<typeof setInterval> | null = null

const restoreFileName = computed(() => restoreFile.value?.name ?? '')
const restoreFileSize = computed(() => restoreFile.value?.size ?? null)

const restoreStagedParts = computed(() => {
  const staged = restoreStaged.value
  if (!staged) return []
  const parts: string[] = []
  if (staged.archive_name) parts.push(staged.archive_name)
  if (staged.size_bytes !== null) parts.push(formatBytes(staged.size_bytes))
  if (staged.schema_version !== null) parts.push(`${t('ops.backups.schemaVersion')} ${staged.schema_version}`)
  if (staged.sha256) parts.push(truncateMiddle(staged.sha256, 10, 6))
  return parts
})

const restoreStatusParts = computed(() => {
  const status = restoreStatus.value
  if (!status) return []
  const parts: string[] = []
  if (status.source) parts.push(status.source)
  if (status.schema_version !== undefined) {
    parts.push(`${t('ops.backups.schemaVersion')} ${status.schema_version}`)
  }
  if (status.archive_sha256_prefix) parts.push(truncateMiddle(status.archive_sha256_prefix, 8, 0))
  if (status.staged_at !== undefined) parts.push(formatDateTime(status.staged_at * 1000))
  return parts
})

function stopRestorePolling(): void {
  if (restorePollTimer !== null) {
    clearInterval(restorePollTimer)
    restorePollTimer = null
  }
  restorePollActive.value = false
}

async function pollRestoreStatus(): Promise<void> {
  if (restorePollAttempts.value >= RESTORE_POLL_MAX_ATTEMPTS) {
    stopRestorePolling()
    restorePollExhausted.value = true
    return
  }
  restorePollAttempts.value += 1
  try {
    const status = await api.systemRestoreStatus()
    restoreStatus.value = status
    restoreStatusError.value = null
    if (!status.pending) {
      stopRestorePolling()
      restoreNotice.value = t('backups.restoreApplied')
    }
  } catch (caught) {
    restoreStatusError.value = errorMessage(caught, t('backups.restoreStatusFailed'))
  }
  if (restorePollAttempts.value >= RESTORE_POLL_MAX_ATTEMPTS) {
    stopRestorePolling()
    restorePollExhausted.value = true
  }
}

function startRestorePolling(): void {
  stopRestorePolling()
  restorePollAttempts.value = 0
  restorePollExhausted.value = false
  restoreStatusError.value = null
  restorePollActive.value = true
  void pollRestoreStatus()
  restorePollTimer = setInterval(() => {
    void pollRestoreStatus()
  }, RESTORE_POLL_INTERVAL_MS)
}

function onRestoreFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  restoreFileError.value = null
  restoreError.value = null
  if (file && !file.name.toLowerCase().endsWith('.tar.gz')) {
    restoreFile.value = null
    restoreFileError.value = t('backups.restoreTypeError')
    input.value = ''
    return
  }
  restoreFile.value = file
}

function clearRestoreFile(): void {
  if (restoreStaging.value) return
  restoreFile.value = null
  restoreFileError.value = null
  if (restoreFileInput.value) restoreFileInput.value.value = ''
}

function openRestoreConfirm(): void {
  if (!canRestore.value || !restoreFile.value || restoreStaging.value) return
  restoreError.value = null
  restoreConfirmOpen.value = true
}

function cancelRestore(): void {
  if (restoreStaging.value) return
  restoreConfirmOpen.value = false
}

async function submitRestore(): Promise<void> {
  const file = restoreFile.value
  if (!canRestore.value || !file || restoreStaging.value) return
  restoreStaging.value = true
  restoreError.value = null
  try {
    const result = await api.restoreBackup(file)
    restoreConfirmOpen.value = false
    restoreStaged.value = result
    restoreNotice.value = t('backups.restoreStaged', { name: result.archive_name ?? file.name })
    clearRestoreFile()
    startRestorePolling()
  } catch (caught) {
    restoreError.value = errorMessage(caught, t('backups.restoreFailed'))
    restoreConfirmOpen.value = false
  } finally {
    restoreStaging.value = false
  }
}

/* ---------------------------- 备份策略 ---------------------------- */

const settingsState = ref<'loading' | 'ready' | 'error'>('loading')
const settings = ref<BackupSettings | null>(null)
const settingsError = ref<string | null>(null)
const settingsFormError = ref<string | null>(null)
const settingsNotice = ref<string | null>(null)
const savingSettings = ref(false)

const settingsForm = reactive({
  enabled: false,
  interval: '24h',
  retention: '7',
  include_artifacts: false,
})

const intervalValid = computed(() => /^\d+[smhd]$/.test(settingsForm.interval.trim()))
const retentionValid = computed(() => {
  const value = Number(settingsForm.retention)
  return Number.isInteger(value) && value >= 1
})

/** 生效值摘要（纯函数：输入 BackupSettings，输出用户可读的一行）。 */
function settingsSummary(value: BackupSettings | null): string {
  if (!value) return ''
  return t('backups.settingsEffectiveValue', {
    interval: value.interval,
    retention: String(value.retention),
    state: value.enabled ? t('backups.settingsOn') : t('backups.settingsOff'),
  })
}

const settingsEffective = computed(() => settingsSummary(settings.value))

function applySettings(value: BackupSettings): void {
  settingsForm.enabled = value.enabled
  // 后端回填的是 Go duration（24h0m0s），输入框只接受 24h 这种短串。
  settingsForm.interval = toEditableInterval(value.interval)
  settingsForm.retention = String(value.retention)
  settingsForm.include_artifacts = value.include_artifacts
}

async function loadSettings(): Promise<void> {
  if (!canRead.value) return
  settingsState.value = 'loading'
  settingsError.value = null
  try {
    const value = await api.backupSettings()
    settings.value = value
    applySettings(value)
    settingsState.value = 'ready'
  } catch (caught) {
    settingsError.value = errorMessage(caught, t('backups.settingsLoadFailed'))
    settingsState.value = 'error'
  }
}

async function saveSettings(): Promise<void> {
  if (!canWriteSettings.value || savingSettings.value) return
  settingsFormError.value = null
  settingsNotice.value = null
  const interval = settingsForm.interval.trim()
  if (!/^\d+[smhd]$/.test(interval)) {
    settingsFormError.value = t('backups.settingsIntervalInvalid')
    return
  }
  const retention = Number(settingsForm.retention)
  if (!Number.isInteger(retention) || retention < 1) {
    settingsFormError.value = t('backups.settingsRetentionInvalid')
    return
  }
  // PATCH 是部分更新：只提交与已生效值不同的字段，避免把未改动的 enabled 等一并写回。
  const patch: Partial<BackupSettings> = {}
  const current = settings.value
  if (!current) {
    settingsFormError.value = t('backups.settingsLoadFailed')
    return
  }
  if (settingsForm.enabled !== current.enabled) patch.enabled = settingsForm.enabled
  if (interval !== toEditableInterval(current.interval)) patch.interval = interval
  if (retention !== current.retention) patch.retention = retention
  if (settingsForm.include_artifacts !== current.include_artifacts) {
    patch.include_artifacts = settingsForm.include_artifacts
  }
  if (Object.keys(patch).length === 0) {
    settingsNotice.value = t('backups.settingsSaved')
    return
  }
  savingSettings.value = true
  try {
    await api.updateBackupSettings(patch)
    // 保存后重新 GET 回填生效值（后端会把 12h 规范成 12h0m0s，这里再转回输入形态）。
    const value = await api.backupSettings()
    settings.value = value
    applySettings(value)
    settingsNotice.value = t('backups.settingsSaved')
  } catch (caught) {
    settingsFormError.value = errorMessage(caught, t('backups.settingsSaveFailed'))
  } finally {
    savingSettings.value = false
  }
}

onMounted(() => {
  if (!canRead.value) return
  void load()
  void loadSettings()
})

onBeforeUnmount(() => {
  stopRestorePolling()
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

      <div class="ls-backups__grid">
        <section
          v-if="canRestore"
          id="backups-restore"
          class="ls-card ls-backups__card"
          aria-labelledby="backups-restore-title"
        >
          <div class="ls-card__head">
            <div class="ls-card__heading">
              <h2 id="backups-restore-title" class="ls-card__title">{{ t('backups.restoreTitle') }}</h2>
              <p class="ls-card__subtitle">{{ t('backups.restoreSubtitle') }}</p>
            </div>
          </div>

          <div class="ls-backups__file">
            <input
              id="backups-restore-file"
              ref="restoreFileInput"
              class="ls-visually-hidden"
              type="file"
              accept=".tar.gz"
              :disabled="restoreStaging"
              @change="onRestoreFileChange"
            />
            <label
              for="backups-restore-file"
              class="ls-btn ls-backups__file-label"
              :aria-disabled="restoreStaging ? 'true' : 'false'"
            >
              {{ t('backups.restoreChoose') }}
            </label>
            <span id="backups-restore-file-name" class="ls-backups__file-name">
              {{ restoreFile ? t('backups.restoreSelected', { name: restoreFileName, size: formatBytes(restoreFileSize) }) : t('backups.restoreNoFile') }}
            </span>
            <button
              v-if="restoreFile"
              id="backups-restore-clear"
              class="ls-btn ls-btn--small"
              type="button"
              :disabled="restoreStaging"
              @click="clearRestoreFile"
            >
              {{ t('backups.restoreClear') }}
            </button>
          </div>

          <p v-if="restoreFileError" id="backups-restore-file-error" class="ls-adminpage__form-error" role="alert">
            {{ restoreFileError }}
          </p>

          <div class="ls-backups__actions">
            <button
              id="backups-restore-submit"
              class="ls-btn ls-btn--primary"
              type="button"
              :disabled="!restoreFile || restoreStaging"
              @click="openRestoreConfirm"
            >
              <span v-if="restoreStaging" class="ls-spinner" aria-hidden="true" />
              {{ restoreStaging ? t('backups.restoreStaging') : t('backups.restoreSubmit') }}
            </button>
          </div>

          <p class="ls-card__note">{{ t('backups.restoreHint') }}</p>

          <div v-if="restoreError" id="backups-restore-error" class="ls-notice ls-notice--error" role="alert">
            <p class="ls-notice__title">{{ t('backups.restoreFailed') }}</p>
            <p class="ls-notice__text">{{ restoreError }}</p>
            <button class="ls-notice__action" type="button" @click="restoreError = null">
              {{ t('common.close') }}
            </button>
          </div>

          <div v-if="restoreStaged" id="backups-restore-staged" class="ls-backups__staged" role="status">
            <p class="ls-backups__staged-title">{{ restoreNotice }}</p>
            <p v-if="restoreStagedParts.length" class="ls-backups__staged-meta">
              {{ restoreStagedParts.join(' · ') }}
            </p>
            <p id="backups-restore-restart" class="ls-backups__restart">
              <span>{{ t('backups.restoreRestartFlag', { value: restoreStaged.restart_required ? 'true' : 'false' }) }}</span>
              <span v-if="restoreStaged.restart_required">
                {{ t('backups.restoreRestartRequired') }}
                <code class="ls-backups__command">{{ restoreStatus?.hint || t('backups.restoreCommand') }}</code>
              </span>
            </p>
            <p v-if="restorePollActive" id="backups-restore-polling" class="ls-field__hint">
              {{ t('backups.restorePolling') }}
            </p>
            <div v-if="restoreStatus" id="backups-restore-status" class="ls-backups__status">
              <span class="ls-badge" :class="restoreStatus.pending ? 'ls-badge--warning' : 'ls-badge--success'">
                {{ restoreStatus.pending ? t('backups.restoreAwaitingRestart') : t('backups.restoreApplied') }}
              </span>
              <span v-if="restoreStatusParts.length" class="ls-backups__status-meta">
                {{ restoreStatusParts.join(' · ') }}
              </span>
            </div>
            <p v-if="restoreStatusError" id="backups-restore-status-error" class="ls-adminpage__form-error" role="alert">
              {{ restoreStatusError }}
            </p>
            <p v-if="restorePollExhausted" id="backups-restore-poll-timeout" class="ls-field__hint">
              {{ t('backups.restorePollTimeout') }}
            </p>
          </div>
        </section>

        <section
          v-if="canRead"
          id="backups-settings"
          class="ls-card ls-backups__card"
          aria-labelledby="backups-settings-title"
        >
          <div class="ls-card__head">
            <div class="ls-card__heading">
              <h2 id="backups-settings-title" class="ls-card__title">{{ t('backups.settingsTitle') }}</h2>
              <p class="ls-card__subtitle">{{ t('backups.settingsSubtitle') }}</p>
            </div>
            <div class="ls-card__actions">
              <button
                id="backups-settings-refresh"
                class="ls-btn ls-btn--small"
                type="button"
                :disabled="settingsState === 'loading' || savingSettings"
                @click="loadSettings()"
              >
                {{ t('common.refresh') }}
              </button>
            </div>
          </div>

          <p v-if="!canWriteSettings" id="backups-settings-readonly" class="ls-field__hint">
            {{ t('backups.settingsReadOnly') }}
          </p>

          <div
            v-if="settingsState === 'loading'"
            id="backups-settings-skeleton"
            class="ls-adminpage__skeleton"
            aria-busy="true"
            :aria-label="t('common.loading')"
          >
            <span v-for="index in 3" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
          </div>

          <div
            v-else-if="settingsState === 'error'"
            id="backups-settings-load-error"
            class="ls-notice ls-notice--error"
            role="alert"
          >
            <p class="ls-notice__title">{{ t('backups.settingsLoadFailed') }}</p>
            <p class="ls-notice__text">{{ settingsError }}</p>
            <button id="backups-settings-retry" class="ls-notice__action" type="button" @click="loadSettings()">
              {{ t('common.retry') }}
            </button>
          </div>

          <form v-else id="backups-settings-form" class="ls-adminpage__form" @submit.prevent="saveSettings">
            <label class="ls-backups__check">
              <input
                id="backups-settings-enabled"
                v-model="settingsForm.enabled"
                type="checkbox"
                :disabled="!canWriteSettings || savingSettings"
              />
              <span>{{ t('backups.settingsEnabled') }}</span>
            </label>

            <div class="ls-backups__fields">
              <label class="ls-field">
                <span class="ls-field__label">{{ t('backups.settingsInterval') }}</span>
                <input
                  id="backups-settings-interval"
                  v-model="settingsForm.interval"
                  class="ls-input"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  :disabled="!canWriteSettings || savingSettings"
                  :aria-invalid="!intervalValid"
                  aria-describedby="backups-settings-interval-hint"
                />
                <span id="backups-settings-interval-hint" class="ls-field__hint">
                  {{ t('backups.settingsIntervalHint') }}
                </span>
              </label>

              <label class="ls-field">
                <span class="ls-field__label">{{ t('backups.settingsRetention') }}</span>
                <span class="ls-field__row">
                  <input
                    id="backups-settings-retention"
                    v-model="settingsForm.retention"
                    class="ls-input"
                    type="number"
                    min="1"
                    step="1"
                    :disabled="!canWriteSettings || savingSettings"
                    :aria-invalid="!retentionValid"
                    aria-describedby="backups-settings-retention-hint"
                  />
                  <span class="ls-backups__unit">{{ t('backups.settingsRetentionUnit') }}</span>
                </span>
                <span id="backups-settings-retention-hint" class="ls-field__hint">
                  {{ t('backups.settingsRetentionHint') }}
                </span>
              </label>
            </div>

            <label class="ls-backups__check">
              <input
                id="backups-settings-artifacts"
                v-model="settingsForm.include_artifacts"
                type="checkbox"
                :disabled="!canWriteSettings || savingSettings"
              />
              <span>{{ t('backups.settingsIncludeArtifacts') }}</span>
            </label>

            <p v-if="settingsFormError" id="backups-settings-error" class="ls-adminpage__form-error" role="alert">
              {{ settingsFormError }}
            </p>
            <p v-if="settingsNotice" id="backups-settings-notice" class="ls-notice" role="status">
              {{ settingsNotice }}
            </p>

            <div class="ls-backups__actions">
              <button
                id="backups-settings-save"
                class="ls-btn ls-btn--primary"
                type="submit"
                :disabled="!canWriteSettings || savingSettings"
              >
                <span v-if="savingSettings" class="ls-spinner" aria-hidden="true" />
                {{ savingSettings ? t('backups.settingsSaving') : t('backups.settingsSave') }}
              </button>
            </div>

            <p v-if="settingsEffective" id="backups-settings-effective" class="ls-card__note">
              {{ t('backups.settingsEffective', { summary: settingsEffective }) }}
            </p>
          </form>
        </section>
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
            <template v-for="row in verifyRows" :key="row.backup.name">
              <tr class="ls-adminpage__row ls-backupspage__row" :data-backup-name="row.backup.name">
                <td data-label="">
                  <div class="ls-adminpage__primary ls-backupspage__name">{{ row.backup.name }}</div>
                </td>
                <td :data-label="t('ops.common.createdAt')" class="ls-adminpage__date">
                  <div>{{ row.backup.created_at ? formatDateTime(row.backup.created_at) : t('common.na') }}</div>
                  <div v-if="row.backup.created_at" class="ls-adminpage__meta">
                    {{ formatRelativeTime(row.backup.created_at) }}
                  </div>
                </td>
                <td :data-label="t('ops.backups.size')">{{ formatBytes(row.backup.size_bytes) }}</td>
                <td :data-label="t('ops.backups.includesArtifacts')">
                  <span class="ls-chip ls-chip--muted">
                    {{ row.backup.includes_artifacts ? t('ops.backups.artifactsYes') : t('ops.backups.artifactsNo') }}
                  </span>
                </td>
                <td :data-label="t('ops.backups.schemaVersion')">
                  {{ row.backup.schema_version ?? t('ops.common.unknown') }}
                </td>
                <td :data-label="t('ops.backups.sha256')" class="ls-adminpage__date">
                  <span :title="row.backup.sha256 ?? ''">{{ truncateMiddle(row.backup.sha256, 10, 6) }}</span>
                </td>
                <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                  <button
                    v-if="canRestore"
                    class="ls-btn ls-btn--small ls-backupspage__verify"
                    type="button"
                    :disabled="verifyingName !== null"
                    :aria-busy="verifyingName === row.backup.name"
                    @click="verifyBackup(row.backup.name)"
                  >
                    <span v-if="verifyingName === row.backup.name" class="ls-spinner" aria-hidden="true" />
                    {{ verifyingName === row.backup.name ? t('backups.verifying') : t('backups.verify') }}
                  </button>
                  <button
                    class="ls-btn ls-btn--small ls-backupspage__download"
                    type="button"
                    :disabled="downloadingName !== null"
                    @click="downloadBackup(row.backup.name)"
                  >
                    {{ t('ops.backups.download') }}
                  </button>
                  <button
                    v-if="canDelete"
                    class="ls-btn ls-btn--small ls-adminpage__danger ls-backupspage__delete"
                    type="button"
                    :disabled="deleting"
                    @click="openDelete(row.backup)"
                  >
                    {{ t('common.delete') }}
                  </button>
                </td>
              </tr>
              <tr v-if="row.result" :key="`${row.backup.name}-verify`" class="ls-backups__verify-row" data-backup-verify-result>
                <td colspan="7">
                  <div class="ls-backups__verify-panel">
                    <div class="ls-backups__verify-head">
                      <span class="ls-badge" :class="row.result.ok ? 'ls-badge--success' : 'ls-badge--danger'">
                        {{ verifyChipLabel(row.result.ok) }}
                      </span>
                      <span class="ls-backups__verify-name">{{ row.backup.name }}</span>
                      <button
                        class="ls-btn ls-btn--small ls-backups__verify-close"
                        type="button"
                        @click="clearVerify(row.backup.name)"
                      >
                        {{ t('backups.verifyClose') }}
                      </button>
                    </div>

                    <p v-if="!row.result.ok" class="ls-adminpage__form-error" role="alert">
                      {{ row.result.error }}
                    </p>

                    <template v-else-if="row.result.manifest">
                      <dl class="ls-backups__manifest">
                        <template v-for="item in manifestRows(row.result.manifest)" :key="item.label">
                          <dt>{{ item.label }}</dt>
                          <dd>{{ item.value }}</dd>
                        </template>
                      </dl>
                      <p class="ls-field__label">
                        {{ t('backups.manifestFiles', { count: row.result.manifest.files.length }) }}
                      </p>
                      <ul v-if="row.result.manifest.files.length" class="ls-backups__file-list">
                        <li v-for="file in row.result.manifest.files" :key="file.name">
                          <span class="ls-backups__file-entry">{{ file.name }}</span>
                          <span class="ls-backups__file-size">{{ formatBytes(file.size_bytes) }}</span>
                        </li>
                      </ul>
                      <p v-else class="ls-field__hint">{{ t('backups.manifestFilesEmpty') }}</p>
                    </template>
                  </div>
                </td>
              </tr>
            </template>
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

    <AppModal
      v-if="restoreConfirmOpen"
      :title="t('backups.restoreConfirmTitle')"
      :description="t('backups.restoreConfirmImpact', { name: restoreFileName, size: formatBytes(restoreFileSize) })"
      size="sm"
      @close="cancelRestore"
    >
      <div class="ls-adminpage__form">
        <ul class="ls-backups__impact">
          <li>{{ t('backups.restoreImpactStage') }}</li>
          <li>{{ t('backups.restoreImpactOverwrite') }}</li>
          <li>{{ t('backups.restoreImpactRestart') }}</li>
          <li>{{ t('backups.restoreImpactRollback') }}</li>
        </ul>
        <p v-if="restoreError" id="backups-restore-confirm-error" class="ls-adminpage__form-error" role="alert">
          {{ restoreError }}
        </p>
      </div>
      <template #footer>
        <button id="backups-restore-cancel" class="ls-btn" type="button" :disabled="restoreStaging" @click="cancelRestore">
          {{ t('common.cancel') }}
        </button>
        <button
          id="backups-restore-confirm"
          class="ls-btn ls-btn--primary ls-adminpage__danger"
          type="button"
          :disabled="restoreStaging"
          @click="submitRestore"
        >
          <span v-if="restoreStaging" class="ls-spinner" aria-hidden="true" />
          {{ restoreStaging ? t('backups.restoreStaging') : t('backups.restoreConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
