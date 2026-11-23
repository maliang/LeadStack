<script setup lang="ts">
import { useI18n } from '@/i18n'
import type { BatchAction } from '@/lib/api'
import { BATCH_ACTIONS, batchActionLabelKey, isDangerousBatchAction } from '@/lib/server-batch'

/**
 * 批量操作工具条：选中数 > 0 时由页面挂载。
 *
 * 只做展示与发动作，不放请求状态；按钮全部是原生 button，键盘可达。
 * 窄宽靠 flex-wrap 换行，不靠横向滚动（320px 下不允许出现横向溢出）。
 */
const props = withDefaults(
  defineProps<{
    count: number
    busy?: boolean
  }>(),
  { busy: false },
)

const emit = defineEmits<{
  (event: 'action', action: BatchAction): void
  (event: 'clear'): void
}>()

const { t } = useI18n()
</script>

<template>
  <div class="ls-servers-batch" role="region" :aria-label="t('serversBatch.actions')">
    <p class="ls-servers-batch__count" aria-live="polite">
      {{ t('serversBatch.selected', { count: props.count }) }}
    </p>
    <div class="ls-servers-batch__actions">
      <button
        v-for="action in BATCH_ACTIONS"
        :key="action"
        class="ls-btn ls-btn--small"
        :class="{ 'ls-btn--danger': isDangerousBatchAction(action) }"
        type="button"
        :disabled="props.busy"
        @click="emit('action', action)"
      >
        {{ t(batchActionLabelKey(action)) }}
      </button>
      <button
        class="ls-btn ls-btn--small ls-servers-batch__clear"
        type="button"
        :disabled="props.busy"
        @click="emit('clear')"
      >
        {{ t('serversBatch.clearSelection') }}
      </button>
    </div>
  </div>
</template>
