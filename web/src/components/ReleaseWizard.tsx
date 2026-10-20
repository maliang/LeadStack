import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type AppItem,
  type Artifact,
  type DeployTemplate,
  type GroupSummary,
  type Release,
  type Server,
} from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { formatBytes } from '../lib/format'
import { Button } from './Button'
import { ArtifactKindBadge, TemplateKindBadge } from './DeployBadges'
import { ModeBadge } from './StatusBadge'
import { Modal } from './Modal'
import { useToast } from './Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const STEPS = ['基本信息', '制品', '目标服务器', '确认发布'] as const

interface ReleaseWizardProps {
  open: boolean
  apps: AppItem[]
  templates: DeployTemplate[]
  servers: Server[]
  groups: GroupSummary[]
  defaultAppId?: string
  onClose: () => void
  onCreated: (release: Release) => void
}

/** 发起发布向导：应用 → 模板 → 版本 → 制品 → 目标服务器 → 确认。 */
export function ReleaseWizard({
  open,
  apps,
  templates,
  servers,
  groups,
  defaultAppId,
  onClose,
  onCreated,
}: ReleaseWizardProps) {
  const { toast } = useToast()
  const [step, setStep] = useState(0)
  const [appId, setAppId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [version, setVersion] = useState('')
  const [artifactMode, setArtifactMode] = useState<'url' | 'upload'>('url')
  const [artifactUrl, setArtifactUrl] = useState('')
  const [artifactSha, setArtifactSha] = useState('')
  const [artifactFile, setArtifactFile] = useState<File | null>(null)
  const [artifact, setArtifact] = useState<Artifact | null>(null)
  const [uploading, setUploading] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [pickerGroup, setPickerGroup] = useState('')
  const [pickerTag, setPickerTag] = useState('')
  const [concurrency, setConcurrency] = useState('4')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setStep(0)
    setAppId(defaultAppId ?? apps[0]?.id ?? '')
    setTemplateId(templates[0]?.id ?? '')
    setVersion('')
    setArtifactMode('url')
    setArtifactUrl('')
    setArtifactSha('')
    setArtifactFile(null)
    setArtifact(null)
    setUploading(false)
    setSelected(new Set())
    setSearch('')
    setPickerGroup('')
    setPickerTag('')
    setConcurrency('4')
    setSubmitting(false)
    setError(null)
  }, [open, defaultAppId, apps, templates])

  const app = apps.find((item) => item.id === appId) ?? null
  const template = templates.find((item) => item.id === templateId) ?? null

  const tagOptions = useMemo(() => {
    const tags = new Set<string>()
    for (const server of servers) for (const tag of server.tags) tags.add(tag)
    return [...tags].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }, [servers])

  const filteredServers = useMemo(() => {
    const query = search.trim().toLowerCase()
    return servers.filter((server) => {
      if (pickerGroup && server.group !== pickerGroup) return false
      if (pickerTag && !server.tags.includes(pickerTag)) return false
      if (!query) return true
      return (
        server.name.toLowerCase().includes(query) ||
        server.host.toLowerCase().includes(query) ||
        server.tags.some((tag) => tag.toLowerCase().includes(query))
      )
    })
  }, [servers, search, pickerGroup, pickerTag])

  const prepareArtifact = async (): Promise<Artifact | null> => {
    if (artifact) return artifact
    setUploading(true)
    setError(null)
    try {
      const created =
        artifactMode === 'upload'
          ? artifactFile
            ? await api.uploadArtifact(artifactFile)
            : null
          : artifactUrl.trim()
            ? await api.registerUrlArtifact(artifactUrl.trim(), artifactSha.trim())
            : null
      if (!created) {
        setError(artifactMode === 'upload' ? '请选择要上传的制品文件' : '请填写制品 URL')
        return null
      }
      setArtifact(created)
      toast(`制品已就绪${created.sha256 ? ` · sha256 ${created.sha256.slice(0, 12)}…` : ''}`, 'success')
      return created
    } catch (caught) {
      setError(errorMessage(caught, '制品准备失败'))
      return null
    } finally {
      setUploading(false)
    }
  }

  const goNext = async () => {
    setError(null)
    if (step === 0) {
      if (!appId) {
        setError('请选择应用；还没有应用请先在应用页创建')
        return
      }
      if (!templateId) {
        setError('请选择模板；还没有模板请先在模板标签页创建')
        return
      }
      if (!/^[A-Za-z0-9._-]{1,64}$/.test(version.trim())) {
        setError('版本号只能包含字母、数字、点、下划线、短横线，长度 1-64')
        return
      }
      setStep(1)
      return
    }
    if (step === 1) {
      if (artifactMode === 'url' && !/^[0-9a-fA-F]{64}$/.test(artifactSha.trim())) {
        setError('URL 制品必须填写 64 位十六进制 sha256（可用 shasum -a 256 <文件> 计算）')
        return
      }
      const prepared = artifact ? artifact : await prepareArtifact()
      if (!prepared) return
      setStep(2)
      return
    }
    if (step === 2) {
      if (selected.size === 0) {
        setError('请至少选择一台目标服务器')
        return
      }
      setStep(3)
    }
  }

  const handleSubmit = async () => {
    if (!app || !template || !artifact) {
      setError('发布信息不完整，请返回上一步检查')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const response = await api.createRelease({
        app_id: app.id,
        template_id: template.id,
        version: version.trim(),
        artifact_id: artifact.id,
        server_ids: [...selected],
        concurrency: Number(concurrency) || 4,
      })
      toast(`发布 ${app.name} v${version.trim()} 已创建，正在执行`, 'success')
      onCreated(response.release)
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, '创建发布失败'))
    } finally {
      setSubmitting(false)
    }
  }

  const footer = (
    <>
      <Button variant="secondary" onClick={step === 0 ? onClose : () => setStep(step - 1)} disabled={submitting}>
        {step === 0 ? '取消' : '上一步'}
      </Button>
      {step < STEPS.length - 1 ? (
        <Button onClick={() => void goNext()} loading={uploading}>
          {step === 1 ? '准备制品并继续' : '下一步'}
        </Button>
      ) : (
        <Button onClick={() => void handleSubmit()} loading={submitting}>
          确认发布
        </Button>
      )}
    </>
  )

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="发起发布"
      description="选择应用与模板、准备制品、挑选目标服务器；创建后立即跳转发布详情页并可实时看日志。"
      size="lg"
      footer={footer}
    >
      <div className="mb-4 flex flex-wrap items-center gap-2">
        {STEPS.map((label, index) => (
          <span
            key={label}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium',
              index === step
                ? 'bg-indigo-600 text-white'
                : index < step
                  ? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400',
            )}
          >
            {index + 1}. {label}
          </span>
        ))}
      </div>

      {step === 0 ? (
        <div className="space-y-4">
          <div>
            <label htmlFor="release-app" className={labelClass}>
              应用
            </label>
            <select
              id="release-app"
              value={appId}
              onChange={(event) => setAppId(event.target.value)}
              className={inputClass + ' mt-1.5'}
            >
              <option value="">请选择应用</option>
              {apps.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}（{item.slug}）
                </option>
              ))}
            </select>
            {apps.length === 0 ? (
              <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
                还没有应用，请先在应用页创建。
              </p>
            ) : null}
          </div>
          <div>
            <label htmlFor="release-template" className={labelClass}>
              部署模板
            </label>
            <select
              id="release-template"
              value={templateId}
              onChange={(event) => setTemplateId(event.target.value)}
              className={inputClass + ' mt-1.5'}
            >
              <option value="">请选择模板</option>
              {templates.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}（{item.kind}）
                </option>
              ))}
            </select>
            {template ? (
              <p className="mt-1.5 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                <TemplateKindBadge kind={template.kind} />
                发布时会冻结该模板快照，之后改模板不影响本次发布。
              </p>
            ) : null}
          </div>
          <div>
            <label htmlFor="release-version" className={labelClass}>
              版本号
            </label>
            <input
              id="release-version"
              value={version}
              onChange={(event) => setVersion(event.target.value)}
              maxLength={64}
              placeholder="例如：1.4.2 或 2026.10.19-a1b2c3"
              className={inputClass + ' mt-1.5 font-mono text-xs'}
            />
          </div>
        </div>
      ) : null}

      {step === 1 ? (
        <div className="space-y-4">
          <div className="flex gap-2">
            {(
              [
                { value: 'url' as const, label: 'URL 制品' },
                { value: 'upload' as const, label: '上传文件' },
              ]
            ).map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => {
                  setArtifactMode(option.value)
                  setArtifact(null)
                  setError(null)
                }}
                className={cn(
                  'rounded-lg border px-3 py-1.5 text-sm font-medium transition-colors',
                  artifactMode === option.value
                    ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
                )}
              >
                {option.label}
              </button>
            ))}
          </div>

          {artifactMode === 'url' ? (
            <div>
              <label htmlFor="artifact-url" className={labelClass}>
                制品下载地址
              </label>
              <input
                id="artifact-url"
                value={artifactUrl}
                onChange={(event) => {
                  setArtifactUrl(event.target.value)
                  setArtifact(null)
                }}
                placeholder="https://example.com/downloads/app-1.4.2.tar.gz"
                className={inputClass + ' mt-1.5 font-mono text-xs'}
              />
              <div className="mt-3">
                <label htmlFor="artifact-sha256" className={labelClass}>
                  制品 SHA256
                  <span className="ml-1 text-rose-500">*</span>
                </label>
                <input
                  id="artifact-sha256"
                  value={artifactSha}
                  onChange={(event) => {
                    setArtifactSha(event.target.value)
                    setArtifact(null)
                  }}
                  placeholder="64 位十六进制，例如 e3b0c442…（macOS: shasum -a 256 <文件>；Linux: sha256sum <文件>）"
                  className={inputClass + ' mt-1.5 font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  目标机下载后用它在模板里校验（{'{{ARTIFACT_SHA256}}'}）；校验不通过不会切换 current，回滚才有保障，因此后端强制要求。
                </p>
              </div>
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                目标机部署时从这里下载；模板里用 {'{{ARTIFACT_URL}}'} 与 {'{{ARTIFACT_SHA256}}'}。
              </p>
            </div>
          ) : (
            <div>
              <label htmlFor="artifact-file" className={labelClass}>
                上传制品文件
              </label>
              <input
                id="artifact-file"
                type="file"
                onChange={(event) => {
                  setArtifactFile(event.target.files?.[0] ?? null)
                  setArtifact(null)
                }}
                className="mt-1.5 block w-full text-sm text-slate-600 file:mr-3 file:rounded-md file:border-0 file:bg-indigo-50 file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-indigo-700 dark:text-slate-300 dark:file:bg-indigo-500/10 dark:file:text-indigo-300"
              />
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                上传后控制端会计算 sha256，并生成可下载地址供目标机拉取。
              </p>
            </div>
          )}

          {artifact ? (
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200">
              <ArtifactKindBadge kind={artifact.kind} />
              <span className="font-mono">{artifact.location}</span>
              {artifact.sha256 ? <span className="font-mono">sha256 {artifact.sha256.slice(0, 16)}…</span> : null}
              <span>{formatBytes(artifact.size_bytes)}</span>
            </div>
          ) : null}
        </div>
      ) : null}

      {step === 2 ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className={labelClass}>
              目标服务器
              <span className="ml-1.5 font-normal text-slate-400">
                已选 {selected.size} / {servers.length}
              </span>
            </span>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() =>
                  setSelected((current) => {
                    const next = new Set(current)
                    for (const server of filteredServers) next.add(server.id)
                    return next
                  })
                }
                className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
              >
                全选当前筛选（{filteredServers.length}）
              </button>
              <button
                type="button"
                onClick={() => setSelected(new Set())}
                className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400"
              >
                清空
              </button>
            </div>
          </div>

          <div className="grid gap-2 sm:grid-cols-4">
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索名称 / 主机 / 标签"
              className={inputClass + ' sm:col-span-2'}
            />
            <select
              aria-label="按分组筛选目标服务器"
              value={pickerGroup}
              onChange={(event) => setPickerGroup(event.target.value)}
              className={inputClass}
            >
              <option value="">全部分组</option>
              {groups.map((group) => (
                <option key={group.name} value={group.name}>
                  {group.name}（{group.count}）
                </option>
              ))}
            </select>
            <select
              aria-label="按标签筛选目标服务器"
              value={pickerTag}
              onChange={(event) => setPickerTag(event.target.value)}
              className={inputClass}
            >
              <option value="">全部标签</option>
              {tagOptions.map((tag) => (
                <option key={tag} value={tag}>
                  {tag}
                </option>
              ))}
            </select>
          </div>

          <div className="max-h-56 overflow-y-auto rounded-md border border-slate-100 dark:border-slate-800/70">
            {filteredServers.length === 0 ? (
              <p className="px-3 py-4 text-center text-xs text-slate-400 dark:text-slate-500">
                当前筛选条件下没有服务器
              </p>
            ) : (
              filteredServers.map((server) => (
                <label
                  key={server.id}
                  className="flex cursor-pointer items-center gap-2.5 border-b border-slate-100 px-3 py-2 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/70 dark:hover:bg-slate-900/60"
                >
                  <input
                    type="checkbox"
                    checked={selected.has(server.id)}
                    onChange={() =>
                      setSelected((current) => {
                        const next = new Set(current)
                        if (next.has(server.id)) next.delete(server.id)
                        else next.add(server.id)
                        return next
                      })
                    }
                    className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
                  />
                  <span className="min-w-0 flex-1 truncate text-sm text-slate-800 dark:text-slate-100">
                    {server.name}
                  </span>
                  <ModeBadge mode={server.mode} />
                  {server.group ? (
                    <span className="text-[11px] text-slate-400 dark:text-slate-500">{server.group}</span>
                  ) : null}
                  <span className="font-mono text-[11px] text-slate-400 dark:text-slate-500">
                    {server.host}
                  </span>
                </label>
              ))
            )}
          </div>

          <div className="flex items-center gap-3">
            <label htmlFor="release-concurrency" className="text-sm text-slate-600 dark:text-slate-300">
              并发数
            </label>
            <input
              id="release-concurrency"
              value={concurrency}
              onChange={(event) => setConcurrency(event.target.value)}
              inputMode="numeric"
              className={cn(inputClass, 'w-24')}
            />
            <span className="text-xs text-slate-400 dark:text-slate-500">
              同时执行的服务器数量，机器多时建议 4-8，避免压满带宽
            </span>
          </div>
        </div>
      ) : null}

      {step === 3 ? (
        <dl className="space-y-2 rounded-lg border border-slate-200 p-3 text-sm dark:border-slate-800">
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">应用</dt>
            <dd className="text-slate-800 dark:text-slate-100">
              {app ? `${app.name}（${app.slug}）` : '—'}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">模板</dt>
            <dd className="flex items-center gap-2 text-slate-800 dark:text-slate-100">
              {template?.name ?? '—'} {template ? <TemplateKindBadge kind={template.kind} /> : null}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">版本</dt>
            <dd className="font-mono text-slate-800 dark:text-slate-100">{version || '—'}</dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">制品</dt>
            <dd className="flex items-center gap-2 truncate font-mono text-xs text-slate-800 dark:text-slate-100">
              {artifact ? artifact.location : '—'}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">目标服务器</dt>
            <dd className="text-slate-800 dark:text-slate-100">{selected.size} 台</dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">并发</dt>
            <dd className="text-slate-800 dark:text-slate-100">{concurrency}</dd>
          </div>
          <p className="pt-1 text-xs text-slate-400 dark:text-slate-500">
            发布会在每台机器执行模板步骤（严格串行、失败自动回滚）；创建后可立即在详情页看实时日志。
          </p>
        </dl>
      ) : null}

      {error ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {error}
        </p>
      ) : null}
    </Modal>
  )
}
