<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type Server, type ServerGroup } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

const { t } = useI18n()
const { can } = useCan()
const { scope } = useAuth()

const canRead = computed(() => can('server.read'))
const canWrite = computed(() => can('group.write') && scope.value.mode === 'all')

const state = ref<'loading' | 'ready' | 'error'>('loading')
const groups = ref<ServerGroup[]>([])
const error = ref<string | null>(null)
const refreshing = ref(false)

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)

const editorOpen = ref(false)
const editorBusy = ref(false)
const editorError = ref<string | null>(null)
const editorId = ref<string | null>(null)
const editorForm = ref({ name: '', description: '' })

const deleteTarget = ref<ServerGroup | null>(null)
const deleting = ref(false)

const membersGroup = ref<ServerGroup | null>(null)
const membersBusy = ref(false)
const membersError = ref<string | null>(null)
const membersKeyword = ref('')
const membersSelected = ref<Set<string>>(new Set())
const servers = ref<Server[]>([])
const serversLoaded = ref(false)

const countLabel = computed(() => t('ops.groups.count', { count: groups.value.length }))
const membersCountLabel = computed(() =>
  t('ops.groups.membersSelected', { count: membersSelected.value.size }),
)
const filteredServers = computed(() => {
  const keyword = membersKeyword.value.trim().toLowerCase()
  if (keyword === '') return servers.value
  return servers.value.filter(
    (server) =>
      server.name.toLowerCase().includes(keyword) ||
      server.host.toLowerCase().includes(keyword),
  )
})

async function load(options: { silent?: boolean } = {}): Promise<void> {
  if (options.silent) refreshing.value = true
  else state.value = 'loading'
  try {
    groups.value = await api.serverGroups()
    state.value = 'ready'
    error.value = null
  } catch (caught) {
    error.value = errorMessage(caught, t('groups.loadFailed'))
    if (options.silent) actionError.value = error.value
    else state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

function openCreate(): void {
  editorId.value = null
  editorForm.value = { name: '', description: '' }
  editorError.value = null
  editorOpen.value = true
}

function openEdit(group: ServerGroup): void {
  editorId.value = group.id
  editorForm.value = { name: group.name, description: group.description ?? '' }
  editorError.value = null
  editorOpen.value = true
}

function closeEditor(): void {
  if (editorBusy.value) return
  editorOpen.value = false
}

async function submitEditor(): Promise<void> {
  const name = editorForm.value.name.trim()
  if (name === '') {
    editorError.value = t('groups.errorName')
    return
  }
  const description = editorForm.value.description.trim()
  editorBusy.value = true
  try {
    if (editorId.value) {
      await api.updateServerGroup(editorId.value, { name, description })
      actionNotice.value = t('groups.updated', { name })
    } else {
      await api.createServerGroup({ name, description })
      actionNotice.value = t('groups.created', { name })
    }
    editorOpen.value = false
    await load({ silent: true })
  } catch (caught) {
    editorError.value = errorMessage(caught, t('common.error'))
  } finally {
    editorBusy.value = false
  }
}

function openDelete(group: ServerGroup): void {
  deleteTarget.value = group
}

function closeDelete(): void {
  if (deleting.value) return
  deleteTarget.value = null
}

async function submitDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await api.deleteServerGroup(target.id)
    actionNotice.value = t('groups.deleted', { name: target.name })
    deleteTarget.value = null
    await load({ silent: true })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('common.error'))
  } finally {
    deleting.value = false
  }
}

async function openMembers(group: ServerGroup): Promise<void> {
  membersGroup.value = group
  membersKeyword.value = ''
  membersError.value = null
  membersBusy.value = true
  try {
    if (!serversLoaded.value) {
      servers.value = await api.servers()
      serversLoaded.value = true
    }
    const current = await api.serverGroupServers(group.id)
    membersSelected.value = new Set(current.map((server) => server.id))
  } catch (caught) {
    membersError.value = errorMessage(caught, t('groups.loadFailed'))
  } finally {
    membersBusy.value = false
  }
}

function closeMembers(): void {
  if (membersBusy.value) return
  membersGroup.value = null
}

function toggleMember(id: string): void {
  const next = new Set(membersSelected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  membersSelected.value = next
}

async function saveMembers(): Promise<void> {
  const group = membersGroup.value
  if (!group) return
  membersBusy.value = true
  membersError.value = null
  try {
    await api.setServerGroupMembers(group.id, Array.from(membersSelected.value))
    actionNotice.value = t('groups.membersSaved', { name: group.name })
    membersGroup.value = null
    await load({ silent: true })
  } catch (caught) {
    membersError.value = errorMessage(caught, t('groups.membersSaveFailed'))
  } finally {
    membersBusy.value = false
  }
}

onMounted(() => {
  if (canRead.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-groupspage">
    <div v-if="!canRead" id="groups-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('authz.noPermission') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('groups.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('groups.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="groups-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load({ silent: true })"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button
            v-if="canWrite"
            id="groups-create-open"
            class="ls-btn ls-btn--primary"
            type="button"
            @click="openCreate"
          >
            <AppIcon name="plus" size="0.875rem" />
            {{ t('groups.create') }}
          </button>
        </div>
      </header>

      <p v-if="!canWrite" id="groups-readonly-hint" class="ls-adminpage__form-hint">
        {{ t('groups.readOnlyHint') }}
      </p>

      <div class="ls-adminpage__toolbar">
        <span id="groups-count" class="ls-adminpage__count" aria-live="polite">{{ countLabel }}</span>
      </div>

      <div v-if="actionError" id="groups-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="groups-action-notice" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading'"
        id="groups-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && groups.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('groups.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="groups-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="groups.length === 0" id="groups-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('groups.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('groups.emptyDescription') }}</p>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table">
          <caption class="ls-visually-hidden">{{ t('groups.title') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('common.name') }}</th>
              <th scope="col">{{ t('groups.descriptionLabel') }}</th>
              <th scope="col">{{ t('groups.memberCount') }}</th>
              <th scope="col">{{ t('groups.updatedAt') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="group in groups"
              :key="group.id"
              class="ls-adminpage__row ls-groupspage__row"
              :data-group-id="group.id"
            >
              <td data-label="">
                <div class="ls-adminpage__primary">{{ group.name }}</div>
                <div class="ls-adminpage__meta">{{ group.id }}</div>
              </td>
              <td :data-label="t('groups.descriptionLabel')">{{ group.description || t('common.na') }}</td>
              <td :data-label="t('groups.memberCount')">
                <button
                  class="ls-btn ls-btn--small ls-groupspage__members"
                  type="button"
                  :aria-label="`${t('groups.manageMembers')}：${group.name}`"
                  @click="openMembers(group)"
                >
                  {{ group.member_count }}
                </button>
              </td>
              <td :data-label="t('groups.updatedAt')" class="ls-adminpage__date">
                {{ formatDateTime(group.updated_at) }}
              </td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <button
                  v-if="canWrite"
                  class="ls-btn ls-btn--small ls-groupspage__edit"
                  type="button"
                  @click="openEdit(group)"
                >
                  {{ t('common.edit') }}
                </button>
                <button
                  v-if="canWrite"
                  class="ls-btn ls-btn--small ls-adminpage__danger ls-groupspage__delete"
                  type="button"
                  :disabled="deleting"
                  @click="openDelete(group)"
                >
                  {{ t('common.delete') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal
      v-if="editorOpen"
      :title="editorId ? t('groups.edit') : t('groups.create')"
      :description="t('groups.dialogHint')"
      @close="closeEditor"
    >
      <form id="groups-form" class="ls-adminpage__form" @submit.prevent="submitEditor">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('common.name') }}</span>
          <input
            id="groups-form-name"
            v-model="editorForm.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            maxlength="32"
            data-autofocus
            :disabled="editorBusy"
          />
        </label>
        <label class="ls-field">
          <span class="ls-field__label">{{ t('groups.descriptionLabel') }}</span>
          <textarea
            id="groups-form-description"
            v-model="editorForm.description"
            class="ls-input ls-adminpage__textarea"
            rows="3"
            maxlength="200"
            :disabled="editorBusy"
          />
        </label>
        <p v-if="editorError" id="groups-form-error" class="ls-adminpage__form-error" role="alert">
          {{ editorError }}
        </p>
      </form>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="editorBusy" @click="closeEditor">
          {{ t('common.cancel') }}
        </button>
        <button
          id="groups-form-submit"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="groups-form"
          :disabled="editorBusy"
        >
          {{ t('common.save') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="t('groups.deleteTitle')"
      :description="t('groups.deleteImpact', { name: deleteTarget.name, count: deleteTarget.member_count })"
      size="sm"
      @close="closeDelete"
    >
      <div class="ls-adminpage__form">
        <p class="ls-adminpage__form-hint">{{ deleteTarget.name }}</p>
      </div>
      <template #footer>
        <button id="groups-delete-cancel" class="ls-btn" type="button" :disabled="deleting" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="groups-delete-confirm"
          class="ls-btn ls-btn--primary ls-adminpage__danger"
          type="button"
          :disabled="deleting"
          @click="submitDelete"
        >
          {{ t('ops.groups.deleteConfirm') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="membersGroup"
      :title="t('groups.manageMembers')"
      :description="membersGroup.name"
      size="lg"
      @close="closeMembers"
    >
      <div class="ls-adminpage__form">
        <p class="ls-adminpage__form-hint">{{ t('groups.membersImpact') }}</p>
        <div class="ls-adminpage__toolbar">
          <input
            id="groups-members-search"
            v-model="membersKeyword"
            class="ls-input ls-adminpage__search"
            type="search"
            autocomplete="off"
            :placeholder="t('groups.searchServers')"
            :aria-label="t('groups.searchServers')"
          />
          <span id="groups-members-count" class="ls-adminpage__count">{{ membersCountLabel }}</span>
        </div>

        <p v-if="membersError" id="groups-members-error" class="ls-adminpage__form-error" role="alert">
          {{ membersError }}
        </p>

        <div v-if="filteredServers.length === 0" class="ls-notice">
          <p class="ls-notice__text">
            {{ servers.length === 0 ? t('groups.membersEmpty') : t('groups.noServers') }}
          </p>
        </div>

        <ul v-else id="groups-members-list" class="ls-adminpage__pick-list">
          <li v-for="server in filteredServers" :key="server.id">
            <label class="ls-adminpage__option">
              <input
                type="checkbox"
                :value="server.id"
                :checked="membersSelected.has(server.id)"
                :disabled="membersBusy"
                :data-server-id="server.id"
                @change="toggleMember(server.id)"
              />
              <span class="ls-adminpage__primary">{{ server.name }}</span>
              <span class="ls-adminpage__hint">{{ server.host }}:{{ server.port ?? 22 }}</span>
            </label>
          </li>
        </ul>
      </div>
      <template #footer>
        <button id="groups-members-cancel" class="ls-btn" type="button" :disabled="membersBusy" @click="closeMembers">
          {{ t('common.cancel') }}
        </button>
        <button
          id="groups-members-save"
          class="ls-btn ls-btn--primary"
          type="button"
          :disabled="membersBusy"
          @click="saveMembers"
        >
          {{ t('common.save') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
