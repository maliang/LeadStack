<script setup lang="ts">
import AppModal from '@/components/AppModal.vue'

/**
 * 发布域模态适配器：只保留 `open` 受控开关与 tone/testId 两个发布域约定，
 * 焦点陷阱、Esc、滚动锁、焦点回归全部由 AppModal 统一实现。
 *
 * 历史背景：这里曾有一份与 AppModal 平行的焦点陷阱实现（约 100 行）和一套
 * `ls-dialog` 结构样式，两套行为在「焦点回归 / 滚动锁恢复」上已经出现过差异。
 * 现在只保留一份实现：需要模态的页面/组件一律走 AppModal，本文件仅是兼容层。
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    /** danger 用红色标题条，表达破坏性后果 */
    tone?: 'default' | 'danger'
    size?: 'sm' | 'md' | 'lg'
    testId?: string
  }>(),
  { description: '', tone: 'default', size: 'md', testId: 'dialog' },
)

const emit = defineEmits<{
  (event: 'close'): void
}>()
</script>

<template>
  <AppModal
    v-if="props.open"
    :title="props.title"
    :description="props.description || undefined"
    :tone="props.tone"
    :size="props.size"
    :test-id="props.testId"
    @close="emit('close')"
  >
    <slot />
    <template v-if="$slots.footer" #footer>
      <slot name="footer" />
    </template>
  </AppModal>
</template>
