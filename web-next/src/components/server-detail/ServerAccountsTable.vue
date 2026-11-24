<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import { api, type Server, type ServerAccount } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import { errorMessage } from '@/lib/errors'

/** 列表页「访问账号」只读表格：查看、复制、执行登录脚本，不提供增删改。 */
const props = defineProps<{
  server: Server
  canWrite: boolean
}>()

const { t } = useI18n()
const accounts = ref<ServerAccount[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref<string | null>(null)
const secrets = ref<Record<string, string>>({})
const revealed = ref<Set<string>>(new Set())
const copied = ref<string | null>(null)
const running = ref<string | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | null = null

async function load(): Promise<void> {
  state.value = 'loading'
  error.value = null
  try {
    accounts.value = await api.serverAccounts(props.server.id)
    state.value = 'ready'
  } catch (caught) {
    error.value = errorMessage(caught, t('server.accounts.loadFailed'))
    state.value = 'error'
  }
}

async function secretFor(account: ServerAccount): Promise<string> {
  if (!account.has_password) return ''
  if (secrets.value[account.id] !== undefined) return secrets.value[account.id]
  const value = await api.serverAccountSecret(props.server.id, account.id)
  secrets.value = { ...secrets.value, [account.id]: value }
  return value
}

function passwordText(account: ServerAccount): string {
  if (!account.has_password) return t('server.accounts.passwordEmpty')
  return revealed.value.has(account.id) ? secrets.value[account.id] ?? '••••••••' : '••••••••'
}

function flash(key: string): void {
  copied.value = key
  if (copiedTimer) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => { copied.value = null; copiedTimer = null }, 1500)
}

async function copy(account: ServerAccount, field: 'address' | 'username' | 'password' | 'all'): Promise<void> {
  error.value = null
  try {
    let text = field === 'address' ? account.address : field === 'username' ? account.username : await secretFor(account)
    if (field === 'all') {
      const password = await secretFor(account)
      text = [account.name, account.address, account.username, password].filter(Boolean).join('\n')
    }
    if (!text) {
      error.value = t('server.accounts.valueEmpty')
      return
    }
    if (await writeClipboard(text)) flash(`${account.id}:${field}`)
  } catch (caught) {
    error.value = errorMessage(caught, t('server.accounts.copyFailed'))
  }
}

async function reveal(account: ServerAccount): Promise<void> {
  error.value = null
  try {
    await secretFor(account)
    const next = new Set(revealed.value)
    if (next.has(account.id)) next.delete(account.id)
    else next.add(account.id)
    revealed.value = next
  } catch (caught) {
    error.value = errorMessage(caught, t('server.accounts.loadFailed'))
  }
}

function login(account: ServerAccount): void {
  if (!props.canWrite || running.value !== null) return
  const win = window.open('about:blank', '_blank')
  if (!win) {
    error.value = t('server.login.urlBlocked')
    return
  }
  win.opener = null
  running.value = account.id
  void api.serverAccountLogin(props.server.id, account.id).then((result) => {
    if (!win.closed && result.url) win.location.replace(result.url)
    else win.close()
  }).catch((caught) => {
    win.close()
    error.value = errorMessage(caught, t('server.accounts.runFailed'))
  }).finally(() => { running.value = null })
}

onMounted(load)
</script>

<template>
  <div class="ls-accounts-table">
    <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
    <p v-if="state === 'loading'" class="ls-field__hint">{{ t('common.loading') }}</p>
    <p v-else-if="accounts.length === 0" class="ls-empty__text">{{ t('server.accounts.emptyHint') }}</p>
    <div v-else class="ls-table-scroll">
      <table class="ls-table">
        <thead>
          <tr>
            <th>{{ t('server.account.form.name') }}</th>
            <th>{{ t('server.account.form.address') }}</th>
            <th>{{ t('server.account.form.username') }}</th>
            <th>{{ t('server.account.form.password') }}</th>
            <th>{{ t('server.account.form.note') }}</th>
            <th>{{ t('server.accounts.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="account in accounts" :key="account.id" :data-account-id="account.id">
            <td>{{ account.name }}</td>
            <td class="ls-table__mono">
              {{ account.address || '—' }}
              <button v-if="account.address" class="ls-icon-btn" type="button" :aria-label="t('server.accounts.copyField', { field: t('server.account.form.address') })" @click="copy(account, 'address')">
                <AppIcon :name="copied === `${account.id}:address` ? 'shield-check' : 'copy'" size="0.75rem" />
              </button>
            </td>
            <td class="ls-table__mono">
              {{ account.username || '—' }}
              <button v-if="account.username" class="ls-icon-btn" type="button" :aria-label="t('server.accounts.copyField', { field: t('server.account.form.username') })" @click="copy(account, 'username')">
                <AppIcon :name="copied === `${account.id}:username` ? 'shield-check' : 'copy'" size="0.75rem" />
              </button>
            </td>
            <td class="ls-table__mono">
              {{ passwordText(account) }}
              <button v-if="account.has_password" class="ls-icon-btn" type="button" :aria-label="t('server.accounts.showPassword')" @click="reveal(account)">
                <AppIcon :name="revealed.has(account.id) ? 'shield-check' : 'key-round'" size="0.75rem" />
              </button>
              <button v-if="account.has_password" class="ls-icon-btn" type="button" :aria-label="t('server.accounts.copyField', { field: t('server.account.form.password') })" @click="copy(account, 'password')">
                <AppIcon name="copy" size="0.75rem" />
              </button>
            </td>
            <td>{{ account.note || '—' }}</td>
            <td class="ls-table__actions">
              <button v-if="canWrite && account.has_login_script" class="ls-btn ls-btn--small ls-btn--primary" type="button" :disabled="running !== null" :aria-busy="running === account.id" @click="login(account)">
                <span v-if="running === account.id" class="ls-spinner" aria-hidden="true" />
                <AppIcon v-else name="play" size="0.75rem" />
                {{ t('server.accounts.runScript') }}
              </button>
              <button class="ls-btn ls-btn--small" type="button" @click="copy(account, 'all')">
                <AppIcon name="copy" size="0.75rem" />
                {{ t('server.accounts.copyAll') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
