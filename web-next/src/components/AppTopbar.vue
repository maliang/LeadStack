<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from './AppIcon.vue'
import type { Crumb } from '@/lib/nav'
import { useShellCopy } from '@/composables/useShellCopy'
import { useI18n } from '@/i18n'
import type { AuthStatus } from '@/composables/useAuth'

/**
 * 顶栏：抽屉开关、面包屑、会话区（加载 / 未登录+重试 / 已登录）。
 * 不持状态：开关与登出都通过事件交给 AppShell / useAuth。
 */
const props = defineProps<{
  crumbs: Crumb[]
  status: AuthStatus
  userName: string | null
  roleName: string | null
  sessionError: string | null
  mobileOpen: boolean
}>()

const emit = defineEmits<{
  (event: 'toggle-nav'): void
  (event: 'logout'): void
  (event: 'retry'): void
}>()

const copy = useShellCopy()
const { t, language } = useI18n()

const menuButton = ref<HTMLButtonElement | null>(null)

function label(key: string): string {
  void language.value
  return t(key)
}

const initial = computed(() => {
  const name = props.userName?.trim() ?? ''
  return name ? name.slice(0, 1).toUpperCase() : '·'
})

const isLoading = computed(() => props.status === 'loading')
const isAnonymous = computed(() => props.status === 'anonymous')

/** 抽屉关闭后把焦点还给触发按钮（AppShell 调用）。 */
function focusMenuButton(): void {
  menuButton.value?.focus()
}

defineExpose({ focusMenuButton })
</script>

<template>
  <header class="ls-topbar">
    <div class="ls-topbar__start">
      <button
        ref="menuButton"
        class="ls-icon-btn ls-topbar__trigger"
        type="button"
        :aria-label="copy.openNav"
        :aria-expanded="mobileOpen"
        @click="emit('toggle-nav')"
      >
        <AppIcon name="menu" size="1.125rem" />
      </button>

      <nav class="ls-topbar__crumbs" :aria-label="copy.breadcrumbNav">
        <ol class="ls-crumbs">
          <li v-for="(crumb, index) in crumbs" :key="`${crumb.labelKey}-${index}`" class="ls-crumbs__item">
            <RouterLink v-if="crumb.to" class="ls-crumbs__link" :to="crumb.to">
              {{ label(crumb.labelKey) }}
            </RouterLink>
            <span v-else class="ls-crumbs__current" aria-current="page">
              {{ label(crumb.labelKey) }}
            </span>
          </li>
        </ol>
      </nav>
    </div>

    <div class="ls-topbar__end">
      <template v-if="isLoading">
        <span class="ls-chip" aria-live="polite">{{ copy.loading }}</span>
      </template>

      <template v-else-if="isAnonymous">
        <span class="ls-chip ls-chip--muted">{{ copy.anonymousTitle }}</span>
        <button
          v-if="sessionError"
          class="ls-icon-btn"
          type="button"
          :aria-label="copy.retry"
          :title="sessionError"
          @click="emit('retry')"
        >
          <AppIcon name="refresh" size="1rem" />
        </button>
      </template>

      <template v-else>
        <div class="ls-user">
          <span class="ls-user__avatar" aria-hidden="true">{{ initial }}</span>
          <span class="ls-user__meta">
            <span class="ls-user__name" :title="userName ?? undefined">{{ userName }}</span>
            <span class="ls-user__role">{{ roleName ?? copy.roleFallback }}</span>
          </span>
        </div>
        <button class="ls-icon-btn" type="button" :aria-label="copy.logout" :title="copy.logout" @click="emit('logout')">
          <AppIcon name="logout" size="1rem" />
        </button>
      </template>
    </div>
  </header>
</template>
