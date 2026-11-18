<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AlertDetailDrawer from '@/components/alerts/AlertDetailDrawer.vue'
import { useAlerts } from '@/composables/useAlerts'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  ALERTS_PAGE_SIZE,
  ALERT_SEVERITIES,
  ALERT_STATUSES,
  RULE_LEVEL_FILTER,
  alertTime,
  clampPage,
  collectServerOptions,
  emptyAlertFilters,
  filterAlerts,
  hasActiveAlertFilters,
  paginate,
  pageCount,
  serverText,
  severityLabelKey,
  severityTone,
  statusLabelKey,
  statusTone,
  isRuleLevel,
  ruleText,
  type AlertFilterState,
} from '@/lib/alerts'
import type { AlertItem } from '@/lib/api'

/**
 * 告警列表页（挂载在 AppShell 的默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是 useAlerts（列表 + 10s 轮询 + 三态 + 行内确认），
 * 本页只持「筛选条件 / 页码 / 哪一条开了抽屉」三个 UI 状态；
 * 筛选是纯前端计算，不新增后端参数。
 *
 * 权限：无 alert.read 时整页只渲染提示、**不发任何请求**（useAlerts 的 enabled 闸）；
 * 有 alert.read 但没有 alert.write 时行内「确认」全部禁用（后端同样会拒）。
 *
 * 文案：级别/状态/事件的映射全部集中在 lib/alerts.ts 的纯函数里
 * （alert.severity.* / alert.status.* / alert.event.*），页面只调用，不各写一套。
 */
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('alert.read'))
const canWrite = computed(() => can('alert.write'))

const {
  alerts,
  state,
  error,
  loaded,
  refreshing,
  actionError,
  ackingId,
  refresh,
  ack,
  clearActionError,
} = useAlerts({ enabled: () => canRead.value })

const filters = ref<AlertFilterState>(emptyAlertFilters())
const page = ref(1)
const drawerId = ref<string | null>(null)
/** 抽屉关闭后把焦点还给这一行的「详情」按钮 */
let drawerTrigger: HTMLElement | null = null
const now = ref(Date.now())
const clock = setInterval(() => {
  now.value = Date.now()
}, 1_000)

onBeforeUnmount(() => clearInterval(clock))

const serverOptions = computed(() => collectServerOptions(alerts.value))
const visibleAlerts = computed(() => filterAlerts(alerts.value, filters.value))
const filtersActive = computed(() => hasActiveAlertFilters(filters.value))
const pages = computed(() => pageCount(visibleAlerts.value.length, ALERTS_PAGE_SIZE))
const pageAlerts = computed(() => paginate(visibleAlerts.value, page.value, ALERTS_PAGE_SIZE))
const drawerAlert = computed<AlertItem | null>(
  () => visibleAlerts.value.find((alert) => alert.id === drawerId.value) ?? null,
)

// 筛选变化后页码回到第一页，避免停在一个已被筛空的页码上
watch(
  filters,
  () => {
    page.value = 1
  },
  { deep: true },
)

// 数据或筛选变化导致总页数变小时，把页码夹回合法范围（不留空白页）
watch(visibleAlerts, () => {
  page.value = clampPage(page.value, visibleAlerts.value.length, ALERTS_PAGE_SIZE)
})

function patchFilters(partial: Partial<AlertFilterState>): void {
  filters.value = { ...filters.value, ...partial }
}

function resetFilters(): void {
  filters.value = emptyAlertFilters()
}

function selectStatus(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  patchFilters({
    status: ALERT_STATUSES.includes(value as AlertItem['status']) ? (value as AlertItem['status']) : '',
  })
}

function selectSeverity(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  patchFilters({
    severity: ALERT_SEVERITIES.includes(value as AlertItem['severity'])
      ? (value as AlertItem['severity'])
      : '',
  })
}

function selectServer(event: Event): void {
  patchFilters({ server: (event.target as HTMLSelectElement).value })
}

function rowTime(alert: AlertItem): { absolute: string; relative: string } {
  return alertTime(alert.started_at, now.value)
}

function ackButtonText(alert: AlertItem): string {
  return alert.status === 'firing' ? t('alerts.ack') : t(statusLabelKey(alert.status))
}

function canAck(alert: AlertItem): boolean {
  return canWrite.value && alert.status === 'firing' && ackingId.value === null
}

async function runAck(alert: AlertItem): Promise<void> {
  if (!canAck(alert)) return
  await ack(alert)
}

function openDetail(alert: AlertItem): void {
  drawerTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  drawerId.value = alert.id
}

function closeDrawer(): void {
  drawerId.value = null
  void nextTick(() => {
    drawerTrigger?.focus()
    drawerTrigger = null
  })
}
</script>

<template>
  <section class="ls-alertspage">
    <div v-if="!canRead" id="alerts-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('alerts.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('alerts.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-alertspage__head">
        <div class="ls-alertspage__heading">
          <h1 class="ls-alertspage__title">{{ t('alerts.title') }}</h1>
          <p class="ls-alertspage__subtitle">{{ t('alerts.subtitle') }}</p>
        </div>
        <button
          id="alerts-refresh"
          class="ls-btn ls-alertspage__refresh"
          type="button"
          :disabled="refreshing"
          @click="refresh"
        >
          <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
      </header>

      <div class="ls-alertspage__toolbar">
        <div class="ls-alertspage__filters">
          <select
            id="alerts-filter-status"
            class="ls-input ls-alertspage__select"
            :aria-label="t('alerts.colStatus')"
            :value="filters.status"
            @change="selectStatus"
          >
            <option value="">{{ t('alerts.allStatus') }}</option>
            <option v-for="status in ALERT_STATUSES" :key="status" :value="status">
              {{ t(statusLabelKey(status)) }}
            </option>
          </select>

          <select
            id="alerts-filter-severity"
            class="ls-input ls-alertspage__select"
            :aria-label="t('alerts.colSeverity')"
            :value="filters.severity"
            @change="selectSeverity"
          >
            <option value="">{{ t('alerts.allSeverity') }}</option>
            <option v-for="severity in ALERT_SEVERITIES" :key="severity" :value="severity">
              {{ t(severityLabelKey(severity)) }}
            </option>
          </select>

          <select
            id="alerts-filter-server"
            class="ls-input ls-alertspage__select"
            :aria-label="t('alerts.colServer')"
            :value="filters.server"
            @change="selectServer"
          >
            <option value="">{{ t('alerts.allServers') }}</option>
            <option :value="RULE_LEVEL_FILTER">{{ t('alerts.ruleLevel') }}</option>
            <option v-for="server in serverOptions" :key="server.id" :value="server.id">
              {{ server.label }}
            </option>
          </select>

          <button
            v-if="filtersActive"
            id="alerts-filter-reset"
            class="ls-btn ls-alertspage__reset"
            type="button"
            @click="resetFilters"
          >
            {{ t('alerts.resetFilters') }}
          </button>
        </div>

        <span id="alerts-count" class="ls-alertspage__count" aria-live="polite">
          {{ t('alerts.matched', { filtered: visibleAlerts.length, total: alerts.length }) }}
        </span>
      </div>

      <div v-if="error && loaded" id="alerts-banner-error" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-if="actionError" id="alerts-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="clearActionError">
          {{ t('alerts.dismiss') }}
        </button>
      </div>

      <div
        v-if="state === 'loading' && !loaded"
        id="alerts-skeleton"
        class="ls-alertspage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 6" :key="index" class="ls-skeleton ls-alertspage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && alerts.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="alerts-retry" class="ls-notice__action" type="button" @click="refresh">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="alerts.length === 0" id="alerts-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('alerts.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('alerts.emptyHint') }}</p>
      </div>

      <div v-else-if="visibleAlerts.length === 0" id="alerts-empty-filtered" class="ls-notice">
        <p class="ls-notice__title">{{ t('alerts.noMatches') }}</p>
        <p class="ls-notice__text">{{ t('alerts.noMatchesHint') }}</p>
        <button class="ls-notice__action" type="button" @click="resetFilters">
          {{ t('alerts.resetFilters') }}
        </button>
      </div>

      <template v-else>
        <div class="ls-alertspage__scroll">
          <table class="ls-alertspage__table">
            <caption class="ls-visually-hidden">{{ t('alerts.title') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('alerts.colSeverity') }}</th>
                <th scope="col">{{ t('alerts.colRule') }}</th>
                <th scope="col">{{ t('alerts.colServer') }}</th>
                <th scope="col">{{ t('alerts.colStarted') }}</th>
                <th scope="col">{{ t('alerts.colStatus') }}</th>
                <th scope="col">{{ t('alerts.colActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="alert in pageAlerts"
                :key="alert.id"
                class="ls-alertspage__row"
                :data-alert-id="alert.id"
                :data-alert-status="alert.status"
              >
                <td class="ls-alertspage__cell ls-alertspage__cell--severity" :data-label="t('alerts.colSeverity')">
                  <span class="ls-alertspage__badge" :class="`ls-tone--${severityTone(alert.severity)}`">
                    {{ t(severityLabelKey(alert.severity)) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.colRule')">
                  <button
                    :id="`alert-detail-${alert.id}`"
                    class="ls-alertspage__rule"
                    type="button"
                    :title="ruleText(alert)"
                    @click="openDetail(alert)"
                  >
                    {{ ruleText(alert) }}
                  </button>
                  <span class="ls-alertspage__message" :title="alert.message ?? alert.title">
                    {{ alert.message || alert.title }}
                  </span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.colServer')">
                  <span
                    class="ls-alertspage__server"
                    :class="{ 'ls-alertspage__server--rule': isRuleLevel(alert) }"
                  >
                    {{ serverText(alert) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.colStarted')">
                  <span class="ls-alertspage__time" :title="rowTime(alert).absolute">
                    {{ rowTime(alert).absolute }}
                  </span>
                  <span class="ls-alertspage__relative">{{ rowTime(alert).relative }}</span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.colStatus')">
                  <span class="ls-alertspage__badge" :class="`ls-tone--${statusTone(alert.status)}`">
                    {{ t(statusLabelKey(alert.status)) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell ls-alertspage__cell--actions" :data-label="t('alerts.colActions')">
                  <button
                    :id="`alert-view-${alert.id}`"
                    class="ls-btn ls-btn--small"
                    type="button"
                    @click="openDetail(alert)"
                  >
                    {{ t('alerts.detail') }}
                  </button>
                  <button
                    :id="`alert-ack-${alert.id}`"
                    class="ls-btn ls-btn--small ls-alertspage__ack"
                    type="button"
                    :disabled="!canAck(alert)"
                    @click="runAck(alert)"
                  >
                    <span v-if="ackingId === alert.id" class="ls-spinner" aria-hidden="true" />
                    {{ ackButtonText(alert) }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <nav v-if="pages > 1" class="ls-alertspage__pager" :aria-label="t('alerts.title')">
          <button
            id="alerts-prev-page"
            class="ls-btn ls-btn--small ls-alertspage__prev"
            type="button"
            :disabled="page <= 1"
            :aria-label="t('alerts.pagerPrev')"
            @click="page = clampPage(page - 1, visibleAlerts.length, ALERTS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
          <span id="alerts-pager-info" class="ls-alertspage__pager-info">
            {{ t('alerts.pagerInfo', { page, pages }) }}
          </span>
          <button
            id="alerts-next-page"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="page >= pages"
            :aria-label="t('alerts.pagerNext')"
            @click="page = clampPage(page + 1, visibleAlerts.length, ALERTS_PAGE_SIZE)"
          >
            <AppIcon name="chevron-right" size="0.75rem" />
          </button>
        </nav>

        <p class="ls-alertspage__note">{{ t('alerts.note') }}</p>
      </template>
    </template>

    <AlertDetailDrawer v-if="drawerAlert" :alert="drawerAlert" @close="closeDrawer" />
  </section>
</template>
