<script setup lang="ts">
import { computed } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { SConfigProvider } from '@vean/ui'
import type { ConfigProviderThemeOptions } from '@vean/ui'

import AppShell from '@/components/AppShell.vue'
import { BRAND_PRESET } from '@/theme/brand'

/**
 * 品牌主题（唯一来源：@/theme/brand）。
 * `primary` 只接受 Vean 的 26 个内置调色板名，品牌色靠 `preset` 精确到语义 token；
 * 主色 #16a34a（green.600），hover 等价阶 #22c55e（green.500），深色降亮一阶 #15803d。
 * 不要把 hex 写回这个文件——色阶与对比度说明都在 theme/brand.ts。
 */
const route = useRoute()
/** 登录页等不打外壳（无侧栏 / 顶栏），其余页面统一进 AppShell。 */
const blankLayout = computed(() => route.meta.layout === 'blank')

const theme: ConfigProviderThemeOptions = {
  base: 'gray',
  primary: 'green',
  preset: BRAND_PRESET,
  feedback: 'classic',
  radius: '0.625rem',
  size: 'md',
}
</script>

<template>
  <SConfigProvider :theme="theme">
    <RouterView v-slot="{ Component }">
      <component :is="Component" v-if="blankLayout" />
      <AppShell v-else>
        <component :is="Component" />
      </AppShell>
    </RouterView>
  </SConfigProvider>
</template>
