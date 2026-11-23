<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type ServerGroup } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

/** 服务器分组管理弹窗：列表 + 增删查改；写操作要求 group.write 且 scope=all。 */
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'changed'): void
}>()

const { t } = useI18n()
const { can } = useCan()
const { scope } = useAuth()

const canWrite = computed(() => can('group.write') && scope.value.mode === 'all')

const groups = ref<ServerGroup[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')
const loadError = ref<string | null>(null)
const actionError = ref<string | null>(null)
const notice = ref<string | null>(null)
const busy = ref(false)

const formOpen = ref(false)
const editingId = ref<string | null>(null)
const form = ref({ name: '', description: '' })
const formError = ref<string | null>(null)

const deleteId = ref<string | null>(null)

async function load(): Promise<void> {
  state.value = groups.value.length > 0 ? 'ready' : 'loading'
  loadError.value = null
  try {
    groups.value = await api.serverGroups()
    state.value = 'ready'
  } catch (caught) {
    loadError.value = errorMessage(caught, t('server.group.manager.loadFailed'))
    state.value = 'error'
  }
}

function openCreate(): void {
  if (!canWrite.value) return
  editingId.value = null
  form.value = { name: '', description: '' }
  formError.value = null
  formOpen.value = true
}

function openEdit(group: ServerGroup): void {
  if (!canWrite.value) return
  editingId.value = group.id
  form.value = { name: group.name, description: group.description ?? '' }
  formError.value = null
  formOpen.value = true
}

function closeForm(): void {
  if (busy.value) return
  formOpen.value = false
  editingId.value = null
  formError.value = null
}

async function submit(): Promise<void> {
  const name = form.value.name.trim()
  if (name === '') {
    formError.value = t('server.group.manager.errorName')
    return
  }
  if (busy.value) return
  busy.value = true
  actionError.value = null
  formError.value = null
  const description = form.value.description.trim()
  try {
    if (editingId.value) {
      await api.updateServerGroup(editingId.value, { name, description })
      notice.value = t('server.group.manager.updated', { name })
    } else {
      await api.createServerGroup({ name, description })
      notice.value = t('server.group.manager.created', { name })
    }
    formOpen.value = false
    editingId.value = null
    await load()
    emit('changed')
  } catch (caught) {
    formError.value = errorMessage(caught, t('server.group.manager.saveFailed'))
  } finally {
    busy.value = false
  }
}

async function confirmDelete(group: ServerGroup): Promise<void> {
  if (!canWrite.value || busy.value) return
  busy.value = true
  actionError.value = null
  try {
    await api.deleteServerGroup(group.id)
    deleteId.value = null
    notice.value = t('server.group.manager.deleted', { name: group.name })
    await load()
    emit('changed')
  } catch (caught) {
    deleteId.value = null
    actionError.value = errorMessage(caught, t('server.group.manager.deleteFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <AppModal
    :title="t('server.group.manager.title')"
    :description="t('server.group.manager.description')"
    size="lg"
    @close="emit('close')"
  >
    <div class="ls-group-manager">
      <div class="ls-group-manager__bar">
        <span class="ls-chip ls-chip--muted">{{ t('server.group.manager.count', { count: groups.length }) }}</span>
        <span v-if="!canWrite" class="ls-field__hint">{{ t('server.group.manager.readOnly') }}</span>
        <button
          v-if="canWrite"
          id="group-manager-create"
          class="ls-btn ls-btn--small ls-btn--primary"
          type="button"
          @click="openCreate"
        >
          <AppIcon name="plus" size="0.875rem" />
          {{ t('server.group.manager.create') }}
        </button>
      </div>

      <div v-if="actionError" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>
      <div v-if="notice" class="ls-notice ls-notice--success" role="status">
        <p class="ls-notice__title">{{ notice }}</p>
        <button class="ls-notice__action" type="button" @click="notice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <form v-if="formOpen" id="group-manager-form" class="ls-group-manager__form" @submit.prevent="submit">
        <label class="ls-field" for="group-manager-name">
          <span class="ls-field__label">{{ t('server.group.manager.name') }}</span>
          <input
            id="group-manager-name"
            v-model="form.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            data-autofocus
          />
        </label>
        <label class="ls-field" for="group-manager-desc">
          <span class="ls-field__label">{{ t('server.group.manager.descLabel') }}</span>
          <input
            id="group-manager-desc"
            v-model="form.description"
            class="ls-input"
            type="text"
            autocomplete="off"
          />
        </label>
        <p v-if="formError" class="ls-field__error" role="alert">{{ formError }}</p>
        <div class="ls-group-manager__form-actions">
          <button class="ls-btn ls-btn--small" type="button" :disabled="busy" @click="closeForm">
            {{ t('common.cancel') }}
          </button>
          <button class="ls-btn ls-btn--small ls-btn--primary" type="submit" :disabled="busy">
            {{ busy ? t('common.loading') : t('common.save') }}
          </button>
        </div>
      </form>

      <div v-if="state === 'loading'" class="ls-group-manager__loading" role="status">
        <span class="ls-spinner" aria-hidden="true" />
        {{ t('common.loading') }}
      </div>
      <div v-else-if="state === 'error'" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__text">{{ loadError }}</p>
        <button class="ls-notice__action" type="button" @click="load">{{ t('common.retry') }}</button>
      </div>
      <p v-else-if="groups.length === 0" class="ls-field__hint">{{ t('server.group.manager.empty') }}</p>
      <ul v-else class="ls-group-manager__list">
        <li v-for="group in groups" :key="group.id" class="ls-group-manager__item">
          <div class="ls-group-manager__main">
            <span class="ls-group-manager__name">{{ group.name }}</span>
            <span class="ls-group-manager__meta">
              {{ t('server.group.manager.members', { count: group.member_count }) }} ·
              {{ formatDateTime(group.updated_at) }}
            </span>
            <span v-if="group.description" class="ls-group-manager__desc">{{ group.description }}</span>
          </div>
          <div v-if="canWrite" class="ls-group-manager__actions">
            <button class="ls-btn ls-btn--small" type="button" @click="openEdit(group)">
              {{ t('common.edit') }}
            </button>
            <button
              v-if="deleteId !== group.id"
              class="ls-btn ls-btn--small ls-btn--danger"
              type="button"
              @click="deleteId = group.id"
            >
              {{ t('common.delete') }}
            </button>
            <template v-else>
              <button class="ls-btn ls-btn--small ls-btn--danger" type="button" :disabled="busy" @click="confirmDelete(group)">
                {{ busy ? t('common.loading') : t('server.group.manager.deleteConfirm') }}
              </button>
              <button class="ls-btn ls-btn--small" type="button" :disabled="busy" @click="deleteId = null">
                {{ t('common.cancel') }}
              </button>
            </template>
          </div>
        </li>
      </ul>
    </div>
    <template #footer>
      <button class="ls-btn" type="button" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </AppModal>
</template>
