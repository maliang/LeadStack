import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  api,
  isMaskedValue,
  type AlertChannel,
  type ChannelKind,
} from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { ChannelTestSection } from './ChannelTestSection'
import { ChannelKindBadge } from './AlertBadges'
import { Modal } from './Modal'
import { useToast } from './Toast'

import { t } from '../i18n'
const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

interface FieldSpec {
  key: string
  label: string
  placeholder?: string
  hint?: string
  /** 密钥类：后端脱敏返回；留空表示不修改 */
  secret?: boolean
  required?: boolean
  type?: 'text' | 'select' | 'number'
  options?: { value: string; label: string }[]
  /** 数组类字段（逗号分隔） */
  list?: boolean
  default?: string
}

const KINDS: { value: ChannelKind; label: string }[] = [
  { value: 'webhook', label: 'Webhook' },
  { value: 'dingtalk', label: t('s.0092') },
  { value: 'wecom', label: t('s.0095') },
  { value: 'feishu', label: t('s.0094') },
  { value: 'slack', label: 'Slack' },
  { value: 'telegram', label: 'Telegram' },
  { value: 'email', label: t('s.0091') },
]

const KIND_FIELDS: Record<ChannelKind, FieldSpec[]> = {
  webhook: [
    { key: 'url', label: 'Webhook URL', placeholder: 'https://example.com/hook', secret: true, required: true },
    {
      key: 'method',
      label: t('s.0096'),
      type: 'select',
      options: [
        { value: 'POST', label: 'POST' },
        { value: 'PUT', label: 'PUT' },
      ],
      default: 'POST',
    },
    {
      key: 'template',
      label: t('s.0097'),
      type: 'select',
      options: [
        { value: 'json', label: t('s.0098') },
        { value: 'text', label: t('s.0099') },
      ],
      default: 'json',
    },
    { key: 'timeout_s', label: t('s.0100'), type: 'number', placeholder: '10' },
  ],
  dingtalk: [
    { key: 'webhook', label: t('s.0101'), placeholder: 'https://oapi.dingtalk.com/robot/send?access_token=...', secret: true, required: true },
    { key: 'secret', label: t('s.0102'), secret: true, hint: t('s.0103') },
  ],
  wecom: [
    { key: 'webhook', label: t('s.0101'), placeholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...', secret: true, required: true },
  ],
  feishu: [
    { key: 'webhook', label: t('s.0101'), placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/...', secret: true, required: true },
    { key: 'secret', label: t('s.0104'), secret: true, hint: t('s.0105') },
  ],
  slack: [
    { key: 'webhook', label: 'Incoming Webhook URL', placeholder: 'https://hooks.slack.com/services/...', secret: true, required: true },
    { key: 'channel', label: t('s.0106'), placeholder: '#alerts' },
  ],
  telegram: [
    { key: 'bot_token', label: 'Bot Token', placeholder: '123456:ABC-DEF...', secret: true, required: true },
    { key: 'chat_id', label: 'Chat ID', placeholder: '-1001234567890', required: true },
    { key: 'parse_mode', label: t('s.0107'), placeholder: 'Markdown' },
    { key: 'api_base', label: t('s.0108'), placeholder: 'https://api.telegram.org', hint: t('s.0109') },
  ],
  email: [
    { key: 'smtp_host', label: t('s.0110'), placeholder: 'smtp.example.com', required: true },
    { key: 'smtp_port', label: t('s.0111'), type: 'number', placeholder: '587' },
    { key: 'username', label: t('s.0112'), placeholder: 'alerts@example.com' },
    { key: 'password', label: t('s.0113'), secret: true, hint: t('s.0114') },
    { key: 'from', label: t('s.0115'), placeholder: 'alerts@example.com', required: true },
    { key: 'to', label: t('s.0116'), placeholder: 'ops@example.com, dev@example.com', hint: t('s.0117'), list: true, required: true },
    {
      key: 'tls',
      label: t('s.0118'),
      type: 'select',
      options: [
        { value: 'starttls', label: t('s.0119') },
        { value: 'ssl', label: 'ssl' },
        { value: 'none', label: 'none' },
      ],
      default: 'starttls',
    },
  ],
}

interface AlertChannelDialogProps {
  open: boolean
  channel: AlertChannel | null
  onClose: () => void
  onSaved: () => void
}

/**
 * 渠道新建/编辑。
 * 密钥安全：后端返回的 config 里密钥字段已脱敏，这里不会把脱敏串显示成真值——
 * 命中脱敏的值渲染为空输入 + 「已配置，留空表示不修改」占位；提交时留空的密钥字段整个不发。
 */
export function AlertChannelDialog({ open, channel, onClose, onSaved }: AlertChannelDialogProps) {
  const { toast } = useToast()
  const editing = channel !== null

  const [kind, setKind] = useState<ChannelKind>('webhook')
  const [name, setName] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [values, setValues] = useState<Record<string, string>>({})
  const [extraConfig, setExtraConfig] = useState<Record<string, unknown>>({})
  const [configuredSecrets, setConfiguredSecrets] = useState<Set<string>>(new Set())
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    const nextKind = (channel?.kind as ChannelKind) ?? 'webhook'
    const config = channel?.config ?? {}
    const fields = KIND_FIELDS[nextKind] ?? KIND_FIELDS.webhook
    const nextValues: Record<string, string> = {}
    const masked = new Set<string>()
    const known = new Set(fields.map((field) => field.key))
    // 保留后端可能多带的、UI 未展示的配置键（如 webhook.headers），避免 PATCH 时丢失
    const extras: Record<string, unknown> = {}

    for (const field of fields) {
      const raw = config[field.key]
      if (isMaskedValue(raw)) {
        // 脱敏值：不回显、不提交；留空即不修改
        nextValues[field.key] = ''
        masked.add(field.key)
        continue
      }
      if (Array.isArray(raw)) nextValues[field.key] = raw.map(String).join(', ')
      else if (raw === undefined || raw === null) nextValues[field.key] = field.default ?? ''
      else nextValues[field.key] = String(raw)
    }
    for (const [key, raw] of Object.entries(config)) {
      if (!known.has(key) && !isMaskedValue(raw)) extras[key] = raw
    }

    setKind(nextKind)
    setName(channel?.name ?? '')
    setEnabled(channel?.enabled ?? true)
    setValues(nextValues)
    setExtraConfig(extras)
    setConfiguredSecrets(masked)
    setRevealed(new Set())
    setSaving(false)
    setError(null)
  }, [open, channel])

  const fields = useMemo(() => KIND_FIELDS[kind] ?? KIND_FIELDS.webhook, [kind])

  const buildConfig = (): Record<string, unknown> => {
    const config: Record<string, unknown> = { ...extraConfig }
    for (const field of fields) {
      const value = (values[field.key] ?? '').trim()
      if (value === '') {
        // 留空：密钥字段跳过（等价不修改）；非密钥字段跳过让后端用默认值
        continue
      }
      if (field.list) {
        config[field.key] = value
          .split(',')
          .map((item) => item.trim())
          .filter(Boolean)
        continue
      }
      if (field.type === 'number') {
        const parsed = Number(value)
        config[field.key] = Number.isFinite(parsed) ? parsed : value
        continue
      }
      config[field.key] = value
    }
    return config
  }

  const validate = (): string | null => {
    if (!name.trim()) return t('s.0120')
    for (const field of fields) {
      if (!field.required) continue
      const value = (values[field.key] ?? '').trim()
      const alreadyConfigured = configuredSecrets.has(field.key)
      if (value === '' && !alreadyConfigured) return t('s.0121', { '0': field.label })
    }
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
      kind,
      enabled,
      config: buildConfig(),
    }
    try {
      if (editing && channel) {
        await api.updateAlertChannel(channel.id, payload)
        toast(t('s.0122', { '0': name.trim() }), 'success')
      } else {
        await api.createAlertChannel(payload)
        toast(t('s.0123', { '0': name.trim() }), 'success')
      }
      onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0124')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('s.0125') : t('s.0126')}
      description={t('s.0127')}
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="alert-channel-form" loading={saving}>
            {editing ? t('s.0128') : t('s.0129')}
          </Button>
        </>
      }
    >
      <form id="alert-channel-form" onSubmit={handleSubmit} className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-2">
            <label htmlFor="channel-name" className={labelClass}>
              {t('s.0130')}
            </label>
            <input
              id="channel-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0131')}
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
            {t('s.0132')}
          </label>
        </div>

        <div>
          <span className={labelClass}>{t('s.0133')}</span>
          <div className="mt-1.5 flex flex-wrap gap-2">
            {KINDS.map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => {
                  setKind(option.value)
                  setValues({})
                  setConfiguredSecrets(new Set())
                }}
                className={cn(
                  'rounded-lg border px-3 py-1.5 text-sm font-medium transition-colors',
                  kind === option.value
                    ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300 dark:hover:border-slate-600',
                )}
              >
                {option.label}
              </button>
            ))}
          </div>
          <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
            {editing ? t('s.0134') : t('s.0135')}
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          {fields.map((field) => {
            const maskedConfigured = configuredSecrets.has(field.key)
            const inputId = `channel-field-${field.key}`
            return (
              <div key={field.key} className={field.key === 'to' ? 'sm:col-span-2' : undefined}>
                <label htmlFor={inputId} className={labelClass}>
                  {field.label}
                  {field.required && !maskedConfigured ? (
                    <span className="ml-1 text-rose-500">*</span>
                  ) : null}
                </label>

                {field.type === 'select' ? (
                  <select
                    id={inputId}
                    value={values[field.key] ?? ''}
                    onChange={(event) =>
                      setValues((current) => ({ ...current, [field.key]: event.target.value }))
                    }
                    className={inputClass + ' mt-1.5'}
                  >
                    {(field.options ?? []).map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                ) : field.secret ? (
                  <div className="relative mt-1.5">
                    <input
                      id={inputId}
                      type={revealed.has(field.key) ? 'text' : 'password'}
                      value={values[field.key] ?? ''}
                      autoComplete="new-password"
                      onChange={(event) =>
                        setValues((current) => ({ ...current, [field.key]: event.target.value }))
                      }
                      placeholder={
                        maskedConfigured ? t('s.0136') : (field.placeholder ?? '')
                      }
                      className={inputClass + ' pr-14'}
                    />
                    <button
                      type="button"
                      tabIndex={-1}
                      onClick={() =>
                        setRevealed((current) => {
                          const next = new Set(current)
                          if (next.has(field.key)) next.delete(field.key)
                          else next.add(field.key)
                          return next
                        })
                      }
                      className="absolute inset-y-0 right-0 px-3 text-xs font-medium text-slate-400 hover:text-slate-600 dark:hover:text-slate-300"
                    >
                      {revealed.has(field.key) ? t('s.0137') : t('s.0138')}
                    </button>
                  </div>
                ) : (
                  <input
                    id={inputId}
                    type={field.type === 'number' ? 'number' : 'text'}
                    value={values[field.key] ?? ''}
                    onChange={(event) =>
                      setValues((current) => ({ ...current, [field.key]: event.target.value }))
                    }
                    placeholder={field.placeholder ?? ''}
                    className={inputClass + ' mt-1.5'}
                  />
                )}

                {maskedConfigured ? (
                  <p className="mt-1 text-xs text-emerald-600 dark:text-emerald-400">
                    {t('s.0139')}
                  </p>
                ) : field.hint ? (
                  <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">{field.hint}</p>
                ) : null}
              </div>
            )
          })}
        </div>

        <p className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-xs leading-relaxed text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
          {t('s.0140')}
        </p>

        {editing && channel ? (
          <ChannelTestSection
            channelId={channel.id}
            onResult={() => {
              void onSaved()
            }}
          />
        ) : (
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.0141')}
          </p>
        )}

        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}

        {editing && channel ? (
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.0142')}<ChannelKindBadge kind={kind} />
          </p>
        ) : null}
      </form>
    </Modal>
  )
}
