import { t } from '../i18n'
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
  /** 后端 error.details（登录限流剩余次数、retry_after_s 等） */
  readonly details: Record<string, unknown> | null

  constructor(
    status: number,
    code: string,
    message: string,
    details: Record<string, unknown> | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
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
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  auth?: boolean
  signal?: AbortSignal
  /** 返回完整信封（用于分页 page 字段） */
  rawPayload?: boolean
}

interface ErrorEnvelope {
  error?: {
    code?: string
    message?: string
    details?: Record<string, unknown> | null
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
    throw new ApiError(0, 'network_error', t('s.0578'))
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
      envelope?.error?.message ?? t('s.0579', { '0': response.status }),
      toRecord(envelope?.error?.details),
    )
    if (response.status === 401 && auth) {
      clearToken()
      unauthorizedHandler?.()
    }
    throw apiError
  }

  if (options.rawPayload) return payload as T
  if (payload !== null && typeof payload === 'object' && 'data' in payload) {
    return (payload as { data: T }).data
  }
  return payload as T
}

/** 分页接口：显式传 limit 时返回 {data:[...], page:{...}}；不传时仍是数组。 */
async function requestPaged<T>(
  path: string,
  normalize: (raw: unknown) => T,
): Promise<Paged<T>> {
  const payload = await request<unknown>(path, { rawPayload: true })
  const record = toRecord(payload)
  const dataField = record?.data
  const dataRecord = toRecord(dataField)
  const itemsRaw = Array.isArray(payload)
    ? payload
    : Array.isArray(dataField)
      ? dataField
      : dataRecord
        ? extractList(dataRecord, ['logs', 'items', 'entries', 'runs'])
        : []
  const pageRaw = toRecord(record?.page) ?? toRecord(dataRecord?.page)
  const page: PageInfo | null = pageRaw
    ? {
        next_cursor: toText(pageRaw.next_cursor ?? pageRaw.nextCursor),
        has_more: toBool(pageRaw.has_more ?? pageRaw.hasMore),
      }
    : null
  return { items: itemsRaw.map(normalize), page }
}

/** multipart 上传（制品上传用）：与 request 相同的信封与 401 处理。 */
async function requestMultipart<T>(
  path: string,
  form: FormData,
  options: { auth?: boolean } = {},
): Promise<T> {
  const { auth = true } = options
  const headers: Record<string, string> = { Accept: 'application/json' }
  const token = getToken()
  if (auth && token) headers.Authorization = `Bearer ${token}`

  let response: Response
  try {
    response = await fetch(`${API_PREFIX}${path}`, { method: 'POST', headers, body: form })
  } catch {
    throw new ApiError(0, 'network_error', t('s.0578'))
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
      envelope?.error?.message ?? t('s.0579', { '0': response.status }),
      toRecord(envelope?.error?.details),
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

export type ScopeMode = 'all' | 'groups' | 'servers'

export interface RoleScope {
  mode: ScopeMode
  group_ids: string[]
  server_ids: string[]
}

export interface User {
  id: string
  username: string
  role: string
  role_name: string | null
  permissions: string[]
  scope: RoleScope
}

export interface LoginResult {
  token: string
  user: User
}

export type NetworkSource = 'settings' | 'env' | 'default' | string

export interface NetworkConfig {
  /** 配置值（未配置时为空） */
  listen: string | null
  listen_source: NetworkSource
  /** 当前进程实际监听地址 */
  active_listen: string
  public_url: string | null
  public_url_source: NetworkSource
  default_ssh_port: number
  default_ssh_port_source: NetworkSource
  /** 启动回退原因（空串表示未回退） */
  fallback_reason: string | null
  restart_required: boolean
}

export interface NetworkUpdatePayload {
  listen?: string
  public_url?: string
  default_ssh_port?: number
}

export interface UpdateAsset {
  os: string
  arch: string
  url: string
  size_bytes: number
}

export interface UpdateInfo {
  current: string
  latest: string | null
  has_update: boolean
  published_at: number | null
  release_url: string | null
  notes: string | null
  /** 该版本包含数据库迁移 */
  schema_changed: boolean
  assets: UpdateAsset[]
  /** 当前平台/架构是否有可用产物（Windows 上恒为 false：不支持替换运行中的可执行文件） */
  upgradable: boolean
  /** 不能一键升级时的原因（如 Windows 平台限制），空表示无 */
  upgrade_note: string | null
  checked_at: number | null
  /** 检查失败原因（空表示成功）；失败时保留上次数据 */
  error: string | null
  /** 服务端是否允许自升级（LEADSTACK_UPGRADE_ENABLED） */
  upgrade_enabled: boolean
  /** 自动检查间隔（服务端配置，可能缺省） */
  check_interval: string | null
}

export type UpdatePhase =
  | 'idle'
  | 'downloading'
  | 'backing_up'
  | 'replacing'
  | 'restarting'
  | 'success'
  | 'failed'
  | 'rolled_back'
  | string

export interface UpdateStatus {
  job_id: string | null
  phase: UpdatePhase
  target_version: string | null
  current_version: string | null
  message: string | null
  error: string | null
  rolled_back: boolean
  backup_file: string | null
  progress: number | null
  started_at: number | null
  finished_at: number | null
}

export interface HealthInfo {
  status: string
  version: string
  uptime_s: number
}

export interface SystemInfo {
  version: string
  uptime_s: number
  server_count: number
  online_count: number
  /** 协作：首次安装向导是否需要引导 */
  needs_setup?: boolean
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
  /** 纳管 扩展字段（契约第 10 节） */
  group: string | null
  /** 授权分组（契约第 55.4 节） */
  group_ids: string[]
  group_names: string[]
  /** 绑定的宝塔面板凭据（contract §57） */
  panel_credential_id: string | null
  credential_id: string | null
  last_error: string | null
  host_info: HostInfo | null
}

export type CredentialKind = 'password' | 'key' | 'baota' | string

/** 凭证对外元数据：接口绝不返回密钥内容。 */
export interface Credential {
  id: string
  name: string
  kind: CredentialKind | string
  username: string
  fingerprint: string | null
  created_at: number | null
  server_count: number
  /** 宝塔面板地址（kind=baota） */
  panel_url: string | null
  /** 是否跳过面板证书校验 */
  insecure_skip_verify: boolean
}

export interface CreateCredentialPayload {
  name: string
  kind: CredentialKind
  username?: string
  password?: string
  private_key?: string
  passphrase?: string
  /** kind=baota：面板地址，如 https://1.2.3.4:8888 */
  panel_url?: string
  /** kind=baota：面板 API 密钥 */
  api_key?: string
  /** kind=baota：跳过面板证书校验（自签证书时使用） */
  insecure_skip_verify?: boolean
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
/* 检测：检测（契约第 13/15 节）                                          */
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
/* 部署：批量部署（契约第 23/24/26/27 节）                                */
/* ------------------------------------------------------------------ */

export type ReleaseStatus = 'pending' | 'running' | 'success' | 'failed' | 'canceled'
export type TemplateKind = 'compose' | 'systemd' | 'binary'
export type ArtifactKind = 'url' | 'upload' | 'image'

export type AppProvider = 'builtin' | 'baota' | string
export type DeployMode = 'release' | 'inplace' | 'script'

export interface DiscoverSuggested {
  deploy_mode?: DeployMode | string
  target_dir?: string
  restart_cmd?: string
  healthcheck_port?: number
  root?: string
}

export interface DiscoverCandidate {
  kind: 'systemd' | 'docker' | 'site' | string
  name: string
  detail: string
  suggested: DiscoverSuggested
}

export interface DiscoverResult {
  server_id: string
  candidates: DiscoverCandidate[]
  raw: Record<string, string>
}
export type BaotaSiteType = 'static' | 'php' | string

export interface AppItem {
  id: string
  name: string
  slug: string
  root: string | null
  description: string | null
  created_at: number | null
  /** 部署方式：内置 SSH/Agent 或宝塔面板 */
  provider: AppProvider
  /** 宝塔站点名（provider=baota） */
  site_name: string | null
  /** 宝塔站点端口 */
  site_port: number | null
  /** 宝塔站点类型 static/php */
  site_type: BaotaSiteType | null
  /** 发布后健康检查路径 */
  healthcheck_path: string | null
  /** 部署模式：release=多版本目录原子切换；inplace=接管既有目录 */
  deploy_mode: DeployMode | string
  /** inplace 接管目录（绝对路径） */
  target_dir: string | null
  /** 部署后重启命令 */
  restart_cmd: string | null
  /** 首版初始化脚本（每台服务器首次成功部署执行一次） */
  init_script: string | null
  /** 失败/健康检查不通过时执行的回滚脚本（script 模式） */
  rollback_script: string | null
  /** 环境变量 JSON 字符串 */
  env_json: string | null
  /** inplace 同步时保留的相对路径（逗号分隔） */
  keep_paths: string | null
  /** 健康检查端口（0 表示清除） */
  healthcheck_port: number | null
  /** 后端可能聚合出的统计；缺失时前端用 releases 列表本地折算 */
  release_count: number
  server_count: number
  last_release: Release | null
}

export interface DeployTemplate {
  id: string
  name: string
  kind: TemplateKind | string
  content: string
  created_at: number | null
  updated_at: number | null
}

export interface Artifact {
  id: string
  kind: ArtifactKind | string
  location: string
  sha256: string | null
  size_bytes: number
  created_at: number | null
}

export interface ReleaseRun {
  id: string
  release_id: string
  server_id: string
  server_name: string | null
  status: ReleaseStatus
  exit_code: number | null
  message: string | null
  failed_step: string | null
  prev_release_id: string | null
  started_at: number | null
  finished_at: number | null
}

export interface Release {
  id: string
  app_id: string
  app_name: string | null
  app_slug: string | null
  template_id: string
  template_name: string | null
  version: string
  artifact_id: string | null
  status: ReleaseStatus
  created_by: string | null
  created_at: number | null
  started_at: number | null
  finished_at: number | null
  concurrency: number
  /** 发布策略：all=全量；canary=灰度 */
  strategy: 'all' | 'canary' | string
  canary_count: number | null
  /** 灰度观察期（秒） */
  hold_seconds: number | null
  runs: ReleaseRun[]
}

export interface ReleaseLogEntry {
  seq: number
  ts: number | null
  stream: string
  chunk: string
}

/** 模板渲染预览的可用占位符（契约第 26 节）。 */
export const TEMPLATE_PLACEHOLDERS: { name: string; description: string }[] = [
  { name: 'APP_SLUG', description: t('s.0580') },
  { name: 'VERSION', description: t('s.0581') },
  { name: 'RELEASE_ID', description: t('s.0582') },
  { name: 'ROOT', description: t('s.0246') },
  { name: 'RELEASE_DIR', description: '<ROOT>/releases/<RELEASE_ID>' },
  { name: 'ARTIFACT_URL', description: t('s.0470') },
  { name: 'ARTIFACT_SHA256', description: t('s.0583') },
  { name: 'PREV_RELEASE_ID', description: t('s.0584') },
]

export interface CreateAppPayload {
  name: string
  slug: string
  root?: string
  description?: string
  provider?: AppProvider
  site_name?: string
  site_port?: number
  site_type?: BaotaSiteType | null
  healthcheck_path?: string | null
  deploy_mode?: DeployMode
  target_dir?: string | null
  restart_cmd?: string | null
  init_script?: string | null
  rollback_script?: string | null
  env_json?: string | null
  keep_paths?: string | null
  healthcheck_port?: number | null
}

export interface CreateTemplatePayload {
  name: string
  kind: TemplateKind
  content: string
}

export interface CreateReleasePayload {
  app_id: string
  template_id: string
  version: string
  artifact_id: string
  server_ids: string[]
  concurrency?: number
  strategy?: 'all' | 'canary'
  canary_count?: number
  hold_seconds?: number
}

export interface RollbackTarget {
  release_id: string
  version: string
  created_at: number | null
  finished_at: number | null
  server_ids: string[]
}

export interface DiagnoseCheck {
  id: string
  title: string
  status: 'pass' | 'warn' | 'fail' | string
  detail: string
}

export interface DiagnoseRecommendation {
  deploy_mode: string
  target_dir: string
  restart_cmd?: string
  keep_paths?: string
  healthcheck_port?: number
  script?: string
}

export interface DiagnoseResult {
  server_id: string
  name: string
  kind: string
  target_dir: string
  checks: DiagnoseCheck[]
  recommendation: DiagnoseRecommendation
}

export interface CreateReleaseResponse {
  release: Release
  runs: ReleaseRun[]
}

export interface PreviewStep {
  name: string
  command: string
  timeout_s: number | null
  secret: boolean
  ignore_failure: boolean
}

export interface PreviewServerPlan {
  server_id: string
  server_name: string
  plan_root: string | null
  steps: PreviewStep[]
  rollback_steps: PreviewStep[]
  notes: string[]
}

export interface ReleasePreview {
  app_id: string
  app_slug: string
  version: string
  provider: string
  deploy_mode: string
  servers: PreviewServerPlan[]
}

export interface DeployPresetTemplate {
  name: string
  kind: string
  content: string
}

export interface DeployPresetDefaults {
  deploy_mode?: string
  keep_paths?: string
  healthcheck_path?: string
  provider?: string
  site_type?: string
  site_port?: number
}

export interface DeployPreset {
  key: string
  name: string
  description: string
  template: DeployPresetTemplate
  hints: string[]
  defaults: DeployPresetDefaults
}

export interface ReleaseFilters {
  app_id?: string
  status?: ReleaseStatus | ''
  limit?: number
}


/* ------------------------------------------------------------------ */
/* 备份与恢复（契约第 30/31/33 节）                                    */
/* ------------------------------------------------------------------ */

export interface BackupInfo {
  name: string
  size_bytes: number
  created_at: number | null
  includes_artifacts: boolean
  schema_version: number | null
  /** 归档文件本身的 sha256；列表里通常只给前缀或完整值 */
  sha256: string | null
}

export interface BackupManifestFile {
  name: string
  size_bytes: number
  sha256: string | null
}

export interface BackupManifest {
  format_version: number | null
  app_version: string | null
  schema_version: number | null
  created_at: number | null
  includes_artifacts: boolean
  files: BackupManifestFile[]
}

export interface BackupVerifyResult {
  ok: boolean
  manifest: BackupManifest | null
  error: string | null
}

export interface BackupSettings {
  enabled: boolean
  /** Go duration 字符串，如 6h / 12h / 24h / 30m */
  interval: string
  /** 保留份数 */
  retention: number
  include_artifacts: boolean
}

export interface RestoreStagedResult {
  restart_required: boolean
  /** 暂存归档文件名（后端字段可能是 archive_name 或 source） */
  archive_name: string | null
  uploaded_at: number | null
  size_bytes: number | null
  schema_version: number | null
  sha256: string | null
  /** 暂存目录（后端可能不返回） */
  staged_path: string | null
}

export interface BackupDownload {
  blob: Blob
  filename: string
}


/* ------------------------------------------------------------------ */
/* 运维与协作：审计 / 用户 / API Token / 定时任务 / 导入 / 终端 / 文件 / 日志 */
/* ------------------------------------------------------------------ */

export interface PageInfo {
  next_cursor: string | null
  has_more: boolean
}

export interface Paged<T> {
  items: T[]
  page: PageInfo | null
}

export interface AuditLogEntry {
  id: number
  ts: number | null
  actor: string
  actor_role: string | null
  action: string
  resource_type: string | null
  resource_id: string | null
  method: string | null
  path: string | null
  status_code: number | null
  ip: string | null
  user_agent: string | null
  detail: string | null
}

export interface AuditFilters {
  actor?: string
  action?: string
  resource_type?: string
  from?: number
  to?: number
  limit?: number
  cursor?: string
}

export type UserRole = 'admin' | 'operator' | 'viewer'

export interface Permission {
  key: string
  name: string
  description: string
  /** true = 仅内置管理员可拥有，自定义角色不可授予 */
  admin_only: boolean
}

export interface Role {
  key: string
  name: string
  description: string
  builtin: boolean
  permissions: string[]
  scope: RoleScope
  user_count: number
  created_at: number | null
  updated_at: number | null
}

export interface RolePayload {
  name: string
  description: string
  permissions: string[]
  scope: RoleScope
}

export interface ServerGroup {
  id: string
  name: string
  description: string
  member_count: number
  created_at: number | null
  updated_at: number | null
}

export interface ManagedUser {
  id: string
  username: string
  role: string
  role_name: string | null
  disabled: boolean
  locked: boolean
  locked_until: number | null
  failed_attempts: number
  created_at: number | null
  updated_at: number | null
}

export interface ApiToken {
  id: string
  name: string
  role: UserRole | string
  created_by: string | null
  created_at: number | null
  last_used_at: number | null
  expires_at: number | null
  revoked_at: number | null
}

export interface CreatedApiToken extends ApiToken {
  /** 明文只在创建响应出现一次 */
  token: string | null
}

export interface ScheduledTask {
  id: string
  name: string
  cron: string
  command: string
  timeout_s: number
  enabled: boolean
  concurrency: number
  created_at: number | null
  last_run_at: number | null
  next_run_at: number | null
  server_ids: string[]
}

export interface ScheduledTaskPayload {
  name: string
  cron: string
  command: string
  timeout_s: number
  enabled: boolean
  concurrency: number
  server_ids: string[]
}

export type ScheduledRunStatus = 'success' | 'failed' | 'running' | 'pending' | 'canceled' | string

export interface ScheduledTaskRun {
  id: number
  task_id: string
  server_id: string
  ts: number | null
  status: ScheduledRunStatus
  exit_code: number | null
  duration_ms: number | null
  stdout: string | null
  stderr: string | null
  message: string | null
}

export interface ImportServersPayload {
  text: string
  default_port: number
  credential_id: string
  group?: string
  tags?: string[]
  mode: 'agent' | 'ssh'
}

export interface ImportServersResult {
  total: number
  created: { host: string; server_id: string }[]
  failed: { host: string; error: string }[]
}

export interface TerminalTicket {
  ticket: string
  expires_in: number
}

export interface RemoteFileEntry {
  name: string
  is_dir: boolean
  size: number
  mod_time: number | null
  mode: string | null
}

export interface RemoteFileDownload {
  blob: Blob
  filename: string
}

export interface RemoteProcess {
  pid: number
  user: string | null
  cpu_pct: number | null
  mem_pct: number | null
  rss_kb: number | null
  command: string
}

export interface RemotePort {
  proto: string
  local: string
  pid: number | null
  process: string | null
}

export interface ServerLogsResult {
  lines: string[]
  truncated: boolean
}

export interface SetupStatus {
  needs_setup: boolean
  version: string
  instance_name: string | null
}

/* ------------------------------------------------------------------ */
/* 告警：告警（契约第 18/20 节）                                          */
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

// 指标 key 以协议点分命名为准（契约 3.1），同时兼容 基础 期间出现过的下划线别名。
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

/* ---------------------- 检测 检测：归一化 ---------------------- */

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
    name: toText(raw.name) ?? t('s.0585'),
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

/* ---------------------- 告警 告警：归一化 ---------------------- */

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
    name: toText(raw.name) ?? t('s.0586'),
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
    name: toText(raw.name) ?? t('s.0587'),
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
    title: toText(raw.title) ?? t('s.0588'),
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

/* ---------------------- 部署 部署：归一化 ---------------------- */

function normalizeReleaseStatus(value: unknown): ReleaseStatus {
  switch (value) {
    case 'pending':
    case 'running':
    case 'success':
    case 'failed':
    case 'canceled':
      return value
    case 'queued':
      return 'pending'
    case 'cancelled':
      return 'canceled'
    default:
      return 'pending'
  }
}

export function normalizeReleaseRun(input: unknown): ReleaseRun {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    release_id: toText(raw.release_id) ?? '',
    server_id: toText(raw.server_id) ?? '',
    server_name: toText(raw.server_name),
    status: normalizeReleaseStatus(raw.status),
    exit_code: toNumber(raw.exit_code),
    message: toText(raw.message),
    failed_step: toText(raw.failed_step),
    prev_release_id: toText(raw.prev_release_id),
    started_at: toNumber(raw.started_at),
    finished_at: toNumber(raw.finished_at),
  }
}

export function normalizeRelease(input: unknown): Release {
  const raw = toRecord(input) ?? {}
  const runs = extractList(raw.runs, ['runs', 'items']).map(normalizeReleaseRun)
  return {
    id: String(raw.id ?? ''),
    app_id: toText(raw.app_id) ?? '',
    app_name: toText(raw.app_name),
    app_slug: toText(raw.app_slug),
    template_id: toText(raw.template_id) ?? '',
    template_name: toText(raw.template_name),
    version: toText(raw.version) ?? '—',
    artifact_id: toText(raw.artifact_id),
    status: normalizeReleaseStatus(raw.status),
    created_by: toText(raw.created_by),
    created_at: toNumber(raw.created_at),
    started_at: toNumber(raw.started_at),
    finished_at: toNumber(raw.finished_at),
    concurrency: toNumber(raw.concurrency) ?? 4,
    strategy: toText(raw.strategy) ?? 'all',
    canary_count: toNumber(raw.canary_count),
    hold_seconds: toNumber(raw.hold_seconds),
    runs,
  }
}

export function normalizeApp(input: unknown): AppItem {
  const raw = toRecord(input) ?? {}
  const lastReleaseRaw = raw.last_release ?? raw.latest_release
  const lastRelease = lastReleaseRaw ? normalizeRelease(lastReleaseRaw) : null
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? t('s.0589'),
    slug: toText(raw.slug) ?? '',
    root: toText(raw.root),
    description: toText(raw.description),
    created_at: toNumber(raw.created_at),
    provider: toText(raw.provider) ?? 'builtin',
    site_name: toText(raw.site_name),
    site_port: toNumber(raw.site_port),
    site_type: toText(raw.site_type),
    healthcheck_path: toText(raw.healthcheck_path),
    deploy_mode: toText(raw.deploy_mode) ?? 'release',
    target_dir: toText(raw.target_dir),
    restart_cmd: toText(raw.restart_cmd),
    init_script: typeof raw.init_script === 'string' ? raw.init_script : null,
    rollback_script: typeof raw.rollback_script === 'string' ? raw.rollback_script : null,
    env_json: typeof raw.env_json === 'string' ? raw.env_json : null,
    keep_paths: toText(raw.keep_paths),
    healthcheck_port: toNumber(raw.healthcheck_port),
    release_count: toNumber(raw.release_count ?? raw.releases_count) ?? 0,
    server_count: toNumber(raw.server_count ?? raw.target_count) ?? 0,
    last_release: lastRelease,
  }
}

export function normalizeTemplate(input: unknown): DeployTemplate {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? t('s.0590'),
    kind: toText(raw.kind) ?? 'binary',
    content: typeof raw.content === 'string' ? raw.content : '',
    created_at: toNumber(raw.created_at),
    updated_at: toNumber(raw.updated_at),
  }
}

export function normalizeArtifact(input: unknown): Artifact {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    kind: toText(raw.kind) ?? 'url',
    location: toText(raw.location ?? raw.url) ?? '',
    sha256: toText(raw.sha256),
    size_bytes: toNumber(raw.size_bytes) ?? 0,
    created_at: toNumber(raw.created_at),
  }
}

export function normalizeReleaseLogs(input: unknown): ReleaseLogEntry[] {
  return extractList(input, ['logs', 'items', 'entries'])
    .map((item) => {
      const raw = toRecord(item)
      if (!raw) return null
      const seq = toNumber(raw.seq)
      if (seq === null) return null
      return {
        seq,
        ts: toNumber(raw.ts),
        stream: toText(raw.stream) ?? 'stdout',
        chunk: typeof raw.chunk === 'string' ? raw.chunk : String(raw.chunk ?? ''),
      }
    })
    .filter((item): item is ReleaseLogEntry => item !== null)
    .sort((a, b) => a.seq - b.seq)
}

/* ---------------------- 备份：归一化 ---------------------- */

export function normalizeBackupInfo(input: unknown): BackupInfo {
  const raw = toRecord(input) ?? {}
  return {
    name: toText(raw.name) ?? '',
    size_bytes: toNumber(raw.size_bytes) ?? 0,
    created_at: toNumber(raw.created_at),
    includes_artifacts: toBool(raw.includes_artifacts),
    schema_version: toNumber(raw.schema_version),
    sha256: toText(raw.sha256),
  }
}

export function normalizeBackupManifest(input: unknown): BackupManifest | null {
  const raw = toRecord(input)
  if (!raw) return null
  return {
    format_version: toNumber(raw.format_version),
    app_version: toText(raw.app_version),
    schema_version: toNumber(raw.schema_version),
    created_at: toNumber(raw.created_at),
    includes_artifacts: toBool(raw.includes_artifacts),
    files: extractList(raw.files, ['files']).map((item) => {
      const file = toRecord(item) ?? {}
      return {
        name: toText(file.name) ?? '',
        size_bytes: toNumber(file.size_bytes) ?? 0,
        sha256: toText(file.sha256),
      }
    }),
  }
}

export function normalizeBackupSettings(input: unknown): BackupSettings {
  const raw = toRecord(input) ?? {}
  return {
    enabled: toBool(raw.enabled, false),
    interval: toText(raw.interval) ?? '24h',
    retention: toNumber(raw.retention) ?? 7,
    include_artifacts: toBool(raw.include_artifacts, false),
  }
}

export function normalizeBackupVerifyResult(input: unknown): BackupVerifyResult {
  const raw = toRecord(input) ?? {}
  return {
    ok: toBool(raw.ok, false),
    manifest: normalizeBackupManifest(raw.manifest),
    error: toText(raw.error ?? raw.message),
  }
}

export function normalizeRestoreStagedResult(input: unknown): RestoreStagedResult {
  const raw = toRecord(input) ?? {}
  const staged = toRecord(raw.staged) ?? {}
  return {
    restart_required: toBool(raw.restart_required, true),
    archive_name: toText(staged.archive_name ?? staged.source ?? raw.archive_name),
    uploaded_at: toNumber(staged.uploaded_at ?? staged.created_at ?? raw.uploaded_at),
    size_bytes: toNumber(staged.size_bytes ?? raw.size_bytes),
    schema_version: toNumber(staged.schema_version ?? raw.schema_version),
    sha256: toText(staged.archive_sha256 ?? raw.sha256),
    staged_path: toText(raw.staged_path ?? raw.staged_to),
  }
}

/* ---------------------- 运维与协作：归一化 ---------------------- */

export function normalizeAuditLog(input: unknown): AuditLogEntry {
  const raw = toRecord(input) ?? {}
  return {
    id: toNumber(raw.id) ?? 0,
    ts: toNumber(raw.ts),
    actor: toText(raw.actor) ?? '—',
    actor_role: toText(raw.actor_role),
    action: toText(raw.action) ?? '—',
    resource_type: toText(raw.resource_type),
    resource_id: toText(raw.resource_id),
    method: toText(raw.method),
    path: toText(raw.path),
    status_code: toNumber(raw.status_code),
    ip: toText(raw.ip),
    user_agent: toText(raw.user_agent),
    detail: typeof raw.detail === 'string' ? raw.detail : null,
  }
}

export function normalizeRoleScope(input: unknown): RoleScope {
  const raw = toRecord(input) ?? {}
  const modeRaw = toText(raw.mode)
  const mode: ScopeMode = modeRaw === 'groups' || modeRaw === 'servers' ? modeRaw : 'all'
  return {
    mode,
    group_ids: toStringArray(raw.group_ids ?? raw.groupIds),
    server_ids: toStringArray(raw.server_ids ?? raw.serverIds),
  }
}

export function normalizeUser(input: unknown): User {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    username: toText(raw.username) ?? '',
    role: toText(raw.role) ?? 'viewer',
    role_name: toText(raw.role_name ?? raw.roleName),
    permissions: toStringArray(raw.permissions),
    scope: normalizeRoleScope(raw.scope),
  }
}

export function normalizePermission(input: unknown): Permission {
  const raw = toRecord(input) ?? {}
  return {
    key: toText(raw.key) ?? '',
    name: toText(raw.name) ?? toText(raw.key) ?? '',
    description: toText(raw.description) ?? '',
    admin_only: toBool(raw.admin_only),
  }
}

export function normalizeRole(input: unknown): Role {
  const raw = toRecord(input) ?? {}
  return {
    key: toText(raw.key) ?? '',
    name: toText(raw.name) ?? '',
    description: toText(raw.description) ?? '',
    builtin: toBool(raw.builtin),
    permissions: toStringArray(raw.permissions),
    scope: normalizeRoleScope(raw.scope),
    user_count: toNumber(raw.user_count) ?? 0,
    created_at: toNumber(raw.created_at),
    updated_at: toNumber(raw.updated_at),
  }
}

export function normalizeServerGroup(input: unknown): ServerGroup {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '',
    description: toText(raw.description) ?? '',
    member_count: toNumber(raw.member_count) ?? 0,
    created_at: toNumber(raw.created_at),
    updated_at: toNumber(raw.updated_at),
  }
}

export function normalizeManagedUser(input: unknown): ManagedUser {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    username: toText(raw.username) ?? '',
    role: toText(raw.role) ?? 'viewer',
    role_name: toText(raw.role_name ?? raw.roleName),
    disabled: toBool(raw.disabled),
    locked: toBool(raw.locked),
    locked_until: toNumber(raw.locked_until),
    failed_attempts: toNumber(raw.failed_attempts) ?? 0,
    created_at: toNumber(raw.created_at),
    updated_at: toNumber(raw.updated_at),
  }
}

export function normalizeApiToken(input: unknown): CreatedApiToken {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '',
    role: toText(raw.role) ?? 'viewer',
    created_by: toText(raw.created_by),
    created_at: toNumber(raw.created_at),
    last_used_at: toNumber(raw.last_used_at),
    expires_at: toNumber(raw.expires_at),
    revoked_at: toNumber(raw.revoked_at),
    token: toText(raw.token),
  }
}

export function normalizeScheduledTask(input: unknown): ScheduledTask {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? '',
    cron: toText(raw.cron) ?? '',
    command: toText(raw.command) ?? '',
    timeout_s: toNumber(raw.timeout_s) ?? 60,
    enabled: toBool(raw.enabled, true),
    concurrency: toNumber(raw.concurrency) ?? 4,
    created_at: toNumber(raw.created_at),
    last_run_at: toNumber(raw.last_run_at),
    next_run_at: toNumber(raw.next_run_at),
    server_ids: toStringArray(raw.server_ids ?? raw.servers ?? raw.targets),
  }
}

export function normalizeScheduledTaskRun(input: unknown): ScheduledTaskRun {
  const raw = toRecord(input) ?? {}
  return {
    id: toNumber(raw.id) ?? 0,
    task_id: toText(raw.task_id) ?? '',
    server_id: toText(raw.server_id) ?? '',
    ts: toNumber(raw.ts),
    status: toText(raw.status) ?? 'pending',
    exit_code: toNumber(raw.exit_code),
    duration_ms: toNumber(raw.duration_ms),
    stdout: typeof raw.stdout === 'string' ? raw.stdout : null,
    stderr: typeof raw.stderr === 'string' ? raw.stderr : null,
    message: toText(raw.message),
  }
}

export function normalizeNetworkConfig(input: unknown): NetworkConfig {
  const raw = toRecord(input) ?? {}
  return {
    listen: toText(raw.listen),
    listen_source: toText(raw.listen_source) ?? 'default',
    active_listen: toText(raw.active_listen) ?? '',
    public_url: toText(raw.public_url),
    public_url_source: toText(raw.public_url_source) ?? 'default',
    default_ssh_port: toNumber(raw.default_ssh_port) ?? 22,
    default_ssh_port_source: toText(raw.default_ssh_port_source) ?? 'default',
    fallback_reason: toText(raw.fallback_reason),
    restart_required: toBool(raw.restart_required),
  }
}

export function normalizeUpdateInfo(input: unknown): UpdateInfo {
  const raw = toRecord(input) ?? {}
  return {
    current: toText(raw.current) ?? '',
    latest: toText(raw.latest),
    has_update: toBool(raw.has_update),
    published_at: toNumber(raw.published_at),
    release_url: toText(raw.release_url),
    notes: typeof raw.notes === 'string' ? raw.notes : null,
    schema_changed: toBool(raw.schema_changed),
    assets: extractList(raw.assets, ['assets']).map((item) => {
      const asset = toRecord(item) ?? {}
      return {
        os: toText(asset.os) ?? '',
        arch: toText(asset.arch) ?? '',
        url: toText(asset.url) ?? '',
        size_bytes: toNumber(asset.size_bytes) ?? 0,
      }
    }),
    upgradable: toBool(raw.upgradable, true),
    upgrade_note: toText(raw.upgrade_note),
    checked_at: toNumber(raw.checked_at),
    error: toText(raw.error),
    upgrade_enabled: toBool(raw.upgrade_enabled, true),
    check_interval: toText(raw.check_interval),
  }
}

function normalizeUpdatePhase(value: unknown): UpdatePhase {
  const phase = String(value ?? '').toLowerCase().replace(/-/g, '_')
  const map: Record<string, UpdatePhase> = {
    download: 'downloading',
    downloading: 'downloading',
    backup: 'backing_up',
    backing_up: 'backing_up',
    replace: 'replacing',
    replacing: 'replacing',
    restart: 'restarting',
    restarting: 'restarting',
    success: 'success',
    succeeded: 'success',
    done: 'success',
    failed: 'failed',
    failure: 'failed',
    rollback: 'rolled_back',
    rolled_back: 'rolled_back',
  }
  return map[phase] ?? (phase === '' ? 'idle' : phase)
}

export function normalizeUpdateStatus(input: unknown): UpdateStatus {
  const raw = toRecord(input) ?? {}
  return {
    job_id: toText(raw.job_id ?? raw.jobId),
    phase: normalizeUpdatePhase(raw.phase ?? raw.stage ?? raw.state),
    target_version: toText(raw.target_version),
    current_version: toText(raw.current_version),
    message: toText(raw.message),
    error: toText(raw.error),
    rolled_back: toBool(raw.rolled_back ?? raw.rollback),
    backup_file: toText(raw.backup_file),
    progress: toNumber(raw.progress),
    started_at: toNumber(raw.started_at),
    finished_at: toNumber(raw.finished_at),
  }
}

export function normalizeCredential(input: unknown): Credential {
  const raw = toRecord(input) ?? {}
  return {
    id: String(raw.id ?? ''),
    name: toText(raw.name) ?? t('s.0591'),
    kind: toText(raw.kind) ?? 'password',
    username: toText(raw.username) ?? '',
    fingerprint: toText(raw.fingerprint),
    created_at: toNumber(raw.created_at),
    server_count: toNumber(raw.server_count) ?? 0,
    panel_url: toText(raw.panel_url),
    insecure_skip_verify: toBool(raw.insecure_skip_verify),
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
    name: toText(raw.name) ?? t('s.0592'),
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
    group_ids: toStringArray(raw.group_ids ?? raw.groupIds),
    group_names: toStringArray(raw.group_names ?? raw.groupNames),
    panel_credential_id: toText(raw.panel_credential_id),
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

  // 最多补拉 20 台，避免对控制端造成请求风暴（纳管 列表已带指标，通常不触发）。
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
  host?: string
  port?: number
  tags?: string[]
  group?: string | null
  /** 整包替换；缺省表示不改（契约第 55.4 节） */
  group_ids?: string[]
  panel_credential_id?: string | null
}

/** POST /servers 请求体（纳管 扩展字段，契约第 10 节）。 */
export interface CreateServerPayload {
  name: string
  host: string
  port: number
  mode: 'agent' | 'ssh'
  credential_id?: string
  group?: string
  group_ids?: string[]
  panel_credential_id?: string | null
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

export interface BaotaCredentialInput {
  name: string
  kind: 'baota'
  panel_url: string
  api_key: string
  insecure_skip_verify?: boolean
}

export type CreateCredentialInput =
  | PasswordCredentialInput
  | KeyCredentialInput
  | BaotaCredentialInput

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
    return request<unknown>('/auth/login', {
      method: 'POST',
      body: { username, password },
      auth: false,
    }).then((raw) => {
      const record = toRecord(raw) ?? {}
      const userRaw = toRecord(record.user) ?? record
      return {
        token: toText(record.token) ?? '',
        user: normalizeUser(userRaw),
      }
    })
  },

  logout(): Promise<unknown> {
    return request<unknown>('/auth/logout', { method: 'POST' })
  },

  me(): Promise<User> {
    return request<unknown>('/auth/me').then(normalizeUser)
  },

  /** GET /system/update —— 检查更新（缓存） */
  checkForUpdate(): Promise<UpdateInfo> {
    return request<unknown>('/system/update').then(normalizeUpdateInfo)
  },

  /** POST /system/update/check —— 强制刷新检查 */
  refreshUpdateCheck(): Promise<UpdateInfo> {
    return request<unknown>('/system/update/check', { method: 'POST' }).then(normalizeUpdateInfo)
  },

  /** POST /system/update/apply —— 触发升级（自动备份 + 失败回退） */
  applyUpdate(payload: { version: string; allow_major?: boolean }): Promise<{ job_id: string | null }> {
    return request<unknown>('/system/update/apply', { method: 'POST', body: payload }).then((raw) => {
      const record = toRecord(raw) ?? {}
      return { job_id: toText(record.job_id ?? record.jobId) }
    })
  },

  /** GET /system/update/status —— 升级进度（2s 轮询） */
  updateStatus(): Promise<UpdateStatus> {
    return request<unknown>('/system/update/status').then(normalizeUpdateStatus)
  },

  /** GET /healthz —— 不经过 /api/v1，用于升级重启期间探测服务恢复 */
  async health(): Promise<HealthInfo> {
    let response: Response
    try {
      response = await fetch('/healthz', { headers: { Accept: 'application/json' } })
    } catch {
      throw new ApiError(0, 'network_error', 'healthz unreachable')
    }
    if (!response.ok) throw new ApiError(response.status, 'health_failed', `HTTP ${response.status}`)
    const payload = (await response.json()) as unknown
    const raw = toRecord(payload)
    const data = toRecord(raw?.data) ?? raw
    return {
      status: toText(data?.status) ?? 'ok',
      version: toText(data?.version) ?? '',
      uptime_s: toNumber(data?.uptime_s) ?? 0,
    }
  },

  /** GET /system/network —— 监听地址/对外地址/默认 SSH 端口（契约 §49） */
  network(): Promise<NetworkConfig> {
    return request<unknown>('/system/network').then(normalizeNetworkConfig)
  },

  updateNetwork(payload: NetworkUpdatePayload): Promise<NetworkConfig> {
    return request<unknown>('/system/network', { method: 'PUT', body: payload }).then(
      normalizeNetworkConfig,
    )
  },

  resetNetwork(): Promise<NetworkConfig> {
    return request<unknown>('/system/network/reset', { method: 'POST' }).then(
      normalizeNetworkConfig,
    )
  },

  /* ---------------------- 权限 / 角色 / 分组（契约 §52–§55） ---------------------- */

  permissions(): Promise<Permission[]> {
    return request<unknown>('/permissions').then((raw) =>
      extractList(raw, ['permissions', 'items']).map(normalizePermission),
    )
  },

  roles(): Promise<Role[]> {
    return request<unknown>('/roles').then((raw) =>
      extractList(raw, ['roles', 'items']).map(normalizeRole),
    )
  },

  createRole(payload: RolePayload): Promise<Role> {
    return request<unknown>('/roles', { method: 'POST', body: payload }).then(normalizeRole)
  },

  updateRole(key: string, payload: Partial<RolePayload>): Promise<Role> {
    return request<unknown>(`/roles/${encodeURIComponent(key)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeRole)
  },

  deleteRole(key: string): Promise<unknown> {
    return request<unknown>(`/roles/${encodeURIComponent(key)}`, { method: 'DELETE' })
  },

  serverGroups(): Promise<ServerGroup[]> {
    return request<unknown>('/server-groups').then((raw) =>
      extractList(raw, ['groups', 'items']).map(normalizeServerGroup),
    )
  },

  createServerGroup(payload: { name: string; description: string }): Promise<ServerGroup> {
    return request<unknown>('/server-groups', { method: 'POST', body: payload }).then(
      normalizeServerGroup,
    )
  },

  updateServerGroup(
    id: string,
    payload: { name?: string; description?: string },
  ): Promise<ServerGroup> {
    return request<unknown>(`/server-groups/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeServerGroup)
  },

  deleteServerGroup(id: string): Promise<unknown> {
    return request<unknown>(`/server-groups/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** 整包替换分组成员 */
  setServerGroupMembers(id: string, serverIds: string[]): Promise<unknown> {
    return request<unknown>(`/server-groups/${encodeURIComponent(id)}/servers`, {
      method: 'PUT',
      body: { server_ids: serverIds },
    })
  },

  serverGroupServers(id: string): Promise<Server[]> {
    return request<unknown>(`/server-groups/${encodeURIComponent(id)}/servers`).then((raw) => {
      const list = Array.isArray(raw) ? raw : extractList(raw, ['servers', 'items'])
      return list.map(normalizeServer)
    })
  },

  /** POST /servers/{id}/discover —— 接管已有应用（server.read） */
  discoverServer(serverId: string): Promise<DiscoverResult> {
    return request<unknown>(`/servers/${encodeURIComponent(serverId)}/discover`, {
      method: 'POST',
    }).then((raw) => {
      const record = toRecord(raw) ?? {}
      const rawMap: Record<string, string> = {}
      const rawRecord = toRecord(record.raw)
      if (rawRecord) {
        for (const [key, value] of Object.entries(rawRecord)) {
          rawMap[key] = typeof value === 'string' ? value : JSON.stringify(value)
        }
      }
      return {
        server_id: toText(record.server_id) ?? serverId,
        candidates: extractList(record.candidates, ['candidates']).map((item) => {
          const candidate = toRecord(item) ?? {}
          const suggested = toRecord(candidate.suggested) ?? {}
          return {
            kind: toText(candidate.kind) ?? 'site',
            name: toText(candidate.name) ?? '',
            detail: toText(candidate.detail) ?? '',
            suggested: {
              deploy_mode: toText(suggested.deploy_mode) ?? undefined,
              target_dir: toText(suggested.target_dir) ?? undefined,
              restart_cmd: toText(suggested.restart_cmd) ?? undefined,
              healthcheck_port: toNumber(suggested.healthcheck_port) ?? undefined,
              root: toText(suggested.root) ?? undefined,
            },
          }
        }),
        raw: rawMap,
      }
    })
  },

  unlockUser(id: string): Promise<unknown> {
    return request<unknown>(`/users/${encodeURIComponent(id)}/unlock`, { method: 'POST' })
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

  /** POST /credentials/{id}/test —— 仅宝塔凭据支持 */
  testCredential(id: string): Promise<{ ok: boolean; message: string }> {
    return request<unknown>(`/credentials/${encodeURIComponent(id)}/test`, {
      method: 'POST',
    }).then((raw) => {
      const record = toRecord(raw) ?? {}
      return {
        ok: toBool(record.ok),
        message: toText(record.message) ?? '',
      }
    })
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

  /* ---------------------- 检测：检测（契约第 15 节） ---------------------- */

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

  /* ---------------------- 告警：告警（契约第 20 节） ---------------------- */

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

  /* ---------------------- 部署：批量部署（契约第 27 节） ---------------------- */

  apps(): Promise<AppItem[]> {
    return request<unknown>('/apps').then((raw) =>
      extractList(raw, ['apps', 'items']).map(normalizeApp),
    )
  },

  app(id: string): Promise<AppItem> {
    return request<unknown>(`/apps/${encodeURIComponent(id)}`).then(normalizeApp)
  },

  createApp(payload: CreateAppPayload): Promise<AppItem> {
    return request<unknown>('/apps', { method: 'POST', body: payload }).then(normalizeApp)
  },

  updateApp(id: string, payload: Partial<CreateAppPayload>): Promise<AppItem> {
    return request<unknown>(`/apps/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeApp)
  },

  deleteApp(id: string): Promise<unknown> {
    return request<unknown>(`/apps/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  templates(): Promise<DeployTemplate[]> {
    return request<unknown>('/templates').then((raw) =>
      extractList(raw, ['templates', 'items']).map(normalizeTemplate),
    )
  },

  template(id: string): Promise<DeployTemplate> {
    return request<unknown>(`/templates/${encodeURIComponent(id)}`).then(normalizeTemplate)
  },

  createTemplate(payload: CreateTemplatePayload): Promise<DeployTemplate> {
    return request<unknown>('/templates', { method: 'POST', body: payload }).then(normalizeTemplate)
  },

  updateTemplate(id: string, payload: Partial<CreateTemplatePayload>): Promise<DeployTemplate> {
    return request<unknown>(`/templates/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeTemplate)
  },

  deleteTemplate(id: string): Promise<unknown> {
    return request<unknown>(`/templates/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** POST /templates/{id}/render —— 只做占位符替换预览，不执行任何命令 */
  renderTemplate(id: string, vars: Record<string, string>): Promise<string> {
    return request<unknown>(`/templates/${encodeURIComponent(id)}/render`, {
      method: 'POST',
      body: { vars },
    }).then((raw) => toText(toRecord(raw)?.content) ?? '')
  },

  /** POST /artifacts（multipart）—— 上传制品 */
  uploadArtifact(file: File): Promise<Artifact> {
    const form = new FormData()
    form.append('file', file, file.name)
    return requestMultipart<unknown>('/artifacts', form).then(normalizeArtifact)
  },

  /**
   * POST /artifacts —— 登记 URL 制品。
   * 后端要求必须带 64 位十六进制 sha256（目标机下载后校验，校验不过不切换 current），缺失会 400。
   */
  registerUrlArtifact(url: string, sha256: string, sizeBytes?: number): Promise<Artifact> {
    return request<unknown>('/artifacts', {
      method: 'POST',
      body: {
        kind: 'url',
        url,
        sha256: sha256.trim().toLowerCase(),
        size_bytes: sizeBytes,
      },
    }).then(normalizeArtifact)
  },

  releases(filters: ReleaseFilters = {}): Promise<Release[]> {
    const params = new URLSearchParams()
    if (filters.app_id) params.set('app_id', filters.app_id)
    if (filters.status) params.set('status', filters.status)
    if (filters.limit) params.set('limit', String(filters.limit))
    const suffix = params.toString() ? `?${params.toString()}` : ''
    return request<unknown>(`/releases${suffix}`).then((raw) =>
      extractList(raw, ['releases', 'items']).map(normalizeRelease),
    )
  },

  /** POST /releases/preview —— 只读发布计划预览 */
  previewRelease(payload: {
    app_id: string
    template_id: string
    version: string
    artifact_id?: string
    server_ids: string[]
  }): Promise<ReleasePreview> {
    return request<unknown>('/releases/preview', { method: 'POST', body: payload }).then((raw) => {
      const record = toRecord(raw) ?? {}
      return {
        app_id: toText(record.app_id) ?? '',
        app_slug: toText(record.app_slug) ?? '',
        version: toText(record.version) ?? '',
        provider: toText(record.provider) ?? 'builtin',
        deploy_mode: toText(record.deploy_mode) ?? 'release',
        servers: extractList(record.servers, ['servers']).map((item) => {
          const plan = toRecord(item) ?? {}
          const normalizeSteps = (value: unknown): PreviewStep[] =>
            extractList(value, ['steps']).map((stepRaw) => {
              const step = toRecord(stepRaw) ?? {}
              return {
                name: toText(step.name) ?? '',
                command: typeof step.command === 'string' ? step.command : '',
                timeout_s: toNumber(step.timeout_s),
                secret: toBool(step.secret),
                ignore_failure: toBool(step.ignore_failure),
              }
            })
          return {
            server_id: toText(plan.server_id) ?? '',
            server_name: toText(plan.server_name) ?? '',
            plan_root: toText(plan.plan_root),
            steps: normalizeSteps(plan.steps),
            rollback_steps: normalizeSteps(plan.rollback_steps),
            notes: extractList(plan.notes, ['notes']).map((note) => String(note)),
          }
        }),
      }
    })
  },

  /** GET /deploy/presets —— 内置部署预设 */
  listDeployPresets(): Promise<DeployPreset[]> {
    return request<unknown>('/deploy/presets').then((raw) =>
      extractList(raw, ['presets', 'items']).map((item) => {
        const preset = toRecord(item) ?? {}
        const template = toRecord(preset.template) ?? {}
        const defaults = toRecord(preset.defaults) ?? {}
        return {
          key: toText(preset.key) ?? '',
          name: toText(preset.name) ?? '',
          description: toText(preset.description) ?? '',
          template: {
            name: toText(template.name) ?? '',
            kind: toText(template.kind) ?? 'binary',
            content: typeof template.content === 'string' ? template.content : '',
          },
          hints: extractList(preset.hints, ['hints']).map((hint) => String(hint)),
          defaults: {
            deploy_mode: toText(defaults.deploy_mode) ?? undefined,
            keep_paths: toText(defaults.keep_paths) ?? undefined,
            healthcheck_path: toText(defaults.healthcheck_path) ?? undefined,
            provider: toText(defaults.provider) ?? undefined,
            site_type: toText(defaults.site_type) ?? undefined,
            site_port: toNumber(defaults.site_port) ?? undefined,
          },
        }
      }),
    )
  },

  createRelease(payload: CreateReleasePayload): Promise<CreateReleaseResponse> {
    return request<unknown>('/releases', { method: 'POST', body: payload }).then((raw) => {
      const record = toRecord(raw) ?? {}
      const releaseRaw = record.release ?? raw
      return {
        release: normalizeRelease(releaseRaw),
        runs: extractList(record.runs, ['runs']).map(normalizeReleaseRun),
      }
    })
  },

  /** GET /releases/{id} → {release, runs}；这里拍平成带 runs 的 Release */
  release(id: string): Promise<Release> {
    return request<unknown>(`/releases/${encodeURIComponent(id)}`).then((raw) => {
      const record = toRecord(raw) ?? {}
      const release = normalizeRelease(record.release ?? raw)
      const runs = extractList(record.runs, ['runs']).map(normalizeReleaseRun)
      return runs.length > 0 ? { ...release, runs } : release
    })
  },

  releaseRuns(id: string): Promise<ReleaseRun[]> {
    return request<unknown>(`/releases/${encodeURIComponent(id)}/runs`).then((raw) =>
      extractList(raw, ['runs', 'items']).map(normalizeReleaseRun),
    )
  },

  /** GET /releases/{id}/runs/{serverID}/logs?after_seq= —— 断点续读增量日志 */
  releaseLogs(
    id: string,
    serverId: string,
    afterSeq: number,
  ): Promise<ReleaseLogEntry[]> {
    const params = new URLSearchParams({ after_seq: String(afterSeq) })
    return request<unknown>(
      `/releases/${encodeURIComponent(id)}/runs/${encodeURIComponent(serverId)}/logs?${params.toString()}`,
    ).then(normalizeReleaseLogs)
  },

  /** POST /releases/{id}/retry —— 只重跑 failed 的 run */
  retryRelease(id: string): Promise<CreateReleaseResponse> {
    return request<unknown>(`/releases/${encodeURIComponent(id)}/retry`, { method: 'POST' }).then(
      (raw) => {
        const record = toRecord(raw) ?? {}
        return {
          release: normalizeRelease(record.release ?? raw),
          runs: extractList(record.runs, ['runs']).map(normalizeReleaseRun),
        }
      },
    )
  },

  /** GET /apps/{appID}/rollback-targets —— 可回滚的成功版本 */
  rollbackTargets(appId: string, serverId?: string): Promise<RollbackTarget[]> {
    const suffix = serverId ? `?server_id=${encodeURIComponent(serverId)}` : ''
    return request<unknown>(`/apps/${encodeURIComponent(appId)}/rollback-targets${suffix}`).then(
      (raw) =>
        extractList(raw, ['targets', 'items']).map((item) => {
          const target = toRecord(item) ?? {}
          return {
            release_id: toText(target.release_id) ?? '',
            version: toText(target.version) ?? '',
            created_at: toNumber(target.created_at),
            finished_at: toNumber(target.finished_at),
            server_ids: toStringArray(target.server_ids),
          }
        }),
    )
  },

  /** POST /releases/{id}/rollback —— 回滚；带 target_release_id 回滚到指定版本 */
  rollbackRelease(
    id: string,
    payload?: { server_ids?: string[]; target_release_id?: string },
  ): Promise<unknown> {
    return request<unknown>(`/releases/${encodeURIComponent(id)}/rollback`, {
      method: 'POST',
      body: payload ?? {},
    })
  },

  /** POST /servers/{id}/diagnose —— 接管前体检 */
  diagnoseServer(
    serverId: string,
    payload: { kind: string; name: string; target_dir: string },
  ): Promise<DiagnoseResult> {
    return request<unknown>(`/servers/${encodeURIComponent(serverId)}/diagnose`, {
      method: 'POST',
      body: payload,
    }).then((raw) => {
      const record = toRecord(raw) ?? {}
      const recommendation = toRecord(record.recommendation) ?? {}
      return {
        server_id: toText(record.server_id) ?? serverId,
        name: toText(record.name) ?? payload.name,
        kind: toText(record.kind) ?? payload.kind,
        target_dir: toText(record.target_dir) ?? payload.target_dir,
        checks: extractList(record.checks, ['checks']).map((item) => {
          const check = toRecord(item) ?? {}
          return {
            id: toText(check.id) ?? '',
            title: toText(check.title) ?? '',
            status: toText(check.status) ?? 'warn',
            detail: toText(check.detail) ?? '',
          }
        }),
        recommendation: {
          deploy_mode: toText(recommendation.deploy_mode) ?? 'inplace',
          target_dir: toText(recommendation.target_dir) ?? payload.target_dir,
          restart_cmd: toText(recommendation.restart_cmd) ?? undefined,
          keep_paths: toText(recommendation.keep_paths) ?? undefined,
          healthcheck_port: toNumber(recommendation.healthcheck_port) ?? undefined,
          script: toText(recommendation.script) ?? undefined,
        },
      }
    })
  },

  /** POST /releases/{id}/cancel —— 取消仍在 pending/running 的 run */
  cancelRelease(id: string): Promise<unknown> {
    return request<unknown>(`/releases/${encodeURIComponent(id)}/cancel`, { method: 'POST' })
  },

  /* ---------------------- 备份与恢复（契约第 33 节） ---------------------- */

  backups(): Promise<BackupInfo[]> {
    return request<unknown>('/backups').then((raw) =>
      extractList(raw, ['backups', 'items']).map(normalizeBackupInfo),
    )
  },

  /** POST /backups → 201 + Info（在线备份，不阻塞写入过久） */
  createBackup(includeArtifacts: boolean): Promise<BackupInfo> {
    return request<unknown>('/backups', {
      method: 'POST',
      body: { include_artifacts: includeArtifacts },
    }).then(normalizeBackupInfo)
  },

  /** GET /backups/{name}/download —— 带鉴权下载，返回 Blob（前端再触发保存） */
  async downloadBackup(name: string): Promise<BackupDownload> {
    const token = getToken()
    const response = await fetch(`${API_PREFIX}/backups/${encodeURIComponent(name)}/download`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    })
    if (!response.ok) {
      let message = t('s.0593', { '0': response.status })
      try {
        const payload = (await response.json()) as ErrorEnvelope
        if (payload?.error?.message) message = payload.error.message
      } catch {
        /* ignore */
      }
      if (response.status === 401) {
        clearToken()
        unauthorizedHandler?.()
      }
      throw new ApiError(response.status, 'download_failed', message)
    }
    const disposition = response.headers.get('Content-Disposition') ?? ''
    const match = /filename\*?=(?:UTF-8''|")?([^";]+)/i.exec(disposition)
    const filename = match ? decodeURIComponent(match[1].replace(/"/g, '')) : name
    return { blob: await response.blob(), filename }
  },

  /** POST /backups/{name}/verify → {ok, manifest}；失败为 400 + 可读原因 */
  verifyBackup(name: string): Promise<BackupVerifyResult> {
    return request<unknown>(`/backups/${encodeURIComponent(name)}/verify`, {
      method: 'POST',
    }).then(normalizeBackupVerifyResult)
  },

  deleteBackup(name: string): Promise<unknown> {
    return request<unknown>(`/backups/${encodeURIComponent(name)}`, { method: 'DELETE' })
  },

  /** POST /backups/restore（multipart 上传归档）→ 202 {restart_required:true} */
  restoreBackup(file: File): Promise<RestoreStagedResult> {
    const form = new FormData()
    form.append('file', file, file.name)
    return requestMultipart<unknown>('/backups/restore', form).then(normalizeRestoreStagedResult)
  },

  backupSettings(): Promise<BackupSettings> {
    return request<unknown>('/backup-settings').then(normalizeBackupSettings)
  },

  updateBackupSettings(payload: BackupSettings): Promise<BackupSettings> {
    return request<unknown>('/backup-settings', { method: 'PATCH', body: payload }).then(
      normalizeBackupSettings,
    )
  },

  /* ---------------------- 运维与协作：运维增强（契约第 36–45 节） ---------------------- */

  auditLogs(filters: AuditFilters = {}): Promise<Paged<AuditLogEntry>> {
    const params = new URLSearchParams()
    if (filters.actor) params.set('actor', filters.actor)
    if (filters.action) params.set('action', filters.action)
    if (filters.resource_type) params.set('resource_type', filters.resource_type)
    if (filters.from) params.set('from', String(filters.from))
    if (filters.to) params.set('to', String(filters.to))
    if (filters.limit) params.set('limit', String(filters.limit))
    if (filters.cursor) params.set('cursor', filters.cursor)
    const suffix = params.toString() ? `?${params.toString()}` : ''
    return requestPaged(`/audit-logs${suffix}`, normalizeAuditLog)
  },

  users(): Promise<ManagedUser[]> {
    return request<unknown>('/users').then((raw) =>
      extractList(raw, ['users', 'items']).map(normalizeManagedUser),
    )
  },

  createUser(payload: { username: string; password: string; role: string }): Promise<ManagedUser> {
    return request<unknown>('/users', { method: 'POST', body: payload }).then(normalizeManagedUser)
  },

  updateUser(
    id: string,
    payload: { role?: string; disabled?: boolean; password?: string },
  ): Promise<ManagedUser> {
    return request<unknown>(`/users/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeManagedUser)
  },

  deleteUser(id: string): Promise<unknown> {
    return request<unknown>(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  changeMyPassword(payload: { old_password: string; new_password: string }): Promise<unknown> {
    return request<unknown>('/users/me/password', { method: 'POST', body: payload })
  },

  apiTokens(): Promise<ApiToken[]> {
    return request<unknown>('/api-tokens').then((raw) =>
      extractList(raw, ['tokens', 'items']).map(normalizeApiToken),
    )
  },

  createApiToken(payload: {
    name: string
    role: UserRole
    expires_in?: string
  }): Promise<CreatedApiToken> {
    return request<unknown>('/api-tokens', { method: 'POST', body: payload }).then(normalizeApiToken)
  },

  deleteApiToken(id: string): Promise<unknown> {
    return request<unknown>(`/api-tokens/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  scheduledTasks(): Promise<ScheduledTask[]> {
    return request<unknown>('/scheduled-tasks').then((raw) =>
      extractList(raw, ['tasks', 'items']).map(normalizeScheduledTask),
    )
  },

  createScheduledTask(payload: ScheduledTaskPayload): Promise<ScheduledTask> {
    return request<unknown>('/scheduled-tasks', { method: 'POST', body: payload }).then(
      normalizeScheduledTask,
    )
  },

  updateScheduledTask(
    id: string,
    payload: Partial<ScheduledTaskPayload>,
  ): Promise<ScheduledTask> {
    return request<unknown>(`/scheduled-tasks/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: payload,
    }).then(normalizeScheduledTask)
  },

  deleteScheduledTask(id: string): Promise<unknown> {
    return request<unknown>(`/scheduled-tasks/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  runScheduledTask(id: string): Promise<unknown> {
    return request<unknown>(`/scheduled-tasks/${encodeURIComponent(id)}/run`, { method: 'POST' })
  },

  scheduledTaskRuns(id: string, limit = 50): Promise<ScheduledTaskRun[]> {
    return request<unknown>(
      `/scheduled-tasks/${encodeURIComponent(id)}/runs?limit=${limit}`,
    ).then((raw) => extractList(raw, ['runs', 'items']).map(normalizeScheduledTaskRun))
  },

  importServers(payload: ImportServersPayload): Promise<ImportServersResult> {
    return request<unknown>('/servers/import', { method: 'POST', body: payload }).then((raw) => {
      const record = toRecord(raw) ?? {}
      return {
        total: toNumber(record.total) ?? 0,
        created: extractList(record.created, ['created']).map((item) => {
          const entry = toRecord(item) ?? {}
          return {
            host: toText(entry.host) ?? '',
            server_id: toText(entry.server_id ?? entry.id) ?? '',
          }
        }),
        failed: extractList(record.failed, ['failed']).map((item) => {
          const entry = toRecord(item) ?? {}
          return { host: toText(entry.host) ?? '', error: toText(entry.error) ?? t('s.0301') }
        }),
      }
    })
  },

  /** POST /servers/{id}/terminal-ticket —— 一次性 ticket（60s，避免 JWT 进 URL） */
  createTerminalTicket(serverId: string): Promise<TerminalTicket> {
    return request<unknown>(`/servers/${encodeURIComponent(serverId)}/terminal-ticket`, {
      method: 'POST',
    }).then((raw) => {
      const record = toRecord(raw) ?? {}
      return {
        ticket: toText(record.ticket) ?? '',
        expires_in: toNumber(record.expires_in) ?? 60,
      }
    })
  },

  listFiles(serverId: string, path: string): Promise<RemoteFileEntry[]> {
    const params = new URLSearchParams({ path })
    return request<unknown>(
      `/servers/${encodeURIComponent(serverId)}/files?${params.toString()}`,
    ).then((raw) =>
      extractList(raw, ['entries', 'items']).map((item) => {
        const entry = toRecord(item) ?? {}
        return {
          name: toText(entry.name) ?? '',
          is_dir: toBool(entry.is_dir),
          size: toNumber(entry.size) ?? 0,
          mod_time: toNumber(entry.mod_time),
          mode: toText(entry.mode),
        }
      }),
    )
  },

  async downloadServerFile(serverId: string, path: string): Promise<RemoteFileDownload> {
    const token = getToken()
    const params = new URLSearchParams({ path })
    const response = await fetch(
      `${API_PREFIX}/servers/${encodeURIComponent(serverId)}/files/download?${params.toString()}`,
      { headers: token ? { Authorization: `Bearer ${token}` } : {} },
    )
    if (!response.ok) {
      let message = t('s.0593', { '0': response.status })
      try {
        const payload = (await response.json()) as ErrorEnvelope
        if (payload?.error?.message) message = payload.error.message
      } catch {
        /* ignore */
      }
      if (response.status === 401) {
        clearToken()
        unauthorizedHandler?.()
      }
      throw new ApiError(response.status, 'download_failed', message)
    }
    const disposition = response.headers.get('Content-Disposition') ?? ''
    const match = /filename\*?=(?:UTF-8''|")?([^";]+)/i.exec(disposition)
    const filename = match
      ? decodeURIComponent(match[1].replace(/"/g, ''))
      : path.split('/').pop() || 'download'
    return { blob: await response.blob(), filename }
  },

  uploadServerFile(
    serverId: string,
    path: string,
    file: File,
  ): Promise<{ ok: boolean; bytes: number }> {
    const form = new FormData()
    form.append('path', path)
    form.append('file', file, file.name)
    return requestMultipart<unknown>(
      `/servers/${encodeURIComponent(serverId)}/files/upload`,
      form,
    ).then((raw) => {
      const record = toRecord(raw) ?? {}
      return { ok: toBool(record.ok, true), bytes: toNumber(record.bytes) ?? file.size }
    })
  },

  deleteServerFile(serverId: string, path: string): Promise<unknown> {
    const params = new URLSearchParams({ path })
    return request<unknown>(
      `/servers/${encodeURIComponent(serverId)}/files?${params.toString()}`,
      { method: 'DELETE' },
    )
  },

  serverLogs(
    serverId: string,
    query: { path: string; lines?: number; grep?: string },
  ): Promise<ServerLogsResult> {
    const params = new URLSearchParams({ path: query.path })
    if (query.lines) params.set('lines', String(query.lines))
    if (query.grep) params.set('grep', query.grep)
    return request<unknown>(
      `/servers/${encodeURIComponent(serverId)}/logs?${params.toString()}`,
    ).then((raw) => {
      const record = toRecord(raw) ?? {}
      return {
        lines: extractList(raw, ['lines']).map((line) => String(line)),
        truncated: toBool(record.truncated),
      }
    })
  },

  serverProcesses(
    serverId: string,
    query: { sort?: string; limit?: number } = {},
  ): Promise<RemoteProcess[]> {
    const params = new URLSearchParams()
    if (query.sort) params.set('sort', query.sort)
    if (query.limit) params.set('limit', String(query.limit))
    const suffix = params.toString() ? `?${params.toString()}` : ''
    return request<unknown>(
      `/servers/${encodeURIComponent(serverId)}/processes${suffix}`,
    ).then((raw) =>
      extractList(raw, ['processes', 'items']).map((item) => {
        const entry = toRecord(item) ?? {}
        return {
          pid: toNumber(entry.pid) ?? 0,
          user: toText(entry.user),
          cpu_pct: toNumber(entry.cpu_pct),
          mem_pct: toNumber(entry.mem_pct),
          rss_kb: toNumber(entry.rss_kb),
          command: toText(entry.command) ?? '',
        }
      }),
    )
  },

  serverPorts(serverId: string): Promise<RemotePort[]> {
    return request<unknown>(`/servers/${encodeURIComponent(serverId)}/ports`).then((raw) =>
      extractList(raw, ['ports', 'items']).map((item) => {
        const entry = toRecord(item) ?? {}
        return {
          proto: toText(entry.proto) ?? '',
          local: toText(entry.local) ?? '',
          pid: toNumber(entry.pid),
          process: toText(entry.process),
        }
      }),
    )
  },
}

/** 安装命令里要填的控制端地址：开发态指向被代理的后端，生产态用当前同源地址。 */
export function resolveControlUrl(): string {
  if (import.meta.env.DEV) return 'http://127.0.0.1:8080'
  return window.location.origin
}
