import { onScopeDispose, ref, type Ref } from 'vue'
import zhCN from './zh-CN'
import enUS from './en-US'

/**
 * Vue 版 i18n 入口（对应 web/src/i18n/index.ts 的非 React 部分）。
 * 与旧版保持同名导出：getLanguage / setLanguage / t / initI18n，
 * 只把 React hook `useI18n` 换成 Vue 组合式函数（ref + onScopeDispose）。
 */

export type Language = 'zh-CN' | 'en-US'

export const LANGUAGE_STORAGE_KEY = 'leadstack.lang'
export const SUPPORTED_LANGUAGES: { value: Language; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en-US', label: 'English' },
]

type Dict = Record<string, string>

const DICTIONARIES: Record<Language, Dict> = {
  'zh-CN': zhCN,
  'en-US': enUS,
}

function readStoredLanguage(): Language {
  try {
    const raw = window.localStorage.getItem(LANGUAGE_STORAGE_KEY)
    return raw === 'en-US' ? 'en-US' : 'zh-CN'
  } catch {
    return 'zh-CN'
  }
}

let currentLanguage: Language = readStoredLanguage()
const listeners = new Set<(lang: Language) => void>()

export function getLanguage(): Language {
  return currentLanguage
}

export function setLanguage(lang: Language): void {
  currentLanguage = lang
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, lang)
  } catch {
    /* ignore */
  }
  document.documentElement.setAttribute('lang', lang === 'en-US' ? 'en' : 'zh-CN')
  for (const listener of listeners) listener(lang)
}

/**
 * 翻译函数（可在 Vue 组件与普通模块中使用）。
 * 回退链：当前语言 → zh-CN → key，保证任何情况下都不会把 key 原文暴露给用户。
 */
export function t(key: string, vars?: Record<string, string | number>): string {
  const dict = DICTIONARIES[currentLanguage] ?? {}
  let text = dict[key]
  if (text === undefined) text = DICTIONARIES['zh-CN'][key]
  if (text === undefined) text = key
  if (!vars) return text
  return text.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match,
  )
}

/** Vue 组合式函数：语言变化时触发重渲染。 */
export function useI18n(): {
  t: typeof t
  language: Ref<Language>
  setLanguage: (lang: Language) => void
} {
  const language = ref<Language>(getLanguage())
  const listener = (lang: Language): void => {
    language.value = lang
  }
  listeners.add(listener)
  onScopeDispose(() => {
    listeners.delete(listener)
  })
  return { t, language, setLanguage }
}

/** 初始化 <html lang>，供入口调用一次。 */
export function initI18n(): void {
  document.documentElement.setAttribute('lang', currentLanguage === 'en-US' ? 'en' : 'zh-CN')
}

export type TranslationKey = string
