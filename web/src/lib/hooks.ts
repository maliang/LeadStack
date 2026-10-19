import { useCallback, useEffect, useRef, useState } from 'react'

export interface PollingQuery<T> {
  data: T | null
  error: string | null
  /** 首次加载中（后续轮询不会把页面切回 loading）。 */
  loading: boolean
  /** 最近一次成功刷新的时间戳（毫秒）。 */
  updatedAt: number | null
  refresh: () => Promise<void>
}

/**
 * 轻量轮询 hook（不引额外数据请求库）。
 * 默认 10 秒一次；页面不可见时跳过轮询，回到前台后的下一个 tick 自动补上。
 * `key` 变化（例如筛选条件改变）时立即重新拉取。
 */
export function usePolling<T>(
  fetcher: () => Promise<T>,
  intervalMs: number = 10_000,
  enabled: boolean = true,
  key: string = '',
): PollingQuery<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState<boolean>(enabled)
  const [updatedAt, setUpdatedAt] = useState<number | null>(null)

  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher

  const inFlightRef = useRef(false)
  const mountedRef = useRef(true)

  const run = useCallback(async () => {
    if (inFlightRef.current) return
    inFlightRef.current = true
    try {
      const result = await fetcherRef.current()
      if (!mountedRef.current) return
      setData(result)
      setError(null)
      setUpdatedAt(Date.now())
    } catch (caught) {
      if (!mountedRef.current) return
      setError(caught instanceof Error ? caught.message : '加载失败，请稍后重试')
    } finally {
      inFlightRef.current = false
      if (mountedRef.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    if (!enabled) {
      setLoading(false)
      return
    }
    setLoading(true)
    void run()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void run()
    }, intervalMs)
    return () => window.clearInterval(timer)
  }, [enabled, intervalMs, run, key])

  return { data, error, loading, updatedAt, refresh: run }
}

/** 每秒推进一次的「当前时间」，用于刷新相对时间文案。 */
export function useNow(intervalMs: number = 1_000): number {
  const [now, setNow] = useState<number>(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(timer)
  }, [intervalMs])
  return now
}
