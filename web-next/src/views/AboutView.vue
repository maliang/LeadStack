<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import { api, type SystemInfo } from '@/lib/api'
import { errorMessage } from '@/lib/errors'

/**
 * 关于页：产品定位 + 版本 + 能力清单 + 出口链接。
 *
 * 之前这里是 create-vue 脚手架（「关于 web-next」+ 硬编码中文 + 裸色值），
 * 既不是产品信息，也破坏了 i18n 与设计 token 约定。现在只展示运行时可得的
 * 真实信息（/system/info 的版本），版本读不到时降级为一行可重试提示。
 */
const { t } = useI18n()

const info = ref<SystemInfo | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref<string | null>(null)

const version = computed(() =>
  info.value?.version ? info.value.version : t('about.versionUnavailable'),
)

const capabilities = computed(() => [
  { icon: 'server' as const, key: 'about.capability.servers' },
  { icon: 'pulse' as const, key: 'about.capability.checks' },
  { icon: 'play' as const, key: 'about.capability.deploy' },
  { icon: 'terminal' as const, key: 'about.capability.ops' },
  { icon: 'shield-check' as const, key: 'about.capability.rbac' },
])

async function load(): Promise<void> {
  state.value = 'loading'
  error.value = null
  try {
    info.value = await api.systemInfo()
    state.value = 'ready'
  } catch (caught) {
    error.value = errorMessage(caught, t('about.versionUnavailable'))
    state.value = 'error'
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <section class="ls-adminpage ls-about">
    <header class="ls-adminpage__head">
      <div class="ls-adminpage__heading">
        <h1 class="ls-adminpage__title">{{ t('about.title') }}</h1>
        <p class="ls-adminpage__subtitle">{{ t('about.subtitle') }}</p>
      </div>
      <div class="ls-adminpage__head-actions">
        <RouterLink id="about-servers" class="ls-btn" to="/servers">
          <AppIcon name="server" size="0.875rem" />
          {{ t('about.openServers') }}
        </RouterLink>
        <RouterLink id="about-settings" class="ls-btn ls-btn--primary" to="/settings">
          <AppIcon name="settings" size="0.875rem" />
          {{ t('about.openSettings') }}
        </RouterLink>
      </div>
    </header>

    <section class="ls-card" aria-labelledby="about-version-title">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="about-version-title" class="ls-card__title">{{ t('about.versionTitle') }}</h2>
          <p class="ls-card__subtitle">{{ t('about.versionSubtitle') }}</p>
        </div>
      </header>
      <div class="ls-about__version">
        <span v-if="state === 'loading'" class="ls-spinner" aria-hidden="true" />
        <code v-else id="about-version" class="ls-about__version-value">{{ version }}</code>
        <button
          v-if="state === 'error'"
          class="ls-btn ls-btn--small"
          type="button"
          @click="load()"
        >
          {{ t('common.retry') }}
        </button>
      </div>
      <p v-if="state === 'error' && error" class="ls-inline-error" role="alert">{{ error }}</p>
    </section>

    <section class="ls-card" aria-labelledby="about-capabilities-title">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="about-capabilities-title" class="ls-card__title">
            {{ t('about.capabilitiesTitle') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('about.capabilitiesSubtitle') }}</p>
        </div>
      </header>
      <ul class="ls-about__list">
        <li v-for="item in capabilities" :key="item.key" class="ls-about__item">
          <span class="ls-about__icon" aria-hidden="true">
            <AppIcon :name="item.icon" size="1rem" />
          </span>
          <span class="ls-about__text">{{ t(item.key) }}</span>
        </li>
      </ul>
    </section>

    <p class="ls-about__foot">{{ t('about.footer') }}</p>
  </section>
</template>
