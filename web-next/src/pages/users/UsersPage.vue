<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type ManagedUser, type Role } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import {
  USERS_PAGE_SIZE,
  clampPage,
  filterUsers,
  pageCount,
  paginate,
  roleNameMap,
  sortRoles,
  userStatus,
  userStatusLabelKey,
} from '@/lib/user-admin'

/**
 * 用户管理页（挂载面：AppShell 默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是本页：users + roles 一次 Promise.all 拉齐，失败进 error 三态；
 * UI 状态只有搜索词 / 页码 / 三个弹窗的目标。
 * 权限：无 user.manage（admin-only）时整页只渲染提示，**不发起任何请求**，
 * 也不渲染表格与操作列；自己的账号不允许停用 / 重置 / 删除（后端同样拒绝）。
 */
const { t } = useI18n()
const { can } = useCan()
const { user: currentUser } = useAuth()

const canManage = computed(() => can('user.manage'))

const users = ref<ManagedUser[]>([])
const roles = ref<Role[]>([])
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const error = ref<string | null>(null)
const loaded = ref(false)
const refreshing = ref(false)

const keyword = ref('')
const page = ref(1)

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)
const busyId = ref<string | null>(null)

const roleNames = computed(() => roleNameMap(roles.value))
const filteredUsers = computed(() => filterUsers(users.value, keyword.value))
const pages = computed(() => pageCount(filteredUsers.value.length, USERS_PAGE_SIZE))
const pageUsers = computed(() => paginate(filteredUsers.value, page.value, USERS_PAGE_SIZE))
const roleOptions = computed(() => sortRoles(roles.value))

watch(keyword, () => {
  page.value = 1
})

watch(filteredUsers, () => {
  page.value = clampPage(page.value, filteredUsers.value.length, USERS_PAGE_SIZE)
})

watch(canManage, (allowed) => {
  if (allowed && !loaded.value && state.value !== 'loading') void load()
})

async function load(silent = false): Promise<void> {
  if (!canManage.value) return
  if (!silent) {
    if (!loaded.value) state.value = 'loading'
    else refreshing.value = true
  }
  error.value = null
  try {
    const [userList, roleList] = await Promise.all([api.users(), api.roles()])
    users.value = userList
    roles.value = roleList
    loaded.value = true
    state.value = 'ready'
  } catch (caught) {
    error.value = errorMessage(caught, t('users.loadFailed'))
    if (!loaded.value) state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

function roleLabel(target: ManagedUser): string {
  if (target.role_name) return target.role_name
  return roleNames.value[target.role] ?? target.role
}

function isSelf(target: ManagedUser): boolean {
  return currentUser.value?.id === target.id
}

function clearFeedback(): void {
  actionError.value = null
  actionNotice.value = null
}

async function toggleUser(target: ManagedUser, disabled: boolean): Promise<void> {
  if (isSelf(target) || busyId.value !== null) return
  clearFeedback()
  busyId.value = target.id
  try {
    await api.updateUser(target.id, { disabled })
    actionNotice.value = t(disabled ? 'users.disabledToast' : 'users.enabledToast', {
      name: target.username,
    })
    await load(true)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('users.actionFailed'))
  } finally {
    busyId.value = null
  }
}

async function unlockUser(target: ManagedUser): Promise<void> {
  if (isSelf(target) || busyId.value !== null) return
  clearFeedback()
  busyId.value = target.id
  try {
    await api.unlockUser(target.id)
    actionNotice.value = t('users.unlocked', { name: target.username })
    await load(true)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('users.actionFailed'))
  } finally {
    busyId.value = null
  }
}

/* ---------- 新建用户 ---------- */

const createOpen = ref(false)
const createBusy = ref(false)
const createError = ref<string | null>(null)
const createForm = ref({ username: '', password: '', role: '' })

function openCreate(): void {
  const fallback = roleOptions.value.find((role) => role.key === 'viewer') ?? roleOptions.value[0]
  createForm.value = { username: '', password: '', role: fallback?.key ?? 'viewer' }
  createError.value = null
  createOpen.value = true
}

function closeCreate(): void {
  if (createBusy.value) return
  createOpen.value = false
  createForm.value = { username: '', password: '', role: '' }
  createError.value = null
}

async function submitCreate(): Promise<void> {
  const username = createForm.value.username.trim()
  if (username === '') {
    createError.value = t('users.errorUsername')
    return
  }
  if (createForm.value.password.length < 6) {
    createError.value = t('users.errorPassword')
    return
  }
  createBusy.value = true
  createError.value = null
  try {
    const created = await api.createUser({
      username,
      password: createForm.value.password,
      role: createForm.value.role,
    })
    actionNotice.value = t('users.created', { name: created.username })
    createOpen.value = false
    createForm.value = { username: '', password: '', role: '' }
    await load(true)
  } catch (caught) {
    createError.value = errorMessage(caught, t('users.actionFailed'))
  } finally {
    createBusy.value = false
  }
}

/* ---------- 重置密码（二次确认弹窗） ---------- */

const resetTarget = ref<ManagedUser | null>(null)
const resetValue = ref('')
const resetBusy = ref(false)
const resetError = ref<string | null>(null)

function openReset(target: ManagedUser): void {
  if (isSelf(target)) return
  resetTarget.value = target
  resetValue.value = ''
  resetError.value = null
}

function closeReset(): void {
  if (resetBusy.value) return
  resetTarget.value = null
  resetValue.value = ''
  resetError.value = null
}

async function submitReset(): Promise<void> {
  const target = resetTarget.value
  if (!target) return
  if (resetValue.value.length < 6) {
    resetError.value = t('users.errorPassword')
    return
  }
  resetBusy.value = true
  resetError.value = null
  try {
    await api.updateUser(target.id, { password: resetValue.value })
    actionNotice.value = t('users.passwordReset', { name: target.username })
    resetValue.value = ''
    resetTarget.value = null
    await load(true)
  } catch (caught) {
    resetError.value = errorMessage(caught, t('users.actionFailed'))
  } finally {
    resetValue.value = ''
    resetBusy.value = false
  }
}

/* ---------- 删除用户（二次确认弹窗） ---------- */

const deleteTarget = ref<ManagedUser | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function openDelete(target: ManagedUser): void {
  if (isSelf(target)) return
  deleteTarget.value = target
  deleteError.value = null
}

function closeDelete(): void {
  if (deleteBusy.value) return
  deleteTarget.value = null
  deleteError.value = null
}

async function submitDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    await api.deleteUser(target.id)
    actionNotice.value = t('users.deleted', { name: target.username })
    deleteTarget.value = null
    await load(true)
  } catch (caught) {
    deleteError.value = errorMessage(caught, t('users.actionFailed'))
  } finally {
    deleteBusy.value = false
  }
}

onMounted(() => {
  if (canManage.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-userspage">
    <div v-if="!canManage" id="users-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('users.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('users.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('users.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('users.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="users-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load()"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button id="users-create-open" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
            <AppIcon name="plus" size="0.875rem" />
            {{ t('users.create') }}
          </button>
        </div>
      </header>

      <div class="ls-adminpage__toolbar">
        <input
          id="users-search-input"
          class="ls-input ls-adminpage__search"
          type="search"
          autocomplete="off"
          :placeholder="t('users.searchPlaceholder')"
          :aria-label="t('common.search')"
          :value="keyword"
          @input="keyword = ($event.target as HTMLInputElement).value"
        />
        <span id="users-count" class="ls-adminpage__count" aria-live="polite">
          {{ t('users.count', { filtered: filteredUsers.length, total: users.length }) }}
        </span>
      </div>

      <div v-if="error && loaded" id="users-banner-error" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-if="actionError" id="users-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="users-action-notice" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading' && !loaded"
        id="users-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 6" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && users.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="users-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="users.length === 0" id="users-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('users.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('users.emptyDescription') }}</p>
      </div>

      <div v-else-if="filteredUsers.length === 0" id="users-empty-filtered" class="ls-notice">
        <p class="ls-notice__title">{{ t('users.noMatches') }}</p>
        <p class="ls-notice__text">{{ t('users.noMatchesHint') }}</p>
        <button class="ls-notice__action" type="button" @click="keyword = ''">
          {{ t('users.resetFilters') }}
        </button>
      </div>

      <template v-else>
        <div class="ls-adminpage__scroll">
          <table class="ls-adminpage__table">
            <caption class="ls-visually-hidden">{{ t('users.title') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('users.username') }}</th>
                <th scope="col">{{ t('users.role') }}</th>
                <th scope="col">{{ t('common.status') }}</th>
                <th scope="col">{{ t('users.createdAt') }}</th>
                <th scope="col">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="target in pageUsers"
                :key="target.id"
                class="ls-adminpage__row ls-userspage__row"
                :data-user-id="target.id"
                :data-username="target.username"
              >
                <td data-label="">
                  <div class="ls-adminpage__primary">
                    <span class="ls-userspage__username">{{ target.username }}</span>
                    <span v-if="isSelf(target)" class="ls-chip ls-chip--muted">{{ t('users.you') }}</span>
                  </div>
                  <div class="ls-adminpage__meta">{{ target.id }}</div>
                </td>
                <td :data-label="t('users.role')">{{ roleLabel(target) }}</td>
                <td :data-label="t('common.status')">
                  <span
                    class="ls-chip ls-adminpage__status"
                    :class="`ls-adminpage__status--${userStatus(target)}`"
                  >
                    {{ t(userStatusLabelKey(userStatus(target))) }}
                  </span>
                  <div v-if="target.locked" class="ls-adminpage__meta">
                    {{ t('users.failedAttempts', { count: target.failed_attempts }) }}
                  </div>
                </td>
                <td :data-label="t('users.createdAt')" class="ls-adminpage__date">
                  {{ formatDateTime(target.created_at) }}
                </td>
                <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                  <span v-if="isSelf(target)" class="ls-adminpage__hint">{{ t('users.selfHint') }}</span>
                  <template v-else>
                    <button
                      v-if="target.locked"
                      class="ls-btn ls-btn--small ls-userspage__unlock"
                      type="button"
                      :disabled="busyId !== null"
                      @click="unlockUser(target)"
                    >
                      {{ t('users.unlock') }}
                    </button>
                    <button
                      class="ls-btn ls-btn--small ls-userspage__reset"
                      type="button"
                      :disabled="busyId !== null"
                      @click="openReset(target)"
                    >
                      {{ t('users.resetPassword') }}
                    </button>
                    <button
                      class="ls-btn ls-btn--small ls-userspage__toggle"
                      type="button"
                      :disabled="busyId !== null"
                      @click="toggleUser(target, !target.disabled)"
                    >
                      {{ target.disabled ? t('users.enable') : t('users.disable') }}
                    </button>
                    <button
                      class="ls-btn ls-btn--small ls-userspage__delete"
                      type="button"
                      :disabled="busyId !== null"
                      @click="openDelete(target)"
                    >
                      {{ t('common.delete') }}
                    </button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <nav v-if="pages > 1" class="ls-adminpage__pager" :aria-label="t('users.title')">
          <button
            id="users-prev-page"
            class="ls-btn ls-btn--small ls-adminpage__prev"
            type="button"
            :disabled="page <= 1"
            :aria-label="t('users.pagerPrev')"
            @click="page = clampPage(page - 1, filteredUsers.length, USERS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
          <span id="users-pager-info" class="ls-adminpage__pager-info">
            {{ t('users.pagerInfo', { page, pages }) }}
          </span>
          <button
            id="users-next-page"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="page >= pages"
            :aria-label="t('users.pagerNext')"
            @click="page = clampPage(page + 1, filteredUsers.length, USERS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
        </nav>
      </template>
    </template>

    <AppModal
      v-if="createOpen"
      :title="t('users.create')"
      :description="t('users.createHint')"
      @close="closeCreate"
    >
      <form id="users-create-form" class="ls-adminpage__form" @submit.prevent="submitCreate">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('users.username') }}</span>
          <input
            id="users-create-username"
            v-model="createForm.username"
            class="ls-input"
            type="text"
            autocomplete="off"
            data-autofocus
            :disabled="createBusy"
          />
        </label>
        <label class="ls-field">
          <span class="ls-field__label">{{ t('users.password') }}</span>
          <input
            id="users-create-password"
            v-model="createForm.password"
            class="ls-input"
            type="password"
            autocomplete="new-password"
            :disabled="createBusy"
          />
        </label>
        <label class="ls-field">
          <span class="ls-field__label">{{ t('users.role') }}</span>
          <select id="users-create-role" v-model="createForm.role" class="ls-input" :disabled="createBusy">
            <option v-for="role in roleOptions" :key="role.key" :value="role.key">
              {{ role.name }}
            </option>
          </select>
        </label>
        <p v-if="createError" id="users-create-error" class="ls-adminpage__form-error" role="alert">
          {{ createError }}
        </p>
      </form>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="createBusy" @click="closeCreate">
          {{ t('common.cancel') }}
        </button>
        <button
          id="users-create-submit"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="users-create-form"
          :disabled="createBusy"
        >
          {{ t('common.create') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="resetTarget"
      :title="t('users.resetPassword')"
      :description="t('users.resetHint', { name: resetTarget.username })"
      size="sm"
      @close="closeReset"
    >
      <form id="users-reset-form" class="ls-adminpage__form" @submit.prevent="submitReset">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('users.newPassword') }}</span>
          <input
            id="users-reset-password"
            v-model="resetValue"
            class="ls-input"
            type="password"
            autocomplete="new-password"
            data-autofocus
            :disabled="resetBusy"
          />
        </label>
        <p v-if="resetError" id="users-reset-error" class="ls-adminpage__form-error" role="alert">
          {{ resetError }}
        </p>
      </form>
      <template #footer>
        <button
          id="users-reset-cancel"
          class="ls-btn"
          type="button"
          :disabled="resetBusy"
          @click="closeReset"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          id="users-reset-confirm"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="users-reset-form"
          :disabled="resetBusy"
        >
          {{ t('users.resetConfirm') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="t('users.deleteTitle')"
      :description="t('users.deleteHint', { name: deleteTarget.username })"
      size="sm"
      @close="closeDelete"
    >
      <p class="ls-adminpage__form-hint">{{ t('users.deleteImpact') }}</p>
      <p v-if="deleteError" id="users-delete-error" class="ls-adminpage__form-error" role="alert">
        {{ deleteError }}
      </p>
      <template #footer>
        <button
          id="users-delete-cancel"
          class="ls-btn"
          type="button"
          :disabled="deleteBusy"
          @click="closeDelete"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          id="users-delete-confirm"
          class="ls-btn ls-adminpage__danger"
          type="button"
          :disabled="deleteBusy"
          @click="submitDelete"
        >
          {{ t('users.deleteConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
