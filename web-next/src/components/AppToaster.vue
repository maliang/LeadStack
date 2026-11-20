<script setup lang="ts">
import { useI18n } from '@/i18n'
import { writeClipboard } from '@/lib/clipboard'
import { dismissToast, useToasts, type ToastItem } from '@/lib/toast'
import { ref } from 'vue'

/**
 * 全局提示渲染层（数据在 `lib/toast.ts`）。
 *
 * 无障碍约定：错误用 `role="alert"` + `aria-live="assertive"`（错误必须被立刻播报），
 * 其余用 `role="status"` + `aria-live="polite"`。
 */
const { t } = useI18n()
const toasts = useToasts()
const copiedId = ref<number | null>(null)

function copy(item: ToastItem): void {
  const text = item.requestId ? `${item.title}：${item.message}` : `${item.title}：${item.message}`
  void writeClipboard(text).then((ok) => {
    copiedId.value = ok ? item.id : null
    if (ok) setTimeout(() => { if (copiedId.value === item.id) copiedId.value = null }, 1800)
  })
}
</script>

<template>
  <div class="ls-toaster" aria-label="notifications">
    <div
      v-for="item in toasts"
      :key="item.id"
      class="ls-notice"
      :class="item.tone === 'error' ? 'ls-notice--error' : item.tone === 'success' ? 'ls-notice--success' : ''"
      :role="item.tone === 'error' ? 'alert' : 'status'"
      :aria-live="item.tone === 'error' ? 'assertive' : 'polite'"
      :data-toast-id="item.id"
      :data-toast-tone="item.tone"
    >
      <div class="ls-notice__body">
        <p class="ls-notice__title">{{ item.title }}</p>
        <p v-if="item.message" class="ls-notice__text">{{ item.message }}</p>
      </div>
      <div class="ls-toaster__actions">
        <button class="ls-btn ls-btn--small" type="button" data-toast-copy @click="copy(item)">
          {{ copiedId === item.id ? t('common.copied') : t('toast.copy') }}
        </button>
        <button class="ls-btn ls-btn--small" type="button" data-toast-close @click="dismissToast(item.id)">
          {{ t('toast.close') }}
        </button>
      </div>
    </div>
  </div>
</template>
