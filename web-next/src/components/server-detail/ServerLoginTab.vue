<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server, ServerLoginResult } from '@/lib/api'
import { writeClipboard } from '@/lib/clipboard'
import { loginResultMode } from '@/lib/server-detail'

/**
 * 「一键登录」标签：点击真实执行一次登录脚本，按返回的 url / terminal / output 分派。
 *
 * 纯展示 + 复制；执行、打开新标签都由页面负责（同一个动作只允许一个 owner）。
 */
const props = defineProps<{
  server: Server
  busy: boolean
  error: string | null
  result: ServerLoginResult | null
  notice: string | null
}>()

const emit = defineEmits<{
  (event: 'run'): void
  (event: 'open-url'): void
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

onBeforeUnmount(() => {
  if (copiedTimer !== null) clearTimeout(copiedTimer)
  copiedTimer = null
})
</script>

<template>
  <div class="ls-detail__stack">
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
  </div>
</template>
