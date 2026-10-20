import { useState } from 'react'
import { api } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { Button } from './Button'
import { CheckIcon, CloseIcon, SendIcon, SpinnerIcon } from './Icons'
import { t } from '../i18n'

interface ChannelTestSectionProps {
  channelId: string
  /** 测试完成后回调（用于刷新列表里的最近成功/失败时间） */
  onResult?: (ok: boolean) => void
  compact?: boolean
}

/**
 * 「测试发送」：调用 POST /alert-channels/{id}/test 并原样展示真实成功/失败原因。
 * 通知渠道最容易失败在这里，所以错误信息不做任何包装、可复制。
 */
export function ChannelTestSection({ channelId, onResult, compact = false }: ChannelTestSectionProps) {
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; error: string | null } | null>(null)
  const [requestError, setRequestError] = useState<string | null>(null)

  const handleTest = async () => {
    setLoading(true)
    setResult(null)
    setRequestError(null)
    try {
      const response = await api.testAlertChannel(channelId)
      setResult(response)
      onResult?.(response.ok)
    } catch (caught) {
      setRequestError(errorMessage(caught, t('s.0306')))
      onResult?.(false)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className={compact ? 'space-y-2' : 'rounded-lg border border-slate-200 p-3 dark:border-slate-800'}>
      {!compact ? (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <p className="text-sm font-medium text-slate-700 dark:text-slate-200">{t('s.0303')}</p>
            <p className="mt-0.5 text-xs text-slate-400 dark:text-slate-500">
              {t('s.0307')}
            </p>
          </div>
          <Button
            size="sm"
            variant="secondary"
            onClick={handleTest}
            loading={loading}
            icon={<SendIcon className="h-3.5 w-3.5" />}
          >
            {t('s.0303')}
          </Button>
        </div>
      ) : (
        <Button
          size="sm"
          variant="secondary"
          onClick={handleTest}
          loading={loading}
          icon={<SendIcon className="h-3.5 w-3.5" />}
        >
          {t('s.0303')}
        </Button>
      )}

      {loading ? (
        <p className="mt-2 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <SpinnerIcon className="h-3.5 w-3.5" />
          {t('s.0308')}
        </p>
      ) : null}

      {requestError ? (
        <p className="mt-2 whitespace-pre-wrap break-words rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {t('s.0309')}{requestError}
        </p>
      ) : null}

      {result ? (
        <div
          className={cn(
            'mt-2 rounded-md border px-3 py-2 text-xs',
            result.ok
              ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200'
              : 'border-rose-200 bg-rose-50 text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200',
          )}
        >
          <p className="inline-flex items-center gap-1.5 font-medium">
            {result.ok ? <CheckIcon className="h-3.5 w-3.5" /> : <CloseIcon className="h-3.5 w-3.5" />}
            {result.ok ? t('s.0310') : t('s.0311')}
          </p>
          {!result.ok ? (
            <p className="mt-1 whitespace-pre-wrap break-words font-mono leading-relaxed">
              {result.error?.trim() ? result.error : t('s.0312')}
            </p>
          ) : (
            <p className="mt-1 text-[11px] opacity-80">{t('s.0313')}</p>
          )}
        </div>
      ) : null}
    </div>
  )
}
