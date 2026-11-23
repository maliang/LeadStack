<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  api,
  type Permission,
  type Role,
  type RolePayload,
  type ScopeMode,
  type Server,
  type ServerGroup,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import {
  SCOPE_MODES,
  assignablePermissions,
  groupPermissionsByDomain,
  pickerGroups,
  pickerServers,
  scopeSummary,
  selectedGrantable,
  showsGroups,
  showsServers,
  sortRoles,
  toggleSelection,
  validateRoleForm,
} from '@/lib/user-admin'

/**
 * 角色与权限页（挂载面：AppShell 默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是本页：roles / permissions / server-groups / servers 一次拉齐。
 * 内置 admin/operator/viewer 只读：行内不给编辑与删除按钮，只显示「内置不可改删」。
 * 6 个 admin-only 权限点在自定义角色表单里置灰并写明原因（后端收到会 400）。
 * 权限：无 role.manage（admin-only）时整页只渲染提示，**不发起任何请求**。
 */
const { t } = useI18n()
const { can } = useCan()

const canManage = computed(() => can('role.manage'))

const roles = ref<Role[]>([])
const permissions = ref<Permission[]>([])
const groups = ref<ServerGroup[]>([])
const servers = ref<Server[]>([])

const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const error = ref<string | null>(null)
const loaded = ref(false)
const refreshing = ref(false)

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)

const sortedRoles = computed(() => sortRoles(roles.value))
const permissionGroups = computed(() => groupPermissionsByDomain(permissions.value))
const grantablePermissions = computed(() => assignablePermissions(permissions.value))

async function load(silent = false): Promise<void> {
  if (!canManage.value) return
  if (!silent) {
    if (!loaded.value) state.value = 'loading'
    else refreshing.value = true
  }
  error.value = null
  try {
    const [roleList, permissionList, groupList, serverList] = await Promise.all([
      api.roles(),
      api.permissions(),
      api.serverGroups(),
      api.servers(),
    ])
    roles.value = roleList
    permissions.value = permissionList
    groups.value = groupList
    servers.value = serverList
    loaded.value = true
    state.value = 'ready'
  } catch (caught) {
    error.value = errorMessage(caught, t('roles.loadFailed'))
    if (!loaded.value) state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

/* ---------- 新建 / 编辑角色 ---------- */

const dialogOpen = ref(false)
const editingKey = ref<string | null>(null)
const saving = ref(false)
const formError = ref<string | null>(null)
const groupQuery = ref('')
const serverQuery = ref('')

const form = ref({
  name: '',
  description: '',
  permissions: [] as string[],
  mode: 'all' as ScopeMode,
  groupIds: [] as string[],
  serverIds: [] as string[],
})

const visibleGroups = computed(() => pickerGroups(groups.value, groupQuery.value))
const visibleServers = computed(() => pickerServers(servers.value, serverQuery.value))
const selectedCount = computed(() =>
  selectedGrantable(permissions.value, form.value.permissions).length,
)
const editingRole = computed(
  () => roles.value.find((role) => role.key === editingKey.value) ?? null,
)

function resetForm(): void {
  form.value = {
    name: '',
    description: '',
    permissions: [],
    mode: 'all',
    groupIds: [],
    serverIds: [],
  }
  formError.value = null
  groupQuery.value = ''
  serverQuery.value = ''
}

function openCreate(): void {
  editingKey.value = null
  resetForm()
  dialogOpen.value = true
}

function openEdit(role: Role): void {
  if (role.builtin) return
  editingKey.value = role.key
  form.value = {
    name: role.name,
    description: role.description,
    permissions: role.permissions.filter((key) =>
      grantablePermissions.value.some((permission) => permission.key === key),
    ),
    mode: role.scope.mode,
    groupIds: [...role.scope.group_ids],
    serverIds: [...role.scope.server_ids],
  }
  formError.value = null
  groupQuery.value = ''
  serverQuery.value = ''
  dialogOpen.value = true
}

function closeDialog(): void {
  if (saving.value) return
  dialogOpen.value = false
  editingKey.value = null
  resetForm()
}

function togglePermission(key: string): void {
  form.value.permissions = toggleSelection(form.value.permissions, key)
}

function toggleGroup(id: string): void {
  form.value.groupIds = toggleSelection(form.value.groupIds, id)
}

function toggleServer(id: string): void {
  form.value.serverIds = toggleSelection(form.value.serverIds, id)
}

function setMode(mode: ScopeMode): void {
  form.value.mode = mode
  if (mode === 'all') {
    form.value.groupIds = []
    form.value.serverIds = []
  } else if (mode === 'groups') {
    form.value.serverIds = []
  } else {
    form.value.groupIds = []
  }
}

async function submit(): Promise<void> {
  const selected = selectedGrantable(permissions.value, form.value.permissions)
  const validation = validateRoleForm({
    name: form.value.name,
    selected,
    mode: form.value.mode,
    groupIds: form.value.groupIds,
    serverIds: form.value.serverIds,
  })
  if (validation !== '') {
    formError.value = t(validation)
    return
  }
  const payload: RolePayload = {
    name: form.value.name.trim(),
    description: form.value.description.trim(),
    permissions: selected,
    scope: {
      mode: form.value.mode,
      group_ids: form.value.mode === 'all' ? [] : [...form.value.groupIds],
      server_ids: form.value.mode === 'all' ? [] : [...form.value.serverIds],
    },
  }
  saving.value = true
  formError.value = null
  try {
    if (editingKey.value) {
      const updated = await api.updateRole(editingKey.value, payload)
      actionNotice.value = t('roles.updated', { name: updated.name })
    } else {
      const created = await api.createRole(payload)
      actionNotice.value = t('roles.created', { name: created.name })
    }
    dialogOpen.value = false
    resetForm()
    await load(true)
  } catch (caught) {
    formError.value = errorMessage(caught, t('roles.actionFailed'))
  } finally {
    saving.value = false
  }
}

/* ---------- 删除角色 ---------- */

const deleteTarget = ref<Role | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function openDelete(role: Role): void {
  if (role.builtin) return
  deleteTarget.value = role
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
    await api.deleteRole(target.key)
    actionNotice.value = t('roles.deleted', { name: target.name })
    deleteTarget.value = null
    await load(true)
  } catch (caught) {
    deleteError.value = errorMessage(caught, t('roles.actionFailed'))
  } finally {
    deleteBusy.value = false
  }
}

onMounted(() => {
  if (canManage.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-rolespage">
    <div v-if="!canManage" id="roles-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('roles.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('roles.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('roles.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('roles.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="roles-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load()"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button id="roles-create-open" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
            <AppIcon name="plus" size="0.875rem" />
            {{ t('roles.create') }}
          </button>
        </div>
      </header>

      <div class="ls-adminpage__toolbar">
        <span id="roles-count" class="ls-adminpage__count" aria-live="polite">
          {{ t('roles.count', { total: roles.length }) }}
        </span>
      </div>

      <div v-if="error && loaded" id="roles-banner-error" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-if="actionError" id="roles-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="roles-action-notice" class="ls-notice ls-notice--success" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading' && !loaded"
        id="roles-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && roles.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="roles-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="roles.length === 0" id="roles-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('roles.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('roles.emptyDescription') }}</p>
        <button id="roles-empty-create" class="ls-notice__action" type="button" @click="openCreate">
          {{ t('roles.create') }}
        </button>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table">
          <caption class="ls-visually-hidden">{{ t('roles.title') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('roles.name') }}</th>
              <th scope="col">{{ t('roles.type') }}</th>
              <th scope="col">{{ t('roles.permissions') }}</th>
              <th scope="col">{{ t('roles.scope') }}</th>
              <th scope="col">{{ t('roles.userCount') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="role in sortedRoles"
              :key="role.key"
              class="ls-adminpage__row ls-rolespage__row"
              :class="{ 'ls-rolespage__row--builtin': role.builtin }"
              :data-role-key="role.key"
              :data-builtin="role.builtin ? 'true' : 'false'"
            >
              <td data-label="">
                <div class="ls-adminpage__primary">{{ role.name }}</div>
                <div class="ls-adminpage__meta">
                  <span class="ls-rolespage__key">{{ role.key }}</span>
                  <span v-if="role.description" class="ls-rolespage__desc" :title="role.description">
                    {{ role.description }}
                  </span>
                </div>
              </td>
              <td :data-label="t('roles.type')">
                <span class="ls-chip" :class="role.builtin ? '' : 'ls-chip--muted'">
                  {{ role.builtin ? t('roles.builtin') : t('roles.custom') }}
                </span>
                <div class="ls-adminpage__meta">{{ formatDateTime(role.created_at) }}</div>
              </td>
              <td :data-label="t('roles.permissions')">
                {{ t('roles.permissionCount', { count: role.permissions.length }) }}
              </td>
              <td :data-label="t('roles.scope')" class="ls-rolespage__scope">
                {{ scopeSummary(role) }}
              </td>
              <td :data-label="t('roles.userCount')">{{ role.user_count }}</td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <span v-if="role.builtin" class="ls-adminpage__hint ls-rolespage__builtin-hint">
                  {{ t('roles.builtinHint') }}
                </span>
                <template v-else>
                  <button
                    class="ls-btn ls-btn--small ls-rolespage__edit"
                    type="button"
                    @click="openEdit(role)"
                  >
                    {{ t('roles.edit') }}
                  </button>
                  <button
                    class="ls-btn ls-btn--small ls-rolespage__delete"
                    type="button"
                    @click="openDelete(role)"
                  >
                    {{ t('common.delete') }}
                  </button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal
      v-if="dialogOpen"
      :title="editingRole ? t('roles.edit') : t('roles.create')"
      :description="t('roles.dialogHint')"
      size="lg"
      @close="closeDialog"
    >
      <form id="roles-form" class="ls-adminpage__form" @submit.prevent="submit">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('roles.name') }}</span>
          <input
            id="roles-form-name"
            v-model="form.name"
            class="ls-input"
            type="text"
            maxlength="32"
            autocomplete="off"
            data-autofocus
            :disabled="saving"
          />
        </label>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('roles.descriptionLabel') }}</span>
          <textarea
            id="roles-form-description"
            v-model="form.description"
            class="ls-input ls-adminpage__textarea"
            rows="2"
            maxlength="200"
            :disabled="saving"
          />
        </label>

        <fieldset class="ls-adminpage__fieldset" :disabled="saving">
          <legend class="ls-field__label">{{ t('roles.scope') }}</legend>
          <div class="ls-adminpage__modes" role="group" :aria-label="t('roles.scope')">
            <button
              v-for="mode in SCOPE_MODES"
              :id="`roles-scope-${mode.value}`"
              :key="mode.value"
              class="ls-adminpage__mode"
              :class="{ 'is-active': form.mode === mode.value }"
              type="button"
              :aria-pressed="form.mode === mode.value"
              @click="setMode(mode.value)"
            >
              <span class="ls-adminpage__mode-label">{{ t(mode.labelKey) }}</span>
              <span class="ls-adminpage__mode-hint">{{ t(mode.hintKey) }}</span>
            </button>
          </div>

          <div v-if="showsGroups(form.mode)" class="ls-adminpage__pick">
            <div class="ls-adminpage__pick-head">
              <span class="ls-adminpage__pick-title">{{ t('roles.groups') }}</span>
              <input
                id="roles-group-search"
                v-model="groupQuery"
                class="ls-input ls-adminpage__pick-search"
                type="search"
                autocomplete="off"
                :placeholder="t('roles.searchGroups')"
                :aria-label="t('roles.searchGroups')"
              />
            </div>
            <p v-if="groups.length === 0" class="ls-adminpage__meta">{{ t('roles.noGroups') }}</p>
            <p v-else-if="visibleGroups.length === 0" class="ls-adminpage__meta">
              {{ t('roles.noGroups') }}
            </p>
            <div v-else class="ls-adminpage__pick-list">
              <label v-for="group in visibleGroups" :key="group.id" class="ls-adminpage__option">
                <input
                  type="checkbox"
                  :data-group-id="group.id"
                  :checked="form.groupIds.includes(group.id)"
                  @change="toggleGroup(group.id)"
                />
                <span class="ls-adminpage__option-text">{{ group.name }}</span>
                <span class="ls-adminpage__meta">{{ group.member_count }}</span>
              </label>
            </div>
          </div>

          <div v-if="showsServers(form.mode)" class="ls-adminpage__pick">
            <div class="ls-adminpage__pick-head">
              <span class="ls-adminpage__pick-title">
                {{ form.mode === 'groups' ? t('roles.servers') : t('roles.serversOnly') }}
              </span>
              <input
                id="roles-server-search"
                v-model="serverQuery"
                class="ls-input ls-adminpage__pick-search"
                type="search"
                autocomplete="off"
                :placeholder="t('roles.searchServers')"
                :aria-label="t('roles.searchServers')"
              />
            </div>
            <p v-if="servers.length === 0" class="ls-adminpage__meta">{{ t('roles.noServers') }}</p>
            <p v-else-if="visibleServers.length === 0" class="ls-adminpage__meta">
              {{ t('roles.noServers') }}
            </p>
            <div v-else class="ls-adminpage__pick-list">
              <label v-for="server in visibleServers" :key="server.id" class="ls-adminpage__option">
                <input
                  type="checkbox"
                  :data-server-id="server.id"
                  :checked="form.serverIds.includes(server.id)"
                  @change="toggleServer(server.id)"
                />
                <span class="ls-adminpage__option-text">{{ server.name }}</span>
                <span class="ls-adminpage__meta">{{ server.host }}</span>
              </label>
            </div>
          </div>
        </fieldset>

        <fieldset class="ls-adminpage__fieldset" :disabled="saving">
          <legend class="ls-field__label">{{ t('roles.permissions') }}</legend>
          <p class="ls-adminpage__form-hint">{{ t('roles.permissionsHint') }}</p>
          <p id="roles-permission-count" class="ls-adminpage__meta">
            {{ t('roles.permissionSelected', { selected: selectedCount, grantable: grantablePermissions.length }) }}
          </p>
          <div v-for="group in permissionGroups" :key="group.domain" class="ls-adminpage__perm-group">
            <p class="ls-adminpage__perm-domain">{{ group.label }}</p>
            <div class="ls-adminpage__perm-grid">
              <label
                v-for="permission in group.items"
                :key="permission.key"
                class="ls-adminpage__perm"
                :class="{ 'is-admin-only': permission.admin_only }"
              >
                <input
                  type="checkbox"
                  :data-permission-key="permission.key"
                  :data-admin-only="permission.admin_only ? 'true' : undefined"
                  :checked="form.permissions.includes(permission.key)"
                  :disabled="permission.admin_only"
                  @change="togglePermission(permission.key)"
                />
                <span class="ls-adminpage__perm-body">
                  <span class="ls-adminpage__perm-name">{{ permission.name }}</span>
                  <span class="ls-adminpage__perm-key">{{ permission.key }}</span>
                  <span v-if="permission.admin_only" class="ls-adminpage__perm-note">
                    <span class="ls-chip ls-chip--muted">{{ t('roles.adminOnly') }}</span>
                    <span class="ls-adminpage__perm-reason">{{ t('roles.adminOnlyReason') }}</span>
                  </span>
                </span>
              </label>
            </div>
          </div>
        </fieldset>

        <p v-if="formError" id="roles-form-error" class="ls-adminpage__form-error" role="alert">
          {{ formError }}
        </p>
      </form>
      <template #footer>
        <button id="roles-cancel" class="ls-btn" type="button" :disabled="saving" @click="closeDialog">
          {{ t('common.cancel') }}
        </button>
        <button
          id="roles-save"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="roles-form"
          :disabled="saving"
        >
          {{ t('common.save') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="t('roles.deleteTitle')"
      :description="t('roles.deleteHint', { name: deleteTarget.name })"
      size="sm"
      @close="closeDelete"
    >
      <p class="ls-adminpage__form-hint">{{ t('roles.deleteImpact') }}</p>
      <p v-if="deleteError" id="roles-delete-error" class="ls-adminpage__form-error" role="alert">
        {{ deleteError }}
      </p>
      <template #footer>
        <button
          id="roles-delete-cancel"
          class="ls-btn"
          type="button"
          :disabled="deleteBusy"
          @click="closeDelete"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          id="roles-delete-confirm"
          class="ls-btn ls-btn--danger"
          type="button"
          :disabled="deleteBusy"
          @click="submitDelete"
        >
          {{ t('roles.deleteConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
