<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type Credential, type CreateCredentialInput } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime, truncateMiddle } from '@/lib/format'

type CredentialKind = 'password' | 'key'

const { t } = useI18n()
const { can } = useCan()
const canWrite = computed(() => can('server.write'))

const state = ref<'loading' | 'ready' | 'error'>('loading')
const credentials = ref<Credential[]>([])
const error = ref<string | null>(null)
const refreshing = ref(false)

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)

const createOpen = ref(false)
const createBusy = ref(false)
const createError = ref<string | null>(null)
const form = ref({
  kind: 'password' as CredentialKind,
  name: '',
  username: 'root',
  password: '',
  privateKey: '',
  passphrase: '',
})

const deleteTarget = ref<Credential | null>(null)
const deleting = ref(false)

const countLabel = computed(() => t('ops.credentials.count', { count: credentials.value.length }))

function kindLabel(kind: string): string {
  if (kind === 'password') return t('ops.credentials.kind.password')
  if (kind === 'key') return t('ops.credentials.kind.key')
  // baota 凭证由服务器接入流程创建，这里只读展示，不提供新建入口。
  return kind
}

async function load(options: { silent?: boolean } = {}): Promise<void> {
  if (options.silent) refreshing.value = true
  else state.value = 'loading'
  try {
    credentials.value = await api.credentials()
    state.value = 'ready'
    error.value = null
  } catch (caught) {
    error.value = errorMessage(caught, t('ops.credentials.loadFailed'))
    if (options.silent) actionError.value = error.value
    else state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

function openCreate(): void {
  // 打开表单时清空所有密文域，避免上一次输入残留。
  form.value = { kind: 'password', name: '', username: 'root', password: '', privateKey: '', passphrase: '' }
  createError.value = null
  createOpen.value = true
}

function closeCreate(): void {
  if (createBusy.value) return
  form.value = { kind: 'password', name: '', username: 'root', password: '', privateKey: '', passphrase: '' }
  createOpen.value = false
}

async function submitCreate(): Promise<void> {
  const name = form.value.name.trim()
  const username = form.value.username.trim()
  createError.value = null
  if (name === '') {
    createError.value = t('ops.credentials.errorName')
    return
  }
  if (username === '') {
    createError.value = t('ops.credentials.errorUsername')
    return
  }
  let payload: CreateCredentialInput
  if (form.value.kind === 'password') {
    if (form.value.password === '') {
      createError.value = t('ops.credentials.errorPassword')
      return
    }
    payload = { name, kind: 'password', username, password: form.value.password }
  } else {
    if (form.value.privateKey.trim() === '') {
      createError.value = t('ops.credentials.errorKey')
      return
    }
    payload = {
      name,
      kind: 'key',
      username,
      private_key: form.value.privateKey,
      passphrase: form.value.passphrase.trim() === '' ? undefined : form.value.passphrase,
    }
  }

  createBusy.value = true
  try {
    const created = await api.createCredential(payload)
    actionNotice.value = t('ops.credentials.created', { name: created.name })
    // 提交后立即丢弃明文：列表与后续任何渲染都只可能拿到名称/类型/指纹。
    // 注意不能走 closeCreate()——它会在 createBusy 期间直接 return，弹窗就关不掉。
    createOpen.value = false
    form.value = { kind: 'password', name: '', username: 'root', password: '', privateKey: '', passphrase: '' }
    await load({ silent: true })
  } catch (caught) {
    createError.value = errorMessage(caught, t('ops.credentials.createFailed'))
  } finally {
    createBusy.value = false
  }
}

function openDelete(target: Credential): void {
  deleteTarget.value = target
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
    await api.deleteCredential(target.id)
    actionNotice.value = t('ops.credentials.deleted', { name: target.name })
    deleteTarget.value = null
    await load({ silent: true })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('ops.credentials.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  if (canWrite.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-credentialspage">
    <div v-if="!canWrite" id="credentials-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('authz.noPermission') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('ops.credentials.pageTitle') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('ops.credentials.pageDescription') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="credentials-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load({ silent: true })"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button id="credentials-create-open" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
            <AppIcon name="key" size="0.875rem" />
            {{ t('ops.credentials.create') }}
          </button>
        </div>
      </header>

      <p class="ls-adminpage__form-hint">{{ t('ops.credentials.securityNote') }}</p>

      <div class="ls-adminpage__toolbar">
        <span id="credentials-count" class="ls-adminpage__count" aria-live="polite">{{ countLabel }}</span>
      </div>

      <div v-if="actionError" id="credentials-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="credentials-action-notice" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading'"
        id="credentials-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && credentials.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('ops.credentials.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="credentials-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="credentials.length === 0" id="credentials-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('ops.credentials.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('ops.credentials.emptyDescription') }}</p>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table">
          <caption class="ls-visually-hidden">{{ t('ops.credentials.pageTitle') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('ops.credentials.name') }}</th>
              <th scope="col">{{ t('ops.credentials.kind') }}</th>
              <th scope="col">{{ t('ops.credentials.username') }}</th>
              <th scope="col">{{ t('ops.credentials.fingerprint') }}</th>
              <th scope="col">{{ t('ops.credentials.serverCount') }}</th>
              <th scope="col">{{ t('ops.common.createdAt') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="credential in credentials"
              :key="credential.id"
              class="ls-adminpage__row ls-credentialspage__row"
              :data-credential-id="credential.id"
              :data-credential-kind="credential.kind"
            >
              <td data-label="">
                <div class="ls-adminpage__primary">{{ credential.name }}</div>
                <div class="ls-adminpage__meta">{{ credential.id }}</div>
              </td>
              <td :data-label="t('ops.credentials.kind')">
                <span class="ls-chip ls-chip--muted">{{ kindLabel(credential.kind) }}</span>
              </td>
              <td :data-label="t('ops.credentials.username')">{{ credential.username || t('common.na') }}</td>
              <td :data-label="t('ops.credentials.fingerprint')" class="ls-adminpage__date">
                <code :title="credential.fingerprint ?? ''" class="ls-credentialspage__fingerprint">
                  {{ credential.fingerprint ? truncateMiddle(credential.fingerprint, 12, 8) : t('ops.credentials.noFingerprint') }}
                </code>
              </td>
              <td :data-label="t('ops.credentials.serverCount')">
                {{ credential.server_count > 0 ? credential.server_count : t('ops.credentials.noServers') }}
              </td>
              <td :data-label="t('ops.common.createdAt')" class="ls-adminpage__date">
                {{ formatDateTime(credential.created_at) }}
              </td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <button
                  class="ls-btn ls-btn--small ls-adminpage__danger ls-credentialspage__delete"
                  type="button"
                  :disabled="deleting"
                  @click="openDelete(credential)"
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
      v-if="createOpen"
      :title="t('ops.credentials.create')"
      :description="t('ops.credentials.createHint')"
      @close="closeCreate"
    >
      <form id="credentials-create-form" class="ls-adminpage__form" @submit.prevent="submitCreate">
        <fieldset class="ls-adminpage__fieldset">
          <legend class="ls-field__label">{{ t('ops.credentials.kind') }}</legend>
          <div class="ls-adminpage__modes">
            <label class="ls-adminpage__mode">
              <input
                id="credentials-kind-password"
                v-model="form.kind"
                type="radio"
                value="password"
                :disabled="createBusy"
              />
              <span>{{ t('ops.credentials.kind.password') }}</span>
              <span class="ls-adminpage__hint">{{ t('ops.credentials.kindHint.password') }}</span>
            </label>
            <label class="ls-adminpage__mode">
              <input id="credentials-kind-key" v-model="form.kind" type="radio" value="key" :disabled="createBusy" />
              <span>{{ t('ops.credentials.kind.key') }}</span>
              <span class="ls-adminpage__hint">{{ t('ops.credentials.kindHint.key') }}</span>
            </label>
          </div>
        </fieldset>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('ops.credentials.name') }}</span>
          <input
            id="credentials-name"
            v-model="form.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            maxlength="64"
            :placeholder="t('ops.credentials.namePlaceholder')"
            data-autofocus
            :disabled="createBusy"
          />
        </label>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('ops.credentials.username') }}</span>
          <input
            id="credentials-username"
            v-model="form.username"
            class="ls-input"
            type="text"
            autocomplete="off"
            maxlength="64"
            :disabled="createBusy"
          />
        </label>

        <label v-if="form.kind === 'password'" class="ls-field">
          <span class="ls-field__label">{{ t('ops.credentials.password') }}</span>
          <input
            id="credentials-password"
            v-model="form.password"
            class="ls-input"
            type="password"
            autocomplete="new-password"
            :disabled="createBusy"
          />
        </label>

        <template v-else>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('ops.credentials.privateKey') }}</span>
            <textarea
              id="credentials-key"
              v-model="form.privateKey"
              class="ls-input ls-adminpage__textarea"
              rows="6"
              spellcheck="false"
              autocomplete="off"
              :placeholder="t('ops.credentials.privateKeyPlaceholder')"
              :disabled="createBusy"
            />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('ops.credentials.passphrase') }}</span>
            <input
              id="credentials-passphrase"
              v-model="form.passphrase"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :disabled="createBusy"
            />
          </label>
        </template>

        <p v-if="createError" id="credentials-create-error" class="ls-adminpage__form-error" role="alert">
          {{ createError }}
        </p>
      </form>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="createBusy" @click="closeCreate">
          {{ t('common.cancel') }}
        </button>
        <button
          id="credentials-create-submit"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="credentials-create-form"
          :disabled="createBusy"
        >
          {{ t('common.create') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="t('ops.credentials.deleteTitle')"
      :description="t('ops.credentials.deleteImpact')"
      size="sm"
      @close="closeDelete"
    >
      <div class="ls-adminpage__form">
        <p class="ls-adminpage__form-hint">{{ deleteTarget.name }}</p>
      </div>
      <template #footer>
        <button id="credentials-delete-cancel" class="ls-btn" type="button" :disabled="deleting" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="credentials-delete-confirm"
          class="ls-btn ls-btn--primary ls-adminpage__danger"
          type="button"
          :disabled="deleting"
          @click="submitDelete"
        >
          {{ t('ops.credentials.deleteConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
