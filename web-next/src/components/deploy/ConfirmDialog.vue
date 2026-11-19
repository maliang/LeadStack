<script setup lang="ts">
import { useI18n } from '@/i18n'
import ModalDialog from './ModalDialog.vue'

/**
 * 危险动作二次确认：把「对象名 + 后果」摊在弹窗里，按钮文案说明结果而不是「确定」。
 * 取消是默认焦点（ModalDialog 把焦点给第一个可聚焦元素 = 取消按钮），误按回车不会删数据。
 */
withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    /** 逐条影响范围，例如「应用名」「版本」「会影响 N 台服务器」 */
    details?: string[]
    confirmLabel: string
    tone?: 'danger' | 'default'
    busy?: boolean
    error?: string | null
    testId?: string
  }>(),
  { description: '', details: () => [], tone: 'danger', busy: false, error: null, testId: 'confirm-dialog' },
)

const emit = defineEmits<{
  (event: 'confirm'): void
  (event: 'close'): void
}>()

const { t } = useI18n()
</script>

<template>
  <ModalDialog
    :open="open"
    :title="title"
    :description="description"
    :tone="tone"
    :test-id="testId"
    @close="emit('close')"
  >
    <ul v-if="details.length > 0" class="ls-dialog__details">
      <li v-for="detail in details" :key="detail" class="ls-dialog__detail">{{ detail }}</li>
    </ul>
    <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
    <template #footer>
      <button type="button" class="ls-btn" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="ls-btn"
        :class="tone === 'danger' ? 'ls-btn--danger' : 'ls-btn--primary'"
        :disabled="busy"
        :aria-busy="busy"
        @click="emit('confirm')"
      >
        {{ busy ? t('common.loading') : confirmLabel }}
      </button>
    </template>
  </ModalDialog>
</template>
