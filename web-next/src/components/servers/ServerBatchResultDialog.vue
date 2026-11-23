<script setup lang="ts">
import { computed } from 'vue'
import AppModal from '@/components/AppModal.vue'
import { useI18n } from '@/i18n'
import type { BatchAction, BatchResultItem } from '@/lib/api'
import {
  batchActionLabelKey,
  batchResultTotal,
  groupBatchResults,
  serverLabel,
} from '@/lib/server-batch'

/**
 * 批量结果弹窗：成功/失败两段 + 「成功 X 台，失败 Y 台」汇总。
 *
 * 结果数组由页面持有（页面是唯一 owner）；这里只算分组与展示，
 * 失败行带「重试」入口，重试本身由页面发请求后替换条目。
 */
const props = withDefaults(
  defineProps<{
    results: BatchResultItem[]
    action: BatchAction
    names: Record<string, string>
    retriedIds?: string[]
    retryingId?: string | null
  }>(),
  { retriedIds: () => [], retryingId: null },
)

const emit = defineEmits<{
  (event: 'retry', id: string): void
  (event: 'close'): void
}>()

const { t } = useI18n()

const groups = computed(() => groupBatchResults(props.results))
const total = computed(() => batchResultTotal(groups.value))
const subtitle = computed(() =>
  t('serversBatch.result.subtitle', {
    action: t(batchActionLabelKey(props.action)),
    count: total.value,
  }),
)

function labelFor(id: string): string {
  return serverLabel(id, props.names)
}

function retried(id: string): boolean {
  return props.retriedIds.includes(id)
}

function retryDisabled(id: string): boolean {
  if (retried(id)) return true
  return props.retryingId !== null && props.retryingId !== id
}
</script>

<template>
  <AppModal
    size="lg"
    :title="t('serversBatch.result.title')"
    :description="subtitle"
    @close="emit('close')"
  >
    <div class="ls-batch-result">
      <p
        class="ls-batch-result__summary"
        :class="
          groups.summary.failed > 0
            ? 'ls-batch-result__summary--warn'
            : 'ls-batch-result__summary--ok'
        "
        role="status"
      >
        {{ t('serversBatch.result.summary', groups.summary) }}
      </p>

      <section v-if="groups.succeeded.length > 0" class="ls-batch-result__section">
        <h3 class="ls-batch-result__title">
          {{ t('serversBatch.result.successTitle', { count: groups.succeeded.length }) }}
        </h3>
        <ul class="ls-batch-result__list">
          <li
            v-for="item in groups.succeeded"
            :key="item.id"
            class="ls-batch-result__row ls-batch-result__row--ok"
          >
            <span class="ls-batch-result__name" :title="labelFor(item.id)">
              {{ labelFor(item.id) }}
            </span>
            <span class="ls-batch-result__badge ls-batch-result__badge--ok">
              {{ t('serversBatch.result.okBadge') }}
            </span>
          </li>
        </ul>
      </section>

      <section v-if="groups.failed.length > 0" class="ls-batch-result__section">
        <h3 class="ls-batch-result__title">
          {{ t('serversBatch.result.failedTitle', { count: groups.failed.length }) }}
        </h3>
        <ul class="ls-batch-result__list">
          <li
            v-for="item in groups.failed"
            :key="item.id"
            class="ls-batch-result__row ls-batch-result__row--failed"
          >
            <div class="ls-batch-result__detail">
              <span class="ls-batch-result__name" :title="labelFor(item.id)">
                {{ labelFor(item.id) }}
              </span>
              <p class="ls-batch-result__error">{{ item.error || t('serversBatch.result.noError') }}</p>
            </div>
            <button
              class="ls-btn ls-btn--small ls-batch-result__retry"
              type="button"
              :disabled="retryDisabled(item.id)"
              :aria-label="`${t('serversBatch.result.retry')} ${labelFor(item.id)}`"
              @click="emit('retry', item.id)"
            >
              <span v-if="props.retryingId === item.id" class="ls-spinner" aria-hidden="true" />
              {{
                retried(item.id)
                  ? t('serversBatch.result.retried')
                  : props.retryingId === item.id
                    ? t('serversBatch.result.retrying')
                    : t('serversBatch.result.retry')
              }}
            </button>
          </li>
        </ul>
      </section>
    </div>

    <template #footer>
      <button class="ls-btn" type="button" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </AppModal>
</template>
