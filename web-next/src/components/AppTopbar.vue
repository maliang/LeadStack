<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from './AppIcon.vue'
import AppModal from './AppModal.vue'
import type { Crumb } from '@/lib/nav'
import { useShellCopy } from '@/composables/useShellCopy'
import { useI18n } from '@/i18n'
import type { AuthStatus } from '@/composables/useAuth'
import { api, type User } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { useUpgradeCenter } from '@/composables/useUpgradeCenter'

/**
 * 顶栏：抽屉开关、面包屑、账号下拉（资料 / 改密 / 退出）。
 * 不持会话状态：用户资料来自 props.user，登出交给 AppShell/useAuth。
 */
const props = defineProps<{
  crumbs: Crumb[]
  status: AuthStatus
  user: User | null
  sessionError: string | null
  mobileOpen: boolean
}>()

const emit = defineEmits<{
  (event: 'toggle-nav'): void
  (event: 'logout'): void
  (event: 'retry'): void
}>()

const copy = useShellCopy()
const route = useRoute()
const { t, language } = useI18n()
const upgrade = useUpgradeCenter()
const upgradeOpen = ref(false)

const menuButton = ref<HTMLButtonElement | null>(null)
const accountButton = ref<HTMLButtonElement | null>(null)
const menuOpen = ref(false)

function label(key: string): string {
  void language.value
  return t(key)
}

const displayName = computed(() => props.user?.display_name?.trim() || props.user?.username || '')
const roleLabel = computed(() => {
  const current = props.user
  if (!current) return copy.value.roleFallback
  const key = `role.${current.role}`
  const translated = label(key)
  if (translated !== key) return translated
  return current.role_name || current.role || copy.value.roleFallback
})
const initial = computed(() => (displayName.value ? displayName.value.slice(0, 1).toUpperCase() : '·'))

const isLoading = computed(() => props.status === 'loading')
const isAnonymous = computed(() => props.status === 'anonymous')

/* ---------- account menu ---------- */

function toggleMenu(): void {
  menuOpen.value = !menuOpen.value
  if (menuOpen.value) document.addEventListener('click', onDocumentClick, true)
  else document.removeEventListener('click', onDocumentClick, true)
}

function closeMenu(restoreFocus = false): void {
  if (!menuOpen.value) return
  menuOpen.value = false
  document.removeEventListener('click', onDocumentClick, true)
  if (restoreFocus) accountButton.value?.focus()
}

function onDocumentClick(event: MouseEvent): void {
  const target = event.target as Node | null
  const root = accountButton.value?.closest('.ls-topbar-account')
  if (target && root?.contains(target)) return
  closeMenu()
}

function onMenuKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    closeMenu(true)
  }
}

/* ---------- profile ---------- */

const profileOpen = ref(false)
const profileBusy = ref(false)
const profileError = ref<string | null>(null)
const profileNotice = ref<string | null>(null)
const profileForm = ref({ display_name: '', phone: '', notes: '' })

function openProfile(): void {
  closeMenu()
  profileForm.value = {
    display_name: props.user?.display_name ?? '',
    phone: props.user?.phone ?? '',
    notes: props.user?.notes ?? '',
  }
  profileError.value = null
  profileNotice.value = null
  profileOpen.value = true
}

async function saveProfile(): Promise<void> {
  if (profileBusy.value) return
  profileBusy.value = true
  profileError.value = null
  profileNotice.value = null
  try {
    await api.updateMyProfile({
      display_name: profileForm.value.display_name.trim(),
      phone: profileForm.value.phone.trim(),
      notes: profileForm.value.notes.trim(),
    })
    profileNotice.value = t('user.profileSaved')
    // 通知 AppShell/useAuth 重新拉取 /auth/me，顶栏与页面立即用新资料。
    window.dispatchEvent(new CustomEvent('leadstack:profile-updated'))
  } catch (caught) {
    profileError.value = errorMessage(caught, t('user.profileSaveFailed'))
  } finally {
    profileBusy.value = false
  }
}

/* ---------- change password ---------- */

const passwordOpen = ref(false)
const passwordBusy = ref(false)
const passwordError = ref<string | null>(null)
const passwordNotice = ref<string | null>(null)
const passwordForm = ref({ old: '', next: '', confirm: '' })

function openPassword(): void {
  closeMenu()
  passwordForm.value = { old: '', next: '', confirm: '' }
  passwordError.value = null
  passwordNotice.value = null
  passwordOpen.value = true
}

async function savePassword(): Promise<void> {
  if (passwordBusy.value) return
  passwordError.value = null
  passwordNotice.value = null
  if (passwordForm.value.next.length < 6) {
    passwordError.value = t('users.errorPassword')
    return
  }
  if (passwordForm.value.next !== passwordForm.value.confirm) {
    passwordError.value = t('settings.passwordMismatch')
    return
  }
  passwordBusy.value = true
  try {
    await api.changeMyPassword({
      old_password: passwordForm.value.old,
      new_password: passwordForm.value.next,
    })
    passwordForm.value = { old: '', next: '', confirm: '' }
    passwordNotice.value = t('users.ownPasswordChanged')
  } catch (caught) {
    passwordError.value = errorMessage(caught, t('common.error'))
  } finally {
    passwordBusy.value = false
  }
}

async function confirmUpgrade(): Promise<void> {
  try {
    await upgrade.apply()
    upgradeOpen.value = false
  } catch {
    // 错误已写入 upgrade.error，弹窗保留给用户重试/取消。
  }
}

watch(
  () => route.fullPath,
  () => {
    closeMenu(false)
    profileOpen.value = false
    passwordOpen.value = false
    upgradeOpen.value = false
  },
)

function handleLogout(): void {
  closeMenu()
  emit('logout')
}

/** 抽屉关闭后把焦点还给触发按钮（AppShell 调用）。 */
function focusMenuButton(): void {
  menuButton.value?.focus()
}

defineExpose({ focusMenuButton })

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocumentClick, true)
})
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
        <button
          v-if="upgrade.allowed.value && upgrade.active.value"
          id="topbar-upgrade-progress"
          class="ls-upgrade-pill ls-upgrade-pill--busy"
          type="button"
          :title="`${upgrade.phaseLabel.value} ${upgrade.progress.value}%`"
          aria-live="polite"
        >
          <span class="ls-upgrade-pill__bar" aria-hidden="true">
            <span class="ls-upgrade-pill__fill" :style="{ width: `${upgrade.progress.value}%` }" />
          </span>
          <span class="ls-upgrade-pill__text">{{ upgrade.phaseLabel.value }} {{ upgrade.progress.value }}%</span>
        </button>
        <button
          v-else-if="upgrade.allowed.value && upgrade.hasUpdate.value"
          id="topbar-upgrade"
          class="ls-upgrade-pill"
          type="button"
          @click="upgradeOpen = true"
        >
          <AppIcon name="upgrade" size="0.875rem" />
          <span class="ls-upgrade-pill__text">{{ t('upgrade.available', { version: upgrade.latest.value ?? '' }) }}</span>
        </button>
        <div class="ls-topbar-account" @keydown="onMenuKeydown">
          <button
            ref="accountButton"
            id="topbar-account"
            class="ls-topbar-account__trigger"
            type="button"
            aria-haspopup="menu"
            :aria-expanded="menuOpen"
            :aria-label="t('user.menuLabel')"
            @click.stop="toggleMenu"
          >
            <span class="ls-user__avatar" aria-hidden="true">{{ initial }}</span>
            <span class="ls-user__meta">
              <span class="ls-user__name" :title="displayName">{{ displayName }}</span>
              <span class="ls-user__role">{{ roleLabel }}</span>
            </span>
            <AppIcon name="chevron-right" size="0.875rem" class="ls-topbar-account__caret" />
          </button>
          <div v-if="menuOpen" class="ls-topbar-account__menu" role="menu" aria-labelledby="topbar-account">
            <button class="ls-topbar-account__item" type="button" role="menuitem" @click="openProfile">
              <AppIcon name="users" size="0.875rem" />
              {{ t('user.profile') }}
            </button>
            <button class="ls-topbar-account__item" type="button" role="menuitem" @click="openPassword">
              <AppIcon name="key-round" size="0.875rem" />
              {{ t('user.changePassword') }}
            </button>
            <div class="ls-topbar-account__sep" role="separator" />
            <button class="ls-topbar-account__item ls-topbar-account__item--danger" type="button" role="menuitem" @click="handleLogout">
              <AppIcon name="logout" size="0.875rem" />
              {{ copy.logout }}
            </button>
          </div>
        </div>
      </template>
    </div>

    <AppModal
      v-if="profileOpen"
      :title="t('user.profileTitle')"
      :description="t('user.profileHint')"
      size="md"
      @close="profileOpen = false"
    >
      <div class="ls-adminpage__form">
        <div class="ls-form__row">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('users.username') }}</span>
            <input class="ls-input" type="text" :value="user?.username ?? ''" readonly />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('users.role') }}</span>
            <input class="ls-input" type="text" :value="roleLabel" readonly />
          </label>
        </div>
        <label class="ls-field" for="profile-display-name">
          <span class="ls-field__label">{{ t('user.displayName') }}</span>
          <input id="profile-display-name" v-model="profileForm.display_name" class="ls-input" type="text" autocomplete="name" />
        </label>
        <label class="ls-field" for="profile-phone">
          <span class="ls-field__label">{{ t('user.phone') }}</span>
          <input id="profile-phone" v-model="profileForm.phone" class="ls-input" type="tel" autocomplete="tel" />
        </label>
        <label class="ls-field" for="profile-notes">
          <span class="ls-field__label">{{ t('user.notes') }}</span>
          <textarea id="profile-notes" v-model="profileForm.notes" class="ls-input ls-textarea" rows="3" />
        </label>
        <p v-if="profileError" class="ls-inline-error" role="alert">{{ profileError }}</p>
        <p v-if="profileNotice" class="ls-ok-hint" role="status">{{ profileNotice }}</p>
      </div>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="profileBusy" @click="profileOpen = false">{{ t('common.close') }}</button>
        <button class="ls-btn ls-btn--primary" type="button" :disabled="profileBusy" :aria-busy="profileBusy" @click="saveProfile">
          {{ profileBusy ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="upgradeOpen"
      :title="t('upgrade.confirmTitle', { version: upgrade.latest.value ?? '' })"
      :description="t('upgrade.confirmText', { from: upgrade.info.value?.current ?? t('common.na'), to: upgrade.latest.value ?? '' })"
      size="sm"
      @close="upgradeOpen = false"
    >
      <p v-if="upgrade.error.value" class="ls-inline-error" role="alert">{{ upgrade.error.value }}</p>
      <template #footer>
        <button id="topbar-upgrade-cancel" class="ls-btn" type="button" :disabled="upgrade.applying.value" @click="upgradeOpen = false">
          {{ t('upgrade.cancel') }}
        </button>
        <button
          id="topbar-upgrade-confirm"
          class="ls-btn ls-btn--primary"
          type="button"
          :disabled="upgrade.applying.value"
          :aria-busy="upgrade.applying.value"
          @click="confirmUpgrade"
        >
          {{ upgrade.applying.value ? t('upgrade.submitting') : t('upgrade.confirmApply') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="passwordOpen"
      :title="t('users.changePassword')"
      :description="t('user.passwordHint')"
      size="sm"
      @close="passwordOpen = false"
    >
      <div class="ls-adminpage__form">
        <label class="ls-field" for="profile-old-password">
          <span class="ls-field__label">{{ t('users.oldPassword') }}</span>
          <input id="profile-old-password" v-model="passwordForm.old" class="ls-input" type="password" autocomplete="current-password" />
        </label>
        <label class="ls-field" for="profile-new-password">
          <span class="ls-field__label">{{ t('users.newPassword') }}</span>
          <input id="profile-new-password" v-model="passwordForm.next" class="ls-input" type="password" autocomplete="new-password" />
        </label>
        <label class="ls-field" for="profile-confirm-password">
          <span class="ls-field__label">{{ t('settings.confirmPassword') }}</span>
          <input id="profile-confirm-password" v-model="passwordForm.confirm" class="ls-input" type="password" autocomplete="new-password" />
        </label>
        <p v-if="passwordError" id="profile-password-error" class="ls-inline-error" role="alert">{{ passwordError }}</p>
        <p v-if="passwordNotice" id="profile-password-notice" class="ls-ok-hint" role="status">{{ passwordNotice }}</p>
      </div>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="passwordBusy" @click="passwordOpen = false">{{ t('common.close') }}</button>
        <button class="ls-btn ls-btn--primary" type="button" :disabled="passwordBusy" :aria-busy="passwordBusy" @click="savePassword">
          {{ passwordBusy ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </AppModal>
  </header>
</template>
