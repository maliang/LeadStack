<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useCan } from '@/composables/useCan'
import {
  emptyServerFilters,
  filterServers,
  serverStatusLabelKey,
  serverStatusTone,
  useServers,
} from '@/composables/useServers'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatRelativeTime } from '@/lib/format'
import type { ServerToolKind } from '@/router'

/**
 * 终端 / 文件 / 日志三个入口页的共用实现（对应旧版 web/src/pages/ServerToolsPage.tsx）。
 *
 * 路由通过 meta.toolKind 传 kind，本页不复制三份：
 * - 数据 owner 是 useServers（加载 + 轮询 + 三态），筛选沿用同一套纯函数；
 * - 权限：无 terminal.use 时整页只渲染说明，且 useServers 的 enabled 闸保证**零请求**；
 * - 点行不是页内切换，而是深链 `#/servers/<id>?tab=<kind>`，跳详情页对应面板。
 */
const TITLE_KEYS: Record<ServerToolKind, string> = {
  terminal: 'tools.terminalTitle',
  files: 'tools.filesTitle',
  logs: 'tools.logsTitle',
}

const DESC_KEYS: Record<ServerToolKind, string> = {
  terminal: 'tools.terminalDescription',
  files: 'tools.filesDescription',
  logs: 'tools.logsDescription',
}

/** 路由 meta 的 kind 归一化：未知值回 terminal，避免空白页。 */
function normalizeToolKind(raw: unknown): ServerToolKind {
  const value = Array.isArray(raw) ? raw[0] : raw
  return value === 'terminal' || value === 'files' || value === 'logs' ? value : 'terminal'
}

/** 深链目标（与旧版 `/servers/${id}?tab=${kind}` 对齐，走命名路由避免手拼查询串）。 */
function toolDetailTarget(
  kind: ServerToolKind,
  id: string,
): { name: 'server-detail'; params: { id: string }; query: { tab: ServerToolKind } } {
  return { name: 'server-detail', params: { id }, query: { tab: kind } }
}

const route = useRoute()
const { t } = useI18n()
const { can } = useCan()

const kind = computed<ServerToolKind>(() => normalizeToolKind(route.meta.toolKind))
const titleKey = computed(() => TITLE_KEYS[kind.value])
const descKey = computed(() => DESC_KEYS[kind.value])
const canUse = computed(() => can('terminal.use'))

const { servers, status, error, loaded, refreshing, refresh } = useServers({
  enabled: () => canUse.value,
})

const keyword = ref('')
const filters = computed(() => ({ ...emptyServerFilters(), keyword: keyword.value }))
const visibleServers = computed(() => filterServers(servers.value, filters.value))

function lastSeenText(server: Server): string {
  return server.last_seen_at ? formatRelativeTime(server.last_seen_at) : t('s.0543')
}
</script>

<template>
  <section class="ls-toolspage" :data-tool-kind="kind">
    <div v-if="!canUse" id="tools-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('tools.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('tools.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-toolspage__head">
        <div class="ls-toolspage__heading">
          <h1 class="ls-toolspage__title">{{ t(titleKey) }}</h1>
          <p class="ls-toolspage__subtitle">{{ t(descKey) }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="tools-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing"
            :aria-busy="refreshing"
            @click="refresh"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </header>

      <div class="ls-toolspage__toolbar">
        <input
          id="tools-search-input"
          class="ls-input ls-toolspage__search"
          type="search"
          autocomplete="off"
          :placeholder="t('tools.searchPlaceholder')"
          :aria-label="t('common.search')"
          :value="keyword"
          @input="keyword = ($event.target as HTMLInputElement).value"
        />
        <span v-if="loaded && servers.length > 0" class="ls-toolspage__count" aria-live="polite">
          {{ t('tools.count', { shown: visibleServers.length, total: servers.length }) }}
        </span>
      </div>

      <div v-if="error && loaded" id="tools-error-banner" class="ls-banner" role="alert">
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button id="tools-retry" class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div
        v-if="status === 'loading' && !loaded"
        class="ls-toolspage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 4" :key="index" class="ls-skeleton ls-toolspage__skeleton-row" />
      </div>

      <div
        v-else-if="status === 'error' && !loaded"
        id="tools-error"
        class="ls-notice ls-notice--error"
        role="alert"
      >
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="tools-retry" class="ls-btn" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="servers.length === 0" id="tools-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('tools.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('tools.emptyDescription') }}</p>
        <RouterLink id="tools-add-server" class="ls-btn ls-btn--primary" to="/servers">
          {{ t('tools.addServer') }}
        </RouterLink>
      </div>

      <div v-else-if="visibleServers.length === 0" id="tools-no-match" class="ls-notice">
        <p class="ls-notice__title">{{ t('tools.noMatchTitle') }}</p>
        <p class="ls-notice__text">{{ t('tools.noMatchHint') }}</p>
        <button class="ls-btn" type="button" @click="keyword = ''">
          {{ t('tools.clearSearch') }}
        </button>
      </div>

      <ul v-else class="ls-toolspage__list">
        <li v-for="server in visibleServers" :key="server.id">
          <RouterLink
            class="ls-toolspage__row"
            :to="toolDetailTarget(kind, server.id)"
            :data-server-id="server.id"
          >
            <span class="ls-toolspage__row-main">
              <span class="ls-toolspage__name" :title="server.name">{{ server.name }}</span>
              <span class="ls-toolspage__host" :title="server.host">{{ server.host }}</span>
            </span>
            <span class="ls-toolspage__meta">
              <span v-if="server.mode" class="ls-chip ls-chip--muted">{{ server.mode }}</span>
              <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
                <span class="ls-srv-status__dot" aria-hidden="true" />
                {{ t(serverStatusLabelKey(server.status)) }}
              </span>
              <span class="ls-toolspage__seen">{{ lastSeenText(server) }}</span>
            </span>
          </RouterLink>
        </li>
      </ul>
    </template>
  </section>
</template>
