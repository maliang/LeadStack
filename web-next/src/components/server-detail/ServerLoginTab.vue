<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server, ServerLoginResult } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import { LOGIN_SCRIPT_SAMPLES } from '@/lib/login-samples'
import {
  LOGIN_SCRIPT_MAX_RUNES,
  insertLoginScriptSample,
  loginScriptPayload,
  loginScriptRuneCount,
  loginScriptValidate,
  type LoginScriptIssue,
} from '@/lib/server-login'
import { loginResultMode } from '@/lib/server-detail'

/**
 * 「一键登录」标签：点击真实执行一次登录脚本，按返回的 url / terminal / output 分派。
 *
 * 纯展示 + 复制 + 脚本草稿；执行、打开新标签、保存脚本（PATCH）都由页面负责
 * （同一个动作只允许一个 owner）。本组件只 emit：
 * - run：执行一次脚本
 * - open-url：重新打开登录地址
 * - save：提交脚本草稿（空串表示清除），校验不过不 emit
 */
const props = defineProps<{
  server: Server
  busy: boolean
  error: string | null
  result: ServerLoginResult | null
  notice: string | null
  /** server.write：没有权限时脚本区只读，保存/清除/示例都不渲染。 */
  canWrite: boolean
  scriptSaving: boolean
  scriptError: string | null
  scriptNotice: string | null
  /** 是否绑定了宝塔面板 API 密钥凭据 */
  panelBound: boolean
  panelBusy: boolean
  panelError: string | null
}>()

const emit = defineEmits<{
  (event: 'run'): void
  (event: 'open-url'): void
  (event: 'save', payload: { login_script: string }): void
  (event: 'open-panel'): void
}>()

const { t } = useI18n()

const mode = computed(() => loginResultMode(props.result))
const configured = computed(() => props.server.login_script.trim() !== '')

const copied = ref<'command' | 'failed' | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | null = null

function flash(value: 'command' | 'failed'): void {
  copied.value = value
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copied.value = null
    copiedTimer = null
  }, 1800)
}

async function copyCommand(): Promise<void> {
  const command = props.result?.terminal ?? ''
  if (command === '') return
  const ok = await writeClipboard(command)
  flash(ok ? 'command' : 'failed')
}

/* 脚本草稿 ---------------------------------------------------------- */

/** 服务端已保存的值；草稿与它不同才算「有未保存改动」。 */
const savedScript = ref(props.server.login_script)
const draft = ref(props.server.login_script)
const issue = ref<LoginScriptIssue | null>(null)
const pendingClear = ref(false)
/** 本次提交的脚本值：非 null 表示「正在等服务端回填」，成功后草稿直接跟随服务端值。 */
const submitted = ref<string | null>(null)
const editor = ref<HTMLTextAreaElement | null>(null)
const editorErrorId = 'login-script-error'

const runeCount = computed(() => loginScriptRuneCount(draft.value))
const overLimit = computed(() => runeCount.value > LOGIN_SCRIPT_MAX_RUNES)
const dirty = computed(() => draft.value !== savedScript.value)
const hasScript = computed(() => savedScript.value.trim() !== '')

const issueText = computed(() => {
  if (issue.value === 'empty') return t('server.login.scriptErrorEmpty')
  if (issue.value === 'tooLong') {
    return t('server.login.scriptErrorTooLong', {
      used: runeCount.value,
      limit: LOGIN_SCRIPT_MAX_RUNES,
    })
  }
  return ''
})

// 换服务器：草稿与服务端值一起重置。
watch(
  () => props.server.id,
  () => {
    savedScript.value = props.server.login_script
    draft.value = props.server.login_script
    issue.value = null
    pendingClear.value = false
    submitted.value = null
  },
)

// 保存成功后页面会把响应合并回 server：刚提交过就以服务端值为准回填草稿；
// 否则只在没有未保存改动时跟随，避免外部刷新覆盖用户正在写的内容。
watch(
  () => props.server.login_script,
  (next) => {
    if (submitted.value !== null || draft.value === savedScript.value) draft.value = next
    submitted.value = null
    savedScript.value = next
    issue.value = null
    pendingClear.value = false
  },
)

function markEdited(): void {
  issue.value = null
  pendingClear.value = false
  submitted.value = null
}

function submit(): void {
  if (!props.canWrite || props.scriptSaving) return
  const found = loginScriptValidate(draft.value)
  issue.value = found
  if (found) {
    // 字段级错误把焦点带回编辑器，键盘用户不用自己找错误位置。
    editor.value?.focus()
    return
  }
  const payload = loginScriptPayload(draft.value)
  submitted.value = payload
  emit('save', { login_script: payload })
}

function beginClear(): void {
  if (!props.canWrite || props.scriptSaving || !hasScript.value) return
  pendingClear.value = true
}

function cancelClear(): void {
  pendingClear.value = false
}

function confirmClear(): void {
  if (!props.canWrite || props.scriptSaving) return
  pendingClear.value = false
  issue.value = null
  // 空串 = 清除；payload 只带这一个键，不能顺手带 notes / custom_fields。
  submitted.value = ''
  emit('save', { login_script: '' })
}

async function insertSample(sample: string): Promise<void> {
  if (!props.canWrite || props.scriptSaving) return
  const element = editor.value
  const result = insertLoginScriptSample(
    draft.value,
    sample,
    element?.selectionStart ?? null,
    element?.selectionEnd ?? null,
  )
  draft.value = result.text
  markEdited()
  await nextTick()
  const target = editor.value
  if (target !== null) {
    target.focus()
    target.setSelectionRange(result.caret, result.caret)
  }
}

onBeforeUnmount(() => {
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = null
})
</script>

<template>
  <div class="ls-detail__stack">
    <section class="ls-card" :aria-label="t('server.login.panelTitle')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="key-round" size="1rem" />
            {{ t('server.login.panelTitle') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.login.panelHint') }}</p>
        </div>
        <div class="ls-card__actions">
          <span v-if="panelBound" class="ls-chip ls-chip--muted">{{ t('server.login.panelBound') }}</span>
          <button
            v-if="panelBound"
            id="server-login-panel"
            class="ls-btn ls-btn--primary ls-btn--small"
            type="button"
            :disabled="panelBusy"
            :aria-busy="panelBusy"
            @click="emit('open-panel')"
          >
            <span v-if="panelBusy" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="key-round" size="0.875rem" />
            {{ panelBusy ? t('server.login.panelOpening') : t('server.login.panelAction') }}
          </button>
        </div>
      </header>
      <p v-if="!panelBound" class="ls-field__hint">
        {{ t('server.login.panelUnbound') }}
        <RouterLink to="/credentials">{{ t('server.login.panelOpenCredentials') }}</RouterLink>
      </p>
      <div v-if="panelError" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__text">{{ panelError }}</p>
      </div>
    </section>

    <section class="ls-card" :aria-label="t('server.login.title')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="play" size="1rem" />
            {{ t('server.login.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.login.hint') }}</p>
        </div>
        <button
          id="server-login-run"
          class="ls-btn ls-btn--primary ls-btn--small"
          type="button"
          :disabled="busy || !configured"
          @click="emit('run')"
        >
          <span v-if="busy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="play" size="0.875rem" />
          {{ busy ? t('server.login.running') : t('server.login.action') }}
        </button>
      </header>

      <p v-if="!configured" class="ls-card__note">
        {{ t('server.login.notConfigured') }} {{ t('server.login.notConfiguredHint') }}
      </p>

      <div v-if="error" class="ls-notice ls-notice--error" role="alert">
        <div class="ls-notice__body">
          <p class="ls-notice__title">{{ t('server.login.failed') }}</p>
          <p class="ls-notice__text">{{ error }}</p>
        </div>
      </div>

      <p v-if="notice" class="ls-ok-hint" role="status">
        <AppIcon name="shield-check" size="0.875rem" />
        {{ notice }}
      </p>
    </section>

    <section v-if="result && mode !== 'empty'" class="ls-card" :aria-label="t('server.login.resultTitle')">
      <header class="ls-card__head">
        <h2 class="ls-card__title">{{ t('server.login.resultTitle') }}</h2>
        <button
          v-if="mode === 'terminal'"
          class="ls-btn ls-btn--small"
          type="button"
          @click="copyCommand"
        >
          <AppIcon name="file-text" size="0.875rem" />
          {{ copied === 'command' ? t('server.login.terminalCopied') : t('server.login.copyCommand') }}
        </button>
      </header>

      <template v-if="mode === 'url'">
        <p class="ls-card__note">{{ t('server.login.urlHint') }}</p>
        <button class="ls-btn ls-btn--small" type="button" @click="emit('open-url')">
          <AppIcon name="chevron-right" size="0.875rem" />
          {{ t('server.login.openUrl') }}
        </button>
      </template>

      <template v-else-if="mode === 'terminal'">
        <p class="ls-card__note">{{ t('server.login.terminalHint') }}</p>
        <pre class="ls-code" tabindex="0">{{ result.terminal }}</pre>
      </template>

      <template v-else>
        <p class="ls-card__note">{{ t('server.login.outputHint') }}</p>
        <pre class="ls-code ls-code--output" tabindex="0">{{ result.output }}</pre>
      </template>

      <p v-if="copied === 'failed'" class="ls-inline-error" role="alert">
        {{ t('server.login.copyFailed') }}
      </p>
    </section>

    <div v-else-if="result && mode === 'empty'" class="ls-notice" role="status">
      <p class="ls-notice__text">{{ t('server.login.resultEmpty') }}</p>
    </div>

    <section class="ls-card" :aria-label="t('server.login.scriptTitle')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="file-text" size="1rem" />
            {{ t('server.login.scriptTitle') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.login.scriptHint') }}</p>
        </div>
      </header>

      <p v-if="!canWrite" class="ls-card__note">{{ t('server.login.scriptReadOnly') }}</p>

      <div class="ls-field">
        <div class="ls-field__row">
          <label class="ls-field__label" for="login-script-editor">
            {{ t('server.login.scriptEdit') }}
          </label>
          <span
            class="ls-field__counter"
            :class="{ 'ls-field__counter--over': overLimit }"
            data-testid="login-script-counter"
          >
            {{ runeCount }} / {{ LOGIN_SCRIPT_MAX_RUNES }}
          </span>
        </div>
        <textarea
          id="login-script-editor"
          ref="editor"
          v-model="draft"
          class="ls-input ls-textarea ls-script-editor"
          :disabled="!canWrite || scriptSaving"
          :placeholder="t('server.login.scriptPlaceholder')"
          :aria-invalid="issue !== null"
          :aria-describedby="issue !== null ? editorErrorId : undefined"
          spellcheck="false"
          rows="10"
          @input="markEdited"
        />
        <p class="ls-field__hint">{{ t('server.login.scriptCountHint') }}</p>
        <p v-if="issue !== null" :id="editorErrorId" class="ls-inline-error" role="alert">
          {{ issueText }}
        </p>
      </div>

      <div v-if="canWrite" class="ls-field">
        <div class="ls-field__row">
          <span class="ls-field__label">{{ t('server.login.sampleLabel') }}</span>
        </div>
        <div class="ls-script-samples">
          <button
            v-for="sample in LOGIN_SCRIPT_SAMPLES"
            :id="`login-script-sample-${sample.key}`"
            :key="sample.key"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="scriptSaving"
            @click="insertSample(sample.script)"
          >
            <AppIcon name="file-text" size="0.875rem" />
            {{ t(sample.labelKey) }}
          </button>
        </div>
        <p class="ls-field__hint">{{ t('server.login.scriptRisk') }}</p>
      </div>

      <div v-if="canWrite" class="ls-script-actions">
        <button
          id="login-script-save"
          class="ls-btn ls-btn--primary ls-btn--small"
          type="button"
          :disabled="scriptSaving || !dirty"
          @click="submit"
        >
          <span v-if="scriptSaving" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="shield-check" size="0.875rem" />
          {{ t('server.login.scriptSave') }}
        </button>

        <template v-if="!pendingClear">
          <button
            id="login-script-clear"
            class="ls-btn ls-btn--small ls-btn--danger"
            type="button"
            :disabled="scriptSaving || !hasScript"
            @click="beginClear"
          >
            {{ t('server.login.scriptClear') }}
          </button>
        </template>
        <template v-else>
          <button
            id="login-script-clear-confirm"
            class="ls-btn ls-btn--small ls-btn--danger"
            type="button"
            :disabled="scriptSaving"
            @click="confirmClear"
          >
            {{ t('server.login.scriptClearConfirm') }}
          </button>
          <button
            id="login-script-clear-cancel"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="scriptSaving"
            @click="cancelClear"
          >
            {{ t('server.login.scriptClearCancel') }}
          </button>
        </template>
      </div>

      <p v-if="pendingClear" class="ls-card__note" role="status">
        {{ t('server.login.scriptClearImpact') }}
      </p>

      <div v-if="scriptError" class="ls-notice ls-notice--error" role="alert">
        <div class="ls-notice__body">
          <p class="ls-notice__title">{{ t('server.login.scriptSaveFailed') }}</p>
          <p class="ls-notice__text">{{ scriptError }}</p>
        </div>
      </div>

      <p v-if="scriptNotice" class="ls-ok-hint" role="status">
        <AppIcon name="shield-check" size="0.875rem" />
        {{ scriptNotice }}
      </p>
    </section>
  </div>
</template>
