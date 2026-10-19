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
}

/** 安装命令里要填的控制端地址：开发态指向被代理的后端，生产态用当前同源地址。 */
export function resolveControlUrl(): string {
  if (import.meta.env.DEV) return 'http://127.0.0.1:8080'
  return window.location.origin
}
