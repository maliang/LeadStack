/**
 * 发布说明的轻量 Markdown 渲染器（不引入第三方依赖）。
 *
 * 安全边界：先整体 HTML 转义，再只生成白名单标签（h1-h6 / p / ul / ol / li /
 * blockquote / pre / code / strong / em / a / hr / br）。发布说明来自本仓库的
 * Release body，但仍按不可信文本处理：绝不透传原始 HTML，链接只允许 http/https。
 *
 * 支持：标题、无序/有序列表、引用、围栏代码块、行内代码、粗体、链接、分隔线。
 */
export function renderReleaseNotes(source: string): string {
  const text = source
    .replace(/\r\n?/g, '\n')
    // 发布流水线写入的 schema_changed 标记不应出现在界面正文里。
    .replace(/<!--[\s\S]*?-->/g, '')
  const lines = text.split('\n')
  const out: string[] = []
  let paragraph: string[] = []
  let index = 0

  const flushParagraph = (): void => {
    if (paragraph.length === 0) return
    out.push(`<p>${inline(paragraph.join(' '))}</p>`)
    paragraph = []
  }

  while (index < lines.length) {
    const line = lines[index]
    const trimmed = line.trim()

    if (trimmed.startsWith('```')) {
      flushParagraph()
      const language = trimmed.slice(3).trim()
      const code: string[] = []
      index += 1
      while (index < lines.length && !lines[index].trim().startsWith('```')) {
        code.push(lines[index])
        index += 1
      }
      // 跳过结束围栏（缺失时也安全收尾）
      if (index < lines.length) index += 1
      const classAttr = language ? ` class="language-${escapeHtml(language)}"` : ''
      out.push(`<pre><code${classAttr}>${escapeHtml(code.join('\n'))}</code></pre>`)
      continue
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(trimmed)
    if (heading) {
      flushParagraph()
      const level = heading[1].length
      out.push(`<h${level}>${inline(heading[2].trim())}</h${level}>`)
      index += 1
      continue
    }

    if (/^\s*[-*]\s+/.test(line)) {
      flushParagraph()
      const items: string[] = []
      while (index < lines.length && /^\s*[-*]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*[-*]\s+/, '').trim())
        index += 1
      }
      out.push(`<ul>${items.map((item) => `<li>${inline(item)}</li>`).join('')}</ul>`)
      continue
    }

    if (/^\s*\d+\.\s+/.test(line)) {
      flushParagraph()
      const items: string[] = []
      while (index < lines.length && /^\s*\d+\.\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*\d+\.\s+/, '').trim())
        index += 1
      }
      out.push(`<ol>${items.map((item) => `<li>${inline(item)}</li>`).join('')}</ol>`)
      continue
    }

    if (/^>\s?/.test(trimmed)) {
      flushParagraph()
      const quotes: string[] = []
      while (index < lines.length && /^>\s?/.test(lines[index].trim())) {
        quotes.push(lines[index].trim().replace(/^>\s?/, ''))
        index += 1
      }
      out.push(`<blockquote>${inline(quotes.join(' '))}</blockquote>`)
      continue
    }

    if (/^\s*---+\s*$/.test(line)) {
      flushParagraph()
      out.push('<hr>')
      index += 1
      continue
    }

    if (trimmed === '') {
      flushParagraph()
      index += 1
      continue
    }

    paragraph.push(trimmed)
    index += 1
  }
  flushParagraph()
  return out.join('\n')
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/** 行内格式：先把代码片段抽出来，避免其中的 * / 链接语法被误解析。 */
function inline(value: string): string {
  let text = value
  const codes: string[] = []
  text = text.replace(/`([^`]+)`/g, (_match, code: string) => {
    codes.push(code)
    return `\u0000${codes.length - 1}\u0000`
  })
  text = escapeHtml(text)
  text = text.replace(
    /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
    (_match, label: string, url: string) =>
      `<a href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`,
  )
  text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  text = text.replace(/(^|[\s(])\*([^*\s][^*]*)\*/g, '$1<em>$2</em>')
  text = text.replace(/\u0000(\d+)\u0000/g, (_match, idx: string) => `<code>${escapeHtml(codes[Number(idx)] ?? '')}</code>`)
  return text
}
