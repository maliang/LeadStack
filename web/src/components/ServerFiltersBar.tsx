import type { GroupSummary, ServerFilters, ServerStatus } from '../lib/api'

interface ServerFiltersBarProps {
  filters: ServerFilters
  groups: GroupSummary[]
  tags: string[]
  onChange: (filters: ServerFilters) => void
}

const selectClass =
  'h-9 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

/** 列表页筛选：分组 / 标签 / 状态 / 接入方式（对应契约 GET /servers 的查询参数）。 */
export function ServerFiltersBar({ filters, groups, tags, onChange }: ServerFiltersBarProps) {
  const active = Boolean(filters.group || filters.tag || filters.status || filters.mode)

  return (
    <div className="flex flex-wrap items-center gap-2">
      <select
        aria-label="按分组筛选"
        value={filters.group ?? ''}
        onChange={(event) => onChange({ ...filters, group: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">全部分组</option>
        {groups.map((group) => (
          <option key={group.name} value={group.name}>
            {group.name}（{group.count}）
          </option>
        ))}
      </select>

      <select
        aria-label="按标签筛选"
        value={filters.tag ?? ''}
        onChange={(event) => onChange({ ...filters, tag: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">全部标签</option>
        {tags.map((tag) => (
          <option key={tag} value={tag}>
            {tag}
          </option>
        ))}
      </select>

      <select
        aria-label="按状态筛选"
        value={filters.status ?? ''}
        onChange={(event) =>
          onChange({ ...filters, status: (event.target.value || undefined) as ServerStatus | undefined })
        }
        className={selectClass}
      >
        <option value="">全部状态</option>
        <option value="online">在线</option>
        <option value="offline">离线</option>
        <option value="unknown">未知</option>
      </select>

      <select
        aria-label="按接入方式筛选"
        value={filters.mode ?? ''}
        onChange={(event) => onChange({ ...filters, mode: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">全部方式</option>
        <option value="agent">Agent</option>
        <option value="ssh">SSH</option>
      </select>

      {active ? (
        <button
          type="button"
          onClick={() => onChange({})}
          className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
        >
          清除筛选
        </button>
      ) : null}
    </div>
  )
}
