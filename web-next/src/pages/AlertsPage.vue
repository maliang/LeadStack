<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import AlertDetailDrawer from '@/components/alerts/AlertDetailDrawer.vue'
import { useAlerts } from '@/composables/useAlerts'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  ALERTS_PAGE_SIZE,
  ALERTS_TABS,
  ALERT_COMPARATORS,
  ALERT_SEVERITIES,
  ALERT_STATUSES,
  CHANNEL_KINDS,
  RULE_LEVEL_FILTER,
  RULE_SOURCES,
  SILENCE_OPTIONS,
  alertTime,
  channelErrorText,
  channelKindLabelKey,
  clampPage,
  collectServerOptions,
  emptyAlertFilters,
  filterAlerts,
  hasActiveAlertFilters,
  isRuleLevel,
  isRuleSilenced,
  normalizeAlertsTab,
  normalizeChannelKind,
  pageCount,
  paginate,
  ruleConditionText,
  ruleSilenceText,
  ruleSourceLabelKey,
  ruleTargetText,
  ruleText,
  serverText,
  severityLabelKey,
  severityTone,
  statusLabelKey,
  statusTone,
  type AlertFilterState,
  type AlertsTab,
} from '@/lib/alerts'
import { api } from '@/lib/api'
import type {
  AlertChannel,
  AlertChannelPayload,
  AlertItem,
  AlertRule,
  AlertRulePayload,
  AlertSeverity,
  AlertSource,
  ChannelKind,
  CheckItem,
  Server,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

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

/* ------------------------------------------------------------------ */
/* 分区 tab：事件（默认，不写 query）/ 告警规则 / 通知渠道               */
/* ------------------------------------------------------------------ */

const route = useRoute()
const router = useRouter()

const tab = computed<AlertsTab>(() => normalizeAlertsTab(route.query.tab))

/** tab 切换写进 query（router.replace，不新增历史栈）；事件页清掉参数保持干净链接。 */
function selectTab(next: AlertsTab): void {
  if (next === tab.value) return
  const query: LocationQueryRaw = { ...route.query }
  delete query.tab
  if (next !== 'events') query.tab = next
  void router.replace({ query })
}

/* ------------------------------ 告警规则 ------------------------------ */

type SectionState = 'idle' | 'loading' | 'loaded' | 'error'

const rules = ref<AlertRule[]>([])
const rulesState = ref<SectionState>('idle')
const rulesError = ref('')
const rulesActionError = ref('')
const rulesNotice = ref('')
const togglingRuleId = ref<string | null>(null)

/** 规则弹窗里检测项/服务器多选的选项；缺对应读权限时保持空数组并给提示。 */
const rulesChecks = ref<CheckItem[]>([])
const rulesServers = ref<Server[]>([])
const optionsError = ref('')

async function loadRules(): Promise<void> {
  if (!canRead.value) return
  rulesState.value = 'loading'
  rulesError.value = ''
  try {
    rules.value = await api.alertRules()
    rulesState.value = 'loaded'
  } catch (caught) {
    rulesState.value = 'error'
    rulesError.value = errorMessage(caught, t('alerts.rules.loadFailed'))
  }
}

async function loadRuleOptions(): Promise<void> {
  let failed = false
  const tasks: Promise<void>[] = []
  if (rulesChecks.value.length === 0) {
    if (!can('check.read')) failed = true
    else
      tasks.push(
        api.checks().then(
          (list) => {
            rulesChecks.value = list
          },
          () => {
            failed = true
          },
        ),
      )
  }
  if (rulesServers.value.length === 0) {
    if (!can('server.read')) failed = true
    else
      tasks.push(
        api.servers().then(
          (list) => {
            rulesServers.value = list
          },
          () => {
            failed = true
          },
        ),
      )
  }
  await Promise.all(tasks)
  optionsError.value = failed ? t('alerts.rules.optionsFailed') : ''
}

async function refreshRules(): Promise<void> {
  await Promise.all([loadRules(), loadRuleOptions()])
}

async function toggleRuleEnabled(rule: AlertRule, event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  if (!canWrite.value || togglingRuleId.value !== null) {
    input.checked = rule.enabled
    return
  }
  const enabled = input.checked
  togglingRuleId.value = rule.id
  rulesActionError.value = ''
  try {
    const updated = await api.updateAlertRule(rule.id, { enabled })
    const index = rules.value.findIndex((item) => item.id === rule.id)
    if (index >= 0) rules.value[index] = updated
  } catch (caught) {
    input.checked = rule.enabled
    rulesActionError.value = errorMessage(caught, t('alerts.rules.toggleFailed'))
  } finally {
    togglingRuleId.value = null
  }
}

/* ------------------------------ 规则弹窗 ------------------------------ */

interface RuleFormState {
  name: string
  source: string
  severity: string
  enabled: boolean
  /** number 输入框的 v-model 会写入 number，不能假定 string */
  forDuration: string | number
  checkIds: string[]
  serverIds: string[]
  metric: string
  comparator: string
  threshold: string | number
  channelIds: string[]
  dedupWindowS: number
}

function emptyRuleForm(): RuleFormState {
  return {
    name: '',
    source: 'check',
    severity: 'warning',
    enabled: true,
    forDuration: '0',
    checkIds: [],
    serverIds: [],
    metric: '',
    comparator: '>',
    threshold: '',
    channelIds: [],
    dedupWindowS: 300,
  }
}

const ruleModalOpen = ref(false)
const ruleEditingId = ref<string | null>(null)
const ruleFormError = ref('')
const ruleSaving = ref(false)
const ruleForm = reactive<RuleFormState>(emptyRuleForm())
/** 编辑态的原始 payload：提交时与当前表单做 diff，PATCH 只带变更字段 */
const ruleBaseline = ref<AlertRulePayload | null>(null)

const RULE_PATCH_KEYS = [
  'name',
  'enabled',
  'source',
  'for_duration_s',
  'severity',
  'dedup_window_s',
  'channel_ids',
  'check_ids',
  'server_ids',
  'metric',
  'comparator',
  'threshold',
] as const

/** 只保留与 baseline 不同的字段；空对象表示没有修改，不发请求。 */
function diffRulePayload(baseline: AlertRulePayload, next: AlertRulePayload): Partial<AlertRulePayload> {
  const patch: Record<string, unknown> = {}
  for (const key of RULE_PATCH_KEYS) {
    if (JSON.stringify(baseline[key]) !== JSON.stringify(next[key])) patch[key] = next[key]
  }
  // 来源从 metric 切走时显式清空 metric，避免后端留着旧的指标条件
  if (baseline.source === 'metric' && next.source !== 'metric') patch.metric = ''
  return patch as Partial<AlertRulePayload>
}

function openCreateRule(): void {
  if (!canWrite.value) return
  ruleEditingId.value = null
  ruleBaseline.value = null
  Object.assign(ruleForm, emptyRuleForm())
  ruleFormError.value = ''
  ruleModalOpen.value = true
  void loadRuleOptions()
  if (channels.value.length === 0) void loadChannels()
}

function openEditRule(rule: AlertRule): void {
  if (!canWrite.value) return
  ruleEditingId.value = rule.id
  Object.assign(ruleForm, {
    name: rule.name,
    source: rule.source,
    severity: rule.severity,
    enabled: rule.enabled,
    forDuration: String(rule.for_duration_s),
    checkIds: [...rule.check_ids],
    serverIds: [...rule.server_ids],
    metric: rule.metric ?? '',
    comparator: rule.comparator ?? '>',
    threshold: rule.threshold === null ? '' : String(rule.threshold),
    channelIds: [...rule.channel_ids],
    dedupWindowS: rule.dedup_window_s,
  } satisfies RuleFormState)
  ruleBaseline.value = buildRulePayload(ruleForm)
  ruleFormError.value = ''
  ruleModalOpen.value = true
  void loadRuleOptions()
  if (channels.value.length === 0) void loadChannels()
}

function closeRuleModal(): void {
  if (ruleSaving.value) return
  ruleModalOpen.value = false
  ruleBaseline.value = null
  ruleFormError.value = ''
}

function toggleCheckId(id: string): void {
  const index = ruleForm.checkIds.indexOf(id)
  if (index >= 0) ruleForm.checkIds.splice(index, 1)
  else ruleForm.checkIds.push(id)
}

function toggleServerId(id: string): void {
  const index = ruleForm.serverIds.indexOf(id)
  if (index >= 0) ruleForm.serverIds.splice(index, 1)
  else ruleForm.serverIds.push(id)
}

function toggleChannelId(id: string): void {
  const index = ruleForm.channelIds.indexOf(id)
  if (index >= 0) ruleForm.channelIds.splice(index, 1)
  else ruleForm.channelIds.push(id)
}

/** 校验通过返回 null；否则返回 i18n key（必填不过不发请求，由文案指出缺哪项）。 */
function validateRuleForm(form: RuleFormState): string | null {
  if (form.name.trim() === '') return 'alerts.rules.requiredName'
  if (!(RULE_SOURCES as readonly string[]).includes(form.source)) return 'alerts.rules.requiredName'
  if (form.source === 'check' && form.checkIds.length === 0) return 'alerts.rules.requiredChecks'
  if (form.source === 'server' && form.serverIds.length === 0) return 'alerts.rules.requiredServers'
  if (form.source === 'metric') {
    if (form.metric.trim() === '') return 'alerts.rules.requiredMetric'
    if (!(ALERT_COMPARATORS as readonly string[]).includes(form.comparator)) {
      return 'alerts.rules.requiredComparator'
    }
    const threshold = String(form.threshold).trim()
    if (threshold === '' || !Number.isFinite(Number(threshold))) {
      return 'alerts.rules.requiredThreshold'
    }
  }
  const rawDuration = String(form.forDuration).trim()
  const duration = Number(rawDuration)
  if (rawDuration === '' || !Number.isInteger(duration) || duration < 0 || duration > 604800) {
    return 'alerts.rules.invalidDuration'
  }
  return null
}

function buildRulePayload(form: RuleFormState): AlertRulePayload {
  const payload: AlertRulePayload = {
    name: form.name.trim(),
    enabled: form.enabled,
    source: form.source as AlertSource,
    for_duration_s: Number(String(form.forDuration).trim()),
    severity: form.severity as AlertSeverity,
    // 0 表示不去重（engine 里 windowS=0 即每次都通知），编辑时必须原样保留
    dedup_window_s: Number.isFinite(form.dedupWindowS) ? Math.max(0, form.dedupWindowS) : 300,
    channel_ids: [...form.channelIds],
  }
  // 目标数组总是显式下发：切换 source 时清掉旧来源的选择，避免编辑后残留
  payload.check_ids = form.source === 'check' ? [...form.checkIds] : []
  payload.server_ids = form.source === 'server' ? [...form.serverIds] : []
  if (form.source === 'metric') {
    payload.metric = form.metric.trim()
    payload.comparator = form.comparator
    payload.threshold = Number(String(form.threshold).trim())
  }
  return payload
}

async function submitRule(): Promise<void> {
  if (!canWrite.value || ruleSaving.value) return
  const invalidKey = validateRuleForm(ruleForm)
  if (invalidKey !== null) {
    ruleFormError.value = t(invalidKey)
    return
  }
  ruleFormError.value = ''
  ruleSaving.value = true
  rulesActionError.value = ''
  try {
    const payload = buildRulePayload(ruleForm)
    if (ruleEditingId.value === null) {
      await api.createAlertRule(payload)
    } else {
      const baseline = ruleBaseline.value
      const patch = baseline === null ? payload : diffRulePayload(baseline, payload)
      if (Object.keys(patch).length === 0) {
        ruleModalOpen.value = false
        ruleBaseline.value = null
        return
      }
      await api.updateAlertRule(ruleEditingId.value, patch)
    }
    ruleModalOpen.value = false
    ruleBaseline.value = null
    await loadRules()
    rulesNotice.value = t('alerts.rules.saved')
  } catch (caught) {
    ruleFormError.value = errorMessage(caught, t('alerts.rules.saveFailed'))
  } finally {
    ruleSaving.value = false
  }
}

/* ------------------------------ 静默弹窗 ------------------------------ */

const silenceTarget = ref<AlertRule | null>(null)
const silenceBusy = ref(false)
const silenceError = ref('')

function openSilence(rule: AlertRule): void {
  if (!canWrite.value) return
  silenceTarget.value = rule
  silenceError.value = ''
}

function closeSilence(): void {
  if (silenceBusy.value) return
  silenceTarget.value = null
  silenceError.value = ''
}

async function applySilence(seconds: number): Promise<void> {
  const rule = silenceTarget.value
  if (!rule || !canWrite.value || silenceBusy.value) return
  silenceBusy.value = true
  silenceError.value = ''
  try {
    await api.silenceAlertRule(rule.id, seconds)
    silenceTarget.value = null
    await loadRules()
    rulesNotice.value = t('alerts.rules.silenced')
  } catch (caught) {
    silenceError.value = errorMessage(caught, t('alerts.rules.silenceFailed'))
  } finally {
    silenceBusy.value = false
  }
}

async function unsilenceRule(rule: AlertRule): Promise<void> {
  if (!canWrite.value) return
  rulesActionError.value = ''
  try {
    await api.silenceAlertRule(rule.id, 0)
    await loadRules()
    rulesNotice.value = t('alerts.rules.unsilenced')
  } catch (caught) {
    rulesActionError.value = errorMessage(caught, t('alerts.rules.unsilenceFailed'))
  }
}

/* ------------------------------ 通知渠道 ------------------------------ */

const channels = ref<AlertChannel[]>([])
const channelsState = ref<SectionState>('idle')
const channelsError = ref('')
const channelsActionError = ref('')
const channelsNotice = ref('')
const testingChannelId = ref<string | null>(null)
const channelTest = ref<{ id: string; name: string; ok: boolean; error: string } | null>(null)

async function loadChannels(): Promise<void> {
  if (!canRead.value) return
  channelsState.value = 'loading'
  channelsError.value = ''
  try {
    channels.value = await api.alertChannels()
    channelsState.value = 'loaded'
  } catch (caught) {
    channelsState.value = 'error'
    channelsError.value = errorMessage(caught, t('alerts.channels.loadFailed'))
  }
}

async function refreshActive(): Promise<void> {
  if (tab.value === 'rules') await refreshRules()
  else if (tab.value === 'channels') await loadChannels()
  else await refresh()
}

const refreshingActive = computed(() => {
  if (tab.value === 'rules') return rulesState.value === 'loading'
  if (tab.value === 'channels') return channelsState.value === 'loading'
  return refreshing.value
})

async function runChannelTest(channel: AlertChannel): Promise<void> {
  if (!canWrite.value || testingChannelId.value !== null) return
  testingChannelId.value = channel.id
  channelTest.value = null
  try {
    const result = await api.testAlertChannel(channel.id)
    channelTest.value = {
      id: channel.id,
      name: channel.name,
      ok: result.ok,
      error: result.error ?? '',
    }
    await loadChannels()
  } catch (caught) {
    channelTest.value = {
      id: channel.id,
      name: channel.name,
      ok: false,
      error: errorMessage(caught, t('alerts.channels.testFailed')),
    }
  } finally {
    testingChannelId.value = null
  }
}

function channelLastOkText(channel: AlertChannel): string {
  return channel.last_ok_at === null ? t('alerts.channels.never') : formatDateTime(channel.last_ok_at)
}

/* ------------------------------ 渠道弹窗 ------------------------------ */

interface ChannelFormState {
  name: string
  kind: string
  enabled: boolean
  url: string
  secret: string
  chatId: string
  botToken: string
  to: string
  from: string
  host: string
  port: string | number
  user: string
  password: string
}

function emptyChannelForm(kind = 'webhook'): ChannelFormState {
  return {
    name: '',
    kind,
    enabled: true,
    url: '',
    secret: '',
    chatId: '',
    botToken: '',
    to: '',
    from: '',
    host: '',
    port: '587',
    user: '',
    password: '',
  }
}

const channelModalOpen = ref(false)
const channelEditingId = ref<string | null>(null)
const channelFormError = ref('')
const channelSaving = ref(false)
const channelForm = reactive<ChannelFormState>(emptyChannelForm())
/** 编辑旧渠道时后端已脱敏、因此留空即「不修改」的字段。 */
const maskedKeys = ref<Set<string>>(new Set())

function isMaskedValue(value: unknown): boolean {
  return typeof value === 'string' && (value.includes('***') || value.includes('•••'))
}

function configText(config: Record<string, unknown>, key: string): string {
  const value = config[key]
  if (typeof value === 'string') return isMaskedValue(value) ? '' : value
  if (typeof value === 'number') return String(value)
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === 'string').join(', ')
  }
  return ''
}

function openCreateChannel(): void {
  if (!canWrite.value) return
  channelEditingId.value = null
  maskedKeys.value = new Set()
  Object.assign(channelForm, emptyChannelForm())
  channelFormError.value = ''
  channelModalOpen.value = true
}

function openEditChannel(channel: AlertChannel): void {
  if (!canWrite.value) return
  const config = channel.config ?? {}
  const masked = new Set<string>()
  const kind = normalizeChannelKind(channel.kind)
  const urlKey = kind === 'webhook' ? 'url' : 'webhook'
  if (isMaskedValue(config[urlKey])) masked.add('url')
  if (isMaskedValue(config.secret)) masked.add('secret')
  if (isMaskedValue(config.bot_token)) masked.add('bot_token')
  if (isMaskedValue(config.password)) masked.add('password')

  channelEditingId.value = channel.id
  maskedKeys.value = masked
  Object.assign(channelForm, {
    name: channel.name,
    kind,
    enabled: channel.enabled,
    url: configText(config, urlKey),
    secret: configText(config, 'secret'),
    chatId: configText(config, 'chat_id'),
    botToken: configText(config, 'bot_token'),
    to: configText(config, 'to'),
    from: configText(config, 'from'),
    host: configText(config, 'smtp_host'),
    port: configText(config, 'smtp_port') || '587',
    user: configText(config, 'username'),
    password: configText(config, 'password'),
  } satisfies ChannelFormState)
  channelFormError.value = ''
  channelModalOpen.value = true
}

function closeChannelModal(): void {
  if (channelSaving.value) return
  channelModalOpen.value = false
  channelFormError.value = ''
}

/** 多个收件人按逗号/空白切分，去空去重。 */
function splitRecipients(raw: string): string[] {
  const parts = raw
    .split(/[,;\s]+/)
    .map((item) => item.trim())
    .filter((item) => item !== '')
  return Array.from(new Set(parts))
}

function validateChannelForm(form: ChannelFormState): string | null {
  if (form.name.trim() === '') return 'alerts.channels.requiredName'
  if (!(CHANNEL_KINDS as readonly string[]).includes(form.kind)) return 'alerts.channels.requiredName'
  if (form.kind === 'email') {
    if (splitRecipients(form.to).length === 0) return 'alerts.channels.requiredTo'
    if (form.from.trim() === '') return 'alerts.channels.requiredFrom'
    if (form.host.trim() === '') return 'alerts.channels.requiredHost'
    const rawPort = String(form.port).trim()
    const port = Number(rawPort)
    if (rawPort === '' || !Number.isInteger(port) || port < 1 || port > 65535) {
      return 'alerts.channels.invalidPort'
    }
    return null
  }
  if (form.kind === 'telegram') {
    if (form.chatId.trim() === '') return 'alerts.channels.requiredChatId'
    if (form.botToken.trim() === '' && !maskedKeys.value.has('bot_token')) {
      return 'alerts.channels.requiredBotToken'
    }
    return null
  }
  if (form.url.trim() === '' && !maskedKeys.value.has('url')) return 'alerts.channels.requiredUrl'
  return null
}

/** 按 kind 收敛 config：只带非空字段，编辑时空字段由后端 PATCH 合并逻辑保留原值。 */
function buildChannelConfig(form: ChannelFormState): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  const put = (key: string, value: string): void => {
    const trimmed = value.trim()
    if (trimmed !== '') config[key] = trimmed
  }
  if (form.kind === 'email') {
    const recipients = splitRecipients(form.to)
    if (recipients.length > 0) config.to = recipients
    put('from', form.from)
    put('smtp_host', form.host)
    const port = Number(String(form.port).trim())
    if (Number.isInteger(port) && port > 0) config.smtp_port = port
    put('username', form.user)
    put('password', form.password)
    return config
  }
  if (form.kind === 'telegram') {
    put('chat_id', form.chatId)
    put('bot_token', form.botToken)
    return config
  }
  if (form.kind === 'webhook') put('url', form.url)
  else put('webhook', form.url)
  if (form.kind === 'dingtalk' || form.kind === 'feishu') put('secret', form.secret)
  return config
}

async function submitChannel(): Promise<void> {
  if (!canWrite.value || channelSaving.value) return
  const invalidKey = validateChannelForm(channelForm)
  if (invalidKey !== null) {
    channelFormError.value = t(invalidKey)
    return
  }
  channelFormError.value = ''
  channelSaving.value = true
  channelsActionError.value = ''
  try {
    const payload: AlertChannelPayload = {
      name: channelForm.name.trim(),
      kind: channelForm.kind as ChannelKind,
      enabled: channelForm.enabled,
      config: buildChannelConfig(channelForm),
    }
    if (channelEditingId.value === null) await api.createAlertChannel(payload)
    else await api.updateAlertChannel(channelEditingId.value, payload)
    channelModalOpen.value = false
    await loadChannels()
    channelsNotice.value = t('alerts.channels.saved')
  } catch (caught) {
    channelFormError.value = errorMessage(caught, t('alerts.channels.saveFailed'))
  } finally {
    channelSaving.value = false
  }
}

/* ------------------------------ 删除二次确认（规则/渠道共用） ------------------------------ */

type DeleteTarget =
  | { kind: 'rule'; id: string; name: string }
  | { kind: 'channel'; id: string; name: string }

const deleteTarget = ref<DeleteTarget | null>(null)
/** 二次确认里展示的影响数量：规则=绑定渠道数，渠道=引用它的规则数 */
const deleteUsage = ref<number | null>(null)
const deleting = ref(false)
const deleteError = ref('')

function countRulesUsingChannel(channelId: string): number {
  return rules.value.filter((rule) => rule.channel_ids.includes(channelId)).length
}

function askDeleteRule(rule: AlertRule): void {
  if (!canWrite.value) return
  deleteTarget.value = { kind: 'rule', id: rule.id, name: rule.name }
  deleteUsage.value = rule.channel_ids.length
  deleteError.value = ''
}

async function askDeleteChannel(channel: AlertChannel): Promise<void> {
  if (!canWrite.value) return
  deleteTarget.value = { kind: 'channel', id: channel.id, name: channel.name }
  deleteUsage.value = null
  deleteError.value = ''
  // 影响范围要给出数量：规则已加载就直接算，否则先取一次（无 alert.read 时宁可不显示数量）
  if (!can('alert.read') || rulesState.value === 'loading') return
  if (rulesState.value !== 'loaded') await loadRules()
  if (rulesState.value !== 'loaded') return
  if (deleteTarget.value?.kind !== 'channel' || deleteTarget.value.id !== channel.id) return
  deleteUsage.value = countRulesUsingChannel(channel.id)
}

function closeDelete(): void {
  if (deleting.value) return
  deleteTarget.value = null
  deleteUsage.value = null
  deleteError.value = ''
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || !canWrite.value || deleting.value) return
  deleting.value = true
  deleteError.value = ''
  try {
    if (target.kind === 'rule') {
      await api.deleteAlertRule(target.id)
      deleteTarget.value = null
      await loadRules()
      rulesNotice.value = t('alerts.rules.deleted')
    } else {
      await api.deleteAlertChannel(target.id)
      deleteTarget.value = null
      await loadChannels()
      channelsNotice.value = t('alerts.channels.deleted')
    }
  } catch (caught) {
    deleteError.value = errorMessage(
      caught,
      target.kind === 'rule' ? t('alerts.rules.deleteFailed') : t('alerts.channels.deleteFailed'),
    )
  } finally {
    deleting.value = false
  }
}

/* 进页面/切 tab 时按需加载；events 沿用 useAlerts 自己的轮询。 */
watch(
  tab,
  (value) => {
    // 无 alert.read 时整页只渲染提示，连规则/渠道的读请求也不发
    if (!canRead.value) return
    if (value === 'rules') void refreshRules()
    else if (value === 'channels') void loadChannels()
  },
  { immediate: true },
)
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
          <p class="ls-alertspage__subtitle">
            {{
              tab === 'rules'
                ? t('alerts.rules.subtitle')
                : tab === 'channels'
                  ? t('alerts.channels.subtitle')
                  : t('alerts.subtitle')
            }}
          </p>
        </div>
        <button
          id="alerts-refresh"
          class="ls-btn ls-alertspage__refresh"
          type="button"
          :disabled="refreshingActive"
          @click="refreshActive"
        >
          <span v-if="refreshingActive" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
      </header>

      <div class="ls-alertspage__tabs" role="tablist" :aria-label="t('alerts.rules.tabsLabel')">
        <button
          v-for="item in ALERTS_TABS"
          :id="`alerts-tab-${item.key}`"
          :key="item.key"
          class="ls-alertspage__tab"
          :class="{ 'ls-alertspage__tab--active': tab === item.key }"
          type="button"
          role="tab"
          :aria-selected="tab === item.key"
          @click="selectTab(item.key)"
        >
          {{ t(item.labelKey) }}
        </button>
      </div>

      <template v-if="tab === 'events'">
      <div class="ls-alertspage__toolbar">
        <div class="ls-alertspage__filters">
          <AppSelect
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
          </AppSelect>

          <AppSelect
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
          </AppSelect>

          <AppSelect
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
          </AppSelect>

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

      <section
        v-else-if="tab === 'rules'"
        id="alerts-panel-rules"
        class="ls-alerts__panel"
        role="tabpanel"
        aria-labelledby="alerts-tab-rules"
      >
        <div class="ls-alerts__panel-head">
          <div class="ls-alerts__panel-heading">
            <h2 class="ls-alerts__panel-title">{{ t('alerts.rules.listTitle') }}</h2>
            <span class="ls-alerts__count">{{ t('alerts.rules.count', { count: rules.length }) }}</span>
          </div>
          <button
            v-if="canWrite"
            id="alerts-rule-create"
            class="ls-btn ls-btn--primary"
            type="button"
            @click="openCreateRule"
          >
            {{ t('alerts.rules.create') }}
          </button>
        </div>

        <p v-if="optionsError" id="alerts-rules-options-hint" class="ls-alerts__hint" role="status">
          {{ optionsError }}
        </p>

        <div v-if="rulesNotice" id="alerts-rules-notice" class="ls-alerts__notice ls-alerts__notice--ok" role="status">
          <p class="ls-alerts__notice-text">{{ rulesNotice }}</p>
          <button class="ls-notice__action" type="button" @click="rulesNotice = ''">
            {{ t('alerts.dismiss') }}
          </button>
        </div>
        <div v-if="rulesActionError" id="alerts-rules-action-error" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__title">{{ t('common.error') }}</p>
          <p class="ls-notice__text">{{ rulesActionError }}</p>
          <button class="ls-notice__action" type="button" @click="rulesActionError = ''">
            {{ t('alerts.dismiss') }}
          </button>
        </div>

        <div
          v-if="rulesState === 'loading' && rules.length === 0"
          id="alerts-rules-skeleton"
          class="ls-alertspage__skeleton"
          aria-busy="true"
          :aria-label="t('common.loading')"
        >
          <span v-for="index in 5" :key="index" class="ls-skeleton ls-alertspage__skeleton-item" />
        </div>

        <div
          v-else-if="rulesState === 'error' && rules.length === 0"
          id="alerts-rules-error"
          class="ls-notice ls-notice--error"
          role="alert"
        >
          <p class="ls-notice__title">{{ t('alerts.rules.loadFailed') }}</p>
          <p class="ls-notice__text">{{ rulesError }}</p>
          <button id="alerts-rules-retry" class="ls-notice__action" type="button" @click="refreshRules">
            {{ t('common.retry') }}
          </button>
        </div>

        <div v-else-if="rules.length === 0" id="alerts-rules-empty" class="ls-notice">
          <p class="ls-notice__title">{{ t('alerts.rules.emptyTitle') }}</p>
          <p class="ls-notice__text">{{ t('alerts.rules.emptyHint') }}</p>
        </div>

        <div v-else class="ls-alertspage__scroll ls-alertspage__scroll--table">
          <table class="ls-alertspage__table ls-alerts__table">
            <caption class="ls-visually-hidden">{{ t('alerts.rules.listTitle') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('alerts.rules.colName') }}</th>
                <th scope="col">{{ t('alerts.rules.colSource') }}</th>
                <th scope="col">{{ t('alerts.rules.colTarget') }}</th>
                <th scope="col">{{ t('alerts.rules.colCondition') }}</th>
                <th scope="col">{{ t('alerts.rules.colSeverity') }}</th>
                <th scope="col">{{ t('alerts.rules.colChannels') }}</th>
                <th scope="col">{{ t('alerts.rules.colEnabled') }}</th>
                <th scope="col">{{ t('alerts.rules.colSilence') }}</th>
                <th scope="col">{{ t('alerts.rules.colActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="rule in rules" :key="rule.id" class="ls-alertspage__row" :data-rule-id="rule.id">
                <td class="ls-alertspage__cell ls-alerts__cell--name" :data-label="t('alerts.rules.colName')">
                  <span class="ls-alerts__name" :title="rule.name">{{ rule.name }}</span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.rules.colSource')">
                  {{ t(ruleSourceLabelKey(rule.source)) }}
                </td>
                <td class="ls-alertspage__cell ls-alerts__cell--target" :data-label="t('alerts.rules.colTarget')">
                  <span :title="ruleTargetText(rule, rulesChecks, rulesServers)">
                    {{ ruleTargetText(rule, rulesChecks, rulesServers) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell ls-alerts__cell--condition" :data-label="t('alerts.rules.colCondition')">
                  {{ ruleConditionText(rule) }}
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.rules.colSeverity')">
                  <span class="ls-alertspage__badge" :class="`ls-tone--${severityTone(rule.severity)}`">
                    {{ t(severityLabelKey(rule.severity)) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell ls-alerts__cell--num" :data-label="t('alerts.rules.colChannels')">
                  {{ t('alerts.rules.channelCount', { count: rule.channel_ids.length }) }}
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.rules.colEnabled')">
                  <label v-if="canWrite" class="ls-alerts__toggle">
                    <input
                      :id="`alerts-rule-enabled-${rule.id}`"
                      type="checkbox"
                      :checked="rule.enabled"
                      :disabled="togglingRuleId !== null"
                      :aria-label="t('alerts.rules.fieldEnabled')"
                      @change="toggleRuleEnabled(rule, $event)"
                    />
                    <span>{{ rule.enabled ? t('common.enabled') : t('common.disabled') }}</span>
                  </label>
                  <span v-else class="ls-alertspage__badge ls-tone--muted">
                    {{ rule.enabled ? t('common.enabled') : t('common.disabled') }}
                  </span>
                </td>
                <td class="ls-alertspage__cell ls-alerts__cell--silence" :data-label="t('alerts.rules.colSilence')">
                  <span v-if="isRuleSilenced(rule, now)" class="ls-alertspage__badge ls-tone--warning">
                    {{ ruleSilenceText(rule, now) }}
                  </span>
                  <span v-else class="ls-alerts__muted">{{ t('alerts.rules.notSilenced') }}</span>
                </td>
                <td class="ls-alertspage__cell ls-alertspage__cell--actions" :data-label="t('alerts.rules.colActions')">
                  <template v-if="canWrite">
                    <button
                      :id="`alerts-rule-edit-${rule.id}`"
                      class="ls-btn ls-btn--small"
                      type="button"
                      @click="openEditRule(rule)"
                    >
                      {{ t('alerts.rules.edit') }}
                    </button>
                    <button
                      v-if="isRuleSilenced(rule, now)"
                      :id="`alerts-rule-unsilence-${rule.id}`"
                      class="ls-btn ls-btn--small"
                      type="button"
                      @click="unsilenceRule(rule)"
                    >
                      {{ t('alerts.rules.unsilence') }}
                    </button>
                    <button
                      v-else
                      :id="`alerts-rule-silence-${rule.id}`"
                      class="ls-btn ls-btn--small"
                      type="button"
                      @click="openSilence(rule)"
                    >
                      {{ t('alerts.rules.silence') }}
                    </button>
                    <button
                      :id="`alerts-rule-delete-${rule.id}`"
                      class="ls-btn ls-btn--small ls-btn--danger"
                      type="button"
                      @click="askDeleteRule(rule)"
                    >
                      {{ t('alerts.rules.delete') }}
                    </button>
                  </template>
                  <span v-else class="ls-alerts__muted">{{ t('alerts.rules.noWriteHint') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section
        v-else
        id="alerts-panel-channels"
        class="ls-alerts__panel"
        role="tabpanel"
        aria-labelledby="alerts-tab-channels"
      >
        <div class="ls-alerts__panel-head">
          <div class="ls-alerts__panel-heading">
            <h2 class="ls-alerts__panel-title">{{ t('alerts.channels.listTitle') }}</h2>
            <span class="ls-alerts__count">{{ t('alerts.channels.count', { count: channels.length }) }}</span>
          </div>
          <button
            v-if="canWrite"
            id="alerts-channel-create"
            class="ls-btn ls-btn--primary"
            type="button"
            @click="openCreateChannel"
          >
            {{ t('alerts.channels.create') }}
          </button>
        </div>

        <div v-if="channelsNotice" id="alerts-channels-notice" class="ls-alerts__notice ls-alerts__notice--ok" role="status">
          <p class="ls-alerts__notice-text">{{ channelsNotice }}</p>
          <button class="ls-notice__action" type="button" @click="channelsNotice = ''">
            {{ t('alerts.dismiss') }}
          </button>
        </div>
        <div v-if="channelsActionError" id="alerts-channels-action-error" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__title">{{ t('common.error') }}</p>
          <p class="ls-notice__text">{{ channelsActionError }}</p>
          <button class="ls-notice__action" type="button" @click="channelsActionError = ''">
            {{ t('alerts.dismiss') }}
          </button>
        </div>

        <div
          v-if="channelTest"
          id="alerts-channel-test-result"
          class="ls-alerts__notice"
          :class="channelTest.ok ? 'ls-alerts__notice--ok' : 'ls-alerts__notice--error'"
          role="status"
        >
          <p class="ls-alerts__notice-title">
            {{ channelTest.ok ? t('alerts.channels.testOk') : t('alerts.channels.testFailed') }}
          </p>
          <p class="ls-alerts__notice-text">
            <span class="ls-alerts__notice-name">{{ channelTest.name }}</span>
            <span v-if="!channelTest.ok" class="ls-alerts__notice-detail">
              {{ channelTest.error === '' ? t('alerts.channels.testFailed') : channelTest.error }}
            </span>
          </p>
          <button class="ls-notice__action" type="button" @click="channelTest = null">
            {{ t('alerts.dismiss') }}
          </button>
        </div>

        <div
          v-if="channelsState === 'loading' && channels.length === 0"
          id="alerts-channels-skeleton"
          class="ls-alertspage__skeleton"
          aria-busy="true"
          :aria-label="t('common.loading')"
        >
          <span v-for="index in 4" :key="index" class="ls-skeleton ls-alertspage__skeleton-item" />
        </div>

        <div
          v-else-if="channelsState === 'error' && channels.length === 0"
          id="alerts-channels-error"
          class="ls-notice ls-notice--error"
          role="alert"
        >
          <p class="ls-notice__title">{{ t('alerts.channels.loadFailed') }}</p>
          <p class="ls-notice__text">{{ channelsError }}</p>
          <button id="alerts-channels-retry" class="ls-notice__action" type="button" @click="loadChannels">
            {{ t('common.retry') }}
          </button>
        </div>

        <div v-else-if="channels.length === 0" id="alerts-channels-empty" class="ls-notice">
          <p class="ls-notice__title">{{ t('alerts.channels.emptyTitle') }}</p>
          <p class="ls-notice__text">{{ t('alerts.channels.emptyHint') }}</p>
        </div>

        <div v-else class="ls-alertspage__scroll ls-alertspage__scroll--table">
          <table class="ls-alertspage__table ls-alerts__table">
            <caption class="ls-visually-hidden">{{ t('alerts.channels.listTitle') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('alerts.channels.colName') }}</th>
                <th scope="col">{{ t('alerts.channels.colType') }}</th>
                <th scope="col">{{ t('alerts.channels.colEnabled') }}</th>
                <th scope="col">{{ t('alerts.channels.colLastOk') }}</th>
                <th scope="col">{{ t('alerts.channels.colLastError') }}</th>
                <th scope="col">{{ t('alerts.channels.colActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="channel in channels" :key="channel.id" class="ls-alertspage__row" :data-channel-id="channel.id">
                <td class="ls-alertspage__cell ls-alerts__cell--name" :data-label="t('alerts.channels.colName')">
                  <span class="ls-alerts__name" :title="channel.name">{{ channel.name }}</span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.channels.colType')">
                  {{ t(channelKindLabelKey(normalizeChannelKind(channel.kind))) }}
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.channels.colEnabled')">
                  <span
                    class="ls-alertspage__badge"
                    :class="channel.enabled ? 'ls-tone--success' : 'ls-tone--muted'"
                  >
                    {{ channel.enabled ? t('common.enabled') : t('common.disabled') }}
                  </span>
                </td>
                <td class="ls-alertspage__cell" :data-label="t('alerts.channels.colLastOk')">
                  {{ channelLastOkText(channel) }}
                </td>
                <td class="ls-alertspage__cell ls-alerts__cell--error" :data-label="t('alerts.channels.colLastError')">
                  <span
                    class="ls-alerts__error-text"
                    :class="{ 'ls-alerts__error-text--bad': (channel.last_error ?? '').trim() !== '' }"
                    :title="channelErrorText(channel)"
                  >
                    {{ channelErrorText(channel) }}
                  </span>
                </td>
                <td class="ls-alertspage__cell ls-alertspage__cell--actions" :data-label="t('alerts.channels.colActions')">
                  <template v-if="canWrite">
                    <button
                      :id="`alerts-channel-test-${channel.id}`"
                      class="ls-btn ls-btn--small"
                      type="button"
                      :disabled="testingChannelId !== null"
                      @click="runChannelTest(channel)"
                    >
                      <span v-if="testingChannelId === channel.id" class="ls-spinner" aria-hidden="true" />
                      {{ testingChannelId === channel.id ? t('alerts.channels.testing') : t('alerts.channels.test') }}
                    </button>
                    <button
                      :id="`alerts-channel-edit-${channel.id}`"
                      class="ls-btn ls-btn--small"
                      type="button"
                      @click="openEditChannel(channel)"
                    >
                      {{ t('alerts.channels.edit') }}
                    </button>
                    <button
                      :id="`alerts-channel-delete-${channel.id}`"
                      class="ls-btn ls-btn--small ls-btn--danger"
                      type="button"
                      @click="askDeleteChannel(channel)"
                    >
                      {{ t('alerts.channels.delete') }}
                    </button>
                  </template>
                  <span v-else class="ls-alerts__muted">{{ t('alerts.channels.noWriteHint') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <AlertDetailDrawer v-if="drawerAlert" :alert="drawerAlert" @close="closeDrawer" />

    <AppModal
      v-if="ruleModalOpen"
      :title="ruleEditingId === null ? t('alerts.rules.formCreateTitle') : t('alerts.rules.formEditTitle')"
      :description="t(ruleSourceLabelKey(ruleForm.source))"
      size="lg"
      @close="closeRuleModal"
    >
      <form id="alerts-rule-form" class="ls-adminpage__form" novalidate @submit.prevent="submitRule">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('alerts.rules.fieldName') }}</span>
          <input id="alerts-rule-name" v-model="ruleForm.name" class="ls-input" type="text" autocomplete="off" />
        </label>

        <div class="ls-form__row">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.rules.fieldSource') }}</span>
            <AppSelect id="alerts-rule-source" v-model="ruleForm.source" class="ls-input">
              <option v-for="source in RULE_SOURCES" :key="source" :value="source">
                {{ t(ruleSourceLabelKey(source)) }}
              </option>
            </AppSelect>
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.rules.fieldSeverity') }}</span>
            <AppSelect id="alerts-rule-severity" v-model="ruleForm.severity" class="ls-input">
              <option v-for="severity in ALERT_SEVERITIES" :key="severity" :value="severity">
                {{ t(severityLabelKey(severity)) }}
              </option>
            </AppSelect>
          </label>
        </div>

        <fieldset v-if="ruleForm.source === 'check'" class="ls-adminpage__fieldset">
          <legend class="ls-field__label">{{ t('alerts.rules.fieldChecks') }}</legend>
          <p v-if="rulesChecks.length === 0" class="ls-field__hint">{{ t('alerts.rules.noChecks') }}</p>
          <div v-else class="ls-alerts__options">
            <label v-for="check in rulesChecks" :key="check.id" class="ls-adminpage__option">
              <input
                type="checkbox"
                :value="check.id"
                :checked="ruleForm.checkIds.includes(check.id)"
                @change="toggleCheckId(check.id)"
              />
              <span>{{ check.name }}</span>
            </label>
          </div>
        </fieldset>

        <fieldset v-if="ruleForm.source === 'server'" class="ls-adminpage__fieldset">
          <legend class="ls-field__label">{{ t('alerts.rules.fieldServers') }}</legend>
          <p v-if="rulesServers.length === 0" class="ls-field__hint">{{ t('alerts.rules.noServers') }}</p>
          <div v-else class="ls-alerts__options">
            <label v-for="server in rulesServers" :key="server.id" class="ls-adminpage__option">
              <input
                type="checkbox"
                :value="server.id"
                :checked="ruleForm.serverIds.includes(server.id)"
                @change="toggleServerId(server.id)"
              />
              <span>{{ server.name }}</span>
              <span class="ls-adminpage__meta">{{ server.host }}</span>
            </label>
          </div>
        </fieldset>

        <template v-if="ruleForm.source === 'metric'">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.rules.fieldMetric') }}</span>
            <input id="alerts-rule-metric" v-model="ruleForm.metric" class="ls-input" type="text" autocomplete="off" />
          </label>
          <div class="ls-form__row">
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.rules.fieldComparator') }}</span>
              <AppSelect id="alerts-rule-comparator" v-model="ruleForm.comparator" class="ls-input">
                <option v-for="comparator in ALERT_COMPARATORS" :key="comparator" :value="comparator">
                  {{ comparator }}
                </option>
              </AppSelect>
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.rules.fieldThreshold') }}</span>
              <input
                id="alerts-rule-threshold"
                v-model="ruleForm.threshold"
                class="ls-input"
                type="number"
                step="any"
              />
            </label>
          </div>
        </template>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('alerts.rules.fieldDuration') }}</span>
          <input
            id="alerts-rule-duration"
            v-model="ruleForm.forDuration"
            class="ls-input"
            type="number"
            min="0"
            max="604800"
            step="1"
          />
          <span class="ls-field__hint">{{ t('alerts.rules.durationHint') }}</span>
        </label>

        <fieldset class="ls-adminpage__fieldset">
          <legend class="ls-field__label">{{ t('alerts.rules.fieldChannels') }}</legend>
          <p v-if="channels.length === 0" class="ls-field__hint">{{ t('alerts.rules.noChannels') }}</p>
          <div v-else class="ls-alerts__options">
            <label v-for="channel in channels" :key="channel.id" class="ls-adminpage__option">
              <input
                type="checkbox"
                :value="channel.id"
                :checked="ruleForm.channelIds.includes(channel.id)"
                @change="toggleChannelId(channel.id)"
              />
              <span>{{ channel.name }}</span>
              <span class="ls-adminpage__meta">{{ t(channelKindLabelKey(normalizeChannelKind(channel.kind))) }}</span>
            </label>
          </div>
        </fieldset>

        <label class="ls-alerts__toggle">
          <input id="alerts-rule-enabled" v-model="ruleForm.enabled" type="checkbox" />
          <span>{{ t('alerts.rules.fieldEnabled') }}</span>
        </label>

        <p v-if="ruleFormError" id="alerts-rule-form-error" class="ls-adminpage__form-error" role="alert">
          {{ ruleFormError }}
        </p>
      </form>

      <template #footer>
        <button id="alerts-rule-cancel" class="ls-btn" type="button" :disabled="ruleSaving" @click="closeRuleModal">
          {{ t('common.cancel') }}
        </button>
        <button
          id="alerts-rule-save"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="alerts-rule-form"
          :disabled="ruleSaving"
        >
          {{ ruleSaving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="silenceTarget"
      :title="t('alerts.rules.silenceTitle')"
      :description="silenceTarget.name"
      size="sm"
      @close="closeSilence"
    >
      <p class="ls-alerts__modal-text">{{ t('alerts.rules.silenceBody') }}</p>
      <p v-if="isRuleSilenced(silenceTarget, now)" class="ls-field__hint">
        {{ ruleSilenceText(silenceTarget, now) }}
      </p>
      <p v-if="silenceError" id="alerts-silence-error" class="ls-adminpage__form-error" role="alert">
        {{ silenceError }}
      </p>
      <div class="ls-alerts__silence-options">
        <button
          v-for="option in SILENCE_OPTIONS"
          :id="`alerts-silence-${option.seconds}`"
          :key="option.seconds"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="silenceBusy"
          @click="applySilence(option.seconds)"
        >
          {{ t(option.labelKey) }}
        </button>
      </div>
      <template #footer>
        <button id="alerts-silence-cancel" class="ls-btn" type="button" :disabled="silenceBusy" @click="closeSilence">
          {{ t('common.cancel') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="deleteTarget.kind === 'rule' ? t('alerts.rules.deleteTitle') : t('alerts.channels.deleteTitle')"
      :description="deleteTarget.name"
      size="sm"
      @close="closeDelete"
    >
      <p class="ls-alerts__modal-text">
        {{
          deleteTarget.kind === 'rule'
            ? t('alerts.rules.deleteBody', { name: deleteTarget.name })
            : t('alerts.channels.deleteBody', { name: deleteTarget.name })
        }}
      </p>
      <p v-if="deleteUsage !== null" id="alerts-delete-usage" class="ls-alerts__modal-text ls-alerts__muted">
        {{
          deleteTarget.kind === 'rule'
            ? t('alerts.rules.deleteUsage', { count: deleteUsage })
            : t('alerts.channels.deleteUsage', { count: deleteUsage })
        }}
      </p>
      <p v-if="deleteError" id="alerts-delete-error" class="ls-adminpage__form-error" role="alert">
        {{ deleteError }}
      </p>
      <template #footer>
        <button id="alerts-delete-cancel" class="ls-btn" type="button" :disabled="deleting" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="alerts-delete-confirm"
          class="ls-btn ls-btn--danger"
          type="button"
          :disabled="deleting"
          @click="confirmDelete"
        >
          {{
            deleting
              ? t('common.loading')
              : deleteTarget.kind === 'rule'
                ? t('alerts.rules.deleteConfirm')
                : t('alerts.channels.deleteConfirm')
          }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="channelModalOpen"
      :title="channelEditingId === null ? t('alerts.channels.formCreateTitle') : t('alerts.channels.formEditTitle')"
      size="lg"
      @close="closeChannelModal"
    >
      <form id="alerts-channel-form" class="ls-adminpage__form" novalidate @submit.prevent="submitChannel">
        <div class="ls-form__row">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldName') }}</span>
            <input
              id="alerts-channel-name"
              v-model="channelForm.name"
              class="ls-input"
              type="text"
              autocomplete="off"
            />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldKind') }}</span>
            <AppSelect id="alerts-channel-kind" v-model="channelForm.kind" class="ls-input">
              <option v-for="kind in CHANNEL_KINDS" :key="kind" :value="kind">
                {{ t(channelKindLabelKey(kind)) }}
              </option>
            </AppSelect>
          </label>
        </div>

        <template v-if="channelForm.kind === 'email'">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldTo') }}</span>
            <input id="alerts-channel-to" v-model="channelForm.to" class="ls-input" type="text" autocomplete="off" />
            <span class="ls-field__hint">{{ t('alerts.channels.toHint') }}</span>
          </label>
          <div class="ls-form__row">
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.channels.fieldFrom') }}</span>
              <input
                id="alerts-channel-from"
                v-model="channelForm.from"
                class="ls-input"
                type="text"
                autocomplete="off"
              />
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.channels.fieldHost') }}</span>
              <input
                id="alerts-channel-host"
                v-model="channelForm.host"
                class="ls-input"
                type="text"
                autocomplete="off"
              />
            </label>
          </div>
          <div class="ls-form__row">
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.channels.fieldPort') }}</span>
              <input
                id="alerts-channel-port"
                v-model="channelForm.port"
                class="ls-input"
                type="number"
                min="1"
                max="65535"
                step="1"
              />
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('alerts.channels.fieldUser') }}</span>
              <input
                id="alerts-channel-user"
                v-model="channelForm.user"
                class="ls-input"
                type="text"
                autocomplete="off"
              />
            </label>
          </div>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldPassword') }}</span>
            <input
              id="alerts-channel-password"
              v-model="channelForm.password"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :placeholder="maskedKeys.has('password') ? t('alerts.channels.secretPlaceholder') : ''"
            />
            <span v-if="maskedKeys.has('password')" class="ls-field__hint">
              {{ t('alerts.channels.secretHint') }}
            </span>
          </label>
        </template>

        <template v-else-if="channelForm.kind === 'telegram'">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldChatId') }}</span>
            <input
              id="alerts-channel-chat-id"
              v-model="channelForm.chatId"
              class="ls-input"
              type="text"
              autocomplete="off"
            />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldBotToken') }}</span>
            <input
              id="alerts-channel-bot-token"
              v-model="channelForm.botToken"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :placeholder="maskedKeys.has('bot_token') ? t('alerts.channels.secretPlaceholder') : ''"
            />
            <span v-if="maskedKeys.has('bot_token')" class="ls-field__hint">
              {{ t('alerts.channels.secretHint') }}
            </span>
          </label>
        </template>

        <template v-else>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldUrl') }}</span>
            <input
              id="alerts-channel-url"
              v-model="channelForm.url"
              class="ls-input"
              type="url"
              autocomplete="off"
              :placeholder="maskedKeys.has('url') ? t('alerts.channels.secretPlaceholder') : t('alerts.channels.urlPlaceholder')"
            />
            <span v-if="maskedKeys.has('url')" class="ls-field__hint">{{ t('alerts.channels.secretHint') }}</span>
          </label>
          <label v-if="channelForm.kind === 'dingtalk' || channelForm.kind === 'feishu'" class="ls-field">
            <span class="ls-field__label">{{ t('alerts.channels.fieldSecret') }}</span>
            <input
              id="alerts-channel-secret"
              v-model="channelForm.secret"
              class="ls-input"
              type="password"
              autocomplete="new-password"
              :placeholder="maskedKeys.has('secret') ? t('alerts.channels.secretPlaceholder') : ''"
            />
            <span v-if="maskedKeys.has('secret')" class="ls-field__hint">
              {{ t('alerts.channels.secretHint') }}
            </span>
          </label>
        </template>

        <label class="ls-alerts__toggle">
          <input id="alerts-channel-enabled" v-model="channelForm.enabled" type="checkbox" />
          <span>{{ t('alerts.channels.fieldEnabled') }}</span>
        </label>

        <p v-if="channelFormError" id="alerts-channel-form-error" class="ls-adminpage__form-error" role="alert">
          {{ channelFormError }}
        </p>
      </form>

      <template #footer>
        <button
          id="alerts-channel-cancel"
          class="ls-btn"
          type="button"
          :disabled="channelSaving"
          @click="closeChannelModal"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          id="alerts-channel-save"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="alerts-channel-form"
          :disabled="channelSaving"
        >
          {{ channelSaving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
