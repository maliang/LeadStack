<script setup lang="ts">
import { computed, defineComponent, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { SConfigProvider, useTheme } from '@vean/ui'
import type { ConfigProviderThemeOptions } from '@vean/ui'

import AppShell from '@/components/AppShell.vue'
import AppToaster from '@/components/AppToaster.vue'
import { BRAND_PRESET } from '@/theme/brand'
import { setLanguage, useI18n } from '@/i18n'
import { applyDocumentTitle } from '@/router'
import {
  appearanceDensity,
  DEFAULT_APPEARANCE,
  isAppearanceDensity,
  readStoredMode,
  setDensity,
} from '@/lib/appearance'

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

/**
 * 默认主题为「跟随系统」。
 * @vean/ui 0.51.5 在没有持久化 envelope 时把 mode 兜底成 'light'，所以这里在 provider
 * 内部挂一个 bridge：仅当用户从未选过主题（envelope 无 mode）时写入 auto。一旦持久化过，
 * 后续刷新以 envelope 为准，不再覆盖用户的选择。主题本身的读写由 provider 的 context
 * （mode / effectiveMode）驱动，SThemeModeSelect 直接 v-model 到同一个 mode。
 */
const AppearanceDefaultMode = defineComponent({
  name: 'AppearanceDefaultMode',
  setup() {
    const { mode } = useTheme()
    if (readStoredMode() === null) mode.value = DEFAULT_APPEARANCE.mode
    return () => null
  },
})

const { language } = useI18n()

/**
 * 切语言不是导航，路由 afterEach 不会触发：这里监听语言变化重拼标签题，
 * 否则用户从中文切到 English 后标签题仍是中文，直到下次跳转才更新。
 */
watch(language, () => applyDocumentTitle())

/** Vean 组件的内置文案（SThemeModeSelect 的自动/浅色/深色）跟随界面语言。 */
const providerLocale = computed(() => (language.value === 'en-US' ? 'en' : 'zh-CN'))

/**
 * 语言与密度塞进 provider 的同一个持久化 envelope（persist-theme）。
 * provider 的 writer 在 watch getter 里调用本函数，响应式依赖会被追踪，且与主题共用
 * 同一个 debounce writer，不会出现双写冲突（已读 @vean/ui 0.51.5 源码确认）。
 */
function getExtraTheme(): Record<string, unknown> {
  return { language: language.value, density: appearanceDensity.value }
}

/** envelope 回填：首次挂载与其它标签页写入时都会回调。 */
function onExtraThemeChange(extra: Record<string, unknown>): void {
  if (!extra) return
  const nextLanguage = extra.language
  if (nextLanguage === 'zh-CN' || nextLanguage === 'en-US') setLanguage(nextLanguage)
  if (isAppearanceDensity(extra.density)) setDensity(extra.density)
}
</script>

<template>
  <SConfigProvider
    :theme="theme"
    :locale="providerLocale"
    :get-extra-theme="getExtraTheme"
    :on-extra-theme-change="onExtraThemeChange"
    persist-theme
  >
    <AppearanceDefaultMode />
    <RouterView v-slot="{ Component }">
      <component :is="Component" v-if="blankLayout" />
      <AppShell v-else>
        <component :is="Component" />
      </AppShell>
    </RouterView>
    <!-- 全局提示：错误常驻、可关闭可复制（A27）；数据在 lib/toast.ts -->
    <AppToaster />
  </SConfigProvider>
</template>
