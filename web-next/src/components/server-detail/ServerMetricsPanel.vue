<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { MetricsResponse, MetricsStep } from '@/lib/api'
import { formatClockTime, formatDateTime } from '@/lib/format'
import {
  METRICS_CHART_HEIGHT,
  METRICS_CHART_PADDING,
  METRICS_CHART_WIDTH,
  METRICS_STEPS,
  METRICS_WINDOWS,
  buildMetricCharts,
  formatMetricValue,
  type MetricChart,
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
const hovered = ref<Record<string, MetricChart['markers'][number] | null>>({})

function onChartMove(event: MouseEvent, chart: MetricChart): void {
  const svg = event.currentTarget as SVGSVGElement
  if (chart.markers.length === 0) return
  const rect = svg.getBoundingClientRect()
  if (rect.width <= 0) return
  const x = ((event.clientX - rect.left) / rect.width) * METRICS_CHART_WIDTH
  let nearest = chart.markers[0]
  let distance = Math.abs(nearest.x - x)
  for (const marker of chart.markers.slice(1)) {
    const next = Math.abs(marker.x - x)
    if (next < distance) {
      nearest = marker
      distance = next
    }
  }
  hovered.value[chart.metric] = nearest
}

function clearHover(chart: MetricChart): void {
  hovered.value[chart.metric] = null
}

function tooltipStyle(chart: MetricChart): Record<string, string> {
  const point = hovered.value[chart.metric]
  if (!point) return {}
  return {
    left: `${Math.max(12, Math.min(88, (point.x / METRICS_CHART_WIDTH) * 100))}%`,
    top: `${Math.max(10, (point.y / METRICS_CHART_HEIGHT) * 100)}%`,
  }
}
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
          <div class="ls-metrics__plot">
            <svg
              class="ls-metrics__chart"
              :viewBox="`0 0 ${METRICS_CHART_WIDTH} ${METRICS_CHART_HEIGHT}`"
              preserveAspectRatio="none"
              role="img"
              :aria-label="`${chart.label} ${chart.latest}`"
              focusable="false"
              @mousemove="onChartMove($event, chart)"
              @mouseleave="clearHover(chart)"
            >
              <g class="ls-metrics__grid-lines" aria-hidden="true">
                <line
                  v-for="tick in chart.yTicks"
                  :key="`grid-${chart.metric}-${tick.y}`"
                  :x1="METRICS_CHART_PADDING.left"
                  :x2="METRICS_CHART_WIDTH - METRICS_CHART_PADDING.right"
                  :y1="tick.y"
                  :y2="tick.y"
                />
              </g>
              <path v-if="chart.area" class="ls-metrics__area" :d="chart.area" />
              <path v-if="chart.path" class="ls-metrics__line" :d="chart.path" />
              <circle
                v-for="marker in chart.markers"
                v-show="chart.markers.length <= 80"
                :key="`point-${chart.metric}-${marker.ts}`"
                class="ls-metrics__point"
                :cx="marker.x"
                :cy="marker.y"
                r="1.6"
              />
              <line
                v-if="hovered[chart.metric]"
                class="ls-metrics__crosshair"
                :x1="hovered[chart.metric]!.x"
                :x2="hovered[chart.metric]!.x"
                :y1="METRICS_CHART_PADDING.top"
                :y2="METRICS_CHART_HEIGHT - METRICS_CHART_PADDING.bottom"
              />
              <circle
                v-if="hovered[chart.metric]"
                class="ls-metrics__hover-dot"
                :cx="hovered[chart.metric]!.x"
                :cy="hovered[chart.metric]!.y"
                r="3.5"
              />
              <circle class="ls-metrics__dot" :cx="chart.lastX" :cy="chart.lastY" r="3" />
              <g class="ls-metrics__axis" aria-hidden="true">
                <text
                  v-for="tick in chart.yTicks"
                  :key="`y-${chart.metric}-${tick.y}`"
                  class="ls-metrics__axis-text"
                  :x="METRICS_CHART_PADDING.left - 6"
                  :y="tick.y + 3"
                  text-anchor="end"
                >{{ tick.label }}</text>
                <text
                  v-for="tick in chart.xTicks"
                  :key="`x-${chart.metric}-${tick.x}`"
                  class="ls-metrics__axis-text ls-metrics__axis-text--x"
                  :x="tick.x"
                  :y="METRICS_CHART_HEIGHT - 7"
                  text-anchor="middle"
                >{{ tick.label }}</text>
              </g>
            </svg>
            <div
              v-if="hovered[chart.metric]"
              class="ls-metrics__tooltip"
              :style="tooltipStyle(chart)"
              role="status"
            >
              <span>{{ formatDateTime(hovered[chart.metric]!.ts) }}</span>
              <strong>{{ formatMetricValue(chart.metric, hovered[chart.metric]!.value) }}</strong>
            </div>
          </div>
        </article>
      </div>
    </template>
  </section>
</template>
