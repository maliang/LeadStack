<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import ServerListToolbar from '@/components/servers/ServerListToolbar.vue'
import ServerCard from '@/components/servers/ServerCard.vue'
import ServerRow from '@/components/servers/ServerRow.vue'
import { useCan } from '@/composables/useCan'
import {
  collectGroupOptions,
  collectTagOptions,
  emptyServerFilters,
  filterServers,
  hasActiveServerFilters,
  readServersView,
  useServers,
  writeServersView,
  type ServerFilterState,
  type ServersView,
} from '@/composables/useServers'
import { useI18n } from '@/i18n'

/**
 * 服务器列表页（挂载在 AppShell 的默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是 useServers（加载 + 30s 轮询 + 三态），本页只持筛选条件、
 * 视图偏好与「哪一行展开」三个 UI 状态；筛选是纯前端计算，不新增后端参数。
 */
const { t } = useI18n()
const { can } = useCan()
const { servers, status, error, loaded, refreshing, refresh } = useServers()

const filters = ref<ServerFilterState>(emptyServerFilters())
const viewMode = ref<ServersView>(readServersView())
const expandedId = ref<string | null>(null)

watch(viewMode, (value) => writeServersView(value))

const terminalAllowed = computed(() => can('terminal.use'))
const groups = computed(() => collectGroupOptions(servers.value))
const tags = computed(() => collectTagOptions(servers.value))
const visibleServers = computed(() => filterServers(servers.value, filters.value))
const filtersActive = computed(() => hasActiveServerFilters(filters.value))

function setFilters(value: ServerFilterState): void {
  filters.value = value
}

function resetFilters(): void {
  filters.value = emptyServerFilters()
}

function toggleRow(id: string): void {
  expandedId.value = expandedId.value === id ? null : id
}
</script>

<template>
  <section class="ls-servers">
    <header class="ls-servers__head">
      <div class="ls-servers__heading">
        <h1 class="ls-servers__title">{{ t('servers.title') }}</h1>
        <p class="ls-servers__subtitle">{{ t('servers.subtitle') }}</p>
      </div>
      <button
        id="servers-refresh"
        class="ls-btn ls-servers__refresh"
        type="button"
        :disabled="refreshing"
        @click="refresh"
      >
        <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
        <AppIcon v-else name="refresh" size="0.875rem" />
        {{ t('common.refresh') }}
      </button>
    </header>

    <ServerListToolbar
      :filters="filters"
      :groups="groups"
      :tags="tags"
      :view="viewMode"
      :total="servers.length"
      :filtered="visibleServers.length"
      @update:filters="setFilters"
      @update:view="viewMode = $event"
      @reset="resetFilters"
    />

    <div v-if="error && loaded" class="ls-banner" role="alert">
      <AppIcon name="alert-triangle" size="1rem" />
      <div class="ls-banner__body">
        <p class="ls-banner__title">{{ t('common.error') }}</p>
        <p class="ls-banner__text">{{ error }}</p>
      </div>
      <button class="ls-notice__action" type="button" @click="refresh">
        {{ t('common.retry') }}
      </button>
    </div>

    <div
      v-if="status === 'loading' && !loaded"
      class="ls-servers__skeleton"
      :class="`ls-servers__skeleton--${viewMode}`"
      aria-busy="true"
      :aria-label="t('common.loading')"
    >
      <span v-for="index in 6" :key="index" class="ls-skeleton ls-servers__skeleton-item" />
    </div>

    <div v-else-if="status === 'error' && servers.length === 0" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('common.error') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button class="ls-notice__action" type="button" @click="refresh">
        {{ t('common.retry') }}
      </button>
    </div>

    <div v-else-if="visibleServers.length === 0" class="ls-notice">
      <p class="ls-notice__title">
        {{ filtersActive ? t('servers.noMatches') : t('servers.emptyTitle') }}
      </p>
      <p class="ls-notice__text">
        {{ filtersActive ? t('servers.noMatchesHint') : t('servers.emptyHint') }}
      </p>
      <button v-if="filtersActive" class="ls-notice__action" type="button" @click="resetFilters">
        {{ t('s.0525') }}
      </button>
    </div>

    <div v-else-if="viewMode === 'card'" class="ls-servers__cards">
      <ServerCard v-for="server in visibleServers" :key="server.id" :server="server" />
    </div>

    <ul v-else class="ls-servers__list">
      <ServerRow
        v-for="server in visibleServers"
        :key="server.id"
        :server="server"
        :expanded="expandedId === server.id"
        :terminal-allowed="terminalAllowed"
        @toggle="toggleRow"
      />
    </ul>
  </section>
</template>
