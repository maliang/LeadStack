import { useCallback, useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

const THEME_STORAGE_KEY = 'leadstack.theme'

function readStoredTheme(): Theme {
  try {
    const raw = window.localStorage.getItem(THEME_STORAGE_KEY)
    if (raw === 'dark' || raw === 'light') return raw
  } catch {
    /* ignore */
  }
  return 'light'
}

let currentTheme: Theme = readStoredTheme()
const listeners = new Set<(theme: Theme) => void>()

export function getTheme(): Theme {
  return currentTheme
}

/** 仅应用解析后的 light/dark（主题模式由 prefs 负责），并通知图表重绘。 */
export function applyTheme(theme: Theme): void {
  currentTheme = theme
  document.documentElement.classList.toggle('dark', theme === 'dark')
  for (const listener of listeners) listener(theme)
}

export function onThemeChange(listener: (theme: Theme) => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** 旧接口保留：图表订阅主题变化；开关由设置面板负责。 */
export function useTheme(): { theme: Theme; toggleTheme: () => void } {
  const [theme, setTheme] = useState<Theme>(() => getTheme())

  useEffect(() => onThemeChange(setTheme), [])

  const toggleTheme = useCallback(() => {
    applyTheme(getTheme() === 'dark' ? 'light' : 'dark')
  }, [])

  return { theme, toggleTheme }
}
