<script setup lang="ts">
import { computed } from 'vue'
import { usageTone } from '@/lib/format'

/** 服务器卡片/行的环形指标：CPU / 内存 / 磁盘共用一份视觉。 */
const props = defineProps<{
  label: string
  value: number | null | undefined
  total?: string
}>()

const pct = computed(() => (typeof props.value === 'number' ? Math.max(0, Math.min(100, props.value)) : 0))
const tone = computed(() => usageTone(props.value))
const valueText = computed(() => (typeof props.value === 'number' ? `${props.value.toFixed(0)}%` : '—'))
</script>

<template>
  <span class="ls-metric-ring" :class="`ls-metric-ring--${tone}`" :style="{ '--pct': `${pct * 3.6}deg` }">
    <span class="ls-metric-ring__circle" aria-hidden="true">
      <span class="ls-metric-ring__value">{{ valueText }}</span>
    </span>
    <span class="ls-metric-ring__label">{{ label }}</span>
    <span v-if="total" class="ls-metric-ring__total">{{ total }}</span>
  </span>
</template>
