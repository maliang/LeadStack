import type { GroupSummary, ServerFilters, ServerGroup, ServerStatus } from '../lib/api'
import { t } from '../i18n'

interface ServerFiltersBarProps {
  filters: ServerFilters
  groups: GroupSummary[]
  tags: string[]
  onChange: (filters: ServerFilters) => void
  /** 授权分组（服务端分组，与标签筛选并存） */
  serverGroups?: ServerGroup[]
  groupId?: string
  onGroupIdChange?: (groupId: string) => void
}

const selectClass =
  'h-9 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

/** 列表页筛选：分组 / 标签 / 状态 / 接入方式（对应契约 GET /servers 的查询参数）。 */
export function ServerFiltersBar({
  filters,
  groups,
  tags,
  onChange,
  serverGroups,
  groupId,
  onGroupIdChange,
}: ServerFiltersBarProps) {
  const active = Boolean(filters.group || filters.tag || filters.status || filters.mode || groupId)

  return (
    <div className="flex flex-wrap items-center gap-2">
      <select
        aria-label={t('s.0517')}
        value={filters.group ?? ''}
        onChange={(event) => onChange({ ...filters, group: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">{t('s.0376')}</option>
        {groups.map((group) => (
          <option key={group.name} value={group.name}>
            {group.name}（{group.count}）
          </option>
        ))}
      </select>

      <select
        aria-label={t('s.0518')}
        value={filters.tag ?? ''}
        onChange={(event) => onChange({ ...filters, tag: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">{t('s.0378')}</option>
        {tags.map((tag) => (
          <option key={tag} value={tag}>
            {tag}
          </option>
        ))}
      </select>

      <select
        aria-label={t('s.0519')}
        value={filters.status ?? ''}
        onChange={(event) =>
          onChange({ ...filters, status: (event.target.value || undefined) as ServerStatus | undefined })
        }
        className={selectClass}
      >
        <option value="">{t('s.0520')}</option>
        <option value="online">{t('s.0521')}</option>
        <option value="offline">{t('s.0522')}</option>
        <option value="unknown">{t('s.0089')}</option>
      </select>

      <select
        aria-label={t('s.0523')}
        value={filters.mode ?? ''}
        onChange={(event) => onChange({ ...filters, mode: event.target.value || undefined })}
        className={selectClass}
      >
        <option value="">{t('s.0524')}</option>
        <option value="agent">Agent</option>
        <option value="ssh">SSH</option>
      </select>

      {serverGroups && onGroupIdChange ? (
        <select
          aria-label={t('groups.filterByGroup')}
          value={groupId ?? ''}
          onChange={(event) => onGroupIdChange(event.target.value)}
          className={selectClass}
        >
          <option value="">{t('groups.allGroups')}</option>
          {serverGroups.map((group) => (
            <option key={group.id} value={group.id}>
              {group.name}（{group.member_count}）
            </option>
          ))}
        </select>
      ) : null}

      {active ? (
        <button
          type="button"
          onClick={() => {
            onChange({})
            onGroupIdChange?.('')
          }}
          className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
        >
          {t('s.0525')}
        </button>
      ) : null}
    </div>
  )
}
