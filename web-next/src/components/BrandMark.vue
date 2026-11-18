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
    <path class="ls-brandmark__frame" d="M17 11 V45 H52" stroke-width="7.2" />
    <path class="ls-brandmark__pulse" d="M26 33 L32 33 L36 21 L42 38 L46 27 L52 27" stroke-width="4.8" />
  </svg>
</template>
