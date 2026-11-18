import { getLanguage, t } from '../i18n'

/**
 * 结构化错误信息：`message` 与 {@link errorMessage} 的文案口径一致，
 * 另带后端 `error.code` 与 `error.request_id`（T-05 起 500/所有错误体都带 request_id，
 * ApiError 已把它暴露为 `readonly requestId: string | null`）。
 *
 * 用它可以不改既有调用签名就拿到 code / requestId；只关心文案的调用点继续用 errorMessage。
 */
export interface ErrorInfo {
  message: string
  code: string | null
  requestId: string | null
}

/** 从任意被抛对象上读一个非空字符串字段（ApiError / 裸错误信封都适用）。 */
function readField(error: unknown, key: 'code' | 'requestId'): string | null {
  if (typeof error !== 'object' || error === null) return null
  const value = (error as Record<string, unknown>)[key]
  return typeof value === 'string' && value.trim() !== '' ? value.trim() : null
}

/**
 * 在错误文案末尾追加「请求编号 xxx」（i18n 键 errors.requestId）。
 * requestId 缺省/空串时原样返回；文案里已包含同一个 id 时不重复追加（幂等），
 * 因此 errorMessage 与 toast 两处都调用也不会出现两条编号。
 */
export function withRequestId(message: string, requestId?: string | null): string {
  const id = typeof requestId === 'string' ? requestId.trim() : ''
  if (id === '' || message.includes(id)) return message
  const label = t('errors.requestId', { id })
  return getLanguage() === 'en-US' ? `${message} (${label})` : `${message}（${label}）`
}

/**
 * 拆解错误：`message` 的取值规则与改造前完全一致（Error 且 message 非空 → 原文，否则 fallback），
 * 额外带上 `code` / `requestId`，供需要区分错误码或展示请求编号的调用点使用。
 */
export function errorInfo(error: unknown, fallback: string = t('s.0625')): ErrorInfo {
  const message = error instanceof Error && error.message.trim() !== '' ? error.message : fallback
  return { message, code: readField(error, 'code'), requestId: readField(error, 'requestId') }
}

/**
 * 后端/网络错误的用户可见文案。
 *
 * 签名与返回值形态（string）与改造前保持一致，既有调用点（含 toast(...) 与 setError(...)）无需改动；
 * 错误携带 request_id 时自动追加「请求编号 xxx」，用户报障可直接拿它去后端日志里定位那一次请求。
 */
export function errorMessage(error: unknown, fallback: string = t('s.0625')): string {
  const info = errorInfo(error, fallback)
  return withRequestId(info.message, info.requestId)
}
