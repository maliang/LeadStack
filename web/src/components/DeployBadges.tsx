import type { ArtifactKind, ReleaseStatus, TemplateKind } from '../lib/api'
import { cn } from '../lib/cn'

const statusConfig: Record<ReleaseStatus, { label: string; container: string; dot: string }> = {
  pending: {
    label: '等待中',
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
    dot: 'bg-slate-400',
  },
  running: {
    label: '进行中',
    container:
      'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-500/10 dark:text-blue-300 dark:ring-blue-400/30',
    dot: 'bg-blue-500 animate-pulse',
  },
  success: {
    label: '成功',
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
    dot: 'bg-emerald-500',
  },
  failed: {
    label: '失败',
    container:
      'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-400/30',
    dot: 'bg-rose-500',
  },
  canceled: {
    label: '已取消',
    container:
      'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/30',
    dot: 'bg-amber-500',
  },
}

/** 发布/run 状态徽标：pending 灰 / running 蓝 / success 绿 / failed 红 / canceled 琥珀。 */
export function ReleaseStatusBadge({
  status,
  className,
}: {
  status: ReleaseStatus
  className?: string
}) {
  const config = statusConfig[status] ?? statusConfig.pending
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={`状态：${status}`}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', config.dot)} aria-hidden="true" />
      {config.label}
    </span>
  )
}

const kindConfig: Record<string, { label: string; container: string }> = {
  compose: {
    label: 'Compose',
    container:
      'bg-sky-50 text-sky-700 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/30',
  },
  systemd: {
    label: 'systemd',
    container:
      'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30',
  },
  binary: {
    label: '二进制',
    container:
      'bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-400/30',
  },
}

export function TemplateKindBadge({
  kind,
  className,
}: {
  kind: TemplateKind | string
  className?: string
}) {
  const config = kindConfig[kind] ?? {
    label: kind || '未知',
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
  }
  return (
    <span
      className={cn(
        'inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
      title={`模板类型：${kind}`}
    >
      {config.label}
    </span>
  )
}

const artifactConfig: Record<string, { label: string; container: string }> = {
  url: {
    label: 'URL',
    container:
      'bg-sky-50 text-sky-700 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/30',
  },
  upload: {
    label: '上传',
    container:
      'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/30',
  },
  image: {
    label: '镜像',
    container:
      'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30',
  },
}

export function ArtifactKindBadge({
  kind,
  className,
}: {
  kind: ArtifactKind | string
  className?: string
}) {
  const config = artifactConfig[kind] ?? {
    label: kind || '未知',
    container:
      'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30',
  }
  return (
    <span
      className={cn(
        'inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        config.container,
        className,
      )}
    >
      {config.label}
    </span>
  )
}
