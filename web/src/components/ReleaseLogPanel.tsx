import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type ReleaseLogEntry, type ReleaseRun } from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { formatClockTime } from '../lib/format'
import { Button } from './Button'
import { ReleaseStatusBadge } from './DeployBadges'
import { SpinnerIcon } from './Icons'

interface ReleaseLogPanelProps {
  releaseId: string
  runs: ReleaseRun[]
  /** 仍有可能产生新日志时保持轮询；false 时仅首次加载（仍可手动刷新） */
  streaming: boolean
  /** 受控选中（由详情页表格行点击切换）；不传则内部自行维护 */
  activeServerId?: string
  onSelectServer?: (serverId: string) => void
}

const POLL_INTERVAL_MS = 2_500
const MAX_RENDERED = 4_000

/**
 * 发布实时日志面板：
 * 用 `?after_seq=` 增量拉取并追加（不整段重拉），自动滚到底部，可切换服务器。
 */
export function ReleaseLogPanel({
  releaseId,
  runs,
  streaming,
  activeServerId,
  onSelectServer,
}: ReleaseLogPanelProps) {
  const [internalServerId, setInternalServerId] = useState('')
  const selectedServerId = activeServerId ?? internalServerId
  const setSelectedServerId = (next: string) => {
    setInternalServerId(next)
    onSelectServer?.(next)
  }
  const [logs, setLogs] = useState<ReleaseLogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [autoScroll, setAutoScroll] = useState(true)
  const [newCount, setNewCount] = useState(0)

  const lastSeqRef = useRef(0)
  const seenRef = useRef<Set<number>>(new Set())
  const scrollRef = useRef<HTMLDivElement | null>(null)

  // 默认选中：优先 running，其次 failed，最后第一个
  useEffect(() => {
    if (activeServerId !== undefined) return
    if (runs.length === 0) {
      setInternalServerId('')
      return
    }
    setInternalServerId((current) => {
      if (current && runs.some((run) => run.server_id === current)) return current
      const running = runs.find((run) => run.status === 'running' || run.status === 'pending')
      const failed = runs.find((run) => run.status === 'failed')
      return (running ?? failed ?? runs[0]).server_id
    })
  }, [runs, activeServerId])

  useEffect(() => {
    if (!releaseId || !selectedServerId) return
    let cancelled = false
    lastSeqRef.current = 0
    seenRef.current = new Set()
    setLogs([])
    setNewCount(0)
    setLoading(true)
    setError(null)

    const pull = async () => {
      try {
        const entries = await api.releaseLogs(releaseId, selectedServerId, lastSeqRef.current)
        if (cancelled) return
        const fresh = entries.filter((entry) => !seenRef.current.has(entry.seq))
        if (fresh.length > 0) {
          for (const entry of fresh) seenRef.current.add(entry.seq)
          lastSeqRef.current = Math.max(
            lastSeqRef.current,
            ...fresh.map((entry) => entry.seq),
          )
          setLogs((current) => {
            const next = [...current, ...fresh]
            return next.length > MAX_RENDERED ? next.slice(next.length - MAX_RENDERED) : next
          })
          setNewCount((count) => count + fresh.length)
        }
        setError(null)
      } catch (caught) {
        if (!cancelled) setError(errorMessage(caught, '日志拉取失败'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void pull()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void pull()
    }, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [releaseId, selectedServerId])

  // 自动滚到底部
  useEffect(() => {
    if (!autoScroll) return
    const container = scrollRef.current
    if (!container) return
    container.scrollTop = container.scrollHeight
  }, [logs, autoScroll])

  const selectedRun = useMemo(
    () => runs.find((run) => run.server_id === selectedServerId) ?? null,
    [runs, selectedServerId],
  )

  const handleCopy = async () => {
    const text = logs
      .map((entry) => `[${entry.seq}] ${entry.stream === 'stderr' ? '[stderr] ' : ''}${entry.chunk}`)
      .join('')
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      /* 剪贴板不可用时忽略 */
    }
  }

  const handleScroll = () => {
    const container = scrollRef.current
    if (!container) return
    const atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 24
    if (!atBottom && autoScroll) setAutoScroll(false)
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        {runs.map((run) => (
          <button
            key={run.server_id}
            type="button"
            onClick={() => setSelectedServerId(run.server_id)}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
              run.server_id === selectedServerId
                ? 'border-indigo-400 bg-indigo-50 text-indigo-700 dark:border-indigo-500/50 dark:bg-indigo-500/10 dark:text-indigo-200'
                : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
            )}
          >
            <span className="max-w-[160px] truncate">{run.server_name ?? run.server_id}</span>
            <ReleaseStatusBadge status={run.status} className="px-1.5 py-0 text-[10px]" />
          </button>
        ))}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-slate-200 bg-slate-50 px-2.5 py-1.5 dark:border-slate-800 dark:bg-slate-950/60">
        <div className="flex items-center gap-3 text-xs text-slate-500 dark:text-slate-400">
          <span className="inline-flex items-center gap-1.5">
            {streaming ? (
              <>
                <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-blue-500" />
                实时追加中（增量 after_seq，2.5s 一次）
              </>
            ) : (
              '日志流已结束（可手动刷新）'
            )}
          </span>
          <span className="tabular-nums">共 {logs.length} 条 · 最新 seq {lastSeqRef.current}</span>
          {selectedRun?.exit_code !== null && selectedRun?.exit_code !== undefined ? (
            <span className={cn('tabular-nums', selectedRun.exit_code === 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400')}>
              退出码 {selectedRun.exit_code}
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <label className="inline-flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400">
            <input
              type="checkbox"
              checked={autoScroll}
              onChange={(event) => {
                setAutoScroll(event.target.checked)
                setNewCount(0)
              }}
              className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
            />
            自动滚动
          </label>
          <Button size="sm" variant="ghost" onClick={() => void handleCopy()} disabled={logs.length === 0}>
            复制日志
          </Button>
        </div>
      </div>

      {!autoScroll && newCount > 0 ? (
        <button
          type="button"
          onClick={() => {
            setAutoScroll(true)
            setNewCount(0)
          }}
          className="w-full rounded-md border border-indigo-200 bg-indigo-50 px-3 py-1.5 text-xs font-medium text-indigo-700 hover:bg-indigo-100 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200"
        >
          有 {newCount} 条新日志，点击回到底部
        </button>
      ) : null}

      {error ? (
        <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          日志拉取失败：{error}
        </p>
      ) : null}

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="h-72 overflow-auto rounded-md border border-slate-200 bg-slate-950 p-3 font-mono text-[11px] leading-relaxed text-slate-100 dark:border-slate-800"
      >
        {loading && logs.length === 0 ? (
          <p className="flex items-center gap-2 text-slate-400">
            <SpinnerIcon className="h-3.5 w-3.5" />
            正在拉取日志…
          </p>
        ) : logs.length === 0 ? (
          <p className="text-slate-500">
            {selectedRun
              ? `该服务器（${selectedRun.server_name ?? selectedRun.server_id}）暂无日志；状态：${selectedRun.status}`
              : '请选择一台服务器查看日志'}
          </p>
        ) : (
          logs.map((entry) => (
            <div key={entry.seq} className="whitespace-pre-wrap break-words">
              <span className="mr-2 select-none text-slate-600">
                {formatClockTime(entry.ts)} [{entry.seq}]
              </span>
              <span className={entry.stream === 'stderr' ? 'text-rose-300' : 'text-slate-100'}>
                {entry.chunk}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
