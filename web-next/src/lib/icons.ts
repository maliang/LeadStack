/**
 * 内联 SVG 图标注册表（替代 web/src/components/Icons.tsx 的 React 版本）。
 *
 * 刻意不依赖 Iconify 运行时拉取：控制台由 Go 二进制在内网/离线环境托管，
 * 图标必须随包内联，不能依赖 CDN。失量数据与旧版 Icons.tsx 保持一致。
 */

export type IconTag = 'path' | 'rect' | 'circle'

export interface IconNode {
  tag: IconTag
  attrs: Record<string, string | number>
}

const ICON_PATHS = {
  dashboard: [
    { tag: 'rect', attrs: { x: 3, y: 3, width: 7, height: 9, rx: 1.5 } },
    { tag: 'rect', attrs: { x: 14, y: 3, width: 7, height: 5, rx: 1.5 } },
    { tag: 'rect', attrs: { x: 14, y: 12, width: 7, height: 9, rx: 1.5 } },
    { tag: 'rect', attrs: { x: 3, y: 16, width: 7, height: 5, rx: 1.5 } },
  ],
  server: [
    { tag: 'rect', attrs: { x: 3, y: 4, width: 18, height: 7, rx: 2 } },
    { tag: 'rect', attrs: { x: 3, y: 13, width: 18, height: 7, rx: 2 } },
    { tag: 'path', attrs: { d: 'M7 7.5h.01M7 16.5h.01' } },
  ],
  pulse: [{ tag: 'path', attrs: { d: 'M2 12h3.5l2-6 3.5 12 2.5-9 1.5 3H21' } }],
  bell: [
    {
      tag: 'path',
      attrs: { d: 'M18 8a6 6 0 1 0-12 0c0 5-2 6-2 6h16s-2-1-2-6' },
    },
    { tag: 'path', attrs: { d: 'M10.3 19a2 2 0 0 0 3.4 0' } },
  ],
  terminal: [
    { tag: 'rect', attrs: { x: 3, y: 4, width: 18, height: 16, rx: 2 } },
    { tag: 'path', attrs: { d: 'm7 9 3 3-3 3M13 15h4' } },
  ],
  folder: [
    {
      tag: 'path',
      attrs: {
        d: 'M2 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z',
      },
    },
  ],
  clock: [
    { tag: 'circle', attrs: { cx: 12, cy: 12, r: 9 } },
    { tag: 'path', attrs: { d: 'M12 7v5l3 2' } },
  ],
  'file-text': [
    {
      tag: 'path',
      attrs: { d: 'M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9Z' },
    },
    { tag: 'path', attrs: { d: 'M14 3v6h6M8 13h8M8 17h5' } },
  ],
  plus: [{ tag: 'path', attrs: { d: 'M12 5v14M5 12h14' } }],
  'list-checks': [
    { tag: 'path', attrs: { d: 'm3 6 2 2 3-3M3 14l2 2 3-3M3 20h4' } },
    { tag: 'path', attrs: { d: 'M13 7h8M13 15h8M13 21h8' } },
  ],
  play: [{ tag: 'path', attrs: { d: 'M7 4.5v15l12-7.5Z' } }],
  key: [
    { tag: 'circle', attrs: { cx: 8, cy: 15, r: 4 } },
    { tag: 'path', attrs: { d: 'm10.8 12.2 7.2-7.2M15.5 5.5 18 8M13 8l3 3' } },
  ],
  'key-round': [
    { tag: 'circle', attrs: { cx: 8, cy: 15, r: 4 } },
    { tag: 'path', attrs: { d: 'm10.8 12.2 8.2-8.2M16.5 4.5 19 7M14 7l2.5 2.5' } },
  ],
  users: [
    { tag: 'circle', attrs: { cx: 9, cy: 8, r: 3.5 } },
    {
      tag: 'path',
      attrs: {
        d: 'M5.5 20a6.5 6.5 0 0 1 13 0M16 5.2a3.5 3.5 0 0 1 0 6.6M18 14.5a6 6 0 0 1 3.5 5.5',
      },
    },
  ],
  'shield-check': [
    { tag: 'path', attrs: { d: 'M12 3l7 3v6c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6Z' } },
    { tag: 'path', attrs: { d: 'm9 12 2 2 4-4' } },
  ],
  archive: [
    { tag: 'rect', attrs: { x: 3, y: 4, width: 18, height: 4, rx: 1 } },
    { tag: 'path', attrs: { d: 'M3 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4' } },
  ],
  settings: [
    { tag: 'circle', attrs: { cx: 12, cy: 12, r: 3 } },
    {
      tag: 'path',
      attrs: {
        d: 'M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1.11-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09A1.7 1.7 0 0 0 4.6 8.9a1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87V9a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.51 1Z',
      },
    },
  ],
  menu: [{ tag: 'path', attrs: { d: 'M4 6h16M4 12h16M4 18h16' } }],
  close: [{ tag: 'path', attrs: { d: 'M6 6l12 12M18 6 6 18' } }],
  logout: [
    {
      tag: 'path',
      attrs: { d: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9' },
    },
  ],
  refresh: [
    { tag: 'path', attrs: { d: 'M20 11a8 8 0 1 0-2.34 5.66M20 5v6h-6' } },
  ],
  'alert-triangle': [
    {
      tag: 'path',
      attrs: { d: 'M10.3 3.9 2.5 17.4A2 2 0 0 0 4.2 20.4h15.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z' },
    },
    { tag: 'path', attrs: { d: 'M12 9v4M12 16.5h.01' } },
  ],
  'chevron-right': [{ tag: 'path', attrs: { d: 'm9 18 6-6-6-6' } }],
} as const satisfies Record<string, readonly IconNode[]>

export type IconName = keyof typeof ICON_PATHS

/** 取图标节点；未注册的名字返回空数组，渲染成空壳而不是抛错。 */
export function iconNodes(name: IconName): readonly IconNode[] {
  return ICON_PATHS[name] ?? []
}
