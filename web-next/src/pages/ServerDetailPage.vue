<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
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
import { api, type CheckItem, type Server, type ServerAccess, type ServerLoginResult } from '@/lib/api'
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

/* 面板入口 --------------------------------------------------------- */
const panelBusy = ref(false)
const panelError = ref<string | null>(null)

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
  panelError.value = null
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
      <button
        class="ls-btn ls-btn--small"
        type="button"
        :disabled="refreshing || status === 'loading'"
        @click="loadServer(false)"
      >
        <AppIcon name="refresh" size="0.875rem" />
        {{ t('common.refresh') }}
      </button>
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
          @open-panel="openPanel"
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
          @run="runLogin"
          @open-url="reopenUrl"
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
  </section>
</template>
