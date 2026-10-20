import { MoonIcon, SunIcon } from './Icons'
import { IconButton } from './Button'
import { usePrefs } from '../lib/prefs'
import { useI18n } from '../i18n'

/** 登录页使用的简易主题开关：在 light/dark 间切换（system 视为浅色起点）。 */
export function ThemeToggle() {
  const { theme, setTheme } = usePrefs()
  const { t } = useI18n()
  const isDark = theme === 'dark'
  return (
    <IconButton
      label={isDark ? t('appearance.theme.light') : t('appearance.theme.dark')}
      onClick={() => setTheme(isDark ? 'light' : 'dark')}
    >
      {isDark ? <SunIcon className="h-4.5 w-4.5" /> : <MoonIcon className="h-4.5 w-4.5" />}
    </IconButton>
  )
}
