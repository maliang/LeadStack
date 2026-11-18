import { t } from '../i18n'
import type { AlertItem, AlertSeverity, AlertStatus } from './api'
import { formatDateTime, formatRelativeTime } from './format'

/**
 * 告警列表页（AlertsPage）的纯函数与常量：筛选、分页、级别/状态映射只此一处。
 *
 * 组件只渲染，页面只持筛选/分页/抽屉三个 UI 状态。
 * 筛选是纯前端计算：GET /alerts 一次取回（limit=200），不新增后端查询参数，
 * 与服务器页 / 检测页保持同一套交互语义。
 */

/** 每页条数（纯前端分页，避免一次渲染两百行）。 */
export const ALERTS_PAGE_SIZE = 20

/** 单次拉取上限：与旧版 web/src/pages/AlertsPage.tsx 的 limit=200 一致。 */
export const ALERTS_FETCH_LIMIT = 200

/** 服务器筛选里表示「规则级告警（server_id 为空）」的特殊值。 */
export const RULE_LEVEL_FILTER = 'rule'

/** 状态筛选的固定顺序。 */
export const ALERT_STATUSES: readonly AlertStatus[] = ['firing', 'acked', 'resolved']

/** 级别筛选的固定顺序（严重 → 提示）。 */
export const ALERT_SEVERITIES: readonly AlertSeverity[] = ['critical', 'warning', 'info']

export interface AlertFilterState {
  /** 空串 = 全部状态 */
  status: '' | AlertStatus
  /** 空串 = 全部级别 */
  severity: '' | AlertSeverity
  /** 空串 = 全部服务器；RULE_LEVEL_FILTER = 只看规则级；否则为 server_id */
  server: string
}

export function emptyAlertFilters(): AlertFilterState {
  return { status: '', severity: '', server: '' }
}

/** 级别 → i18n key（critical/warning/info 三档）。 */
export function severityLabelKey(severity: AlertSeverity | string): string {
  if (severity === 'critical') return 'alert.severity.critical'
  if (severity === 'warning') return 'alert.severity.warning'
  if (severity === 'info') return 'alert.severity.info'
  return 'alerts.unknown'
}

/** 级别 → 色调（严重=危险，警告=警示，提示=中性）。 */
export function severityTone(severity: AlertSeverity | string): string {
  if (severity === 'critical') return 'danger'
  if (severity === 'warning') return 'warning'
  return 'muted'
}

/** 状态 → i18n key。 */
export function statusLabelKey(status: AlertStatus | string): string {
  if (status === 'firing') return 'alert.status.firing'
  if (status === 'acked') return 'alert.status.acked'
  if (status === 'resolved') return 'alert.status.resolved'
  return 'alerts.unknown'
}

/** 状态 → 色调：触发中=危险，已确认=警示，已恢复=成功。 */
export function statusTone(status: AlertStatus | string): string {
  if (status === 'firing') return 'danger'
  if (status === 'acked') return 'warning'
  if (status === 'resolved') return 'success'
  return 'muted'
}

/**
 * 规则级告警：没有绑定具体服务器（server_id 为空）。
 * 这类告警的「服务器」列必须显示「规则级」，不能因为取不到名称而空白或报错。
 */
export function isRuleLevel(alert: AlertItem): boolean {
  return !alert.server_id || alert.server_id.trim() === ''
}

/** 服务器展示文案：优先后端带出的 server_name，其次 id；规则级落到「规则级」。 */
export function serverText(alert: AlertItem): string {
  if (isRuleLevel(alert)) return t('alerts.ruleLevel')
  const name = (alert.server_name ?? '').trim()
  return name !== '' ? name : String(alert.server_id)
}

/** 规则展示文案：规则名缺失时退回 rule_id，避免整列空白。 */
export function ruleText(alert: AlertItem): string {
  const name = (alert.rule_name ?? '').trim()
  if (name !== '') return name
  return alert.rule_id || '—'
}

/** 服务器筛选项：只从告警数据里收集（不额外请求 /servers，alert.read 即可用）。 */
export function collectServerOptions(alerts: readonly AlertItem[]): { id: string; label: string }[] {
  const seen = new Map<string, string>()
  for (const alert of alerts) {
    if (isRuleLevel(alert)) continue
    const id = String(alert.server_id)
    if (seen.has(id)) continue
    seen.set(id, serverText(alert))
  }
  return [...seen.entries()]
    .map(([id, label]) => ({ id, label }))
    .sort((left, right) => left.label.localeCompare(right.label))
}

/** 筛选语义：状态 / 级别 / 服务器三者 AND；服务器支持「只看规则级」。 */
export function filterAlerts(
  alerts: readonly AlertItem[],
  filters: AlertFilterState,
): AlertItem[] {
  return alerts.filter((alert) => {
    if (filters.status && alert.status !== filters.status) return false
    if (filters.severity && alert.severity !== filters.severity) return false
    if (filters.server === RULE_LEVEL_FILTER) return isRuleLevel(alert)
    if (filters.server !== '' && alert.server_id !== filters.server) return false
    return true
  })
}

export function hasActiveAlertFilters(filters: AlertFilterState): boolean {
  return Boolean(filters.status || filters.severity || filters.server)
}

/** 总页数；0 条时仍返回 1，避免「第 0 页」这种非法页码。 */
export function pageCount(total: number, pageSize: number = ALERTS_PAGE_SIZE): number {
  if (pageSize <= 0) return 1
  return Math.max(1, Math.ceil(total / pageSize))
}

/** 页码夹取到 [1, pages]，筛选后条目变少时不会停在一个空页上。 */
export function clampPage(page: number, total: number, pageSize: number = ALERTS_PAGE_SIZE): number {
  return Math.min(Math.max(1, Math.trunc(page) || 1), pageCount(total, pageSize))
}

/** 取当前页切片。 */
export function paginate<T>(
  items: readonly T[],
  page: number,
  pageSize: number = ALERTS_PAGE_SIZE,
): T[] {
  const safe = clampPage(page, items.length, pageSize)
  return items.slice((safe - 1) * pageSize, safe * pageSize)
}

/**
 * 时间展示对：本地时区绝对时间 + 相对时间。
 * 绝对时间用于定位「几点发生的」，相对时间用于快速判断「多久前」；两者都为空时显示「—」。
 */
export function alertTime(
  ts: number | null | undefined,
  now: number = Date.now(),
): { absolute: string; relative: string } {
  return {
    absolute: formatDateTime(ts),
    relative: formatRelativeTime(ts, now),
  }
}

/**
 * 事件类型文案：已知类型走 i18n，未知类型原样展示（不把 key 泄漏到界面上）。
 */
export function eventKindText(kind: string): string {
  const raw = (kind ?? '').trim()
  if (raw === '') return t('alerts.unknown')
  const key = `alert.event.${raw}`
  const text = t(key)
  return text === key ? raw : text
}
