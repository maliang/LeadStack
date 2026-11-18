<script setup lang="ts">
import { computed } from 'vue'
import { iconNodes, type IconName } from '@/lib/icons'

/**
 * 内联 SVG 图标。默认 1em（跟随字号），装饰性图标 aria-hidden。
 * 需要语义的地方由调用方给父元素 aria-label，图标本身不抢读屏。
 */
const props = withDefaults(
  defineProps<{
    name: IconName
    /** 同时设置宽高，避免用 font-size 之外的魔法数字 */
    size?: string
  }>(),
  { size: '1em' },
)

const nodes = computed(() => iconNodes(props.name))
</script>

<template>
  <svg
    class="ls-icon"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
    :style="{ width: size, height: size }"
  >
    <component :is="node.tag" v-for="(node, index) in nodes" :key="index" v-bind="node.attrs" />
  </svg>
</template>
