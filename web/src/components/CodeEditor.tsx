import { useEffect, useRef, useState } from 'react'
import { cn } from '../lib/cn'

interface CodeEditorProps {
  value: string
  onChange: (value: string) => void
  /** 缩进宽度（空格数），Tab 键插入这么多空格而不是切换焦点 */
  indentSize?: number
  rows?: number
  placeholder?: string
  readOnly?: boolean
  ariaLabel?: string
}

/**
 * 轻量代码编辑器：等宽字体、保留缩进、Tab 插入缩进、自动行号显示。
 * 不引额外编辑器依赖（保持前端依赖克制）。
 */
export function CodeEditor({
  value,
  onChange,
  indentSize = 2,
  rows = 14,
  placeholder,
  readOnly = false,
  ariaLabel = '代码编辑器',
}: CodeEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const gutterRef = useRef<HTMLDivElement | null>(null)
  const [focused, setFocused] = useState(false)

  const lineCount = Math.max(value.split('\n').length, 1)

  // 行号跟随滚动
  useEffect(() => {
    const textarea = textareaRef.current
    const gutter = gutterRef.current
    if (!textarea || !gutter) return
    const sync = () => {
      gutter.scrollTop = textarea.scrollTop
    }
    textarea.addEventListener('scroll', sync)
    return () => textarea.removeEventListener('scroll', sync)
  }, [])

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    const textarea = event.currentTarget
    if (event.key !== 'Tab') return
    event.preventDefault()

    const indent = ' '.repeat(indentSize)
    const { selectionStart, selectionEnd } = textarea

    // 有选中内容：整块缩进 / 反缩进
    if (selectionStart !== selectionEnd && textarea.value.slice(selectionStart, selectionEnd).includes('\n')) {
      const blockStart = textarea.value.lastIndexOf('\n', selectionStart - 1) + 1
      const selected = textarea.value.slice(blockStart, selectionEnd)
      const shifted = event.shiftKey
        ? selected
            .split('\n')
            .map((line) => (line.startsWith(indent) ? line.slice(indent.length) : line.replace(/^\s/, '')))
            .join('\n')
        : selected
            .split('\n')
            .map((line) => indent + line)
            .join('\n')
      const next = textarea.value.slice(0, blockStart) + shifted + textarea.value.slice(selectionEnd)
      onChange(next)
      requestAnimationFrame(() => {
        textarea.selectionStart = blockStart
        textarea.selectionEnd = blockStart + shifted.length
      })
      return
    }

    const before = textarea.value.slice(0, selectionStart)
    const after = textarea.value.slice(selectionEnd)
    onChange(`${before}${indent}${after}`)
    requestAnimationFrame(() => {
      textarea.selectionStart = selectionStart + indent.length
      textarea.selectionEnd = selectionStart + indent.length
    })
  }

  return (
    <div
      className={cn(
        'flex overflow-hidden rounded-md border bg-slate-50 dark:bg-slate-950',
        focused ? 'border-indigo-500 ring-2 ring-indigo-500/20' : 'border-slate-300 dark:border-slate-700',
      )}
    >
      <div
        ref={gutterRef}
        aria-hidden="true"
        className="max-h-[70vh] shrink-0 select-none overflow-hidden border-r border-slate-200 bg-slate-100 px-2 py-2.5 text-right font-mono text-xs leading-relaxed text-slate-400 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-500"
      >
        {Array.from({ length: lineCount }, (_, index) => (
          <div key={index}>{index + 1}</div>
        ))}
      </div>
      <textarea
        ref={textareaRef}
        value={value}
        aria-label={ariaLabel}
        readOnly={readOnly}
        rows={rows}
        spellCheck={false}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        className="max-h-[70vh] min-h-[8rem] w-full resize-y bg-transparent px-3 py-2.5 font-mono text-xs leading-relaxed text-slate-800 outline-none placeholder:text-slate-400 dark:text-slate-100 dark:placeholder:text-slate-500"
      />
    </div>
  )
}
