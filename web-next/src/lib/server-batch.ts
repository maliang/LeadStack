import type { BatchAction, BatchPayload, BatchResultItem } from './api'

/**
 * 服务器批量操作的纯函数层。
 *
 * 页面只持「选中集 + 表单草稿 + 结果列表」三份状态，所有映射都走这里：
 *   - 选中集 → POST /servers/batch 的 payload（含参数校验）
 *   - 逐台结果 → 成功/失败两段与汇总数字
 *   - 失败项重试 → 单台 payload 与结果替换
 * 不在这里发请求，方便单测与在页面里做三态。
 */

/** 后端 handlers_servers.go 的 maxBatchIDs。 */
export const MAX_BATCH_IDS = 200
/** 后端 maxTagCount。 */
export const MAX_BATCH_TAGS = 32
/** 后端 maxTagRunes。 */
export const MAX_BATCH_TAG_LENGTH = 64
/** 后端 maxGroupRunes。 */
export const MAX_BATCH_GROUP_LENGTH = 64

/** set-group 下拉里代表「输入新分组」的哨兵值，不会作为分组名提交。 */
export const CUSTOM_GROUP_VALUE = '__custom__'

export const BATCH_ACTIONS: readonly BatchAction[] = [
  'test-connection',
  'refresh-host-info',
  'add-tags',
  'set-group',
  'delete',
]

const ACTION_NAME: Record<BatchAction, string> = {
  'test-connection': 'testConnection',
  'refresh-host-info': 'refreshHostInfo',
  'add-tags': 'addTags',
  'set-group': 'setGroup',
  delete: 'delete',
}

export function batchActionLabelKey(action: BatchAction): string {
  return `serversBatch.action.${ACTION_NAME[action]}`
}

export function batchActionDialogKey(action: BatchAction, part: 'title' | 'desc' | 'confirm'): string {
  return `serversBatch.dialog.${ACTION_NAME[action]}.${part}`
}

/** 删除是唯一不可撤销的动作：按钮用危险色并带二次确认文案。 */
export function isDangerousBatchAction(action: BatchAction): boolean {
  return action === 'delete'
}

export interface BatchDraft {
  /** add-tags 的原始输入：逗号 / 中文逗号 / 换行分隔 */
  tagsText: string
  /** set-group 下拉选中的分组名，或 CUSTOM_GROUP_VALUE */
  group: string
  /** set-group 选择「新分组」时手填的名称 */
  customGroup: string
}

export function emptyBatchDraft(): BatchDraft {
  return { tagsText: '', group: '', customGroup: '' }
}

/** 去空白、按分隔符拆分、去重（保留首次出现的顺序）。 */
export function parseTagsInput(raw: string): string[] {
  const parts = raw.split(/[,，\n\r]+/)
  const seen = new Set<string>()
  const tags: string[] = []
  for (const part of parts) {
    const tag = part.trim()
    if (tag === '' || seen.has(tag)) continue
    seen.add(tag)
    tags.push(tag)
  }
  return tags
}

/** 选中集去空白、去重，保持页面顺序。 */
export function normalizeIds(ids: readonly string[]): string[] {
  const seen = new Set<string>()
  const next: string[] = []
  for (const raw of ids) {
    const id = raw.trim()
    if (id === '' || seen.has(id)) continue
    seen.add(id)
    next.push(id)
  }
  return next
}

export function resolveDraftGroup(draft: BatchDraft): string {
  const group = draft.group === CUSTOM_GROUP_VALUE ? draft.customGroup : draft.group
  return group.trim()
}

export type BatchValidationKey =
  | 'serversBatch.error.noSelection'
  | 'serversBatch.error.tooMany'
  | 'serversBatch.error.tagsEmpty'
  | 'serversBatch.error.tagsTooMany'
  | 'serversBatch.error.tagTooLong'
  | 'serversBatch.error.groupEmpty'
  | 'serversBatch.error.groupTooLong'

export interface BatchValidationError {
  key: BatchValidationKey
  vars?: Record<string, string | number>
}

/**
 * 提交前的统一校验：选中数、（add-tags）标签数量与长度、（set-group）分组名。
 * 返回 null 表示可以提交；文案键由调用方 t() 展开，便于两种语言共用同一套规则。
 */
export function validateBatchDraft(
  ids: readonly string[],
  action: BatchAction,
  draft: BatchDraft,
): BatchValidationError | null {
  const normalized = normalizeIds(ids)
  if (normalized.length === 0) return { key: 'serversBatch.error.noSelection' }
  if (normalized.length > MAX_BATCH_IDS) {
    return { key: 'serversBatch.error.tooMany', vars: { max: MAX_BATCH_IDS } }
  }
  if (action === 'add-tags') {
    const tags = parseTagsInput(draft.tagsText)
    if (tags.length === 0) return { key: 'serversBatch.error.tagsEmpty' }
    if (tags.length > MAX_BATCH_TAGS) {
      return { key: 'serversBatch.error.tagsTooMany', vars: { max: MAX_BATCH_TAGS } }
    }
    const tooLong = tags.find((tag) => tag.length > MAX_BATCH_TAG_LENGTH)
    if (tooLong !== undefined) {
      return {
        key: 'serversBatch.error.tagTooLong',
        vars: { len: MAX_BATCH_TAG_LENGTH, tag: tooLong },
      }
    }
  }
  if (action === 'set-group') {
    const group = resolveDraftGroup(draft)
    if (group === '') return { key: 'serversBatch.error.groupEmpty' }
    if (group.length > MAX_BATCH_GROUP_LENGTH) {
      return { key: 'serversBatch.error.groupTooLong', vars: { len: MAX_BATCH_GROUP_LENGTH } }
    }
  }
  return null
}

/** 只组装 action 需要的参数，避免给 delete 之类塞一个空 params。 */
export function buildBatchParams(
  action: BatchAction,
  draft: BatchDraft,
): Record<string, unknown> | undefined {
  if (action === 'add-tags') return { tags: parseTagsInput(draft.tagsText) }
  if (action === 'set-group') return { group: resolveDraftGroup(draft) }
  return undefined
}

/** 选中集 + 动作 + 表单草稿 → POST /servers/batch 请求体。 */
export function buildBatchPayload(
  ids: readonly string[],
  action: BatchAction,
  draft: BatchDraft = emptyBatchDraft(),
): BatchPayload {
  const payload: BatchPayload = { ids: normalizeIds(ids), action }
  const params = buildBatchParams(action, draft)
  if (params !== undefined) payload.params = params
  return payload
}

/* ------------------------------------------------------------------ */
/* 选中集                                                              */
/* ------------------------------------------------------------------ */

export function toggleSelection(ids: readonly string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((item) => item !== id) : [...ids, id]
}

/** 全选「当前筛选结果」：先与可见集求交，再并上已选中的隐藏项。 */
export function selectAllVisible(selected: readonly string[], visible: readonly string[]): string[] {
  const next = [...selected]
  for (const id of visible) if (!next.includes(id)) next.push(id)
  return next
}

/** 过滤条件变化后丢掉已不可见的选中项，保持原顺序。 */
export function pruneSelection(selected: readonly string[], visible: readonly string[]): string[] {
  const allowed = new Set(visible)
  return selected.filter((id) => allowed.has(id))
}

export function allVisibleSelected(selected: readonly string[], visible: readonly string[]): boolean {
  return visible.length > 0 && visible.every((id) => selected.includes(id))
}

export function someVisibleSelected(selected: readonly string[], visible: readonly string[]): boolean {
  return visible.some((id) => selected.includes(id)) && !allVisibleSelected(selected, visible)
}

/* ------------------------------------------------------------------ */
/* 逐台结果                                                           */
/* ------------------------------------------------------------------ */

export interface BatchResultGroups {
  succeeded: BatchResultItem[]
  failed: BatchResultItem[]
  summary: { ok: number; failed: number }
}

/** 成功/失败两段 + 汇总；汇总按 results 实算，不信任可能落后的后端 summary。 */
export function groupBatchResults(results: readonly BatchResultItem[]): BatchResultGroups {
  const succeeded: BatchResultItem[] = []
  const failed: BatchResultItem[] = []
  for (const item of results) (item.ok ? succeeded : failed).push(item)
  return {
    succeeded,
    failed,
    summary: { ok: succeeded.length, failed: failed.length },
  }
}

export function batchResultTotal(groups: BatchResultGroups): number {
  return groups.summary.ok + groups.summary.failed
}

/** 结果只带 id，展示时回查名称；列表刷新后已删除的服务器回退到 id 本身。 */
export function serverLabel(id: string, names: Readonly<Record<string, string>>): string {
  const name = names[id]
  return name !== undefined && name.trim() !== '' ? name : id
}

export function buildServerNameMap(
  servers: readonly { id: string; name: string }[],
): Record<string, string> {
  const map: Record<string, string> = {}
  for (const server of servers) map[server.id] = server.name
  return map
}

/** 单台失败项重试一次用的 payload，沿用原动作与原参数。 */
export function retryPayload(
  id: string,
  action: BatchAction,
  params?: Record<string, unknown>,
): BatchPayload {
  const payload: BatchPayload = { ids: [id], action }
  if (params !== undefined) payload.params = params
  return payload
}

/** 用重试结果替换对应条目，保持原顺序；条目不存在时原样返回。 */
export function mergeBatchResult(
  results: readonly BatchResultItem[],
  next: BatchResultItem,
): BatchResultItem[] {
  let found = false
  const merged = results.map((item) => {
    if (item.id !== next.id) return item
    found = true
    return next
  })
  return found ? merged : [...merged, next]
}
