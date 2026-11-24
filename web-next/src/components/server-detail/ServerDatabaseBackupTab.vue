<script setup lang="ts">
import { ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import { api, type Server } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { pushToast } from '@/lib/toast'

/**
 * 「备份」标签：以 MySQL/MariaDB 逻辑备份为主。
 * 通过目标机上的 mysqldump 执行（agent/SSH 通道），生成的 SQL 直接下载。
 */
const props = defineProps<{
  server: Server
  canWrite: boolean
}>()

const { t } = useI18n()
const form = ref({
  host: '127.0.0.1',
  port: '3306',
  user: 'root',
  password: '',
  database: '',
})
const busy = ref(false)
const error = ref<string | null>(null)
const notice = ref<string | null>(null)

function fieldError(): string | null {
  if (form.value.host.trim() === '') return t('server.database.hostRequired')
  const port = Number(form.value.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) return t('server.database.portInvalid')
  if (form.value.user.trim() === '') return t('server.database.userRequired')
  if (form.value.database.trim() === '') return t('server.database.nameRequired')
  return null
}

async function backup(): Promise<void> {
  if (!props.canWrite || busy.value) return
  const invalid = fieldError()
  if (invalid) {
    error.value = invalid
    return
  }
  busy.value = true
  error.value = null
  notice.value = null
  try {
    const { blob, filename } = await api.backupServerDatabase(props.server.id, {
      engine: 'mysql',
      host: form.value.host.trim(),
      port: Number(form.value.port),
      user: form.value.user.trim(),
      password: form.value.password,
      database: form.value.database.trim(),
    })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
    notice.value = t('server.database.done', { name: filename, size: formatBytes(blob.size) })
    pushToast({ tone: 'success', title: notice.value })
  } catch (caught) {
    error.value = errorMessage(caught, t('server.database.failed'))
  } finally {
    busy.value = false
  }
}
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
    <div v-else class="ls-adminpage__form">
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-host">
          <span class="ls-field__label">{{ t('server.database.host') }}</span>
          <input id="db-backup-host" v-model="form.host" class="ls-input" type="text" autocomplete="off" />
        </label>
        <label class="ls-field" for="db-backup-port">
          <span class="ls-field__label">{{ t('server.database.port') }}</span>
          <input id="db-backup-port" v-model="form.port" class="ls-input" type="number" min="1" max="65535" />
        </label>
      </div>
      <div class="ls-form__row">
        <label class="ls-field" for="db-backup-user">
          <span class="ls-field__label">{{ t('server.database.user') }}</span>
          <input id="db-backup-user" v-model="form.user" class="ls-input" type="text" autocomplete="username" />
        </label>
        <label class="ls-field" for="db-backup-password">
          <span class="ls-field__label">{{ t('server.database.password') }}</span>
          <input id="db-backup-password" v-model="form.password" class="ls-input" type="password" autocomplete="current-password" />
        </label>
      </div>
      <label class="ls-field" for="db-backup-database">
        <span class="ls-field__label">{{ t('server.database.database') }}</span>
        <input id="db-backup-database" v-model="form.database" class="ls-input" type="text" autocomplete="off" :placeholder="t('server.database.databasePlaceholder')" />
        <span class="ls-field__hint">{{ t('server.database.securityHint') }}</span>
      </label>
      <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
      <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>
      <div class="ls-form__actions">
        <button id="db-backup-submit" class="ls-btn ls-btn--primary" type="button" :disabled="busy" :aria-busy="busy" @click="backup">
          <span v-if="busy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="download" size="0.875rem" />
          {{ busy ? t('server.database.running') : t('server.database.run') }}
        </button>
      </div>
    </div>
  </section>
</template>
