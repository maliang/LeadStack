<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import CheckDetailDrawer from '@/components/checks/CheckDetailDrawer.vue'
import CheckFormDialog from '@/components/checks/CheckFormDialog.vue'
import ChecksRow from '@/components/checks/ChecksRow.vue'
import { useCan } from '@/composables/useCan'
import { useChecks } from '@/composables/useChecks'
import { useI18n } from '@/i18n'
import { api, type CheckItem, type CheckState } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import {
  CHECKS_PAGE_SIZE,
  checkKindLabel,
  clampPage,
  collectKindOptions,
  emptyCheckFilters,
  filterChecks,
  hasActiveCheckFilters,
  pageCount,
  paginate,
  runItemText,
  runSummaryText,
  serverNameIndex,
  type CheckFilterState,
} from '@/lib/checks'

/**
 * 检测列表页（挂载在 AppShell 的默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是 useChecks（列表 + 10s 轮询 + 三态 + 立即执行/启用开关），
 * 本页只持「筛选条件 / 页码 / 哪一行开了抽屉」三个 UI 状态。
 *
 * 权限：无 check.read 时整页只渲染提示、**不发任何请求**（useChecks 的 enabled 闸）；
 * 有 check.read 但没有 check.write 时行内操作全部禁用（后端同样会拒）。
 */
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('check.read'))
const canWrite = computed(() => can('check.write'))

const {
  checks,
  state,
  error,
  loaded,
  refreshing,
  actionError,
  runningId,
  togglingId,
  lastRun,
  refresh,
  runCheck,
  setEnabled,
  clearActionError,
} = useChecks({ enabled: () => canRead.value })

const filters = ref<CheckFilterState>(emptyCheckFilters())
const page = ref(1)
const drawerId = ref<string | null>(null)
/** 抽屉关闭后把焦点还给这一行的「详情」按钮 */
let drawerTrigger: HTMLElement | null = null

const kindOptions = computed(() => collectKindOptions(checks.value))
const resolveServerName = computed(() => serverNameIndex(checks.value))
const visibleChecks = computed(() =>
  filterChecks(checks.value, filters.value, (id) => resolveServerName.value(id)),
)
const filtersActive = computed(() => hasActiveCheckFilters(filters.value))
const pages = computed(() => pageCount(visibleChecks.value.length, CHECKS_PAGE_SIZE))
const pageChecks = computed(() => paginate(visibleChecks.value, page.value, CHECKS_PAGE_SIZE))
const agentProbeCount = computed(
  () => checks.value.filter((check) => check.params?.probe_via === 'agent').length,
)
const drawerCheck = computed<CheckItem | null>(
  () => checks.value.find((check) => check.id === drawerId.value) ?? null,
)

const stateOptions: { value: '' | CheckState; labelKey: string }[] = [
  { value: '', labelKey: 'checks.filterState' },
  { value: 'up', labelKey: 's.0317' },
  { value: 'down', labelKey: 's.0318' },
  { value: 'unknown', labelKey: 's.0089' },
]

// 筛选变化后页码回到第一页，避免停在一个已被筛空的页码上
watch(filters, () => {
  page.value = 1
}, { deep: true })

// 数据或筛选变化导致总页数变小时，把页码夹回合法范围（不留空白页）
watch(visibleChecks, () => {
  page.value = clampPage(page.value, visibleChecks.value.length, CHECKS_PAGE_SIZE)
})

function patchFilters(partial: Partial<CheckFilterState>): void {
  filters.value = { ...filters.value, ...partial }
}

function resetFilters(): void {
  filters.value = emptyCheckFilters()
}

function selectState(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  patchFilters({ state: value === 'up' || value === 'down' || value === 'unknown' ? value : '' })
}

async function toggle(check: CheckItem, enabled: boolean): Promise<void> {
  await setEnabled(check, enabled)
}

async function run(check: CheckItem): Promise<void> {
  await runCheck(check)
}

async function runDrawerCheck(): Promise<void> {
  const check = drawerCheck.value
  if (check) await runCheck(check)
}

function openDetail(check: CheckItem): void {
  drawerTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  drawerId.value = check.id
}

function closeDrawer(): void {
  drawerId.value = null
  void nextTick(() => {
    drawerTrigger?.focus()
    drawerTrigger = null
  })
}

/* ---------------- 新建 / 编辑 / 删除 ---------------- */

const formOpen = ref(false)
const formTarget = ref<CheckItem | null>(null)
const notice = ref<string | null>(null)

const deleteTarget = ref<CheckItem | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function openCreateForm(): void {
  if (!canWrite.value) return
  formTarget.value = null
  notice.value = null
  formOpen.value = true
}

function openEditForm(check: CheckItem): void {
  if (!canWrite.value) return
  formTarget.value = check
  notice.value = null
  formOpen.value = true
}

function closeForm(): void {
  formOpen.value = false
  formTarget.value = null
}

async function handleFormSaved(saved: CheckItem): Promise<void> {
  const edited = formTarget.value !== null
  formOpen.value = false
  formTarget.value = null
  notice.value = t(edited ? 'checks.form.saved' : 'checks.form.created', { name: saved.name })
  await refresh()
}

function askDelete(check: CheckItem): void {
  if (!canWrite.value) return
  deleteTarget.value = check
  deleteError.value = null
}

function closeDelete(): void {
  if (deleteBusy.value) return
  deleteTarget.value = null
  deleteError.value = null
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleteBusy.value) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    await api.deleteCheck(target.id)
    deleteTarget.value = null
    notice.value = t('checks.form.deleted', { name: target.name })
    if (drawerId.value === target.id) closeDrawer()
    await refresh()
  } catch (caught) {
    deleteError.value = errorMessage(caught, t('checks.form.deleteFailed'))
  } finally {
    deleteBusy.value = false
  }
}

function dismissRunFeedback(): void {
  lastRun.value = null
}
</script>

<template>
  <section class="ls-checkspage">
    <div v-if="!canRead" id="checks-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('checks.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('checks.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-checkspage__head">
        <div class="ls-checkspage__heading">
          <h1 class="ls-checkspage__title">{{ t('checks.title') }}</h1>
          <p class="ls-checkspage__subtitle">{{ t('checks.subtitle') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="checks-refresh"
            class="ls-btn ls-checkspage__refresh"
            type="button"
            :disabled="refreshing"
            @click="refresh"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button
            v-if="canWrite"
            id="checks-create"
            class="ls-btn ls-btn--primary"
            type="button"
            @click="openCreateForm"
          >
            <AppIcon name="plus" size="0.875rem" />
            {{ t('checks.form.createAction') }}
          </button>
        </div>
      </header>

      <div v-if="notice" id="checks-action-notice" class="ls-notice ls-notice--success" role="status">
        <p class="ls-notice__title">{{ notice }}</p>
        <button class="ls-notice__action" type="button" @click="notice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div class="ls-checkspage__toolbar">
        <div class="ls-checkspage__filters">
          <input
            id="checks-search-input"
            class="ls-input ls-checkspage__search"
            type="search"
            autocomplete="off"
            :placeholder="t('checks.searchPlaceholder')"
            :aria-label="t('common.search')"
            :value="filters.keyword"
            @input="patchFilters({ keyword: ($event.target as HTMLInputElement).value })"
          />

          <AppSelect
            id="checks-filter-kind"
            class="ls-input ls-checkspage__select"
            :aria-label="t('checks.colKind')"
            :value="filters.kind"
            @change="patchFilters({ kind: ($event.target as HTMLSelectElement).value })"
          >
            <option value="">{{ t('checks.filterKind') }}</option>
            <option v-for="kind in kindOptions" :key="kind" :value="kind">
              {{ checkKindLabel(kind) }}
            </option>
          </AppSelect>

          <AppSelect
            id="checks-filter-state"
            class="ls-input ls-checkspage__select"
            :aria-label="t('checks.colState')"
            :value="filters.state"
            @change="selectState"
          >
            <option v-for="option in stateOptions" :key="option.value" :value="option.value">
              {{ t(option.labelKey) }}
            </option>
          </AppSelect>

          <button
            v-if="filtersActive"
            id="checks-filter-reset"
            class="ls-btn ls-checkspage__reset"
            type="button"
            @click="resetFilters"
          >
            {{ t('checks.resetFilters') }}
          </button>
        </div>

        <span class="ls-checkspage__count" aria-live="polite">
          {{ t('checks.matched', { filtered: visibleChecks.length, total: checks.length }) }}
        </span>
      </div>

      <p v-if="agentProbeCount > 0" id="checks-agent-probe-hint" class="ls-checkspage__hint">
        {{ t('checks.agentProbeHint', { count: agentProbeCount }) }}
      </p>

      <div v-if="error && loaded" id="checks-banner-error" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-if="actionError" id="checks-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="clearActionError">
          {{ t('checks.dismiss') }}
        </button>
      </div>

      <div v-if="lastRun" id="checks-run-feedback" class="ls-notice" role="status">
        <p class="ls-notice__title">{{ t('checks.runFeedbackTitle') }}</p>
        <p class="ls-notice__text">{{ runSummaryText(lastRun) }}</p>
        <ul class="ls-checkspage__run-items">
          <li v-for="item in lastRun.results" :key="item.server_id" class="ls-checkspage__run-item">
            <span class="ls-checkspage__run-server">{{ resolveServerName(item.server_id) }}</span>
            <span class="ls-checkspage__run-text">{{ runItemText(item) }}</span>
          </li>
        </ul>
        <button class="ls-notice__action" type="button" @click="dismissRunFeedback">
          {{ t('checks.dismiss') }}
        </button>
      </div>

      <div
        v-if="state === 'loading' && !loaded"
        id="checks-skeleton"
        class="ls-checkspage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 6" :key="index" class="ls-skeleton ls-checkspage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && checks.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="checks-retry" class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="checks.length === 0" id="checks-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('checks.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('checks.emptyHint') }}</p>
        <button
          v-if="canWrite"
          id="checks-empty-create"
          class="ls-notice__action"
          type="button"
          @click="openCreateForm"
        >
          {{ t('checks.form.createAction') }}
        </button>
      </div>

      <div v-else-if="visibleChecks.length === 0" id="checks-empty-filtered" class="ls-notice">
        <p class="ls-notice__title">{{ t('checks.noMatches') }}</p>
        <p class="ls-notice__text">{{ t('checks.noMatchesHint') }}</p>
        <button class="ls-notice__action" type="button" @click="resetFilters">
          {{ t('checks.resetFilters') }}
        </button>
      </div>

      <template v-else>
        <div class="ls-checkspage__scroll">
          <table class="ls-checkspage__table">
            <caption class="ls-visually-hidden">{{ t('checks.title') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('checks.colName') }}</th>
                <th scope="col">{{ t('checks.colKind') }}</th>
                <th scope="col">{{ t('checks.colTarget') }}</th>
                <th scope="col">{{ t('checks.colInterval') }}</th>
                <th scope="col">{{ t('checks.colEnabled') }}</th>
                <th scope="col">{{ t('checks.colState') }}</th>
                <th scope="col">{{ t('checks.colActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <ChecksRow
                v-for="check in pageChecks"
                :key="check.id"
                :check="check"
                :can-write="canWrite"
                :running="runningId === check.id"
                :toggling="togglingId === check.id"
                :Enabled="check.enabled"
                @run="run(check)"
                @toggle="toggle(check, $event)"
                @detail="openDetail(check)"
                @edit="openEditForm(check)"
                @delete="askDelete(check)"
              />
            </tbody>
          </table>
        </div>

        <nav v-if="pages > 1" class="ls-checkspage__pager" :aria-label="t('checks.title')">
          <button
            id="checks-prev-page"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="page <= 1"
            :aria-label="t('checks.pagerPrev')"
            @click="page = clampPage(page - 1, visibleChecks.length, CHECKS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
          <span id="checks-pager-info" class="ls-checkspage__pager-info">
            {{ t('checks.pagerInfo', { page, pages }) }}
          </span>
          <button
            id="checks-next-page"
            class="ls-btn ls-btn--small ls-checkspage__next"
            type="button"
            :disabled="page >= pages"
            :aria-label="t('checks.pagerNext')"
            @click="page = clampPage(page + 1, visibleChecks.length, CHECKS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
        </nav>
      </template>

      <p class="ls-checkspage__note">{{ t('s.0968') }}</p>
    </template>

    <CheckDetailDrawer
      v-if="drawerCheck"
      :check="drawerCheck"
      :can-write="canWrite"
      :running="runningId === drawerCheck.id"
      @close="closeDrawer"
      @run="runDrawerCheck"
    />

    <CheckFormDialog
      v-if="formOpen"
      :check="formTarget"
      @close="closeForm"
      @saved="handleFormSaved"
    />

    <AppModal
      v-if="deleteTarget"
      :title="t('checks.form.deleteTitle')"
      :description="deleteTarget.name"
      tone="danger"
      size="sm"
      @close="closeDelete"
    >
      <p class="ls-notice__text">{{ t('checks.form.deleteImpact') }}</p>
      <p v-if="deleteError" class="ls-adminpage__form-error" role="alert">{{ deleteError }}</p>
      <template #footer>
        <button id="check-delete-cancel" class="ls-btn" type="button" :disabled="deleteBusy" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="check-delete-confirm"
          class="ls-btn ls-btn--danger"
          type="button"
          :disabled="deleteBusy"
          :aria-busy="deleteBusy"
          @click="confirmDelete"
        >
          {{ deleteBusy ? t('common.loading') : t('common.delete') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
