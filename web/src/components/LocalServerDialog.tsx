import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type LocalServerResult } from '../lib/api'
import { useNow } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { formatDuration } from '../lib/format'
import { Button } from './Button'
import { CheckIcon, CopyIcon, RefreshIcon, ServerIcon } from './Icons'
import { InlineLoading } from './Loading'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

interface LocalServerDialogProps {
  open: boolean
  onClose: () => void
  /** 服务器列表需要刷新（创建成功 / agent 上线）时回调 */
  onChanged: () => void
}

/** 一键接入本机：创建 127.0.0.1 的 agent 服务器并展示安装命令，轮询直到上线或令牌过期。 */
export function LocalServerDialog({ open, onClose, onChanged }: LocalServerDialogProps) {
  const { toast } = useToast()
  const now = useNow(1_000)
  const onChangedRef = useRef(onChanged)
  onChangedRef.current = onChanged
  const [creating, setCreating] = useState(false)
  const [result, setResult] = useState<LocalServerResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [state, setState] = useState<'waiting' | 'online' | 'expired'>('waiting')
  const [copied, setCopied] = useState(false)

  const create = useCallback(async () => {
    setCreating(true)
    setError(null)
    setCopied(false)
    setState('waiting')
    try {
      const created = await api.createLocalServer()
      setResult(created)
      if (created.expires_at != null && Date.now() > created.expires_at) {
        setState('expired')
      }
      onChanged()
    } catch (caught) {
      setResult(null)
      setError(errorMessage(caught, t('localServer.createFailed')))
    } finally {
      setCreating(false)
    }
  }, [onChanged])

  useEffect(() => {
    if (!open) {
      setResult(null)
      setError(null)
      setState('waiting')
      setCopied(false)
      return
    }
    void create()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useEffect(() => {
    if (!open || !result || state !== 'waiting') return
    let cancelled = false
    const check = async () => {
      if (result.expires_at != null && Date.now() > result.expires_at) {
        if (!cancelled) setState('expired')
        return
      }
      try {
        const server = await api.server(result.server.id)
        if (!cancelled && server.status === 'online') {
          setState('online')
          onChangedRef.current()
        }
      } catch {
        /* 轮询失败保持等待，不打断弹窗 */
      }
    }
    void check()
    const timer = window.setInterval(() => void check(), 3_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [open, result, state])

  const handleCopy = async () => {
    if (!result) return
    try {
      await navigator.clipboard.writeText(result.command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2_000)
    } catch {
      toast(t('localServer.copyFailed'), 'error')
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('localServer.title')}
      description={t('localServer.description')}
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => void create()}
            loading={creating}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            {t('localServer.regenerate')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}

        {creating && !result ? <InlineLoading label={t('localServer.creating')} /> : null}

        {result ? (
          <>
            <div className="flex flex-wrap items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
              <span className="inline-flex h-7 w-7 items-center justify-center rounded-md bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10 dark:text-emerald-300">
                <ServerIcon className="h-4 w-4" />
              </span>
              <span className="font-medium">{result.server.name}</span>
              <span className="font-mono text-xs text-slate-400 dark:text-slate-500">
                {result.server.host} · agent
              </span>
            </div>

            <div>
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-medium text-slate-600 dark:text-slate-300">
                  {t('localServer.command')}
                </span>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => void handleCopy()}
                  icon={copied ? <CheckIcon className="h-3.5 w-3.5" /> : <CopyIcon className="h-3.5 w-3.5" />}
                >
                  {copied ? t('localServer.copied') : t('localServer.copy')}
                </Button>
              </div>
              <pre className="mt-1.5 max-h-40 overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-slate-100">
                {result.command}
              </pre>
            </div>

            <p className="text-xs text-slate-500 dark:text-slate-400">
              {t('localServer.tokenExpiry')}
              {result.expires_at != null
                ? t('localServer.tokenRemaining', {
                    time: formatDuration(Math.max(0, Math.floor((result.expires_at - now) / 1000))),
                  })
                : t('localServer.noExpiry')}
              {t('localServer.oneTime')}
            </p>

            <div className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-xs leading-relaxed text-slate-600 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-300">
              {t('localServer.hint')}
            </div>

            {state === 'waiting' ? (
              <div className="flex items-center gap-2 rounded-md border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-800 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
                <span className="h-2 w-2 animate-pulse rounded-full bg-indigo-500" />
                {t('localServer.waiting')}
              </div>
            ) : state === 'online' ? (
              <div className="flex items-center gap-2 rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs font-medium text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200">
                <CheckIcon className="h-3.5 w-3.5" />
                {t('localServer.online')}
              </div>
            ) : (
              <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
                {t('localServer.expired')}
              </div>
            )}
          </>
        ) : null}
      </div>
    </Modal>
  )
}
