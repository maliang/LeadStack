<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import { RouterLink } from 'vue-router'
import { useTheme } from '@vean/ui'

import AppIcon from '@/components/AppIcon.vue'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { SUPPORTED_LANGUAGES, switchLanguage, useI18n, type Language } from '@/i18n'
import { api, type NetworkConfig, type NetworkUpdatePayload, type SystemInfo } from '@/lib/api'
import { appearanceDensity, setDensity, type AppearanceDensity } from '@/lib/appearance'
import { errorMessage } from '@/lib/errors'
import { parsePort, validateListenAddress, validatePublicUrl } from '@/lib/network'
import UpgradeCard from '@/components/system/UpgradeCard.vue'

const { t, language } = useI18n()
const { mode: themeMode } = useTheme()
const { can } = useCan()
const { user, scope } = useAuth()

/** 读取 language 建立渲染依赖：切换语言后整页（含新卡片标签）重新渲染。 */
const htmlLang = computed(() => (language.value === 'en-US' ? 'en' : 'zh-CN'))

const languageError = ref<string | null>(null)

const languageModel = computed<Language>({
  get: () => language.value,
  // en-US 词典是按需加载的独立 chunk：先 await 词典就绪再切换，
  // 保证下拉框选完立即是英文，而不是先闪一下 zh-CN 回退文案。
  // 加载失败时 switchLanguage 不切语言、不落盘并返回 false：
  // 这里提示失败，下拉框因 language 未变随重渲染回到旧语言（即回滚），用户可重选重试。
  set: async (value) => {
    languageError.value = null
    if (value === language.value) return
    const ok = await switchLanguage(value)
    if (!ok && value !== language.value) languageError.value = t('common.error')
  },
})

/** 密度选项文案随语言切换，读取 language 建立依赖。 */
const densityOptions = computed<{ value: AppearanceDensity; label: string }[]>(() => {
  void language.value
  return [
    { value: 'standard', label: t('appearance.density.standard') },
    { value: 'compact', label: t('appearance.density.compact') },
  ]
})

const densityModel = computed<AppearanceDensity>({
  get: () => appearanceDensity.value,
  set: (value) => setDensity(value),
})

const themeModeOptions = computed(() => {
  void language.value
  return [
    { value: 'auto', label: t('appearance.theme.system') },
    { value: 'light', label: t('appearance.theme.light') },
    { value: 'dark', label: t('appearance.theme.dark') },
  ]
})

const themeModeModel = computed({
  get: () => themeMode.value,
  set: (value: string) => {
    if (value === 'auto' || value === 'light' || value === 'dark') themeMode.value = value
  },
})

const canManage = computed(() => can('system.manage'))
const canNetwork = computed(() => canManage.value && scope.value.mode === 'all')

const info = ref<SystemInfo | null>(null)
const infoError = ref<string | null>(null)
const infoLoading = ref(false)

const network = ref<NetworkConfig | null>(null)
const networkError = ref<string | null>(null)
const networkState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const savingNetwork = ref(false)
const networkNotice = ref<string | null>(null)
const networkFieldError = ref<string | null>(null)

const form = ref({ listen: '', publicUrl: '', sshPort: '22' })

const passwordForm = ref({ old: '', next: '', confirm: '' })
const passwordBusy = ref(false)
const passwordError = ref<string | null>(null)
const passwordNotice = ref<string | null>(null)

/**
 * 角色名优先用后端给的 role_name（自定义角色没有 role.<key> 文案，
 * 直接拼 key 会把 `role.auditor` 这样的内部标识显示到界面上）。
 * 内置角色没有 role_name 时才查词表，查不到再退回 key。
 */
const roleLabel = computed(() => {
  const current = user.value
  if (!current) return t('common.na')
  if (current.role_name) return current.role_name
  const key = `role.${current.role}`
  const translated = t(key)
  return translated === key ? current.role : translated
})

function syncForm(data: NetworkConfig): void {
  form.value = {
    listen: data.listen ?? data.active_listen,
    publicUrl: data.public_url ?? '',
    sshPort: String(data.default_ssh_port),
  }
}

async function loadInfo(): Promise<void> {
  infoLoading.value = true
  infoError.value = null
  try {
    info.value = await api.systemInfo()
  } catch (caught) {
    infoError.value = errorMessage(caught, t('ops.settings.versionUnavailable'))
  } finally {
    infoLoading.value = false
  }
}

async function loadNetwork(): Promise<void> {
  if (!canNetwork.value) return
  networkState.value = 'loading'
  networkError.value = null
  try {
    const data = await api.network()
    network.value = data
    syncForm(data)
    networkState.value = 'ready'
  } catch (caught) {
    networkError.value = errorMessage(caught, t('network.loadFailed'))
    networkState.value = 'error'
  }
}

function sourceLabel(source: NetworkConfig['listen_source']): string {
  const normalized = source === 'settings' || source === 'env' ? source : 'default'
  return t(`network.source.${normalized}`)
}

const listenChanged = computed(() => {
  const data = network.value
  if (!data) return false
  return form.value.listen.trim() !== (data.listen ?? data.active_listen)
})
const needsRestart = computed(() => Boolean(network.value?.restart_required) || listenChanged.value)

async function saveNetwork(): Promise<void> {
  const data = network.value
  if (!data) return
  networkFieldError.value = null
  const listenError = validateListenAddress(form.value.listen)
  if (listenError) {
    networkFieldError.value = t(listenError)
    return
  }
  const publicUrlError = validatePublicUrl(form.value.publicUrl)
  if (publicUrlError) {
    networkFieldError.value = t(publicUrlError)
    return
  }
  const port = parsePort(form.value.sshPort)
  if (port === null) {
    networkFieldError.value = t('network.error.portRange')
    return
  }
  const trimmedUrl = form.value.publicUrl.trim()
  if (data.public_url && trimmedUrl === '') {
    networkFieldError.value = t('network.error.publicUrlCannotClear')
    return
  }

  const payload: NetworkUpdatePayload = {}
  const listen = form.value.listen.trim()
  if (listen !== (data.listen ?? '')) payload.listen = listen
  if (trimmedUrl !== (data.public_url ?? '')) payload.public_url = trimmedUrl
  if (port !== data.default_ssh_port) payload.default_ssh_port = port

  if (Object.keys(payload).length === 0) {
    networkNotice.value = t('network.noRestart')
    return
  }

  savingNetwork.value = true
  networkNotice.value = null
  try {
    await api.updateNetwork(payload)
    // 保存后再从 GET /system/network 读回，界面显示的一定是服务端持久化后的值。
    await loadNetwork()
    networkNotice.value = t('network.saved')
  } catch (caught) {
    networkFieldError.value = errorMessage(caught, t('network.saveFailedHint'))
  } finally {
    savingNetwork.value = false
  }
}

async function changePassword(): Promise<void> {
  passwordError.value = null
  passwordNotice.value = null
  if (!passwordForm.value.old || passwordForm.value.next.length < 6) {
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

onMounted(() => {
  void loadInfo()
  if (canNetwork.value) void loadNetwork()
})
</script>

<template>
  <section class="ls-adminpage ls-settingspage" :lang="htmlLang">
    <header class="ls-adminpage__head">
      <div class="ls-adminpage__heading">
        <h1 class="ls-adminpage__title">{{ t('nav.settings') }}</h1>
        <p class="ls-adminpage__subtitle">{{ t('settings.description') }}</p>
      </div>
    </header>

    <div class="ls-settingspage__grid">
      <section class="ls-card">
        <header class="ls-card__head">
          <h2 class="ls-card__title">{{ t('settings.account') }}</h2>
          <p class="ls-card__note">{{ t('ops.settings.accountHint') }}</p>
        </header>
        <dl class="ls-settingspage__dl">
          <dt>{{ t('users.username') }}</dt>
          <dd>{{ user?.username ?? t('common.na') }}</dd>
          <dt>{{ t('users.role') }}</dt>
          <dd>{{ roleLabel }}</dd>
          <dt>{{ t('settings.version') }}</dt>
          <dd id="settings-version">
            <span v-if="infoLoading" class="ls-spinner" aria-hidden="true" />
            <template v-else-if="info">{{ info.version }}</template>
            <template v-else>{{ infoError ?? t('ops.settings.versionUnavailable') }}</template>
          </dd>
        </dl>
        <div v-if="infoError" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__text">{{ infoError }}</p>
          <button id="settings-info-retry" class="ls-notice__action" type="button" @click="loadInfo()">
            {{ t('common.retry') }}
          </button>
        </div>
        <div class="ls-settingspage__links">
          <RouterLink v-if="can('user.manage')" class="ls-btn ls-btn--small" to="/users">
            {{ t('settings.manageUsers') }}
          </RouterLink>
          <RouterLink v-if="can('server.write')" class="ls-btn ls-btn--small" to="/credentials">
            {{ t('nav.credentials') }}
          </RouterLink>
        </div>
      </section>

      <section class="ls-card">
        <header class="ls-card__head">
          <h2 class="ls-card__title">{{ t('users.changePassword') }}</h2>
          <p class="ls-card__note">{{ t('ops.settings.securityNote') }}</p>
        </header>
        <form id="settings-password-form" class="ls-adminpage__form" @submit.prevent="changePassword">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('users.oldPassword') }}</span>
            <input
              id="settings-old-password"
              v-model="passwordForm.old"
              class="ls-input"
              type="password"
              autocomplete="current-password"
              :disabled="passwordBusy"
            />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('users.newPassword') }}</span>
            <input
              id="settings-new-password"
              v-model="passwordForm.next"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :disabled="passwordBusy"
            />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('settings.confirmPassword') }}</span>
            <input
              id="settings-confirm-password"
              v-model="passwordForm.confirm"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :disabled="passwordBusy"
            />
          </label>
          <p v-if="passwordError" id="settings-password-error" class="ls-adminpage__form-error" role="alert">
            {{ passwordError }}
          </p>
          <div class="ls-adminpage__primary">
            <button
              id="settings-password-submit"
              class="ls-btn ls-btn--primary"
              type="submit"
              :disabled="passwordBusy"
              :aria-busy="passwordBusy"
            >
              <span v-if="passwordBusy" class="ls-spinner" aria-hidden="true" />
              {{ t('common.save') }}
            </button>
          </div>
        </form>
        <p
          v-if="passwordNotice"
          id="settings-password-notice"
          class="ls-notice ls-notice--success"
          role="status"
        >
          {{ passwordNotice }}
        </p>
      </section>

      <section v-if="canNetwork" class="ls-card ls-settingspage__wide">
        <header class="ls-card__head">
          <h2 class="ls-card__title">{{ t('network.title') }}</h2>
          <p class="ls-card__note">{{ t('network.description') }}</p>
        </header>

        <div
          v-if="networkState === 'loading'"
          id="settings-network-skeleton"
          class="ls-adminpage__skeleton"
          aria-busy="true"
          :aria-label="t('common.loading')"
        >
          <span class="ls-skeleton ls-adminpage__skeleton-item" />
        </div>

        <div v-else-if="networkState === 'error'" id="settings-network-error" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__title">{{ t('network.loadFailed') }}</p>
          <p class="ls-notice__text">{{ networkError }}</p>
          <button id="settings-network-retry" class="ls-notice__action" type="button" @click="loadNetwork()">
            {{ t('common.retry') }}
          </button>
        </div>

        <form
          v-else-if="networkState === 'ready' && network"
          id="settings-network-form"
          class="ls-adminpage__form"
          @submit.prevent="saveNetwork"
        >
          <div class="ls-settingspage__active">
            <span>{{ t('network.effectiveNow') }}</span>
            <code id="settings-active-listen">{{ network.active_listen }}</code>
            <span class="ls-chip ls-chip--muted">{{ sourceLabel(network.listen_source) }}</span>
          </div>

          <label class="ls-field">
            <span class="ls-field__label">{{ t('network.listen') }}</span>
            <input
              id="settings-listen"
              v-model="form.listen"
              class="ls-input"
              type="text"
              autocomplete="off"
              :placeholder="t('network.listenHint')"
              :disabled="savingNetwork"
            />
            <span class="ls-adminpage__hint">{{ t('network.listenDefaultHint') }}</span>
          </label>

          <label class="ls-field">
            <span class="ls-field__label">{{ t('network.publicUrl') }}</span>
            <input
              id="settings-public-url"
              v-model="form.publicUrl"
              class="ls-input"
              type="url"
              autocomplete="off"
              placeholder="https://leadstack.example.com"
              :disabled="savingNetwork"
            />
            <span class="ls-adminpage__hint">{{ t('network.publicUrlHint') }}</span>
          </label>

          <label class="ls-field">
            <span class="ls-field__label">{{ t('network.defaultSshPort') }}</span>
            <input
              id="settings-ssh-port"
              v-model="form.sshPort"
              class="ls-input"
              type="number"
              min="1"
              max="65535"
              inputmode="numeric"
              :disabled="savingNetwork"
            />
            <span class="ls-adminpage__hint">{{ t('network.defaultSshPortUsage') }}</span>
          </label>

          <div v-if="needsRestart" class="ls-banner ls-banner--warning" role="status">
            <AppIcon name="alert-triangle" size="1rem" />
            <div class="ls-banner__body">
              <p class="ls-banner__title">{{ t('network.restartRequiredTitle') }}</p>
              <p class="ls-banner__text">
                {{ t('network.restartRequiredHint', { active: network.active_listen, configured: form.listen }) }}
              </p>
            </div>
          </div>

          <p v-if="networkFieldError" id="settings-network-field-error" class="ls-adminpage__form-error" role="alert">
            {{ networkFieldError }}
          </p>

          <div class="ls-adminpage__primary">
            <button
              id="settings-network-save"
              class="ls-btn ls-btn--primary"
              type="submit"
              :disabled="savingNetwork"
            >
              <span v-if="savingNetwork" class="ls-spinner" aria-hidden="true" />
              {{ t('common.save') }}
            </button>
          </div>
          <p class="ls-adminpage__form-hint">{{ t('network.honestNotice') }}</p>
        </form>

        <p
          v-if="networkNotice"
          id="settings-network-notice"
          class="ls-notice ls-notice--success"
          role="status"
        >
          {{ networkNotice }}
        </p>
      </section>

      <div v-else-if="canManage" id="settings-network-locked" class="ls-notice ls-settingspage__wide">
        <p class="ls-notice__title">{{ t('network.needsGlobalScopeTitle') }}</p>
        <p class="ls-notice__text">{{ t('network.needsGlobalScope') }}</p>
      </div>

      <div v-else id="settings-network-no-permission" class="ls-notice ls-settingspage__wide">
        <p class="ls-notice__title">{{ t('ops.settings.networkNeedsPermission') }}</p>
      </div>

      <section class="ls-card ls-settingspage__wide">
        <header class="ls-card__head">
          <h2 class="ls-card__title">{{ t('appearance.title') }}</h2>
          <p class="ls-card__note">{{ t('appearance.description') }}</p>
        </header>
        <div class="ls-appearance">
          <div class="ls-appearance__row">
            <div class="ls-appearance__copy">
              <span id="settings-appearance-theme-label" class="ls-appearance__label">
                {{ t('appearance.theme') }}
              </span>
              <span class="ls-appearance__hint">{{ t('appearance.themeHint') }}</span>
            </div>
            <div class="ls-appearance__control">
              <AppSelect
                id="settings-appearance-theme"
                v-model="themeModeModel"
                class="ls-input ls-appearance__select"
                :aria-label="t('appearance.theme')"
              >
                <option v-for="option in themeModeOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </option>
              </AppSelect>
            </div>
          </div>

          <div class="ls-appearance__row">
            <label class="ls-appearance__copy" for="settings-appearance-language">
              <span class="ls-appearance__label">{{ t('appearance.language') }}</span>
              <span class="ls-appearance__hint">{{ t('appearance.languageHint') }}</span>
            </label>
            <div class="ls-appearance__control">
              <AppSelect
                id="settings-appearance-language"
                v-model="languageModel"
                class="ls-input ls-appearance__select"
              >
                <option v-for="item in SUPPORTED_LANGUAGES" :key="item.value" :value="item.value">
                  {{ item.label }}
                </option>
              </AppSelect>
            </div>
          </div>

          <div class="ls-appearance__row">
            <label class="ls-appearance__copy" for="settings-appearance-density">
              <span class="ls-appearance__label">{{ t('appearance.density') }}</span>
              <span class="ls-appearance__hint">{{ t('appearance.densityHint') }}</span>
            </label>
            <div class="ls-appearance__control">
              <AppSelect
                id="settings-appearance-density"
                v-model="densityModel"
                class="ls-input ls-appearance__select"
              >
                <option v-for="item in densityOptions" :key="item.value" :value="item.value">
                  {{ item.label }}
                </option>
              </AppSelect>
            </div>
          </div>
        </div>
        <p
          v-if="languageError"
          id="settings-language-error"
          class="ls-adminpage__form-error"
          role="alert"
        >
          {{ languageError }}
        </p>
        <p class="ls-card__note">{{ t('appearance.persistHint') }}</p>
      </section>

      <UpgradeCard v-if="canManage" />

      <section class="ls-card ls-settingspage__wide">
        <header class="ls-card__head">
          <h2 class="ls-card__title">{{ t('settings.accessControl') }}</h2>
          <p class="ls-card__note">{{ t('settings.accessControlHint') }}</p>
        </header>
        <div class="ls-settingspage__links">
          <RouterLink v-if="can('role.manage')" class="ls-btn ls-btn--small" to="/roles">
            {{ t('nav.roles') }}
          </RouterLink>
          <RouterLink v-if="can('server.read')" class="ls-btn ls-btn--small" to="/groups">
            {{ t('nav.groups') }}
          </RouterLink>
        </div>
      </section>
    </div>
  </section>
</template>
