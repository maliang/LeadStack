<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { ServerAccess } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import {
  ACCESS_PASSWORD_MASK,
  formatServerAccess,
  type ServerAccessLabels,
} from '@/lib/server-detail'

/**
 * 「登录信息」卡片：默认折叠，展开才由页面发起请求，收起立刻丢弃明文。
 *
 * 审计语义（ARCHITECTURE §67.6）：明文只存在于本次展开的内存里，
 * 不进 URL、不进 storage、不进 console；复制成功只回「已复制」，绝不回显内容。
 */
const props = defineProps<{
  title: string
  expanded: boolean
  access: ServerAccess | null
  loading: boolean
  error: string | null
}>()

const emit = defineEmits<{
  (event: 'toggle'): void
  (event: 'reload'): void
}>()

const { t } = useI18n()

const passwordVisible = ref(false)
const copiedKey = ref<string | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | null = null

const isKeyAuth = computed(() => props.access?.auth_type === 'key')

const labels = computed<ServerAccessLabels>(() => ({
  name: t('server.access.fieldName'),
  host: t('server.access.fieldHost'),
  port: t('server.access.fieldPort'),
  username: t('server.access.fieldUsername'),
  password: t('server.access.fieldPassword'),
}))

const passwordText = computed(() => {
  if (!props.access) return ''
  if (isKeyAuth.value) return '—'
  if (passwordVisible.value) return props.access.password
  return props.access.password === '' ? '—' : ACCESS_PASSWORD_MASK
})

const rows = computed(() => {
  if (!props.access) return []
  return [
    { key: 'name', label: t('server.access.fieldName'), value: props.access.name, mono: false },
    { key: 'host', label: t('server.access.fieldHost'), value: props.access.host, mono: true },
    {
      key: 'port',
      label: t('server.access.fieldPort'),
      value: props.access.port === null ? t('common.na') : String(props.access.port),
      mono: true,
    },
    { key: 'username', label: t('server.access.fieldUsername'), value: props.access.username, mono: true },
    { key: 'password', label: t('server.access.fieldPassword'), value: passwordText.value, mono: true },
  ]
})

function flashCopied(key: string): void {
  copiedKey.value = key
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copiedKey.value = null
    copiedTimer = null
  }, 1500)
}

async function copyText(key: string, text: string): Promise<void> {
  const ok = await writeClipboard(text)
  flashCopied(ok ? key : `${key}:failed`)
}

function copyRow(key: string): void {
  if (!props.access) return
  if (key === 'password') {
    void copyText(key, props.access.password)
    return
  }
  const row = rows.value.find((item) => item.key === key)
  if (row) void copyText(key, row.value)
}

function copyAll(): void {
  if (!props.access) return
  void copyText('all', formatServerAccess(props.access, labels.value))
}

// 收起（或换了一台服务器 / 重新读取）就丢弃可见状态：明文不再停留在界面上。
watch(
  () => [props.expanded, props.access?.server_id],
  () => {
    passwordVisible.value = false
    copiedKey.value = null
  },
)

onBeforeUnmount(() => {
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = null
})
</script>

<template>
  <section class="ls-card" :aria-label="title">
    <header class="ls-card__head">
      <div class="ls-card__heading">
        <h2 class="ls-card__title">
          <AppIcon name="key-round" size="1rem" />
          {{ title }}
        </h2>
        <p class="ls-card__subtitle">{{ t('server.access.hint') }}</p>
      </div>
      <div class="ls-card__actions">
        <button
          v-if="expanded && access"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="loading"
          @click="emit('reload')"
        >
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('server.access.refresh') }}
        </button>
        <button
          class="ls-btn ls-btn--small"
          type="button"
          :aria-expanded="expanded"
          :aria-controls="'server-access-panel'"
          @click="emit('toggle')"
        >
          <AppIcon :name="expanded ? 'chevron-right' : 'chevron-right'" size="0.875rem" />
          {{ expanded ? t('server.access.collapse') : t('server.access.expand') }}
        </button>
      </div>
    </header>

    <div v-if="expanded" id="server-access-panel" class="ls-access">
      <div v-if="loading" class="ls-access__loading" role="status">
        <span class="ls-spinner" aria-hidden="true" />
        {{ t('server.access.loading') }}
      </div>

      <div v-else-if="error" class="ls-notice ls-notice--error" role="alert">
        <div class="ls-notice__body">
          <p class="ls-notice__title">{{ t('server.access.loadFailed') }}</p>
          <p class="ls-notice__text">{{ error }}</p>
        </div>
        <button class="ls-btn ls-btn--small" type="button" @click="emit('reload')">
          {{ t('server.access.retry') }}
        </button>
      </div>

      <template v-else-if="access">
        <dl class="ls-kv">
          <div v-for="row in rows" :key="row.key" class="ls-kv__row">
            <dt>{{ row.label }}</dt>
            <dd class="ls-kv__value">
              <span class="ls-kv__text" :class="{ 'ls-kv__value--mono': row.mono }" :title="row.value">
                {{ row.value }}
              </span>
              <span class="ls-kv__tools">
                <button
                  v-if="row.key === 'password' && !isKeyAuth && access.password !== ''"
                  class="ls-icon-btn"
                  type="button"
                  @click="passwordVisible = !passwordVisible"
                >
                  {{ passwordVisible ? t('server.access.hidePassword') : t('server.access.showPassword') }}
                </button>
                <button
                  v-if="!(row.key === 'password' && (isKeyAuth || access.password === ''))"
                  class="ls-icon-btn"
                  type="button"
                  :aria-label="`${t('server.access.copy')} ${row.label}`"
                  @click="copyRow(row.key)"
                >
                  {{ copiedKey === row.key ? t('common.copied') : t('server.access.copy') }}
                </button>
              </span>
            </dd>
          </div>
        </dl>

        <p v-if="isKeyAuth" class="ls-card__note">{{ t('server.access.keyAuthNote') }}</p>

        <div class="ls-access__foot">
          <button class="ls-btn ls-btn--small" type="button" @click="copyAll">
            <AppIcon name="file-text" size="0.875rem" />
            {{ copiedKey === 'all' ? t('common.copied') : t('server.access.copyAll') }}
          </button>
          <span v-if="copiedKey?.endsWith(':failed')" class="ls-inline-error" role="alert">
            {{ t('server.access.copyFailed') }}
          </span>
          <p class="ls-card__note">{{ t('server.access.copyAllHint') }}</p>
        </div>
      </template>
    </div>
  </section>
</template>
