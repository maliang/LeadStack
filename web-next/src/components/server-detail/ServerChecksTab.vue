<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { CheckItem } from '@/lib/api'
import { formatDateTime, formatLatency, formatRelativeTime, formatPercent } from '@/lib/format'
import { availabilityTone, checkStateLabelKey, checkStateTone } from '@/lib/server-detail'

/**
 * 「检测」标签：只读展示应用到这台服务器的检测项与最近状态。
 *
 * 不提供执行按钮（执行在「检测」页），避免在只读视图里触发副作用。
 * 结果由页面用 api.checks() 拉回后过滤（checksForServer），这里不自己请求。
 */
const props = defineProps<{
  checks: CheckItem[]
  loading: boolean
  error: string | null
}>()

const emit = defineEmits<{ (event: 'reload'): void }>()

const { t } = useI18n()

const downCount = computed(() => props.checks.filter((check) => check.state === 'down').length)

function availabilityText(value: number | null): string {
  return value === null ? '—' : formatPercent(value, 2)
}

function lastResultText(check: CheckItem): string {
  if (!check.last_result) return t('server.checks.noResult')
  const parts: string[] = []
  if (check.last_result.ts !== null) parts.push(formatRelativeTime(check.last_result.ts))
  if (check.last_result.message) parts.push(check.last_result.message)
  return parts.length > 0 ? parts.join(' · ') : t('server.checks.noResult')
}
</script>

<template>
  <section class="ls-card" :aria-label="t('server.checks.title')">
    <header class="ls-card__head">
      <div class="ls-card__heading">
        <h2 class="ls-card__title">
          <AppIcon name="list-checks" size="1rem" />
          {{ t('server.checks.title') }}
        </h2>
        <p class="ls-card__subtitle">{{ t('server.checks.hint') }}</p>
      </div>
      <div class="ls-card__actions">
        <span class="ls-chip ls-chip--muted" :title="t('server.checks.notRunHere')">
          {{ t('server.checks.readOnly') }}
        </span>
        <button class="ls-btn ls-btn--small" type="button" :disabled="loading" @click="emit('reload')">
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
      </div>
    </header>

    <div v-if="loading" class="ls-checks__loading" role="status">
      <span class="ls-spinner" aria-hidden="true" />
      {{ t('server.checks.loading') }}
    </div>

    <div v-else-if="error" class="ls-notice ls-notice--error" role="alert">
      <div class="ls-notice__body">
        <p class="ls-notice__title">{{ t('server.checks.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
      </div>
      <button class="ls-btn ls-btn--small" type="button" @click="emit('reload')">
        {{ t('common.retry') }}
      </button>
    </div>

    <div v-else-if="checks.length === 0" class="ls-empty" role="status">
      <AppIcon name="list-checks" size="1.25rem" />
      <p class="ls-empty__title">{{ t('server.checks.empty') }}</p>
      <p class="ls-empty__text">{{ t('server.checks.emptyHint') }}</p>
    </div>

    <template v-else>
      <p v-if="downCount > 0" class="ls-banner ls-banner--warning" role="status">
        <AppIcon name="alert-triangle" size="1rem" />
        <span>{{ t('s.0081', { '0': t('s.0318') }) }} · {{ downCount }}</span>
      </p>

      <ul class="ls-checks">
        <li v-for="check in checks" :key="check.id" class="ls-checks__row">
          <div class="ls-checks__main">
            <p class="ls-checks__name" :title="check.name">{{ check.name }}</p>
            <p class="ls-checks__target" :title="check.target">{{ check.target }}</p>
          </div>
          <div class="ls-checks__meta">
            <span class="ls-srv-status" :class="`ls-srv-status--${checkStateTone(check.state)}`">
              <span class="ls-srv-status__dot" aria-hidden="true" />
              {{ t(checkStateLabelKey(check.state)) }}
            </span>
            <span
              class="ls-checks__availability"
              :class="`ls-tone--${availabilityTone(check.availability_24h)}`"
              :title="t('server.checks.colAvailability')"
              >{{ availabilityText(check.availability_24h) }}</span
            >
            <span class="ls-checks__interval" :title="t('server.checks.colInterval')">
              {{ check.interval_s }}s
            </span>
          </div>
          <div class="ls-checks__last" :title="check.last_result?.ts ? formatDateTime(check.last_result.ts) : ''">
            <span class="ls-checks__last-label">{{ t('server.checks.colLast') }}</span>
            <span class="ls-checks__last-text">{{ lastResultText(check) }}</span>
            <span v-if="check.last_result?.latency_ms !== null && check.last_result?.latency_ms !== undefined" class="ls-checks__latency">
              {{ formatLatency(check.last_result.latency_ms) }}
            </span>
          </div>
        </li>
      </ul>
    </template>
  </section>
</template>
