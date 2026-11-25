<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { MetricsResponse, MetricsStep } from '@/lib/api'
import { formatClockTime, formatDateTime } from '@/lib/format'
import {
  METRICS_CHART_HEIGHT,
  METRICS_CHART_WIDTH,
  METRICS_STEPS,
  METRICS_WINDOWS,
  buildMetricCharts,
  formatMetricValue,
  type MetricsWindow,
} from '@/lib/server-metrics'

/** 安全页的指标曲线：一行两个，hover 每个点显示时间与数值。 */
const props = defineProps<{
  metrics: MetricsResponse | null
  metricsLoading: boolean
  metricsError: string | null
  metricsWindow: MetricsWindow
  metricsStep: MetricsStep
  metricsFrom: number
  metricsTo: number
}>()

const emit = defineEmits<{
  (event: 'metrics-window', value: MetricsWindow): void
  (event: 'metrics-step', value: MetricsStep): void
  (event: 'metrics-retry'): void
}>()

const { t } = useI18n()
const charts = computed(() => buildMetricCharts(props.metrics?.series ?? [], props.metricsFrom, props.metricsTo))
const metricsEmpty = computed(() => !props.metricsLoading && props.metricsError === null && charts.value.length === 0)
const rangeLabel = computed(() => `${formatClockTime(props.metricsFrom)} – ${formatClockTime(props.metricsTo)}`)
const midTime = computed(() => formatClockTime((props.metricsFrom + props.metricsTo) / 2))
</script>

<template>
  <section class="ls-card ls-metrics ls-metrics-panel" :aria-label="t('servers.detail.metrics.title')">
    <header class="ls-card__head">
      <div class="ls-card__heading">
        <h2 class="ls-card__title">
          <AppIcon name="pulse" size="1rem" />
          {{ t('servers.detail.metrics.title') }}
        </h2>
        <p class="ls-card__subtitle">{{ t('servers.detail.metrics.hint') }}</p>
      </div>
      <div class="ls-card__actions">
        <div class="ls-seg" role="group" :aria-label="t('servers.detail.metrics.step')">
          <button
            v-for="option in METRICS_STEPS"
            :key="option.key"
            type="button"
            class="ls-seg__btn"
            :class="{ 'ls-seg__btn--active': metricsStep === option.key }"
            :aria-pressed="metricsStep === option.key"
            @click="emit('metrics-step', option.key)"
          >
            {{ t(option.labelKey) }}
          </button>
        </div>
        <div class="ls-seg" role="group" :aria-label="t('servers.detail.metrics.window')">
          <button
            v-for="option in METRICS_WINDOWS"
            :key="option.key"
            type="button"
            class="ls-seg__btn"
            :class="{ 'ls-seg__btn--active': metricsWindow === option.key }"
            :aria-pressed="metricsWindow === option.key"
            @click="emit('metrics-window', option.key)"
          >
            {{ t(option.labelKey) }}
          </button>
        </div>
        <button class="ls-btn ls-btn--small" type="button" :disabled="metricsLoading" @click="emit('metrics-retry')">
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
      </div>
    </header>

    <div v-if="metricsLoading" class="ls-metrics__state" role="status" aria-busy="true">
      <span class="ls-spinner" aria-hidden="true" />
      {{ t('servers.detail.metrics.loading') }}
    </div>
    <div v-else-if="metricsError" class="ls-metrics__state" role="alert">
      <p class="ls-inline-error">{{ metricsError }}</p>
      <button class="ls-btn ls-btn--small" type="button" @click="emit('metrics-retry')">{{ t('common.retry') }}</button>
    </div>
    <div v-else-if="metricsEmpty" class="ls-metrics__state">
      <p class="ls-card__note">{{ t('servers.detail.metrics.empty') }}</p>
      <p class="ls-card__note">{{ t('servers.detail.metrics.emptyHint') }}</p>
    </div>
    <template v-else>
      <p class="ls-metrics__range" :title="rangeLabel">{{ rangeLabel }}</p>
      <div class="ls-metrics__grid ls-metrics__grid--security">
        <article v-for="chart in charts" :key="chart.metric" class="ls-metrics__item">
          <header class="ls-metrics__itemHead">
            <span class="ls-metrics__name" :title="chart.label">{{ chart.label }}</span>
            <span class="ls-metrics__value">{{ chart.latest }}</span>
          </header>
          <svg
            class="ls-metrics__chart"
            :viewBox="`0 0 ${METRICS_CHART_WIDTH} ${METRICS_CHART_HEIGHT}`"
            preserveAspectRatio="none"
            role="img"
            :aria-label="`${chart.label} ${chart.latest}`"
            focusable="false"
          >
            <line class="ls-metrics__baseline" x1="0" :y1="METRICS_CHART_HEIGHT" :x2="METRICS_CHART_WIDTH" :y2="METRICS_CHART_HEIGHT" />
            <polyline v-if="chart.line" class="ls-metrics__line" :points="chart.line" />
            <circle
              v-for="marker in chart.markers"
              :key="`${chart.metric}-${marker.ts}`"
              class="ls-metrics__hover"
              :cx="marker.x"
              :cy="marker.y"
              r="4"
            >
              <title>{{ formatDateTime(marker.ts) }} · {{ formatMetricValue(chart.metric, marker.value) }}</title>
            </circle>
            <circle class="ls-metrics__dot" :cx="chart.lastX" :cy="chart.lastY" r="3" />
            <text class="ls-metrics__axis-text" x="2" y="12">{{ chart.maxLabel }}</text>
            <text class="ls-metrics__axis-text" x="2" y="112">{{ chart.minLabel }}</text>
            <text class="ls-metrics__axis-text ls-metrics__axis-text--x" x="2" y="116" text-anchor="start">{{ formatClockTime(metricsFrom) }}</text>
            <text class="ls-metrics__axis-text ls-metrics__axis-text--x" :x="METRICS_CHART_WIDTH / 2" y="116" text-anchor="middle">{{ midTime }}</text>
            <text class="ls-metrics__axis-text ls-metrics__axis-text--x" :x="METRICS_CHART_WIDTH - 2" y="116" text-anchor="end">{{ formatClockTime(metricsTo) }}</text>
          </svg>
        </article>
      </div>
    </template>
  </section>
</template>
