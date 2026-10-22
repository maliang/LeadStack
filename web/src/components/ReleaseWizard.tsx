import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type AppItem,
  type ReleasePreview,
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

import { t } from '../i18n'
const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const STEPS = [t('s.0441'), t('s.0442'), t('s.0443'), t('s.0444')] as const

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
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [preview, setPreview] = useState<ReleasePreview | null>(null)

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
        setError(artifactMode === 'upload' ? t('s.0445') : t('s.0446'))
        return null
      }
      setArtifact(created)
      toast(t('s.0447', { '0': created.sha256 ? ` · sha256 ${created.sha256.slice(0, 12)}…` : '' }), 'success')
      return created
    } catch (caught) {
      setError(errorMessage(caught, t('s.0448')))
      return null
    } finally {
      setUploading(false)
    }
  }

  const goNext = async () => {
    setError(null)
    if (step === 0) {
      if (!appId) {
        setError(t('s.0449'))
        return
      }
      if (!templateId) {
        setError(t('s.0450'))
        return
      }
      if (!/^[A-Za-z0-9._-]{1,64}$/.test(version.trim())) {
        setError(t('s.0451'))
        return
      }
      const selectedTemplate = templates.find((item) => item.id === templateId)
      if (scriptMode) {
        if (!selectedTemplate || selectedTemplate.kind !== 'binary' || selectedTemplate.content.trim() === '') {
          setError(t('release.scriptTemplateRequired'))
          return
        }
      }
      setStep(1)
      return
    }
    if (step === 1) {
      if (scriptMode) {
        setStep(2)
        return
      }
      if (artifactMode === 'url' && !/^[0-9a-fA-F]{64}$/.test(artifactSha.trim())) {
        setError(t('s.0452'))
        return
      }
      const prepared = artifact ? artifact : await prepareArtifact()
      if (!prepared) return
      setStep(2)
      return
    }
    if (step === 2) {
      if (selected.size === 0) {
        setError(t('s.0453'))
        return
      }
      setStep(3)
    }
  }

  const handleSubmit = async () => {
    if (!app || !template || (!scriptMode && !artifact)) {
      setError(t('s.0454'))
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const response = await api.createRelease({
        app_id: app.id,
        template_id: template.id,
        version: version.trim(),
        artifact_id: artifact?.id ?? '',
        server_ids: [...selected],
        concurrency: Number(concurrency) || 4,
      })
      toast(t('s.0455', { '0': app.name, '1': version.trim() }), 'success')
      onCreated(response.release)
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0456')))
    } finally {
      setSubmitting(false)
    }
  }

  const scriptMode = app?.deploy_mode === 'script'

  const handlePreview = async () => {
    if (!app || !template) return
    setPreviewLoading(true)
    setError(null)
    try {
      const result = await api.previewRelease({
        app_id: app.id,
        template_id: template.id,
        version: version.trim(),
        artifact_id: artifact?.id,
        server_ids: [...selected],
      })
      setPreview(result)
      setPreviewOpen(true)
    } catch (caught) {
      setError(errorMessage(caught, t('s.0456')))
    } finally {
      setPreviewLoading(false)
    }
  }

  const footer = (
    <>
      <Button variant="secondary" onClick={step === 0 ? onClose : () => setStep(step - 1)} disabled={submitting}>
        {step === 0 ? t('s.0024') : t('s.0027')}
      </Button>
      {step < STEPS.length - 1 ? (
        <Button onClick={() => void goNext()} loading={uploading}>
          {step === 1 ? t('s.0457') : t('s.0458')}
        </Button>
      ) : (
        <Button onClick={() => void handleSubmit()} loading={submitting}>
          {t('s.0444')}
        </Button>
      )}
    </>
  )

  return (
    <>
    <Modal
      open={open}
      onClose={onClose}
      title={t('s.0459')}
      description={t('s.0460')}
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
              {t('s.0461')}
            </label>
            <select
              id="release-app"
              value={appId}
              onChange={(event) => setAppId(event.target.value)}
              className={inputClass + ' mt-1.5'}
            >
              <option value="">{t('s.0462')}</option>
              {apps.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}（{item.slug}）
                </option>
              ))}
            </select>
            {apps.length === 0 ? (
              <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
                {t('s.0463')}
              </p>
            ) : null}
          </div>
          <div>
            <label htmlFor="release-template" className={labelClass}>
              {t('s.0464')}
            </label>
            <select
              id="release-template"
              value={templateId}
              onChange={(event) => setTemplateId(event.target.value)}
              className={inputClass + ' mt-1.5'}
            >
              <option value="">{t('s.0465')}</option>
              {templates.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}（{item.kind}）
                </option>
              ))}
            </select>
            {template ? (
              <p className="mt-1.5 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                <TemplateKindBadge kind={template.kind} />
                {t('s.0466')}
              </p>
            ) : null}
          </div>
          <div>
            <label htmlFor="release-version" className={labelClass}>
              {t('s.0467')}
            </label>
            <input
              id="release-version"
              value={version}
              onChange={(event) => setVersion(event.target.value)}
              maxLength={64}
              placeholder={t('s.0468')}
              className={inputClass + ' mt-1.5 font-mono text-xs'}
            />
          </div>
        </div>
      ) : null}

      {step === 1 ? (
        <div className="space-y-4">
          {scriptMode ? (
            <div className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2.5 text-xs leading-relaxed text-sky-900 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-200">
              {t('release.scriptNoArtifact')}
            </div>
          ) : null}
          {!scriptMode ? (
          <>
          <div className="flex gap-2">
            {(
              [
                { value: 'url' as const, label: t('s.0469') },
                { value: 'upload' as const, label: t('s.0408') },
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
                {t('s.0470')}
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
                  {t('s.0471')}
                  <span className="ml-1 text-rose-500">*</span>
                </label>
                <input
                  id="artifact-sha256"
                  value={artifactSha}
                  onChange={(event) => {
                    setArtifactSha(event.target.value)
                    setArtifact(null)
                  }}
                  placeholder={t('s.0472')}
                  className={inputClass + ' mt-1.5 font-mono text-xs'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('s.0473')}{'{{ARTIFACT_SHA256}}'}{t('s.0474')}
                </p>
              </div>
              <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                {t('s.0475')} {'{{ARTIFACT_URL}}'} {t('s.0476')} {'{{ARTIFACT_SHA256}}'}。
              </p>
            </div>
          ) : (
            <div>
              <label htmlFor="artifact-file" className={labelClass}>
                {t('s.0477')}
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
                {t('s.0478')}
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
          </>
          ) : null}
        </div>
      ) : null}

      {step === 2 ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className={labelClass}>
              {t('s.0443')}
              <span className="ml-1.5 font-normal text-slate-400">
                {t('s.0208')} {selected.size} / {servers.length}
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
                {t('s.0373')}{filteredServers.length}）
              </button>
              <button
                type="button"
                onClick={() => setSelected(new Set())}
                className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400"
              >
                {t('s.0210')}
              </button>
            </div>
          </div>

          <div className="grid gap-2 sm:grid-cols-4">
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('s.0214')}
              className={inputClass + ' sm:col-span-2'}
            />
            <select
              aria-label={t('s.0479')}
              value={pickerGroup}
              onChange={(event) => setPickerGroup(event.target.value)}
              className={inputClass}
            >
              <option value="">{t('s.0376')}</option>
              {groups.map((group) => (
                <option key={group.name} value={group.name}>
                  {group.name}（{group.count}）
                </option>
              ))}
            </select>
            <select
              aria-label={t('s.0480')}
              value={pickerTag}
              onChange={(event) => setPickerTag(event.target.value)}
              className={inputClass}
            >
              <option value="">{t('s.0378')}</option>
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
                {t('s.0380')}
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
              {t('s.0481')}
            </label>
            <input
              id="release-concurrency"
              value={concurrency}
              onChange={(event) => setConcurrency(event.target.value)}
              inputMode="numeric"
              className={cn(inputClass, 'w-24')}
            />
            <span className="text-xs text-slate-400 dark:text-slate-500">
              {t('s.0482')}
            </span>
          </div>
        </div>
      ) : null}

      {step === 3 ? (
        <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-indigo-200 bg-indigo-50/70 px-3 py-2 dark:border-indigo-500/30 dark:bg-indigo-500/10">
          <p className="text-xs text-indigo-900 dark:text-indigo-200">{t('release.previewHint')}</p>
          <Button size="sm" variant="secondary" onClick={() => void handlePreview()} loading={previewLoading}>
            {t('release.preview')}
          </Button>
        </div>
        <dl className="space-y-2 rounded-lg border border-slate-200 p-3 text-sm dark:border-slate-800">
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0461')}</dt>
            <dd className="text-slate-800 dark:text-slate-100">
              {app ? `${app.name}（${app.slug}）` : '—'}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0483')}</dt>
            <dd className="flex items-center gap-2 text-slate-800 dark:text-slate-100">
              {template?.name ?? '—'} {template ? <TemplateKindBadge kind={template.kind} /> : null}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0484')}</dt>
            <dd className="font-mono text-slate-800 dark:text-slate-100">{version || '—'}</dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0442')}</dt>
            <dd className="flex items-center gap-2 truncate font-mono text-xs text-slate-800 dark:text-slate-100">
              {artifact ? artifact.location : '—'}
            </dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0443')}</dt>
            <dd className="text-slate-800 dark:text-slate-100">{selected.size} {t('s.0280')}</dd>
          </div>
          <div className="flex justify-between gap-3">
            <dt className="text-slate-500 dark:text-slate-400">{t('s.0485')}</dt>
            <dd className="text-slate-800 dark:text-slate-100">{concurrency}</dd>
          </div>
          <p className="pt-1 text-xs text-slate-400 dark:text-slate-500">
            {t('s.0486')}
          </p>
        </dl>
        </div>
      ) : null}

      {error ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {error}
        </p>
      ) : null}
    </Modal>

    <Modal
      open={previewOpen}
      onClose={() => setPreviewOpen(false)}
      title={t('release.previewTitle')}
      description={t('release.previewDescription')}
      size="lg"
      footer={
        <Button variant="secondary" onClick={() => setPreviewOpen(false)}>
          {t('common.close')}
        </Button>
      }
    >
      {preview ? (
        <div className="space-y-3">
          <p className="text-xs text-slate-500 dark:text-slate-400">
            {preview.app_slug} · v{preview.version} · {preview.deploy_mode} · {preview.provider}
          </p>
          {preview.servers.map((plan) => (
            <section
              key={plan.server_id}
              className="rounded-lg border border-slate-200 p-3 dark:border-slate-800"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm font-medium text-slate-800 dark:text-slate-100">
                  {plan.server_name || plan.server_id}
                </p>
                {plan.plan_root ? (
                  <code className="max-w-[260px] truncate rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                    {plan.plan_root}
                  </code>
                ) : null}
              </div>
              <ol className="mt-2 space-y-1">
                {plan.steps.map((step, index) => (
                  <li
                    key={`${plan.server_id}-step-${index}`}
                    className="rounded border border-slate-100 px-2 py-1.5 text-xs dark:border-slate-800/70"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium text-slate-700 dark:text-slate-200">
                        {index + 1}. {step.name}
                      </span>
                      {step.timeout_s ? (
                        <span className="text-[11px] text-slate-400">{step.timeout_s}s</span>
                      ) : null}
                      {step.ignore_failure ? (
                        <span className="text-[11px] text-amber-600 dark:text-amber-400">
                          {t('release.previewIgnoreFailure')}
                        </span>
                      ) : null}
                    </div>
                    {step.secret ? (
                      <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                        {t('release.previewSecret')}
                      </span>
                    ) : step.command ? (
                      <pre className="mt-0.5 overflow-x-auto whitespace-pre-wrap break-words font-mono text-[11px] text-slate-600 dark:text-slate-300">
                        {step.command}
                      </pre>
                    ) : null}
                  </li>
                ))}
              </ol>
              {plan.rollback_steps.length > 0 ? (
                <details className="mt-2">
                  <summary className="cursor-pointer text-[11px] font-medium text-slate-500 dark:text-slate-400">
                    {t('release.previewRollback', { count: plan.rollback_steps.length })}
                  </summary>
                  <ol className="mt-1 space-y-1">
                    {plan.rollback_steps.map((step, index) => (
                      <li
                        key={`${plan.server_id}-rollback-${index}`}
                        className="rounded bg-rose-50/60 px-2 py-1 text-[11px] dark:bg-rose-500/10"
                      >
                        <span className="font-medium text-rose-800 dark:text-rose-200">
                          {step.name}
                        </span>
                        {step.secret ? (
                          <span className="ml-1 text-rose-500/70">（{t('release.previewSecret')}）</span>
                        ) : step.command ? (
                          <pre className="mt-0.5 whitespace-pre-wrap break-words font-mono text-[11px] text-rose-700/90 dark:text-rose-200/90">
                            {step.command}
                          </pre>
                        ) : null}
                      </li>
                    ))}
                  </ol>
                </details>
              ) : null}
              {plan.notes.length > 0 ? (
                <ul className="mt-2 space-y-0.5 rounded-md border border-amber-200 bg-amber-50 px-2.5 py-1.5 text-[11px] leading-relaxed text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
                  {plan.notes.map((note, index) => (
                    <li key={`${plan.server_id}-note-${index}`}>· {note}</li>
                  ))}
                </ul>
              ) : null}
            </section>
          ))}
        </div>
      ) : null}
    </Modal>
    </>
  )
}
