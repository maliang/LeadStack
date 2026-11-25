<script setup lang="ts">
import { computed } from 'vue'
import { usageTone } from '@/lib/format'

/** 服务器卡片/行的线性指标条：CPU / 内存 / 磁盘共用一份视觉。 */
const props = defineProps<{
  label: string
  value: number | null | undefined
  used?: string | null
  total?: string | null
}>()

const pct = computed(() => (typeof props.value === 'number' ? Math.max(0, Math.min(100, props.value)) : 0))
const tone = computed(() => usageTone(props.value))
const text = computed(() => {
  if (props.used && props.total) return `${props.used} / ${props.total}`
  if (props.used) return props.used
  return typeof props.value === 'number' ? `${props.value.toFixed(0)}%` : '—'
})
</script>

<template>
  <span class="ls-metric-bar" :class="`ls-metric-bar--${tone}`">
    <span class="ls-metric-bar__label">{{ label }}</span>
    <span class="ls-metric-bar__track" aria-hidden="true">
      <span class="ls-metric-bar__fill" :style="{ width: `${pct}%` }" />
    </span>
    <span class="ls-metric-bar__text">{{ text }}</span>
  </span>
</template>
