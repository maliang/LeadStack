/**
 * 控制端 REST 客户端。
 *
 * 契约见 docs/ARCHITECTURE.md 第 3 节：
 *   - 成功：{"data": ...}；失败：{"error":{"code","message"}}
 *   - 除 /auth/login 外均需 Authorization: Bearer <jwt>
 *   - 所有路径前缀 /api/v1（/healthz 除外）
 *   - 时间戳统一为 Unix 毫秒
 *
 * 开发态由 vite.config.ts 把 /api 代理到 http://127.0.0.1:8080；
 * 生产态与 API 同源（Go 二进制同一端口提供）。
 */

const API_PREFIX = '/api/v1'
const TOKEN_STORAGE_KEY = 'leadstack_token'

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

/* ------------------------------------------------------------------ */
/* 登录态存储                                                          */
/* ------------------------------------------------------------------ */

export function getToken(): string | null {
  try {
    return window.localStorage.getItem(TOKEN_STORAGE_KEY)
  } catch {
    return null
  }
}

export function setToken(token: string): void {
  try {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, token)
  } catch {
    /* 隐私模式下忽略，内存态仍可用 */
  }
}

export function clearToken(): void {
  try {
    window.localStorage.removeItem(TOKEN_STORAGE_KEY)
  } catch {
    /* ignore */
  }
}

type UnauthorizedHandler = () => void
let unauthorizedHandler: UnauthorizedHandler | null = null

/** 任意请求收到 401 时触发（登录接口除外），用于自动登出。 */
export function onUnauthorized(handler: UnauthorizedHandler): () => void {
  unauthorizedHandler = handler
  return () => {
    if (unauthorizedHandler === handler) unauthorizedHandler = null
  }
}

/* ------------------------------------------------------------------ */
/* 基础请求                                                            */
/* ------------------------------------------------------------------ */

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE'
  body?: unknown
  auth?: boolean
  signal?: AbortSignal
}

interface ErrorEnvelope {
  error?: {
    code?: string
    message?: string
  }
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, auth = true, signal } = options
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const token = getToken()
  if (auth && token) headers.Authorization = `Bearer ${token}`

  let response: Response
  try {
    response = await fetch(`${API_PREFIX}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(0, 'network_error', '无法连接控制端，请确认 LeadStack 服务已启动')
  }

  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const envelope = payload as ErrorEnvelope | null
    const apiError = new ApiError(
      response.status,
      envelope?.error?.code ?? 'http_error',
      envelope?.error?.message ?? `请求失败（HTTP ${response.status}）`,
    )
    if (response.status === 401 && auth) {
      clearToken()
      unauthorizedHandler?.()
    }
    throw apiError
  }

  if (payload !== null && typeof payload === 'object' && 'data' in payload) {
    return (payload as { data: T }).data
  }
  return payload as T
}

/* ------------------------------------------------------------------ */
/* 数据类型（字段名严格对齐契约第 3 节）                                 */
/* ------------------------------------------------------------------ */

export type ServerStatus = 'online' | 'offline' | 'unknown'

export interface User {
  id: string
  username: string
  role: string
}

export interface LoginResult {
  token: string
  user: User
}

export interface SystemInfo {
  version: string
  uptime_s: number
  server_count: number
  online_count: number
}

/** 列表/详情里附带的最新指标快照（契约 3.1，map[string]float64）。 */
export interface MetricsSummary {
  cpu_pct: number | null
  mem_used_pct: number | null
  mem_used_bytes: number | null
  disk_used_pct: number | null
  load1: number | null
  uptime_s: number | null
}

/** 主机信息（契约 8.3 hostinfo.Info / 第 10 节 host_info）。 */
export interface HostInfo {
  hostname: string | null
  os: string | null
  os_version: string | null
  kernel: string | null
  arch: string | null
  cpu_model: string | null
  cpu_cores: number | null
  mem_total_bytes: number | null
  disk_total_bytes: number | null
  disk_used_bytes: number | null
  uptime_s: number | null
  timezone: string | null
  docker: boolean
  docker_version: string | null
  ip_addrs: string[]
  collected_at: number | null
}

export interface Server {
  id: string
  name: string
  host: string
  port: number | null
  mode: string
  status: ServerStatus
  last_seen_at: number | null
  agent_version: string | null
  os: string | null
  arch: string | null
  kernel: string | null
  tags: string[]
  metrics_summary: MetricsSummary | null
  created_at: number | null
  /** M1 扩展字段（契约第 10 节） */
  group: string | null
  credential_id: string | null
  last_error: string | null
  host_info: HostInfo | null
}

export type CredentialKind = 'password' | 'key'

/** 凭证对外元数据：接口绝不返回密钥内容。 */
export interface Credential {
  id: string
  name: string
  kind: CredentialKind | string
  username: string
  fingerprint: string | null
  created_at: number | null
  server_count: number
}

export interface CreateCredentialPayload {
  name: string
  kind: CredentialKind
  username: string
  password?: string
  private_key?: string
  passphrase?: string
}

export interface TestConnectionResult {
  ok: boolean
  latency_ms: number | null
  ssh_banner: string | null
  host_key_fingerprint: string | null
  error?: string | null
}

export interface MetricPoint {
  ts: number
  value: number
}

export interface MetricSeries {
  metric: string
  points: MetricPoint[]
}

export type MetricsStep = 'raw' | '1m' | '1h'

export interface MetricsResponse {
  series: MetricSeries[]
  step: string
}

export type BatchAction =
  | 'test-connection'
  | 'refresh-host-info'
  | 'add-tags'
  | 'set-group'
  | 'delete'

export interface BatchResultItem {
  id: string
  ok: boolean
  error?: string | null
}

export interface BatchResponse {
  results: BatchResultItem[]
  summary: { ok: number; failed: number }
}

export interface GroupSummary {
  name: string
  count: number
}

/** GET /servers 过滤参数（契约第 10 节）。 */
export interface ServerFilters {
  group?: string
  tag?: string
  status?: ServerStatus
  mode?: string
}

export interface EnrollToken {
  id: string
  token?: string
  note?: string | null
  expires_at: number
  used_at?: number | null
}

export interface InstallScript {
  command: string
}

/* ------------------------------------------------------------------ */
/* M2：检测（契约第 13/15 节）                                          */
/* ------------------------------------------------------------------ */

export type CheckKind = 'tcp' | 'http' | 'ping' | 'process' | 'script'

/** 状态口径（契约第 15 节）：连续失败 ≥ fail_threshold 才算 down，无结果为 unknown。 */
export type CheckState = 'up' | 'down' | 'unknown'

export interface CheckParams {
  /** http */
  expect_status?: string
  keyword?: string
  method?: string
  insecure?: boolean
  /** tcp */
  timeout_ms?: number
  /** process */
  min_count?: number
  user?: string
  /** script */
  exit_zero_ok?: boolean
  expect_stdout?: string
  /** 状态判定：连续失败次数阈值，默认 3 */
  fail_threshold?: number
  /** 兼容后端可能多带的字段 */
  [key: string]: unknown
}

export interface CheckResult {
  id: number | null
  check_id: string
  server_id: string
  /** 后端在结果里可能带上服务器名（omitempty），用于时间线提示 */
  server_name?: string | null
  ts: number | null
  ok: boolean
  latency_ms: number | null
  status_code: number | null
  message: string | null
}

export interface CheckItem {
  id: string
  name: string
  kind: CheckKind | string
  target: string
  interval_s: number
  timeout_s: number
  params: CheckParams
  enabled: boolean
  created_at: number | null
  server_ids: string[]
  server_count: number
  state: CheckState
  /** 24 小时可用率，百分比 0-100；null 表示暂无数据 */
  availability_24h: number | null
  last_result: CheckResult | null
}

export interface CheckSummary {
  total: number
  up: number
  down: number
  unknown: number
  checks: CheckItem[]
}

export interface CreateCheckPayload {
  name: string
  kind: CheckKind
  target: string
  interval_s: number
  timeout_s: number
  params?: CheckParams
  server_ids?: string[]
}

export interface UpdateCheckPayload {
  name?: string
  target?: string
  interval_s?: number
  timeout_s?: number
  params?: CheckParams
  enabled?: boolean
}

export interface RunCheckResultItem {
  server_id: string
  ok: boolean
  latency_ms: number | null
  message: string | null
}

export interface RunCheckResponse {
  results: RunCheckResultItem[]
  summary: { ok: number; failed: number }
}

export interface CheckResultsQuery {
  server_id?: string
  from?: number
  to?: number
  limit?: number
}

/* ------------------------------------------------------------------ */
/* M3：告警（契约第 18/20 节）                                          */
/* ------------------------------------------------------------------ */

export type AlertSeverity = 'critical' | 'warning' | 'info'
export type AlertSource = 'check' | 'server' | 'metric'
export type AlertStatus = 'firing' | 'resolved' | 'acked'
export type AlertComparator = '>' | '>=' | '<' | '<='

export type ChannelKind =
  | 'webhook'
  | 'email'
  | 'dingtalk'
  | 'wecom'
  | 'feishu'
  | 'telegram'
  | 'slack'

/** 规则（契约 19.1 Rule）。 */
export interface AlertRule {
  id: string
  name: string
  enabled: boolean
  source: AlertSource
  check_ids: string[]
  server_ids: string[]
  groups: string[]
  tags: string[]
  metric: string | null
  comparator: AlertComparator | string | null
  threshold: number | null
  for_duration_s: number
  severity: AlertSeverity
  dedup_window_s: number
  channel_ids: string[]
  silence_until: number | null
  created_at: number | null
}

export interface AlertRulePayload {
  name: string
  enabled: boolean
  source: AlertSource
  check_ids?: string[]
  server_ids?: string[]
  groups?: string[]
  tags?: string[]
  metric?: string
  comparator?: string
  threshold?: number
  for_duration_s: number
  severity: AlertSeverity
  dedup_window_s: number
  channel_ids: string[]
}

/** 渠道（契约 19.2 Channel）。config 由后端脱敏返回，密钥字段不会是明文。 */
export interface AlertChannel {
  id: string
  name: string
  kind: ChannelKind | string
  enabled: boolean
  config: Record<string, unknown>
  last_error: string | null
  last_ok_at: number | null
  created_at: number | null
}

export interface AlertChannelPayload {
  name: string
  kind: ChannelKind
  enabled: boolean
  config: Record<string, unknown>
}

export interface ChannelTestResult {
  ok: boolean
  error: string | null
}

/** 告警事件轨迹（契约 21：alert_events）。 */
export interface AlertEvent {
  id: number | null
  kind: string
  ts: number | null
  detail: string | null
}

export interface AlertItem {
  id: string
  rule_id: string
  dedup_key: string
  server_id: string | null
  check_id: string | null
  status: AlertStatus
  severity: AlertSeverity
  title: string
  message: string | null
  value: number | null
  started_at: number | null
  updated_at: number | null
  resolved_at: number | null
  acked_at: number | null
  acked_by: string | null
  notify_count: number
  last_notify_at: number | null
  silence_until: number | null
  /** 列表/详情可能带出规则名与服务器名，缺失时前端用本地列表补 */
  rule_name: string | null
  server_name: string | null
  check_name: string | null
  events: AlertEvent[]
}

export interface AlertSummary {
  firing: number
  acked: number
  resolved_24h: number
  by_severity: { critical: number; warning: number; info: number }
}

export interface AlertFilters {
  status?: AlertStatus | ''
  severity?: AlertSeverity | ''
  rule_id?: string
  server_id?: string
  from?: number
  to?: number
  limit?: number
}

/* ------------------------------------------------------------------ */
/* 响应归一化：后端并行开发中，字段可能多给/少给，UI 要稳                */
/* ------------------------------------------------------------------ */

type RawRecord = Record<string, unknown>

function toRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as RawRecord)
    : null
}

function toNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return null
}

function toText(value: unknown): string | null {
  return typeof value === 'string' && value.trim() !== '' ? value : null
}

// 指标 key 以协议点分命名为准（契约 3.1），同时兼容 M0 期间出现过的下划线别名。
export const METRIC_KEYS = {
  cpu: ['cpu.pct', 'cpu_pct', 'cpu', 'cpu_percent'],
  mem: ['mem.used_pct', 'mem_used_pct', 'mem.pct', 'mem', 'mem_percent', 'memory_used_pct'],
  memBytes: ['mem.used_bytes', 'mem_used_bytes'],
  disk: ['disk.used.pct', 'disk.used_pct', 'disk_used_pct', 'disk.pct', 'disk', 'disk_percent'],
  load: ['load.1', 'load1', 'load'],
  uptime: ['uptime_s', 'uptime'],
  netRx: ['net.rx_bytes', 'net_rx_bytes'],
  netTx: ['net.tx_bytes', 'net_tx_bytes'],
  proc: ['proc.count', 'proc_count'],
} as const

/** 兼容 {cpu_pct:..} / {"cpu.pct":..} / [{metric,value,ts}] 三种形态。 */
function normalizeMetrics(input: unknown): MetricsSummary | null {
  const raw = toRecord(input)
  if (!raw) return null

  // 形如 {samples:[{metric:"cpu.pct", value:12.3, ts:...}]}
  if (Array.isArray(raw.samples)) {
    const byMetric: RawRecord = {}
    for (const sample of raw.samples) {
      const item = toRecord(sample)
      if (!item) continue
      const metric = toText(item.metric)
      const value = toNumber(item.value)
      if (metric && value !== null) byMetric[metric] = value
    }
    return normalizeMetrics(byMetric)
  }

  const pick = (keys: readonly string[]): number | null => {
    for (const key of keys) {
      const value = toNumber(raw[key])
      if (value !== null) return value
    }
    return null
  }

  const metrics: MetricsSummary = {
    cpu_pct: pick(METRIC_KEYS.cpu),
    mem_used_pct: pick(METRIC_KEYS.mem),
    mem_used_bytes: pick(METRIC_KEYS.memBytes),
    disk_used_pct: pick(METRIC_KEYS.disk),
    load1: pick(METRIC_KEYS.load),
    uptime_s: pick(METRIC_KEYS.uptime),
  }
  const hasAny = Object.values(metrics).some((value) => value !== null)
  return hasAny ? metrics : null
}

function normalizeTags(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((tag): tag is string => typeof tag === 'string' && tag.trim() !== '')
  }
  if (typeof value === 'string' && value.trim() !== '') {
    try {
      return normalizeTags(JSON.parse(value))
    } catch {
      return value
        .split(',')
        .map((tag) => tag.trim())
        .filter(Boolean)
    }
  }
  return []
}

function normalizeStatus(value: unknown, lastSeenAt: number | null): ServerStatus {
  if (value === 'online' || value === 'offline' || value === 'unknown') return value
  if (lastSeenAt === null) return 'unknown'
  return Date.now() - lastSeenAt <= 45_000 ? 'online' : 'offline'
}

function normalizeHostInfo(input: unknown): HostInfo | null {
  const raw = toRecord(input)
  if (!raw) return null
  const ipAddrs = Array.isArray(raw.ip_addrs)
    ? raw.ip_addrs.filter((ip): ip is string => typeof ip === 'string' && ip.trim() !== '')
    : []
  return {
    hostname: toText(raw.hostname),
    os: toText(raw.os),
    os_version: toText(raw.os_version),
    kernel: toText(raw.kernel),
    arch: toText(raw.arch),
    cpu_model: toText(raw.cpu_model),
    cpu_cores: toNumber(raw.cpu_cores),
    mem_total_bytes: toNumber(raw.mem_total_bytes),
    disk_total_bytes: toNumber(raw.disk_total_bytes),
    disk_used_bytes: toNumber(raw.disk_used_bytes),
    uptime_s: toNumber(raw.uptime_s),
    timezone: toText(raw.timezone),
    docker: raw.docker === true,
    docker_version: toText(raw.docker_version),
    ip_addrs: ipAddrs,
    collected_at: toNumber(raw.collected_at),
  }
}

function normalizeMetricSeries(input: unknown): MetricSeries {
  const raw = toRecord(input) ?? {}
  const points = Array.isArray(raw.points)
    ? raw.points
        .map((point) => {
          const record = toRecord(point)
          if (!record) return null
          const ts = toNumber(record.ts)
          const value = toNumber(record.value)
          if (ts === null || value === null) return null
          return { ts, value }
        })
        .filter((point): point is MetricPoint => point !== null)
    : []
  return { metric: toText(raw.metric) ?? '', points }
}

/* ---------------------- M2 检测：归一化 ---------------------- */

function toBool(value: unknown, fallback = false): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') {
    const lowered = value.trim().toLowerCase()
    if (['true', '1', 'yes', 'on'].includes(lowered)) return true
    if (['false', '0', 'no', 'off'].includes(lowered)) return false
  }
  return fallback
}

function extractList(input: unknown, keys: string[]): unknown[] {
  if (Array.isArray(input)) return input
  const raw = toRecord(input)
  if (!raw) return []
  for (const key of keys) {
    const value = raw[key]
    if (Array.isArray(value)) return value
  }
  return []
}

function normalizeParams(value: unknown): CheckParams {
  if (typeof value === 'string' && value.trim() !== '') {
    try {
      return normalizeParams(JSON.parse(value))
    } catch {
      return {}
    }
  }
  const raw = toRecord(value)
  return raw ? (raw as CheckParams) : {}
}

function normalizeCheckResult(input: unknown): CheckResult | null {
  const raw = toRecord(input)
  if (!raw) return null
  return {
    id: toNumber(raw.id),
    check_id: toText(raw.check_id) ?? '',
    server_id: toText(raw.server_id) ?? '',
    server_name: toText(raw.server_name),
    ts: toNumber(raw.ts),
    ok: toBool(raw.ok),
    latency_ms: toNumber(raw.latency_ms ?? raw.latency),
    status_code: toNumber(raw.status_code ?? raw.code),
    message: toText(raw.message),
  }
}

function normalizeCheckState(value: unknown, lastResult: CheckResult | null, params: CheckParams): CheckState {
  if (value === 'up' || value === 'down' || value === 'unknown') return value
  if (lastResult === null) return 'unknown'
  if (lastResult.ok) return 'up'
  const threshold = toNumber(params.fail_threshold) ?? 3
  // 后端未给状态时：只有明确给出连续失败次数且达到阈值才判 down，否则保持 up（避免误报）
  const raw = toRecord(value)
  if (raw) {
    const consecutive = toNumber(raw.consecutive_fail ?? raw.consecutive_failures)
    if (consecutive !== null) return consecutive >= threshold ? 'down' : 'up'
  }
  return threshold <= 1 ? 'down' : 'up'
}

export function normalizeCheck(input: unknown): CheckItem {
  const raw = toRecord(input) ?? {}
  const params = normalizeParams(raw.params ?? raw.params_json)
  const lastResult =
    normalizeCheckResult(raw.last_result ?? raw.latest_result ?? raw.last) ?? null

  const serverIdsRaw = Array.isArray(raw.server_ids)
    ? raw.server_ids
    : Array.isArray(raw.serverIds)
      ? raw.serverIds
      : Array.isArray(raw.targets)
        ? raw.targets
        : []
  const server_ids = serverIdsRaw
    .map((item) => (typeof item === 'string' ? item : (toText(toRecord(item)?.server_id) ?? '')))
    .filter((id) => id !== '')

  // 契约（第 15 节）与后端实现中 availability_24h 均为百分比 0-100。
  const rawAvailability =
    toNumber(raw.availability_24h ?? raw.uptime_24h ?? raw.availability ?? raw.uptime_pct) ?? null
  const availability_24h =
    rawAvailability === null ? null : Math.max(0, Math.min(100, rawAvailability))

  const serverCount =
    toNumber(raw.server_count ?? raw.target_count ?? raw.serverCount) ?? server_ids.length

  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '未命名检测项',
    kind: toText(raw.kind) ?? 'tcp',
    target: toText(raw.target) ?? '',
    interval_s: toNumber(raw.interval_s) ?? 30,
    timeout_s: toNumber(raw.timeout_s) ?? 10,
    params,
    enabled: toBool(raw.enabled, true),
    created_at: toNumber(raw.created_at),
    server_ids,
    server_count: serverCount,
    state: normalizeCheckState(raw.state ?? raw.status, lastResult, params),
    availability_24h:
      availability_24h === null ? null : Math.max(0, Math.min(100, availability_24h)),
    last_result: lastResult,
  }
}

export function normalizeCheckSummary(input: unknown): CheckSummary {
  const raw = toRecord(input) ?? {}
  const checks = extractList(input, ['checks', 'items']).map(normalizeCheck)
  const derived = {
    up: checks.filter((check) => check.state === 'up').length,
    down: checks.filter((check) => check.state === 'down').length,
    unknown: checks.filter((check) => check.state === 'unknown').length,
  }
  return {
    total: toNumber(raw.total) ?? checks.length,
    up: toNumber(raw.up) ?? derived.up,
    down: toNumber(raw.down) ?? derived.down,
    unknown: toNumber(raw.unknown) ?? derived.unknown,
    checks,
  }
}

export function normalizeRunCheckResponse(input: unknown): RunCheckResponse {
  const items = extractList(input, ['results', 'items']).map((item) => {
    const raw = toRecord(item) ?? {}
    return {
      server_id: toText(raw.server_id) ?? '',
      ok: toBool(raw.ok),
      latency_ms: toNumber(raw.latency_ms ?? raw.latency),
      message: toText(raw.message ?? raw.error),
    }
  })
  const raw = toRecord(input)
  const summaryRaw = toRecord(raw?.summary)
  const ok = summaryRaw ? (toNumber(summaryRaw.ok) ?? 0) : items.filter((item) => item.ok).length
  const failed = summaryRaw
    ? (toNumber(summaryRaw.failed) ?? 0)
    : items.filter((item) => !item.ok).length
  return { results: items, summary: { ok, failed } }
}

export function normalizeCheckResults(input: unknown): CheckResult[] {
  return extractList(input, ['results', 'items', 'timeline'])
    .map(normalizeCheckResult)
    .filter((item): item is CheckResult => item !== null)
}

/* ---------------------- M3 告警：归一化 ---------------------- */

function toStringArray(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value
      .map((item) => {
        if (typeof item === 'string') return item
        const record = toRecord(item)
        if (!record) return ''
        return toText(record.id) ?? toText(record.name) ?? ''
      })
      .filter((item) => item !== '')
  }
  if (typeof value === 'string' && value.trim() !== '') {
    try {
      return toStringArray(JSON.parse(value))
    } catch {
      return value
        .split(',')
        .map((item) => item.trim())
        .filter(Boolean)
    }
  }
  return []
}

/**
 * 后端对渠道密钥类字段做脱敏（如 `***abcd`、`xxxx`）；命中即视为「已配置」，
 * 编辑时留空表示不修改，绝不把脱敏串当真实值回传。
 */
export function isMaskedValue(value: unknown): boolean {
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  if (trimmed === '') return false
  return /(\*{2,}|x{3,}|X{3,}|•{2,}|ｘ{3,})/.test(trimmed) || trimmed === '[redacted]'
}

export function normalizeAlertRule(input: unknown): AlertRule {
  const raw = toRecord(input) ?? {}
  const severity = toText(raw.severity)
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '未命名规则',
    enabled: toBool(raw.enabled, true),
    source: (toText(raw.source) as AlertSource) ?? 'check',
    check_ids: toStringArray(raw.check_ids),
    server_ids: toStringArray(raw.server_ids),
    groups: toStringArray(raw.groups),
    tags: toStringArray(raw.tags),
    metric: toText(raw.metric),
    comparator: toText(raw.comparator),
    threshold: toNumber(raw.threshold),
    for_duration_s: toNumber(raw.for_duration_s) ?? 0,
    severity:
      severity === 'critical' || severity === 'warning' || severity === 'info'
        ? severity
        : 'warning',
    dedup_window_s: toNumber(raw.dedup_window_s) ?? 300,
    channel_ids: toStringArray(raw.channel_ids),
    silence_until: toNumber(raw.silence_until),
    created_at: toNumber(raw.created_at),
  }
}

export function normalizeAlertChannel(input: unknown): AlertChannel {
  const raw = toRecord(input) ?? {}
  let config: Record<string, unknown> = {}
  if (typeof raw.config === 'string' && raw.config.trim() !== '') {
    try {
      const parsed = JSON.parse(raw.config)
      const record = toRecord(parsed)
      if (record) config = record
    } catch {
      config = {}
    }
  } else {
    config = toRecord(raw.config) ?? {}
  }
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '未命名渠道',
    kind: toText(raw.kind) ?? 'webhook',
    enabled: toBool(raw.enabled, true),
    config,
    last_error: toText(raw.last_error),
    last_ok_at: toNumber(raw.last_ok_at),
    created_at: toNumber(raw.created_at),
  }
}

export function normalizeAlertEvent(input: unknown): AlertEvent | null {
  const raw = toRecord(input)
  if (!raw) return null
  return {
    id: toNumber(raw.id),
    kind: toText(raw.kind) ?? 'event',
    ts: toNumber(raw.ts),
    detail: toText(raw.detail ?? raw.message),
  }
}

export function normalizeAlert(input: unknown): AlertItem {
  const raw = toRecord(input) ?? {}
  const status = toText(raw.status)
  const severity = toText(raw.severity)
  const events = extractList(raw.events, ['events', 'items'])
    .map(normalizeAlertEvent)
    .filter((event): event is AlertEvent => event !== null)
    .sort((a, b) => (a.ts ?? 0) - (b.ts ?? 0))
  return {
    id: String(raw.id ?? ''),
    rule_id: toText(raw.rule_id) ?? '',
    dedup_key: toText(raw.dedup_key) ?? '',
    server_id: toText(raw.server_id),
    check_id: toText(raw.check_id),
    status: status === 'firing' || status === 'resolved' || status === 'acked' ? status : 'firing',
    severity:
      severity === 'critical' || severity === 'warning' || severity === 'info'
        ? severity
        : 'warning',
    title: toText(raw.title) ?? '未命名告警',
    message: toText(raw.message),
    value: toNumber(raw.value),
    started_at: toNumber(raw.started_at),
    updated_at: toNumber(raw.updated_at),
    resolved_at: toNumber(raw.resolved_at),
    acked_at: toNumber(raw.acked_at),
    acked_by: toText(raw.acked_by),
    notify_count: toNumber(raw.notify_count) ?? 0,
    last_notify_at: toNumber(raw.last_notify_at),
    silence_until: toNumber(raw.silence_until),
    rule_name: toText(raw.rule_name),
    server_name: toText(raw.server_name),
    check_name: toText(raw.check_name),
    events,
  }
}

export function normalizeAlertSummary(input: unknown): AlertSummary {
  const raw = toRecord(input) ?? {}
  const bySeverity = toRecord(raw.by_severity) ?? {}
  return {
    firing: toNumber(raw.firing) ?? 0,
    acked: toNumber(raw.acked) ?? 0,
    resolved_24h: toNumber(raw.resolved_24h ?? raw.resolved24h) ?? 0,
    by_severity: {
      critical: toNumber(bySeverity.critical) ?? 0,
      warning: toNumber(bySeverity.warning) ?? 0,
      info: toNumber(bySeverity.info) ?? 0,
    },
  }
}

export function normalizeCredential(input: unknown): Credential {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '未命名凭证',
    kind: toText(raw.kind) ?? 'password',
    username: toText(raw.username) ?? '',
    fingerprint: toText(raw.fingerprint),
    created_at: toNumber(raw.created_at),
    server_count: toNumber(raw.server_count) ?? 0,
  }
}

export function normalizeServer(input: unknown): Server {
  const raw = toRecord(input) ?? {}
  const lastSeenAt = toNumber(raw.last_seen_at)
  const flatMetrics = normalizeMetrics(raw)
  const metrics =
    normalizeMetrics(raw.metrics_summary) ??
    normalizeMetrics(raw.metrics) ??
    normalizeMetrics(raw.latest_metrics) ??
    flatMetrics

  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '未命名服务器',
    host: toText(raw.host) ?? '—',
    port: toNumber(raw.port),
    mode: toText(raw.mode) ?? 'agent',
    status: normalizeStatus(raw.status, lastSeenAt),
    last_seen_at: lastSeenAt,
    agent_version: toText(raw.agent_version),
    os: toText(raw.os),
    arch: toText(raw.arch),
    kernel: toText(raw.kernel),
    tags: normalizeTags(raw.tags),
    metrics_summary: metrics,
    created_at: toNumber(raw.created_at),
    group: toText(raw.group),
    credential_id: toText(raw.credential_id),
    last_error: toText(raw.last_error),
    host_info: normalizeHostInfo(raw.host_info),
  }
}

/* ------------------------------------------------------------------ */
/* 业务接口                                                            */
/* ------------------------------------------------------------------ */

/** 列表接口契约未包含指标，若后端未附带则回退拉详情取 metrics_summary。 */
async function fetchServers(filters: ServerFilters = {}): Promise<Server[]> {
  const query = new URLSearchParams()
  if (filters.group) query.set('group', filters.group)
  if (filters.tag) query.set('tag', filters.tag)
  if (filters.status) query.set('status', filters.status)
  if (filters.mode) query.set('mode', filters.mode)
  const suffix = query.toString() ? `?${query.toString()}` : ''

  const raw = await request<unknown>(`/servers${suffix}`)
  const list = Array.isArray(raw) ? raw.map(normalizeServer) : []

  // 最多补拉 20 台，避免对控制端造成请求风暴（M1 列表已带指标，通常不触发）。
  const missing = list
    .filter((server) => server.id !== '' && server.metrics_summary === null)
    .slice(0, 20)

  if (missing.length > 0) {
    const details = await Promise.allSettled(
      missing.map((server) => request<unknown>(`/servers/${encodeURIComponent(server.id)}`)),
    )
    const byId = new Map<string, Server>()
    for (const result of details) {
      if (result.status !== 'fulfilled') continue
      const detail = normalizeServer(result.value)
      if (detail.id !== '') byId.set(detail.id, detail)
    }
    return list.map((server) => {
      const detail = byId.get(server.id)
      if (!detail) return server
      return {
        ...server,
        metrics_summary: server.metrics_summary ?? detail.metrics_summary,
        tags: server.tags.length > 0 ? server.tags : detail.tags,
      }
    })
  }

  return list
}

export interface UpdateServerPayload {
  name?: string
  tags?: string[]
  group?: string | null
}

/** POST /servers 请求体（M1 扩展字段，契约第 10 节）。 */
export interface CreateServerPayload {
  name: string
  host: string
  port: number
  mode: 'agent' | 'ssh'
  credential_id?: string
  group?: string
  tags?: string[]
  host_key_policy?: 'insecure' | 'accept-new' | 'strict'
}

export interface CreateEnrollTokenPayload {
  note: string
  ttl_s: number
}

export interface InstallScriptPayload {
  token: string
  server_url: string
}

export interface PasswordCredentialInput {
  name: string
  kind: 'password'
  username: string
  password: string
}

export interface KeyCredentialInput {
  name: string
  kind: 'key'
  username: string
  private_key: string
  passphrase?: string
}

export type CreateCredentialInput = PasswordCredentialInput | KeyCredentialInput

export interface MetricsQuery {
  from: number
  to: number
  step: MetricsStep
}

export interface BatchPayload {
  ids: string[]
  action: BatchAction
  params?: Record<string, unknown>
}

export const api = {
  login(username: string, password: string): Promise<LoginResult> {
    return request<LoginResult>('/auth/login', {
      method: 'POST',
      body: { username, password },
      auth: false,
    })
  },

  logout(): Promise<unknown> {
    return request<unknown>('/auth/logout', { method: 'POST' })
  },

  me(): Promise<User> {
    return request<User>('/auth/me')
  },

  systemInfo(): Promise<SystemInfo> {
    return request<SystemInfo>('/system/info')
  },

  servers(filters?: ServerFilters): Promise<Server[]> {
    return fetchServers(filters)
  },

  createServer(payload: CreateServerPayload): Promise<Server> {
    return request<unknown>('/servers', { method: 'POST', body: payload }).then(normalizeServer)
  },

  server(id: string): Promise<Server> {
    return request<unknown>(`/servers/${encodeURIComponent(id)}`).then(normalizeServer)
  },

  updateServer(id: string, payload: UpdateServerPayload): Promise<Server> {
    return request<unknown>(`/servers/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeServer)
  },

  deleteServer(id: string): Promise<unknown> {
    return request<unknown>(`/servers/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** 契约第 10 节：POST /servers/{id}/test-connection */
  testConnection(id: string): Promise<TestConnectionResult> {
    return request<TestConnectionResult>(`/servers/${encodeURIComponent(id)}/test-connection`, {
      method: 'POST',
    })
  },

  /** 契约第 10 节：POST /servers/{id}/refresh-host-info → host_info */
  refreshHostInfo(id: string): Promise<HostInfo | null> {
    return request<unknown>(`/servers/${encodeURIComponent(id)}/refresh-host-info`, {
      method: 'POST',
    }).then(normalizeHostInfo)
  },

  /** 契约第 10 节：POST /servers/batch → 逐台结果 + 汇总 */
  batch(payload: BatchPayload): Promise<BatchResponse> {
    return request<BatchResponse>('/servers/batch', { method: 'POST', body: payload })
  },

  /** 契约第 10 节：GET /servers/{id}/metrics?from&to&step */
  metrics(id: string, query: MetricsQuery): Promise<MetricsResponse> {
    const params = new URLSearchParams({
      from: String(query.from),
      to: String(query.to),
      step: query.step,
    })
    return request<MetricsResponse>(
      `/servers/${encodeURIComponent(id)}/metrics?${params.toString()}`,
    ).then((raw) => ({
      series: Array.isArray(raw?.series) ? raw.series.map(normalizeMetricSeries) : [],
      step: typeof raw?.step === 'string' && raw.step !== '' ? raw.step : query.step,
    }))
  },

  /** 契约第 10 节：GET /groups —— 由 servers 聚合出的分组与数量 */
  groups(): Promise<GroupSummary[]> {
    return request<unknown>('/groups').then((raw) =>
      Array.isArray(raw)
        ? raw
            .map((item) => {
              const record = toRecord(item)
              if (!record) return null
              return {
                name: toText(record.name) ?? '',
                count: toNumber(record.count) ?? 0,
              }
            })
            .filter((group): group is GroupSummary => group !== null && group.name !== '')
        : [],
    )
  },

  /** 契约第 10 节：凭证接口绝不返回密钥内容 */
  credentials(): Promise<Credential[]> {
    return request<unknown>('/credentials').then((raw) =>
      Array.isArray(raw) ? raw.map(normalizeCredential) : [],
    )
  },

  createCredential(payload: CreateCredentialInput): Promise<Credential> {
    return request<unknown>('/credentials', { method: 'POST', body: payload }).then(
      normalizeCredential,
    )
  },

  deleteCredential(id: string): Promise<unknown> {
    return request<unknown>(`/credentials/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  enrollTokens(): Promise<EnrollToken[]> {
    return request<EnrollToken[]>('/enroll-tokens')
  },

  createEnrollToken(payload: CreateEnrollTokenPayload): Promise<EnrollToken> {
    return request<EnrollToken>('/enroll-tokens', { method: 'POST', body: payload })
  },

  installScript(payload: InstallScriptPayload): Promise<InstallScript> {
    return request<InstallScript>('/agent/install-script', { method: 'POST', body: payload })
  },

  /* ---------------------- M2：检测（契约第 15 节） ---------------------- */

  /** GET /checks —— 含目标服务器数、最近结果、当前状态、24h 可用率 */
  checks(): Promise<CheckItem[]> {
    return request<unknown>('/checks').then((raw) =>
      extractList(raw, ['checks', 'items']).map(normalizeCheck),
    )
  },

  check(id: string): Promise<CheckItem> {
    return request<unknown>(`/checks/${encodeURIComponent(id)}`).then(normalizeCheck)
  },

  createCheck(payload: CreateCheckPayload): Promise<CheckItem> {
    return request<unknown>('/checks', { method: 'POST', body: payload }).then(normalizeCheck)
  },

  updateCheck(id: string, payload: UpdateCheckPayload): Promise<CheckItem> {
    return request<unknown>(`/checks/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeCheck)
  },

  deleteCheck(id: string): Promise<unknown> {
    return request<unknown>(`/checks/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** POST /checks/{id}/targets —— 覆盖式设置适用服务器 */
  setCheckTargets(id: string, serverIds: string[]): Promise<unknown> {
    return request<unknown>(`/checks/${encodeURIComponent(id)}/targets`, {
      method: 'POST',
      body: { server_ids: serverIds },
    })
  },

  /** POST /checks/{id}/run —— 立即对全部目标执行一次，返回逐台结果 */
  runCheck(id: string): Promise<RunCheckResponse> {
    return request<unknown>(`/checks/${encodeURIComponent(id)}/run`, { method: 'POST' }).then(
      normalizeRunCheckResponse,
    )
  },

  /** GET /checks/{id}/results —— 结果时间线（默认最近 100 条） */
  checkResults(id: string, query: CheckResultsQuery = {}): Promise<CheckResult[]> {
    const params = new URLSearchParams()
    if (query.server_id) params.set('server_id', query.server_id)
    if (query.from) params.set('from', String(query.from))
    if (query.to) params.set('to', String(query.to))
    params.set('limit', String(query.limit ?? 100))
    return request<unknown>(
      `/checks/${encodeURIComponent(id)}/results?${params.toString()}`,
    ).then(normalizeCheckResults)
  },

  /** GET /checks/summary —— 总览页检测概览 */
  checksSummary(): Promise<CheckSummary> {
    return request<unknown>('/checks/summary').then(normalizeCheckSummary)
  },

  /** GET /servers/{id}/checks —— 该服务器适用的检测项 + 最近结果 + 状态 */
  serverChecks(serverId: string): Promise<CheckItem[]> {
    return request<unknown>(`/servers/${encodeURIComponent(serverId)}/checks`).then((raw) =>
      extractList(raw, ['checks', 'items']).map(normalizeCheck),
    )
  },

  /* ---------------------- M3：告警（契约第 20 节） ---------------------- */

  alertRules(): Promise<AlertRule[]> {
    return request<unknown>('/alert-rules').then((raw) =>
      extractList(raw, ['rules', 'items']).map(normalizeAlertRule),
    )
  },

  createAlertRule(payload: AlertRulePayload): Promise<AlertRule> {
    return request<unknown>('/alert-rules', { method: 'POST', body: payload }).then(
      normalizeAlertRule,
    )
  },

  updateAlertRule(id: string, payload: Partial<AlertRulePayload>): Promise<AlertRule> {
    return request<unknown>(`/alert-rules/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeAlertRule)
  },

  deleteAlertRule(id: string): Promise<unknown> {
    return request<unknown>(`/alert-rules/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** POST /alert-rules/{id}/silence —— 临时静默（只写 silence_until） */
  silenceAlertRule(id: string, durationS: number): Promise<AlertRule> {
    return request<unknown>(`/alert-rules/${encodeURIComponent(id)}/silence`, {
      method: 'POST',
      body: { duration_s: durationS },
    }).then(normalizeAlertRule)
  },

  alertChannels(): Promise<AlertChannel[]> {
    return request<unknown>('/alert-channels').then((raw) =>
      extractList(raw, ['channels', 'items']).map(normalizeAlertChannel),
    )
  },

  createAlertChannel(payload: AlertChannelPayload): Promise<AlertChannel> {
    return request<unknown>('/alert-channels', { method: 'POST', body: payload }).then(
      normalizeAlertChannel,
    )
  },

  updateAlertChannel(
    id: string,
    payload: Partial<AlertChannelPayload>,
  ): Promise<AlertChannel> {
    return request<unknown>(`/alert-channels/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeAlertChannel)
  },

  deleteAlertChannel(id: string): Promise<unknown> {
    return request<unknown>(`/alert-channels/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** POST /alert-channels/{id}/test —— 真实发送一条测试消息，返回失败原因 */
  testAlertChannel(id: string): Promise<ChannelTestResult> {
    return request<unknown>(`/alert-channels/${encodeURIComponent(id)}/test`, {
      method: 'POST',
    }).then((raw) => {
      const record = toRecord(raw)
      return {
        ok: toBool(record?.ok, false),
        error: toText(record?.error ?? record?.message),
      }
    })
  },

  /** GET /alerts?status=&severity=&rule_id=&server_id=&from=&to=&limit= */
  alerts(filters: AlertFilters = {}): Promise<AlertItem[]> {
    const params = new URLSearchParams()
    if (filters.status) params.set('status', filters.status)
    if (filters.severity) params.set('severity', filters.severity)
    if (filters.rule_id) params.set('rule_id', filters.rule_id)
    if (filters.server_id) params.set('server_id', filters.server_id)
    if (filters.from) params.set('from', String(filters.from))
    if (filters.to) params.set('to', String(filters.to))
    if (filters.limit) params.set('limit', String(filters.limit))
    const suffix = params.toString() ? `?${params.toString()}` : ''
    return request<unknown>(`/alerts${suffix}`).then((raw) =>
      extractList(raw, ['alerts', 'items']).map(normalizeAlert),
    )
  },

  /** GET /alerts/{id} —— 详情 + events 状态轨迹 */
  alert(id: string): Promise<AlertItem> {
    return request<unknown>(`/alerts/${encodeURIComponent(id)}`).then(normalizeAlert)
  },

  /** POST /alerts/{id}/ack —— 人工确认 */
  ackAlert(id: string): Promise<AlertItem> {
    return request<unknown>(`/alerts/${encodeURIComponent(id)}/ack`, { method: 'POST' }).then(
      normalizeAlert,
    )
  },

  alertsSummary(): Promise<AlertSummary> {
    return request<unknown>('/alerts/summary').then(normalizeAlertSummary)
  },
}

/** 安装命令里要填的控制端地址：开发态指向被代理的后端，生产态用当前同源地址。 */
export function resolveControlUrl(): string {
  if (import.meta.env.DEV) return 'http://127.0.0.1:8080'
  return window.location.origin
}
