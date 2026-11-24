<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useApps } from '@/composables/useApps'
import { useAuth } from '@/composables/useAuth'
import { useCan } from '@/composables/useCan'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'
import { useI18n } from '@/i18n'
import {
  api,
  type AlertSummary,
  type CheckSummary,
  type Release,
  type Server,
  type SystemInfo,
} from '@/lib/api'
import {
  buildAppNameMap,
  releaseAppLabel,
  releaseStats,
  releaseStatusLabelKey,
  releaseStatusTone,
} from '@/lib/deploy-domain'
import { errorMessage } from '@/lib/errors'
import { formatRelativeTime, formatUptime } from '@/lib/format'

/**
 * 总览页（挂载在 AppShell 默认 slot → .ls-main__inner），替换脚手架首屏。
 *
 * 只回答「现在整体怎么样」：三张 KPI、需要关注的服务器、检测概览、告警概览、最近发布。
 * 每个区块各有独立的 loading / error / empty / ready 四态与独立重试入口——
 * 某一接口失败只让对应区块报错，其余区块照常渲染（页面不整页崩）。
 *
 * 数据接口全部沿用旧版 web/src/pages/DashboardPage.tsx：GET /system/info、/servers、
 * /checks/summary、/alerts/summary、/releases?limit=5；这里不新增端点。
 *
 * 权限：服务器相关（/system/info + /servers）需 server.read、检测概览需 check.read、
 * 告警概览需 alert.read、最近发布需 deploy.read（/apps 与 /releases 同门控）。
 * 缺权限的块只渲染「无权查看」空态，不发请求也不起轮询定时器。
 */

/** 「需要关注」最多列出多少台机器（与旧版一致）。 */
const ATTENTION_LIMIT = 6
/** 各区块轮询间隔：与旧版一致。 */
const POLL_MS = 5_000

type BlockState = 'loading' | 'ready' | 'error'

interface Resource<T> {
  data: Ref<T | null>
  state: Ref<BlockState>
  error: Ref<string>
  loaded: Ref<boolean>
  refreshing: Ref<boolean>
  refresh: () => Promise<void>
}

/**
 * 单个区块的数据 owner：一次请求 + 10s 轮询 + 三态（empty 由各区块按数据判定）。
 * 组件只读它，不各自发同一请求。
 */
function useResource<T>(
  fetcher: () => Promise<T>,
  options: { enabled?: () => boolean; fallback?: string; intervalMs?: number } = {},
): Resource<T> {
  const enabled = options.enabled ?? (() => true)
  const intervalMs = options.intervalMs ?? POLL_MS
  const data = shallowRef<T | null>(null)
  const state = ref<BlockState>('loading')
  const error = ref('')
  const loaded = ref(false)
  const refreshing = ref(false)

  let inflight: Promise<void> | null = null
  let timer: ReturnType<typeof setInterval> | null = null

  async function load(): Promise<void> {
    if (!enabled()) return
    if (inflight) return inflight
    refreshing.value = true
    inflight = (async () => {
      try {
        data.value = await fetcher()
        loaded.value = true
        state.value = 'ready'
        error.value = ''
      } catch (caught) {
        // 首屏失败才把区块打回错误态；已有数据时保留旧数据，只更新错误文案（可重试）
        if (!loaded.value) state.value = 'error'
        error.value = errorMessage(caught, options.fallback)
      } finally {
        refreshing.value = false
        inflight = null
      }
    })()
    return inflight
  }

  async function refresh(): Promise<void> {
    if (inflight) await inflight
    await load()
  }

  function stopTimer(): void {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  function startTimer(): void {
    stopTimer()
    if (intervalMs <= 0) return
    timer = setInterval(() => {
      // 页面在后台时不轮询：省一次无意义的请求
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
      void load()
    }, intervalMs)
  }

  watch(
    enabled,
    (allowed) => {
      if (allowed) {
        void load()
        startTimer()
      } else {
        stopTimer()
        data.value = null
        loaded.value = false
        state.value = 'loading'
        error.value = ''
      }
    },
    { immediate: true },
  )

  onScopeDispose(stopTimer)

  return { data, state, error, loaded, refreshing, refresh }
}

const { t } = useI18n()
const { can } = useCan()
const { scope } = useAuth()

/** 四个区块各按自己的权限点短路：无权限 → 该块只渲染「无权查看」空态，零请求、零轮询。 */
const canViewServers = computed(() => can('server.read'))
const canViewChecks = computed(() => can('check.read'))
const canViewAlerts = computed(() => can('alert.read'))
const canViewReleases = computed(() => can('deploy.read'))

const infoRes = useResource<SystemInfo>(() => api.systemInfo(), {
  enabled: () => canViewServers.value,
  fallback: t('dashboard.kpi.loadFailed'),
})
const serversRes = useResource<Server[]>(() => api.servers({}), {
  enabled: () => canViewServers.value,
  fallback: t('dashboard.attention.loadFailed'),
})
const checksRes = useResource<CheckSummary>(() => api.checksSummary(), {
  enabled: () => canViewChecks.value,
  fallback: t('dashboard.checks.loadFailed'),
})
const alertsRes = useResource<AlertSummary>(() => api.alertsSummary(), {
  enabled: () => canViewAlerts.value,
  fallback: t('dashboard.alerts.loadFailed'),
})
const releasesRes = useResource<Release[]>(() => api.releases({ limit: 5 }), {
  enabled: () => canViewReleases.value,
  fallback: t('dashboard.releases.loadFailed'),
})
/** GET /releases 只返回 app_id，应用名要用同一次门控的 GET /apps 建表（与 /releases 共用 deploy.read，不多发请求）。 */
const { apps, refresh: refreshApps } = useApps({ enabled: () => canViewReleases.value })

const refreshingAll = ref(false)

const info = computed(() => infoRes.data.value)
const servers = computed(() => serversRes.data.value ?? [])

const totalCount = computed(() => info.value?.server_count ?? servers.value.length)
const onlineCount = computed(
  () => info.value?.online_count ?? servers.value.filter((server) => server.status === 'online').length,
)
const offlineCount = computed(() => Math.max(totalCount.value - onlineCount.value, 0))
const onlineRate = computed(() =>
  totalCount.value > 0 ? `${Math.round((onlineCount.value / totalCount.value) * 100)}%` : t('dashboard.valueEmpty'),
)

/** KPI 区块：任一接口成功即可出数，两个都失败才算错误态。 */
const kpiView = computed<'denied' | 'loading' | 'ready' | 'error' | 'empty'>(() => {
  if (!canViewServers.value) return 'denied'
  if (infoRes.loaded.value || serversRes.loaded.value) {
    return totalCount.value === 0 ? 'empty' : 'ready'
  }
  if (infoRes.state.value === 'error' || serversRes.state.value === 'error') return 'error'
  return 'loading'
})
const kpiError = computed(() => infoRes.error.value || serversRes.error.value)
const kpiStaleError = computed(() =>
  kpiView.value === 'ready' && (infoRes.error.value || serversRes.error.value)
    ? infoRes.error.value || serversRes.error.value
    : '',
)

/** 「需要关注」= 状态非 online（离线 / 未知），最多 6 条。 */
const attention = computed(() => servers.value.filter((server) => server.status !== 'online'))
const attentionRows = computed(() => attention.value.slice(0, ATTENTION_LIMIT))
const attentionMore = computed(() => Math.max(attention.value.length - ATTENTION_LIMIT, 0))

const attentionView = computed<'denied' | 'loading' | 'error' | 'empty' | 'all-online' | 'ready'>(() => {
  if (!canViewServers.value) return 'denied'
  if (serversRes.state.value === 'error' && !serversRes.loaded.value) return 'error'
  if (!serversRes.loaded.value) return 'loading'
  if (servers.value.length === 0) return 'empty'
  if (attention.value.length === 0) return 'all-online'
  return 'ready'
})

const checkCounts = computed(() => ({
  total: checksRes.data.value?.total ?? 0,
  up: checksRes.data.value?.up ?? 0,
  down: checksRes.data.value?.down ?? 0,
  unknown: checksRes.data.value?.unknown ?? 0,
}))

const checksView = computed<'denied' | 'loading' | 'error' | 'empty' | 'ready'>(() => {
  if (!canViewChecks.value) return 'denied'
  if (checksRes.state.value === 'error' && !checksRes.loaded.value) return 'error'
  if (!checksRes.loaded.value) return 'loading'
  return checkCounts.value.total === 0 ? 'empty' : 'ready'
})

const alertCounts = computed(() => ({
  firing: alertsRes.data.value?.firing ?? 0,
  acked: alertsRes.data.value?.acked ?? 0,
  resolved24h: alertsRes.data.value?.resolved_24h ?? 0,
  critical: alertsRes.data.value?.by_severity?.critical ?? 0,
  warning: alertsRes.data.value?.by_severity?.warning ?? 0,
  info: alertsRes.data.value?.by_severity?.info ?? 0,
}))

const alertsView = computed<'denied' | 'loading' | 'error' | 'empty' | 'ready'>(() => {
  if (!canViewAlerts.value) return 'denied'
  if (alertsRes.state.value === 'error' && !alertsRes.loaded.value) return 'error'
  if (!alertsRes.loaded.value) return 'loading'
  const counts = alertCounts.value
  const empty = counts.firing + counts.acked + counts.resolved24h + counts.critical + counts.warning + counts.info === 0
  return empty ? 'empty' : 'ready'
})

const recentReleases = computed(() => (releasesRes.data.value ?? []).slice(0, 5))
/** app_id → 应用名：一次应用列表拉取后建表复用，绝不按行再拉。 */
const appNames = computed(() => buildAppNameMap(apps.value))

const releasesView = computed<'denied' | 'loading' | 'error' | 'empty' | 'ready'>(() => {
  if (!canViewReleases.value) return 'denied'
  if (releasesRes.state.value === 'error' && !releasesRes.loaded.value) return 'error'
  if (!releasesRes.loaded.value) return 'loading'
  return recentReleases.value.length === 0 ? 'empty' : 'ready'
})

const releaseEmptyText = computed(() =>
  scope.value.mode === 'all' ? t('dashboard.releases.empty') : t('dashboard.releases.scopedEmpty'),
)

async function refreshAll(): Promise<void> {
  refreshingAll.value = true
  try {
    await Promise.all([
      infoRes.refresh(),
      serversRes.refresh(),
      checksRes.refresh(),
      alertsRes.refresh(),
      canViewReleases.value ? releasesRes.refresh() : Promise.resolve(),
      canViewReleases.value ? refreshApps() : Promise.resolve(),
    ])
  } finally {
    refreshingAll.value = false
  }
}

function releaseSummary(release: Release): string {
  const stats = releaseStats(release)
  return t('dashboard.releases.runs', {
    success: stats.success,
    failed: stats.failed,
    active: stats.active,
  })
}
</script>

<template>
  <div id="dashboard-page" class="ls-adminpage ls-dashboard">
    <div class="ls-adminpage__head">
      <div class="ls-adminpage__heading">
        <h1 class="ls-adminpage__title">{{ t('dashboard.title') }}</h1>
        <p class="ls-adminpage__subtitle">
          {{ t('dashboard.subtitle') }}
          <span v-if="info"> · {{ t('dashboard.version', { version: info.version }) }}</span>
        </p>
      </div>
      <div class="ls-adminpage__head-actions">
        <button
          id="dash-refresh"
          class="ls-btn"
          type="button"
          :disabled="refreshingAll"
          @click="refreshAll"
        >
          <span v-if="refreshingAll" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="refresh" size="0.875rem" />
          {{ t('dashboard.refresh') }}
        </button>
        <RouterLink id="dash-view-servers" class="ls-btn ls-btn--primary" to="/servers">
          {{ t('dashboard.viewServers') }}
          <AppIcon name="chevron-right" size="0.875rem" />
        </RouterLink>
      </div>
    </div>

    <div v-if="kpiStaleError" id="dash-kpi-stale" class="ls-banner" role="alert">
      <AppIcon name="alert-triangle" size="1rem" />
      <div class="ls-banner__body">
        <p class="ls-banner__title">{{ t('dashboard.partialTitle') }}</p>
        <p class="ls-banner__text">{{ kpiStaleError }}</p>
      </div>
      <button class="ls-notice__action" type="button" @click="refreshAll">
        {{ t('dashboard.retry') }}
      </button>
    </div>

    <!-- 三张 KPI：总数 / 在线 / 离线 -->
    <section id="dash-kpi" class="ls-dashboard__kpis" aria-labelledby="dash-kpi-title">
      <h2 id="dash-kpi-title" class="ls-dashboard__sr-only">{{ t('dashboard.kpi.title') }}</h2>

      <div v-if="kpiView === 'error'" id="dash-kpi-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('dashboard.kpi.loadFailed') }}</p>
        <p class="ls-notice__text">{{ kpiError }}</p>
        <button id="dash-kpi-retry" class="ls-notice__action" type="button" @click="refreshAll">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-else-if="kpiView === 'denied'" id="dash-kpi-denied" class="ls-dashboard__empty">
        {{ t('dashboard.kpi.noPermission') }}
      </p>

      <template v-else>
        <article id="dash-kpi-total" class="ls-dashboard__kpi" data-kpi="total">
          <span class="ls-dashboard__kpi-label">{{ t('dashboard.kpi.total') }}</span>
          <strong class="ls-dashboard__kpi-value tabular-nums">
            {{ kpiView === 'loading' ? '—' : totalCount }}
          </strong>
          <span class="ls-dashboard__kpi-hint">
            {{ info ? t('dashboard.kpi.uptime', { uptime: formatUptime(info.uptime_s) }) : t('dashboard.kpi.loading') }}
          </span>
        </article>
        <article id="dash-kpi-online" class="ls-dashboard__kpi ls-dashboard__kpi--success" data-kpi="online">
          <span class="ls-dashboard__kpi-label">{{ t('dashboard.kpi.online') }}</span>
          <strong class="ls-dashboard__kpi-value tabular-nums">
            {{ kpiView === 'loading' ? '—' : onlineCount }}
          </strong>
          <span class="ls-dashboard__kpi-hint">
            {{ t('dashboard.kpi.onlineRate', { rate: onlineRate }) }}
          </span>
        </article>
        <article id="dash-kpi-offline" class="ls-dashboard__kpi ls-dashboard__kpi--danger" data-kpi="offline">
          <span class="ls-dashboard__kpi-label">{{ t('dashboard.kpi.offline') }}</span>
          <strong class="ls-dashboard__kpi-value tabular-nums">
            {{ kpiView === 'loading' ? '—' : offlineCount }}
          </strong>
          <span class="ls-dashboard__kpi-hint">{{ t('dashboard.kpi.offlineHint') }}</span>
        </article>
      </template>
    </section>

    <p v-if="kpiView === 'empty'" id="dash-kpi-empty" class="ls-dashboard__empty">
      {{ t('dashboard.kpi.noServers') }}
    </p>

    <!-- 需要关注：非 online 服务器 -->
    <section id="dash-attention" class="ls-card" aria-labelledby="dash-attention-title">
      <div class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="dash-attention-title" class="ls-card__title">
            <AppIcon name="alert-triangle" size="1rem" />
            {{ t('dashboard.attention.title') }}
            <span v-if="attentionView === 'ready'" id="dash-attention-count" class="ls-dashboard__count tabular-nums">
              {{ attention.length }}
            </span>
          </h2>
          <p class="ls-card__subtitle">{{ t('dashboard.attention.subtitle') }}</p>
        </div>
        <div class="ls-card__actions">
          <RouterLink class="ls-dashboard__link" to="/servers">
            {{ t('dashboard.viewServers') }}
          </RouterLink>
        </div>
      </div>

      <div v-if="serversRes.error.value && serversRes.loaded.value" id="dash-attention-stale" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('dashboard.attention.staleTitle') }}</p>
          <p class="ls-banner__text">{{ serversRes.error.value }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="serversRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-if="attentionView === 'denied'" id="dash-attention-denied" class="ls-dashboard__empty">
        {{ t('dashboard.attention.noPermission') }}
      </p>

      <div
        v-else-if="attentionView === 'loading'"
        id="dash-attention-loading"
        class="ls-dashboard__skeletons"
        aria-busy="true"
        :aria-label="t('dashboard.loading')"
      >
        <span v-for="index in 3" :key="index" class="ls-skeleton ls-dashboard__skeleton" />
      </div>

      <div v-else-if="attentionView === 'error'" id="dash-attention-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('dashboard.attention.loadFailed') }}</p>
        <p class="ls-notice__text">{{ serversRes.error.value }}</p>
        <button id="dash-attention-retry" class="ls-notice__action" type="button" @click="serversRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-else-if="attentionView === 'empty'" id="dash-attention-empty" class="ls-dashboard__empty">
        {{ t('dashboard.attention.noServers') }}
      </p>

      <p v-else-if="attentionView === 'all-online'" id="dash-attention-all-online" class="ls-dashboard__all-online">
        {{ t('dashboard.attention.allOnline') }}
      </p>

      <template v-else>
        <ul class="ls-dashboard__attention-list">
          <li v-for="server in attentionRows" :key="server.id">
            <RouterLink
              class="ls-dashboard__attention-row"
              :to="`/servers/${encodeURIComponent(server.id)}`"
              :data-server-id="server.id"
              :data-status="server.status"
            >
              <span class="ls-badge" :class="`ls-badge--${serverStatusTone(server.status)}`">
                {{ t(serverStatusLabelKey(server.status)) }}
              </span>
              <span class="ls-dashboard__attention-name" :title="server.name">{{ server.name }}</span>
              <span class="ls-dashboard__attention-host" :title="server.host">
                {{ server.port && server.port > 0 ? `${server.host}:${server.port}` : server.host }}
              </span>
              <span class="ls-dashboard__attention-time">
                {{ server.last_seen_at ? formatRelativeTime(server.last_seen_at) : '—' }}
              </span>
            </RouterLink>
          </li>
        </ul>
        <p v-if="attentionMore > 0" id="dash-attention-more" class="ls-dashboard__more">
          {{ t('dashboard.attention.more', { count: attentionMore }) }}
        </p>
      </template>
    </section>

    <!-- 检测概览 -->
    <section id="dash-checks" class="ls-card" aria-labelledby="dash-checks-title">
      <div class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="dash-checks-title" class="ls-card__title">
            <AppIcon name="pulse" size="1rem" />
            {{ t('dashboard.checks.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('dashboard.checks.subtitle') }}</p>
        </div>
        <div class="ls-card__actions">
          <RouterLink class="ls-dashboard__link" to="/checks">{{ t('dashboard.checks.viewAll') }}</RouterLink>
        </div>
      </div>

      <div v-if="checksRes.error.value && checksRes.loaded.value" id="dash-checks-stale" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('dashboard.checks.staleTitle') }}</p>
          <p class="ls-banner__text">{{ checksRes.error.value }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="checksRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-if="checksView === 'denied'" id="dash-checks-denied" class="ls-dashboard__empty">
        {{ t('dashboard.checks.noPermission') }}
      </p>

      <div
        v-else-if="checksView === 'loading'"
        id="dash-checks-loading"
        class="ls-dashboard__skeletons ls-dashboard__skeletons--inline"
        aria-busy="true"
        :aria-label="t('dashboard.loading')"
      >
        <span v-for="index in 2" :key="index" class="ls-skeleton ls-dashboard__skeleton" />
      </div>

      <div v-else-if="checksView === 'error'" id="dash-checks-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('dashboard.checks.loadFailed') }}</p>
        <p class="ls-notice__text">{{ checksRes.error.value }}</p>
        <button id="dash-checks-retry" class="ls-notice__action" type="button" @click="checksRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-else-if="checksView === 'empty'" id="dash-checks-empty" class="ls-dashboard__empty">
        {{ t('dashboard.checks.empty') }}
      </p>

      <div v-else id="dash-checks-ready" class="ls-dashboard__tiles">
        <div class="ls-dashboard__tile" data-check="total">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.checks.total') }}</span>
          <strong class="ls-dashboard__tile-value tabular-nums">{{ checkCounts.total }}</strong>
        </div>
        <div class="ls-dashboard__tile" data-check="up">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.checks.up') }}</span>
          <strong class="ls-dashboard__tile-value ls-dashboard__tile-value--success tabular-nums">
            {{ checkCounts.up }}
          </strong>
        </div>
        <div class="ls-dashboard__tile" data-check="down">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.checks.down') }}</span>
          <strong class="ls-dashboard__tile-value ls-dashboard__tile-value--danger tabular-nums">
            {{ checkCounts.down }}
          </strong>
        </div>
        <div class="ls-dashboard__tile" data-check="unknown">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.checks.unknown') }}</span>
          <strong class="ls-dashboard__tile-value tabular-nums">{{ checkCounts.unknown }}</strong>
        </div>
      </div>
    </section>

    <!-- 告警概览 -->
    <section id="dash-alerts" class="ls-card" aria-labelledby="dash-alerts-title">
      <div class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="dash-alerts-title" class="ls-card__title">
            <AppIcon name="bell" size="1rem" />
            {{ t('dashboard.alerts.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('dashboard.alerts.subtitle') }}</p>
        </div>
        <div class="ls-card__actions">
          <RouterLink class="ls-dashboard__link" to="/alerts">{{ t('dashboard.alerts.viewAll') }}</RouterLink>
        </div>
      </div>

      <div v-if="alertsRes.error.value && alertsRes.loaded.value" id="dash-alerts-stale" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('dashboard.alerts.staleTitle') }}</p>
          <p class="ls-banner__text">{{ alertsRes.error.value }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="alertsRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-if="alertsView === 'denied'" id="dash-alerts-denied" class="ls-dashboard__empty">
        {{ t('dashboard.alerts.noPermission') }}
      </p>

      <div
        v-else-if="alertsView === 'loading'"
        id="dash-alerts-loading"
        class="ls-dashboard__skeletons ls-dashboard__skeletons--inline"
        aria-busy="true"
        :aria-label="t('dashboard.loading')"
      >
        <span v-for="index in 2" :key="index" class="ls-skeleton ls-dashboard__skeleton" />
      </div>

      <div v-else-if="alertsView === 'error'" id="dash-alerts-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('dashboard.alerts.loadFailed') }}</p>
        <p class="ls-notice__text">{{ alertsRes.error.value }}</p>
        <button id="dash-alerts-retry" class="ls-notice__action" type="button" @click="alertsRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-else-if="alertsView === 'empty'" id="dash-alerts-empty" class="ls-dashboard__empty">
        {{ t('dashboard.alerts.empty') }}
      </p>

      <div v-else id="dash-alerts-ready" class="ls-dashboard__tiles">
        <div class="ls-dashboard__tile" data-alert="firing">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.alerts.firing') }}</span>
          <strong class="ls-dashboard__tile-value ls-dashboard__tile-value--danger tabular-nums">
            {{ alertCounts.firing }}
          </strong>
        </div>
        <div class="ls-dashboard__tile" data-alert="acked">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.alerts.acked') }}</span>
          <strong class="ls-dashboard__tile-value tabular-nums">{{ alertCounts.acked }}</strong>
        </div>
        <div class="ls-dashboard__tile" data-alert="resolved">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.alerts.resolved24h') }}</span>
          <strong class="ls-dashboard__tile-value ls-dashboard__tile-value--success tabular-nums">
            {{ alertCounts.resolved24h }}
          </strong>
        </div>
        <div class="ls-dashboard__tile" data-alert="severity">
          <span class="ls-dashboard__tile-label">{{ t('dashboard.alerts.severity') }}</span>
          <strong class="ls-dashboard__tile-value tabular-nums">
            {{ alertCounts.critical }}/{{ alertCounts.warning }}/{{ alertCounts.info }}
          </strong>
        </div>
      </div>
    </section>

    <!-- 最近发布 -->
    <section id="dash-releases" class="ls-card" aria-labelledby="dash-releases-title">
      <div class="ls-card__head">
        <div class="ls-card__heading">
          <h2 id="dash-releases-title" class="ls-card__title">
            <AppIcon name="folder" size="1rem" />
            {{ t('dashboard.releases.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('dashboard.releases.subtitle') }}</p>
        </div>
        <div v-if="canViewReleases" class="ls-card__actions">
          <RouterLink class="ls-dashboard__link" to="/releases">{{ t('dashboard.releases.viewAll') }}</RouterLink>
        </div>
      </div>

      <div v-if="releasesRes.error.value && releasesRes.loaded.value" id="dash-releases-stale" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('dashboard.releases.staleTitle') }}</p>
          <p class="ls-banner__text">{{ releasesRes.error.value }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="releasesRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-if="releasesView === 'denied'" id="dash-releases-denied" class="ls-dashboard__empty">
        {{ t('dashboard.releases.noPermission') }}
      </p>

      <div
        v-else-if="releasesView === 'loading'"
        id="dash-releases-loading"
        class="ls-dashboard__skeletons"
        aria-busy="true"
        :aria-label="t('dashboard.loading')"
      >
        <span v-for="index in 3" :key="index" class="ls-skeleton ls-dashboard__skeleton" />
      </div>

      <div v-else-if="releasesView === 'error'" id="dash-releases-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('dashboard.releases.loadFailed') }}</p>
        <p class="ls-notice__text">{{ releasesRes.error.value }}</p>
        <button id="dash-releases-retry" class="ls-notice__action" type="button" @click="releasesRes.refresh()">
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <p v-else-if="releasesView === 'empty'" id="dash-releases-empty" class="ls-dashboard__empty">
        {{ releaseEmptyText }}
      </p>

      <ul v-else id="dash-releases-ready" class="ls-dashboard__release-list">
        <li v-for="release in recentReleases" :key="release.id">
          <RouterLink
            class="ls-dashboard__release"
            :to="`/releases/${encodeURIComponent(release.id)}`"
            :data-release-id="release.id"
          >
            <span class="ls-badge" :class="`ls-badge--${releaseStatusTone(release.status)}`">
              {{ t(releaseStatusLabelKey(release.status)) }}
            </span>
            <span class="ls-dashboard__release-app" :title="releaseAppLabel(release, appNames)">
              {{ releaseAppLabel(release, appNames) }}
            </span>
            <span class="ls-dashboard__release-version">v{{ release.version }}</span>
            <span class="ls-dashboard__release-runs">{{ releaseSummary(release) }}</span>
            <span class="ls-dashboard__release-time">{{ formatRelativeTime(release.created_at) }}</span>
          </RouterLink>
        </li>
      </ul>
    </section>

    <p class="ls-dashboard__foot">{{ t('dashboard.footnote') }}</p>
  </div>
</template>
