<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import { api, type DatabaseBackupConfig, type DatabaseBackupFile, type Server } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatDateTime } from '@/lib/format'
import { pushToast } from '@/lib/toast'

/**
 * 数据库备份：一个配置可包含多个 MySQL/MariaDB 库；支持周期自动备份、保留份数、
 * 立即执行与下载历史 SQL。
 */
const props = defineProps<{
  server: Server
  canWrite: boolean
}>()

const { t } = useI18n()
const configs = ref<DatabaseBackupConfig[]>([])
const files = ref<Record<string, DatabaseBackupFile[]>>({})
const loading = ref(false)
const error = ref<string | null>(null)
const notice = ref<string | null>(null)
const busy = ref(false)

const editingId = ref<string | null>(null)
const form = ref({
  databases: '',
  host: '127.0.0.1',
  port: '3306',
  username: 'root',
  password: '',
  interval: '24',
  retention: '7',
  enabled: true,
})

const formTitle = computed(() =>
  editingId.value ? t('server.database.editTitle') : t('server.database.createTitle'),
)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    configs.value = await api.databaseBackupConfigs(props.server.id)
    const entries = await Promise.all(
      configs.value.map(async (cfg) => [cfg.id, await api.databaseBackupFiles(props.server.id, cfg.id)] as const),
    )
    files.value = Object.fromEntries(entries)
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.loadFailed'))
  } finally {
    loading.value = false
  }
}

function resetForm(): void {
  editingId.value = null
  form.value = { databases: '', host: '127.0.0.1', port: '3306', username: 'root', password: '', interval: '24', retention: '7', enabled: true }
}

function edit(cfg: DatabaseBackupConfig): void {
  editingId.value = cfg.id
  form.value = {
    databases: cfg.databases.join(', '),
    host: cfg.host,
    port: String(cfg.port),
    username: cfg.username,
    password: '',
    interval: String(cfg.interval_hours),
    retention: String(cfg.retention),
    enabled: cfg.enabled,
  }
}

function databaseNames(): string[] {
  return [...new Set(form.value.databases.split(/[\s,;]+/).map((item) => item.trim()).filter(Boolean))]
}

async function save(): Promise<void> {
  if (!props.canWrite || busy.value) return
  const names = databaseNames()
  if (names.length === 0) {
    error.value = t('server.database.nameRequired')
    return
  }
  if (!editingId.value && form.value.password === '') {
    error.value = t('server.database.passwordRequired')
    return
  }
  busy.value = true
  error.value = null
  try {
    await api.saveDatabaseBackupConfig(props.server.id, {
      id: editingId.value ?? undefined,
      databases: names,
      host: form.value.host.trim(),
      port: Number(form.value.port),
      username: form.value.username.trim(),
      password: form.value.password || undefined,
      interval_hours: Number(form.value.interval),
      retention: Number(form.value.retention),
      enabled: form.value.enabled,
    })
    notice.value = t('server.database.saved')
    pushToast({ tone: 'success', title: notice.value })
    resetForm()
    await load()
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.failed'))
  } finally {
    busy.value = false
  }
}

async function remove(cfg: DatabaseBackupConfig): Promise<void> {
  if (!props.canWrite || busy.value) return
  busy.value = true
  error.value = null
  try {
    await api.deleteDatabaseBackupConfig(props.server.id, cfg.id)
    if (editingId.value === cfg.id) resetForm()
    await load()
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.failed'))
  } finally {
    busy.value = false
  }
}

async function runNow(cfg: DatabaseBackupConfig): Promise<void> {
  if (!props.canWrite || busy.value) return
  busy.value = true
  error.value = null
  try {
    const result = await api.runDatabaseBackupConfig(props.server.id, cfg.id)
    notice.value = t('server.database.done', { name: result.files.join(', '), size: '' })
    await load()
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.failed'))
  } finally {
    busy.value = false
  }
}

async function download(cfg: DatabaseBackupConfig, file: DatabaseBackupFile): Promise<void> {
  try {
    const result = await api.downloadDatabaseBackupFile(props.server.id, cfg.id, file.name)
    const url = URL.createObjectURL(result.blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = result.filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.failed'))
  }
}

onMounted(load)
</script>

<template>
  <section id="server-database-backup" class="ls-card" :aria-label="t('server.database.title')">
    <header class="ls-card__head">
      <div class="ls-card__heading">
        <h2 class="ls-card__title">
          <AppIcon name="archive" size="1rem" />
          {{ t('server.database.title') }}
        </h2>
        <p class="ls-card__subtitle">{{ t('server.database.hint', { host: server.host }) }}</p>
      </div>
    </header>

    <p v-if="!canWrite" class="ls-field__hint">{{ t('server.database.noPermission') }}</p>
    <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>
    <p v-if="loading" class="ls-field__hint">{{ t('common.loading') }}</p>

    <div v-if="canWrite" class="ls-adminpage__form">
      <h3 class="ls-card__title">{{ formTitle }}</h3>
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-databases">
          <span class="ls-field__label">{{ t('server.database.databases') }}</span>
          <input id="db-backup-databases" v-model="form.databases" class="ls-input" type="text" :placeholder="t('server.database.databasesPlaceholder')" />
          <span class="ls-field__hint">{{ t('server.database.databasesHint') }}</span>
        </label>
        <label class="ls-field" for="db-backup-interval">
          <span class="ls-field__label">{{ t('server.database.interval') }}</span>
          <input id="db-backup-interval" v-model="form.interval" class="ls-input" type="number" min="1" max="720" />
          <span class="ls-field__hint">{{ t('server.database.intervalHint') }}</span>
        </label>
      </div>
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-host">
          <span class="ls-field__label">{{ t('server.database.host') }}</span>
          <input id="db-backup-host" v-model="form.host" class="ls-input" type="text" />
        </label>
        <label class="ls-field" for="db-backup-port">
          <span class="ls-field__label">{{ t('server.database.port') }}</span>
          <input id="db-backup-port" v-model="form.port" class="ls-input" type="number" min="1" max="65535" />
        </label>
      </div>
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-user">
          <span class="ls-field__label">{{ t('server.database.user') }}</span>
          <input id="db-backup-user" v-model="form.username" class="ls-input" type="text" autocomplete="username" />
        </label>
        <label class="ls-field" for="db-backup-password">
          <span class="ls-field__label">{{ t('server.database.password') }}</span>
          <input id="db-backup-password" v-model="form.password" class="ls-input" type="password" autocomplete="current-password" :placeholder="editingId ? t('server.database.passwordKeep') : ''" />
        </label>
      </div>
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-retention">
          <span class="ls-field__label">{{ t('server.database.retention') }}</span>
          <input id="db-backup-retention" v-model="form.retention" class="ls-input" type="number" min="1" max="365" />
        </label>
        <label class="ls-field ls-check">
          <input v-model="form.enabled" type="checkbox" />
          <span>{{ t('server.database.enabled') }}</span>
        </label>
      </div>
      <div class="ls-form__actions">
        <button id="db-backup-submit" class="ls-btn ls-btn--primary" type="button" :disabled="busy" :aria-busy="busy" @click="save">
          <span v-if="busy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="archive" size="0.875rem" />
          {{ editingId ? t('common.save') : t('server.database.create') }}
        </button>
        <button v-if="editingId" class="ls-btn" type="button" @click="resetForm">{{ t('common.cancel') }}</button>
      </div>
    </div>

    <div v-if="configs.length > 0" class="ls-db-backups">
      <article v-for="cfg in configs" :key="cfg.id" class="ls-db-backup" :data-config-id="cfg.id">
        <header class="ls-db-backup__head">
          <strong>{{ cfg.databases.join(', ') }}</strong>
          <span class="ls-chip" :class="cfg.enabled ? 'ls-chip--ok' : 'ls-chip--muted'">
            {{ cfg.enabled ? t('server.database.enabledOn') : t('server.database.enabledOff') }}
          </span>
          <span class="ls-field__hint">{{ t('server.database.every', { hours: cfg.interval_hours, keep: cfg.retention }) }}</span>
          <span class="ls-db-backup__actions">
            <button class="ls-btn ls-btn--small" type="button" :disabled="busy" @click="runNow(cfg)">{{ t('server.database.run') }}</button>
            <button class="ls-btn ls-btn--small" type="button" @click="edit(cfg)">{{ t('common.edit') }}</button>
            <button class="ls-btn ls-btn--small ls-btn--danger" type="button" @click="remove(cfg)">{{ t('common.delete') }}</button>
          </span>
        </header>
        <p class="ls-field__hint">
          {{ t('server.database.lastRun', { time: cfg.last_run_at ? formatDateTime(cfg.last_run_at) : t('common.na'), next: cfg.next_run_at ? formatDateTime(cfg.next_run_at) : t('common.na') }) }}
        </p>
        <p v-if="cfg.last_error" class="ls-inline-error">{{ cfg.last_error }}</p>
        <ul v-if="files[cfg.id]?.length" class="ls-db-backup__files">
          <li v-for="file in files[cfg.id]" :key="file.name">
            <button class="ls-btn ls-btn--small" type="button" @click="download(cfg, file)">
              <AppIcon name="download" size="0.75rem" />
              {{ file.name }} · {{ formatBytes(file.size) }}
            </button>
          </li>
        </ul>
      </article>
    </div>
  </section>
</template>
