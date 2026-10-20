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
    if (entry.isDirectory()) {
      if (full === I18N_DIR) continue
      walk(full)
    } else if (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) {
      files.push(full)
    }
  }
}
walk(SRC)

const zh = {}
let counter = 0
const nextKey = () => `s.${String(++counter).padStart(4, '0')}`

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
  const replacements = [] // {start, end, text}

  const add = (text) => {
    const key = nextKey()
    zh[key] = text
    return key
  }

  const skipNode = new Set()
  const markSkip = (node) => { if (node) skipNode.add(node) }

  function visit(node) {
    // skip type nodes / names / imports
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return
    if (ts.isTypeNode && ts.isTypeNode(node)) return
    if (ts.isPropertyName && ts.isPropertyName(node)) {
      // property names are skipped, but visit children? initializer will be handled separately
      return
    }

    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      const parent = node.parent
      const isImportOrExport =
        (ts.isImportDeclaration(parent) || ts.isExportDeclaration(parent) ||
          ts.isImportSpecifier(parent) || ts.isExportSpecifier(parent) ||
          ts.isExternalModuleReference(parent))
      const isPropertyName =
        (ts.isPropertyAssignment(parent) && parent.name === node) ||
        (ts.isPropertySignature(parent) && parent.name === node) ||
        (ts.isMethodDeclaration(parent) && parent.name === node) ||
        (ts.isMethodSignature(parent) && parent.name === node) ||
        (ts.isEnumMember(parent) && parent.name === node) ||
        ts.isImportEqualsDeclaration(parent) ||
        (ts.isJsxAttribute(parent) && parent.name === node) ||
        (ts.isCaseClause(parent) && parent.expression === node) ||
        (ts.isLiteralTypeNode(parent))
      const isDirective = ts.isExpressionStatement(parent) && ts.isStringLiteral(node)
      if (!isImportOrExport && !isPropertyName && !isDirective && CJK.test(node.text)) {
        const key = add(node.text)
        // JSX attribute: wrap in braces
        if (ts.isJsxAttribute(parent) && parent.initializer === node) {
          replacements.push({ start: node.getStart(sf), end: node.getEnd(), text: `{t('${key}')}` })
        } else {
          replacements.push({ start: node.getStart(sf), end: node.getEnd(), text: `t('${key}')` })
        }
      }
      return
    }

    if (ts.isTemplateExpression(node)) {
      const whole = sourceText.slice(node.getStart(sf), node.getEnd())
      if (!CJK.test(whole)) return
      // Build template with numbered placeholders and expression args
      let text = node.head.text
      const args = []
      node.templateSpans.forEach((span, index) => {
        const exprText = sourceText.slice(span.expression.getStart(sf), span.expression.getEnd())
        args.push({ name: String(index), text: exprText })
        text += `{${index}}` + span.literal.text
      })
      const key = add(text)
      const argText = args.map((arg) => `'${arg.name}': ${arg.text}`).join(', ')
      replacements.push({
        start: node.getStart(sf),
        end: node.getEnd(),
        text: argText ? `t('${key}', { ${argText} })` : `t('${key}')`,
      })
      return
    }

    if (ts.isJsxText(node) && CJK.test(node.text)) {
      const raw = node.text
      const trimmed = raw.trim()
      if (trimmed !== '') {
        const leading = raw.indexOf(trimmed)
        const start = node.getStart(sf) + leading
        const key = add(trimmed)
        replacements.push({ start, end: start + trimmed.length, text: `{t('${key}')}` })
      }
      return
    }

    ts.forEachChild(node, visit)
  }
  ts.forEachChild(sf, visit)

  if (replacements.length === 0) continue

  replacements.sort((a, b) => b.start - a.start)
  let output = sourceText
  for (const item of replacements) {
    output = output.slice(0, item.start) + item.text + output.slice(item.end)
  }

  // add import if needed
  if (!/\bimport\s*\{[^}]*\bt\b[^}]*\}\s*from\s*['"][^'"]*i18n['"]/.test(output)) {
    const importLine = `import { t } from '${relImport(file)}'\n`
    const lines = output.split('\n')
    let insertAt = 0
    for (let i = 0; i < lines.length; i += 1) {
      if (lines[i].startsWith('import ')) insertAt = i + 1
      else if (lines[i].trim() !== '' && !lines[i].startsWith('//')) break
    }
    lines.splice(insertAt, 0, importLine.trimEnd())
    output = lines.join('\n')
  }

  fs.writeFileSync(file, output)
  changedFiles += 1
  console.log('patched', path.relative(SRC, file), 'replacements:', replacements.length)
}

fs.writeFileSync('/tmp/legacy-zh.json', JSON.stringify(zh, null, 2))
console.log('changed files:', changedFiles, 'keys:', Object.keys(zh).length)
