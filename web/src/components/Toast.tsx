import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { cn } from '../lib/cn'
import { withRequestId } from '../lib/errors'
import { t } from '../i18n'
import { Button } from './Button'
import { CopyButton } from './CopyButton'
import { CloseIcon } from './Icons'

export type ToastKind = 'success' | 'error' | 'info'

/**
 * 可选的错误附带信息（T-13）：调用方若已从 ApiError 上解构出 request_id，可一并传入，
 * toast 会按 errors.requestId 追加「请求编号 xxx」。不传时行为与改造前完全一致。
 */
export interface ToastDetail {
  requestId?: string | null
}

interface ToastItem {
  id: number
  kind: ToastKind
  message: string
}

interface ToastContextValue {
  toast: (message: string, kind?: ToastKind, detail?: ToastDetail) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

const MAX_TOASTS = 4
const TOAST_DURATION_MS = 4_000

let nextToastId = 1

const kindStyles: Record<ToastKind, string> = {
  success:
    'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200',
  error:
    'border-rose-200 bg-rose-50 text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200',
  info: 'border-slate-200 bg-white text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200',
}

/** 单条 toast：正文 + 复制（toast.copy，复制完整 message）+ 关闭（toast.close）。 */
function ToastRow({ item, onDismiss }: { item: ToastItem; onDismiss: (id: number) => void }) {
  return (
    <div
      className={cn(
        'pointer-events-auto w-full max-w-sm rounded-lg border px-4 py-3 text-sm shadow-lg shadow-slate-900/5 dark:shadow-black/30',
        kindStyles[item.kind],
      )}
    >
      <p className="break-words whitespace-pre-wrap">{item.message}</p>
      <div className="mt-2 flex items-center justify-end gap-2">
        <CopyButton value={item.message} label={t('toast.copy')} />
        <Button variant="ghost" size="sm" onClick={() => onDismiss(item.id)} icon={<CloseIcon className="h-4 w-4" />}>
          {t('toast.close')}
        </Button>
      </div>
    </div>
  )
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])

  const dismiss = useCallback((id: number) => {
    setItems((current) => current.filter((item) => item.id !== id))
  }, [])

  const toast = useCallback(
    (message: string, kind: ToastKind = 'info', detail?: ToastDetail) => {
      const id = nextToastId++
      // 队列上限语义不变：保留最近 MAX_TOASTS - 1 条，再追加新的这条。
      setItems((current) => [...current.slice(-(MAX_TOASTS - 1)), { id, kind, message: withRequestId(message, detail?.requestId) }])
      // 错误类 toast 不设定时器：留给用户读完、复制或手动关闭（A27）。
      if (kind !== 'error') {
        window.setTimeout(() => dismiss(id), TOAST_DURATION_MS)
      }
    },
    [dismiss],
  )

  const value = useMemo<ToastContextValue>(() => ({ toast }), [toast])

  const errorItems = items.filter((item) => item.kind === 'error')
  const otherItems = items.filter((item) => item.kind !== 'error')

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="pointer-events-none fixed inset-x-0 bottom-4 z-[60] flex flex-col items-center gap-2 px-4 sm:bottom-6 sm:right-6 sm:left-auto sm:items-end">
        {/* 非 error 保持 polite：自动消失，不打断朗读。 */}
        <div
          className="flex w-full flex-col items-center gap-2 sm:items-end"
          aria-live="polite"
          role="status"
        >
          {otherItems.map((item) => (
            <ToastRow key={item.id} item={item} onDismiss={dismiss} />
          ))}
        </div>
        {/* error 用 role="alert"（assertive）：不自动消失，需要用户处理。 */}
        <div
          className="flex w-full flex-col items-center gap-2 sm:items-end"
          aria-live="assertive"
          role="alert"
        >
          {errorItems.map((item) => (
            <ToastRow key={item.id} item={item} onDismiss={dismiss} />
          ))}
        </div>
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const context = useContext(ToastContext)
  if (!context) throw new Error(t('s.0577'))
  return context
}
