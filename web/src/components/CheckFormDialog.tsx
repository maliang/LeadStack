import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  api,
  type CheckItem,
  type CheckKind,
  type CheckParams,
  type GroupSummary,
  type Server,
} from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { CheckKindBadge } from './CheckBadges'
import { Modal } from './Modal'
import { ModeBadge } from './StatusBadge'
import { useToast } from './Toast'

import { t } from '../i18n'
const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 disabled:cursor-not-allowed disabled:bg-slate-50 disabled:text-slate-500 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:disabled:bg-slate-900 dark:disabled:text-slate-400'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const KIND_OPTIONS: { value: CheckKind; label: string; hint: string }[] = [
  { value: 'tcp', label: 'TCP', hint: t('s.0319') },
  { value: 'http', label: 'HTTP', hint: t('s.0320') },
  { value: 'ping', label: 'Ping', hint: t('s.0321') },
  { value: 'process', label: t('s.0314'), hint: t('s.0322') },
  { value: 'script', label: t('s.0315'), hint: t('s.0323') },
]

const TARGET_PLACEHOLDER: Record<CheckKind, string> = {
  tcp: t('s.0324'),
  http: t('s.0325'),
  ping: t('s.0326'),
  process: t('s.0327'),
  script: '',
}

const TARGET_LABEL: Record<CheckKind, string> = {
  tcp: t('s.0328'),
  http: t('s.0329'),
  ping: t('s.0330'),
  process: t('s.0331'),
  script: t('s.0332'),
}

interface CheckFormDialogProps {
  open: boolean
  /** 非空为编辑模式：类型与目标不可改（契约 PATCH 只支持名称/间隔/超时/参数/启用） */
  check: CheckItem | null
  servers: Server[]
  groups: GroupSummary[]
  onClose: () => void
  onSaved: () => void
}

export function CheckFormDialog({
  open,
  check,
  servers,
  groups,
  onClose,
  onSaved,
}: CheckFormDialogProps) {
  const { toast } = useToast()
  const [kind, setKind] = useState<CheckKind>('tcp')
  const [name, setName] = useState('')
  const [target, setTarget] = useState('')
  const [intervalS, setIntervalS] = useState('30')
  const [timeoutS, setTimeoutS] = useState('10')
  const [threshold, setThreshold] = useState('3')
  const [enabled, setEnabled] = useState(true)

  const [expectStatus, setExpectStatus] = useState('200')
  const [keyword, setKeyword] = useState('')
  const [insecure, setInsecure] = useState(false)
  const [minCount, setMinCount] = useState('1')
  const [processUser, setProcessUser] = useState('')
  const [exitZeroOk, setExitZeroOk] = useState(true)
  const [expectStdout, setExpectStdout] = useState('')

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [pickerGroup, setPickerGroup] = useState('')
  const [pickerTag, setPickerTag] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const editing = check !== null

  useEffect(() => {
    if (!open) return
    const params = check?.params ?? {}
    setKind((check?.kind as CheckKind) ?? 'tcp')
    setName(check?.name ?? '')
    setTarget(check?.target ?? '')
    setIntervalS(String(check?.interval_s ?? 30))
    setTimeoutS(String(check?.timeout_s ?? 10))
    setThreshold(String(params.fail_threshold ?? 3))
    setEnabled(check?.enabled ?? true)
    setExpectStatus(String(params.expect_status ?? '200'))
    setKeyword(String(params.keyword ?? ''))
    setInsecure(Boolean(params.insecure))
    setMinCount(String(params.min_count ?? 1))
    setProcessUser(String(params.user ?? ''))
    setExitZeroOk(params.exit_zero_ok !== false)
    setExpectStdout(String(params.expect_stdout ?? ''))
    setSelected(new Set(check?.server_ids ?? []))
    setSearch('')
    setPickerGroup('')
    setPickerTag('')
    setSaving(false)
    setError(null)
  }, [open, check])

  const tagOptions = useMemo(() => {
    const tags = new Set<string>()
    for (const server of servers) for (const tag of server.tags) tags.add(tag)
    return [...tags].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }, [servers])

  const filteredServers = useMemo(() => {
    const query = search.trim().toLowerCase()
    return servers.filter((server) => {
      if (pickerGroup && server.group !== pickerGroup) return false
      if (pickerTag && !server.tags.includes(pickerTag)) return false
      if (!query) return true
      return (
        server.name.toLowerCase().includes(query) ||
        server.host.toLowerCase().includes(query) ||
        server.tags.some((tag) => tag.toLowerCase().includes(query))
      )
    })
  }, [servers, pickerGroup, pickerTag, search])

  const toggleServer = (id: string) => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const validate = (): string | null => {
    if (!name.trim()) return t('s.0333')
    const interval = Number(intervalS)
    if (!Number.isInteger(interval) || interval < 10) return t('s.0334')
    const timeout = Number(timeoutS)
    if (!Number.isInteger(timeout) || timeout < 1) return t('s.0335')
    const thresholdValue = Number(threshold)
    if (!Number.isInteger(thresholdValue) || thresholdValue < 1) return t('s.0336')

    const value = target.trim()
    if (!value) return t('s.0121', { '0': TARGET_LABEL[kind] })
    if (kind === 'tcp') {
      const match = value.match(/^([^\s:]+):(\d{1,5})$/)
      if (!match) return t('s.0337')
      const port = Number(match[2])
      if (port <= 0 || port > 65535) return t('s.0338')
    }
    if (kind === 'http' && !/^https?:\/\/\S+$/i.test(value)) {
      return t('s.0339')
    }
    if (kind === 'ping' && /[:/]/.test(value)) {
      return t('s.0340')
    }
    if (kind === 'process' && /\s/.test(value)) return t('s.0341')
    if (selected.size === 0) return t('s.0342')
    return null
  }

  const buildParams = (): CheckParams => {
    const params: CheckParams = { fail_threshold: Number(threshold) }
    if (kind === 'http') {
      params.expect_status = expectStatus.trim() || '200'
      params.method = 'GET'
      if (keyword.trim()) params.keyword = keyword.trim()
      if (insecure) params.insecure = true
    }
    if (kind === 'process') {
      params.min_count = Number(minCount) || 1
      if (processUser.trim()) params.user = processUser.trim()
    }
    if (kind === 'script') {
      params.exit_zero_ok = exitZeroOk
      if (expectStdout.trim()) params.expect_stdout = expectStdout.trim()
    }
    return params
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
    const serverIds = [...selected]
    try {
      if (editing && check) {
        await api.updateCheck(check.id, {
          name: name.trim(),
          interval_s: Number(intervalS),
          timeout_s: Number(timeoutS),
          params: buildParams(),
          enabled,
        })
        try {
          await api.setCheckTargets(check.id, serverIds)
        } catch (caught) {
          toast(t('s.0343', { '0': errorMessage(caught) }), 'error')
          onSaved()
          onClose()
          return
        }
        toast(t('s.0344', { '0': name.trim() }), 'success')
      } else {
        await api.createCheck({
          name: name.trim(),
          kind,
          target: target.trim(),
          interval_s: Number(intervalS),
          timeout_s: Number(timeoutS),
          params: buildParams(),
          server_ids: serverIds,
        })
        toast(t('s.0345', { '0': name.trim() }), 'success')
      }
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0346')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('s.0347') : t('s.0348')}
      description={
        editing
          ? t('s.0349')
          : t('s.0350')
      }
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="check-form" loading={saving}>
            {editing ? t('s.0128') : t('s.0351')}
          </Button>
        </>
      }
    >
      <form id="check-form" onSubmit={handleSubmit} className="space-y-5">
        <div>
          <span className={labelClass}>{t('s.0352')}</span>
          {editing ? (
            <div className="mt-1.5 flex items-center gap-2">
              <CheckKindBadge kind={kind} />
              <span className="text-xs text-slate-400 dark:text-slate-500">
                {t('s.0353')}
              </span>
            </div>
          ) : (
            <div className="mt-1.5 grid grid-cols-2 gap-2 sm:grid-cols-5">
              {KIND_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  onClick={() => {
                    setKind(option.value)
                    if (option.value !== 'http') setTarget('')
                  }}
                  title={option.hint}
                  className={cn(
                    'rounded-lg border px-2.5 py-2 text-center text-sm font-medium transition-colors',
                    kind === option.value
                      ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                      : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300 dark:hover:border-slate-600',
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
          )}
          <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
            {KIND_OPTIONS.find((option) => option.value === kind)?.hint}
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-1">
            <label htmlFor="check-name" className={labelClass}>
              {t('s.0354')}
            </label>
            <input
              id="check-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0355')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="check-interval" className={labelClass}>
              {t('s.0356')}
            </label>
            <input
              id="check-interval"
              value={intervalS}
              onChange={(event) => setIntervalS(event.target.value)}
              inputMode="numeric"
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="check-timeout" className={labelClass}>
              {t('s.0100')}
            </label>
            <input
              id="check-timeout"
              value={timeoutS}
              onChange={(event) => setTimeoutS(event.target.value)}
              inputMode="numeric"
              className={inputClass + ' mt-1.5'}
            />
          </div>
        </div>

        <div>
          <label htmlFor="check-target" className={labelClass}>
            {TARGET_LABEL[kind]}
          </label>
          {kind === 'script' ? (
            <textarea
              id="check-target"
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              rows={4}
              spellCheck={false}
              disabled={editing}
              placeholder={'#!/bin/sh\ncurl -fsS localhost:8080/healthz >/dev/null'}
              className={inputClass + ' mt-1.5 resize-y font-mono text-xs leading-relaxed'}
            />
          ) : (
            <input
              id="check-target"
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              disabled={editing}
              placeholder={TARGET_PLACEHOLDER[kind]}
              className={inputClass + ' mt-1.5 font-mono text-xs'}
            />
          )}
          {kind === 'script' ? (
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
              {t('s.0357')}
            </p>
          ) : null}
        </div>

        {kind === 'http' ? (
          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <label htmlFor="check-expect-status" className={labelClass}>
                {t('s.0358')}
              </label>
              <input
                id="check-expect-status"
                value={expectStatus}
                onChange={(event) => setExpectStatus(event.target.value)}
                placeholder={t('s.0359')}
                className={inputClass + ' mt-1.5'}
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('s.0360')}
              </p>
            </div>
            <div>
              <label htmlFor="check-keyword" className={labelClass}>
                {t('s.0361')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
              </label>
              <input
                id="check-keyword"
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder={t('s.0362')}
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <label className="mt-6 flex items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
              <input
                type="checkbox"
                checked={insecure}
                onChange={(event) => setInsecure(event.target.checked)}
                className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
              />
              {t('s.0363')}
            </label>
          </div>
        ) : null}

        {kind === 'process' ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor="check-min-count" className={labelClass}>
                {t('s.0364')}
              </label>
              <input
                id="check-min-count"
                value={minCount}
                onChange={(event) => setMinCount(event.target.value)}
                inputMode="numeric"
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <div>
              <label htmlFor="check-process-user" className={labelClass}>
                {t('s.0365')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
              </label>
              <input
                id="check-process-user"
                value={processUser}
                onChange={(event) => setProcessUser(event.target.value)}
                placeholder={t('s.0366')}
                className={inputClass + ' mt-1.5'}
              />
            </div>
          </div>
        ) : null}

        {kind === 'script' ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="flex items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
              <input
                type="checkbox"
                checked={exitZeroOk}
                onChange={(event) => setExitZeroOk(event.target.checked)}
                className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
              />
              {t('s.0367')}
            </label>
            <div>
              <label htmlFor="check-expect-stdout" className={labelClass}>
                {t('s.0368')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
              </label>
              <input
                id="check-expect-stdout"
                value={expectStdout}
                onChange={(event) => setExpectStdout(event.target.value)}
                placeholder={t('s.0369')}
                className={inputClass + ' mt-1.5'}
              />
            </div>
          </div>
        ) : null}

        <div className="grid gap-4 sm:grid-cols-3">
          <div>
            <label htmlFor="check-threshold" className={labelClass}>
              {t('s.0370')}
            </label>
            <input
              id="check-threshold"
              value={threshold}
              onChange={(event) => setThreshold(event.target.value)}
              inputMode="numeric"
              className={inputClass + ' mt-1.5'}
            />
            <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
              {t('s.0371')}
            </p>
          </div>
          <label className="mt-6 flex items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => setEnabled(event.target.checked)}
              className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
            />
            {t('s.0372')}
          </label>
        </div>

        <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className={labelClass}>
              {t('s.0212')}
              <span className="ml-1.5 font-normal text-slate-400">
                {t('s.0208')} {selected.size} / {servers.length}
              </span>
            </span>
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() =>
                  setSelected((current) => {
                    const next = new Set(current)
                    for (const server of filteredServers) next.add(server.id)
                    return next
                  })
                }
                className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
              >
                {t('s.0373')}{filteredServers.length}）
              </button>
              <button
                type="button"
                onClick={() =>
                  setSelected((current) => {
                    const next = new Set(current)
                    for (const server of filteredServers) next.delete(server.id)
                    return next
                  })
                }
                className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
              >
                {t('s.0374')}
              </button>
              <button
                type="button"
                onClick={() => setSelected(new Set())}
                className="text-xs font-medium text-slate-500 hover:text-rose-600 dark:text-slate-400 dark:hover:text-rose-400"
              >
                {t('s.0210')}
              </button>
            </div>
          </div>

          <div className="mt-2 grid gap-2 sm:grid-cols-4">
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('s.0214')}
              className={inputClass + ' sm:col-span-2'}
            />
            <select
              aria-label={t('s.0375')}
              value={pickerGroup}
              onChange={(event) => setPickerGroup(event.target.value)}
              className={inputClass}
            >
              <option value="">{t('s.0376')}</option>
              {groups.map((group) => (
                <option key={group.name} value={group.name}>
                  {group.name}（{group.count}）
                </option>
              ))}
            </select>
            <select
              aria-label={t('s.0377')}
              value={pickerTag}
              onChange={(event) => setPickerTag(event.target.value)}
              className={inputClass}
            >
              <option value="">{t('s.0378')}</option>
              {tagOptions.map((tag) => (
                <option key={tag} value={tag}>
                  {tag}
                </option>
              ))}
            </select>
          </div>

          <div className="mt-2 max-h-52 overflow-y-auto rounded-md border border-slate-100 dark:border-slate-800/70">
            {servers.length === 0 ? (
              <p className="px-3 py-4 text-center text-xs text-slate-400 dark:text-slate-500">
                {t('s.0379')}
              </p>
            ) : filteredServers.length === 0 ? (
              <p className="px-3 py-4 text-center text-xs text-slate-400 dark:text-slate-500">
                {t('s.0380')}
              </p>
            ) : (
              filteredServers.map((server) => (
                <label
                  key={server.id}
                  className="flex cursor-pointer items-center gap-2.5 border-b border-slate-100 px-3 py-2 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/70 dark:hover:bg-slate-900/60"
                >
                  <input
                    type="checkbox"
                    checked={selected.has(server.id)}
                    onChange={() => toggleServer(server.id)}
                    className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5">
                      <span className="truncate text-sm text-slate-800 dark:text-slate-100">
                        {server.name}
                      </span>
                      <ModeBadge mode={server.mode} />
                    </span>
                    <span className="block truncate font-mono text-[11px] text-slate-400 dark:text-slate-500">
                      {server.host}
                      {server.group ? ` · ${server.group}` : ''}
                      {server.tags.length > 0 ? ` · ${server.tags.join('/')}` : ''}
                    </span>
                  </span>
                </label>
              ))
            )}
          </div>
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
