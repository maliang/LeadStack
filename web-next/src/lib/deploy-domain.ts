import type { AppItem, CreateAppPayload, Release, ReleaseStatus } from './api'

/**
 * 发布域纯函数：筛选、状态映射、表单校验。
 *
 * 页面与对话框只调用这里的函数，不在模板里各写一套
 * 「状态 → 文案 / 颜色」映射，也不在事件处理里散落校验规则，
 * 这样同一份口径能被冒烟脚本与后续单测直接断言。
 */

/** 应用表单值（全部是字符串，方便直接绑 input）。 */
export interface AppFormValues {
  name: string
  slug: string
  root: string
  description: string
  deployMode: string
  healthcheckPath: string
  restartCmd: string
}

export type AppFormErrors = Partial<Record<keyof AppFormValues, string>>

export function emptyAppForm(): AppFormValues {
  return {
    name: '',
    slug: '',
    root: '',
    description: '',
    deployMode: 'release',
    healthcheckPath: '',
    restartCmd: '',
  }
}

export function appFormFromItem(app: AppItem): AppFormValues {
  return {
    name: app.name,
    slug: app.slug,
    root: app.root ?? '',
    description: app.description ?? '',
    deployMode: app.deploy_mode || 'release',
    healthcheckPath: app.healthcheck_path ?? '',
    restartCmd: app.restart_cmd ?? '',
  }
}

const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{0,62}$/

/** 必填与格式校验：返回 i18n 键（页面用 t() 渲染），空对象表示通过。 */
export function validateAppForm(values: AppFormValues): AppFormErrors {
  const errors: AppFormErrors = {}
  const name = values.name.trim()
  const slug = values.slug.trim()
  if (name === '') errors.name = 'apps.form.nameRequired'
  else if (name.length > 64) errors.name = 'apps.form.nameTooLong'
  if (slug === '') errors.slug = 'apps.form.slugRequired'
  else if (!SLUG_PATTERN.test(slug.toLowerCase())) errors.slug = 'apps.form.slugInvalid'
  return errors
}

export function appPayloadFromForm(values: AppFormValues): CreateAppPayload {
  const mode = values.deployMode === 'inplace' ? 'inplace' : values.deployMode === 'script' ? 'script' : 'release'
  return {
    name: values.name.trim(),
    slug: values.slug.trim().toLowerCase(),
    root: values.root.trim() || undefined,
    description: values.description.trim() || undefined,
    deploy_mode: mode,
    healthcheck_path: values.healthcheckPath.trim() || null,
    restart_cmd: values.restartCmd.trim() || null,
  }
}

/** 应用搜索文本：名称 / 标识 / 根目录 / 描述，任一命中即可。 */
export function appSearchText(app: AppItem): string {
  return [app.name, app.slug, app.root ?? '', app.description ?? ''].join(' ').toLowerCase()
}

export function filterApps(apps: readonly AppItem[], keyword: string): AppItem[] {
  const needle = keyword.trim().toLowerCase()
  if (needle === '') return apps.slice()
  return apps.filter((app) => appSearchText(app).includes(needle))
}

export function deployModeLabelKey(mode: string | null | undefined): string {
  if (mode === 'inplace') return 'apps.mode.inplace'
  if (mode === 'script') return 'apps.mode.script'
  return 'apps.mode.release'
}

/* ------------------------------ 发布单 ------------------------------ */

export const RELEASE_STATUSES: ReleaseStatus[] = ['pending', 'running', 'success', 'failed', 'canceled']

/** 状态文案键；未知状态回退到 pending 的文案，不把原始枚举塞给用户。 */
export function releaseStatusLabelKey(status: string | null | undefined): string {
  return RELEASE_STATUSES.includes(status as ReleaseStatus)
    ? `releases.status.${status}`
    : 'releases.status.pending'
}

/** 状态 → 语义色调（颜色只有这三档 + 中性，避免同义异色）。 */
export function releaseStatusTone(status: string | null | undefined): 'success' | 'danger' | 'warning' | 'muted' {
  if (status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running' || status === 'pending') return 'warning'
  return 'muted'
}

export interface ReleaseStats {
  success: number
  failed: number
  active: number
  canceled: number
  total: number
}

export function releaseStats(release: Release): ReleaseStats {
  const runs = release.runs ?? []
  let success = 0
  let failed = 0
  let active = 0
  let canceled = 0
  for (const run of runs) {
    if (run.status === 'success') success += 1
    else if (run.status === 'failed') failed += 1
    else if (run.status === 'canceled') canceled += 1
    else active += 1
  }
  return { success, failed, active, canceled, total: runs.length }
}

/** app_id → 应用名：列表页一次拉取应用列表后建表复用，避免每行发一次请求。 */
export function buildAppNameMap(apps: readonly AppItem[]): Map<string, string> {
  const map = new Map<string, string>()
  for (const app of apps) map.set(app.id, app.name)
  return map
}

/**
 * 发布单的应用名：优先 release 自带的 app_name/app_slug；
 * 后端列表 / 详情只返回 app_id，此时用应用列表映射出的名字兜底；
 * 映射不到（应用列表不可读或应用已删）才回退 app_id。
 */
export function releaseAppLabel(release: Release, appNames?: ReadonlyMap<string, string> | null): string {
  return release.app_name || release.app_slug || appNames?.get(release.app_id) || release.app_id
}

/** 发布时长（秒）；未开始 / 未结束返回 null，由调用方显示占位符。 */
export function releaseDurationSeconds(release: Release): number | null {
  if (!release.started_at || !release.finished_at) return null
  return Math.max(0, Math.round((release.finished_at - release.started_at) / 1000))
}

export function releaseSearchText(release: Release, appNames?: ReadonlyMap<string, string> | null): string {
  return [releaseAppLabel(release, appNames), release.version, release.id].join(' ').toLowerCase()
}

export function filterReleases(
  releases: readonly Release[],
  filters: {
    keyword?: string
    appId?: string
    status?: string
    /** 关键词要按界面显示的应用名匹配，所以搜索也需要同一张 app_id → 名称表。 */
    appNames?: ReadonlyMap<string, string> | null
  } = {},
): Release[] {
  const needle = (filters.keyword ?? '').trim().toLowerCase()
  return releases.filter((release) => {
    if (filters.appId && release.app_id !== filters.appId) return false
    if (filters.status && release.status !== filters.status) return false
    if (needle !== '' && !releaseSearchText(release, filters.appNames).includes(needle)) return false
    return true
  })
}

/** 灰度先发台数的上限：至少给剩余批次留一台。 */
export function canaryMaxFor(serverCount: number): number {
  return Math.max(1, serverCount - 1)
}

/* ------------------------------ 发布表单 ------------------------------ */

export interface ReleaseFormValues {
  appId: string
  version: string
  templateId: string
  artifactId: string
  serverIds: string[]
  strategy: 'all' | 'canary'
  canaryCount: string
  holdMinutes: string
}

export type ReleaseFormErrors = Partial<Record<keyof ReleaseFormValues, string>>

export function emptyReleaseForm(appId = ''): ReleaseFormValues {
  return {
    appId,
    version: '',
    templateId: '',
    artifactId: '',
    serverIds: [],
    strategy: 'all',
    canaryCount: '1',
    holdMinutes: '0',
  }
}

const VERSION_PATTERN = /^\d+(\.\d+){0,3}([-+][0-9A-Za-z.-]+)?$/

export function validateReleaseForm(values: ReleaseFormValues): ReleaseFormErrors {
  const errors: ReleaseFormErrors = {}
  if (values.appId.trim() === '') errors.appId = 'apps.releaseForm.appRequired'
  if (values.version.trim() === '') errors.version = 'apps.releaseForm.versionRequired'
  else if (!VERSION_PATTERN.test(values.version.trim())) errors.version = 'apps.releaseForm.versionInvalid'
  if (values.templateId.trim() === '') errors.templateId = 'apps.releaseForm.templateRequired'
  if (values.serverIds.length === 0) errors.serverIds = 'apps.releaseForm.serversRequired'
  if (values.strategy === 'canary') {
    if (values.serverIds.length < 2) errors.canaryCount = 'release.canaryNeedTwo'
    else {
      const max = canaryMaxFor(values.serverIds.length)
      const count = Number(values.canaryCount)
      if (!Number.isInteger(count) || count < 1 || count > max) errors.canaryCount = 'release.canaryCountInvalid'
    }
    if (values.strategy === 'canary') {
      const hold = Number(values.holdMinutes)
      if (!Number.isInteger(hold) || hold < 0 || hold > 60) errors.holdMinutes = 'release.canaryHoldInvalid'
    }
  }
  return errors
}

export function releasePayloadFromForm(values: ReleaseFormValues): {
  app_id: string
  template_id: string
  version: string
  artifact_id: string
  server_ids: string[]
  strategy?: 'all' | 'canary'
  canary_count?: number
  hold_seconds?: number
} {
  const payload = {
    app_id: values.appId.trim(),
    template_id: values.templateId.trim(),
    version: values.version.trim(),
    artifact_id: values.artifactId.trim(),
    server_ids: values.serverIds.slice(),
  }
  if (values.strategy !== 'canary') return payload
  return {
    ...payload,
    strategy: 'canary',
    canary_count: Number(values.canaryCount),
    hold_seconds: Number(values.holdMinutes) * 60,
  }
}
