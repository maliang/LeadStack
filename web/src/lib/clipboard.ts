/**
 * 把文本写入剪贴板。
 *
 * 安全上下文（HTTPS / localhost）下优先使用 `navigator.clipboard`；
 * 不可用或不安全上下文（如内网 http 访问）时回落到隐藏 textarea +
 * `document.execCommand('copy')`，与 `CopyButton` 的降级写法保持一致。
 *
 * @returns 是否写入成功；调用方据此决定提示成功还是失败，禁止静默吞掉失败。
 */
export async function writeClipboard(value: string): Promise<boolean> {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    /* 权限被拒或接口不可用，继续走 execCommand 降级 */
  }

  let textarea: HTMLTextAreaElement | null = null
  try {
    textarea = document.createElement('textarea')
    textarea.value = value
    textarea.setAttribute('readonly', '')
    textarea.style.position = 'fixed'
    textarea.style.top = '0'
    textarea.style.left = '-9999px'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    textarea.setSelectionRange(0, textarea.value.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    textarea?.remove()
  }
}
