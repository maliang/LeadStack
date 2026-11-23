<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import { formatOs, formatRelativeTime, serverAddress } from '@/lib/format'
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
  }>(),
  { selectable: false, selected: false, canEdit: false },
)

const emit = defineEmits<{
  (event: 'select', id: string, selected: boolean): void
  (event: 'edit', id: string): void
}>()

const { t } = useI18n()

function onSelect(event: Event): void {
  emit('select', props.server.id, (event.target as HTMLInputElement).checked)
}

const titleId = computed(() => `server-card-${props.server.id}-title`)
const detailTarget = computed(() => ({ name: 'server-detail', params: { id: props.server.id } }))
const lastSeen = computed(() =>
  props.server.last_seen_at ? formatRelativeTime(props.server.last_seen_at) : t('s.0543'),
)
const groupLabels = computed(() => {
  if (props.server.group_names.length > 0) return props.server.group_names
  return props.server.group ? [props.server.group] : []
})
const mode = computed(() => props.server.mode.trim())
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
      <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
        <span class="ls-srv-status__dot" aria-hidden="true" />
        {{ t(serverStatusLabelKey(server.status)) }}
      </span>
    </header>

    <p class="ls-srv-card__address" :title="serverAddress(server)">
      <AppIcon name="server" size="0.875rem" />
      <span class="ls-srv-card__address-text">{{ serverAddress(server) }}</span>
    </p>

    <dl class="ls-srv-meta">
      <div class="ls-srv-meta__item">
        <dt>{{ t('s.0534') }}</dt>
        <dd :title="formatOs(server.os, server.arch)">{{ formatOs(server.os, server.arch) }}</dd>
      </div>
      <div class="ls-srv-meta__item">
        <dt>{{ t('s.0538') }}</dt>
        <dd>{{ lastSeen }}</dd>
      </div>
    </dl>

    <div v-if="mode || groupLabels.length > 0 || server.tags.length > 0" class="ls-srv-card__chips">
      <span v-if="mode" class="ls-chip ls-chip--muted">{{ mode }}</span>
      <span v-for="group in groupLabels" :key="group" class="ls-chip" :title="group">{{ group }}</span>
      <span v-for="tag in server.tags" :key="tag" class="ls-chip ls-chip--muted" :title="`#${tag}`">
        #{{ tag }}
      </span>
    </div>

    <div class="ls-srv-card__foot">
      <button
        v-if="props.canEdit"
        :id="`server-card-${server.id}-edit`"
        class="ls-btn ls-btn--small"
        type="button"
        @click="emit('edit', server.id)"
      >
        <AppIcon name="settings" size="0.875rem" />
        {{ t('common.edit') }}
      </button>
      <RouterLink class="ls-btn ls-btn--small" :to="detailTarget">{{ t('servers.viewDetail') }}</RouterLink>
    </div>
  </article>
</template>
