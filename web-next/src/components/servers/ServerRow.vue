<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatRelativeTime, serverAddress } from '@/lib/format'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'
import ServerTerminalPanel from '@/components/server-tools/ServerTerminalPanel.vue'
import ServerFilesPanel from '@/components/server-tools/ServerFilesPanel.vue'

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
}>()

const { t } = useI18n()
const router = useRouter()

function onSelect(event: Event): void {
  emit('select', props.server.id, (event.target as HTMLInputElement).checked)
}

const panelsId = computed(() => `server-panels-${props.server.id}`)
const address = computed(() => serverAddress(props.server))
const detailTarget = computed(() => ({ name: 'server-detail', params: { id: props.server.id } }))
const lastSeen = computed(() =>
  props.server.last_seen_at ? formatRelativeTime(props.server.last_seen_at) : t('s.0543'),
)
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
        <h2 class="ls-srv-row__name" :title="server.name">
          <RouterLink class="ls-srv-row__name-link" :to="detailTarget" @click.stop>{{ server.name }}</RouterLink>
        </h2>
        <span class="ls-srv-row__address" :title="address">{{ address }}</span>
      </div>

      <div class="ls-srv-row__meta">
        <span v-if="server.mode" class="ls-chip ls-chip--muted">{{ server.mode }}</span>
        <span v-for="group in server.group_names" :key="group" class="ls-chip" :title="group">
          {{ group }}
        </span>
        <span v-for="tag in server.tags" :key="tag" class="ls-chip ls-chip--muted" :title="`#${tag}`">
          #{{ tag }}
        </span>
        <span class="ls-srv-row__seen">{{ t('s.0538') }} · {{ lastSeen }}</span>
      </div>

      <div class="ls-srv-row__actions">
        <button
          :id="`server-${server.id}-terminal`"
          class="ls-btn ls-srv-row__action"
          type="button"
          :aria-expanded="expanded"
          :aria-controls="panelsId"
          @click.stop.prevent="emit('toggle', server.id)"
        >
          <AppIcon name="terminal" size="0.875rem" />
          {{ t('servers.expandTerminal') }}
        </button>
        <button
          :id="`server-${server.id}-files`"
          class="ls-btn ls-srv-row__action"
          type="button"
          :aria-expanded="expanded"
          :aria-controls="panelsId"
          @click.stop.prevent="emit('toggle', server.id)"
        >
          <AppIcon name="folder" size="0.875rem" />
          {{ t('servers.expandFiles') }}
        </button>
        <button
          v-if="props.panelAllowed"
          :id="`server-${server.id}-panel`"
          class="ls-btn ls-srv-row__action ls-srv-row__panel"
          type="button"
          @click.stop.prevent="emit('open-panel', server.id)"
        >
          <AppIcon name="key-round" size="0.875rem" />
          {{ t('server.panel.open') }}
        </button>
        <button
          v-if="props.canEdit"
          :id="`server-${server.id}-edit`"
          class="ls-btn ls-srv-row__action"
          type="button"
          @click.stop.prevent="emit('edit', server.id)"
        >
          <AppIcon name="settings" size="0.875rem" />
          {{ t('common.edit') }}
        </button>
      </div>
    </div>

    <div v-if="expanded" :id="panelsId" class="ls-srv-row__panels">
      <section class="ls-srv-panel" :aria-label="`${server.name} · ${t('nav.terminal')}`">
        <h3 class="ls-srv-panel__title">
          <AppIcon name="terminal" size="0.875rem" />
          {{ t('nav.terminal') }}
        </h3>
        <ServerTerminalPanel :server-id="server.id" :allowed="terminalAllowed" />
      </section>

      <section class="ls-srv-panel" :aria-label="`${server.name} · ${t('nav.files')}`">
        <h3 class="ls-srv-panel__title">
          <AppIcon name="folder" size="0.875rem" />
          {{ t('nav.files') }}
        </h3>
        <ServerFilesPanel :server-id="server.id" :allowed="terminalAllowed" />
      </section>
    </div>
  </li>
</template>
