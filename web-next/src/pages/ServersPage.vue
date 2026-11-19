<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import ServerBatchBar from '@/components/servers/ServerBatchBar.vue'
import ServerBatchResultDialog from '@/components/servers/ServerBatchResultDialog.vue'
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
import { api, type BatchAction, type BatchResultItem } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import {
  CUSTOM_GROUP_VALUE,
  MAX_BATCH_TAG_LENGTH,
  MAX_BATCH_TAGS,
  allVisibleSelected,
  batchActionDialogKey,
  buildBatchPayload,
  buildServerNameMap,
  emptyBatchDraft,
  isDangerousBatchAction,
  mergeBatchResult,
  pruneSelection,
  retryPayload,
  selectAllVisible,
  someVisibleSelected,
  validateBatchDraft,
  type BatchDraft,
} from '@/lib/server-batch'

/**
 * 服务器列表页（挂载在 AppShell 的默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是 useServers（加载 + 30s 轮询 + 三态），本页只持筛选条件、
 * 视图偏好与「哪一行展开」三个 UI 状态；筛选是纯前端计算，不新增后端参数。
 * 批量操作的选中集、表单草稿与结果列表也由本页持有，映射规则在 lib/server-batch.ts。
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

/* ---------------- 批量操作：选中集 ---------------- */

const canWrite = computed(() => can('server.write'))
const selectedIds = ref<string[]>([])
const visibleIds = computed(() => visibleServers.value.map((server) => server.id))
const allSelected = computed(() => allVisibleSelected(selectedIds.value, visibleIds.value))
const someSelected = computed(() => someVisibleSelected(selectedIds.value, visibleIds.value))
const selectedServers = computed(() =>
  servers.value.filter((server) => selectedIds.value.includes(server.id)),
)

// 筛选/刷新后丢掉已不可见的选中项；权限丢失时直接清空
watch(visibleIds, (ids) => {
  const next = pruneSelection(selectedIds.value, ids)
  if (next.length !== selectedIds.value.length) selectedIds.value = next
})
watch(canWrite, (allowed) => {
  if (!allowed) selectedIds.value = []
})

function onSelect(id: string, checked: boolean): void {
  const already = selectedIds.value.includes(id)
  if (checked === already) return
  selectedIds.value = checked
    ? [...selectedIds.value, id]
    : selectedIds.value.filter((item) => item !== id)
}

function toggleSelectAll(event: Event): void {
  const checked = (event.target as HTMLInputElement).checked
  if (checked) {
    selectedIds.value = selectAllVisible(selectedIds.value, visibleIds.value)
    return
  }
  const visible = new Set(visibleIds.value)
  selectedIds.value = selectedIds.value.filter((id) => !visible.has(id))
}

function clearSelection(): void {
  selectedIds.value = []
}

/* ---------------- 批量操作：参数与提交 ---------------- */

const batchAction = ref<BatchAction | null>(null)
const batchDraft = reactive<BatchDraft>(emptyBatchDraft())
const batchError = ref<string | null>(null)
const submitting = ref(false)

const batchDialogTitle = computed(() => {
  const action = batchAction.value
  if (action === null) return ''
  // delete.title 带 {count}，标题也要一起插值，否则会原样显示占位符
  return t(batchActionDialogKey(action, 'title'), { count: selectedIds.value.length })
})
const batchDialogDesc = computed(() =>
  batchAction.value === null
    ? ''
    : t(batchActionDialogKey(batchAction.value, 'desc'), { count: selectedIds.value.length }),
)
const batchConfirmLabel = computed(() => {
  const action = batchAction.value
  if (action === null) return ''
  const vars = { count: selectedIds.value.length }
  return isDangerousBatchAction(action)
    ? t(batchActionDialogKey(action, 'confirm'), vars)
    : t(batchActionDialogKey(action, 'confirm'))
})
const batchIsDangerous = computed(
  () => batchAction.value !== null && isDangerousBatchAction(batchAction.value),
)
const showTagsField = computed(() => batchAction.value === 'add-tags')
const showGroupField = computed(() => batchAction.value === 'set-group')
const showDeleteConfirm = computed(() => batchAction.value === 'delete')
const useCustomGroup = computed(() => batchDraft.group === CUSTOM_GROUP_VALUE)

function openBatchDialog(action: BatchAction): void {
  if (!canWrite.value || selectedIds.value.length === 0) return
  batchAction.value = action
  batchDraft.tagsText = ''
  batchDraft.group = groups.value[0] ?? CUSTOM_GROUP_VALUE
  batchDraft.customGroup = ''
  batchError.value = null
}

function closeBatchDialog(): void {
  if (submitting.value) return
  batchAction.value = null
  batchError.value = null
}

/* ---------------- 批量操作：结果与单台重试 ---------------- */

const resultOpen = ref(false)
const batchResults = ref<BatchResultItem[]>([])
// 结果弹窗只在 resultOpen 为 true 时挂载；动作与参数保留快照供「重试」复用
const batchResultAction = ref<BatchAction>('test-connection')
const batchResultParams = ref<Record<string, unknown> | undefined>(undefined)
const batchResultNames = ref<Record<string, string>>({})
const retriedIds = ref<string[]>([])
const retryingId = ref<string | null>(null)

async function submitBatch(): Promise<void> {
  const action = batchAction.value
  if (action === null || submitting.value) return
  const invalid = validateBatchDraft(selectedIds.value, action, batchDraft)
  if (invalid !== null) {
    batchError.value = t(invalid.key, invalid.vars)
    return
  }
  const ids = [...selectedIds.value]
  const payload = buildBatchPayload(ids, action, batchDraft)
  submitting.value = true
  batchError.value = null
  try {
    const response = await api.batch(payload)
    batchResults.value = Array.isArray(response.results) ? response.results : []
    batchResultAction.value = action
    batchResultParams.value = payload.params
    batchResultNames.value = buildServerNameMap(
      servers.value.filter((server) => ids.includes(server.id)),
    )
    retriedIds.value = []
    retryingId.value = null
    resultOpen.value = true
    batchAction.value = null
    selectedIds.value = []
    await refresh()
  } catch (caught) {
    batchError.value = errorMessage(caught, t('serversBatch.error.requestFailed'))
  } finally {
    submitting.value = false
  }
}

async function retryBatchItem(id: string): Promise<void> {
  if (!resultOpen.value || retriedIds.value.includes(id) || retryingId.value !== null) return
  const action = batchResultAction.value
  retryingId.value = id
  try {
    const response = await api.batch(retryPayload(id, action, batchResultParams.value))
    const next =
      response.results.find((item) => item.id === id) ??
      ({ id, ok: false, error: t('serversBatch.result.noError') } satisfies BatchResultItem)
    batchResults.value = mergeBatchResult(batchResults.value, next)
    if (next.ok) void refresh()
  } catch (caught) {
    batchResults.value = mergeBatchResult(batchResults.value, {
      id,
      ok: false,
      error: errorMessage(caught, t('serversBatch.error.retryFailed')),
    })
  } finally {
    retriedIds.value = [...retriedIds.value, id]
    retryingId.value = null
  }
}

function closeBatchResult(): void {
  resultOpen.value = false
  batchResults.value = []
  batchResultParams.value = undefined
  retriedIds.value = []
  retryingId.value = null
}

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

    <div v-if="canWrite && visibleServers.length > 0" class="ls-servers__selection">
      <label class="ls-servers__select-all">
        <input
          type="checkbox"
          :checked="allSelected"
          :indeterminate="someSelected"
          @change="toggleSelectAll"
        />
        <span>{{ t('serversBatch.selectAll', { count: visibleServers.length }) }}</span>
      </label>
    </div>

    <ServerBatchBar
      v-if="canWrite && selectedIds.length > 0"
      :count="selectedIds.length"
      :busy="submitting"
      @action="openBatchDialog"
      @clear="clearSelection"
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
      <ServerCard
        v-for="server in visibleServers"
        :key="server.id"
        :server="server"
        :selectable="canWrite"
        :selected="selectedIds.includes(server.id)"
        @select="onSelect"
      />
    </div>

    <ul v-else class="ls-servers__list">
      <ServerRow
        v-for="server in visibleServers"
        :key="server.id"
        :server="server"
        :expanded="expandedId === server.id"
        :terminal-allowed="terminalAllowed"
        :selectable="canWrite"
        :selected="selectedIds.includes(server.id)"
        @toggle="toggleRow"
        @select="onSelect"
      />
    </ul>

    <AppModal
      v-if="batchAction !== null"
      size="sm"
      :title="batchDialogTitle"
      :description="batchDialogDesc"
      @close="closeBatchDialog"
    >
      <form id="servers-batch-form" class="ls-batch-form" novalidate @submit.prevent="submitBatch">
        <div v-if="showTagsField" class="ls-field">
          <label class="ls-field__label" for="servers-batch-tags">
            {{ t('serversBatch.dialog.addTags.label') }}
          </label>
          <input
            id="servers-batch-tags"
            v-model="batchDraft.tagsText"
            class="ls-input"
            type="text"
            autocomplete="off"
            :placeholder="t('serversBatch.dialog.addTags.placeholder')"
            :disabled="submitting"
            data-autofocus
          />
          <p class="ls-field__hint">
            {{
              t('serversBatch.dialog.addTags.hint', {
                max: MAX_BATCH_TAGS,
                len: MAX_BATCH_TAG_LENGTH,
              })
            }}
          </p>
        </div>

        <div v-else-if="showGroupField" class="ls-field">
          <label class="ls-field__label" for="servers-batch-group">
            {{ t('serversBatch.dialog.setGroup.label') }}
          </label>
          <select
            id="servers-batch-group"
            v-model="batchDraft.group"
            class="ls-input"
            :disabled="submitting"
          >
            <option v-for="group in groups" :key="group" :value="group">{{ group }}</option>
            <option :value="CUSTOM_GROUP_VALUE">
              {{ t('serversBatch.dialog.setGroup.custom') }}
            </option>
          </select>
          <template v-if="useCustomGroup">
            <label class="ls-field__label" for="servers-batch-group-custom">
              {{ t('serversBatch.dialog.setGroup.newLabel') }}
            </label>
            <input
              id="servers-batch-group-custom"
              v-model="batchDraft.customGroup"
              class="ls-input"
              type="text"
              autocomplete="off"
              :placeholder="t('serversBatch.dialog.setGroup.placeholder')"
              :disabled="submitting"
              data-autofocus
            />
          </template>
        </div>

        <template v-else-if="showDeleteConfirm">
          <p class="ls-batch-form__warning">
            {{ t('serversBatch.dialog.delete.warning', { count: selectedIds.length }) }}
          </p>
          <div class="ls-batch-form__targets">
            <p class="ls-batch-form__targets-title">{{ t('serversBatch.dialog.targetsTitle') }}</p>
            <ul class="ls-batch-form__target-list">
              <li v-for="server in selectedServers" :key="server.id" :title="server.name">
                {{ server.name }}
              </li>
            </ul>
          </div>
        </template>

        <div v-if="batchError" class="ls-banner ls-banner--warning" role="alert">
          <AppIcon name="alert-triangle" size="1rem" />
          <div class="ls-banner__body">
            <p class="ls-banner__text">{{ batchError }}</p>
          </div>
        </div>
      </form>

      <template #footer>
        <button class="ls-btn" type="button" :disabled="submitting" @click="closeBatchDialog">
          {{ t('serversBatch.cancel') }}
        </button>
        <button
          class="ls-btn"
          :class="batchIsDangerous ? 'ls-btn--danger' : 'ls-btn--primary'"
          type="submit"
          form="servers-batch-form"
          :disabled="submitting"
        >
          <span v-if="submitting" class="ls-spinner" aria-hidden="true" />
          {{ submitting ? t('serversBatch.submitting') : batchConfirmLabel }}
        </button>
      </template>
    </AppModal>

    <ServerBatchResultDialog
      v-if="resultOpen"
      :results="batchResults"
      :action="batchResultAction"
      :names="batchResultNames"
      :retried-ids="retriedIds"
      :retrying-id="retryingId"
      @retry="retryBatchItem"
      @close="closeBatchResult"
    />
  </section>
</template>

<style scoped>
/* 批量选择行：与工具栏同宽，窄宽下整行换行而不是横向溢出 */
.ls-servers__selection {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--ls-space-2);
  min-width: 0;
}

.ls-servers__select-all {
  display: inline-flex;
  align-items: center;
  gap: var(--ls-space-2);
  min-width: 0;
  min-height: 1.75rem;
  color: var(--ls-text-muted);
  font: 500 var(--ls-text-xs) / 1.4 var(--ls-font-sans);
  cursor: pointer;
}

.ls-servers__select-all input {
  flex: none;
  width: 1rem;
  height: 1rem;
  margin: 0;
  accent-color: var(--ls-primary-fill);
  cursor: pointer;
}

.ls-servers__select-all span {
  overflow-wrap: anywhere;
}

/* 批量参数弹窗：目标列表内滚，避免长名单把弹窗撑出屏幕 */
.ls-batch-form {
  display: flex;
  flex-direction: column;
  gap: var(--ls-space-3);
  min-width: 0;
}

.ls-batch-form__warning {
  margin: 0;
  padding: var(--ls-space-2) var(--ls-space-3);
  border-radius: var(--ls-radius-md);
  background: var(--ls-danger-soft);
  color: var(--ls-danger);
  font: 500 var(--ls-text-sm) / 1.5 var(--ls-font-sans);
}

.ls-batch-form__targets {
  display: flex;
  flex-direction: column;
  gap: var(--ls-space-1);
  min-width: 0;
}

.ls-batch-form__targets-title {
  margin: 0;
  color: var(--ls-text-subtle);
  font: 500 var(--ls-text-2xs) / 1.4 var(--ls-font-sans);
}

.ls-batch-form__target-list {
  max-height: 10rem;
  margin: 0;
  padding-inline-start: var(--ls-space-4);
  overflow-y: auto;
}

.ls-batch-form__target-list li {
  overflow-wrap: anywhere;
  color: var(--ls-text);
  font: 400 var(--ls-text-xs) / 1.6 var(--ls-font-sans);
}
</style>
