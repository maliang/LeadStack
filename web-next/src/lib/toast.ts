import { reactive, readonly } from 'vue'
import { withRequestId } from './errors'

/**
 * 全局提示（toast）状态。
 *
 * 产品契约（`scripts/verify_ops.sh` 的 A27）：
 * - **错误类提示不自寻消失**：用户往往要复制「请求编号」去查后端日志，自动消失会让人重做一次操作；
 * - 可手动关闭、可一键复制正文；
 * - 无障碍：错误用 `role="alert"` + `aria-live="assertive"`，其余用 `role="status"` + `polite`。
 *
 * 这里只保存数据，渲染在 `components/AppToaster.vue`，避免任何页面各写一套提示。
 */

export type ToastTone = 'info' | 'success' | 'error'

export interface ToastItem {
  id: number
  tone: ToastTone
  /** 标题（通常是一句「操作失败」之类的短句） */
  title: string
  /** 正文：错误文案（已含请求编号时不会重复追加） */
  message: string
  /** 请求编号，便于用户直接报障 */
  requestId: string | null
  /** 常驻（不自动消失） */
  sticky: boolean
}

export interface ToastInput {
  tone?: ToastTone
  title: string
  message?: string
  requestId?: string | null
  sticky?: boolean
}

/** 非常驻提示的停留时长（错误类默认常驻，不受此值影响） */
export const TOAST_AUTO_DISMISS_MS = 6_000

const state = reactive<{ items: ToastItem[] }>({ items: [] })
const timers = new Map<number, ReturnType<typeof setTimeout>>()
let seq = 0

export function useToasts(): readonly ToastItem[] {
  return readonly(state).items
}

export function pushToast(input: ToastInput): number {
  const tone = input.tone ?? 'info'
  const id = ++seq
  const message = input.message ?? ''
  const item: ToastItem = {
    id,
    tone,
    title: input.title,
    message: message.includes(input.requestId ?? '\u0000')
      ? message
      : withRequestId(message, input.requestId),
    requestId: input.requestId ?? null,
    // 错误默认常驻：用户要复制请求编号，自动消失等于把线索拿走
    sticky: input.sticky ?? tone === 'error',
  }
  state.items = [...state.items, item]
  if (!item.sticky) {
    timers.set(
      id,
      setTimeout(() => dismissToast(id), TOAST_AUTO_DISMISS_MS),
    )
  }
  return id
}

export function dismissToast(id: number): void {
  const timer = timers.get(id)
  if (timer !== undefined) {
    clearTimeout(timer)
    timers.delete(id)
  }
  state.items = state.items.filter((item) => item.id !== id)
}
