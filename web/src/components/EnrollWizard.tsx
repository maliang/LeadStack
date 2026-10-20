import { useEffect, useState } from 'react'
import { api, resolveControlUrl, type EnrollToken } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { Button } from './Button'
import { CopyButton } from './CopyButton'
import { InlineLoading } from './Loading'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

const TTL_OPTIONS = [
  { value: 3_600, label: t('s.0001') },
  { value: 86_400, label: t('s.0002') },
  { value: 604_800, label: t('s.0003') },
]

const STEPS = [
  { title: t('s.0008'), description: t('s.0009') },
  { title: t('s.0010'), description: t('s.0011') },
  { title: t('s.0012'), description: t('s.0013') },
]

interface EnrollWizardProps {
  open: boolean
  onClose: () => void
  /** 已生成过令牌时关闭弹窗，通知列表刷新。 */
  onFinished: () => void
}

export function EnrollWizard({ open, onClose, onFinished }: EnrollWizardProps) {
  const { toast } = useToast()
  const [note, setNote] = useState('')
  const [ttl, setTtl] = useState(86_400)
  const [creating, setCreating] = useState(false)
  const [token, setToken] = useState<EnrollToken | null>(null)
  const [command, setCommand] = useState<string | null>(null)
  const [commandLoading, setCommandLoading] = useState(false)
  const [commandError, setCommandError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // 每次打开重置向导状态
  useEffect(() => {
    if (!open) return
    setNote('')
    setTtl(86_400)
    setCreating(false)
    setToken(null)
    setCommand(null)
    setCommandLoading(false)
    setCommandError(null)
    setError(null)
  }, [open])

  const loadInstallScript = async (plainToken: string) => {
    setCommandLoading(true)
    setCommandError(null)
    try {
      const script = await api.installScript({
        token: plainToken,
        server_url: resolveControlUrl(),
      })
      setCommand(script.command)
    } catch (caught) {
      setCommand(null)
      setCommandError(errorMessage(caught, t('s.0014')))
    } finally {
      setCommandLoading(false)
    }
  }

  const handleCreate = async () => {
    setCreating(true)
    setError(null)
    try {
      const created = await api.createEnrollToken({
        note: note.trim() || t('s.0015'),
        ttl_s: ttl,
      })
      setToken(created)
      if (created.token) {
        toast(t('s.0016'), 'success')
        await loadInstallScript(created.token)
      } else {
        setError(t('s.0017'))
      }
    } catch (caught) {
      setError(errorMessage(caught, t('s.0018')))
    } finally {
      setCreating(false)
    }
  }

  const handleClose = () => {
    if (token) onFinished()
    onClose()
  }

  const handleRegenerate = () => {
    setToken(null)
    setCommand(null)
    setCommandError(null)
    setError(null)
  }

  const footer = token ? (
    <>
      <Button variant="secondary" onClick={handleRegenerate} disabled={creating}>
        {t('s.0025')}
      </Button>
      <Button onClick={handleClose}>{t('s.0026')}</Button>
    </>
  ) : (
    <>
      <Button variant="secondary" onClick={onClose} disabled={creating}>
        {t('s.0024')}
      </Button>
      <Button onClick={handleCreate} loading={creating}>
        {t('s.0028')}
      </Button>
    </>
  )

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title={t('s.0423')}
      description={t('s.0031')}
      size="lg"
      footer={footer}
    >
      {token === null ? (
        <div className="space-y-5">
          <ol className="grid gap-3 sm:grid-cols-3">
            {STEPS.map((step, index) => (
              <li
                key={step.title}
                className="rounded-lg border border-slate-200 bg-slate-50/60 p-3 dark:border-slate-800 dark:bg-slate-950/40"
              >
                <span className="flex h-5 w-5 items-center justify-center rounded-full bg-indigo-600 text-[11px] font-semibold text-white">
                  {index + 1}
                </span>
                <p className="mt-2 text-sm font-medium text-slate-800 dark:text-slate-200">
                  {step.title}
                </p>
                <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
                  {step.description}
                </p>
              </li>
            ))}
          </ol>

          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label
                htmlFor="enroll-note"
                className="block text-sm font-medium text-slate-700 dark:text-slate-200"
              >
                {t('s.0039')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
              </label>
              <input
                id="enroll-note"
                value={note}
                onChange={(event) => setNote(event.target.value)}
                maxLength={64}
                placeholder={t('s.0041')}
                className="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
              />
            </div>
            <div>
              <label
                htmlFor="enroll-ttl"
                className="block text-sm font-medium text-slate-700 dark:text-slate-200"
              >
                {t('s.0424')}
              </label>
              <select
                id="enroll-ttl"
                value={ttl}
                onChange={(event) => setTtl(Number(event.target.value))}
                className="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
              >
                {TTL_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.0043')}
          </p>

          {error ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {error}
            </p>
          ) : null}
        </div>
      ) : (
        <div className="space-y-4">
          <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-3 dark:border-emerald-500/30 dark:bg-emerald-500/10">
            <p className="text-sm font-medium text-emerald-800 dark:text-emerald-200">
              {t('s.0044')}
            </p>
            <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
              {t('s.0045')} {formatDateTime(token.expires_at)} {t('s.0046')}
            </p>
          </div>

          {token.token ? (
            <div>
              <p className="text-sm font-medium text-slate-700 dark:text-slate-200">{t('s.0047')}</p>
              <div className="mt-1.5 flex items-start gap-2">
                <code className="min-w-0 flex-1 break-all rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs text-slate-600 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-300">
                  {token.token}
                </code>
                <CopyButton value={token.token} label={t('s.0048')} />
              </div>
            </div>
          ) : null}

          <div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-sm font-medium text-slate-700 dark:text-slate-200">
                {t('s.0049')}
                <span className="ml-1.5 font-normal text-slate-400 dark:text-slate-500">
                  {t('s.0050')}
                </span>
              </p>
              {command ? <CopyButton value={command} label={t('s.0051')} /> : null}
            </div>

            {commandLoading ? (
              <div className="mt-1.5 rounded-md border border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-950">
                <InlineLoading label={t('s.0052')} />
              </div>
            ) : command ? (
              <pre className="mt-1.5 overflow-x-auto rounded-md border border-slate-200 bg-slate-50 px-3 py-2.5 font-mono text-xs leading-relaxed text-slate-700 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-300">
                <code className="whitespace-pre-wrap break-all">{command}</code>
              </pre>
            ) : null}

            {commandError ? (
              <div className="mt-1.5 flex flex-wrap items-center justify-between gap-2 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                <span>{commandError}</span>
                {token.token ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void loadInstallScript(token.token!)}
                  >
                    {t('s.0053')}
                  </Button>
                ) : null}
              </div>
            ) : null}
          </div>

          <div className="rounded-md border border-slate-200 bg-slate-50 px-3.5 py-3 text-xs leading-relaxed text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
            <p>
              {t('s.0054')}{' '}
              <code className="rounded bg-white px-1 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-900 dark:text-slate-300">
                {resolveControlUrl()}
              </code>
              {t('s.0055')}
            </p>
            <p className="mt-1">{t('s.0056')}</p>
          </div>
        </div>
      )}
    </Modal>
  )
}
