<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import ServerBatchBar from '@/components/servers/ServerBatchBar.vue'
import ServerBatchResultDialog from '@/components/servers/ServerBatchResultDialog.vue'
import ServerListToolbar from '@/components/servers/ServerListToolbar.vue'
import ServerCard from '@/components/servers/ServerCard.vue'
import ServerAccountsDialog from '@/components/servers/ServerAccountsDialog.vue'
import ServerFormDialog from '@/components/servers/ServerFormDialog.vue'
import ServerGroupManagerDialog from '@/components/servers/ServerGroupManagerDialog.vue'
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
import { api, type BatchAction, type BatchResultItem, type Server, type ServerGroup } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import {
  CUSTOM_GROUP_VALUE,
  MAX_BATCH_TAG_LENGTH,
  MAX_BATCH_TAGS,
  batchActionDialogKey,
  buildBatchPayload,
  buildServerNameMap,
  emptyBatchDraft,
  isDangerousBatchAction,
  mergeBatchResult,
  pruneSelection,
  retryPayload,
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
const serverGroups = ref<ServerGroup[]>([])
const groupManagerOpen = ref(false)

const groups = computed(() => {
  const names = new Set(collectGroupOptions(servers.value))
  for (const group of serverGroups.value) names.add(group.name)
  return [...names].sort((a, b) => a.localeCompare(b))
})
const tags = computed(() => collectTagOptions(servers.value))
const visibleServers = computed(() => filterServers(servers.value, filters.value))
const filtersActive = computed(() => hasActiveServerFilters(filters.value))

/* ---------------- 批量操作：选中集 ---------------- */

const canWrite = computed(() => can('server.write'))
const selectedIds = ref<string[]>([])
const visibleIds = computed(() => visibleServers.value.map((server) => server.id))
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

/* ---------------- 新增 / 编辑服务器 ---------------- */

const formOpen = ref(false)
const formTarget = ref<Server | null>(null)
const actionNotice = ref<string | null>(null)

function openCreateForm(): void {
  if (!canWrite.value) return
  formTarget.value = null
  actionNotice.value = null
  formOpen.value = true
}

const accountsTarget = ref<Server | null>(null)
const onboardId = ref<string | null>(null)

function filterByTag(tag: string): void {
  const next = filters.value.tags.includes(tag)
    ? filters.value.tags.filter((item) => item !== tag)
    : [...filters.value.tags, tag]
  filters.value = { ...filters.value, tags: next }
}

function openAccounts(id: string): void {
  accountsTarget.value = servers.value.find((server) => server.id === id) ?? null
}

function closeAccounts(): void {
  accountsTarget.value = null
}

async function openEditForm(id: string): Promise<void> {
  const target = servers.value.find((server) => server.id === id)
  if (!target || !canWrite.value) return
  actionNotice.value = null
  try {
    // 列表接口不含 notes/custom_fields；编辑必须取详情，避免保存时把档案清空。
    formTarget.value = await api.server(id)
    formOpen.value = true
  } catch (caught) {
    actionNotice.value = errorMessage(caught, t('server.detail.loadFailed'))
  }
}

/* ---------------- 宝塔面板一键登录 ---------------- */

const panelBusyId = ref<string | null>(null)
const panelError = ref<string | null>(null)

async function openPanel(id: string): Promise<void> {
  if (panelBusyId.value !== null) return
  panelBusyId.value = id
  panelError.value = null
  // 必须在点击事件内同步开窗，否则 await 之后会被浏览器当成弹窗拦截。
  const win = window.open('about:blank', '_blank')
  if (!win) {
    panelError.value = t('server.login.urlBlocked')
    panelBusyId.value = null
    return
  }
  win.opener = null
  try {
    const login = await api.serverPanelLogin(id)
    if (!win.closed) win.location.replace(login.url)
  } catch (caught) {
    win.close()
    panelError.value = `${t('server.panel.failed')}${errorMessage(caught, '')}`.trim()
  } finally {
    panelBusyId.value = null
  }
}

async function loadServerGroups(): Promise<void> {
  try {
    serverGroups.value = await api.serverGroups()
  } catch {
    // 分组列表只用于筛选/下拉建议；失败不阻断服务器列表。
    serverGroups.value = []
  }
}

onMounted(() => {
  void loadServerGroups()
})

async function handleGroupsChanged(): Promise<void> {
  await loadServerGroups()
  await refresh()
}

function closeForm(): void {
  formOpen.value = false
  formTarget.value = null
}

async function handleFormSaved(saved: Server): Promise<void> {
  const edited = formTarget.value !== null
  formOpen.value = false
  formTarget.value = null
  const message = t(edited ? 'server.form.saved' : 'server.form.created', { name: saved.name })
  actionNotice.value = message
  onboardId.value = edited ? null : saved.id
  await refresh()
}
</script>

<template>
  <section class="ls-servers">
    <header class="ls-servers__head">
      <div class="ls-servers__heading">
        <h1 class="ls-servers__title">{{ t('servers.title') }}</h1>
        <p class="ls-servers__subtitle">{{ t('servers.subtitle') }}</p>
      </div>
      <div class="ls-servers__head-actions">
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
        <button
          id="servers-groups"
          class="ls-btn"
          type="button"
          @click="groupManagerOpen = true"
        >
          <AppIcon name="folder" size="0.875rem" />
          {{ t('server.group.manager.title') }}
        </button>
        <button
          v-if="canWrite"
          id="servers-create"
          class="ls-btn ls-btn--primary"
          type="button"
          @click="openCreateForm"
        >
          <AppIcon name="plus" size="0.875rem" />
          {{ t('server.form.createAction') }}
        </button>
      </div>
    </header>

    <div v-if="onboardId" id="servers-onboard-banner" class="ls-banner ls-banner--warning" role="status">
      <AppIcon name="info" size="1rem" />
      <div class="ls-banner__body">
        <p class="ls-banner__title">{{ t('servers.onboardBannerTitle') }}</p>
        <p class="ls-banner__text">{{ t('servers.onboardBannerText') }}</p>
      </div>
      <RouterLink class="ls-btn ls-btn--small" :to="{ name: 'server-detail', params: { id: onboardId }, query: { tab: 'overview' } }">
        {{ t('servers.onboardBannerAction') }}
      </RouterLink>
    </div>

    <div v-if="actionNotice" id="servers-action-notice" class="ls-notice ls-notice--success" role="status">
      <p class="ls-notice__title">{{ actionNotice }}</p>
      <button class="ls-notice__action" type="button" @click="actionNotice = null">
        {{ t('common.close') }}
      </button>
    </div>

    <div v-if="panelError" id="servers-panel-error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('server.panel.open') }}</p>
      <p class="ls-notice__text">{{ panelError }}</p>
      <button class="ls-notice__action" type="button" @click="panelError = null">
        {{ t('common.close') }}
      </button>
    </div>

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

    <div v-if="tags.length > 0" class="ls-servers__selection">
      <div v-if="tags.length > 0" class="ls-servers__tag-filters" role="group" :aria-label="t('servers.filterByTag')">
        <button
          v-for="tag in tags"
          :key="tag"
          class="ls-chip ls-chip--button"
          :class="{ 'ls-chip--muted': !filters.tags.includes(tag), 'ls-chip--active': filters.tags.includes(tag) }"
          type="button"
          :aria-pressed="filters.tags.includes(tag)"
          @click="filterByTag(tag)"
        >
          {{ tag }}
        </button>
      </div>
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
      <button
        v-else-if="canWrite"
        id="servers-empty-create"
        class="ls-notice__action"
        type="button"
        @click="openCreateForm"
      >
        {{ t('server.form.createAction') }}
      </button>
    </div>

    <div v-else-if="viewMode === 'card'" class="ls-servers__cards">
      <ServerCard
        v-for="server in visibleServers"
        :key="server.id"
        :server="server"
        :can-edit="canWrite"
        :panel-allowed="Boolean(server.panel_credential_id)"
        @edit="openEditForm"
        @open-panel="openPanel"
        @open-accounts="openAccounts"
        @filter-tag="filterByTag"
      />
    </div>

    <ul v-else class="ls-servers__list">
      <ServerRow
        v-for="server in visibleServers"
        :key="server.id"
        :server="server"
        :expanded="expandedId === server.id"
        :terminal-allowed="terminalAllowed"
        :can-edit="canWrite"
        :panel-allowed="Boolean(server.panel_credential_id)"
        @toggle="toggleRow"
        @edit="openEditForm"
        @open-panel="openPanel"
        @open-accounts="openAccounts"
        @filter-tag="filterByTag"
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
          <AppSelect
            id="servers-batch-group"
            v-model="batchDraft.group"
            class="ls-input"
            :disabled="submitting"
          >
            <option v-for="group in groups" :key="group" :value="group">{{ group }}</option>
            <option :value="CUSTOM_GROUP_VALUE">
              {{ t('serversBatch.dialog.setGroup.custom') }}
            </option>
          </AppSelect>
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

    <ServerAccountsDialog
      v-if="accountsTarget"
      :server="accountsTarget"
      :can-write="canWrite"
      @close="closeAccounts"
    />

    <ServerFormDialog
      v-if="formOpen"
      :server="formTarget"
      @close="closeForm"
      @saved="handleFormSaved"
      @local-created="refresh()"
    />

    <ServerGroupManagerDialog
      v-if="groupManagerOpen"
      @close="groupManagerOpen = false"
      @changed="handleGroupsChanged"
    />

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
