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
 *   500 #22c55e  ← 浅色模式 hover 的等价阶 / 焦点环
 *   600 #16a34a  ← 品牌主色，浅色模式填充
 *   700 #15803d  ← 深色模式填充：600 降亮一阶
 *   900 #14532d
 *   950 #052e16  ← 浅色模式按钮前景
 *
 * 对比度自查（WCAG AA 正文 4.5:1，sRGB 相对亮度）：
 *   #052e16 on #16a34a = 4.52:1 ✓（所以浅色模式前景必须是深绿）
 *   #ffffff on #16a34a = 3.30:1 ✗（旧版白字方案不达 AA）
 *   #f8fafc on #15803d = 4.87:1 ✓（深色模式）
 * 注意 Vean 没有 hover 语义 token：solid 按钮 hover 是 `bg-primary/80` 叠在页面上，
 * 600 叠白 ≈ #45b56e，与 #22c55e 同色相、介于 500–400 之间，仍落在品牌绿色阶内；
 * 这正是把填充放在 600、把更亮的 500 留给 hover/焦点环的原因。
 */
export const BRAND_SCALE = {
  50: '#f0fdf4',
  100: '#dcfce7',
  500: '#22c55e',
  600: '#16a34a',
  700: '#15803d',
  900: '#14532d',
  950: '#052e16',
} as const

/**
 * 语义 token 覆盖。浅色：品牌绿 600 做填充、950 做前景（AA 4.52:1）、500 做焦点环；
 * 深色：整体降亮一阶到 700，前景回到近白（4.87:1）。
 *
 * `sidebar-primary*` 是 mirror 规则，mirror 在 overrides 之前解析，所以必须在这里
 * 再声明一次，否则侧栏品牌色会停在内置 green.500 上。
 */
export const BRAND_PRESET: ThemePresetColors = {
  light: {
    primary: 'green.600',
    'primary-foreground': 'green.950',
    ring: 'green.500',
    'sidebar-primary': 'green.600',
    'sidebar-primary-foreground': 'green.950',
  },
  dark: {
    primary: 'green.700',
    'primary-foreground': 'gray.50',
    ring: 'green.500',
    'sidebar-primary': 'green.700',
    'sidebar-primary-foreground': 'gray.50',
  },
}

/**
 * 主题选项由 App.vue 组装（`primary: 'green'` + `preset: BRAND_PRESET`）：
 * primary 让内置角色阶（--primary-50…950 / chart-N）与品牌绿一致，
 * preset 负责精确的 token 语义。
 */
