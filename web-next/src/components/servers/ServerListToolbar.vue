<script setup lang="ts">
import { useI18n } from '@/i18n'
import type { ServerStatus } from '@/lib/api'
import {
  hasActiveServerFilters,
  type ServerFilterState,
  type ServersView,
} from '@/composables/useServers'

/**
 * 服务器列表筛选栏：分组 / 状态 / 标签 / 关键字 + 卡片⇄列表切换。
 *
 * 纯展示 + 受控输入：筛选条件由页面持有（唯一 owner），这里只回传新对象，
 * 不在组件内部再存一份，避免两处状态不同步。视图切换同样受控。
 */
const props = defineProps<{
  filters: ServerFilterState
  groups: string[]
  tags: string[]
  view: ServersView
  total: number
  filtered: number
}>()

const emit = defineEmits<{
  (event: 'update:filters', value: ServerFilterState): void
  (event: 'update:view', value: ServersView): void
  (event: 'reset'): void
}>()

const { t } = useI18n()

const statusOptions: { value: '' | ServerStatus; labelKey: string }[] = [
  { value: '', labelKey: 's.0520' },
  { value: 'online', labelKey: 's.0521' },
  { value: 'offline', labelKey: 's.0522' },
  { value: 'unknown', labelKey: 's.0089' },
]

function patch(partial: Partial<ServerFilterState>): void {
  emit('update:filters', { ...props.filters, ...partial })
}

function selectStatus(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  patch({ status: value === 'online' || value === 'offline' || value === 'unknown' ? value : '' })
}
</script>

<template>
  <div class="ls-servers-toolbar">
    <div class="ls-servers-toolbar__filters">
      <select
        id="servers-filter-group"
        class="ls-input ls-servers-toolbar__select"
        :aria-label="t('s.0517')"
        :value="filters.group"
        @change="patch({ group: ($event.target as HTMLSelectElement).value })"
      >
        <option value="">{{ t('s.0376') }}</option>
        <option v-for="group in groups" :key="group" :value="group">{{ group }}</option>
      </select>

      <select
        id="servers-filter-status"
        class="ls-input ls-servers-toolbar__select"
        :aria-label="t('s.0519')"
        :value="filters.status"
        @change="selectStatus"
      >
        <option v-for="option in statusOptions" :key="option.value" :value="option.value">
          {{ t(option.labelKey) }}
        </option>
      </select>

      <select
        id="servers-filter-tag"
        class="ls-input ls-servers-toolbar__select"
        :aria-label="t('s.0518')"
        :value="filters.tag"
        @change="patch({ tag: ($event.target as HTMLSelectElement).value })"
      >
        <option value="">{{ t('s.0378') }}</option>
        <option v-for="tag in tags" :key="tag" :value="tag">{{ tag }}</option>
      </select>

      <input
        id="servers-search-input"
        class="ls-input ls-servers-toolbar__search"
        type="search"
        autocomplete="off"
        :placeholder="t('servers.searchPlaceholder')"
        :aria-label="t('common.search')"
        :value="filters.keyword"
        @input="patch({ keyword: ($event.target as HTMLInputElement).value })"
      />

      <button
        v-if="hasActiveServerFilters(filters)"
        id="servers-filter-reset"
        class="ls-btn ls-servers-toolbar__reset"
        type="button"
        @click="emit('reset')"
      >
        {{ t('s.0525') }}
      </button>
    </div>

    <div class="ls-servers-toolbar__end">
      <span class="ls-servers-toolbar__count" aria-live="polite">
        {{ t('servers.matched', { filtered, total }) }}
      </span>
      <div class="ls-servers-toolbar__views" role="group" :aria-label="t('servers.viewGroup')">
        <button
          id="servers-view-card"
          class="ls-servers-toolbar__view"
          type="button"
          :aria-pressed="view === 'card'"
          @click="emit('update:view', 'card')"
        >
          {{ t('servers.viewCard') }}
        </button>
        <button
          id="servers-view-list"
          class="ls-servers-toolbar__view"
          type="button"
          :aria-pressed="view === 'list'"
          @click="emit('update:view', 'list')"
        >
          {{ t('servers.viewList') }}
        </button>
      </div>
    </div>
  </div>
</template>
