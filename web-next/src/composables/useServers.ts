import { onScopeDispose, ref, type Ref } from 'vue'
import { api, type Server, type ServerStatus } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { t } from '@/i18n'

/**
 * 服务器列表的数据 owner：加载、30s 轮询、骨架/空/失败三态。
 *
 * 筛选是纯前端行为（旧界面的分组/状态/标签/关键字语义），这里只导出纯函数，
 * 组件不各写一套过滤或状态映射；接口仍然只有 api.servers()，不新增后端查询参数。
 */

/** 列表自动刷新间隔。 */
export const SERVERS_POLL_MS = 30_000

/** 视图偏好（卡片/列表）的 localStorage 键；默认列表。 */
export const SERVERS_VIEW_STORAGE_KEY = 'leadstack.servers.view'

export type ServersView = 'card' | 'list'
export type ServersStatus = 'loading' | 'ready' | 'error'

/** 纯前端筛选条件（空串 = 不筛选）。 */
export interface ServerFilterState {
  group: string
  status: '' | ServerStatus
  tag: string
  keyword: string
}

export function emptyServerFilters(): ServerFilterState {
  return { group: '', status: '', tag: '', keyword: '' }
}

export function readServersView(): ServersView {
  try {
    return window.localStorage.getItem(SERVERS_VIEW_STORAGE_KEY) === 'card' ? 'card' : 'list'
  } catch {
    return 'list'
  }
}

export function writeServersView(view: ServersView): void {
  try {
    window.localStorage.setItem(SERVERS_VIEW_STORAGE_KEY, view)
  } catch {
    /* 隐私模式等场景写不进去也不影响当前会话 */
  }
}

/** 分组筛选项：兼容 server.group（单个）与授权分组 group_names（多个）。 */
export function collectGroupOptions(servers: readonly Server[]): string[] {
  const names = new Set<string>()
  for (const server of servers) {
    if (server.group) names.add(server.group)
    for (const name of server.group_names) names.add(name)
  }
  return [...names].sort((left, right) => left.localeCompare(right))
}

export function collectTagOptions(servers: readonly Server[]): string[] {
  const tags = new Set<string>()
  for (const server of servers) for (const tag of server.tags) tags.add(tag)
  return [...tags].sort((left, right) => left.localeCompare(right))
}

function matchesGroup(server: Server, group: string): boolean {
  if (server.group === group) return true
  return server.group_names.includes(group)
}

/**
 * 旧界面的筛选语义：分组 / 状态 / 标签为精确命中，关键字对名称、主机、系统、分组、标签
 * 做大小写不敏感的子串匹配。条件之间是 AND。
 */
export function filterServers(
  servers: readonly Server[],
  filters: ServerFilterState,
): Server[] {
  const keyword = filters.keyword.trim().toLowerCase()
  return servers.filter((server) => {
    if (filters.group && !matchesGroup(server, filters.group)) return false
    if (filters.status && server.status !== filters.status) return false
    if (filters.tag && !server.tags.includes(filters.tag)) return false
    if (keyword) {
      const haystack = [
        server.name,
        server.host,
        server.os ?? '',
        server.arch ?? '',
        server.group ?? '',
        ...server.group_names,
        ...server.tags,
      ]
        .join('\n')
        .toLowerCase()
      if (!haystack.includes(keyword)) return false
    }
    return true
  })
}

export function hasActiveServerFilters(filters: ServerFilterState): boolean {
  return Boolean(filters.group || filters.status || filters.tag || filters.keyword.trim())
}

/** 状态到 i18n 文案键的映射（只此一处，toolbar / 卡片 / 行共用）。 */
export function serverStatusLabelKey(status: ServerStatus): string {
  if (status === 'online') return 's.0521'
  if (status === 'offline') return 's.0522'
  return 's.0089'
}

/** 状态到语义色的映射：正常/警示/错误各一色，未知走中性。 */
export function serverStatusTone(status: ServerStatus): 'success' | 'danger' | 'muted' {
  if (status === 'online') return 'success'
  if (status === 'offline') return 'danger'
  return 'muted'
}

export interface UseServers {
  servers: Ref<Server[]>
  status: Ref<ServersStatus>
  /** 首屏失败/轮询失败的可见文案（已带 requestId） */
  error: Ref<string | null>
  /** 首次请求是否已完成：区分骨架态与真正的空态 */
  loaded: Ref<boolean>
  /** 正在进行一次请求（手动重试按钮的禁用/旋转态） */
  refreshing: Ref<boolean>
  refresh: () => Promise<void>
}

export function useServers(): UseServers {
  const servers = ref<Server[]>([])
  const status = ref<ServersStatus>('loading')
  const error = ref<string | null>(null)
  const loaded = ref(false)
  const refreshing = ref(false)

  let inflight: Promise<void> | null = null
  let disposed = false
  let timer: ReturnType<typeof setInterval> | null = null

  const refresh = (): Promise<void> => {
    if (inflight) return inflight
    refreshing.value = true
    inflight = (async () => {
      try {
        const data = await api.servers()
        if (disposed) return
        servers.value = data
        loaded.value = true
        status.value = 'ready'
        error.value = null
      } catch (caught) {
        if (disposed) return
        // 锁在列表上的失败不能把已加载数据清空：首屏显示错误页，轮询失败显示顶部横幅。
        error.value = errorMessage(caught, t('common.error'))
        if (!loaded.value) status.value = 'error'
      } finally {
        if (!disposed) refreshing.value = false
        inflight = null
      }
    })()
    return inflight
  }

  timer = setInterval(() => {
    if (document.visibilityState === 'hidden') return
    void refresh()
  }, SERVERS_POLL_MS)

  onScopeDispose(() => {
    disposed = true
    if (timer !== null) clearInterval(timer)
    timer = null
  })

  void refresh()

  return { servers, status, error, loaded, refreshing, refresh }
}
