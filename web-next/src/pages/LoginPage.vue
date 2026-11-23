<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import BrandMark from '@/components/BrandMark.vue'
import { loginErrorMessage, useLoginCopy } from '@/composables/useLoginCopy'
import { useAuth } from '@/composables/useAuth'

/**
 * 登录页：对接 lib/api.ts 的 api.login（经 useAuth().login 落到会话单例）。
 *
 * 状态机：校验中（loading）→ 待输入 / 提交中（submitting）/ 失败（error + 重试）。
 * 三种异步态都有可见反馈，失败不静默。
 */
const route = useRoute()
const router = useRouter()
const copy = useLoginCopy()
const { status, login, ensureSession } = useAuth()

const username = ref('admin')
const password = ref('')
const revealPassword = ref(false)
const submitting = ref(false)
const errorText = ref<string | null>(null)
const usernameInput = ref<HTMLInputElement | null>(null)
const passwordInput = ref<HTMLInputElement | null>(null)

const isChecking = computed(() => status.value === 'loading')

/** 只接受站内 hash 路径，避免 ?redirect=//evil.com 这类开放跳转。 */
function safeRedirect(raw: unknown): string {
  const target = typeof raw === 'string' ? raw : ''
  if (!target.startsWith('/') || target.startsWith('//')) return '/'
  const path = target.split('#')[0]
  if (path === '/login' || path.startsWith('/login?')) return '/'
  return target
}

const redirectTarget = computed(() => safeRedirect(route.query.redirect))

async function goAfterLogin(): Promise<void> {
  await router.replace(redirectTarget.value)
}

async function submit(): Promise<void> {
  if (submitting.value) return
  const name = username.value.trim()
  if (!name || !password.value) {
    errorText.value = copy.value.requiredError
    return
  }
  submitting.value = true
  errorText.value = null
  try {
    await login(name, password.value)
    await goAfterLogin()
  } catch (error) {
    errorText.value = loginErrorMessage(error)
    await nextTick()
    // 失败后把焦点交回密码框，键盘用户可以直接改密码重试
    passwordInput.value?.focus()
  } finally {
    submitting.value = false
  }
}

/** 已登录时（例如直接打开 /login）不再显示表单，回到目标页。 */
watch(
  status,
  (value) => {
    if (value === 'authenticated') void goAfterLogin()
  },
  { immediate: true },
)

onMounted(() => {
  void ensureSession().then(() => {
    if (!username.value.trim()) usernameInput.value?.focus()
  })
})
</script>

<template>
  <div class="ls-login">
    <div class="ls-login__halo" aria-hidden="true" />

    <section class="ls-login__card" :aria-busy="isChecking || submitting">
      <div class="ls-login__head">
        <span class="ls-login__mark" aria-hidden="true">
          <BrandMark size="2rem" />
        </span>
        <h1 class="ls-login__title">
          LeadStack
          <span class="ls-login__title-tag">{{ copy.brandTagline }}</span>
        </h1>
        <p class="ls-login__subtitle">{{ copy.subtitle }}</p>
      </div>

      <p v-if="isChecking" class="ls-login__checking" role="status" aria-live="polite">
        <span class="ls-spinner" aria-hidden="true" />
        {{ copy.checkingSession }}
      </p>

      <form v-else class="ls-login__form" novalidate @submit.prevent="submit">
        <h2 class="ls-login__form-title">{{ copy.formTitle }}</h2>
        <p class="ls-login__form-hint">{{ copy.formHint }}</p>

        <div class="ls-field">
          <label class="ls-field__label" for="login-username">{{ copy.usernameLabel }}</label>
          <input
            id="login-username"
            ref="usernameInput"
            v-model="username"
            class="ls-input"
            name="username"
            type="text"
            autocomplete="username"
            autocapitalize="none"
            spellcheck="false"
            placeholder="admin"
            :disabled="submitting"
            :aria-describedby="errorText ? 'login-error' : undefined"
          />
        </div>

        <div class="ls-field">
          <label class="ls-field__label" for="login-password">{{ copy.passwordLabel }}</label>
          <div class="ls-field__row">
            <input
              id="login-password"
              ref="passwordInput"
              v-model="password"
              class="ls-input"
              name="password"
              :type="revealPassword ? 'text' : 'password'"
              autocomplete="current-password"
              :disabled="submitting"
              :aria-describedby="errorText ? 'login-error' : undefined"
            />
            <button
              class="ls-icon-btn ls-field__action"
              type="button"
              :aria-pressed="revealPassword"
              :aria-label="revealPassword ? copy.hidePassword : copy.showPassword"
              :title="revealPassword ? copy.hidePassword : copy.showPassword"
              :disabled="submitting"
              @click="revealPassword = !revealPassword"
            >
              <AppIcon :name="revealPassword ? 'shield-check' : 'key'" size="1rem" />
            </button>
          </div>
        </div>

        <p v-if="errorText" id="login-error" class="ls-login__error" role="alert">
          <AppIcon name="alert-triangle" size="0.875rem" />
          <span>{{ errorText }}</span>
          <button class="ls-login__retry" type="button" :disabled="submitting" @click="submit">
            {{ copy.retry }}
          </button>
        </p>

        <button
          id="login-submit"
          class="ls-btn ls-btn--primary ls-btn--block"
          type="submit"
          :disabled="submitting"
          :aria-busy="submitting"
        >
          <span v-if="submitting" class="ls-spinner" aria-hidden="true" />
          {{ submitting ? copy.submitting : copy.submit }}
        </button>

        <p class="ls-login__foot">{{ copy.footHint }}</p>
      </form>
    </section>
  </div>
</template>
