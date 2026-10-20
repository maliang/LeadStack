import { useEffect, useMemo, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import {
  api,
  type AlertChannel,
  type AlertRule,
  type AlertSeverity,
  type AlertSource,
  type CheckItem,
  type GroupSummary,
  type Server,
} from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { formatDuration, metricLabel } from '../lib/format'
import { Button } from './Button'
import { ChannelKindBadge } from './AlertBadges'
import { Modal } from './Modal'
import { CheckKindBadge } from './CheckBadges'
import { ModeBadge } from './StatusBadge'
import { useToast } from './Toast'

import { t } from '../i18n'
const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 disabled:cursor-not-allowed disabled:bg-slate-50 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:disabled:bg-slate-900'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const SOURCES: { value: AlertSource; label: string; hint: string }[] = [
  { value: 'check', label: t('s.0086'), hint: t('s.0179') },
  { value: 'server', label: t('s.0087'), hint: t('s.0180') },
  { value: 'metric', label: t('s.0088'), hint: t('s.0181') },
]

const SEVERITIES: { value: AlertSeverity; label: string }[] = [
  { value: 'critical', label: t('s.0082') },
  { value: 'warning', label: t('s.0083') },
  { value: 'info', label: t('s.0084') },
]

const METRIC_OPTIONS = [
  'cpu.pct',
  'mem.used_pct',
  'mem.used_bytes',
  'disk.used.pct',
  'load.1',
  'net.rx_bytes',
  'net.tx_bytes',
  'proc.count',
]

const FOR_PRESETS = [
  { value: 0, label: t('s.0182') },
  { value: 60, label: t('s.0183') },
  { value: 300, label: t('s.0184') },
  { value: 600, label: t('s.0185') },
]

const DEDUP_PRESETS = [
  { value: 300, label: t('s.0184') },
  { value: 900, label: t('s.0186') },
  { value: 3600, label: t('s.0001') },
]

interface AlertRuleDialogProps {
  open: boolean
  rule: AlertRule | null
  checks: CheckItem[]
  servers: Server[]
  groups: GroupSummary[]
  channels: AlertChannel[]
  onClose: () => void
  onSaved: () => void
}

export function AlertRuleDialog({
  open,
  rule,
  checks,
  servers,
  groups,
  channels,
  onClose,
  onSaved,
}: AlertRuleDialogProps) {
  const { toast } = useToast()
  const editing = rule !== null

  const [name, setName] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [source, setSource] = useState<AlertSource>('check')
  const [checkIds, setCheckIds] = useState<Set<string>>(new Set())
  const [serverIds, setServerIds] = useState<Set<string>>(new Set())
  const [pickedGroups, setPickedGroups] = useState<Set<string>>(new Set())
  const [pickedTags, setPickedTags] = useState<Set<string>>(new Set())
  const [metric, setMetric] = useState('cpu.pct')
  const [comparator, setComparator] = useState('>')
  const [threshold, setThreshold] = useState('80')
  const [forDuration, setForDuration] = useState('0')
  const [dedupWindow, setDedupWindow] = useState('300')
  const [severity, setSeverity] = useState<AlertSeverity>('warning')
  const [channelIds, setChannelIds] = useState<Set<string>>(new Set())
  const [serverSearch, setServerSearch] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(rule?.name ?? '')
    setEnabled(rule?.enabled ?? true)
    setSource(rule?.source ?? 'check')
    setCheckIds(new Set(rule?.check_ids ?? []))
    setServerIds(new Set(rule?.server_ids ?? []))
    setPickedGroups(new Set(rule?.groups ?? []))
    setPickedTags(new Set(rule?.tags ?? []))
    setMetric(rule?.metric ?? 'cpu.pct')
    setComparator(rule?.comparator ?? '>')
    setThreshold(String(rule?.threshold ?? 80))
    setForDuration(String(rule?.for_duration_s ?? 0))
    setDedupWindow(String(rule?.dedup_window_s ?? 300))
    setSeverity(rule?.severity ?? 'warning')
    setChannelIds(new Set(rule?.channel_ids ?? []))
    setServerSearch('')
    setSaving(false)
    setError(null)
  }, [open, rule])

  const tagOptions = useMemo(() => {
    const tags = new Set<string>()
    for (const server of servers) for (const tag of server.tags) tags.add(tag)
    return [...tags].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }, [servers])

  const filteredServers = useMemo(() => {
    const query = serverSearch.trim().toLowerCase()
    if (!query) return servers
    return servers.filter(
      (server) =>
        server.name.toLowerCase().includes(query) ||
        server.host.toLowerCase().includes(query) ||
        server.tags.some((tag) => tag.toLowerCase().includes(query)),
    )
  }, [servers, serverSearch])

  const toggle = (setter: Dispatch<SetStateAction<Set<string>>>, id: string) => {
    setter((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const validate = (): string | null => {
    if (!name.trim()) return t('s.0187')
    const forValue = Number(forDuration)
    if (!Number.isInteger(forValue) || forValue < 0) return t('s.0188')
    const dedupValue = Number(dedupWindow)
    if (!Number.isInteger(dedupValue) || dedupValue < 0) return t('s.0189')
    if (source === 'metric') {
      if (!metric.trim()) return t('s.0190')
      if (!['>', '>=', '<', '<='].includes(comparator)) return t('s.0191')
      const thresholdValue = Number(threshold)
      if (!Number.isFinite(thresholdValue)) return t('s.0192')
    }
    if (channelIds.size === 0) return t('s.0193')
    return null
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    setSaving(true)
    setError(null)
    const payload = {
      name: name.trim(),
      enabled,
      source,
      check_ids: source === 'check' ? [...checkIds] : [],
      server_ids: source === 'server' ? [...serverIds] : [],
      groups: source === 'server' ? [...pickedGroups] : [],
      tags: source === 'server' ? [...pickedTags] : [],
      metric: source === 'metric' ? metric.trim() : undefined,
      comparator: source === 'metric' ? comparator : undefined,
      threshold: source === 'metric' ? Number(threshold) : undefined,
      for_duration_s: Number(forDuration),
      severity,
      dedup_window_s: Number(dedupWindow),
      channel_ids: [...channelIds],
    }
    try {
      if (editing && rule) {
        await api.updateAlertRule(rule.id, payload)
        toast(t('s.0194', { '0': name.trim() }), 'success')
      } else {
        await api.createAlertRule(payload)
        toast(t('s.0195', { '0': name.trim() }), 'success')
      }
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0196')))
    } finally {
      setSaving(false)
    }
  }

  const scopeEmptyHint =
    source === 'check'
      ? t('s.0197')
      : source === 'server'
        ? t('s.0198')
        : ''

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('s.0199') : t('s.0200')}
      description={t('s.0201')}
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="alert-rule-form" loading={saving}>
            {editing ? t('s.0128') : t('s.0202')}
          </Button>
        </>
      }
    >
      <form id="alert-rule-form" onSubmit={handleSubmit} className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-2">
            <label htmlFor="rule-name" className={labelClass}>
              {t('s.0203')}
            </label>
            <input
              id="rule-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0204')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <label className="mt-6 flex items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => setEnabled(event.target.checked)}
              className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
            />
            {t('s.0205')}
          </label>
        </div>

        <div>
          <span className={labelClass}>{t('s.0206')}</span>
          <div className="mt-1.5 grid grid-cols-1 gap-2 sm:grid-cols-3">
            {SOURCES.map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => setSource(option.value)}
                className={cn(
                  'rounded-lg border px-3 py-2 text-left transition-colors',
                  source === option.value
                    ? 'border-indigo-500 bg-indigo-50/70 dark:border-indigo-400 dark:bg-indigo-500/10'
                    : 'border-slate-200 hover:border-slate-300 dark:border-slate-700 dark:hover:border-slate-600',
                )}
              >
                <span className="block text-sm font-medium text-slate-800 dark:text-slate-100">
                  {option.label}
                </span>
                <span className="mt-0.5 block text-[11px] leading-relaxed text-slate-500 dark:text-slate-400">
                  {option.hint}
                </span>
              </button>
            ))}
          </div>
        </div>

        {source === 'check' ? (
          <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className={labelClass}>
                {t('s.0207')}
                <span className="ml-1.5 font-normal text-slate-400">
                  {t('s.0208')} {checkIds.size}
                  {checks.length > 0 ? ` / ${checks.length}` : ''}
                </span>
              </span>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setCheckIds(new Set(checks.map((check) => check.id)))}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  {t('s.0209')}
                </button>
                <button
                  type="button"
                  onClick={() => setCheckIds(new Set())}
                  className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400"
                >
                  {t('s.0210')}
                </button>
              </div>
            </div>
            <div className="mt-2 max-h-48 overflow-y-auto rounded-md border border-slate-100 dark:border-slate-800/70">
              {checks.length === 0 ? (
                <p className="px-3 py-4 text-center text-xs text-slate-400 dark:text-slate-500">
                  {t('s.0211')}
                </p>
              ) : (
                checks.map((check) => (
                  <label
                    key={check.id}
                    className="flex cursor-pointer items-center gap-2.5 border-b border-slate-100 px-3 py-2 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/70 dark:hover:bg-slate-900/60"
                  >
                    <input
                      type="checkbox"
                      checked={checkIds.has(check.id)}
                      onChange={() => toggle(setCheckIds, check.id)}
                      className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                    />
                    <CheckKindBadge kind={check.kind} />
                    <span className="min-w-0 flex-1 truncate text-sm text-slate-800 dark:text-slate-100">
                      {check.name}
                    </span>
                    <span className="truncate font-mono text-[11px] text-slate-400 dark:text-slate-500">
                      {check.target}
                    </span>
                  </label>
                ))
              )}
            </div>
            <p className="mt-1.5 text-xs text-slate-400 dark:text-slate-500">{scopeEmptyHint}</p>
          </div>
        ) : null}

        {source === 'server' ? (
          <div className="space-y-3 rounded-lg border border-slate-200 p-3 dark:border-slate-800">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className={labelClass}>
                {t('s.0212')}
                <span className="ml-1.5 font-normal text-slate-400">{t('s.0208')} {serverIds.size}</span>
              </span>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() =>
                    setServerIds((current) => {
                      const next = new Set(current)
                      for (const server of filteredServers) next.add(server.id)
                      return next
                    })
                  }
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  {t('s.0213')}
                </button>
                <button
                  type="button"
                  onClick={() => setServerIds(new Set())}
                  className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400"
                >
                  {t('s.0210')}
                </button>
              </div>
            </div>
            <input
              value={serverSearch}
              onChange={(event) => setServerSearch(event.target.value)}
              placeholder={t('s.0214')}
              className={inputClass}
            />
            <div className="max-h-40 overflow-y-auto rounded-md border border-slate-100 dark:border-slate-800/70">
              {filteredServers.map((server) => (
                <label
                  key={server.id}
                  className="flex cursor-pointer items-center gap-2.5 border-b border-slate-100 px-3 py-2 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/70 dark:hover:bg-slate-900/60"
                >
                  <input
                    type="checkbox"
                    checked={serverIds.has(server.id)}
                    onChange={() => toggle(setServerIds, server.id)}
                    className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                  />
                  <span className="min-w-0 flex-1 truncate text-sm text-slate-800 dark:text-slate-100">
                    {server.name}
                  </span>
                  <ModeBadge mode={server.mode} />
                  <span className="font-mono text-[11px] text-slate-400 dark:text-slate-500">
                    {server.host}
                  </span>
                </label>
              ))}
            </div>

            <div>
              <p className="text-xs font-medium text-slate-600 dark:text-slate-300">{t('s.0215')}</p>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {groups.length === 0 ? (
                  <span className="text-xs text-slate-400 dark:text-slate-500">{t('s.0216')}</span>
                ) : (
                  groups.map((group) => (
                    <button
                      key={group.name}
                      type="button"
                      onClick={() => toggle(setPickedGroups, group.name)}
                      className={cn(
                        'rounded-full px-2 py-0.5 text-xs ring-1 ring-inset transition-colors',
                        pickedGroups.has(group.name)
                          ? 'bg-indigo-50 text-indigo-700 ring-indigo-500/40 dark:bg-indigo-500/15 dark:text-indigo-200'
                          : 'bg-white text-slate-600 ring-slate-300 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:ring-slate-700',
                      )}
                    >
                      {group.name}（{group.count}）
                    </button>
                  ))
                )}
              </div>
            </div>

            <div>
              <p className="text-xs font-medium text-slate-600 dark:text-slate-300">{t('s.0217')}</p>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {tagOptions.length === 0 ? (
                  <span className="text-xs text-slate-400 dark:text-slate-500">{t('s.0218')}</span>
                ) : (
                  tagOptions.map((tag) => (
                    <button
                      key={tag}
                      type="button"
                      onClick={() => toggle(setPickedTags, tag)}
                      className={cn(
                        'rounded-full px-2 py-0.5 text-xs ring-1 ring-inset transition-colors',
                        pickedTags.has(tag)
                          ? 'bg-indigo-50 text-indigo-700 ring-indigo-500/40 dark:bg-indigo-500/15 dark:text-indigo-200'
                          : 'bg-white text-slate-600 ring-slate-300 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:ring-slate-700',
                      )}
                    >
                      {tag}
                    </button>
                  ))
                )}
              </div>
            </div>
            <p className="text-xs text-slate-400 dark:text-slate-500">{scopeEmptyHint}</p>
          </div>
        ) : null}

        {source === 'metric' ? (
          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <label htmlFor="rule-metric" className={labelClass}>
                {t('s.0088')}
              </label>
              <input
                id="rule-metric"
                value={metric}
                onChange={(event) => setMetric(event.target.value)}
                list="rule-metric-options"
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <datalist id="rule-metric-options">
                {METRIC_OPTIONS.map((option) => (
                  <option key={option} value={option}>
                    {metricLabel(option)}
                  </option>
                ))}
              </datalist>
            </div>
            <div>
              <label htmlFor="rule-comparator" className={labelClass}>
                {t('s.0219')}
              </label>
              <select
                id="rule-comparator"
                value={comparator}
                onChange={(event) => setComparator(event.target.value)}
                className={inputClass + ' mt-1.5'}
              >
                {['>', '>=', '<', '<='].map((option) => (
                  <option key={option} value={option}>
                    {option}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="rule-threshold" className={labelClass}>
                {t('s.0220')}
              </label>
              <input
                id="rule-threshold"
                value={threshold}
                onChange={(event) => setThreshold(event.target.value)}
                inputMode="decimal"
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <p className="text-xs text-slate-400 sm:col-span-3 dark:text-slate-500">
              {t('s.0221')}
            </p>
          </div>
        ) : null}

        <div className="grid gap-4 sm:grid-cols-3">
          <div>
            <label htmlFor="rule-for-duration" className={labelClass}>
              {t('s.0222')}
            </label>
            <input
              id="rule-for-duration"
              value={forDuration}
              onChange={(event) => setForDuration(event.target.value)}
              inputMode="numeric"
              className={inputClass + ' mt-1.5'}
            />
            <p className="mt-1 text-xs leading-relaxed text-slate-400 dark:text-slate-500">
              {t('s.0223')}
            </p>
            <div className="mt-1.5 flex flex-wrap gap-1.5">
              {FOR_PRESETS.map((preset) => (
                <button
                  key={preset.value}
                  type="button"
                  onClick={() => setForDuration(String(preset.value))}
                  className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
                >
                  {preset.label}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label htmlFor="rule-dedup" className={labelClass}>
              {t('s.0224')}
            </label>
            <input
              id="rule-dedup"
              value={dedupWindow}
              onChange={(event) => setDedupWindow(event.target.value)}
              inputMode="numeric"
              className={inputClass + ' mt-1.5'}
            />
            <p className="mt-1 text-xs leading-relaxed text-slate-400 dark:text-slate-500">
              {t('s.0225')}
            </p>
            <div className="mt-1.5 flex flex-wrap gap-1.5">
              {DEDUP_PRESETS.map((preset) => (
                <button
                  key={preset.value}
                  type="button"
                  onClick={() => setDedupWindow(String(preset.value))}
                  className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
                >
                  {preset.label}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label htmlFor="rule-severity" className={labelClass}>
              {t('s.0226')}
            </label>
            <select
              id="rule-severity"
              value={severity}
              onChange={(event) => setSeverity(event.target.value as AlertSeverity)}
              className={inputClass + ' mt-1.5'}
            >
              {SEVERITIES.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}（{option.value}）
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
              {t('s.0227')} {formatDuration(Number(forDuration) || 0)}{t('s.0228')} {formatDuration(Number(dedupWindow) || 0)}{t('s.0229')}
            </p>
          </div>
        </div>

        <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className={labelClass}>
              {t('s.0230')}
              <span className="ml-1.5 font-normal text-slate-400">{t('s.0208')} {channelIds.size}</span>
            </span>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setChannelIds(new Set(channels.map((channel) => channel.id)))}
                className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
              >
                {t('s.0209')}
              </button>
              <button
                type="button"
                onClick={() => setChannelIds(new Set())}
                className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400"
              >
                {t('s.0210')}
              </button>
            </div>
          </div>
          <div className="mt-2 grid gap-1.5 sm:grid-cols-2">
            {channels.length === 0 ? (
              <p className="text-xs text-slate-400 sm:col-span-2 dark:text-slate-500">
                {t('s.0231')}
              </p>
            ) : (
              channels.map((channel) => (
                <label
                  key={channel.id}
                  className="flex cursor-pointer items-center gap-2 rounded-md border border-slate-100 px-2.5 py-1.5 hover:bg-slate-50 dark:border-slate-800/70 dark:hover:bg-slate-900/60"
                >
                  <input
                    type="checkbox"
                    checked={channelIds.has(channel.id)}
                    onChange={() => toggle(setChannelIds, channel.id)}
                    className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                  />
                  <span className="min-w-0 flex-1 truncate text-sm text-slate-800 dark:text-slate-100">
                    {channel.name}
                  </span>
                  <ChannelKindBadge kind={channel.kind} />
                  {!channel.enabled ? (
                    <span className="text-[11px] text-slate-400 dark:text-slate-500">{t('s.0232')}</span>
                  ) : null}
                </label>
              ))
            )}
          </div>
          <p className="mt-1.5 text-xs text-slate-400 dark:text-slate-500">
            {t('s.0233')}
          </p>
        </div>

        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </form>
    </Modal>
  )
}
