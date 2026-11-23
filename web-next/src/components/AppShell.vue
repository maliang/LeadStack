<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from './AppIcon.vue'
import AppSidebar from './AppSidebar.vue'
import AppTopbar from './AppTopbar.vue'
import { breadcrumbsFor, visibleNavGroups, NAV_GROUPS } from '@/lib/nav'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useShellCopy } from '@/composables/useShellCopy'

/**
 * 外壳：唯一持有「导航分组 / 抽屉开关」的地方。
 *
 * 侧边栏与顶栏都是纯渲染组件，权限过滤与状态映射在这里（或 lib/ 纯函数里）完成。
 * 移动端抽屉按模态处理：Teleport 到 body、Esc 关闭、打开时锁滚动、焦点回归触发按钮。
 */
const route = useRoute()
const router = useRouter()
const copy = useShellCopy()
const { can } = useCan()
const { status, user, error, ensureSession, refreshSession, logout } = useAuth()

const groups = computed(() => visibleNavGroups(NAV_GROUPS, can))
const crumbs = computed(() => breadcrumbsFor(route.path))

const mobileOpen = ref(false)
const topbar = ref<InstanceType<typeof AppTopbar> | null>(null)
const drawerPanel = ref<HTMLElement | null>(null)
const mainEl = ref<HTMLElement | null>(null)

function focusMain(): void {
  mainEl.value?.focus()
}

function openDrawer(): void {
  mobileOpen.value = true
}

function closeDrawer(restoreFocus = true): void {
  if (!mobileOpen.value) return
  mobileOpen.value = false
  if (restoreFocus) topbar.value?.focusMenuButton()
}

function drawerFocusables(): HTMLElement[] {
  const root = drawerPanel.value
  if (!root) return []
  return Array.from(
    root.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])'),
  ).filter((element) => element.offsetParent !== null || element === root)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    closeDrawer()
    return
  }
  if (event.key !== 'Tab') return
  // 焦点陷阱：抽屉是模态，Tab 只在其内部循环
  const focusables = drawerFocusables()
  if (focusables.length === 0) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  const active = document.activeElement as HTMLElement | null
  if (event.shiftKey && (active === first || !drawerPanel.value?.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !drawerPanel.value?.contains(active))) {
    event.preventDefault()
    first.focus()
  }
}

watch(mobileOpen, async (open) => {
  if (typeof document === 'undefined') return
  if (open) {
    document.addEventListener('keydown', onKeydown)
    document.body.classList.add('ls-scroll-locked')
    await nextTick()
    drawerFocusables()[0]?.focus()
    return
  }
  document.removeEventListener('keydown', onKeydown)
  document.body.classList.remove('ls-scroll-locked')
})

function onProfileUpdated(): void {
  void refreshSession()
}

onMounted(() => {
  void ensureSession()
  window.addEventListener('leadstack:profile-updated', onProfileUpdated)
})

/**
 * 会话失效（401 被 onUnauthorized 清态）后立刻回登录页。
 * 只靠守卫不够：清态发生时没有导航，页面会停在一个空壳上。
 *
 * 不能按「当前路由是否 public」早退：/forbidden 是 public 且挂外壳的页面，
 * 在这里早退会让 401 / 登出后的用户永远停在无权限页（登出按钮已随匿名态消失）。
 * 唯一的例外是已经在登录页本身，否则会产生一次自我跳转。
 * 主动登出另有 handleLogout 里的显式导航，不依赖本 watcher。
 */
watch(status, (value) => {
  if (value !== 'anonymous') return
  if (route.name === 'login') return
  const redirect = route.meta.public === true || route.fullPath === '/' ? {} : { redirect: route.fullPath }
  void router.replace({ name: 'login', query: redirect })
})

onBeforeUnmount(() => {
  window.removeEventListener('leadstack:profile-updated', onProfileUpdated)
  if (typeof document === 'undefined') return
  document.removeEventListener('keydown', onKeydown)
  document.body.classList.remove('ls-scroll-locked')
})

function handleRetry(): void {
  void refreshSession()
}

/**
 * 主动登出：logout() 保证「请求失败也清本地态」，这里无条件回登录页。
 * 不能只靠 status watcher —— 用户可能停在 public 路由（如 /forbidden），
 * 那一路 watcher 的语义是「会话失效兜底」，不该成为登出的唯一出口。
 */
async function handleLogout(): Promise<void> {
  closeDrawer(false)
  try {
    await logout()
  } finally {
    await router.replace({ name: 'login' })
  }
}
</script>

<template>
  <div class="ls-shell">
    <a class="ls-skip-link" href="#ls-main" @click.prevent="focusMain">{{ copy.skipToContent }}</a>

    <aside id="ls-app-sidebar" class="ls-shell__sidebar">
      <AppSidebar
        :groups="groups"
        :status="status"
        :active-path="route.path"
        :session-error="error"
        variant="desktop"
        @retry="handleRetry"
      />
    </aside>

    <div class="ls-shell__body">
      <AppTopbar
        ref="topbar"
        :crumbs="crumbs"
        :status="status"
        :user="user"
        :session-error="error"
        :mobile-open="mobileOpen"
        @toggle-nav="openDrawer"
        @logout="handleLogout"
        @retry="handleRetry"
      />

      <main id="ls-main" ref="mainEl" class="ls-main" tabindex="-1">
        <div v-if="error" class="ls-banner" role="alert">
          <AppIcon name="alert-triangle" size="1rem" />
          <div class="ls-banner__body">
            <p class="ls-banner__title">{{ copy.sessionErrorTitle }}</p>
            <p class="ls-banner__text">{{ error }}</p>
          </div>
          <button class="ls-notice__action" type="button" @click="handleRetry">
            <AppIcon name="refresh" size="0.875rem" />
            {{ copy.retry }}
          </button>
        </div>

        <div class="ls-main__inner">
          <slot />
        </div>
      </main>
    </div>

    <Teleport to="body">
      <div v-if="mobileOpen" class="ls-drawer">
        <button
          class="ls-drawer__backdrop"
          type="button"
          :aria-label="copy.closeNav"
          @click="closeDrawer()"
        />
        <div
          id="ls-mobile-nav"
          ref="drawerPanel"
          class="ls-drawer__panel"
          role="dialog"
          aria-modal="true"
          :aria-label="copy.mainNav"
        >
          <div class="ls-drawer__top">
            <button
              class="ls-icon-btn"
              type="button"
              :aria-label="copy.closeNav"
              :title="copy.closeNav"
              @click="closeDrawer()"
            >
              <AppIcon name="close" size="1.125rem" />
            </button>
          </div>
          <AppSidebar
            class="ls-drawer__nav"
            :groups="groups"
            :status="status"
            :active-path="route.path"
            :session-error="error"
            variant="drawer"
            @navigate="closeDrawer(false)"
            @retry="handleRetry"
          />
        </div>
      </div>
    </Teleport>
  </div>
</template>
