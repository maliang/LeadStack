<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import ServerAccountFormDialog from '@/components/server-detail/ServerAccountFormDialog.vue'
import { useI18n } from '@/i18n'
import { api, type Server, type ServerAccount, type ServerAccountLoginResult } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import { errorMessage } from '@/lib/errors'
import { loginResultMode } from '@/lib/server-detail'

/**
 * 「访问账号」标签：一台服务器可保存多条登录信息。
 *
 * 列表接口不返回密码；只有用户点「显示密码 / 复制密码 / 复制整条」时才按需读取
 * （服务端每次写 server.account.access 审计）。脚本执行走账号级
 * POST /servers/{id}/accounts/{aid}/login，结果按 url / terminal / output 分派。
 */
const props = defineProps<{
  server: Server
  /** server.write：没有权限时新增/编辑/删除/执行都不渲染。 */
  canWrite: boolean
}>()

const { t } = useI18n()

const accounts = ref<ServerAccount[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref<string | null>(null)
const actionError = ref<string | null>(null)
const notice = ref<string | null>(null)
const refreshing = ref(false)

const secrets = ref<Record<string, string>>({})
const revealed = ref<Set<string>>(new Set())
const copiedKey = ref<string | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | null = null

const formOpen = ref(false)
const editTarget = ref<ServerAccount | null>(null)
const deleteTarget = ref<ServerAccount | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

const runningId = ref<string | null>(null)
const result = ref<{ accountId: string; data: ServerAccountLoginResult } | null>(null)
const resultCopied = ref(false)

const defaultAddress = computed(() => {
  const port = props.server.port
  return port ? `${props.server.host}:${port}` : props.server.host
})

const countLabel = computed(() => t('server.accounts.count', { count: accounts.value.length }))
const resultMode = computed(() => (result.value ? loginResultMode(result.value.data) : 'empty'))

function flash(key: string): void {
  copiedKey.value = key
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copiedKey.value = null
    copiedTimer = null
  }, 1600)
}

async function load(): Promise<void> {
  state.value = accounts.value.length > 0 ? 'ready' : 'loading'
  error.value = null
  try {
    accounts.value = await api.serverAccounts(props.server.id)
    state.value = 'ready'
  } catch (caught) {
    error.value = errorMessage(caught, t('server.accounts.loadFailed'))
    state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

function refresh(): void {
  refreshing.value = true
  actionError.value = null
  void load()
}

async function ensureSecret(account: ServerAccount): Promise<string> {
  if (!account.has_password) return ''
  const cached = secrets.value[account.id]
  if (cached !== undefined) return cached
  const password = await api.serverAccountSecret(props.server.id, account.id)
  secrets.value = { ...secrets.value, [account.id]: password }
  return password
}

function isRevealed(account: ServerAccount): boolean {
  return revealed.value.has(account.id)
}

async function toggleReveal(account: ServerAccount): Promise<void> {
  if (!account.has_password) return
  actionError.value = null
  try {
    await ensureSecret(account)
    const next = new Set(revealed.value)
    if (next.has(account.id)) next.delete(account.id)
    else next.add(account.id)
    revealed.value = next
  } catch (caught) {
    actionError.value = errorMessage(caught, t('server.accounts.loadFailed'))
  }
}

function passwordText(account: ServerAccount): string {
  if (!account.has_password) return t('server.accounts.passwordEmpty')
  if (!isRevealed(account)) return '••••••••'
  return secrets.value[account.id] ?? '••••••••'
}

async function copyText(key: string, text: string): Promise<void> {
  const ok = await writeClipboard(text)
  flash(ok ? key : `${key}:failed`)
}

async function copyField(account: ServerAccount, field: 'name' | 'address' | 'username' | 'note' | 'password'): Promise<void> {
  actionError.value = null
  try {
    let value =
      field === 'password'
        ? await ensureSecret(account)
        : field === 'name'
          ? account.name
          : field === 'address'
            ? account.address
            : field === 'username'
              ? account.username
              : account.note
    if (value === '') {
      actionError.value = t(field === 'password' ? 'server.accounts.passwordEmpty' : 'server.accounts.valueEmpty')
      return
    }
    await copyText(`${account.id}:${field}`, value)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('server.accounts.copyFailed'))
  }
}

function buildAccountText(account: ServerAccount, password: string): string {
  const lines = [
    `${t('server.account.form.name')}: ${account.name}`,
    `${t('server.account.form.address')}: ${account.address}`,
    `${t('server.account.form.username')}: ${account.username}`,
  ]
  if (password !== '' || account.has_password) {
    lines.push(`${t('server.account.form.password')}: ${password}`)
  }
  if (account.note !== '') lines.push(`${t('server.account.form.note')}: ${account.note}`)
  const fields = Object.entries(account.custom_fields)
  if (fields.length > 0) {
    lines.push(`${t('server.account.form.customFields')}:`)
    for (const [key, value] of fields) lines.push(`  ${key}: ${value}`)
  }
  return lines.join('\n')
}

async function copyAll(account: ServerAccount): Promise<void> {
  actionError.value = null
  try {
    const password = await ensureSecret(account)
    await copyText(`all:${account.id}`, buildAccountText(account, password))
  } catch (caught) {
    actionError.value = errorMessage(caught, t('server.accounts.copyFailed'))
  }
}

async function runScript(account: ServerAccount): Promise<void> {
  if (!account.has_login_script || runningId.value !== null) return
  runningId.value = account.id
  actionError.value = null
  result.value = null
  try {
    const data = await api.serverAccountLogin(props.server.id, account.id)
    result.value = { accountId: account.id, data }
    if (data.url !== '') window.open(data.url, '_blank', 'noopener,noreferrer')
  } catch (caught) {
    actionError.value = errorMessage(caught, t('server.accounts.runFailed'))
  } finally {
    runningId.value = null
  }
}

function openResultUrl(): void {
  const url = result.value?.data.url ?? ''
  if (url !== '') window.open(url, '_blank', 'noopener,noreferrer')
}

async function copyCommand(): Promise<void> {
  const command = result.value?.data.terminal ?? ''
  if (command === '') return
  resultCopied.value = await writeClipboard(command)
  if (resultCopied.value) setTimeout(() => (resultCopied.value = false), 1600)
}

function openCreate(): void {
  editTarget.value = null
  formOpen.value = true
}

function openEdit(account: ServerAccount): void {
  editTarget.value = account
  formOpen.value = true
}

function closeForm(): void {
  formOpen.value = false
  editTarget.value = null
}

async function handleSaved(saved: ServerAccount): Promise<void> {
  const edited = editTarget.value !== null
  formOpen.value = false
  editTarget.value = null
  notice.value = t(edited ? 'server.accounts.saved' : 'server.accounts.created', { name: saved.name })
  secrets.value = {}
  revealed.value = new Set()
  await load()
}

function askDelete(account: ServerAccount): void {
  deleteTarget.value = account
  deleteError.value = null
}

function closeDelete(): void {
  if (deleteBusy.value) return
  deleteTarget.value = null
  deleteError.value = null
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleteBusy.value) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    await api.deleteServerAccount(props.server.id, target.id)
    deleteTarget.value = null
    notice.value = t('server.accounts.deleted', { name: target.name })
    if (result.value?.accountId === target.id) result.value = null
    await load()
  } catch (caught) {
    deleteError.value = errorMessage(caught, t('server.accounts.saveFailed'))
  } finally {
    deleteBusy.value = false
  }
}

function fieldLabel(field: string): string {
  if (field === 'name') return t('server.account.form.name')
  if (field === 'address') return t('server.account.form.address')
  if (field === 'username') return t('server.account.form.username')
  if (field === 'password') return t('server.account.form.password')
  if (field === 'note') return t('server.account.form.note')
  return field
}

onMounted(() => {
  void load()
})

watch(
  () => props.server.id,
  () => {
    accounts.value = []
    secrets.value = {}
    revealed.value = new Set()
    result.value = null
    notice.value = null
    actionError.value = null
    void load()
  },
)

onBeforeUnmount(() => {
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = null
})
</script>

<template>
  <div class="ls-accounts">
    <section class="ls-card" :aria-label="t('server.accounts.title')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="key-round" size="1rem" />
            {{ t('server.accounts.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.accounts.subtitle') }}</p>
        </div>
        <div class="ls-card__actions">
          <span class="ls-chip ls-chip--muted">{{ countLabel }}</span>
          <button class="ls-btn ls-btn--small" type="button" :disabled="refreshing" @click="refresh">
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('server.accounts.refresh') }}
          </button>
          <button v-if="canWrite" class="ls-btn ls-btn--small ls-btn--primary" type="button" @click="openCreate">
            <AppIcon name="plus" size="0.875rem" />
            {{ t('server.accounts.add') }}
          </button>
        </div>
      </header>

      <p class="ls-card__note">{{ t('server.accounts.hint') }}</p>

      <div v-if="actionError" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
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

      <div v-if="state === 'loading'" class="ls-accounts__loading" role="status">
        <span class="ls-spinner" aria-hidden="true" />
        {{ t('common.loading') }}
      </div>

      <div v-else-if="state === 'error'" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('server.accounts.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="accounts.length === 0" class="ls-empty" role="status">
        <p class="ls-empty__title">{{ t('server.accounts.emptyTitle') }}</p>
        <p class="ls-empty__text">{{ t('server.accounts.emptyHint') }}</p>
        <button v-if="canWrite" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
          <AppIcon name="plus" size="0.875rem" />
          {{ t('server.accounts.add') }}
        </button>
      </div>

      <ul v-else class="ls-accounts__list">
        <li v-for="account in accounts" :key="account.id" class="ls-account" :data-account-id="account.id">
          <header class="ls-account__head">
            <div class="ls-account__heading">
              <h3 class="ls-account__name">{{ account.name }}</h3>
              <button
                class="ls-icon-btn"
                type="button"
                :aria-label="t('server.accounts.copyField', { field: fieldLabel('name') })"
                :title="t('server.accounts.copyField', { field: fieldLabel('name') })"
                @click="copyField(account, 'name')"
              >
                {{ copiedKey === `${account.id}:name` ? t('common.copied') : t('server.access.copy') }}
              </button>
              <div class="ls-account__chips">
                <span v-if="account.has_password" class="ls-chip ls-chip--muted">
                  {{ t('server.accounts.hasPassword') }}
                </span>
                <span v-if="account.has_login_script" class="ls-chip ls-chip--muted">
                  {{ t('server.accounts.hasScript') }}
                </span>
                <span v-if="Object.keys(account.custom_fields).length > 0" class="ls-chip ls-chip--muted">
                  {{ t('server.accounts.customFieldsCount', { count: Object.keys(account.custom_fields).length }) }}
                </span>
              </div>
            </div>
            <div class="ls-account__actions">
              <button class="ls-btn ls-btn--small" type="button" @click="copyAll(account)">
                <AppIcon name="file-text" size="0.875rem" />
                {{
                  copiedKey === `all:${account.id}`
                    ? t('common.copied')
                    : copiedKey === `all:${account.id}:failed`
                      ? t('server.accounts.copyFailed')
                      : t('server.accounts.copyAll')
                }}
              </button>
              <button
                v-if="canWrite && account.has_login_script"
                class="ls-btn ls-btn--small ls-btn--primary"
                type="button"
                :disabled="runningId !== null"
                :aria-busy="runningId === account.id"
                @click="runScript(account)"
              >
                <span v-if="runningId === account.id" class="ls-spinner" aria-hidden="true" />
                <AppIcon v-else name="play" size="0.875rem" />
                {{ runningId === account.id ? t('server.accounts.running') : t('server.accounts.runScript') }}
              </button>
              <button v-if="canWrite" class="ls-btn ls-btn--small" type="button" @click="openEdit(account)">
                {{ t('server.accounts.edit') }}
              </button>
              <button
                v-if="canWrite"
                class="ls-btn ls-btn--small ls-btn--danger"
                type="button"
                @click="askDelete(account)"
              >
                {{ t('server.accounts.delete') }}
              </button>
            </div>
          </header>

          <dl class="ls-kv ls-account__kv">
            <div class="ls-kv__row">
              <dt>{{ t('server.account.form.address') }}</dt>
              <dd class="ls-kv__value">
                <span class="ls-kv__text ls-kv__value--mono" :title="account.address">
                  {{ account.address || t('common.na') }}
                </span>
                <span class="ls-kv__tools">
                  <button
                    v-if="account.address"
                    class="ls-icon-btn"
                    type="button"
                    :aria-label="t('server.accounts.copyField', { field: fieldLabel('address') })"
                    @click="copyField(account, 'address')"
                  >
                    {{ copiedKey === `${account.id}:address` ? t('common.copied') : t('server.access.copy') }}
                  </button>
                </span>
              </dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('server.account.form.username') }}</dt>
              <dd class="ls-kv__value">
                <span class="ls-kv__text ls-kv__value--mono" :title="account.username">
                  {{ account.username || t('common.na') }}
                </span>
                <span class="ls-kv__tools">
                  <button
                    v-if="account.username"
                    class="ls-icon-btn"
                    type="button"
                    :aria-label="t('server.accounts.copyField', { field: fieldLabel('username') })"
                    @click="copyField(account, 'username')"
                  >
                    {{ copiedKey === `${account.id}:username` ? t('common.copied') : t('server.access.copy') }}
                  </button>
                </span>
              </dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('server.account.form.password') }}</dt>
              <dd class="ls-kv__value">
                <span class="ls-kv__text ls-kv__value--mono" :title="passwordText(account)">
                  {{ passwordText(account) }}
                </span>
                <span v-if="account.has_password" class="ls-kv__tools">
                  <button class="ls-icon-btn" type="button" @click="toggleReveal(account)">
                    {{ isRevealed(account) ? t('server.accounts.hidePassword') : t('server.accounts.showPassword') }}
                  </button>
                  <button
                    class="ls-icon-btn"
                    type="button"
                    :aria-label="t('server.accounts.copyField', { field: fieldLabel('password') })"
                    @click="copyField(account, 'password')"
                  >
                    {{ copiedKey === `${account.id}:password` ? t('common.copied') : t('server.access.copy') }}
                  </button>
                </span>
              </dd>
            </div>
            <div v-if="account.note" class="ls-kv__row">
              <dt>{{ t('server.account.form.note') }}</dt>
              <dd class="ls-kv__value">
                <span class="ls-kv__text" :title="account.note">{{ account.note }}</span>
                <span class="ls-kv__tools">
                  <button
                    class="ls-icon-btn"
                    type="button"
                    :aria-label="t('server.accounts.copyField', { field: fieldLabel('note') })"
                    @click="copyField(account, 'note')"
                  >
                    {{ copiedKey === `${account.id}:note` ? t('common.copied') : t('server.access.copy') }}
                  </button>
                </span>
              </dd>
            </div>
          </dl>

          <ul v-if="Object.keys(account.custom_fields).length > 0" class="ls-account__fields">
            <li v-for="(value, key) in account.custom_fields" :key="key" class="ls-account__field">
              <span class="ls-account__field-key">{{ key }}</span>
              <span class="ls-account__field-value" :title="value">{{ value }}</span>
              <button
                class="ls-icon-btn"
                type="button"
                :aria-label="t('server.accounts.copyField', { field: key })"
                @click="copyText(`${account.id}:field:${key}`, value)"
              >
                {{ copiedKey === `${account.id}:field:${key}` ? t('common.copied') : t('server.access.copy') }}
              </button>
            </li>
          </ul>

          <div v-if="result && result.accountId === account.id" class="ls-account__result" role="status">
            <div class="ls-card__head">
              <h4 class="ls-card__title">{{ t('server.accounts.resultTitle') }}</h4>
              <div class="ls-card__actions">
                <button
                  v-if="resultMode === 'terminal'"
                  class="ls-btn ls-btn--small"
                  type="button"
                  @click="copyCommand"
                >
                  <AppIcon name="file-text" size="0.875rem" />
                  {{ resultCopied ? t('common.copied') : t('server.accounts.copyCommand') }}
                </button>
                <button
                  v-if="resultMode === 'url'"
                  class="ls-btn ls-btn--small"
                  type="button"
                  @click="openResultUrl"
                >
                  <AppIcon name="chevron-right" size="0.875rem" />
                  {{ t('server.accounts.openUrl') }}
                </button>
              </div>
            </div>
            <pre v-if="resultMode === 'terminal'" class="ls-code">{{ result.data.terminal }}</pre>
            <pre v-else-if="resultMode === 'output'" class="ls-code ls-code--output">{{ result.data.output }}</pre>
            <p v-else class="ls-card__note">{{ t('server.accounts.resultEmpty') }}</p>
          </div>
        </li>
      </ul>
    </section>

    <ServerAccountFormDialog
      v-if="formOpen"
      :server-id="props.server.id"
      :account="editTarget"
      :default-address="defaultAddress"
      @close="closeForm"
      @saved="handleSaved"
    />

    <AppModal
      v-if="deleteTarget"
      :title="t('server.accounts.deleteTitle')"
      :description="deleteTarget.name"
      tone="danger"
      size="sm"
      @close="closeDelete"
    >
      <p class="ls-notice__text">{{ t('server.accounts.deleteImpact') }}</p>
      <p v-if="deleteError" class="ls-adminpage__form-error" role="alert">{{ deleteError }}</p>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="deleteBusy" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          class="ls-btn ls-btn--danger"
          type="button"
          :disabled="deleteBusy"
          :aria-busy="deleteBusy"
          @click="confirmDelete"
        >
          {{ deleteBusy ? t('common.loading') : t('server.accounts.deleteConfirm') }}
        </button>
      </template>
    </AppModal>
  </div>
</template>
