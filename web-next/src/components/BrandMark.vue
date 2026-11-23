<script setup lang="ts">
/**
 * LeadStack 品牌标记（Vue 版，造型与 web/src/components/BrandMark.tsx 一致）。
 *
 * 白色 L 形框架 =「领」；贯穿的脉冲折线 =「栈」上跑着的检测与健康状态。
 * 颜色走样式 owner 文件（styles/main.css）里的 token，不在组件里写死色值。
 */
withDefaults(
  defineProps<{
    size?: string
  }>(),
  { size: '1.25rem' },
)
</script>

<template>
  <svg
    class="ls-brandmark"
    viewBox="0 0 64 64"
    fill="none"
    aria-hidden="true"
    focusable="false"
    :style="{ width: size, height: size }"
  >
    <path class="ls-brandmark__frame" d="M15 8 V49 H56" stroke-width="7.6" />
    <path class="ls-brandmark__pulse" d="M24 35 L30 35 L35 20 L42 41 L47 27 L56 27" stroke-width="5.2" />
  </svg>
</template>
