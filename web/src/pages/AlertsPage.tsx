import { Fragment, useEffect, useMemo, useState } from 'react'
import {
  api,
  type AlertChannel,
  type AlertFilters,
  type AlertItem,
  type AlertRule,
  type AlertSeverity,
  type AlertStatus,
} from '../lib/api'
import { useNow, usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { formatDateTime, formatDuration, formatRelativeTime } from '../lib/format'
import { AlertChannelDialog } from '../components/AlertChannelDialog'
import { AlertDetailPanel } from '../components/AlertDetailPanel'
import { AlertRuleDialog } from '../components/AlertRuleDialog'
import {
  AlertSourceBadge,
  AlertStatusBadge,
  ChannelKindBadge,
  SeverityBadge,
} from '../components/AlertBadges'
import { Button, IconButton } from '../components/Button'
import { ChannelTestDialog } from '../components/ChannelTestDialog'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import { EnableSwitch } from '../components/EnableSwitch'
import {
  AlertTriangleIcon,
  BellIcon,
  BellOffIcon,
  CheckIcon,
  ChevronRightIcon,
  HistoryIcon,
  PencilIcon,
  PlusIcon,
  RefreshIcon,
  SendIcon,
  TrashIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { useToast } from '../components/Toast'

type Tab = 'alerts' | 'rules' | 'channels'

const RANGES = [
  { key: '1h', label: '近 1 小时', ms: 3_600_000 },
  { key: '24h', label: '近 24 小时', ms: 86_400_000 },
  { key: '7d', label: '近 7 天', ms: 604_800_000 },
  { key: 'all', label: '全部时间', ms: 0 },
] as const

type RangeKey = (typeof RANGES)[number]['key']

const SILENCE_OPTIONS = [
  { value: 1_800, label: '30 分钟' },
  { value: 7_200, label: '2 小时' },
  { value: 86_400, label: '1 天' },
]

const selectClass =
  'h-9 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

interface AlertDetailState {
  loading: boolean
  error: string | null
  alert: AlertItem | null
}

export default function AlertsPage() {
  const { toast } = useToast()
  const now = useNow(1_000)

  const [tab, setTab] = useState<Tab>('alerts')
  const summaryQuery = usePolling(api.alertsSummary, 10_000)
  const rulesQuery = usePolling(api.alertRules, 10_000)
  const channelsQuery = usePolling(api.alertChannels, 10_000)
  const serversQuery = usePolling(api.servers, 30_000)
  const checksQuery = usePolling(api.checks, 30_000)
  const groupsQuery = usePolling(api.groups, 30_000)

  const [statusFilter, setStatusFilter] = useState<AlertStatus | ''>('')
  const [severityFilter, setSeverityFilter] = useState<AlertSeverity | ''>('')
  const [ruleFilter, setRuleFilter] = useState('')
  const [serverFilter, setServerFilter] = useState('')
  const [rangeKey, setRangeKey] = useState<RangeKey>('24h')

  const alertFilters = useMemo<AlertFilters>(
    () => ({
      status: statusFilter || undefined,
      severity: severityFilter || undefined,
      rule_id: ruleFilter || undefined,
      server_id: serverFilter || undefined,
      limit: 200,
    }),
    [statusFilter, severityFilter, ruleFilter, serverFilter],
  )

  const alertFilterKey = JSON.stringify({ alertFilters, rangeKey })
  const alertsQuery = usePolling(
    () => {
      const range = RANGES.find((item) => item.key === rangeKey) ?? RANGES[1]
      return api.alerts({
        ...alertFilters,
        from: range.ms > 0 ? Date.now() - range.ms : undefined,
      })
    },
    10_000,
    tab === 'alerts',
    alertFilterKey,
  )

  const alerts = alertsQuery.data ?? []
  const rules = rulesQuery.data ?? []
  const channels = channelsQuery.data ?? []
  const servers = serversQuery.data ?? []
  const summary = summaryQuery.data

  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [details, setDetails] = useState<Record<string, AlertDetailState>>({})
  const [ackingId, setAckingId] = useState<string | null>(null)

  const [ruleDialogOpen, setRuleDialogOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<AlertRule | null>(null)
  const [deletingRule, setDeletingRule] = useState<AlertRule | null>(null)
  const [deleteRuleLoading, setDeleteRuleLoading] = useState(false)
  const [deleteRuleError, setDeleteRuleError] = useState<string | null>(null)
  const [silenceRule, setSilenceRule] = useState<AlertRule | null>(null)
  const [silenceLoading, setSilenceLoading] = useState(false)
  const [togglingRuleId, setTogglingRuleId] = useState<string | null>(null)

  const [channelDialogOpen, setChannelDialogOpen] = useState(false)
  const [editingChannel, setEditingChannel] = useState<AlertChannel | null>(null)
  const [testingChannel, setTestingChannel] = useState<AlertChannel | null>(null)
  const [deletingChannel, setDeletingChannel] = useState<AlertChannel | null>(null)
  const [deleteChannelLoading, setDeleteChannelLoading] = useState(false)
  const [deleteChannelError, setDeleteChannelError] = useState<string | null>(null)
  const [togglingChannelId, setTogglingChannelId] = useState<string | null>(null)

  const [refreshing, setRefreshing] = useState(false)

  const ruleNameOf = useMemo(() => {
    const names = new Map(rules.map((rule) => [rule.id, rule.name]))
    return (id: string) => names.get(id) ?? id
  }, [rules])

  const serverNameOf = useMemo(() => {
    const names = new Map(servers.map((server) => [server.id, server.name]))
    return (id: string) => names.get(id) ?? id
  }, [servers])

  const checkNameOf = useMemo(() => {
    const names = new Map((checksQuery.data ?? []).map((check) => [check.id, check.name]))
    return (id: string) => names.get(id) ?? id
  }, [checksQuery.data])

  const refreshAll = async () => {
    setRefreshing(true)
    try {
      await Promise.all([
        summaryQuery.refresh(),
        rulesQuery.refresh(),
        channelsQuery.refresh(),
        alertsQuery.refresh(),
      ])
    } finally {
      setRefreshing(false)
    }
  }

  const loadDetail = async (alertId: string) => {
    setDetails((current) => ({
      ...current,
      [alertId]: { loading: true, error: null, alert: current[alertId]?.alert ?? null },
    }))
    try {
      const detail = await api.alert(alertId)
      setDetails((current) => ({ ...current, [alertId]: { loading: false, error: null, alert: detail } }))
    } catch (caught) {
      setDetails((current) => ({
        ...current,
        [alertId]: {
          loading: false,
          error: errorMessage(caught, '加载详情失败'),
          alert: current[alertId]?.alert ?? null,
        },
      }))
    }
  }

  const toggleExpand = (alert: AlertItem) => {
    if (expandedId === alert.id) {
      setExpandedId(null)
      return
    }
    setExpandedId(alert.id)
    // 状态可能随时翻转（firing→acked→resolved），每次展开都重新拉一次轨迹，避免看到过期详情
    void loadDetail(alert.id)
  }

  const handleAck = async (alert: AlertItem) => {
    setAckingId(alert.id)
    try {
      await api.ackAlert(alert.id)
      toast(`告警「${alert.title}」已确认`, 'success')
      await refreshAll()
      if (details[alert.id]) void loadDetail(alert.id)
    } catch (caught) {
      toast(errorMessage(caught, '确认失败'), 'error')
    } finally {
      setAckingId(null)
    }
  }

  const handleToggleRule = async (rule: AlertRule) => {
    setTogglingRuleId(rule.id)
    try {
      await api.updateAlertRule(rule.id, { enabled: !rule.enabled })
      toast(`规则「${rule.name}」已${rule.enabled ? '停用' : '启用'}`, 'success')
      await refreshAll()
    } catch (caught) {
      toast(errorMessage(caught, '切换规则状态失败'), 'error')
    } finally {
      setTogglingRuleId(null)
    }
  }

  const handleDeleteRule = async () => {
    if (!deletingRule) return
    setDeleteRuleLoading(true)
    setDeleteRuleError(null)
    try {
      await api.deleteAlertRule(deletingRule.id)
      toast(`已删除规则「${deletingRule.name}」`, 'success')
      setDeletingRule(null)
      await refreshAll()
    } catch (caught) {
      setDeleteRuleError(errorMessage(caught, '删除规则失败'))
    } finally {
      setDeleteRuleLoading(false)
    }
  }

  const handleSilence = async (durationS: number) => {
    if (!silenceRule) return
    setSilenceLoading(true)
    try {
      await api.silenceAlertRule(silenceRule.id, durationS)
      toast(`规则「${silenceRule.name}」已静默 ${formatDuration(durationS)}`, 'success')
      setSilenceRule(null)
      await refreshAll()
    } catch (caught) {
      toast(errorMessage(caught, '静默失败'), 'error')
    } finally {
      setSilenceLoading(false)
    }
  }

  const handleToggleChannel = async (channel: AlertChannel) => {
    setTogglingChannelId(channel.id)
    try {
      await api.updateAlertChannel(channel.id, { enabled: !channel.enabled })
      toast(`渠道「${channel.name}」已${channel.enabled ? '停用' : '启用'}`, 'success')
      await refreshAll()
    } catch (caught) {
      toast(errorMessage(caught, '切换渠道状态失败'), 'error')
    } finally {
      setTogglingChannelId(null)
    }
  }

  const handleDeleteChannel = async () => {
    if (!deletingChannel) return
    setDeleteChannelLoading(true)
    setDeleteChannelError(null)
    try {
      await api.deleteAlertChannel(deletingChannel.id)
      toast(`已删除渠道「${deletingChannel.name}」`, 'success')
      setDeletingChannel(null)
      await refreshAll()
    } catch (caught) {
      setDeleteChannelError(errorMessage(caught, '删除渠道失败'))
    } finally {
      setDeleteChannelLoading(false)
    }
  }

  const ruleScope = (rule: AlertRule): string => {
    if (rule.source === 'metric') {
      return `${rule.metric ?? '—'} ${rule.comparator ?? ''} ${rule.threshold ?? ''}`.trim()
    }
    if (rule.source === 'check') {
      if (rule.check_ids.length === 0) return '全部检测项'
      const names = rule.check_ids.slice(0, 2).map(checkNameOf)
      return `检测项：${names.join('、')}${rule.check_ids.length > 2 ? ` 等 ${rule.check_ids.length} 项` : ''}`
    }
    const parts: string[] = []
    if (rule.server_ids.length > 0) parts.push(`${rule.server_ids.length} 台服务器`)
    if (rule.groups.length > 0) parts.push(`分组：${rule.groups.join('、')}`)
    if (rule.tags.length > 0) parts.push(`标签：${rule.tags.join('、')}`)
    return parts.length > 0 ? parts.join(' · ') : '全部服务器'
  }

  const hasAlertFilters = Boolean(
    statusFilter || severityFilter || ruleFilter || serverFilter || rangeKey !== '24h',
  )

  // 轮询后收起已不在当前列表中的展开项
  useEffect(() => {
    if (expandedId && !alerts.some((alert) => alert.id === expandedId)) setExpandedId(null)
  }, [alerts, expandedId])

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            告警
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            规则触发、通知、确认与恢复的完整轨迹，每 10 秒自动刷新
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={refreshAll}
            loading={refreshing}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            刷新
          </Button>
          {tab === 'rules' ? (
            <Button
              icon={<PlusIcon className="h-4 w-4" />}
              onClick={() => {
                setEditingRule(null)
                setRuleDialogOpen(true)
              }}
            >
              新建规则
            </Button>
          ) : null}
          {tab === 'channels' ? (
            <Button
              icon={<PlusIcon className="h-4 w-4" />}
              onClick={() => {
                setEditingChannel(null)
                setChannelDialogOpen(true)
              }}
            >
              新建渠道
            </Button>
          ) : null}
        </div>
      </div>

      {summary && summary.firing > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-300 bg-rose-50 px-4 py-3 text-sm text-rose-800 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200">
          <span className="inline-flex items-center gap-2 font-medium">
            <AlertTriangleIcon className="h-4 w-4" />
            当前有 {summary.firing} 条告警处于进行中（firing），请及时处理
          </span>
          <button
            type="button"
            onClick={() => {
              setTab('alerts')
              setStatusFilter('firing')
            }}
            className="text-xs font-medium underline-offset-2 hover:underline"
          >
            只看进行中
          </button>
        </div>
      ) : null}

      <div className="flex items-center gap-1 border-b border-slate-200 dark:border-slate-800">
        {(
          [
            { key: 'alerts' as const, label: '告警列表', count: summary?.firing ?? alerts.length },
            { key: 'rules' as const, label: '规则', count: rules.length },
            { key: 'channels' as const, label: '渠道', count: channels.length },
          ]
        ).map((option) => (
          <button
            key={option.key}
            type="button"
            onClick={() => setTab(option.key)}
            className={cn(
              '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors',
              tab === option.key
                ? 'border-indigo-600 text-indigo-700 dark:border-indigo-400 dark:text-indigo-300'
                : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
            )}
          >
            {option.label}
            <span className="rounded-full bg-slate-100 px-1.5 py-0.5 text-[11px] tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {option.count}
            </span>
          </button>
        ))}
      </div>

      {tab === 'alerts' ? (
        <section className="space-y-4">
          {summary ? (
            <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
              {[
                { label: '进行中 (firing)', value: summary.firing, tone: 'text-rose-600 dark:text-rose-300' },
                { label: '已确认 (acked)', value: summary.acked, tone: 'text-blue-600 dark:text-blue-300' },
                { label: '24h 已恢复', value: summary.resolved_24h, tone: 'text-emerald-600 dark:text-emerald-300' },
                {
                  label: '按级别 严重/警告/提示',
                  value: `${summary.by_severity.critical}/${summary.by_severity.warning}/${summary.by_severity.info}`,
                  tone: 'text-slate-700 dark:text-slate-200',
                },
              ].map((card) => (
                <div
                  key={card.label}
                  className="rounded-xl border border-slate-200 bg-white p-3.5 shadow-sm dark:border-slate-800 dark:bg-slate-900"
                >
                  <span className="block text-xs text-slate-500 dark:text-slate-400">{card.label}</span>
                  <span className={cn('mt-1 block text-xl font-semibold tabular-nums', card.tone)}>
                    {card.value}
                  </span>
                </div>
              ))}
            </div>
          ) : null}

          <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">告警列表</h2>
                <select
                  aria-label="按状态筛选告警"
                  value={statusFilter}
                  onChange={(event) => setStatusFilter(event.target.value as AlertStatus | '')}
                  className={selectClass}
                >
                  <option value="">全部状态</option>
                  <option value="firing">进行中 (firing)</option>
                  <option value="acked">已确认 (acked)</option>
                  <option value="resolved">已恢复 (resolved)</option>
                </select>
                <select
                  aria-label="按级别筛选告警"
                  value={severityFilter}
                  onChange={(event) => setSeverityFilter(event.target.value as AlertSeverity | '')}
                  className={selectClass}
                >
                  <option value="">全部级别</option>
                  <option value="critical">严重 (critical)</option>
                  <option value="warning">警告 (warning)</option>
                  <option value="info">提示 (info)</option>
                </select>
                <select
                  aria-label="按规则筛选告警"
                  value={ruleFilter}
                  onChange={(event) => setRuleFilter(event.target.value)}
                  className={selectClass}
                >
                  <option value="">全部规则</option>
                  {rules.map((rule) => (
                    <option key={rule.id} value={rule.id}>
                      {rule.name}
                    </option>
                  ))}
                </select>
                <select
                  aria-label="按服务器筛选告警"
                  value={serverFilter}
                  onChange={(event) => setServerFilter(event.target.value)}
                  className={selectClass}
                >
                  <option value="">全部服务器</option>
                  {servers.map((server) => (
                    <option key={server.id} value={server.id}>
                      {server.name}
                    </option>
                  ))}
                </select>
                <select
                  aria-label="按时间范围筛选告警"
                  value={rangeKey}
                  onChange={(event) => setRangeKey(event.target.value as RangeKey)}
                  className={selectClass}
                >
                  {RANGES.map((range) => (
                    <option key={range.key} value={range.key}>
                      {range.label}
                    </option>
                  ))}
                </select>
                {hasAlertFilters ? (
                  <button
                    type="button"
                    onClick={() => {
                      setStatusFilter('')
                      setSeverityFilter('')
                      setRuleFilter('')
                      setServerFilter('')
                      setRangeKey('24h')
                    }}
                    className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
                  >
                    清除筛选
                  </button>
                ) : null}
              </div>
              <p className="text-xs text-slate-400 dark:text-slate-500">
                {alertsQuery.updatedAt
                  ? `共 ${alerts.length} 条 · 更新于 ${new Date(alertsQuery.updatedAt).toLocaleTimeString('zh-CN', { hour12: false })}`
                  : '加载中…'}
              </p>
            </div>

            {alertsQuery.error ? (
              <div className="flex flex-wrap items-center justify-between gap-2 border-b border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                <span>加载告警失败：{alertsQuery.error}</span>
                <Button size="sm" variant="secondary" onClick={() => void alertsQuery.refresh()}>
                  重试
                </Button>
              </div>
            ) : null}

            {alertsQuery.loading && alertsQuery.data === null ? (
              <InlineLoading label="正在加载告警…" />
            ) : alerts.length === 0 ? (
              <EmptyState
                icon={<BellIcon className="h-6 w-6" />}
                title="没有符合条件的告警"
                description={
                  hasAlertFilters
                    ? '当前筛选条件下没有告警，试试放宽时间范围或清除筛选'
                    : '目前一切正常：规则触发后会在这里出现，并记录触发、通知、确认与恢复轨迹'
                }
                actionLabel={hasAlertFilters ? '清除筛选' : undefined}
                onAction={
                  hasAlertFilters
                    ? () => {
                        setStatusFilter('')
                        setSeverityFilter('')
                        setRuleFilter('')
                        setServerFilter('')
                        setRangeKey('24h')
                      }
                    : undefined
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[1180px] border-collapse text-sm">
                  <thead>
                    <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                      <th scope="col" className="w-8 px-2 py-2.5" />
                      <th scope="col" className="px-4 py-2.5 font-medium">状态</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">级别</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">规则</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">服务器</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">标题 / 信息</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">开始时间</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">持续</th>
                      <th scope="col" className="px-4 py-2.5 font-medium">通知次数</th>
                      <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                    {alerts.map((alert) => {
                      const expanded = expandedId === alert.id
                      const detail = details[alert.id]
                      const durationEnd = alert.resolved_at ?? now
                      const durationS =
                        alert.started_at === null ? null : (durationEnd - alert.started_at) / 1000
                      return (
                        <Fragment key={alert.id}>
                          <tr
                            className={cn(
                              'cursor-pointer transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60',
                              expanded && 'bg-slate-50/80 dark:bg-slate-900/60',
                            )}
                            onClick={() => toggleExpand(alert)}
                          >
                            <td className="px-2 py-3 align-top">
                              <span
                                className={cn(
                                  'inline-flex h-5 w-5 items-center justify-center rounded text-slate-400 transition-transform',
                                  expanded && 'rotate-90 text-indigo-500',
                                )}
                                aria-hidden="true"
                              >
                                <ChevronRightIcon className="h-4 w-4" />
                              </span>
                            </td>
                            <td className="px-4 py-3 align-top">
                              <AlertStatusBadge status={alert.status} />
                            </td>
                            <td className="px-4 py-3 align-top">
                              <SeverityBadge severity={alert.severity} />
                            </td>
                            <td className="max-w-[180px] px-4 py-3 align-top">
                              <span className="block truncate text-xs text-slate-700 dark:text-slate-200">
                                {alert.rule_name ?? ruleNameOf(alert.rule_id)}
                              </span>
                            </td>
                            <td className="max-w-[160px] px-4 py-3 align-top">
                              <span className="block truncate text-xs text-slate-700 dark:text-slate-200">
                                {alert.server_name ??
                                  (alert.server_id ? serverNameOf(alert.server_id) : '—')}
                              </span>
                              {alert.check_id ? (
                                <span className="mt-0.5 block truncate text-[11px] text-slate-400 dark:text-slate-500">
                                  {checkNameOf(alert.check_id)}
                                </span>
                              ) : null}
                            </td>
                            <td className="max-w-[280px] px-4 py-3 align-top">
                              <span className="block truncate font-medium text-slate-900 dark:text-slate-100">
                                {alert.title}
                              </span>
                              {alert.message ? (
                                <span
                                  className="mt-0.5 block truncate text-[11px] text-slate-500 dark:text-slate-400"
                                  title={alert.message}
                                >
                                  {alert.message}
                                </span>
                              ) : null}
                            </td>
                            <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                              {formatRelativeTime(alert.started_at, now)}
                              <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                                {formatDateTime(alert.started_at)}
                              </span>
                            </td>
                            <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                              {durationS === null ? '—' : formatDuration(Math.round(durationS))}
                            </td>
                            <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                              {alert.notify_count}
                              {alert.last_notify_at ? (
                                <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                                  最近 {formatRelativeTime(alert.last_notify_at, now)}
                                </span>
                              ) : null}
                            </td>
                            <td className="px-4 py-3 align-top">
                              <div
                                className="flex items-center justify-end gap-1"
                                onClick={(event) => event.stopPropagation()}
                              >
                                <Button
                                  size="sm"
                                  variant="secondary"
                                  disabled={alert.status !== 'firing'}
                                  loading={ackingId === alert.id}
                                  onClick={() => void handleAck(alert)}
                                  icon={<CheckIcon className="h-3.5 w-3.5" />}
                                >
                                  {alert.status === 'firing' ? '确认' : alert.status === 'acked' ? '已确认' : '已恢复'}
                                </Button>
                              </div>
                            </td>
                          </tr>
                          {expanded ? (
                            <tr>
                              <td colSpan={10} className="p-0">
                                <AlertDetailPanel
                                  alert={detail?.alert ?? null}
                                  loading={detail?.loading ?? false}
                                  error={detail?.error ?? null}
                                  ruleNameOf={ruleNameOf}
                                  serverNameOf={serverNameOf}
                                />
                              </td>
                            </tr>
                          ) : null}
                        </Fragment>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </section>
      ) : null}

      {tab === 'rules' ? (
        <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">告警规则</h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {rules.length}
              </span>
            </div>
            {rulesQuery.error ? (
              <span className="text-xs text-rose-600 dark:text-rose-400">
                加载失败：{rulesQuery.error}
              </span>
            ) : (
              <p className="text-xs text-slate-400 dark:text-slate-500">
                持续时长控制「多久才告警」，去重窗口控制「多久内不重复打扰」
              </p>
            )}
          </div>

          {rulesQuery.loading && rulesQuery.data === null ? (
            <InlineLoading label="正在加载规则…" />
          ) : rules.length === 0 ? (
            <EmptyState
              icon={<BellOffIcon className="h-6 w-6" />}
              title="还没有告警规则"
              description="创建规则后，检测失败、服务器离线或指标越界都会按规则推送到通知渠道"
              actionLabel="新建规则"
              onAction={() => {
                setEditingRule(null)
                setRuleDialogOpen(true)
              }}
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[1080px] border-collapse text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                    <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">来源</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">适用范围</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">持续时长</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">去重窗口</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">级别</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">渠道</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">静默</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">启用</th>
                    <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                  {rules.map((rule) => {
                    const silenced = rule.silence_until !== null && rule.silence_until > now
                    return (
                      <tr
                        key={rule.id}
                        className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                      >
                        <td className="max-w-[200px] px-4 py-3 align-top">
                          <button
                            type="button"
                            onClick={() => {
                              setEditingRule(rule)
                              setRuleDialogOpen(true)
                            }}
                            className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                          >
                            {rule.name}
                          </button>
                        </td>
                        <td className="px-4 py-3 align-top">
                          <AlertSourceBadge source={rule.source} />
                        </td>
                        <td className="max-w-[220px] px-4 py-3 align-top">
                          <span
                            className="block truncate text-xs text-slate-600 dark:text-slate-300"
                            title={ruleScope(rule)}
                          >
                            {ruleScope(rule)}
                          </span>
                        </td>
                        <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                          {formatDuration(rule.for_duration_s)}
                        </td>
                        <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                          {formatDuration(rule.dedup_window_s)}
                        </td>
                        <td className="px-4 py-3 align-top">
                          <SeverityBadge severity={rule.severity} />
                        </td>
                        <td className="px-4 py-3 align-top text-xs tabular-nums text-slate-600 dark:text-slate-300">
                          {rule.channel_ids.length}
                          {rule.channel_ids.length === 0 ? (
                            <span className="ml-1 text-amber-600 dark:text-amber-400">未配置</span>
                          ) : null}
                        </td>
                        <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                          {silenced ? (
                            <span className="text-slate-700 dark:text-slate-200">
                              静默至 {formatDateTime(rule.silence_until)}
                            </span>
                          ) : (
                            '未静默'
                          )}
                        </td>
                        <td className="px-4 py-3 align-top">
                          <EnableSwitch
                            checked={rule.enabled}
                            loading={togglingRuleId === rule.id}
                            label={rule.enabled ? '停用该规则' : '启用该规则'}
                            onChange={() => void handleToggleRule(rule)}
                          />
                        </td>
                        <td className="px-4 py-3 align-top">
                          <div className="flex items-center justify-end gap-0.5">
                            <IconButton
                              label="临时静默"
                              onClick={() => setSilenceRule(rule)}
                            >
                              <BellOffIcon className="h-4 w-4" />
                            </IconButton>
                            <IconButton
                              label="编辑规则"
                              onClick={() => {
                                setEditingRule(rule)
                                setRuleDialogOpen(true)
                              }}
                            >
                              <PencilIcon className="h-4 w-4" />
                            </IconButton>
                            <IconButton
                              label="删除规则"
                              tone="danger"
                              onClick={() => {
                                setDeleteRuleError(null)
                                setDeletingRule(rule)
                              }}
                            >
                              <TrashIcon className="h-4 w-4" />
                            </IconButton>
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      ) : null}

      {tab === 'channels' ? (
        <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">通知渠道</h2>
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {channels.length}
              </span>
            </div>
            <p className="text-xs text-slate-400 dark:text-slate-500">
              密钥字段后端脱敏返回，编辑时留空表示不修改
            </p>
          </div>

          {channelsQuery.loading && channelsQuery.data === null ? (
            <InlineLoading label="正在加载渠道…" />
          ) : channels.length === 0 ? (
            <EmptyState
              icon={<SendIcon className="h-6 w-6" />}
              title="还没有通知渠道"
              description="创建 Webhook / 钉钉 / 企微 / 飞书 / Slack / Telegram / 邮件渠道，告警才能推送给人"
              actionLabel="新建渠道"
              onAction={() => {
                setEditingChannel(null)
                setChannelDialogOpen(true)
              }}
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[960px] border-collapse text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                    <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">类型</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">最近结果</th>
                    <th scope="col" className="px-4 py-2.5 font-medium">启用</th>
                    <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                  {channels.map((channel) => (
                    <tr
                      key={channel.id}
                      className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                    >
                      <td className="max-w-[200px] px-4 py-3 align-top">
                        <button
                          type="button"
                          onClick={() => {
                            setEditingChannel(channel)
                            setChannelDialogOpen(true)
                          }}
                          className="truncate font-medium text-slate-900 hover:text-indigo-600 dark:text-slate-100 dark:hover:text-indigo-400"
                        >
                          {channel.name}
                        </button>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <ChannelKindBadge kind={channel.kind} />
                      </td>
                      <td className="max-w-[360px] px-4 py-3 align-top">
                        {channel.last_error ? (
                          <span className="block">
                            <span className="text-xs font-medium text-rose-600 dark:text-rose-400">
                              最近发送失败
                            </span>
                            <span
                              className="mt-0.5 block truncate font-mono text-[11px] text-rose-600/90 dark:text-rose-300/90"
                              title={channel.last_error}
                            >
                              {channel.last_error}
                            </span>
                          </span>
                        ) : channel.last_ok_at ? (
                          <span className="text-xs text-emerald-600 dark:text-emerald-400">
                            最近成功 {formatRelativeTime(channel.last_ok_at, now)}
                            <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                              {formatDateTime(channel.last_ok_at)}
                            </span>
                          </span>
                        ) : (
                          <span className="text-xs text-slate-400 dark:text-slate-500">
                            尚未发送过，建议先点「测试发送」
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3 align-top">
                        <EnableSwitch
                          checked={channel.enabled}
                          loading={togglingChannelId === channel.id}
                          label={channel.enabled ? '停用该渠道' : '启用该渠道'}
                          onChange={() => void handleToggleChannel(channel)}
                        />
                      </td>
                      <td className="px-4 py-3 align-top">
                        <div className="flex items-center justify-end gap-0.5">
                          <IconButton label="测试发送" onClick={() => setTestingChannel(channel)}>
                            <SendIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label="编辑渠道"
                            onClick={() => {
                              setEditingChannel(channel)
                              setChannelDialogOpen(true)
                            }}
                          >
                            <PencilIcon className="h-4 w-4" />
                          </IconButton>
                          <IconButton
                            label="删除渠道"
                            tone="danger"
                            onClick={() => {
                              setDeleteChannelError(null)
                              setDeletingChannel(channel)
                            }}
                          >
                            <TrashIcon className="h-4 w-4" />
                          </IconButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      ) : null}

      <p className="flex items-center gap-1.5 text-xs text-slate-400 dark:text-slate-500">
        <HistoryIcon className="h-3.5 w-3.5" />
        点击任意告警行可展开事件轨迹：何时触发、通知了哪些渠道、何时恢复、谁在何时确认。
      </p>

      <AlertRuleDialog
        open={ruleDialogOpen}
        rule={editingRule}
        checks={checksQuery.data ?? []}
        servers={servers}
        groups={groupsQuery.data ?? []}
        channels={channels}
        onClose={() => {
          setRuleDialogOpen(false)
          setEditingRule(null)
        }}
        onSaved={() => void refreshAll()}
      />

      <AlertChannelDialog
        open={channelDialogOpen}
        channel={editingChannel}
        onClose={() => {
          setChannelDialogOpen(false)
          setEditingChannel(null)
        }}
        onSaved={() => void refreshAll()}
      />

      <ChannelTestDialog
        channel={testingChannel}
        onClose={() => setTestingChannel(null)}
        onResult={() => void refreshAll()}
      />

      <Modal
        open={silenceRule !== null}
        onClose={() => setSilenceRule(null)}
        title="临时静默规则"
        description={
          silenceRule
            ? `规则：${silenceRule.name} · 静默期内仍会建告警与记录事件，但不发送通知`
            : undefined
        }
        footer={
          <Button variant="secondary" onClick={() => setSilenceRule(null)} disabled={silenceLoading}>
            取消
          </Button>
        }
      >
        <div className="space-y-3">
          <p className="text-sm text-slate-600 dark:text-slate-300">选择静默时长：</p>
          <div className="grid grid-cols-3 gap-2">
            {SILENCE_OPTIONS.map((option) => (
              <Button
                key={option.value}
                variant="secondary"
                loading={silenceLoading}
                onClick={() => void handleSilence(option.value)}
              >
                {option.label}
              </Button>
            ))}
          </div>
          {silenceRule?.silence_until && silenceRule.silence_until > now ? (
            <p className="text-xs text-slate-500 dark:text-slate-400">
              当前静默至 {formatDateTime(silenceRule.silence_until)}，再次选择时长会覆盖。
            </p>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={deletingRule !== null}
        title="删除告警规则"
        description={deletingRule ? `规则：${deletingRule.name}` : undefined}
        confirmLabel="确认删除"
        tone="danger"
        loading={deleteRuleLoading}
        error={deleteRuleError}
        onClose={() => {
          setDeletingRule(null)
          setDeleteRuleError(null)
        }}
        onConfirm={() => void handleDeleteRule()}
      >
        <p>
          确定要删除规则
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deletingRule?.name}」
          </span>
          吗？删除后不再产生新告警，历史告警仍会保留。
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={deletingChannel !== null}
        title="删除通知渠道"
        description={deletingChannel ? `渠道：${deletingChannel.name}` : undefined}
        confirmLabel="确认删除"
        tone="danger"
        loading={deleteChannelLoading}
        error={deleteChannelError}
        onClose={() => {
          setDeletingChannel(null)
          setDeleteChannelError(null)
        }}
        onConfirm={() => void handleDeleteChannel()}
      >
        <p>
          确定要删除渠道
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deletingChannel?.name}」
          </span>
          吗？引用该渠道的规则将不再能推送通知。
        </p>
      </ConfirmDialog>
    </div>
  )
}

