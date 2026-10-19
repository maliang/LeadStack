import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { cn } from '../lib/cn'
import { SpinnerIcon } from './Icons'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'
type Size = 'sm' | 'md'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
  loading?: boolean
  icon?: ReactNode
}

const variantStyles: Record<Variant, string> = {
  primary:
    'border border-transparent bg-indigo-600 text-white shadow-sm hover:bg-indigo-500 focus-visible:outline-indigo-600 disabled:hover:bg-indigo-600 dark:bg-indigo-500 dark:hover:bg-indigo-400',
  secondary:
    'border border-slate-200 bg-white text-slate-700 shadow-sm hover:bg-slate-50 focus-visible:outline-slate-400 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800',
  ghost:
    'border border-transparent bg-transparent text-slate-600 hover:bg-slate-100 focus-visible:outline-slate-400 dark:text-slate-300 dark:hover:bg-slate-800',
  danger:
    'border border-transparent bg-rose-600 text-white shadow-sm hover:bg-rose-500 focus-visible:outline-rose-600 dark:bg-rose-500 dark:hover:bg-rose-400',
}

const sizeStyles: Record<Size, string> = {
  sm: 'h-8 gap-1.5 px-2.5 text-xs',
  md: 'h-9 gap-2 px-3.5 text-sm',
}

export function Button({
  variant = 'primary',
  size = 'md',
  loading = false,
  icon,
  className,
  children,
  disabled,
  type = 'button',
  ...props
}: ButtonProps) {
  return (
    <button
      type={type}
      disabled={disabled || loading}
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-md font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 disabled:cursor-not-allowed disabled:opacity-50',
        variantStyles[variant],
        sizeStyles[size],
        className,
      )}
      {...props}
    >
      {loading ? <SpinnerIcon className="h-4 w-4" /> : icon}
      {children}
    </button>
  )
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string
  tone?: 'default' | 'danger'
}

export function IconButton({ label, tone = 'default', className, children, type = 'button', ...props }: IconButtonProps) {
  return (
    <button
      type={type}
      title={label}
      aria-label={label}
      className={cn(
        'inline-flex h-8 w-8 items-center justify-center rounded-md transition-colors focus-visible:outline-2 focus-visible:outline-offset-2',
        tone === 'danger'
          ? 'text-slate-400 hover:bg-rose-50 hover:text-rose-600 focus-visible:outline-rose-500 dark:text-slate-500 dark:hover:bg-rose-500/10 dark:hover:text-rose-400'
          : 'text-slate-400 hover:bg-slate-100 hover:text-slate-700 focus-visible:outline-slate-400 dark:text-slate-500 dark:hover:bg-slate-800 dark:hover:text-slate-200',
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
}
