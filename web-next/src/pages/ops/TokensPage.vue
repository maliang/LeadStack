<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type ApiToken, type CreatedApiToken, type UserRole } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import { errorMessage } from '@/lib/errors'
import { formatDateTime, formatRelativeTime } from '@/lib/format'

type TokenStatus = 'active' | 'expired' | 'revoked'

interface ExpiryOption {
  value: string
  labelKey: string
}

const ROLES: UserRole[] = ['admin', 'operator', 'viewer']
const EXPIRY_OPTIONS: ExpiryOption[] = [
  { value: '', labelKey: 'tokens.expiry.never' },
  { value: '720h', labelKey: 'tokens.expiry.30d' },
  { value: '2160h', labelKey: 'tokens.expiry.90d' },
  { value: '8760h', labelKey: 'tokens.expiry.1y' },
]

/** 状态口径：已撤销 → 已过期 → 有效；纯函数，列表与断言共用一套。 */
function tokenStatus(token: ApiToken, now: number): TokenStatus {
  if (token.revoked_at) return 'revoked'
  if (token.expires_at && token.expires_at <= now) return 'expired'
  return 'active'
}

const { t } = useI18n()
const { can } = useCan()
const canManage = computed(() => can('token.manage'))

const state = ref<'loading' | 'ready' | 'error'>('loading')
const tokens = ref<ApiToken[]>([])
const error = ref<string | null>(null)
const refreshing = ref(false)
const now = ref(Date.now())

const actionError = ref<string | null>(null)
const actionNotice = ref<string | null>(null)

const createOpen = ref(false)
const createBusy = ref(false)
const createError = ref<string | null>(null)
const createForm = ref({ name: '', role: 'operator' as UserRole, expiry: '720h' })

const created = ref<CreatedApiToken | null>(null)
const copied = ref(false)

const revokeTarget = ref<ApiToken | null>(null)
const revokeBusy = ref(false)

const rows = computed(() => tokens.value.map((token) => ({
  token,
  status: tokenStatus(token, now.value),
})))

async function load(options: { silent?: boolean } = {}): Promise<void> {
  if (options.silent) refreshing.value = true
  else state.value = 'loading'
  try {
    tokens.value = await api.apiTokens()
    now.value = Date.now()
    state.value = 'ready'
    error.value = null
  } catch (caught) {
    error.value = errorMessage(caught, t('tokens.loadFailed'))
    if (options.silent) actionError.value = error.value
    else state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

function openCreate(): void {
  createForm.value = { name: '', role: 'operator', expiry: '720h' }
  createError.value = null
  createOpen.value = true
}

function closeCreate(): void {
  if (createBusy.value) return
  createOpen.value = false
}

async function submitCreate(): Promise<void> {
  const name = createForm.value.name.trim()
  if (name === '') {
    createError.value = t('tokens.errorName')
    return
  }
  createBusy.value = true
  createError.value = null
  try {
    const payload: { name: string; role: UserRole; expires_in?: string } = {
      name,
      role: createForm.value.role,
    }
    if (createForm.value.expiry !== '') payload.expires_in = createForm.value.expiry
    const result = await api.createApiToken(payload)
    createOpen.value = false
    created.value = result
    copied.value = false
    actionNotice.value = t('ops.tokens.createdToast', { name: result.name })
    await load({ silent: true })
  } catch (caught) {
    createError.value = errorMessage(caught, t('common.error'))
  } finally {
    createBusy.value = false
  }
}

function closeCreated(): void {
  // 明文只活在这个弹窗里：关闭即丢弃，刷新后更不可能再读到。
  created.value = null
}

async function copySecret(): Promise<void> {
  const secret = created.value?.token
  if (!secret) return
  const ok = await writeClipboard(secret)
  copied.value = ok
  if (!ok) actionError.value = t('ops.tokens.copyFailed')
}

function openRevoke(token: ApiToken): void {
  revokeTarget.value = token
}

function closeRevoke(): void {
  if (revokeBusy.value) return
  revokeTarget.value = null
}

async function submitRevoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target) return
  revokeBusy.value = true
  try {
    await api.deleteApiToken(target.id)
    revokeTarget.value = null
    actionNotice.value = t('tokens.revokedToast', { name: target.name })
    await load({ silent: true })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('common.error'))
  } finally {
    revokeBusy.value = false
  }
}

onMounted(() => {
  if (canManage.value) void load()
})
</script>

<template>
  <section class="ls-adminpage ls-tokenspage">
    <div v-if="!canManage" id="tokens-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('authz.noPermission') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('tokens.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('tokens.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="tokens-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing || state === 'loading'"
            @click="load({ silent: true })"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button id="tokens-create-open" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
            <AppIcon name="plus" size="0.875rem" />
            {{ t('tokens.create') }}
          </button>
        </div>
      </header>

      <p class="ls-adminpage__form-hint">{{ t('tokens.securityNote') }}</p>

      <div v-if="actionError" id="tokens-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="actionNotice" id="tokens-action-notice" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ actionNotice }}</p>
        <button class="ls-notice__action" type="button" @click="actionNotice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div
        v-if="state === 'loading'"
        id="tokens-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && tokens.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('tokens.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="tokens-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="tokens.length === 0" id="tokens-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('tokens.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('tokens.emptyDescription') }}</p>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table">
          <caption class="ls-visually-hidden">{{ t('tokens.title') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('common.name') }}</th>
              <th scope="col">{{ t('users.role') }}</th>
              <th scope="col">{{ t('tokens.status') }}</th>
              <th scope="col">{{ t('tokens.lastUsed') }}</th>
              <th scope="col">{{ t('tokens.expiresAt') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.token.id"
              class="ls-adminpage__row ls-tokenspage__row"
              :data-token-id="row.token.id"
            >
              <td data-label="">
                <div class="ls-adminpage__primary">{{ row.token.name }}</div>
                <div class="ls-adminpage__meta">
                  {{ row.token.created_by ? t('tokens.createdBy', { name: row.token.created_by }) : row.token.id }}
                </div>
              </td>
              <td :data-label="t('users.role')">{{ t(`role.${row.token.role}`) }}</td>
              <td :data-label="t('tokens.status')">
                <span
                  class="ls-chip ls-adminpage__status"
                  :class="{
                    'ls-adminpage__status--active': row.status === 'active',
                    'ls-adminpage__status--disabled': row.status !== 'active',
                  }"
                >
                  {{ t(`tokens.status.${row.status}`) }}
                </span>
              </td>
              <td :data-label="t('tokens.lastUsed')" class="ls-adminpage__date">
                {{ row.token.last_used_at ? formatRelativeTime(row.token.last_used_at, now) : t('tokens.neverUsed') }}
              </td>
              <td :data-label="t('tokens.expiresAt')" class="ls-adminpage__date">
                {{ row.token.expires_at ? formatDateTime(row.token.expires_at) : t('tokens.expiry.never') }}
              </td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <button
                  class="ls-btn ls-btn--small ls-tokenspage__revoke"
                  type="button"
                  :disabled="row.status === 'revoked' || revokeBusy"
                  :aria-label="t('tokens.revoke')"
                  @click="openRevoke(row.token)"
                >
                  {{ t('tokens.revoke') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal v-if="createOpen" :title="t('tokens.create')" :description="t('tokens.createHint')" @close="closeCreate">
      <form id="tokens-create-form" class="ls-adminpage__form" @submit.prevent="submitCreate">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('common.name') }}</span>
          <input
            id="tokens-create-name"
            v-model="createForm.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            maxlength="64"
            :placeholder="t('tokens.namePlaceholder')"
            data-autofocus
            :disabled="createBusy"
          />
        </label>
        <label class="ls-field">
          <span class="ls-field__label">{{ t('users.role') }}</span>
          <select id="tokens-create-role" v-model="createForm.role" class="ls-input" :disabled="createBusy">
            <option v-for="role in ROLES" :key="role" :value="role">{{ t(`role.${role}`) }}</option>
          </select>
        </label>
        <label class="ls-field">
          <span class="ls-field__label">{{ t('ops.tokens.expiry') }}</span>
          <select id="tokens-create-expiry" v-model="createForm.expiry" class="ls-input" :disabled="createBusy">
            <option v-for="option in EXPIRY_OPTIONS" :key="option.value" :value="option.value">
              {{ t(option.labelKey) }}
            </option>
          </select>
          <span class="ls-adminpage__hint">{{ t('ops.tokens.expiryHint') }}</span>
        </label>
        <p v-if="createError" id="tokens-create-error" class="ls-adminpage__form-error" role="alert">
          {{ createError }}
        </p>
      </form>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="createBusy" @click="closeCreate">
          {{ t('common.cancel') }}
        </button>
        <button
          id="tokens-create-submit"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="tokens-create-form"
          :disabled="createBusy"
        >
          {{ t('tokens.create') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="created"
      :title="t('tokens.createdTitle')"
      :description="t('tokens.createdHint')"
      @close="closeCreated"
    >
      <div id="tokens-created" class="ls-adminpage__form">
        <p class="ls-notice ls-notice--error" role="alert">{{ t('tokens.onceWarning') }}</p>
        <code id="tokens-created-secret" class="ls-tokenspage__secret">{{ created.token }}</code>
        <div class="ls-adminpage__primary">
          <button id="tokens-created-copy" class="ls-btn ls-btn--small" type="button" @click="copySecret">
            {{ copied ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
      </div>
      <template #footer>
        <button id="tokens-created-close" class="ls-btn ls-btn--primary" type="button" @click="closeCreated">
          {{ t('tokens.savedIt') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="revokeTarget"
      :title="t('ops.tokens.revokeTitle')"
      :description="t('tokens.revokeImpact')"
      size="sm"
      @close="closeRevoke"
    >
      <div class="ls-adminpage__form">
        <p class="ls-adminpage__form-hint">{{ t('tokens.revoke') }}：{{ revokeTarget.name }}</p>
      </div>
      <template #footer>
        <button id="tokens-revoke-cancel" class="ls-btn" type="button" :disabled="revokeBusy" @click="closeRevoke">
          {{ t('common.cancel') }}
        </button>
        <button
          id="tokens-revoke-confirm"
          class="ls-btn ls-btn--primary ls-adminpage__danger"
          type="button"
          :disabled="revokeBusy"
          @click="submitRevoke"
        >
          {{ t('ops.tokens.revokeConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
