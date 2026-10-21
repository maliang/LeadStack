import { useMemo, useState } from 'react'
import { cn } from '../lib/cn'
import { CloseIcon } from './Icons'

export interface MultiSelectOption {
  id: string
  label: string
  hint?: string
}

interface SearchMultiSelectProps {
  options: MultiSelectOption[]
  value: string[]
  onChange: (next: string[]) => void
  placeholder: string
  emptyText: string
  /** 列表最大高度类名，默认 max-h-44 */
  listClassName?: string
}

/** 可搜索多选：选中项以 chip 展示，列表支持按名称/ID 过滤（角色范围、分组成员复用）。 */
export function SearchMultiSelect({
  options,
  value,
  onChange,
  placeholder,
  emptyText,
  listClassName = 'max-h-44',
}: SearchMultiSelectProps) {
  const [keyword, setKeyword] = useState('')
  const selected = useMemo(() => new Set(value), [value])
  const filtered = useMemo(() => {
    const query = keyword.trim().toLowerCase()
    if (!query) return options
    return options.filter(
      (option) =>
        option.label.toLowerCase().includes(query) ||
        option.id.toLowerCase().includes(query) ||
        (option.hint ?? '').toLowerCase().includes(query),
    )
  }, [options, keyword])

  const toggle = (id: string) => {
    if (selected.has(id)) onChange(value.filter((item) => item !== id))
    else onChange([...value, id])
  }

  const selectedOptions = options.filter((option) => selected.has(option.id))

  return (
    <div className="space-y-1.5">
      {selectedOptions.length > 0 ? (
        <div className="flex flex-wrap gap-1">
          {selectedOptions.map((option) => (
            <span
              key={option.id}
              className="inline-flex max-w-full items-center gap-1 rounded bg-indigo-50 px-1.5 py-0.5 text-[11px] text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300"
            >
              <span className="truncate">{option.label}</span>
              <button
                type="button"
                aria-label={option.label}
                onClick={() => toggle(option.id)}
                className="shrink-0 rounded p-0.5 hover:bg-indigo-100 dark:hover:bg-indigo-500/20"
              >
                <CloseIcon className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      ) : null}
      <input
        value={keyword}
        onChange={(event) => setKeyword(event.target.value)}
        placeholder={placeholder}
        className="w-full rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-800 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <div
        className={cn(
          'overflow-y-auto rounded-md border border-slate-200 p-1 dark:border-slate-800',
          listClassName,
        )}
      >
        {filtered.length === 0 ? (
          <p className="px-2 py-3 text-center text-[11px] text-slate-400 dark:text-slate-500">
            {emptyText}
          </p>
        ) : (
          filtered.map((option) => (
            <label
              key={option.id}
              className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-xs hover:bg-slate-50 dark:hover:bg-slate-800/60"
            >
              <input
                type="checkbox"
                checked={selected.has(option.id)}
                onChange={() => toggle(option.id)}
                className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 dark:border-slate-600 dark:bg-slate-800"
              />
              <span className="min-w-0 flex-1 truncate text-slate-700 dark:text-slate-200">
                {option.label}
              </span>
              {option.hint ? (
                <span className="shrink-0 font-mono text-[10px] text-slate-400 dark:text-slate-500">
                  {option.hint}
                </span>
              ) : null}
            </label>
          ))
        )}
      </div>
    </div>
  )
}
