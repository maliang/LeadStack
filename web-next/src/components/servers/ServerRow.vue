<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import OsIcon from '@/components/OsIcon.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatBytes, formatServerOs, serverAddress, serverOsId } from '@/lib/format'
import ServerMetricBar from './ServerMetricBar.vue'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'

/**
 * 列表行：摘要 + 可展开的内嵌终端（左）与文件浏览（右）。
 *
 * 展开状态由页面持有（单行手风琴），本节只发 toggle；
 * 两个入口按钮都指向同一个面板，aria-expanded 与 aria-controls 保持一致。
 * 整行可点进详情页，因此展开按钮必须 stop，避免误跳。
 */
const props = defineProps<{
  server: Server
  expanded: boolean
  terminalAllowed: boolean
  /** 无 server.write 时页面传 false：多选框不渲染 */
  selectable?: boolean
  selected?: boolean
  /** 无 server.write 时页面传 false：编辑按钮不渲染 */
  canEdit?: boolean
  /** 绑定了宝塔面板凭据：显示「宝塔登录」入口 */
  panelAllowed?: boolean
}>()

const emit = defineEmits<{
  (event: 'toggle', id: string): void
  (event: 'select', id: string, selected: boolean): void
  (event: 'edit', id: string): void
  (event: 'open-panel', id: string): void
  (event: 'open-accounts', id: string): void
  (event: 'filter-tag', tag: string): void
}>()

const { t } = useI18n()
const router = useRouter()

function onSelect(event: Event): void {
  emit('select', props.server.id, (event.target as HTMLInputElement).checked)
}

const address = computed(() => (props.server.mode === 'agent' ? props.server.host : serverAddress(props.server)))
const memTotal = computed(() => {
  const value = props.server.host_info?.mem_total_bytes
  return value ? formatBytes(value) : null
})
const diskTotal = computed(() => {
  const value = props.server.host_info?.disk_total_bytes
  return value ? formatBytes(value) : null
})
const memUsed = computed(() => {
  const metrics = props.server.metrics_summary
  if (!metrics) return null
  if (metrics.mem_used_bytes !== null) return formatBytes(metrics.mem_used_bytes)
  return metrics.mem_used_pct === null ? null : `${metrics.mem_used_pct.toFixed(0)}%`
})
const diskUsed = computed(() => {
  const metrics = props.server.metrics_summary
  const total = props.server.host_info?.disk_total_bytes
  if (!metrics) return null
  if (total && metrics.disk_used_pct !== null) return formatBytes(total * metrics.disk_used_pct / 100)
  return metrics.disk_used_pct === null ? null : `${metrics.disk_used_pct.toFixed(0)}%`
})
const osLabel = computed(() => formatServerOs(props.server))
const osId = computed(() => serverOsId(props.server))
const modeLabel = computed(() =>
  props.server.mode === 'agent' && props.server.agent_version
    ? `${props.server.mode} ${props.server.agent_version}`
    : props.server.mode,
)
const detailTarget = computed(() => ({ name: 'server-detail', params: { id: props.server.id } }))

const openDetail = (): void => {
  void router.push(detailTarget.value)
}

/** 整行可点只对「空白/摘要区」生效：行内链接、按钮、多选框与展开面板保持原行为。 */
const INTERACTIVE =
  'a, button, input, select, textarea, label, [role="button"], [role="tab"], .ls-srv-row__panels'
function openDetailFromRow(event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target && target.closest(INTERACTIVE)) return
  openDetail()
}
</script>

<template>
  <li
    class="ls-srv-row"
    :class="{ 'ls-srv-row--expanded': expanded }"
    tabindex="0"
    @click="openDetailFromRow"
    @keydown.enter.self.prevent="openDetail"
  >
    <div class="ls-srv-row__main">
      <div class="ls-srv-row__identity">
        <label v-if="props.selectable" class="ls-srv-row__select">
          <input
            type="checkbox"
            :checked="props.selected"
            :aria-label="t('serversBatch.selectOne', { name: server.name })"
            @change="onSelect"
          />
        </label>
        <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
          <span class="ls-srv-status__dot" aria-hidden="true" />
          {{ t(serverStatusLabelKey(server.status)) }}
        </span>
        <div class="ls-srv-row__name-block">
          <div class="ls-srv-row__name-line">
            <h2 class="ls-srv-row__name" :title="server.name">
              <RouterLink class="ls-srv-row__name-link" :to="detailTarget" @click.stop>{{ server.name }}</RouterLink>
            </h2>
            <button
              v-if="props.canEdit"
              :id="`server-${server.id}-edit`"
              class="ls-btn ls-btn--small ls-srv-row__inline-edit"
              type="button"
              @click.stop.prevent="emit('edit', server.id)"
            >
              <AppIcon name="settings" size="0.75rem" />
              {{ t('common.edit') }}
            </button>
          </div>
          <span class="ls-srv-row__os" :title="osLabel">
            <OsIcon :os-id="osId" size="0.875rem" />
            {{ osLabel }}
          </span>
        </div>
        <span class="ls-srv-row__address" :title="address">{{ address }}</span>
        <span v-if="server.mode" class="ls-chip ls-chip--muted ls-srv-row__mode">{{ modeLabel }}</span>
      </div>

      <div class="ls-srv-row__meta">
        <span v-for="group in server.group_names" :key="group" class="ls-chip" :title="group">
          {{ group }}
        </span>
        <button
          v-for="tag in server.tags"
          :key="tag"
          class="ls-chip ls-chip--muted ls-chip--button"
          type="button"
          :title="`${t('servers.filterByTag')} #${tag}`"
          @click.stop.prevent="emit('filter-tag', tag)"
        >
          {{ tag }}
        </button>
        <span v-if="server.metrics_summary" class="ls-srv-metrics">
          <ServerMetricBar :label="t('servers.metricCpu')" :value="server.metrics_summary.cpu_pct" />
          <ServerMetricBar :label="t('servers.metricMem')" :value="server.metrics_summary.mem_used_pct" :used="memUsed" :total="memTotal" />
          <ServerMetricBar :label="t('servers.metricDisk')" :value="server.metrics_summary.disk_used_pct" :used="diskUsed" :total="diskTotal" />
        </span>
        <span v-else class="ls-srv-row__seen">{{ t('servers.metricsPending') }}</span>
      </div>

      <div class="ls-srv-row__actions">
        <button
          v-if="props.panelAllowed"
          :id="`server-${server.id}-panel`"
          class="ls-btn ls-srv-row__action ls-srv-row__panel"
          type="button"
          @click.stop.prevent="emit('open-panel', server.id)"
        >
          <AppIcon name="key-round" size="0.875rem" />
          {{ t('servers.panelLogin') }}
        </button>
        <button
          :id="`server-${server.id}-accounts`"
          class="ls-btn ls-srv-row__action"
          type="button"
          @click.stop.prevent="emit('open-accounts', server.id)"
        >
          <AppIcon name="users" size="0.875rem" />
          {{ t('servers.accessAccounts') }}
        </button>
      </div>
    </div>

  </li>
</template>
