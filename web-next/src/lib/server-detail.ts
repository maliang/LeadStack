import type {
  CheckItem,
  CheckState,
  ServerAccess,
  ServerLoginResult,
} from './api'

/**
 * 服务器详情页的纯函数与常量（文案/状态映射只此一处）。
 *
 * 组件只做渲染与事件派发；密码掩码、档案校验、登录结果三态分派都在这里，
 * 便于单测，也避免「同一个口径在多个组件里各写一套」。
 */

/** 密码掩码占位：只在界面上替换明文，复制时仍取真实值。 */
export const ACCESS_PASSWORD_MASK = '••••••••'

export interface ServerAccessLabels {
  name: string
  host: string
  port: string
  username: string
  password: string
}

/**
 * 把登录信息拼成「字段: 值」多行文本，供一键复制使用。
 * 端口是数字，不带单位后缀（数字与单位分开呈现）。
 */
export function formatServerAccess(access: ServerAccess, labels: ServerAccessLabels): string {
  return [
    `${labels.name}: ${access.name}`,
    `${labels.host}: ${access.host}`,
    `${labels.port}: ${access.port === null ? '' : access.port}`,
    `${labels.username}: ${access.username}`,
    `${labels.password}: ${access.password}`,
  ].join('\n')
}

/* ---------------------------------------------------------------- */
/* 服务器档案：备注 + 自定义字段 KV（契约见 handlers_servers.go）      */
/* ---------------------------------------------------------------- */

/** 备注上限（与后端 maxNotesRunes 对齐）。 */
export const META_NOTES_MAX = 2000
/** 自定义字段键上限。 */
export const META_KEY_MAX = 40
/** 自定义字段值上限。 */
export const META_VALUE_MAX = 200
/** 自定义字段数量上限。 */
export const META_FIELD_MAX = 32

export interface MetaRow {
  id: number
  key: string
  value: string
}

let nextMetaRowId = 1

export function newMetaRow(key = '', value = ''): MetaRow {
  nextMetaRowId += 1
  return { id: nextMetaRowId, key, value }
}

/** 接口的 custom_fields（对象）→ 可编辑行；空表给一行空行，省一次「添加字段」点击。 */
export function metaRowsFromFields(fields: Record<string, string>): MetaRow[] {
  const rows = Object.entries(fields).map(([key, value]) => newMetaRow(key.trim(), value))
  return rows.length > 0 ? rows : [newMetaRow()]
}

/** 可编辑行 → 提交对象：空行丢弃、键去首尾空白，同键后者覆盖前者。 */
export function metaFieldsFromRows(rows: readonly MetaRow[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const row of rows) {
    const key = row.key.trim()
    if (key === '') continue
    out[key] = row.value
  }
  return out
}

/** 已填写的字段数（空键不算）。 */
export function metaFilledFieldCount(rows: readonly MetaRow[]): number {
  return rows.filter((row) => row.key.trim() !== '').length
}

export type MetaIssueCode =
  | 'notesTooLong'
  | 'keyTooLong'
  | 'valueTooLong'
  | 'duplicateKey'
  | 'tooManyFields'

export interface MetaIssue {
  code: MetaIssueCode
  /** 出问题的行下标（非行级问题为 -1）。 */
  index: number
}

/** 保存前校验：返回第一处问题，null 表示可提交。 */
export function metaValidate(notes: string, rows: readonly MetaRow[]): MetaIssue | null {
  if ([...notes].length > META_NOTES_MAX) return { code: 'notesTooLong', index: -1 }
  const filled = metaFilledFieldCount(rows)
  if (filled > META_FIELD_MAX) return { code: 'tooManyFields', index: -1 }
  const seen = new Set<string>()
  for (let index = 0; index < rows.length; index += 1) {
    const row = rows[index]
    const key = row.key.trim()
    if (key === '') continue
    if ([...key].length > META_KEY_MAX) return { code: 'keyTooLong', index }
    if ([...row.value].length > META_VALUE_MAX) return { code: 'valueTooLong', index }
    if (seen.has(key)) return { code: 'duplicateKey', index }
    seen.add(key)
  }
  return null
}

/** 预设模板的 i18n 键（点一下补一个空值字段，键名随界面语言走）。 */
export const META_PRESET_KEYS = [
  'server.meta.preset.datacenter',
  'server.meta.preset.vendor',
  'server.meta.preset.owner',
  'server.meta.preset.expires',
  'server.meta.preset.bandwidth',
  'server.meta.preset.line',
] as const

/* ---------------------------------------------------------------- */
/* 一键登录：url / terminal / output 三态分派                        */
/* ---------------------------------------------------------------- */

export type LoginResultMode = 'url' | 'terminal' | 'output' | 'empty'

/**
 * 按后端返回的字段决定展示哪一态：url 优先，其次 terminal，再次 output。
 * 三个字段全空 = 脚本没有返回可用结果（界面给提示，不静默）。
 */
export function loginResultMode(result: ServerLoginResult | null): LoginResultMode {
  if (!result) return 'empty'
  if (result.url.trim() !== '') return 'url'
  if (result.terminal.trim() !== '') return 'terminal'
  if (result.output !== '') return 'output'
  return 'empty'
}

/* ---------------------------------------------------------------- */
/* 检测：该服务器适用的检测项 + 状态映射                              */
/* ---------------------------------------------------------------- */

/** 只保留 targets（server_ids）里包含该服务器的检测项。 */
export function checksForServer(checks: readonly CheckItem[], serverId: string): CheckItem[] {
  if (serverId === '') return []
  return checks.filter((check) => check.server_ids.includes(serverId))
}

/** 检测状态 → i18n 文案键（up 正常 / down 异常 / unknown 未知）。 */
export function checkStateLabelKey(state: CheckState | string): string {
  if (state === 'up') return 's.0317'
  if (state === 'down') return 's.0318'
  return 's.0089'
}

/** 检测状态 → 语义色（与服务器状态同一套：正常/错误/中性）。 */
export function checkStateTone(state: CheckState | string): 'success' | 'danger' | 'muted' {
  if (state === 'up') return 'success'
  if (state === 'down') return 'danger'
  return 'muted'
}

/** 24h 可用率 → 语义色：≥99.9 正常、≥99 警示、其余错误、无数据中性。 */
export function availabilityTone(
  value: number | null,
): 'success' | 'warning' | 'danger' | 'muted' {
  if (value === null) return 'muted'
  if (value >= 99.9) return 'success'
  if (value >= 99) return 'warning'
  return 'danger'
}
