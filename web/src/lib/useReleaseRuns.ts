import { useEffect, useState } from 'react'
import { api, type ReleaseRun } from './api'

/**
 * 批量拉取若干发布的 runs。
 *
 * 契约里 `GET /releases`（列表）只返回 release 本体，不带宽带了 runs 的明细，
 * 而列表页需要每台的进度/服务器数；因此这里对需要的 release id 并发调用详情接口，
 * 结果缓存在内存里并按 interval 轮询刷新。
 */
export function useReleaseRuns(
  releaseIds: string[],
  enabled: boolean = true,
  intervalMs: number = 10_000,
): Record<string, ReleaseRun[]> {
  const [runsById, setRunsById] = useState<Record<string, ReleaseRun[]>>({})
  const key = releaseIds.filter(Boolean).slice(0, 30).join(',')

  useEffect(() => {
    if (!enabled || key === '') return
    let cancelled = false
    const ids = key.split(',')
    const load = async () => {
      const settled = await Promise.allSettled(ids.map((id) => api.release(id)))
      if (cancelled) return
      const next: Record<string, ReleaseRun[]> = {}
      settled.forEach((result, index) => {
        if (result.status === 'fulfilled') next[ids[index]] = result.value.runs
      })
      setRunsById((current) => ({ ...current, ...next }))
    }
    void load()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, intervalMs)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [key, enabled, intervalMs])

  return runsById
}
