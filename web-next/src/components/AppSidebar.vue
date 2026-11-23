<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from './AppIcon.vue'
import BrandMark from './BrandMark.vue'
import { isNavItemActive, type NavGroup } from '@/lib/nav'
import { useShellCopy } from '@/composables/useShellCopy'
import { useI18n } from '@/i18n'
import type { AuthStatus } from '@/composables/useAuth'

/**
 * 侧边栏：纯渲染，不持有会话或导航状态。
 *
 * 数据（已按权限过滤的分组、当前路径、会话状态）全由 AppShell 传入；
 * 这里只负责三态（加载 / 不可用 / 有数据）与语义化导航标记。
 */
const props = defineProps<{
  groups: NavGroup[]
  status: AuthStatus
  activePath: string
  /** desktop: 常驻列；drawer: 移动端抽屉内的同一份内容 */
  variant: 'desktop' | 'drawer'
  /** 会话校验失败信息（网络 / 5xx），用于失败态文案 */
  sessionError: string | null
}>()

const emit = defineEmits<{
  (event: 'navigate'): void
  (event: 'retry'): void
}>()

const copy = useShellCopy()
const { t, language } = useI18n()

/** 读取 language 以建立响应式依赖，切换语言时整列重渲染 */
function label(key: string): string {
  void language.value
  return t(key)
}

const isLoading = computed(() => props.status === 'loading')
const isEmpty = computed(() => props.groups.length === 0)
const isFailed = computed(() => props.status === 'anonymous' && Boolean(props.sessionError))

function isActive(item: NavGroup['items'][number]): boolean {
  return isNavItemActive(item, props.activePath)
}
</script>

<template>
  <div class="ls-sidebar" :class="`ls-sidebar--${variant}`">
    <div class="ls-sidebar__head">
      <RouterLink class="ls-brand" to="/" @click="emit('navigate')">
        <span class="ls-brand__mark">
          <BrandMark size="1.125rem" />
        </span>
        <span class="ls-brand__text">
          <span class="ls-brand__name">{{ copy.appName }}</span>
          <span class="ls-brand__tagline">{{ copy.appTagline }}</span>
        </span>
      </RouterLink>
    </div>

    <nav class="ls-nav" :aria-label="copy.mainNav" :aria-busy="isLoading">
      <template v-if="isLoading">
        <p class="ls-visually-hidden">{{ copy.loading }}</p>
        <ul class="ls-nav__list">
          <li v-for="index in 5" :key="`skeleton-${index}`" class="ls-nav__skeleton" aria-hidden="true" />
        </ul>
      </template>

      <template v-else-if="isFailed">
        <div class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__title">
            <AppIcon name="alert-triangle" size="1rem" />
            {{ copy.sessionErrorTitle }}
          </p>
          <p class="ls-notice__text">{{ sessionError }}</p>
          <p class="ls-notice__text">{{ copy.sessionErrorHint }}</p>
          <button class="ls-notice__action" type="button" @click="emit('retry')">
            <AppIcon name="refresh" size="0.875rem" />
            {{ copy.retry }}
          </button>
        </div>
      </template>

      <template v-else-if="isEmpty">
        <div class="ls-notice">
          <p class="ls-notice__title">{{ copy.navEmptyTitle }}</p>
          <p class="ls-notice__text">{{ copy.navEmptyHint }}</p>
        </div>
      </template>

      <template v-else>
        <div v-for="group in groups" :key="group.key" class="ls-nav__group">
          <p class="ls-nav__group-label">{{ label(group.labelKey) }}</p>
          <ul class="ls-nav__list">
            <li v-for="item in group.items" :key="item.to">
              <RouterLink
                class="ls-nav__item"
                :to="item.to"
                :aria-current="isActive(item) ? 'page' : undefined"
                @click="emit('navigate')"
              >
                <AppIcon class="ls-nav__icon" :name="item.icon" />
                <span class="ls-nav__label">{{ label(item.labelKey) }}</span>
              </RouterLink>
            </li>
          </ul>
        </div>
      </template>
    </nav>

    <div class="ls-sidebar__foot">
      <RouterLink
        class="ls-nav__item ls-sidebar__about"
        to="/about"
        :aria-current="props.activePath === '/about' ? 'page' : undefined"
        @click="emit('navigate')"
      >
        <AppIcon class="ls-nav__icon" name="info" />
        <span class="ls-nav__label">{{ label('shell.title.about') }}</span>
      </RouterLink>
    </div>
  </div>
</template>
