import { useCallback, useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

const THEME_STORAGE_KEY = 'leadstack_theme'

export function getStoredTheme(): Theme {
  try {
    return window.localStorage.getItem(THEME_STORAGE_KEY) === 'dark' ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

let currentTheme: Theme = getStoredTheme()
const listeners = new Set<(theme: Theme) => void>()

export function getTheme(): Theme {
  return currentTheme
}

export function applyTheme(theme: Theme): void {
  currentTheme = theme
  document.documentElement.classList.toggle('dark', theme === 'dark')
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme)
  } catch {
    /* ignore */
  }
  for (const listener of listeners) listener(theme)
}

/**
 * 全局主题状态：多个组件（含 uPlot 图表）同时订阅，切换时一起重绘。
 * 首屏由 index.html 内联脚本先行应用，避免闪白。
 */
export function useTheme(): { theme: Theme; toggleTheme: () => void } {
  const [theme, setTheme] = useState<Theme>(() => getTheme())

  useEffect(() => {
    const listener = (next: Theme) => setTheme(next)
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }, [])

  const toggleTheme = useCallback(() => {
    applyTheme(getTheme() === 'dark' ? 'light' : 'dark')
  }, [])

  return { theme, toggleTheme }
}
