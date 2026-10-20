import { SpinnerIcon } from './Icons'
import { t } from '../i18n'

/** 全屏加载态（校验登录会话时使用）。 */
export function PageLoading({ label = t('s.0425') }: { label?: string }) {
  return (
    <div className="flex h-full min-h-screen flex-col items-center justify-center gap-3 text-slate-400 dark:text-slate-500">
      <SpinnerIcon className="h-7 w-7" />
      <p className="text-sm">{label}</p>
    </div>
  )
}

/** 区块内加载态。 */
export function InlineLoading({ label = t('s.0425') }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 px-6 py-12 text-sm text-slate-400 dark:text-slate-500">
      <SpinnerIcon className="h-4 w-4" />
      <span>{label}</span>
    </div>
  )
}
