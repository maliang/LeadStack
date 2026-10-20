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
    toast(`已填充「${TEMPLATE_KIND_LABELS[kind]}」示例模板`, 'info')
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!name.trim()) {
      setError('请填写模板名称')
      return
    }
    if (!content.trim()) {
      setError('模板内容不能为空')
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (activeId) {
        await api.updateTemplate(activeId, { name: name.trim(), kind, content })
        toast(`模板「${name.trim()}」已更新`, 'success')
        setSavedId(activeId)
      } else {
        const created = await api.createTemplate({ name: name.trim(), kind, content })
        setSavedId(created.id)
        toast(`模板「${created.name}」已创建`, 'success')
      }
      onSaved()
    } catch (caught) {
      setError(errorMessage(caught, '保存模板失败'))
    } finally {
      setSaving(false)
    }
  }

  const handleRender = async () => {
    if (!activeId) {
      // 未保存：先创建再渲染（渲染端点只能对已存在的模板使用）
      if (!name.trim() || !content.trim()) {
        setRenderError('请先填写模板名称与内容')
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
        toast('模板已保存并生成渲染预览', 'success')
      } catch (caught) {
        setRenderError(errorMessage(caught, '渲染预览失败'))
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
      setRenderError(errorMessage(caught, '渲染预览失败'))
    } finally {
      setRenderLoading(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? '编辑模板' : '新建模板'}
      description="模板是版本化文本，发布时会冻结快照；占位符用 {{VAR}} 表示，未知占位符渲染会报错。"
      size="xl"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            关闭
          </Button>
          <Button type="submit" form="template-form" loading={saving}>
            {activeId ? '保存修改' : '创建模板'}
          </Button>
        </>
      }
    >
      <form id="template-form" onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-2">
            <label htmlFor="template-name" className={labelClass}>
              模板名称
            </label>
            <input
              id="template-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder="例如：Go 二进制标准发布"
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <span className={labelClass}>类型</span>
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
                模板内容
                <span className="ml-1.5 font-normal text-slate-400">Tab 插入 {2} 空格缩进</span>
              </span>
              <div className="flex items-center gap-2">
                <TemplateKindBadge kind={kind} />
                <button
                  type="button"
                  onClick={handleFillSample}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  填充示例模板
                </button>
              </div>
            </div>
            <CodeEditor
              value={content}
              onChange={setContent}
              rows={16}
              ariaLabel="模板内容"
              placeholder={'# 在这里编写部署步骤\nset -e\necho "deploy {{VERSION}} to {{RELEASE_DIR}}"'}
            />
            {unknownPlaceholders.length > 0 ? (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                检测到未知占位符：{unknownPlaceholders.map((item) => `{{${item}}}`).join('、')}
                （渲染时后端会报错，请改成下方可用占位符）
              </p>
            ) : null}
          </div>

          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className={labelClass}>渲染预览</span>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void handleRender()}
                loading={renderLoading}
              >
                {activeId ? '渲染预览' : '保存并预览'}
              </Button>
            </div>

            <div className="rounded-lg border border-slate-200 p-2.5 dark:border-slate-800">
              <p className="text-xs text-slate-500 dark:text-slate-400">
                可用占位符（点击填入变量值）：
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
                渲染失败：{renderError}
              </p>
            ) : null}

            <div className="rounded-md border border-slate-200 bg-slate-50 p-2.5 dark:border-slate-800 dark:bg-slate-950">
              <p className="text-xs text-slate-500 dark:text-slate-400">渲染结果（占位符替换后，不会执行）</p>
              <pre className="mt-1.5 max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-slate-700 dark:text-slate-200">
                {rendered ?? '（尚未渲染：点击右上角「渲染预览」，新模板会先保存再渲染）'}
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
