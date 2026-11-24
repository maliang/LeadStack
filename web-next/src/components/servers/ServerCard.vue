<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatBytes, formatOs, serverAddress } from '@/lib/format'
import ServerMetricRing from './ServerMetricRing.vue'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'

/** 卡片视图：一眼回答「这台机器是谁、通不通、在哪」。展开终端/文件只在列表视图提供。 */
const props = withDefaults(
  defineProps<{
    server: Server
    /** 无 server.write 时页面传 false：多选框不渲染 */
    selectable?: boolean
    selected?: boolean
    /** 无 server.write 时页面传 false：编辑按钮不渲染 */
    canEdit?: boolean
    /** 绑定了宝塔面板凭据：显示「宝塔登录」入口 */
    panelAllowed?: boolean
  }>(),
  { selectable: false, selected: false, canEdit: false, panelAllowed: false },
)

const emit = defineEmits<{
  (event: 'select', id: string, selected: boolean): void
  (event: 'edit', id: string): void
  (event: 'open-panel', id: string): void
  (event: 'open-accounts', id: string): void
  (event: 'filter-tag', tag: string): void
}>()

const { t } = useI18n()

function onSelect(event: Event): void {
  emit('select', props.server.id, (event.target as HTMLInputElement).checked)
}

const titleId = computed(() => `server-card-${props.server.id}-title`)
const detailTarget = computed(() => ({ name: 'server-detail', params: { id: props.server.id } }))

const groupLabels = computed(() => {
  if (props.server.group_names.length > 0) return props.server.group_names
  return props.server.group ? [props.server.group] : []
})
const mode = computed(() => props.server.mode.trim())
const address = computed(() => (props.server.mode === 'agent' ? props.server.host : serverAddress(props.server)))
const memTotal = computed(() => formatBytes(props.server.host_info?.mem_total_bytes ?? null))
const diskTotal = computed(() => formatBytes(props.server.host_info?.disk_total_bytes ?? null))
</script>

<template>
  <article class="ls-srv-card" :aria-labelledby="titleId">
    <header class="ls-srv-card__head">
      <label v-if="props.selectable" class="ls-srv-card__select">
        <input
          type="checkbox"
          :checked="props.selected"
          :aria-label="t('serversBatch.selectOne', { name: server.name })"
          @change="onSelect"
        />
      </label>
      <h2 :id="titleId" class="ls-srv-card__title" :title="server.name">
        <RouterLink class="ls-srv-card__title-link" :to="detailTarget">{{ server.name }}</RouterLink>
      </h2>
      <button
        v-if="props.canEdit"
        :id="`server-card-${server.id}-edit`"
        class="ls-btn ls-btn--small ls-srv-card__inline-edit"
        type="button"
        @click="emit('edit', server.id)"
      >
        <AppIcon name="settings" size="0.75rem" />
        {{ t('common.edit') }}
      </button>
      <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
        <span class="ls-srv-status__dot" aria-hidden="true" />
        {{ t(serverStatusLabelKey(server.status)) }}
      </span>
    </header>

    <p class="ls-srv-card__address" :title="address">
      <AppIcon name="server" size="0.875rem" />
      <span class="ls-srv-card__address-text">{{ address }}</span>
      <span v-if="mode" class="ls-chip ls-chip--muted ls-srv-card__mode">{{ mode }}</span>
    </p>

    <dl class="ls-srv-meta">
      <div class="ls-srv-meta__item">
        <dt>{{ t('s.0534') }}</dt>
        <dd :title="formatOs(server.os, server.arch)">{{ formatOs(server.os, server.arch) }}</dd>
      </div>
    </dl>

    <div v-if="server.metrics_summary" class="ls-srv-metrics ls-srv-card__metrics">
      <ServerMetricRing :label="t('servers.metricCpu')" :value="server.metrics_summary.cpu_pct" />
      <ServerMetricRing :label="t('servers.metricMem')" :value="server.metrics_summary.mem_used_pct" :total="memTotal" />
      <ServerMetricRing :label="t('servers.metricDisk')" :value="server.metrics_summary.disk_used_pct" :total="diskTotal" />
    </div>
    <p v-else class="ls-srv-card__metrics ls-field__hint">{{ t('servers.metricsPending') }}</p>

    <div v-if="groupLabels.length > 0 || server.tags.length > 0" class="ls-srv-card__chips">
      <span v-for="group in groupLabels" :key="group" class="ls-chip" :title="group">{{ group }}</span>
      <button
        v-for="tag in server.tags"
        :key="tag"
        class="ls-chip ls-chip--muted ls-chip--button"
        type="button"
        :title="`${t('servers.filterByTag')} #${tag}`"
        @click="emit('filter-tag', tag)"
      >
        #{{ tag }}
      </button>
    </div>

    <div class="ls-srv-card__foot">
      <button
        v-if="props.panelAllowed"
        :id="`server-card-${server.id}-panel`"
        class="ls-btn ls-btn--small"
        type="button"
        @click="emit('open-panel', server.id)"
      >
        <AppIcon name="key-round" size="0.875rem" />
        {{ t('servers.panelLogin') }}
      </button>
      <button
        :id="`server-card-${server.id}-accounts`"
        class="ls-btn ls-btn--small"
        type="button"
        @click="emit('open-accounts', server.id)"
      >
        <AppIcon name="users" size="0.875rem" />
        {{ t('servers.accessAccounts') }}
      </button>
      <RouterLink class="ls-btn ls-btn--small" :to="detailTarget">{{ t('servers.viewDetail') }}</RouterLink>
    </div>
  </article>
</template>
