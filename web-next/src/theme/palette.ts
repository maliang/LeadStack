import type { ThemePresetColors } from '@vean/ui'

/** 可选的品牌色阶。键名与 CSS 变量 `--ls-brand-*` 一致。 */
export interface BrandScale {
  50: string
  100: string
  200: string
  300: string
  400: string
  500: string
  600: string
  700: string
  800: string
  900: string
  950: string
}

export interface BrandPreset {
  color: string
  labelKey: string
}

/** 默认品牌绿：保持既有设计稿的精确色值，不经过生成器。 */
export const DEFAULT_BRAND_SCALE: BrandScale = {
  50: '#f0fdf4',
  100: '#dcfce7',
  200: '#bbf7d0',
  300: '#86efac',
  400: '#4ade80',
  500: '#22c55e',
  600: '#16a34a',
  700: '#15803d',
  800: '#166534',
  900: '#14532d',
  950: '#052e16',
}

export const BRAND_PRESETS: BrandPreset[] = [
  { color: '#15803d', labelKey: 'appearance.brand.green' },
  { color: '#1d4ed8', labelKey: 'appearance.brand.blue' },
  { color: '#4338ca', labelKey: 'appearance.brand.indigo' },
  { color: '#6d28d9', labelKey: 'appearance.brand.violet' },
  { color: '#be123c', labelKey: 'appearance.brand.rose' },
  { color: '#b91c1c', labelKey: 'appearance.brand.red' },
  { color: '#c2410c', labelKey: 'appearance.brand.orange' },
  { color: '#b45309', labelKey: 'appearance.brand.amber' },
  { color: '#0f766e', labelKey: 'appearance.brand.teal' },
  { color: '#0e7490', labelKey: 'appearance.brand.cyan' },
  { color: '#334155', labelKey: 'appearance.brand.slate' },
]

const HEX_RE = /^#?([A-Za-z0-9]{2})([A-Za-z0-9]{2})([A-Za-z0-9]{2})$/

export function normalizeBrandColor(input: string): string | null {
  const match = HEX_RE.exec(String(input ?? '').trim())
  if (!match) return null
  return `#${match[1]}${match[2]}${match[3]}`.toLowerCase()
}

function hexToRgb(hex: string): [number, number, number] {
  return [
    Number.parseInt(hex.slice(1, 3), 16),
    Number.parseInt(hex.slice(3, 5), 16),
    Number.parseInt(hex.slice(5, 7), 16),
  ]
}

function rgbToHex(r: number, g: number, b: number): string {
  const part = (value: number): string => Math.max(0, Math.min(255, Math.round(value))).toString(16).padStart(2, '0')
  return `#${part(r)}${part(g)}${part(b)}`
}

function rgbToHsl(r: number, g: number, b: number): { h: number; s: number; l: number } {
  const rn = r / 255
  const gn = g / 255
  const bn = b / 255
  const max = Math.max(rn, gn, bn)
  const min = Math.min(rn, gn, bn)
  const l = (max + min) / 2
  if (max === min) return { h: 0, s: 0, l }
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h = 0
  if (max === rn) h = (gn - bn) / d + (gn < bn ? 6 : 0)
  else if (max === gn) h = (bn - rn) / d + 2
  else h = (rn - gn) / d + 4
  return { h: h * 60, s, l }
}

function hslToHex(h: number, s: number, l: number): string {
  const hue = ((h % 360) + 360) % 360
  const sat = Math.max(0, Math.min(1, s))
  const light = Math.max(0, Math.min(1, l))
  const c = (1 - Math.abs(2 * light - 1)) * sat
  const x = c * (1 - Math.abs(((hue / 60) % 2) - 1))
  const m = light - c / 2
  let r = 0
  let g = 0
  let b = 0
  if (hue < 60) [r, g, b] = [c, x, 0]
  else if (hue < 120) [r, g, b] = [x, c, 0]
  else if (hue < 180) [r, g, b] = [0, c, x]
  else if (hue < 240) [r, g, b] = [0, x, c]
  else if (hue < 300) [r, g, b] = [x, 0, c]
  else [r, g, b] = [c, 0, x]
  return rgbToHex((r + m) * 255, (g + m) * 255, (b + m) * 255)
}

function channelLuminance(value: number): number {
  const v = value / 255
  return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
}

function contrastWithWhite(hex: string): number {
  const [r, g, b] = hexToRgb(hex)
  const luminance = 0.2126 * channelLuminance(r) + 0.7152 * channelLuminance(g) + 0.0722 * channelLuminance(b)
  return 1.05 / (luminance + 0.05)
}

/** 生成一个品牌色阶；700 始终保证白字至少 4.5:1，避免自定义浅色不可读。 */
export function buildBrandScale(input: string): BrandScale {
  const normalized = normalizeBrandColor(input) ?? BRAND_PRESETS[0].color
  const { h, s, l } = rgbToHsl(...hexToRgb(normalized))
  let light700 = l
  let color700 = normalized
  // 白字对比度不足时逐步压暗 700 阶。
  while (contrastWithWhite(color700) < 4.5 && light700 > 0.18) {
    light700 -= 0.01
    color700 = hslToHex(h, s, light700)
  }
  const clampS = Math.max(0.25, Math.min(0.9, s))
  return {
    50: hslToHex(h, clampS * 0.35, 0.97),
    100: hslToHex(h, clampS * 0.45, 0.93),
    200: hslToHex(h, clampS * 0.6, 0.87),
    300: hslToHex(h, clampS * 0.72, 0.78),
    400: hslToHex(h, clampS * 0.82, 0.67),
    500: hslToHex(h, clampS * 0.9, Math.min(0.64, light700 + 0.22)),
    600: hslToHex(h, clampS * 0.92, Math.min(0.55, light700 + 0.12)),
    700: color700,
    800: hslToHex(h, clampS * 0.9, Math.max(0.12, light700 - 0.08)),
    900: hslToHex(h, clampS * 0.86, Math.max(0.09, light700 - 0.16)),
    950: hslToHex(h, clampS * 0.8, Math.max(0.05, light700 - 0.23)),
  }
}

/** 已知预设走同一生成器，保证自定义色与预设色观感一致。 */
export function resolveBrandScale(input: string | null | undefined): BrandScale {
  const normalized = normalizeBrandColor(input ?? '')
  if (!normalized) return DEFAULT_BRAND_SCALE
  if (normalized === BRAND_PRESETS[0].color) return DEFAULT_BRAND_SCALE
  return buildBrandScale(normalized)
}

/** 把色阶映射成 @vean/ui 的语义 token（按钮填充 / 焦点环 / 侧栏）。 */
export function brandPreset(scale: BrandScale): ThemePresetColors {
  return {
    light: {
      primary: scale[700],
      'primary-foreground': '#ffffff',
      ring: scale[500],
      'sidebar-primary': scale[700],
      'sidebar-primary-foreground': '#ffffff',
    },
    dark: {
      primary: scale[700],
      'primary-foreground': '#ffffff',
      ring: scale[500],
      'sidebar-primary': scale[700],
      'sidebar-primary-foreground': '#ffffff',
    },
  } as unknown as ThemePresetColors
}

/** 把色阶写到根 CSS 变量；深色模式单独映射，避免浅色阶在深底上失读。 */
export function applyBrandVars(scale: BrandScale, mode: 'light' | 'dark', root?: HTMLElement): void {
  const doc = typeof document === 'undefined' ? null : document
  const element = root ?? doc?.documentElement
  if (!element) return
  const vars: Record<string, string> =
    mode === 'dark'
      ? {
          '--ls-brand-50': scale[950],
          '--ls-brand-100': scale[900],
          '--ls-brand-200': scale[800],
          '--ls-brand-300': scale[700],
          '--ls-brand-400': scale[600],
          '--ls-brand-500': scale[500],
          '--ls-brand-600': scale[400],
          '--ls-brand-700': scale[400],
          '--ls-brand-800': scale[300],
          '--ls-brand-900': scale[200],
          '--ls-brand-950': scale[100],
          '--ls-primary': scale[400],
          '--ls-primary-fill': scale[700],
          '--ls-primary-contrast': '#ffffff',
          '--ls-primary-soft': scale[950],
          '--ls-primary-hover-bg': scale[800],
          '--ls-primary-hover-contrast': '#ffffff',
        }
      : {
          '--ls-brand-50': scale[50],
          '--ls-brand-100': scale[100],
          '--ls-brand-200': scale[200],
          '--ls-brand-300': scale[300],
          '--ls-brand-400': scale[400],
          '--ls-brand-500': scale[500],
          '--ls-brand-600': scale[600],
          '--ls-brand-700': scale[700],
          '--ls-brand-800': scale[800],
          '--ls-brand-900': scale[900],
          '--ls-brand-950': scale[950],
          '--ls-primary': scale[700],
          '--ls-primary-fill': scale[700],
          '--ls-primary-contrast': '#ffffff',
          '--ls-primary-soft': scale[50],
          '--ls-primary-hover-bg': scale[800],
          '--ls-primary-hover-contrast': '#ffffff',
        }
  for (const [name, value] of Object.entries(vars)) element.style.setProperty(name, value)
}
