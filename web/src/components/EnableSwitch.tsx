import { cn } from '../lib/cn'
import { t } from '../i18n'

interface EnableSwitchProps {
  checked: boolean
  loading?: boolean
  label?: string
  onChange: () => void
}

/** 通用的启用/停用开关（检测项、告警规则、通知渠道共用）。 */
export function EnableSwitch({ checked, loading = false, label, onChange }: EnableSwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label ?? (checked ? t('s.0421') : t('s.0422'))}
      disabled={loading}
      onClick={onChange}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500',
        checked ? 'bg-indigo-600' : 'bg-slate-300 dark:bg-slate-700',
        loading && 'opacity-50',
      )}
    >
      <span
        className={cn(
          'inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform',
          checked ? 'translate-x-[19px]' : 'translate-x-[3px]',
        )}
      />
    </button>
  )
}
