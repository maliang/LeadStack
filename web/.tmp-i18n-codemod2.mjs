import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const ts = require('/tmp/tsapi/node_modules/typescript')
import fs from 'node:fs'
import path from 'node:path'

const SRC = path.resolve('src')
const I18N_DIR = path.join(SRC, 'i18n')
const CJK = /[\u3400-\u9fff\uf900-\ufaff]/
const files = []
function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) { if (full === I18N_DIR) continue; walk(full) }
    else if (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) files.push(full)
  }
}
walk(SRC)

const dictionary = {}      // key -> zh text
const keyByText = new Map() // text -> key (reuse)
let counter = 0
const keyFor = (text) => {
  if (keyByText.has(text)) return keyByText.get(text)
  const key = `s.${String(++counter).padStart(4, '0')}`
  dictionary[key] = text
  keyByText.set(text, key)
  return key
}

function relImport(file) {
  let rel = path.relative(path.dirname(file), I18N_DIR).replaceAll(path.sep, '/')
  if (!rel.startsWith('.')) rel = './' + rel
  return rel
}

let changedFiles = 0
for (const file of files) {
  const sourceText = fs.readFileSync(file, 'utf8')
  if (!CJK.test(sourceText)) continue
  const isTsx = file.endsWith('.tsx')
  const sf = ts.createSourceFile(file, sourceText, ts.ScriptTarget.Latest, true, isTsx ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const replacements = []

  function visit(node) {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return

    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      const parent = node.parent
      const skip =
        (ts.isPropertyAssignment(parent) && parent.name === node) ||
        (ts.isPropertySignature(parent) && parent.name === node) ||
        (ts.isMethodDeclaration(parent) && parent.name === node) ||
        (ts.isMethodSignature(parent) && parent.name === node) ||
        (ts.isEnumMember(parent) && parent.name === node) ||
        ts.isImportEqualsDeclaration(parent) ||
        (ts.isJsxAttribute(parent) && parent.name === node) ||
        (ts.isCaseClause(parent) && parent.expression === node) ||
        ts.isLiteralTypeNode(parent)
      if (!skip && CJK.test(node.text)) {
        const key = keyFor(node.text)
        const isJsxAttrInit = ts.isJsxAttribute(parent) && parent.initializer === node
        replacements.push({
          start: node.getStart(sf),
          end: node.getEnd(),
          text: isJsxAttrInit ? `{t('${key}')}` : `t('${key}')`,
        })
      }
      return
    }

    if (ts.isJsxText(node)) {
      const rawStart = node.getStart(sf)
      const rawEnd = node.getEnd()
      const raw = sourceText.slice(rawStart, rawEnd)
      const trimmed = raw.trim()
      if (trimmed !== '' && CJK.test(trimmed)) {
        const key = keyFor(trimmed)
        // 用 source 切片替换，保持前后空白不变，只替换 trimmed 文本本身
        const newRaw = raw.replace(trimmed, `{t('${key}')}`)
        replacements.push({ start: rawStart, end: rawEnd, text: newRaw })
      }
      return
    }

    if (ts.isTemplateExpression(node)) {
      const whole = sourceText.slice(node.getStart(sf), node.getEnd())
      if (!CJK.test(whole)) return
      let text = node.head.text
      const args = []
      node.templateSpans.forEach((span, index) => {
        const exprText = sourceText.slice(span.expression.getStart(sf), span.expression.getEnd())
        args.push({ name: String(index), text: exprText })
        text += `{${index}}` + span.literal.text
      })
      const key = keyFor(text)
      const argText = args.map((a) => `'${a.name}': ${a.text}`).join(', ')
      replacements.push({
        start: node.getStart(sf),
        end: node.getEnd(),
        text: argText ? `t('${key}', { ${argText} })` : `t('${key}')`,
      })
      return
    }

    ts.forEachChild(node, visit)
  }
  ts.forEachChild(sf, visit)
  if (replacements.length === 0) continue

  replacements.sort((a, b) => b.start - a.start)
  let output = sourceText
  for (const item of replacements) output = output.slice(0, item.start) + item.text + output.slice(item.end)

  if (!/\bimport\s*\{[^}]*\bt\b[^}]*\}\s*from\s*['"][^'"]*i18n['"]/.test(output)) {
    const importLine = `import { t } from '${relImport(file)}'`
    const lines = output.split('\n')
    let insertAt = 0
    for (let i = 0; i < lines.length; i += 1) {
      if (lines[i].startsWith('import ')) insertAt = i + 1
      else if (lines[i].trim() !== '' && !lines[i].startsWith('//')) break
    }
    lines.splice(insertAt, 0, importLine)
    output = lines.join('\n')
  }
  fs.writeFileSync(file, output)
  changedFiles += 1
  console.log('patched', path.relative(SRC, file), replacements.length)
}
fs.writeFileSync('/tmp/legacy-zh-v2.json', JSON.stringify(dictionary, null, 2))
console.log('changed', changedFiles, 'unique keys', Object.keys(dictionary).length)
