<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type RollbackTarget } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { useI18n } from '@/i18n'
import ModalDialog from './ModalDialog.vue'

/**
 * 回滚到指定版本：打开即按 app 拉 rollback-targets，加载 / 空 / 失败三态齐全，
 * 选中版本后二次确认里写明「应用名 + 目标版本 + 影响服务器数」。
 * 数据 owner 是本组件（一次选中一个目标），页面只提交结果。
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    appId: string
    appName: string
    busy?: boolean
    error?: string | null
  }>(),
  { busy: false, error: null },
)

const emit = defineEmits<{
  (event: 'submit', targetReleaseId: string): void
  (event: 'close'): void
}>()

const { t } = useI18n()
const targets = ref<RollbackTarget[]>([])
const loading = ref(false)
const loadError = ref<string | null>(null)
const selectedId = ref('')
let request = 0

const selected = computed(() => targets.value.find((target) => target.release_id === selectedId.value) ?? null)
const details = computed(() => {
  const target = selected.value
  if (!target) return []
  return [
    t('release.rollbackConfirmApp', { app: props.appName }),
    t('release.rollbackConfirmVersion', { version: target.version }),
    t('release.rollbackConfirmServers', { count: target.server_ids.length }),
  ]
})

async function load(): Promise<void> {
  const seq = ++request
  loading.value = true
  loadError.value = null
  selectedId.value = ''
  try {
    const list = await api.rollbackTargets(props.appId)
    if (seq !== request) return
    targets.value = list
    if (list.length > 0) selectedId.value = list[0].release_id
  } catch (caught) {
    if (seq !== request) return
    targets.value = []
    loadError.value = errorMessage(caught, t('release.rollbackTargetsFailed'))
  } finally {
    if (seq === request) loading.value = false
  }
}

watch(
  () => [props.open, props.appId] as const,
  ([open]) => {
    if (open && props.appId) void load()
  },
  { immediate: true },
)

function submit(): void {
  if (!selected.value || props.busy) return
  emit('submit', selected.value.release_id)
}
</script>

<template>
  <ModalDialog
    :open="open"
    :title="t('release.rollbackTargetTitle')"
    tone="danger"
    test-id="rollback-dialog"
    @close="emit('close')"
  >
    <p class="ls-modal__text">{{ t('release.rollbackTargetDescription') }}</p>
    <p v-if="loading" class="ls-modal__hint">{{ t('release.rollbackTargetsLoading') }}</p>
    <div v-else-if="loadError" class="ls-inline-error" role="alert">
      {{ t('release.rollbackTargetsFailed') }}：{{ loadError }}
      <button type="button" class="ls-btn ls-btn--small" @click="load">{{ t('common.retry') }}</button>
    </div>
    <p v-else-if="targets.length === 0" class="ls-modal__hint">{{ t('release.rollbackTargetsEmpty') }}</p>
    <fieldset v-else class="ls-modal__choices">
      <legend class="ls-visually-hidden">{{ t('release.rollbackTargetTitle') }}</legend>
      <label v-for="target in targets" :key="target.release_id" class="ls-modal__choice">
        <input v-model="selectedId" type="radio" name="rollback-target" :value="target.release_id" />
        <span class="ls-modal__choice-main">v{{ target.version }}</span>
        <span class="ls-modal__choice-meta">
          {{ formatDateTime(target.finished_at ?? target.created_at) }} · {{ target.server_ids.length }}
        </span>
      </label>
    </fieldset>
    <ul v-if="selected" class="ls-modal__details">
      <li v-for="detail in details" :key="detail" class="ls-modal__detail">{{ detail }}</li>
    </ul>
    <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
    <template #footer>
      <button type="button" class="ls-btn" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="ls-btn ls-btn--danger"
        :disabled="busy || !selected"
        :aria-busy="busy"
        @click="submit"
      >
        {{ busy ? t('common.loading') : t('release.rollbackTargetConfirm') }}
      </button>
    </template>
  </ModalDialog>
</template>
