<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import ServerFormDialog from '@/components/servers/ServerFormDialog.vue'
import ServerOverviewTab from '@/components/server-detail/ServerOverviewTab.vue'
import ServerAccessTab from '@/components/server-detail/ServerAccessTab.vue'
import ServerLoginTab from '@/components/server-detail/ServerLoginTab.vue'
import ServerMetaTab from '@/components/server-detail/ServerMetaTab.vue'
import ServerChecksTab from '@/components/server-detail/ServerChecksTab.vue'
import ServerFilesPanel from '@/components/server-tools/ServerFilesPanel.vue'
import ServerLogsPanel from '@/components/server-tools/ServerLogsPanel.vue'
import ServerTerminalPanel from '@/components/server-tools/ServerTerminalPanel.vue'
import { useCan } from '@/composables/useCan'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'
import { useI18n } from '@/i18n'
import {
  api,
  type AgentUpgradeResult,
  type CheckItem,
  type MetricsResponse,
  type MetricsStep,
  type Server,
  type ServerAccess,
  type ServerLoginResult,
  type TestConnectionResult,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { serverAddress } from '@/lib/format'
import {
  SERVER_DETAIL_TAB_QUERY,
  normalizeServerDetailTab,
  type ServerDetailTab,
} from '@/router'
import {
  checksForServer,
  loginResultMode,
  metaFieldsFromRows,
  type MetaRow,
} from '@/lib/server-detail'
import {
  isSha256Hex,
  metricsRange,
  type AgentUpgradeDraft,
  type MetricsWindow,
} from '@/lib/server-metrics'

/**
 * 服务器详情页：八个 tab（概览 / 登录信息 / 一键登录 / 服务器档案 / 检测 / 终端 / 文件 / 日志）。
 *
 * 本页是唯一的数据 owner：详情、登录信息、一键登录、面板、档案、检测项的
 * 异步状态都在这里，各 tab 组件只负责渲染与派发。这样「登录信息收起即丢弃」
 * 与「档案保存后重新读回详情」只有一处实现，不会在组件之间走样。
 *
 * 终端 / 文件 / 日志共用 terminal.use：没权限时三个 tab 依然可见（用户要知道有这功能），
 * 但内容区只给说明、不挂载面板 —— 面板一旦挂载就会发 ticket / files / logs 请求。
 * 深链 ?tab=terminal|files|logs|overview|access|login|meta|checks 直接落到对应 tab，
 * 页内切换再 router.replace 写回（query 契约集中在 src/router/index.ts）。
 */
const TABS: { key: ServerDetailTab; labelKey: string }[] = [
  { key: 'overview', labelKey: 'server.tab.overview' },
  { key: 'access', labelKey: 'server.tab.access' },
  { key: 'login', labelKey: 'server.tab.login' },
  { key: 'meta', labelKey: 'server.tab.meta' },
  { key: 'checks', labelKey: 'server.tab.checks' },
  { key: 'terminal', labelKey: 'server.tab.terminal' },
  { key: 'files', labelKey: 'server.tab.files' },
  { key: 'logs', labelKey: 'server.tab.logs' },
]

/** 需要 terminal.use 的面板；缺权限时只渲染说明，不挂载组件。 */
const TOOL_TABS: readonly ServerDetailTab[] = ['terminal', 'files', 'logs']

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const { can } = useCan()

const serverId = computed(() => String(route.params.id ?? ''))
const tab = ref<ServerDetailTab>(normalizeServerDetailTab(route.query.tab))

/** 320px 下八个 tab 会横向滚动：深链直达时把选中的 tab 滚进可视区，避免「看不到谁选中」。 */
const tablist = ref<HTMLDivElement | null>(null)

async function revealActiveTab(): Promise<void> {
  await nextTick()
  const list = tablist.value
  const active = list?.querySelector<HTMLElement>('[aria-selected="true"]')
  if (!list || !active) return
  const listRect = list.getBoundingClientRect()
  const activeRect = active.getBoundingClientRect()
  if (activeRect.left < listRect.left) list.scrollLeft -= listRect.left - activeRect.left
  else if (activeRect.right > listRect.right) list.scrollLeft += activeRect.right - listRect.right
}

/* 详情 ------------------------------------------------------------ */
const server = ref<Server | null>(null)
const status = ref<'loading' | 'ready' | 'error'>('loading')
const loadError = ref<string | null>(null)
const refreshing = ref(false)
const notFound = ref(false)

/* 登录信息（默认折叠、展开才请求、收起即丢弃） ----------------------- */
const accessExpanded = ref(false)
const access = ref<ServerAccess | null>(null)
const accessLoading = ref(false)
const accessError = ref<string | null>(null)
let accessRequest = 0

/* 一键登录 --------------------------------------------------------- */
const loginBusy = ref(false)
const loginError = ref<string | null>(null)
const loginResult = ref<ServerLoginResult | null>(null)
const loginNotice = ref<string | null>(null)

/* 登录脚本（server.write）：保存 PATCH 由本页 owner 发，tab 只提交草稿 ---- */
const scriptSaving = ref(false)
const scriptError = ref<string | null>(null)
const scriptNotice = ref<string | null>(null)

/* 面板入口 --------------------------------------------------------- */
const panelBusy = ref(false)
const panelError = ref<string | null>(null)

/* 指标曲线（只读，server.read） ------------------------------------ */
const metricsWindow = ref<MetricsWindow>('1h')
const metricsStep = ref<MetricsStep>('raw')
const metrics = ref<MetricsResponse | null>(null)
const metricsLoading = ref(false)
const metricsError = ref<string | null>(null)
const metricsFrom = ref(0)
const metricsTo = ref(0)
let metricsRequest = 0

/* 主机信息刷新 / 测试连接 / Agent 升级（server.write） ------------- */
const hostBusy = ref(false)
const hostError = ref<string | null>(null)
const hostNotice = ref<string | null>(null)
const connection = ref<TestConnectionResult | null>(null)
const connectionBusy = ref(false)
const connectionError = ref<string | null>(null)
const upgradeBusy = ref(false)
const upgradeResult = ref<AgentUpgradeResult | null>(null)
const upgradeError = ref<string | null>(null)

/* 服务器档案 ------------------------------------------------------- */
const metaSaving = ref(false)
const metaError = ref<string | null>(null)
const metaNotice = ref<string | null>(null)
const metaReloadError = ref<string | null>(null)
const metaSavedAt = ref(0)

/* 检测 ------------------------------------------------------------- */
const checks = ref<CheckItem[]>([])
const checksLoading = ref(false)
const checksError = ref<string | null>(null)
const checksLoaded = ref(false)

const canWrite = computed(() => can('server.write'))
const canRead = computed(() => can('server.read'))
const canTerminal = computed(() => can('terminal.use'))
const serverChecks = computed(() => checksForServer(checks.value, serverId.value))
const title = computed(() => server.value?.name || serverId.value)
const address = computed(() => (server.value ? serverAddress(server.value) : ''))

/* 详情读取 --------------------------------------------------------- */

async function loadServer(initial = false): Promise<void> {
  if (initial) {
    status.value = 'loading'
  } else {
    refreshing.value = true
  }
  loadError.value = null
  notFound.value = false
  try {
    const fresh = await api.server(serverId.value)
    server.value = fresh
    status.value = 'ready'
    // 指标只在首次进入（或切换服务器后）自动拉一次；刷新按钮不重复打指标接口。
    if (canRead.value && metrics.value === null && !metricsLoading.value) void loadMetrics()
  } catch (error) {
    const message = errorMessage(error, t('server.detail.loadFailed'))
    if (/404|not found|不存在/i.test(message)) {
      notFound.value = true
      status.value = 'error'
      loadError.value = t('server.detail.notFound')
    } else if (server.value && !initial) {
      // 已经有数据时刷新失败：保留旧内容，只在顶部提示，避免整页塌成错误态。
      loadError.value = message
      status.value = 'ready'
    } else {
      status.value = 'error'
      loadError.value = message
    }
  } finally {
    refreshing.value = false
  }
}

function discardAccess(): void {
  accessRequest += 1
  accessExpanded.value = false
  access.value = null
  accessError.value = null
  accessLoading.value = false
}

async function loadAccess(): Promise<void> {
  accessRequest += 1
  const token = accessRequest
  accessLoading.value = true
  accessError.value = null
  try {
    const data = await api.serverAccess(serverId.value)
    if (token !== accessRequest) return
    access.value = data
  } catch (error) {
    if (token !== accessRequest) return
    access.value = null
    accessError.value = errorMessage(error, t('server.access.loadFailed'))
  } finally {
    if (token === accessRequest) accessLoading.value = false
  }
}

function toggleAccess(): void {
  if (accessExpanded.value) {
    discardAccess()
    return
  }
  accessExpanded.value = true
  void loadAccess()
}

/* 一键登录 --------------------------------------------------------- */

function openUrl(url: string): void {
  if (url.trim() === '') return
  const opened = window.open(url, '_blank', 'noopener,noreferrer')
  loginNotice.value = opened ? t('server.login.urlOpened') : t('server.login.urlBlocked')
}

async function runLogin(): Promise<void> {
  if (loginBusy.value) return
  loginBusy.value = true
  loginError.value = null
  loginNotice.value = null
  loginResult.value = null
  try {
    const result = await api.serverLogin(serverId.value)
    loginResult.value = result
    if (loginResultMode(result) === 'url') openUrl(result.url)
  } catch (error) {
    loginError.value = errorMessage(error, t('server.login.failed'))
  } finally {
    loginBusy.value = false
  }
}

function reopenUrl(): void {
  if (loginResult.value) openUrl(loginResult.value.url)
}

/**
 * 保存/清除登录脚本。
 *
 * payload 只有 `{ login_script }` 一个键（空串 = 清除，契约见 api.ts UpdateServerPayload）；
 * 不能顺手带 notes / custom_fields，否则会把别的 tab 的档案例写成空。
 * 成功后把 PATCH 返回的 Server 合并回页面状态，配置态与「一键登录」可用性同步刷新。
 */
async function saveLoginScript(payload: { login_script: string }): Promise<void> {
  if (scriptSaving.value) return
  scriptSaving.value = true
  scriptError.value = null
  scriptNotice.value = null
  try {
    const updated = await api.updateServer(serverId.value, { login_script: payload.login_script })
    // 契约只保证 notes / custom_fields 不回显；login_script 若为空而本次提交非空，按提交值合并，避免刚保存就显示成未配置。
    server.value =
      updated.login_script === '' && payload.login_script !== ''
        ? { ...updated, login_script: payload.login_script }
        : updated
    scriptNotice.value = t('server.login.scriptSaved')
  } catch (error) {
    scriptError.value = errorMessage(error, t('server.login.scriptSaveFailed'))
  } finally {
    scriptSaving.value = false
  }
}

/* 面板入口：地址只在点击时向后端索取并直接打开，绝不写进文案 --------- */

async function openPanel(): Promise<void> {
  if (panelBusy.value) return
  panelBusy.value = true
  panelError.value = null
  try {
    const panel = await api.serverPanel(serverId.value)
    const opened = window.open(panel.panel_url, '_blank', 'noopener,noreferrer')
    if (!opened) panelError.value = t('server.login.urlBlocked')
  } catch (error) {
    panelError.value = `${t('server.panel.failed')}${errorMessage(error, '')}`.trim()
  } finally {
    panelBusy.value = false
  }
}

/* 指标曲线 --------------------------------------------------------- */

/**
 * 拉一次指标：时间窗 → from/to 毫秒，step 由用户选择。
 * 后端在 step=raw 且跨度 > 6h 时会自动降级并回传实际 step，这里不覆盖用户选择。
 */
async function loadMetrics(): Promise<void> {
  if (!canRead.value) return
  metricsRequest += 1
  const token = metricsRequest
  metricsLoading.value = true
  metricsError.value = null
  const range = metricsRange(metricsWindow.value)
  metricsFrom.value = range.from
  metricsTo.value = range.to
  try {
    const data = await api.metrics(serverId.value, {
      from: range.from,
      to: range.to,
      step: metricsStep.value,
    })
    if (token !== metricsRequest) return
    metrics.value = data
  } catch (error) {
    if (token !== metricsRequest) return
    metrics.value = null
    metricsError.value = errorMessage(error, t('servers.detail.metrics.loadFailed'))
  } finally {
    if (token === metricsRequest) metricsLoading.value = false
  }
}

/* 主机信息刷新：成功后用返回的 HostInfo 覆盖展示 -------------------- */

async function refreshHost(): Promise<void> {
  if (hostBusy.value || !canWrite.value) return
  hostBusy.value = true
  hostError.value = null
  hostNotice.value = null
  try {
    const info = await api.refreshHostInfo(serverId.value)
    if (!info) {
      hostError.value = t('server.overview.hostEmpty')
      return
    }
    if (server.value) server.value = { ...server.value, host_info: info }
    hostNotice.value = t('servers.detail.host.refreshed')
  } catch (error) {
    hostError.value = errorMessage(error, t('servers.detail.host.refreshFailed'))
  } finally {
    hostBusy.value = false
  }
}

/* 测试连接：ok=false 时把后端 error 原样交给界面，不再包装一层 --------- */

async function testConnection(): Promise<void> {
  if (connectionBusy.value || !canWrite.value) return
  connectionBusy.value = true
  connectionError.value = null
  connection.value = null
  try {
    connection.value = await api.testConnection(serverId.value)
  } catch (error) {
    connectionError.value = errorMessage(error, t('servers.detail.testConnection.requestFailed'))
  } finally {
    connectionBusy.value = false
  }
}

/* Agent 升级：sha256 前端先校验，不合规不发请求 ---------------------- */

async function submitAgentUpgrade(payload: AgentUpgradeDraft): Promise<void> {
  if (upgradeBusy.value || !canWrite.value) return
  if (!isSha256Hex(payload.sha256)) {
    upgradeError.value = t('servers.detail.agentUpgrade.error.invalidSha256')
    return
  }
  upgradeBusy.value = true
  upgradeError.value = null
  upgradeResult.value = null
  try {
    upgradeResult.value = await api.agentUpgrade(serverId.value, {
      version: payload.version,
      url: payload.url,
      sha256: payload.sha256,
    })
  } catch (error) {
    upgradeError.value = errorMessage(error, t('servers.detail.agentUpgrade.failed'))
  } finally {
    upgradeBusy.value = false
  }
}

/* 服务器档案：PATCH 后必须重新 GET 详情（PATCH 响应不含档案） --------- */

async function saveMeta(payload: { notes: string; rows: MetaRow[] }): Promise<void> {
  if (metaSaving.value) return
  metaSaving.value = true
  metaError.value = null
  metaNotice.value = null
  metaReloadError.value = null
  try {
    await api.updateServer(serverId.value, {
      notes: payload.notes,
      custom_fields: metaFieldsFromRows(payload.rows),
    })
  } catch (error) {
    metaError.value = errorMessage(error, t('server.meta.saveFailed'))
    metaSaving.value = false
    return
  }
  // PATCH 响应里的档案字段不可信，一律以重新读回详情为准。
  try {
    server.value = await api.server(serverId.value)
    metaSavedAt.value += 1
    metaNotice.value = t('server.meta.saved')
  } catch (error) {
    metaReloadError.value = errorMessage(error, '')
  } finally {
    metaSaving.value = false
  }
}

/* 检测：只读，进入 tab 时按需拉一次 --------------------------------- */

async function loadChecks(): Promise<void> {
  checksLoading.value = true
  checksError.value = null
  try {
    checks.value = await api.checks()
    checksLoaded.value = true
  } catch (error) {
    checksError.value = errorMessage(error, t('server.checks.loadFailed'))
  } finally {
    checksLoading.value = false
  }
}

/* tab 切换：离开「登录信息」立即丢弃明文；进入「检测」按需加载；query 同步 ---- */

/**
 * 把当前 tab 写回 URL：用 replace 不新增历史记录。
 * 与深链读取共用 SERVER_DETAIL_TAB_QUERY，overview 为默认值、不写参数。
 */
function syncTabQuery(next: ServerDetailTab): void {
  const desired = SERVER_DETAIL_TAB_QUERY[next]
  const current = typeof route.query.tab === 'string' ? route.query.tab : ''
  if (current === (desired ? desired.tab : '')) return
  const query = { ...route.query }
  if (desired) query.tab = desired.tab
  else delete query.tab
  void router.replace({ query })
}

watch(tab, (next) => {
  if (next !== 'access') discardAccess()
  if (next === 'checks' && !checksLoaded.value && !checksLoading.value) void loadChecks()
  syncTabQuery(next)
  void revealActiveTab()
})

// 详情读回来之后 tab 栏才挂载，深链要在这时再滚一次。
watch(server, () => void revealActiveTab())

watch([metricsWindow, metricsStep], () => void loadMetrics())

watch(canRead, (allowed) => {
  if (allowed && server.value && metrics.value === null && !metricsLoading.value) void loadMetrics()
})

watch(serverId, () => {
  server.value = null
  status.value = 'loading'
  loadError.value = null
  notFound.value = false
  accessExpanded.value = false
  access.value = null
  accessError.value = null
  loginResult.value = null
  loginError.value = null
  loginNotice.value = null
  scriptSaving.value = false
  scriptError.value = null
  scriptNotice.value = null
  panelError.value = null
  metricsRequest += 1
  metrics.value = null
  metricsError.value = null
  metricsLoading.value = false
  metricsFrom.value = 0
  metricsTo.value = 0
  hostBusy.value = false
  hostError.value = null
  hostNotice.value = null
  connection.value = null
  connectionBusy.value = false
  connectionError.value = null
  upgradeBusy.value = false
  upgradeResult.value = null
  upgradeError.value = null
  metaError.value = null
  metaNotice.value = null
  metaReloadError.value = null
  checks.value = []
  checksLoaded.value = false
  checksError.value = null
  tab.value = 'overview'
  void loadServer(true)
})

function focusTab(index: number): void {
  const next = TABS[(index + TABS.length) % TABS.length]
  tab.value = next.key
}

function onTabKeydown(event: KeyboardEvent, index: number): void {
  if (event.key === 'ArrowRight') {
    event.preventDefault()
    focusTab(index + 1)
  } else if (event.key === 'ArrowLeft') {
    event.preventDefault()
    focusTab(index - 1)
  } else if (event.key === 'Home') {
    event.preventDefault()
    focusTab(0)
  } else if (event.key === 'End') {
    event.preventDefault()
    focusTab(TABS.length - 1)
  }
}

function backToList(): void {
  void router.push('/servers')
}

/* 编辑服务器（server.write）：复用列表页的纳管弹窗，保存后重读详情 ------- */
const editOpen = ref(false)

function openEdit(): void {
  if (!canWrite.value) return
  editOpen.value = true
}

async function onServerSaved(): Promise<void> {
  editOpen.value = false
  await loadServer(false)
}

onMounted(() => {
  void loadServer(true)
})
</script>

<template>
  <section class="ls-detail">
    <header class="ls-detail__head">
      <div class="ls-detail__headMain">
        <button class="ls-btn ls-btn--small ls-detail__back" type="button" @click="backToList">
          <AppIcon name="chevron-right" size="0.875rem" class="ls-detail__backIcon" />
          {{ t('server.detail.back') }}
        </button>
        <h1 class="ls-detail__title" :title="title">{{ title }}</h1>
        <p class="ls-detail__subtitle">
          <span v-if="server" class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
            <span class="ls-srv-status__dot" aria-hidden="true" />
            {{ t(serverStatusLabelKey(server.status)) }}
          </span>
          <span v-if="address" class="ls-detail__address" :title="address">{{ address }}</span>
        </p>
      </div>
      <div class="ls-detail__actions">
        <button
          v-if="canWrite && server"
          id="server-detail-edit"
          class="ls-btn ls-btn--small"
          type="button"
          @click="openEdit"
        >
          <AppIcon name="settings" size="0.875rem" />
          {{ t('server.edit.action') }}
        </button>
        <button
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="refreshing || status === 'loading'"
          @click="loadServer(false)"
        >
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
      </div>
    </header>

    <div v-if="status === 'loading'" class="ls-detail__loading" role="status">
      <span class="ls-spinner" aria-hidden="true" />
      {{ t('server.detail.loading') }}
    </div>

    <div v-else-if="status === 'error'" class="ls-notice ls-notice--error" role="alert">
      <div class="ls-notice__body">
        <p class="ls-notice__title">{{ notFound ? t('server.detail.notFound') : t('server.detail.loadFailed') }}</p>
        <p class="ls-notice__text">{{ notFound ? t('server.detail.notFoundHint') : loadError }}</p>
      </div>
      <div class="ls-notice__actions">
        <button class="ls-btn ls-btn--small" type="button" @click="loadServer(true)">
          {{ t('common.retry') }}
        </button>
        <button class="ls-btn ls-btn--small" type="button" @click="backToList">
          {{ t('server.detail.back') }}
        </button>
      </div>
    </div>

    <template v-else-if="server">
      <p v-if="loadError" class="ls-banner ls-banner--warning" role="status">
        <AppIcon name="alert-triangle" size="1rem" />
        <span>{{ loadError }}</span>
      </p>

      <div ref="tablist" class="ls-tabs" role="tablist" :aria-label="t('server.detail.tabs')">
        <button
          v-for="(item, index) in TABS"
          :id="`server-tab-${item.key}`"
          :key="item.key"
          class="ls-tabs__btn"
          :class="{ 'ls-tabs__btn--active': tab === item.key }"
          type="button"
          role="tab"
          :aria-selected="tab === item.key"
          :aria-controls="`server-tabpanel-${item.key}`"
          :tabindex="tab === item.key ? 0 : -1"
          @click="tab = item.key"
          @keydown="onTabKeydown($event, index)"
        >
          {{ t(item.labelKey) }}
        </button>
      </div>

      <div
        :id="`server-tabpanel-${tab}`"
        class="ls-detail__panel"
        role="tabpanel"
        :aria-labelledby="`server-tab-${tab}`"
      >
        <ServerOverviewTab
          v-if="tab === 'overview'"
          :server="server"
          :panel-busy="panelBusy"
          :panel-error="panelError"
          :can-read="canRead"
          :can-write="canWrite"
          :metrics="metrics"
          :metrics-loading="metricsLoading"
          :metrics-error="metricsError"
          :metrics-window="metricsWindow"
          :metrics-step="metricsStep"
          :metrics-from="metricsFrom"
          :metrics-to="metricsTo"
          :host-busy="hostBusy"
          :host-error="hostError"
          :host-notice="hostNotice"
          :connection="connection"
          :connection-busy="connectionBusy"
          :connection-error="connectionError"
          :upgrade-busy="upgradeBusy"
          :upgrade-result="upgradeResult"
          :upgrade-error="upgradeError"
          @open-panel="openPanel"
          @metrics-window="metricsWindow = $event"
          @metrics-step="metricsStep = $event"
          @metrics-retry="loadMetrics"
          @refresh-host="refreshHost"
          @test-connection="testConnection"
          @agent-upgrade="submitAgentUpgrade"
        />
        <ServerAccessTab
          v-else-if="tab === 'access'"
          :title="t('server.access.title')"
          :expanded="accessExpanded"
          :access="access"
          :loading="accessLoading"
          :error="accessError"
          @toggle="toggleAccess"
          @reload="loadAccess"
        />
        <ServerLoginTab
          v-else-if="tab === 'login'"
          :server="server"
          :busy="loginBusy"
          :error="loginError"
          :result="loginResult"
          :notice="loginNotice"
          :can-write="canWrite"
          :script-saving="scriptSaving"
          :script-error="scriptError"
          :script-notice="scriptNotice"
          @run="runLogin"
          @open-url="reopenUrl"
          @save="saveLoginScript"
        />
        <ServerMetaTab
          v-else-if="tab === 'meta'"
          :server="server"
          :saving="metaSaving"
          :error="metaError"
          :notice="metaNotice"
          :reload-error="metaReloadError"
          :can-write="canWrite"
          :saved-at="metaSavedAt"
          @save="saveMeta"
        />
        <ServerChecksTab
          v-else-if="tab === 'checks'"
          :checks="serverChecks"
          :loading="checksLoading"
          :error="checksError"
          @reload="loadChecks"
        />

        <!-- 终端 / 文件 / 日志：缺 terminal.use 时只给说明，绝不挂载面板（避免发请求）。 -->
        <div
          v-else-if="TOOL_TABS.includes(tab) && !canTerminal"
          class="ls-banner ls-banner--warning"
          role="status"
        >
          <div class="ls-banner__body">
            <p class="ls-banner__title">{{ t('server.detail.needTerminal') }}</p>
            <p class="ls-banner__text">{{ t('server.detail.needTerminalHint') }}</p>
          </div>
        </div>
        <ServerTerminalPanel
          v-else-if="tab === 'terminal'"
          :server-id="serverId"
          :allowed="canTerminal"
        />
        <ServerFilesPanel v-else-if="tab === 'files'" :server-id="serverId" :allowed="canTerminal" />
        <ServerLogsPanel v-else-if="tab === 'logs'" :server-id="serverId" :allowed="canTerminal" />
      </div>
    </template>

    <ServerFormDialog
      v-if="editOpen && server"
      :server="server"
      @close="editOpen = false"
      @saved="onServerSaved"
    />
  </section>
</template>
