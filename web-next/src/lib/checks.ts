import { t } from '../i18n'
import type { CheckItem, CheckResult, CheckState, RunCheckResponse } from './api'
import { formatLatency } from './format'

/**
 * 检测页（ChecksPage）的纯函数与常量：筛选、分页、文案与状态映射只此一处。
 *
 * 组件只做渲染与事件派发，页面只持筛选/分页/抽屉三个 UI 状态；
 * 后端既有字段名（kind / target / interval_s / params.probe_via / server_ids / last_result）
 * 全部沿用 normalizeCheck 的结果，不在这里另造字段。
 */

/** 列表每页条数（后端 GET /checks 不分页，这里做纯前端分页，避免一次渲染上百行）。 */
export const CHECKS_PAGE_SIZE = 20

/** 类型下拉的固定顺序（未知类型追加在后面，不丢数据）。 */
export const CHECK_KIND_ORDER = ['tcp', 'http', 'ping', 'process', 'script'] as const

export interface CheckFilterState {
  keyword: string
  /** 空串 = 全部类型 */
  kind: string
  /** 空串 = 全部状态 */
  state: '' | CheckState
}

export function emptyCheckFilters(): CheckFilterState {
  return { keyword: '', kind: '', state: '' }
}

/**
 * 筛选语义（与旧版 web/src/pages/ChecksPage.tsx 对齐）：条件之间是 AND，
 * 关键字对名称 / 目标 / 类型 / 目标服务器名做大小写不敏感的子串匹配。
 *
 * serverNameOf 由调用方注入（纯函数不持有服务器清单），缺省把 id 当名字，
 * 这样即使名称解析不可用，按 id 搜索也仍然有效。
 */
export function filterChecks(
  checks: readonly CheckItem[],
  filters: CheckFilterState,
  serverNameOf: (serverId: string) => string = (serverId) => serverId,
): CheckItem[] {
  const keyword = filters.keyword.trim().toLowerCase()
  return checks.filter((check) => {
    if (filters.state && check.state !== filters.state) return false
    if (filters.kind && check.kind !== filters.kind) return false
    if (!keyword) return true
    const haystack = [
      check.name,
      check.target,
      check.kind,
      ...check.server_ids.map((id) => serverNameOf(id)),
    ]
      .join('\n')
      .toLowerCase()
    return haystack.includes(keyword)
  })
}

export function hasActiveCheckFilters(filters: CheckFilterState): boolean {
  return Boolean(filters.keyword.trim() || filters.kind || filters.state)
}

/** 类型下拉的取值：已知类型按固定顺序在前，数据里出现的未知类型按字母序追加。 */
export function collectKindOptions(checks: readonly CheckItem[]): string[] {
  const seen = new Set<string>()
  for (const check of checks) if (check.kind) seen.add(check.kind)
  const known = CHECK_KIND_ORDER.filter((kind) => seen.has(kind))
  const extra = [...seen]
    .filter((kind) => !(CHECK_KIND_ORDER as readonly string[]).includes(kind))
    .sort((left, right) => left.localeCompare(right))
  return [...known, ...extra]
}

/** 检测类型 → 展示文案（tcp/http/ping 是协议名，不翻译）。 */
export function checkKindLabel(kind: string): string {
  if (kind === 'tcp') return 'TCP'
  if (kind === 'http') return 'HTTP'
  if (kind === 'ping') return 'Ping'
  if (kind === 'process') return t('s.0314')
  if (kind === 'script') return t('s.0315')
  return kind || t('s.0089')
}

/**
 * 探测通道是否为 agent 代检（后端字段 params.probe_via，仅 tcp/http/ping 支持）。
 * 旧界面在类型列标出「agent 代检」，本页保留这个前端标记，不新增后端字段。
 */
export function isProbeAgent(check: CheckItem): boolean {
  return check.params?.probe_via === 'agent'
}

export function enabledLabelKey(enabled: boolean): string {
  return enabled ? 'checks.enabled' : 'checks.disabled'
}

/** 检测状态 → i18n 文案键 / 语义色（与服务器状态同一套口径）。 */
export function checkStateLabelKey(state: CheckState | string): string {
  if (state === 'up') return 's.0317'
  if (state === 'down') return 's.0318'
  return 's.0089'
}

export function checkStateTone(state: CheckState | string): 'success' | 'danger' | 'muted' {
  if (state === 'up') return 'success'
  if (state === 'down') return 'danger'
  return 'muted'
}

/** 24h 可用率 → 语义色：≥99.9 正常、≥99 警示、其余错误、无数据中性。 */
export function availabilityTone(
  value: number | null,
): 'success' | 'warning' | 'danger' | 'muted' {
  if (value === null) return 'muted'
  if (value >= 99.9) return 'success'
  if (value >= 99) return 'warning'
  return 'danger'
}

/** 可用率文案：固定两位小数，数字与 % 分开呈现，无数据给破折号。 */
export function availabilityText(value: number | null): string {
  if (value === null || Number.isNaN(value)) return '—'
  return `${value.toFixed(2)}%`
}

/**
 * 服务器 id → 名称的解析器。
 * 检测项列表只带 server_ids（不带名称），但每项的最近结果里有 server_name；
 * 用这些名字建索引，让关键字搜索能命中「服务器名」而不必额外请求 /servers
 * （那样会顺带依赖 server.read 权限，范围角色反而看不到自己的机器）。
 */
export function serverNameIndex(checks: readonly CheckItem[]): (serverId: string) => string {
  const names = new Map<string, string>()
  for (const check of checks) {
    const result = check.last_result
    if (result?.server_id && result.server_name) names.set(result.server_id, result.server_name)
  }
  return (serverId: string) => names.get(serverId) ?? serverId
}

/** 最近一次结果的成功/失败文案键；无结果返回 null，由渲染层显示「暂无结果」。 */
export function lastResultStateKey(check: CheckItem): string | null {
  if (!check.last_result) return null
  return check.last_result.ok ? 's.0300' : 's.0301'
}

export function lastResultLatencyText(check: CheckItem): string {
  return formatLatency(check.last_result?.latency_ms ?? null)
}

/* ------------------------------------------------------------------ */
/* 分页（纯前端）                                                      */
/* ------------------------------------------------------------------ */

export function pageCount(total: number, size: number = CHECKS_PAGE_SIZE): number {
  if (total <= 0) return 1
  return Math.max(1, Math.ceil(total / size))
}

/** 页码夹在 [1, pageCount] 内：筛选变化后残留的大页码不会渲染出空白页。 */
export function clampPage(page: number, total: number, size: number = CHECKS_PAGE_SIZE): number {
  const pages = pageCount(total, size)
  if (!Number.isFinite(page)) return 1
  return Math.min(pages, Math.max(1, Math.trunc(page)))
}

export function paginate<T>(items: readonly T[], page: number, size: number = CHECKS_PAGE_SIZE): T[] {
  const current = clampPage(page, items.length, size)
  const start = (current - 1) * size
  return items.slice(start, start + size)
}

/* ------------------------------------------------------------------ */
/* 详情抽屉：目标服务器 + 最近一次结果                                  */
/* ------------------------------------------------------------------ */

export interface CheckTargetRow {
  serverId: string
  /** 后端结果里的 server_name 优先，其次调用方注入的名称，最后回落 id */
  serverName: string
  /** 该服务器最近一次结果；null = 还没有结果 */
  result: CheckResult | null
}

/** 结果时间线（按 ts 倒序返回）→ 每台服务器最近的一条。 */
export function latestResultByServer(results: readonly CheckResult[]): Map<string, CheckResult> {
  const latest = new Map<string, CheckResult>()
  for (const result of results) {
    if (!result.server_id) continue
    const previous = latest.get(result.server_id)
    if (!previous || (result.ts ?? 0) >= (previous.ts ?? 0)) latest.set(result.server_id, result)
  }
  return latest
}

/**
 * 目标服务器列表：以 check.server_ids 为准（后端 ListCheckTargets 的顺序），
 * 老后端没有 server_ids 时用结果里的 server_id 兜底，保证抽屉不空着。
 */
export function checkTargetRows(
  check: CheckItem,
  results: readonly CheckResult[],
  serverNameOf: (serverId: string) => string = (serverId) => serverId,
): CheckTargetRow[] {
  const latest = latestResultByServer(results)
  const ids = check.server_ids.length > 0 ? check.server_ids : [...latest.keys()]
  return ids.map((serverId) => {
    const result = latest.get(serverId) ?? null
    const fromResult = result?.server_name?.trim() ?? ''
    return {
      serverId,
      serverName: fromResult || serverNameOf(serverId),
      result,
    }
  })
}

/** 立即执行的汇总文案：成功 N 台 · 失败 M 台。 */
export function runSummaryText(response: RunCheckResponse): string {
  return t('checks.runSummary', { ok: response.summary.ok, failed: response.summary.failed })
}

/** 逐台执行结果 → 一行文案（成功/失败 + 延迟 + 原因）。 */
export function runItemText(item: RunCheckResponse['results'][number]): string {
  const state = item.ok ? t('s.0300') : t('s.0301')
  const latency = formatLatency(item.latency_ms)
  const reason = item.message?.trim() ? ` · ${item.message.trim()}` : ''
  return `${state} · ${latency}${reason}`
}
