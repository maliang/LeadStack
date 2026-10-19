import type { RunCheckResponse } from '../lib/api'
import { cn } from '../lib/cn'
import { formatLatency } from '../lib/format'
import { Button } from './Button'
import { CheckIcon, CloseIcon, SpinnerIcon } from './Icons'
import { Modal } from './Modal'

interface RunResultsDialogProps {
  open: boolean
  title: string
  loading: boolean
  error: string | null
  response: RunCheckResponse | null
  serverNameOf: (serverId: string) => string
  onClose: () => void
}

/** 「立即执行」逐台结果：成功/失败 + 延迟 + 失败原因。 */
export function RunResultsDialog({
  open,
  title,
  loading,
  error,
  response,
  serverNameOf,
  onClose,
}: RunResultsDialogProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description="对全部适用服务器各执行一次，逐台结果如下"
      footer={
        <Button onClick={onClose} disabled={loading}>
          关闭
        </Button>
      }
    >
      {loading ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-slate-500 dark:text-slate-400">
          <SpinnerIcon className="h-4 w-4" />
          正在执行…
        </div>
      ) : error ? (
        <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {error}
        </p>
      ) : response ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <span className="inline-flex items-center gap-1 rounded-full bg-emerald-50 px-2 py-0.5 font-medium text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">
              成功 {response.summary.ok}
            </span>
            <span
              className={cn(
                'inline-flex items-center gap-1 rounded-full px-2 py-0.5 font-medium',
                response.summary.failed > 0
                  ? 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400',
              )}
            >
              失败 {response.summary.failed}
            </span>
          </div>

          {response.results.length === 0 ? (
            <p className="text-sm text-slate-500 dark:text-slate-400">
              该检测项还没有适用服务器，请先设置适用服务器。
            </p>
          ) : (
            <ul className="divide-y divide-slate-100 overflow-hidden rounded-lg border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
              {response.results.map((result) => (
                <li key={result.server_id} className="flex items-start gap-2.5 px-3 py-2.5">
                  <span
                    className={cn(
                      'mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full',
                      result.ok
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
                        : 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300',
                    )}
                  >
                    {result.ok ? <CheckIcon className="h-3 w-3" /> : <CloseIcon className="h-3 w-3" />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm text-slate-800 dark:text-slate-100">
                      {serverNameOf(result.server_id)}
                    </span>
                    {!result.ok ? (
                      <span className="mt-0.5 block text-xs leading-relaxed text-rose-600 dark:text-rose-300">
                        {result.message?.trim() ? result.message : '执行失败（控制端未返回原因）'}
                      </span>
                    ) : null}
                  </span>
                  <span className="shrink-0 text-xs tabular-nums text-slate-500 dark:text-slate-400">
                    {formatLatency(result.latency_ms)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </Modal>
  )
}
