import { useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { cn } from '../lib/cn'
import { CloseIcon } from './Icons'
import { IconButton } from './Button'
import { t } from '../i18n'

interface ModalProps {
  open: boolean
  title: string
  description?: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  size?: 'md' | 'lg' | 'xl'
}

export function Modal({ open, title, description, onClose, children, footer, size = 'md' }: ModalProps) {
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      document.body.style.overflow = previousOverflow
    }
  }, [open, onClose])

  if (!open) return null

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-end justify-center p-0 sm:items-center sm:p-4">
      <div
        className="absolute inset-0 bg-slate-900/40 backdrop-blur-sm dark:bg-black/60"
        onClick={onClose}
        aria-hidden="true"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        className={cn(
          'relative flex max-h-[92vh] w-full flex-col overflow-hidden rounded-t-xl border border-slate-200 bg-white shadow-2xl dark:border-slate-800 dark:bg-slate-900 sm:rounded-xl',
          size === 'xl' ? 'sm:max-w-5xl' : size === 'lg' ? 'sm:max-w-2xl' : 'sm:max-w-lg',
        )}
      >
        <div className="flex items-start justify-between gap-4 border-b border-slate-200 px-5 py-4 dark:border-slate-800">
          <div className="min-w-0">
            <h2 id="modal-title" className="text-base font-semibold text-slate-900 dark:text-slate-100">
              {title}
            </h2>
            {description ? (
              <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{description}</p>
            ) : null}
          </div>
          <IconButton label={t('s.0264')} onClick={onClose} className="-mr-1 -mt-1">
            <CloseIcon className="h-5 w-5" />
          </IconButton>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>

        {footer ? (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-slate-200 px-5 py-3.5 dark:border-slate-800">
            {footer}
          </div>
        ) : null}
      </div>
    </div>,
    document.body,
  )
}
