/**
 * 「登录脚本」编辑约束与校验（纯函数，无 Vue / 请求依赖）。
 *
 * 约定：
 * - 长度按 Unicode 码点（rune）计数，上限 8192；UTF-8 字节数与 `len()` 都不是判据。
 * - 保存时 trim 后为空视为非法（清空必须走显式的「清空脚本」，避免误清）。
 * - 校验只回答「能不能提交」，请求由页面 owner 发出（见 ServerDetailPage.saveLoginScript）。
 */

/** 脚本长度上限（Unicode 码点）。 */
export const LOGIN_SCRIPT_MAX_RUNES = 8192

/** 字段级校验结果；空表示可以提交。 */
export type LoginScriptIssue = 'empty' | 'tooLong'

/** 插入示例后的文本与光标位置。 */
export interface LoginScriptInsertion {
  text: string
  /** 插入内容之后的光标位置（供 textarea.setSelectionRange 使用）。 */
  caret: number
}

/**
 * 按 Unicode 码点计数（与后端「rune」口径一致）。
 * 用扩展运算符而不是 `str.length`：后者按 UTF-16 码元算，emoji/生僻字会被算成 2。
 */
export function loginScriptRuneCount(script: string): number {
  let count = 0
  for (const _ of script) count += 1
  return count
}

export function loginScriptIsOverLimit(script: string): boolean {
  return loginScriptRuneCount(script) > LOGIN_SCRIPT_MAX_RUNES
}

/** 保存前的字段级校验：为空 → 'empty'，超长 → 'tooLong'。 */
export function loginScriptValidate(script: string): LoginScriptIssue | null {
  if (script.trim() === '') return 'empty'
  if (loginScriptIsOverLimit(script)) return 'tooLong'
  return null
}

/**
 * 要提交给 PATCH 的脚本文本。
 * 只去掉首尾空白（避免把编辑器里的尾部空行存进库），中间内容逐字节保留。
 */
export function loginScriptPayload(script: string): string {
  return script.trim()
}

/**
 * 把示例脚本插入到光标处（选区被替换）。
 *
 * 不做「有内容就弹窗确认」：插入点由用户的光标决定，空文档时等价于整段写入。
 * 插入前补一个换行、插入后补一个换行，保证与原内容不会连成一行非法命令。
 */
export function insertLoginScriptSample(
  current: string,
  sample: string,
  selectionStart: number | null = null,
  selectionEnd: number | null = null,
): LoginScriptInsertion {
  const text = sample.replace(/\s+$/, '')
  const length = current.length
  const rawStart = selectionStart ?? length
  const rawEnd = selectionEnd ?? rawStart
  const start = Math.min(Math.max(rawStart, 0), length)
  const end = Math.min(Math.max(rawEnd, start), length)
  const before = current.slice(0, start)
  const after = current.slice(end)
  // 只在「会与已有内容粘成同一行」时补换行：往空编辑器里插入必须是示例原文逐字节一致。
  const prefix = before !== '' && !before.endsWith('\n') ? '\n' : ''
  const suffix = after !== '' && !after.startsWith('\n') ? '\n' : ''
  const inserted = `${prefix}${text}${suffix}`
  return { text: `${before}${inserted}${after}`, caret: before.length + inserted.length }
}
