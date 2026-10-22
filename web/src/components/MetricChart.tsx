import { useEffect, useMemo, useRef } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { MetricSeries } from '../lib/api'
import { metricLabel } from '../lib/format'
import { useTheme } from '../lib/theme'
import { t } from '../i18n'

interface MetricChartProps {
  /** 每条 series 画一条曲线；空数据时渲染占位提示。 */
  series: MetricSeries[]
  height?: number
  formatValue?: (value: number) => string
  emptyHint?: string
}

const COLORS = ['#16a34a', '#0ea5e9', '#f59e0b', '#ef4444', '#8b5cf6', '#14b8a6']

/** uPlot 需要对齐的 [x, y1, y2...] 数组，缺失点补 null。 */
function buildAlignedData(series: MetricSeries[]): uPlot.AlignedData {
  const timestamps = new Set<number>()
  for (const item of series) {
    for (const point of item.points) timestamps.add(point.ts)
  }
  const xs = [...timestamps].sort((a, b) => a - b)
  const lookups = series.map((item) => {
    const map = new Map<number, number>()
    for (const point of item.points) map.set(point.ts, point.value)
    return map
  })
  const ys = lookups.map((lookup) =>
    xs.map((ts) => {
      const value = lookup.get(ts)
      return value === undefined ? null : value
    }),
  )
  return [xs.map((ts) => ts / 1000), ...ys] as uPlot.AlignedData
}

export function MetricChart({
  series,
  height = 200,
  formatValue = (value) => value.toFixed(2),
  emptyHint = t('s.0426'),
}: MetricChartProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const formatRef = useRef(formatValue)
  formatRef.current = formatValue
  const { theme } = useTheme()

  const hasData = series.some((item) => item.points.length > 0)
  const aligned = useMemo(() => (hasData ? buildAlignedData(series) : null), [series, hasData])

  useEffect(() => {
    const container = containerRef.current
    if (!container || !aligned) return

    const dark = theme === 'dark'
    const axisColor = dark ? '#94a3b8' : '#64748b'
    const gridColor = dark ? 'rgba(148,163,184,0.16)' : 'rgba(100,116,139,0.14)'
    const width = Math.max(container.clientWidth, 240)

    const options: uPlot.Options = {
      width,
      height,
      padding: [10, 18, 0, 4],
      cursor: { y: false, points: { size: 6 } },
      legend: { show: series.length > 1, live: true },
      scales: {
        x: { time: true },
        y: { auto: true },
      },
      axes: [
        {
          stroke: axisColor,
          grid: { stroke: gridColor, width: 1 },
          ticks: { stroke: gridColor, width: 1 },
          font: '11px system-ui, sans-serif',
          size: 46,
          values: (_u, splits) => {
            const xs = (_u.data[0] ?? []) as number[]
            const span = xs.length > 1 ? (xs[xs.length - 1] ?? 0) - (xs[0] ?? 0) : 0
            const withDate = span >= 86_400
            return splits.map((ts) => {
              const date = new Date(ts * 1000)
              const hh = String(date.getHours()).padStart(2, '0')
              const mm = String(date.getMinutes()).padStart(2, '0')
              if (!withDate) return `${hh}:${mm}`
              const month = String(date.getMonth() + 1).padStart(2, '0')
              const day = String(date.getDate()).padStart(2, '0')
              return `${month}/${day} ${hh}:${mm}`
            })
          },
        },
        {
          stroke: axisColor,
          grid: { stroke: gridColor, width: 1 },
          ticks: { stroke: gridColor, width: 1 },
          font: '11px system-ui, sans-serif',
          size: 56,
          values: (_u, values) => values.map((value) => formatRef.current(value)),
        },
      ],
      series: [
        {},
        ...series.map((item, index) => ({
          label: metricLabel(item.metric),
          stroke: COLORS[index % COLORS.length],
          width: 1.6,
          points: { show: false },
          value: (_u: uPlot, value: number | null) =>
            value === null || value === undefined ? '—' : formatRef.current(value),
        })),
      ],
    }

    const chart = new uPlot(options, aligned, container)
    const observer = new ResizeObserver(() => {
      const nextWidth = container.clientWidth
      if (nextWidth > 0) chart.setSize({ width: nextWidth, height })
    })
    observer.observe(container)

    return () => {
      observer.disconnect()
      chart.destroy()
    }
  }, [aligned, series, theme, height])

  if (!hasData) {
    return (
      <div
        className="flex items-center justify-center rounded-lg border border-dashed border-slate-200 text-xs text-slate-400 dark:border-slate-800 dark:text-slate-500"
        style={{ height }}
      >
        {emptyHint}
      </div>
    )
  }

  return <div ref={containerRef} className="w-full" style={{ height }} />
}
