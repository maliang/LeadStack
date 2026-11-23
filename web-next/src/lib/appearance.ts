/**
 * 外观偏好（主题模式 / 语言 / 界面密度）的唯一契约。
 *
 * 持久化分工（已在 @vean/ui 0.51.5 源码与运行时确认）：
 * - 主题模式由 `SConfigProvider` 的 `persist-theme` 写进 `__VEAN_THEME` envelope
 *   （键名来自 @vean/theme/storage 的 `THEME_STORAGE_KEY`），跨标签由 provider
 *   自带的 storage 监听同步；
 * - 语言、密度走同一个 envelope 的 `extra` 字段（`getExtraTheme` /
 *   `onExtraThemeChange`）。provider 的 writer 在 watch getter 里调用
 *   `getExtraTheme`，因此这里的响应式依赖会被追踪；它与 `persist-theme` 共用同一个
 *   debounce writer，不存在双写冲突，所以不需要 `leadstack.appearance` 兜底。
 * - 语言另有 i18n 的 `leadstack.lang` 作为首屏同步来源（`setLanguage` 自己写），
 *   envelope 里的 `language` 只用于跨标签回填；密度没有第二份存储，首屏由
 *   `index.html` 的内联脚本读 envelope 的 `extra.density` 提前落到 `<html>`。
 */

import { computed, ref } from 'vue'
import {
  BRAND_PRESETS,
  brandPreset,
  normalizeBrandColor,
  resolveBrandScale,
} from '@/theme/palette'

export type AppearanceMode = 'auto' | 'light' | 'dark'
export type AppearanceDensity = 'compact' | 'standard'
/** @vean/theme 的持久化键，首屏内联脚本与 provider 必须共用。 */
export const THEME_STORAGE_KEY = '__VEAN_THEME'
/** 密度落到根元素上的属性名：`html[data-density]`。 */
export const DENSITY_ATTR = 'data-density'

export const DEFAULT_APPEARANCE: { mode: AppearanceMode; density: AppearanceDensity } = {
  mode: 'auto',
  density: 'standard',
}

const MODES: readonly AppearanceMode[] = ['auto', 'light', 'dark']
const DENSITIES: readonly AppearanceDensity[] = ['standard', 'compact']

export function isAppearanceMode(value: unknown): value is AppearanceMode {
  return typeof value === 'string' && MODES.some((mode) => mode === value)
}

export function isAppearanceDensity(value: unknown): value is AppearanceDensity {
  return typeof value === 'string' && DENSITIES.some((density) => density === value)
}

/** 纯函数：envelope 里与外观相关的快照（无法解析的字段一律给 null，交由默认值兜底）。 */
export interface PersistedAppearance {
  mode: AppearanceMode | null
  density: AppearanceDensity | null
  /** buildThemeCss 生成的 `<style id="vean-theme">` 文本，用于首屏防闪；不需要时忽略。 */
  style: string | null
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' ? (value as Record<string, unknown>) : null
}

/** 纯函数：解析 `__VEAN_THEME` envelope；畸形 JSON 或结构不符时返回 null。 */
export function parseThemeEnvelope(raw: string | null): PersistedAppearance | null {
  if (!raw) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  const envelope = asRecord(parsed)
  if (!envelope) return null
  const extra = asRecord(envelope.extra)
  const style = typeof envelope.style === 'string' ? envelope.style : null
  return {
    mode: isAppearanceMode(envelope.mode) ? envelope.mode : null,
    density: extra && isAppearanceDensity(extra.density) ? extra.density : null,
    style,
  }
}

function readEnvelope(): PersistedAppearance | null {
  if (typeof window === 'undefined') return null
  try {
    return parseThemeEnvelope(window.localStorage.getItem(THEME_STORAGE_KEY))
  } catch {
    return null
  }
}

/** 首屏已持久化的主题模式；null 表示用户从未选过（交给 DEFAULT_APPEARANCE.mode）。 */
export function readStoredMode(): AppearanceMode | null {
  return readEnvelope()?.mode ?? null
}

/** 首屏已持久化的密度；null 表示从未选过。 */
export function readStoredDensity(): AppearanceDensity | null {
  return readEnvelope()?.density ?? null
}

/** 把密度写到根元素上（幂等，可在 SSR 外任意调用）。 */
export function applyDensity(value: AppearanceDensity, root?: HTMLElement): void {
  const doc = typeof document === 'undefined' ? null : document
  const element = root ?? doc?.documentElement
  if (!element) return
  element.setAttribute(DENSITY_ATTR, value)
}

/** 全局密度状态：设置页与首屏脚本共用，App 挂载时把持久化值灌进来。 */
export const appearanceDensity = ref<AppearanceDensity>(
  readStoredDensity() ?? DEFAULT_APPEARANCE.density,
)

export function setDensity(value: AppearanceDensity): void {
  const next = isAppearanceDensity(value) ? value : DEFAULT_APPEARANCE.density
  appearanceDensity.value = next
  applyDensity(next)
}


/* ---------- 品牌色（预设 / 自定义） ---------- */

/** 品牌色独立存储：与主题 envelope 解耦，避免 provider 的 extra 回填覆盖。 */
export const BRAND_STORAGE_KEY = 'leadstack.brand'

function readStoredBrand(): string {
  if (typeof window === 'undefined') return BRAND_PRESETS[0].color
  try {
    return normalizeBrandColor(window.localStorage.getItem(BRAND_STORAGE_KEY) ?? '') ?? BRAND_PRESETS[0].color
  } catch {
    return BRAND_PRESETS[0].color
  }
}

export const appearanceBrand = ref<string>(readStoredBrand())
export const appearanceBrandScale = computed(() => resolveBrandScale(appearanceBrand.value))
export const appearanceBrandPreset = computed(() => brandPreset(appearanceBrandScale.value))

/** 设置品牌色；返回 false 表示不是合法 #RRGGBB。 */
export function setBrandColor(input: string): boolean {
  const normalized = normalizeBrandColor(input)
  if (!normalized) return false
  appearanceBrand.value = normalized
  try {
    window.localStorage.setItem(BRAND_STORAGE_KEY, normalized)
  } catch {
    /* ignore */
  }
  return true
}
