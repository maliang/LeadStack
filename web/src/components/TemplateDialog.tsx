import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, TEMPLATE_PLACEHOLDERS, type DeployTemplate, type TemplateKind } from '../lib/api'
import { cn } from '../lib/cn'
import { errorMessage } from '../lib/errors'
import { SAMPLE_TEMPLATES, TEMPLATE_KIND_LABELS } from '../lib/sampleTemplates'
import { Button } from './Button'
import { CodeEditor } from './CodeEditor'
import { TemplateKindBadge } from './DeployBadges'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const KINDS: TemplateKind[] = ['binary', 'systemd', 'compose']

function defaultVars(slug: string): Record<string, string> {
  const releaseId = '20261019-120000-a1b2c3'
  const root = `/opt/leadstack/apps/${slug}`
  return {
    APP_SLUG: slug,
    VERSION: '1.0.0',
    RELEASE_ID: releaseId,
    ROOT: root,
    RELEASE_DIR: `${root}/releases/${releaseId}`,
    ARTIFACT_URL: 'https://example.com/downloads/app-1.0.0.tar.gz',
    ARTIFACT_SHA256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    PREV_RELEASE_ID: '20261018-090000-d4e5f6',
  }
}

interface TemplateDialogProps {
  open: boolean
  template: DeployTemplate | null
  onClose: () => void
  onSaved: () => void
}

/** 模板编辑器：等宽代码编辑（Tab 缩进）+ 三种类型示例一键填充 + 渲染预览。 */
export function TemplateDialog({ open, template, onClose, onSaved }: TemplateDialogProps) {
  const { toast } = useToast()
  const editing = template !== null

  const [name, setName] = useState('')
  const [kind, setKind] = useState<TemplateKind>('binary')
  const [content, setContent] = useState('')
  const [savedId, setSavedId] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [vars, setVars] = useState<Record<string, string>>(() => defaultVars('demo-app'))
  const [rendered, setRendered] = useState<string | null>(null)
  const [renderLoading, setRenderLoading] = useState(false)
  const [renderError, setRenderError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(template?.name ?? '')
    setKind((template?.kind as TemplateKind) ?? 'binary')
    setContent(template?.content ?? '')
    setSavedId(template?.id ?? null)
    setSaving(false)
    setError(null)
    setVars(defaultVars('demo-app'))
    setRendered(null)
    setRenderLoading(false)
    setRenderError(null)
  }, [open, template])

  const activeId = savedId ?? template?.id ?? null
  const placeholderNames = useMemo(() => new Set(TEMPLATE_PLACEHOLDERS.map((item) => item.name)), [])

  const unknownPlaceholders = useMemo(() => {
    const found = new Set<string>()
    const regex = /\{\{\s*\.?([A-Z0-9_]+)\s*\}\}/g
    let match: RegExpExecArray | null
    while ((match = regex.exec(content)) !== null) {
      if (!placeholderNames.has(match[1])) found.add(match[1])
    }
    return [...found]
  }, [content, placeholderNames])

  const handleFillSample = () => {
    const sample = SAMPLE_TEMPLATES[kind]
    setContent(sample.content)
    if (!name.trim()) setName(sample.name)
    toast(t('s.0549', { '0': TEMPLATE_KIND_LABELS[kind] }), 'info')
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!name.trim()) {
      setError(t('s.0550'))
      return
    }
    if (!content.trim()) {
      setError(t('s.0551'))
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (activeId) {
        await api.updateTemplate(activeId, { name: name.trim(), kind, content })
        toast(t('s.0552', { '0': name.trim() }), 'success')
        setSavedId(activeId)
      } else {
        const created = await api.createTemplate({ name: name.trim(), kind, content })
        setSavedId(created.id)
        toast(t('s.0553', { '0': created.name }), 'success')
      }
      onSaved()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0554')))
    } finally {
      setSaving(false)
    }
  }

  const handleRender = async () => {
    if (!activeId) {
      // 未保存：先创建再渲染（渲染端点只能对已存在的模板使用）
      if (!name.trim() || !content.trim()) {
        setRenderError(t('s.0555'))
        return
      }
      setRenderLoading(true)
      setRenderError(null)
      try {
        const created = await api.createTemplate({ name: name.trim(), kind, content })
        setSavedId(created.id)
        onSaved()
        const preview = await api.renderTemplate(created.id, vars)
        setRendered(preview)
        toast(t('s.0556'), 'success')
      } catch (caught) {
        setRenderError(errorMessage(caught, t('s.0557')))
      } finally {
        setRenderLoading(false)
      }
      return
    }
    setRenderLoading(true)
    setRenderError(null)
    try {
      const preview = await api.renderTemplate(activeId, vars)
      setRendered(preview)
    } catch (caught) {
      setRenderError(errorMessage(caught, t('s.0557')))
    } finally {
      setRenderLoading(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('s.0558') : t('s.0559')}
      description={t('s.0560')}
      size="xl"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('s.0264')}
          </Button>
          <Button type="submit" form="template-form" loading={saving}>
            {activeId ? t('s.0128') : t('s.0561')}
          </Button>
        </>
      }
    >
      <form id="template-form" onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-2">
            <label htmlFor="template-name" className={labelClass}>
              {t('s.0562')}
            </label>
            <input
              id="template-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0563')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <span className={labelClass}>{t('s.0399')}</span>
            <div className="mt-1.5 flex gap-1.5">
              {KINDS.map((option) => (
                <button
                  key={option}
                  type="button"
                  onClick={() => setKind(option)}
                  className={cn(
                    'flex-1 rounded-md border px-2 py-1.5 text-xs font-medium transition-colors',
                    kind === option
                      ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                      : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
                  )}
                >
                  {option}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className={labelClass}>
                {t('s.0564')}
                <span className="ml-1.5 font-normal text-slate-400">{t('s.0565')} {2} {t('s.0566')}</span>
              </span>
              <div className="flex items-center gap-2">
                <TemplateKindBadge kind={kind} />
                <button
                  type="button"
                  onClick={handleFillSample}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  {t('s.0567')}
                </button>
              </div>
            </div>
            <CodeEditor
              value={content}
              onChange={setContent}
              rows={16}
              ariaLabel={t('s.0564')}
              placeholder={t('s.0568')}
            />
            {unknownPlaceholders.length > 0 ? (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                {t('s.0569')}{unknownPlaceholders.map((item) => `{{${item}}}`).join('、')}
                {t('s.0570')}
              </p>
            ) : null}
          </div>

          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className={labelClass}>{t('s.0571')}</span>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void handleRender()}
                loading={renderLoading}
              >
                {activeId ? t('s.0571') : t('s.0572')}
              </Button>
            </div>

            <div className="rounded-lg border border-slate-200 p-2.5 dark:border-slate-800">
              <p className="text-xs text-slate-500 dark:text-slate-400">
                {t('s.0573')}
              </p>
              <div className="mt-1.5 grid grid-cols-1 gap-1.5 sm:grid-cols-2">
                {TEMPLATE_PLACEHOLDERS.map((placeholder) => (
                  <label key={placeholder.name} className="block" title={placeholder.description}>
                    <span className="block font-mono text-[11px] text-slate-500 dark:text-slate-400">
                      {`{{${placeholder.name}}}`}
                    </span>
                    <input
                      value={vars[placeholder.name] ?? ''}
                      onChange={(event) =>
                        setVars((current) => ({ ...current, [placeholder.name]: event.target.value }))
                      }
                      className="mt-0.5 w-full rounded border border-slate-300 bg-white px-2 py-1 font-mono text-[11px] text-slate-700 outline-none focus:border-indigo-500 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-200"
                    />
                  </label>
                ))}
              </div>
            </div>

            {renderError ? (
              <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                {t('s.0574')}{renderError}
              </p>
            ) : null}

            <div className="rounded-md border border-slate-200 bg-slate-50 p-2.5 dark:border-slate-800 dark:bg-slate-950">
              <p className="text-xs text-slate-500 dark:text-slate-400">{t('s.0575')}</p>
              <pre className="mt-1.5 max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-slate-700 dark:text-slate-200">
                {rendered ?? t('s.0576')}
              </pre>
            </div>
          </div>
        </div>

        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </form>
    </Modal>
  )
}
