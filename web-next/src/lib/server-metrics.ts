import type { MetricPoint, MetricSeries, MetricsStep } from './api'
import { formatBytes, formatLoad, formatPercent, metricLabel } from './format'

/**
 * 服务器详情「指标曲线 / Agent 升级」面板的纯函数。
 *
 * 只做换算与格式化，不碰 DOM、不发请求：
 * - 时间窗（1h / 6h / 24h）→ `from/to` 毫秒，喂给 `api.metrics(id, {from,to,step})`；
 * - series → SVG polyline 点串（x 按时间窗线性映射、y 按 min/max 归一）；
 * - 指标单位 → 展示文案（百分比 / 字节 / 负载 / 数字）；
 * - agent 升级表单的 sha256 与必填校验。
 *
 * 组件只负责把这里返回的字符串贴进模板，口径只有一处。
 */

/** 指标时间窗（1h / 6h / 24h）。 */
export type MetricsWindow = '1h' | '6h' | '24h'

export interface MetricsWindowOption {
  key: MetricsWindow
  labelKey: string
}

/** 时间窗选项：顺序即按钮顺序。 */
export const METRICS_WINDOWS: readonly MetricsWindowOption[] = [
  { key: '1h', labelKey: 'servers.detail.metrics.window1h' },
  { key: '6h', labelKey: 'servers.detail.metrics.window6h' },
  { key: '24h', labelKey: 'servers.detail.metrics.window24h' },
]

export interface MetricsStepOption {
  key: MetricsStep
  labelKey: string
}

/** 步长选项：raw / 1m / 1h，与 `MetricsStep` 一一对应。 */
export const METRICS_STEPS: readonly MetricsStepOption[] = [
  { key: 'raw', labelKey: 'servers.detail.metrics.stepRaw' },
  { key: '1m', labelKey: 'servers.detail.metrics.step1m' },
  { key: '1h', labelKey: 'servers.detail.metrics.step1h' },
]

const HOUR_MS = 3_600_000

/** 时间窗毫秒数。 */
export function metricsWindowMs(window: MetricsWindow): number {
  switch (window) {
    case '6h':
      return 6 * HOUR_MS
    case '24h':
      return 24 * HOUR_MS
    default:
      return HOUR_MS
  }
}

/**
 * 时间窗 → 查询区间（毫秒）。`to` 取「此刻」，`from` 按窗口回推。
 * 后端容忍客户端时钟偏差（超过 now 会被夹回），这里不做截断以免丢掉最后一分钟。
 */
export function metricsRange(
  window: MetricsWindow,
  now: number = Date.now(),
): { from: number; to: number } {
  const to = Math.floor(now)
  return { from: to - metricsWindowMs(window), to }
}

/* ---------------------------------------------------------------- */
/* SVG 几何                                                          */
/* ---------------------------------------------------------------- */

/** 图表 viewBox 宽度；高度按每条曲线固定，`preserveAspectRatio="none"` 铺满容器。 */
export const METRICS_CHART_WIDTH = 600
export const METRICS_CHART_HEIGHT = 160

/** SVG 绘图区内边距：左侧留给 Y 轴数值，底部留给日期。 */
export const METRICS_CHART_PADDING = { left: 46, right: 10, top: 12, bottom: 28 }

export interface ValueBounds {
  min: number
  max: number
}

/** 取一组点的值域；空集或全是非法值返回 null，单值/等值时上下各留一点余量。 */
export function valueBounds(points: readonly MetricPoint[]): ValueBounds | null {
  let min = Number.POSITIVE_INFINITY
  let max = Number.NEGATIVE_INFINITY
  for (const point of points) {
    if (!Number.isFinite(point.ts) || !Number.isFinite(point.value)) continue
    if (point.value < min) min = point.value
    if (point.value > max) max = point.value
  }
  if (!Number.isFinite(min) || !Number.isFinite(max)) return null
  if (min === max) {
    const pad = Math.abs(min) * 0.05 || 1
    return { min: min - pad, max: max + pad }
  }
  return { min, max }
}

function round2(value: number): number {
  return Math.round(value * 100) / 100
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value))
}

/**
 * 单点投影：x = (ts - from) / (to - from) * width，y = height - 归一化比例 * height。
 * 时间窗之外的 ts 会被 x 夹到边界（调用方通常已先过滤），保证不给 SVG 出 NaN。
 */
export function projectPoint(
  ts: number,
  value: number,
  from: number,
  to: number,
  bounds: ValueBounds,
  width: number = METRICS_CHART_WIDTH,
  height: number = METRICS_CHART_HEIGHT,
): { x: number; y: number } {
  const span = to - from
  const ratio = span > 0 ? (ts - from) / span : 1
  const spanY = bounds.max - bounds.min
  const ratioY = spanY > 0 ? (value - bounds.min) / spanY : 0.5
  const plotWidth = Math.max(1, width - METRICS_CHART_PADDING.left - METRICS_CHART_PADDING.right)
  const plotHeight = Math.max(1, height - METRICS_CHART_PADDING.top - METRICS_CHART_PADDING.bottom)
  return {
    x: round2(METRICS_CHART_PADDING.left + clamp(ratio, 0, 1) * plotWidth),
    y: round2(METRICS_CHART_PADDING.top + (1 - clamp(ratioY, 0, 1)) * plotHeight),
  }
}

/** 平滑折线：Catmull-Rom 转三次贝塞尔，避免长时间窗里出现硬折角。 */
function smoothPath(points: readonly { x: number; y: number }[]): string {
  if (points.length === 0) return ''
  if (points.length === 1) return `M${points[0].x},${points[0].y}`
  let path = `M${points[0].x},${points[0].y}`
  for (let index = 0; index < points.length - 1; index += 1) {
    const p0 = points[index - 1] ?? points[index]
    const p1 = points[index]
    const p2 = points[index + 1]
    const p3 = points[index + 2] ?? p2
    const cp1x = p1.x + (p2.x - p0.x) / 6
    const cp1y = p1.y + (p2.y - p0.y) / 6
    const cp2x = p2.x - (p3.x - p1.x) / 6
    const cp2y = p2.y - (p3.y - p1.y) / 6
    path += ` C${round2(cp1x)},${round2(cp1y)} ${round2(cp2x)},${round2(cp2y)} ${p2.x},${p2.y}`
  }
  return path
}

function formatAxisDate(ts: number): string {
  const date = new Date(ts)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** 单条曲线最多画的点数：raw 窗口内的点数可达上万，等步长抽稀后仍保留尾端。 */
export const METRICS_MAX_POINTS = 600

/**
 * 抽稀：等步长取样，但始终补上最后一个点（曲线尾端必须对应当前值）。
 * 点数不超过上限时原样返回（同一数组的浅拷贝）。
 */
export function samplePoints(
  points: readonly MetricPoint[],
  limit: number = METRICS_MAX_POINTS,
): MetricPoint[] {
  if (limit <= 0) return points.slice()
  if (points.length <= limit) return points.slice()
  const stride = Math.ceil(points.length / limit)
  const sampled: MetricPoint[] = []
  for (let index = 0; index < points.length; index += stride) sampled.push(points[index])
  const last = points[points.length - 1]
  if (sampled[sampled.length - 1] !== last) sampled.push(last)
  return sampled
}

/** series 的点串（`"x,y x,y …"`），按 ts 升序；空数组返回空串。 */
export function svgPointString(
  points: readonly MetricPoint[],
  from: number,
  to: number,
  bounds: ValueBounds,
  width: number = METRICS_CHART_WIDTH,
  height: number = METRICS_CHART_HEIGHT,
): string {
  return points
    .map((point) => {
      const projected = projectPoint(point.ts, point.value, from, to, bounds, width, height)
      return `${projected.x},${projected.y}`
    })
    .join(' ')
}

/* ---------------------------------------------------------------- */
/* 单位与文案                                                        */
/* ---------------------------------------------------------------- */

export type MetricUnit = 'percent' | 'bytes' | 'load' | 'number'

/** 指标 key → 展示单位（契约 3.1 的点分命名，见 pkg/protocol/protocol.go）。 */
export function isNetworkRateMetric(metric: string): boolean {
  return metric === 'net.rx_bytes' || metric === 'net.tx_bytes'
}

export function metricUnit(metric: string): MetricUnit {
  if (metric === 'mem.used_bytes' || metric.endsWith('_bytes')) return 'bytes'
  if (metric === 'load.1' || metric.startsWith('load.')) return 'load'
  if (metric.endsWith('_pct') || metric.endsWith('.pct')) return 'percent'
  return 'number'
}

/** 数值 → 人话（用于「最新值」，数字与单位由 format 层拼好）。 */
export function formatMetricValue(metric: string, value: number): string {
  if (!Number.isFinite(value)) return '—'
  const suffix = isNetworkRateMetric(metric) ? '/s' : ''
  switch (metricUnit(metric)) {
    case 'percent':
      return formatPercent(value, 1)
    case 'bytes':
      return `${formatBytes(value, 1)}${suffix}`
    case 'load':
      return formatLoad(value)
    default:
      return `${Number.isInteger(value) ? String(value) : value.toFixed(2)}${suffix}`
  }
}

/** 数值 → 坐标轴短标签（少一位小数，避免 320px 下把标签挤爆）。 */
export function formatMetricAxisValue(metric: string, value: number): string {
  if (!Number.isFinite(value)) return '—'
  const suffix = isNetworkRateMetric(metric) ? '/s' : ''
  switch (metricUnit(metric)) {
    case 'percent':
      return formatPercent(value, 0)
    case 'bytes':
      return `${formatBytes(value, 0)}${suffix}`
    case 'load':
      return value.toFixed(1)
    default:
      return `${Number.isInteger(value) ? String(value) : value.toFixed(1)}${suffix}`
  }
}

/* ---------------------------------------------------------------- */
/* 每个 series → 一条曲线的渲染数据                                   */
/* ---------------------------------------------------------------- */

export interface MetricChart {
  /** 后端指标 key，如 cpu.pct；同时作为 v-for 的 key。 */
  metric: string
  /** 已本地化的指标名（format.metricLabel）。 */
  label: string
  /** 窗口内、按 ts 升序、已抽稀的点（≤ METRICS_MAX_POINTS）。 */
  points: MetricPoint[]
  /** polyline 的 points 属性；不足 2 点时为空串（由端点圆点兜底）。 */
  line: string
  /** 平滑曲线 path；不足 2 点时为空串。 */
  path: string
  /** 曲线下方面积 path；不足 2 点时为空串。 */
  area: string
  lastX: number
  lastY: number
  /** 最新值文案（含单位）。 */
  latest: string
  /** y 轴下/上限短标签。 */
  minLabel: string
  maxLabel: string
  /** 该曲线的值域，模板里不用再算。 */
  bounds: ValueBounds
  /** 每个点投影后的坐标，供 SVG hover 点显示时间/数值。 */
  markers: { x: number; y: number; ts: number; value: number }[]
  /** Y 轴刻度（含网格线位置）。 */
  yTicks: { y: number; label: string }[]
  /** X 轴日期刻度。 */
  xTicks: { x: number; label: string }[]
}

/**
 * `MetricsResponse.series` + 查询区间 → 曲线数组。
 *
 * - 丢弃窗口外与非法点（后端可能返回窗口边缘的采样）；
 * - 丢弃空 series（全被过滤时就是「暂无指标数据」）；
 * - 点数超过 METRICS_MAX_POINTS 时等步长抽稀（避免 SVG 属性串上万点）；
 * - 标签用 format.metricLabel，未知指标原样显示 key。
 */
export function buildMetricCharts(
  series: readonly MetricSeries[],
  from: number,
  to: number,
  width: number = METRICS_CHART_WIDTH,
  height: number = METRICS_CHART_HEIGHT,
): MetricChart[] {
  const charts: MetricChart[] = []
  for (const item of series) {
    const rawPoints = item.points
      .filter(
        (point) =>
          Number.isFinite(point.ts) &&
          Number.isFinite(point.value) &&
          point.ts >= from &&
          point.ts <= to,
      )
      .slice()
      .sort((a, b) => a.ts - b.ts)
    // 网络接收/发送在采集侧是累计字节计数器；图表显示每秒速率，而不是开机以来的总量。
    let points = rawPoints
    if (isNetworkRateMetric(item.metric) && rawPoints.length > 1) {
      points = rawPoints
        .slice(1)
        .map((point, index) => {
          const prev = rawPoints[index]
          const seconds = (point.ts - prev.ts) / 1000
          return {
            ts: point.ts,
            value: seconds > 0 ? Math.max(0, (point.value - prev.value) / seconds) : 0,
          }
        })
    }
    const bounds = valueBounds(points)
    if (!bounds) continue
    const plotted = samplePoints(points)
    const last = plotted[plotted.length - 1]
    const projected = projectPoint(last.ts, last.value, from, to, bounds, width, height)
    const markers = plotted.map((point) => {
      const pos = projectPoint(point.ts, point.value, from, to, bounds, width, height)
      return { x: pos.x, y: pos.y, ts: point.ts, value: point.value }
    })
    const path = markers.length >= 2 ? smoothPath(markers) : ''
    const plotBottom = height - METRICS_CHART_PADDING.bottom
    const area = path
      ? `${path} L${markers[markers.length - 1].x},${plotBottom} L${markers[0].x},${plotBottom} Z`
      : ''
    const yTicks = Array.from({ length: 5 }, (_, index) => {
      const ratio = index / 4
      const value = bounds.max - (bounds.max - bounds.min) * ratio
      return {
        y: round2(METRICS_CHART_PADDING.top + ratio * (height - METRICS_CHART_PADDING.top - METRICS_CHART_PADDING.bottom)),
        label: formatMetricAxisValue(item.metric, value),
      }
    })
    const plotWidth = width - METRICS_CHART_PADDING.left - METRICS_CHART_PADDING.right
    const xTicks = [0, 0.5, 1].map((ratio) => ({
      x: round2(METRICS_CHART_PADDING.left + plotWidth * ratio),
      label: formatAxisDate(from + (to - from) * ratio),
    }))
    charts.push({
      metric: item.metric,
      label: metricLabel(item.metric),
      points: plotted,
      line: plotted.length >= 2 ? svgPointString(plotted, from, to, bounds, width, height) : '',
      path,
      area,
      lastX: projected.x,
      lastY: projected.y,
      latest: formatMetricValue(item.metric, last.value),
      minLabel: formatMetricAxisValue(item.metric, bounds.min),
      maxLabel: formatMetricAxisValue(item.metric, bounds.max),
      bounds,
      markers,
      yTicks,
      xTicks,
    })
  }
  return charts
}

/* ---------------------------------------------------------------- */
/* Agent 升级表单校验                                                */
/* ---------------------------------------------------------------- */

const SHA256_HEX = /^[0-9a-fA-F]{64}$/

/** sha256 必须是 64 位十六进制（后端强校验，前端先挡一次不发请求）。 */
export function isSha256Hex(value: string): boolean {
  return SHA256_HEX.test(value.trim())
}

export type AgentUpgradeFieldError = 'missingVersion' | 'missingUrl' | 'invalidSha256' | null

export interface AgentUpgradeDraft {
  version: string
  url: string
  sha256: string
}

/** 三字段表单的首个错误；null 表示可以提交。 */
export function agentUpgradeFieldError(draft: AgentUpgradeDraft): AgentUpgradeFieldError {
  if (draft.version.trim() === '') return 'missingVersion'
  if (draft.url.trim() === '') return 'missingUrl'
  if (!isSha256Hex(draft.sha256)) return 'invalidSha256'
  return null
}

/** 去掉首尾空白、sha256 统一小写，再交给 api.agentUpgrade。 */
export function normalizeAgentUpgradeDraft(draft: AgentUpgradeDraft): AgentUpgradeDraft {
  return {
    version: draft.version.trim(),
    url: draft.url.trim(),
    sha256: draft.sha256.trim().toLowerCase(),
  }
}
