/**
 * 服务器详情页的面板契约（深链 `?tab=` 与页内切换共用一份）。
 *
 * 页面只拿这里的结果读写 URL，避免「URL 里能写、页面不认」：
 * - 读：normalizeServerDetailTab(route.query.tab)，未知值一律回 overview；
 * - 写：SERVER_DETAIL_TAB_QUERY[tab]；overview 是默认值，写回时省略参数让地址干净。
 *
 * 与路由记录分离：这是页面状态契约，不是路由定义；由 src/router/index.ts 统一再导出，
 * 调用方仍从 '@/router' 引入，不用改 import 路径。
 */
export type ServerDetailTab =
  | 'overview'
  | 'access'
  | 'accounts'
  | 'login'
  | 'meta'
  | 'checks'
  | 'terminal'
  | 'files'
  | 'logs'

export const SERVER_DETAIL_TABS: readonly ServerDetailTab[] = [
  'overview',
  'access',
  'accounts',
  'login',
  'meta',
  'checks',
  'terminal',
  'files',
  'logs',
]

export const SERVER_DETAIL_TAB_QUERY: Record<ServerDetailTab, { tab: ServerDetailTab } | null> = {
  overview: null,
  access: { tab: 'access' },
  accounts: { tab: 'accounts' },
  login: { tab: 'login' },
  meta: { tab: 'meta' },
  checks: { tab: 'checks' },
  terminal: { tab: 'terminal' },
  files: { tab: 'files' },
  logs: { tab: 'logs' },
}

/** 深链值归一化：`?tab=logs` / `?tab=files` 等原样认，其他（含数组首值）回 overview。 */
export function normalizeServerDetailTab(raw: unknown): ServerDetailTab {
  const value = Array.isArray(raw) ? raw[0] : raw
  return typeof value === 'string' && (SERVER_DETAIL_TABS as readonly string[]).includes(value)
    ? (value as ServerDetailTab)
    : 'overview'
}
