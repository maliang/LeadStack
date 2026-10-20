import { useEffect, useState, type FormEvent } from 'react'
import { Button } from './Button'
import { Modal } from './Modal'
import { CloseIcon } from './Icons'
import { t } from '../i18n'

interface TagsDialogProps {
  open: boolean
  title: string
  description?: string
  initialTags: string[]
  loading?: boolean
  error?: string | null
  submitLabel?: string
  onSubmit: (tags: string[]) => void
  onClose: () => void
}

/** 标签编辑（行内单个服务器 / 批量追加共用）。 */
export function TagsDialog({
  open,
  title,
  description,
  initialTags,
  loading = false,
  error,
  submitLabel = t('s.0509'),
  onSubmit,
  onClose,
}: TagsDialogProps) {
  const [tags, setTags] = useState<string[]>([])
  const [draft, setDraft] = useState('')

  useEffect(() => {
    if (!open) return
    setTags(initialTags)
    setDraft('')
  }, [open, initialTags])

  const addTags = (raw: string) => {
    const parts = raw
      .split(/[,，\s]+/)
      .map((tag) => tag.trim())
      .filter(Boolean)
    if (parts.length === 0) return
    setTags((current) => {
      const next = [...current]
      for (const part of parts) {
        if (!next.includes(part)) next.push(part)
      }
      return next
    })
    setDraft('')
  }

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const pending = draft.trim() ? [...tags, ...draft.split(/[,，\s]+/).map((t) => t.trim()).filter((t) => t && !tags.includes(t))] : tags
    onSubmit(pending)
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={loading}>
            {t('s.0024')}
          </Button>
          <Button type="submit" form="tags-form" loading={loading}>
            {submitLabel}
          </Button>
        </>
      }
    >
      <form id="tags-form" onSubmit={handleSubmit} className="space-y-3">
        <div className="flex flex-wrap gap-1.5">
          {tags.length === 0 ? (
            <span className="text-xs text-slate-400 dark:text-slate-500">{t('s.0218')}</span>
          ) : (
            tags.map((tag) => (
              <span
                key={tag}
                className="inline-flex items-center gap-1 rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600 dark:bg-slate-800 dark:text-slate-300"
              >
                {tag}
                <button
                  type="button"
                  aria-label={t('s.0546', { '0': tag })}
                  onClick={() => setTags((current) => current.filter((item) => item !== tag))}
                  className="text-slate-400 hover:text-rose-500 dark:text-slate-500 dark:hover:text-rose-400"
                >
                  <CloseIcon className="h-3 w-3" />
                </button>
              </span>
            ))
          )}
        </div>

        <div>
          <label
            htmlFor="tags-input"
            className="block text-sm font-medium text-slate-700 dark:text-slate-200"
          >
            {t('s.0547')}
          </label>
          <input
            id="tags-input"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                addTags(draft)
              }
            }}
            placeholder={t('s.0548')}
            className="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
          />
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
