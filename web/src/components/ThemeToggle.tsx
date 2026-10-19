import { MoonIcon, SunIcon } from './Icons'
import { IconButton } from './Button'
import { useTheme } from '../lib/theme'

export function ThemeToggle() {
  const { theme, toggleTheme } = useTheme()
  return (
    <IconButton
      label={theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'}
      onClick={toggleTheme}
    >
      {theme === 'dark' ? <SunIcon className="h-4.5 w-4.5" /> : <MoonIcon className="h-4.5 w-4.5" />}
    </IconButton>
  )
}
