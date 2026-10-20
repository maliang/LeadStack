import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import type { Theme } from './theme'
import { applyTheme } from './theme'
import { t } from '../i18n'

export type ThemeMode = 'light' | 'dark' | 'system'
export type Accent = 'blue' | 'indigo' | 'cyan' | 'emerald' | 'violet'
export type FontSizePref = 'small' | 'normal' | 'large'
export type Density = 'comfortable' | 'compact'
export type Language = 'zh-CN' | 'en-US'

export interface Preferences {
  theme: ThemeMode
  accent: Accent
  fontSize: FontSizePref
  density: Density
  language: Language
}

export const THEME_STORAGE_KEY = 'leadstack.theme'
export const ACCENT_STORAGE_KEY = 'leadstack.accent'
export const FONT_SIZE_STORAGE_KEY = 'leadstack.fontSize'
export const DENSITY_STORAGE_KEY = 'leadstack.density'
export const LANGUAGE_KEY = 'leadstack.lang'

export const FONT_SIZE_PX: Record<FontSizePref, number> = {
  small: 14,
  normal: 16,
  large: 18,
}

export const ACCENTS: { value: Accent; label: string; color: string }[] = [
  { value: 'blue', label: t('s.0645'), color: '#2563eb' },
  { value: 'indigo', label: t('s.0646'), color: '#4f46e5' },
  { value: 'cyan', label: t('s.0647'), color: '#0891b2' },
  { value: 'emerald', label: t('s.0648'), color: '#059669' },
  { value: 'violet', label: t('s.0649'), color: '#7c3aed' },
]

export const DEFAULT_PREFERENCES: Preferences = {
  theme: 'system',
  accent: 'indigo',
  fontSize: 'normal',
  density: 'comfortable',
  language: 'zh-CN',
}

function read(key: string, fallback: string): string {
  try {
    return window.localStorage.getItem(key) ?? fallback
  } catch {
    return fallback
  }
}

export function loadPreferences(): Preferences {
  const theme = read(THEME_STORAGE_KEY, DEFAULT_PREFERENCES.theme)
  const accent = read(ACCENT_STORAGE_KEY, DEFAULT_PREFERENCES.accent)
  const fontSize = read(FONT_SIZE_STORAGE_KEY, DEFAULT_PREFERENCES.fontSize)
  const density = read(DENSITY_STORAGE_KEY, DEFAULT_PREFERENCES.density)
  const language = read(LANGUAGE_KEY, DEFAULT_PREFERENCES.language)
  return {
    theme: theme === 'light' || theme === 'dark' || theme === 'system' ? theme : DEFAULT_PREFERENCES.theme,
    accent: ACCENTS.some((item) => item.value === accent) ? (accent as Accent) : DEFAULT_PREFERENCES.accent,
    fontSize:
      fontSize === 'small' || fontSize === 'normal' || fontSize === 'large'
        ? fontSize
        : DEFAULT_PREFERENCES.fontSize,
    density: density === 'compact' ? 'compact' : 'comfortable',
    language: language === 'en-US' ? 'en-US' : 'zh-CN',
  }
}

export function resolveTheme(mode: ThemeMode): Theme {
  if (mode === 'system') {
    try {
      return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
    } catch {
      return 'light'
    }
  }
  return mode
}

/** 把偏好写入 DOM（主题时同时通知图表重绘）。 */
export function applyPreferences(prefs: Preferences, notifyTheme: (theme: Theme) => void): void {
  const resolved = resolveTheme(prefs.theme)
  const root = document.documentElement
  root.dataset.themeMode = prefs.theme
  root.dataset.theme = resolved
  root.dataset.accent = prefs.accent
  root.dataset.density = prefs.density
  root.dataset.fontSize = prefs.fontSize
  root.style.fontSize = `${FONT_SIZE_PX[prefs.fontSize]}px`
  notifyTheme(resolved)
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, prefs.theme)
    window.localStorage.setItem(ACCENT_STORAGE_KEY, prefs.accent)
    window.localStorage.setItem(FONT_SIZE_STORAGE_KEY, prefs.fontSize)
    window.localStorage.setItem(DENSITY_STORAGE_KEY, prefs.density)
    window.localStorage.setItem(LANGUAGE_KEY, prefs.language)
  } catch {
    /* ignore */
  }
}

import { setLanguage as setI18nLanguage } from '../i18n'
interface PrefsContextValue extends Preferences {
  setTheme: (theme: ThemeMode) => void
  setAccent: (accent: Accent) => void
  setFontSize: (size: FontSizePref) => void
  setDensity: (density: Density) => void
  setLanguage: (language: Language) => void
}

const PrefsContext = createContext<PrefsContextValue | null>(null)

export function PrefsProvider({ children }: { children: ReactNode }) {
  const [prefs, setPrefs] = useState<Preferences>(() => {
    try {
      return loadPreferences()
    } catch {
      return DEFAULT_PREFERENCES
    }
  })

  const commit = useCallback((next: Preferences) => {
    setPrefs(next)
    applyPreferences(next, applyTheme)
    setI18nLanguage(next.language)
  }, [])

  // 首次挂载：应用一次（index.html 内联脚本已做首屏防闪，这里补齐 system 监听/语言）
  useEffect(() => {
    applyPreferences(prefs, applyTheme)
    setI18nLanguage(prefs.language)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // system 模式下跟随系统主题变化
  useEffect(() => {
    if (prefs.theme !== 'system') return
    let media: MediaQueryList | null = null
    try {
      media = window.matchMedia('(prefers-color-scheme: dark)')
    } catch {
      return
    }
    const handler = () => applyPreferences(prefs, applyTheme)
    media.addEventListener('change', handler)
    return () => media.removeEventListener('change', handler)
  }, [prefs])

  const value = useMemo<PrefsContextValue>(
    () => ({
      ...prefs,
      setTheme: (theme) => commit({ ...prefs, theme }),
      setAccent: (accent) => commit({ ...prefs, accent }),
      setFontSize: (fontSize) => commit({ ...prefs, fontSize }),
      setDensity: (density) => commit({ ...prefs, density }),
      setLanguage: (language) => commit({ ...prefs, language }),
    }),
    [prefs, commit],
  )

  return <PrefsContext.Provider value={value}>{children}</PrefsContext.Provider>
}

export function usePrefs(): PrefsContextValue {
  const context = useContext(PrefsContext)
  if (!context) throw new Error(t('s.0650'))
  return context
}

