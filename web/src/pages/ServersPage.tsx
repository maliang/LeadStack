import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type BatchAction,
  type BatchResponse,
  type Server,
  type ServerFilters,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { formatClockTime } from '../lib/format'
import { AddServerWizard } from '../components/AddServerWizard'
import { LocalServerDialog } from '../components/LocalServerDialog'
import { BatchActionBar } from '../components/BatchActionBar'
import { BatchResultDialog } from '../components/BatchResultDialog'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { PlusIcon, RefreshIcon, ServerIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { ServerFiltersBar } from '../components/ServerFiltersBar'
import { DeleteServerDialog, EditServerDialog } from '../components/ServerDialogs'
import { ServerTable } from '../components/ServerTable'
import { TagsDialog } from '../components/TagsDialog'
import { useToast } from '../components/Toast'
import { useAuth } from '../lib/auth'

import { t } from '../i18n'

interface BatchState {
  open: boolean
  title: string
  loading: boolean
  error: string | null
  response: BatchResponse | null
}

/**
 * 服务器页：全部机器的纳管入口（筛选、批量操作、增删改、标签与分组）。
 *
 * 与「总览」的分工（两者此前共用同一个组件，点哪个菜单都一样，见契约第 46 节）：
 * 总览只回答「现在整体怎么样」（KPI + 检测/告警/发布摘要 + 需要关注的机器），
 * 本页负责「具体某一台怎么操作」。
 */
export default function ServersPage() {
  const { toast } = useToast()
  const { can } = useAuth()
  const [filters, setFilters] = useState<ServerFilters>({})
  const [groupId, setGroupId] = useState('')
  const filterKey = JSON.stringify(filters)

  const serversQuery = usePolling(() => api.servers(filters), 10_000, true, filterKey)
  const groupsQuery = usePolling(api.groups, 10_000)
  const serverGroupsQuery = usePolling(api.serverGroups, 30_000)
  const now = useNow()

  const [wizardOpen, setWizardOpen] = useState(false)
  const [localOpen, setLocalOpen] = useState(false)
  const [renaming, setRenaming] = useState<Server | null>(null)
  const [deleting, setDeleting] = useState<Server | null>(null)
  const [tagEditing, setTagEditing] = useState<Server | null>(null)
  const [tagSaving, setTagSaving] = useState(false)
  const [tagError, setTagError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [batch, setBatch] = useState<BatchState | null>(null)
  const [batchNames, setBatchNames] = useState<Record<string, string>>({})
  const [refreshing, setRefreshing] = useState(false)

  const servers = serversQuery.data ?? []
  const visibleServers = groupId
    ? servers.filter((server) => server.group_ids.includes(groupId))
    : servers
  const hasFilters = Boolean(
    filters.group || filters.tag || filters.status || filters.mode || groupId,
  )

  const tagOptions = useMemo(() => {
    const tags = new Set<string>()
    for (const server of servers) for (const tag of server.tags) tags.add(tag)
    return [...tags].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }, [servers])

  const groupOptions = useMemo(
    () => (groupsQuery.data ?? []).map((group) => group.name),
    [groupsQuery.data],
  )

  const selectedServers = useMemo(
    () => servers.filter((server) => selected.has(server.id)),
    [servers, selected],
  )

  // 轮询后自动丢弃已不存在的选中项
  useEffect(() => {
    setSelected((previous) => {
      if (previous.size === 0) return previous
      const ids = new Set(servers.map((server) => server.id))
      const next = new Set([...previous].filter((id) => ids.has(id)))
      return next.size === previous.size ? previous : next
    })
  }, [servers])

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([serversQuery.refresh(), groupsQuery.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  const handleChanged = () => {
    void serversQuery.refresh()
    void groupsQuery.refresh()
  }

  const runBatch = async (title: string, action: BatchAction, params?: Record<string, unknown>) => {
    const targets = selectedServers
    if (targets.length === 0) return
    setBatchNames(Object.fromEntries(targets.map((server) => [server.id, server.name])))
    setBatch({ open: true, title, loading: true, error: null, response: null })
    try {
      const response = await api.batch({ ids: targets.map((server) => server.id), action, params })
      setBatch((current) => (current ? { ...current, loading: false, response } : current))
    } catch (caught) {
      setBatch((current) =>
        current
          ? { ...current, loading: false, error: errorMessage(caught, t('s.0992')) }
          : current,
      )
    } finally {
      if (action === 'delete') setSelected(new Set())
      handleChanged()
    }
  }

  const handleSaveTags = async (tags: string[]) => {
    if (!tagEditing) return
    setTagSaving(true)
    setTagError(null)
    try {
      await api.updateServer(tagEditing.id, { tags })
      toast(t('s.0993', { '0': tagEditing.name }), 'success')
      setTagEditing(null)
      handleChanged()
    } catch (caught) {
      setTagError(errorMessage(caught, t('s.0994')))
    } finally {
      setTagSaving(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            {t('servers.title')}
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{t('servers.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={refreshAll}
            loading={refreshing}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('s.0689')}
          </Button>
          {can('enroll.manage') ? (
            <Button
              variant="secondary"
              onClick={() => setLocalOpen(true)}
              icon={<ServerIcon className="h-4 w-4" />}
            >
              {t('localServer.entry')}
            </Button>
          ) : null}
          {can('server.write') ? (
            <Button onClick={() => setWizardOpen(true)} icon={<PlusIcon className="h-4 w-4" />}>
              {t('s.0029')}
            </Button>
          ) : null}
        </div>
      </div>

      {serversQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>
            {t('s.0998')}
            {serversQuery.error}
            {servers.length > 0 ? t('s.0999') : ''}
          </span>
          <Button size="sm" variant="secondary" onClick={refreshAll}>
            {t('s.0053')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
                {t('s.0087')}
              </h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {visibleServers.length}
                {hasFilters ? t('s.1024') : ''}
              </span>
            </div>
            <ServerFiltersBar
              filters={filters}
              groups={groupsQuery.data ?? []}
              tags={tagOptions}
              onChange={setFilters}
              serverGroups={serverGroupsQuery.data ?? []}
              groupId={groupId}
              onGroupIdChange={setGroupId}
            />
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {serversQuery.updatedAt
              ? t('s.0949', { '0': formatClockTime(serversQuery.updatedAt) })
              : t('s.0425')}
          </p>
        </div>

        {can('server.write') ? (
          <BatchActionBar
            selected={selectedServers}
            pending={batch?.loading ?? false}
            groupOptions={groupOptions}
            onClear={() => setSelected(new Set())}
            onTest={() => void runBatch(t('s.0281'), 'test-connection')}
            onRefreshInfo={() => void runBatch(t('s.0282'), 'refresh-host-info')}
            onAddTags={(tags) => {
              if (tags.length === 0) {
                toast(t('s.1025'), 'error')
                return
              }
              void runBatch(t('s.0547'), 'add-tags', { tags })
            }}
            onSetGroup={(group) => void runBatch(t('s.1026'), 'set-group', { group })}
            onDelete={() => void runBatch(t('s.0513'), 'delete')}
          />
        ) : null}

        {serversQuery.loading && serversQuery.data === null ? (
          <InlineLoading label={t('s.1027')} />
        ) : visibleServers.length === 0 ? (
          hasFilters ? (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title={t('s.1028')}
              description={t('s.1029')}
              actionLabel={t('s.0525')}
              onAction={() => {
                setFilters({})
                setGroupId('')
              }}
            />
          ) : (
            <EmptyState
              icon={<ServerIcon className="h-6 w-6" />}
              title={t('s.1030')}
              description={t('s.1031')}
              actionLabel={t('s.1032')}
              onAction={() => setWizardOpen(true)}
            />
          )
        ) : (
          <ServerTable
            canEdit={can('server.write')}
            canDelete={can('server.delete')}
            servers={visibleServers}
            now={now}
            selected={selected}
            onToggle={(id) =>
              setSelected((previous) => {
                const next = new Set(previous)
                if (next.has(id)) next.delete(id)
                else next.add(id)
                return next
              })
            }
            onToggleAll={(checked) =>
              setSelected(checked ? new Set(visibleServers.map((server) => server.id)) : new Set())
            }
            onEdit={(server) => setRenaming(server)}
            onDelete={(server) => setDeleting(server)}
            onEditTags={(server) => {
              setTagError(null)
              setTagEditing(server)
            }}
          />
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">{t('s.1033')}</p>

      <AddServerWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onFinished={handleChanged}
        onUseLocal={() => {
          setWizardOpen(false)
          setLocalOpen(true)
        }}
      />
      <LocalServerDialog
        open={localOpen}
        onClose={() => setLocalOpen(false)}
        onChanged={handleChanged}
      />
      <EditServerDialog server={renaming} onClose={() => setRenaming(null)} onSaved={handleChanged} />
      <DeleteServerDialog
        server={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={handleChanged}
      />
      <TagsDialog
        open={tagEditing !== null}
        title={t('s.0544')}
        description={tagEditing ? t('s.1034', { '0': tagEditing.name }) : undefined}
        initialTags={tagEditing?.tags ?? []}
        loading={tagSaving}
        error={tagError}
        onClose={() => {
          setTagEditing(null)
          setTagError(null)
        }}
        onSubmit={(tags) => void handleSaveTags(tags)}
      />
      <BatchResultDialog
        open={batch?.open ?? false}
        title={batch?.title ?? ''}
        loading={batch?.loading ?? false}
        error={batch?.error ?? null}
        response={batch?.response ?? null}
        nameOf={(id) => batchNames[id] ?? id}
        onClose={() => setBatch(null)}
      />
    </div>
  )
}
