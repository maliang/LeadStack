<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatDateTime, formatOs, formatRelativeTime, serverAddress } from '@/lib/format'

/**
 * 「概览」标签：这台机器是谁、通不通、跑什么版本。
 *
 * 纯展示：数据与异步状态全部由页面持有，这里只派发「打开面板」。
 * 面板入口只在服务器绑定了 panel_credential_id 时出现（未绑定时后端返回 409，按钮是噪音）。
 */
const props = defineProps<{
  server: Server
  panelBusy: boolean
  panelError: string | null
}>()

const emit = defineEmits<{ (event: 'open-panel'): void }>()

const { t } = useI18n()

const hasPanel = computed(() => (props.server.panel_credential_id ?? '') !== '')
const lastSeen = computed(() =>
  props.server.last_seen_at ? formatRelativeTime(props.server.last_seen_at) : t('s.0543'),
)
const modeLabel = computed(() => (props.server.mode === 'agent' ? t('s.1095') : t('s.0036')))
const createdAt = computed(() =>
  props.server.created_at ? formatDateTime(props.server.created_at) : t('common.na'),
)
const groupLabels = computed(() => {
  if (props.server.group_names.length > 0) return props.server.group_names
  return props.server.group ? [props.server.group] : []
})
</script>

<template>
  <div class="ls-detail__stack">
    <div v-if="server.last_error" class="ls-banner ls-banner--warning" role="status">
      <AppIcon name="alert-triangle" size="1rem" />
      <div class="ls-banner__body">
        <p class="ls-banner__title">{{ t('s.1092') }}</p>
        <p class="ls-banner__text">{{ server.last_error }}</p>
      </div>
    </div>

    <section class="ls-card" :aria-label="t('s.1093')">
      <header class="ls-card__head">
        <h2 class="ls-card__title">{{ t('s.1093') }}</h2>
        <button
          v-if="hasPanel"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="panelBusy"
          @click="emit('open-panel')"
        >
          <span v-if="panelBusy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="archive" size="0.875rem" />
          {{ panelBusy ? t('server.panel.opening') : t('server.panel.open') }}
        </button>
      </header>

      <p v-if="panelError" class="ls-inline-error" role="alert">{{ panelError }}</p>

      <dl class="ls-kv">
        <div class="ls-kv__row">
          <dt>{{ t('s.0066') }}</dt>
          <dd class="ls-kv__value--mono" :title="serverAddress(server)">{{ serverAddress(server) }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0533') }}</dt>
          <dd>
            <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
              <span class="ls-srv-status__dot" aria-hidden="true" />
              {{ t(serverStatusLabelKey(server.status)) }}
            </span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1094') }}</dt>
          <dd>{{ modeLabel }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0538') }}</dt>
          <dd :title="lastSeen">{{ lastSeen }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1096') }}</dt>
          <dd :title="formatOs(server.os, server.arch)">{{ formatOs(server.os, server.arch) }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1097') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.kernel ?? ''">
            {{ server.kernel || t('common.na') }}
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1098') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.agent_version ?? ''">
            {{ server.agent_version || t('common.na') }}
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0062') }}</dt>
          <dd>
            <span v-if="groupLabels.length > 0" class="ls-chip-group">
              <span v-for="group in groupLabels" :key="group" class="ls-chip" :title="group">
                {{ group }}
              </span>
            </span>
            <span v-else>{{ t('common.na') }}</span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0075') }}</dt>
          <dd>
            <span v-if="server.tags.length > 0" class="ls-chip-group">
              <span v-for="tag in server.tags" :key="tag" class="ls-chip ls-chip--muted" :title="`#${tag}`">
                #{{ tag }}
              </span>
            </span>
            <span v-else>{{ t('common.na') }}</span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0271') }}</dt>
          <dd>{{ createdAt }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1099') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.id">{{ server.id }}</dd>
        </div>
      </dl>
    </section>
  </div>
</template>
