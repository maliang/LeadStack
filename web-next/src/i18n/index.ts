import { onScopeDispose, ref, type Ref } from 'vue'
import zhCN from './zh-CN/index'

/**
 * Vue 版 i18n 入口（对应 web/src/i18n/index.ts 的非 React 部分）。
 * 与旧版保持同名导出：getLanguage / setLanguage / t / initI18n，
 * 只把 React hook `useI18n` 换成 Vue 组合式函数（ref + onScopeDispose）。
 *
 * 词典打包策略：zh-CN 是默认语言，静态打包保证首屏同步可用；
 * en-US 走动态 import（独立 chunk），由 ensureLanguage() 按需加载。
 * 因此 t() 必须保持同步——未加载时回退 zh-CN，再回退 key，永不抛异常。
 */

export type Language = 'zh-CN' | 'en-US'

export const LANGUAGE_STORAGE_KEY = 'leadstack.lang'
export const SUPPORTED_LANGUAGES: { value: Language; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en-US', label: 'English' },
]

type Dict = Record<string, string>

/** 默认语言词典：静态打包，任何时刻都可同步读取。 */
const ZH_DICT: Dict = zhCN

/** 已加载词典（en-US 在动态 import 完成后才落进来）。 */
const dictionaries: Partial<Record<Language, Dict>> = { 'zh-CN': ZH_DICT }

/** 各语言的加载器；只有 en-US 有，zh-CN 命中静态词典时不会走到这里。 */
const LOADERS: Partial<Record<Language, () => Promise<{ default: Dict }>>> = {
  'en-US': () => import('./en-US/index'),
}

/** 进行中的加载：同一语言并发调用只发一次请求（防重复 load）。 */
const inflight = new Map<Language, Promise<boolean>>()

/** 标记「词典 import 进行中」，供 main.ts 的预加载错误处理区分语言包失败与路由 chunk 失败。 */
const IMPORT_FLAG = '__leadstackI18nImporting'
type ImportFlagWindow = Window & { [IMPORT_FLAG]?: boolean }

export function readStoredLanguage(): Language {
  try {
    const raw = window.localStorage.getItem(LANGUAGE_STORAGE_KEY)
    return raw === 'en-US' ? 'en-US' : 'zh-CN'
  } catch {
    return 'zh-CN'
  }
}

/** 该语言词典是否已经可用（zh-CN 恒为 true）。 */
export function isLanguageLoaded(lang: Language): boolean {
  return dictionaries[lang] !== undefined
}

/**
 * 确保某语言的词典已加载。
 * - 已加载：立即 resolve(true)；
 * - 加载中：复用同一个 promise（成功 / 失败结果共享）；
 * - 失败：不抛异常（t 会继续回退 zh-CN），清掉 inflight 以便下次重试，并 resolve(false)。
 *
 * 返回布尔值是给 switchLanguage 用的：失败时调用方必须能分辨「词典没就绪」，
 * 从而拒绝切换、拒绝落盘，而不是把加载失败当成切换成功。
 */
export function ensureLanguage(lang: Language): Promise<boolean> {
  if (dictionaries[lang]) return Promise.resolve(true)
  const existing = inflight.get(lang)
  if (existing) return existing

  const loader = LOADERS[lang]
  if (!loader) return Promise.resolve(true)

  ;(window as ImportFlagWindow)[IMPORT_FLAG] = true
  const task = loader()
    .then((mod) => {
      dictionaries[lang] = (mod && mod.default) || {}
      return true
    })
    .catch((error: unknown) => {
      // 词典加载失败不阻塞界面：保持 zh-CN 回退，下次切换/刷新可重试。
      console.warn(`[i18n] 词典加载失败：${lang}`, error)
      return false
    })
    .then((ok) => {
      inflight.delete(lang)
      ;(window as ImportFlagWindow)[IMPORT_FLAG] = false
      return ok
    })

  inflight.set(lang, task)
  return task
}

let currentLanguage: Language = readStoredLanguage()
/** 用户最近一次请求的语言：后台加载完成时据此判断是否已被更新的请求取代。 */
let desiredLanguage: Language = currentLanguage
const listeners = new Set<(lang: Language) => void>()

function notify(lang: Language): void {
  for (const listener of listeners) listener(lang)
}

function applyDocumentLang(lang: Language): void {
  document.documentElement.setAttribute('lang', lang === 'en-US' ? 'en' : 'zh-CN')
}

/**
 * 提交语言切换的三个副作用：落盘、<html lang>、通知监听者。
 * 只有词典确认可用之后才允许调用 —— 断网时加载失败不能把 en-US 写进 localStorage，
 * 否则下次冷启动会反复拉取失败的 chunk（S3）。
 */
function commitLanguage(lang: Language): void {
  currentLanguage = lang
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, lang)
  } catch {
    /* ignore */
  }
  applyDocumentLang(lang)
  notify(lang)
}

export function getLanguage(): Language {
  return currentLanguage
}

/**
 * 同步切换当前语言（保持旧签名，调用点无需改造）。
 * 目标词典未就绪时不切换、不落盘，返回 false，并在后台补一次加载；
 * 加载成功且期间没有更新的语言请求时才提交（避免旧请求覆盖用户的新选择）。
 */
export function setLanguage(lang: Language): boolean {
  desiredLanguage = lang
  if (!isLanguageLoaded(lang)) {
    void ensureLanguage(lang).then((ok) => {
      if (ok && desiredLanguage === lang) commitLanguage(lang)
    })
    return false
  }
  commitLanguage(lang)
  return true
}

/**
 * 异步切换语言：先确保词典就绪，成功后才落盘并通知，切换瞬间就能拿到目标语言文案。
 * 加载失败（或等待期间用户又切了别的语言）时保持原语言不变并返回 false，
 * 由调用方决定是否提示/回滚；t() 仍继续按原语言回退，不阻塞界面。
 */
export async function switchLanguage(lang: Language): Promise<boolean> {
  desiredLanguage = lang
  const ok = await ensureLanguage(lang)
  if (!ok || desiredLanguage !== lang) return false
  commitLanguage(lang)
  return true
}

/**
 * 翻译函数（可在 Vue 组件与普通模块中使用）。
 * 回退链：当前语言 → zh-CN → key，保证任何情况下都不会把 key 原文暴露给用户。
 */
export function t(key: string, vars?: Record<string, string | number>): string {
  const dict = dictionaries[currentLanguage] ?? ZH_DICT
  let text = dict[key]
  if (text === undefined) text = ZH_DICT[key]
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
  setLanguage: (lang: Language) => boolean
  switchLanguage: (lang: Language) => Promise<boolean>
} {
  const language = ref<Language>(getLanguage())
  const listener = (lang: Language): void => {
    language.value = lang
  }
  listeners.add(listener)
  onScopeDispose(() => {
    listeners.delete(listener)
  })
  return { t, language, setLanguage, switchLanguage }
}

/** 初始化 <html lang>，供入口在挂载前调用一次。 */
export function initI18n(): void {
  applyDocumentLang(currentLanguage)
}

export type TranslationKey = string
