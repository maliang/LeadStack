import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeftIcon } from './Icons'

interface PageHeaderProps {
  title: string
  description?: string
  actions?: ReactNode
  backTo?: string
  backLabel?: string
}

/** 统一页面标题区：标题 + 说明 + 右侧主操作（详情页可带返回链接）。 */
export function PageHeader({ title, description, actions, backTo, backLabel }: PageHeaderProps) {
  return (
    <div className="space-y-2">
      {backTo ? (
        <Link
          to={backTo}
          className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500 transition-colors hover:text-indigo-600 dark:text-slate-400 dark:hover:text-indigo-400"
        >
          <ArrowLeftIcon className="h-3.5 w-3.5" />
          {backLabel}
        </Link>
      ) : null}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="truncate text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {title}
          </h1>
          {description ? (
            <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
    </div>
  )
}
