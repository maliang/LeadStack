import type { ThemePresetColors } from '@vean/ui'

/**
 * 品牌绿主题（唯一色彩来源）。
 *
 * 为什么需要这一层：@vean/ui 的 `theme.primary` 只接受 26 个内置调色板名
 * （slate mist gray zinc neutral stone taupe olive mauve red orange amber yellow
 * lime green emerald teal cyan sky blue indigo violet purple fuchsia pink rose），
 * 传不了自定义色；品牌色的唯一入口是 `theme.preset`（ThemePresetColors）——
 * 按语义 token 给出 light/dark 两组覆盖。SConfigProvider 会把 preset 物化成
 * `overrides` 再交给主题引擎（见 @vean/ui dist/components/config-provider/shared.js
 * 的 `presetToOverrides` / `resolveThemeOptions`），所以 preset 能盖住声明式规则，
 * 但**不会改写调色板本身**；`primary` 仍要选一个内置名（这里用 green）来给
 * `--primary-50…950` 角色阶和 chart 派生兜底。
 *
 * 品牌绿色阶（Tailwind green 家族；与旧版的 emerald 青绿 #10b981/#059669 明确区分）：
 *   50  #f0fdf4
 *   100 #dcfce7
 *   500 #22c55e  ← 焦点环
 *   600 #16a34a  ← 品牌主色（浅色模式 logo 渐变起点）
 *   700 #15803d  ← 主按钮填充（浅色 / 深色统一）
 *   800 #166534  ← 主按钮 hover
 *   900 #14532d  ← 主按钮 active
 *   950 #052e16
 *
 * 对比度自查（WCAG AA 正文 4.5:1，sRGB 相对亮度）：
 *   #ffffff on #15803d = 5.02:1 ✓（绿底白字，浅色 / 深色统一）
 *   #ffffff on #16a34a = 3.30:1 ✗（600 太亮，不能配白字）
 *   #ffffff on #166534 = 7.13:1 ✓（hover）
 * 因此主按钮填充固定为 700、hover 到 800、active 到 900；600/500 只用于
 * logo 渐变与焦点环，不承担正文对比度。
 */
export const BRAND_SCALE = {
  50: '#f0fdf4',
  100: '#dcfce7',
  500: '#22c55e',
  600: '#16a34a',
  700: '#15803d',
  800: '#166534',
  900: '#14532d',
  950: '#052e16',
} as const

/**
 * 语义 token 覆盖。浅色 / 深色都用 700 做填充、白字（5.02:1）、500 做焦点环；
 * hover/active 由 `--ls-primary-hover-bg` / `brand-900` 负责。
 *
 * `sidebar-primary*` 是 mirror 规则，mirror 在 overrides 之前解析，所以必须在这里
 * 再声明一次，否则侧栏品牌色会停在内置 green.500 上。
 */
export const BRAND_PRESET: ThemePresetColors = {
  light: {
    primary: 'green.700',
    'primary-foreground': 'white',
    ring: 'green.500',
    'sidebar-primary': 'green.700',
    'sidebar-primary-foreground': 'white',
  },
  dark: {
    primary: 'green.700',
    'primary-foreground': 'white',
    ring: 'green.500',
    'sidebar-primary': 'green.700',
    'sidebar-primary-foreground': 'white',
  },
}

/**
 * 主题选项由 App.vue 组装（`primary: 'green'` + `preset: BRAND_PRESET`）：
 * primary 让内置角色阶（--primary-50…950 / chart-N）与品牌绿一致，
 * preset 负责精确的 token 语义。
 */
