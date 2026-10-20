import { useState } from 'react'
import { cn } from '../lib/cn'
import { Button } from './Button'
import { CheckIcon, CopyIcon } from './Icons'
import { t } from '../i18n'

interface CopyButtonProps {
  value: string
  label?: string
  className?: string
}

/** 复制失败时降级到隐藏 textarea + execCommand（非 HTTPS 环境）。 */
async function writeClipboard(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    /* fall through */
  }
  try {
    const textarea = document.createElement('textarea')
    textarea.value = value
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(textarea)
    return ok
  } catch {
    return false
  }
}

export function CopyButton({ value, label = t('s.0388'), className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    const ok = await writeClipboard(value)
    if (!ok) return
    setCopied(true)
    window.setTimeout(() => setCopied(false), 2_000)
  }

  return (
    <Button
      variant="secondary"
      size="sm"
      onClick={handleCopy}
      className={cn(copied && 'text-emerald-600 dark:text-emerald-300', className)}
      icon={copied ? <CheckIcon className="h-4 w-4" /> : <CopyIcon className="h-4 w-4" />}
    >
      {copied ? t('s.0389') : label}
    </Button>
  )
}
