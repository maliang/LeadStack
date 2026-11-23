#!/usr/bin/env node
/**
 * i18n 键集合闸门。
 *
 * 目录约定：src/i18n/<locale>/{common,servers,deploy,system,tools,alerts,checks,legacy}.ts
 * 每个领域文件导出一个扁平点号键的词典，<locale>/index.ts 展开合并成最终词典，
 * src/i18n/index.ts 的 t() / useI18n() 对外接口保持不变。
 *
 * 本脚本递归收集 zh-CN / en-US 的键路径集合，检查：
 *   1. 两份语言的领域文件同名（missingFiles）
 *   2. 每个同名领域文件的键集合逐字相等（perFileDiff）
 *   3. 合并后总键数相等，且互不存在对方缺失的键（onlyZh / onlyEn）
 *   4. 合并后键数与各领域文件键数之和一致（跨文件重复键会让前者变小）
 *   5. 可选 --expect <n>：校验合并后总键数等于 n（防止误删/漏合并）
 * 「值重复数」只做提示，不影响退出码——同一句译文在多处复用是正常现象。
 *
 * 用法：
 *   node scripts/check-i18n-keys.mjs
 *   node scripts/check-i18n-keys.mjs --expect 3268
 */
import { existsSync, readdirSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const I18N_DIR = resolve(WEB_ROOT, 'src/i18n')
const LOCALES = ['zh-CN', 'en-US']

const argv = process.argv.slice(2)
const expectArg = (() => {
  const i = argv.findIndex((a) => a === '--expect' || a.startsWith('--expect='))
  if (i === -1) return null
  const raw = argv[i].includes('=') ? argv[i].slice('--expect='.length) : argv[i + 1]
  const n = Number(raw)
  return Number.isInteger(n) && n >= 0 ? n : NaN
})()

const fail = (message) => {
  console.error(`\n✖ ${message}`)
  process.exit(1)
}

if (typeof registerHooks !== 'function') {
  fail('需要 Node 22.15+（本脚本用 module.registerHooks 解析无后缀的 .ts 相对导入）')
}
if (Number.isNaN(expectArg)) fail('--expect 需要一个非负整数')

// 让 Node 能像 Vite/TS 那样解析 `./common` → `./common.ts`（源文件里不带后缀）。
registerHooks({
  resolve(specifier, context, nextResolve) {
    try {
      return nextResolve(specifier, context)
    } catch (error) {
      if (context.parentURL && specifier.startsWith('.') && !/\.[cm]?[jt]sx?$/.test(specifier)) {
        const candidate = new URL(`${specifier}.ts`, context.parentURL)
        if (existsSync(fileURLToPath(candidate))) return { url: candidate.href, shortCircuit: true }
        const index = new URL(`${specifier}/index.ts`, context.parentURL)
        if (existsSync(fileURLToPath(index))) return { url: index.href, shortCircuit: true }
      }
      throw error
    }
  },
})

const load = async (file) => {
  try {
    const mod = await import(pathToFileURL(file).href)
    const dict = mod.default
    if (!dict || typeof dict !== 'object') throw new Error('默认导出不是对象')
    return dict
  } catch (error) {
    fail(`加载失败：${file}\n  ${error instanceof Error ? error.message : String(error)}`)
  }
}

/** 递归收集嵌套对象的键路径（数组不算词典）。 */
const collectKeyPaths = (node, prefix = '', out = new Set()) => {
  for (const [key, value] of Object.entries(node)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) collectKeyPaths(value, path, out)
    else out.add(path)
  }
  return out
}

const nameOf = (file) => file.replace(/\.ts$/, '')
const localeDir = (locale) => resolve(I18N_DIR, locale)
const domainFiles = (locale) => {
  const dir = localeDir(locale)
  if (!existsSync(dir)) fail(`缺少语言目录：${dir}`)
  return readdirSync(dir)
    .filter((f) => f.endsWith('.ts') && f !== 'index.ts')
    .sort()
}

const filesByLocale = Object.fromEntries(LOCALES.map((l) => [l, domainFiles(l)]))
const [zhFiles, enFiles] = LOCALES.map((l) => filesByLocale[l])
const missingFiles = zhFiles.filter((f) => !enFiles.includes(f))
const extraFiles = enFiles.filter((f) => !zhFiles.includes(f))

const perFileDiff = []
const merged = {}
for (const locale of LOCALES) merged[locale] = {}
for (const file of zhFiles.filter((f) => enFiles.includes(f))) {
  const zhDict = await load(resolve(localeDir('zh-CN'), file))
  const enDict = await load(resolve(localeDir('en-US'), file))
  const zhKeys = collectKeyPaths(zhDict)
  const enKeys = collectKeyPaths(enDict)
  const onlyZhHere = [...zhKeys].filter((k) => !enKeys.has(k))
  const onlyEnHere = [...enKeys].filter((k) => !zhKeys.has(k))
  if (onlyZhHere.length || onlyEnHere.length) {
    perFileDiff.push({ file, onlyZhHere, onlyEnHere })
  }
  Object.assign(merged['zh-CN'], zhDict)
  Object.assign(merged['en-US'], enDict)
}

const dicts = {
  'zh-CN': await load(resolve(localeDir('zh-CN'), 'index.ts')),
  'en-US': await load(resolve(localeDir('en-US'), 'index.ts')),
}
const keys = Object.fromEntries(LOCALES.map((l) => [l, collectKeyPaths(dicts[l])]))
const onlyZh = [...keys['zh-CN']].filter((k) => !keys['en-US'].has(k)).sort()
const onlyEn = [...keys['en-US']].filter((k) => !keys['zh-CN'].has(k)).sort()

/** 同一语言里被多个键共用、且完全相同（含占位符）的译文。 */
const duplicateValues = (dict) => {
  const byValue = new Map()
  for (const [key, value] of Object.entries(dict)) {
    if (typeof value !== 'string') continue
    if (!byValue.has(value)) byValue.set(value, [])
    byValue.get(value).push(key)
  }
  const groups = [...byValue.values()].filter((group) => group.length > 1)
  return { groups, keys: groups.reduce((sum, group) => sum + group.length, 0) }
}
const dup = { 'zh-CN': duplicateValues(dicts['zh-CN']), 'en-US': duplicateValues(dicts['en-US']) }

const perLocaleFileKeyCount = {}
for (const locale of LOCALES) {
  let sum = 0
  for (const file of filesByLocale[locale]) {
    const dict = await load(resolve(localeDir(locale), file))
    sum += collectKeyPaths(dict).size
  }
  perLocaleFileKeyCount[locale] = sum
}

const zhCount = keys['zh-CN'].size
const enCount = keys['en-US'].size
const mergedTotal = (locale) => Object.keys(dicts[locale]).length

const lines = [
  `i18n 键集合检查（${I18N_DIR.replace(WEB_ROOT + '/', '')}）`,
  `  领域文件      zh ${zhFiles.length} 个 / en ${enFiles.length} 个（${zhFiles.map(nameOf).join(', ')}）`,
  `  zh 键数       ${zhCount}`,
  `  en 键数       ${enCount}`,
  `  仅 zh 有      ${onlyZh.length}`,
  `  仅 en 有      ${onlyEn.length}`,
  `  值重复数      zh ${dup['zh-CN'].keys} 个键 / ${dup['zh-CN'].groups.length} 组，en ${dup['en-US'].keys} 个键 / ${dup['en-US'].groups.length} 组（仅提示，不影响退出码）`,
]
if (expectArg !== null) lines.push(`  --expect      ${expectArg}（实际 ${zhCount}）`)
if (onlyZh.length) lines.push(`    仅 zh 示例：${onlyZh.slice(0, 5).join(', ')}`)
if (onlyEn.length) lines.push(`    仅 en 示例：${onlyEn.slice(0, 5).join(', ')}`)
if (missingFiles.length || extraFiles.length) {
  lines.push(`  文件不一致    仅 zh 有: ${missingFiles.join(', ') || '—'}；仅 en 有: ${extraFiles.join(', ') || '—'}`)
}
for (const diff of perFileDiff) {
  lines.push(
    `  文件内差异    ${diff.file}：仅 zh ${diff.onlyZhHere.length}（${diff.onlyZhHere.slice(0, 3).join(', ')}）` +
      ` / 仅 en ${diff.onlyEnHere.length}（${diff.onlyEnHere.slice(0, 3).join(', ')}）`,
  )
}
// 各领域文件键数之和 vs 合并后键数：不相等说明有键在合并时被覆盖（跨文件重复）。
for (const locale of LOCALES) {
  const sum = perLocaleFileKeyCount[locale]
  const total = mergedTotal(locale)
  if (sum !== total) {
    lines.push(`  跨文件重复键  ${locale}：各文件键数和 ${sum} ≠ 合并后 ${total}`)
  }
}
console.log(lines.join('\n'))

const problems = []
if (onlyZh.length) problems.push(`仅 zh 有 ${onlyZh.length} 个键`)
if (onlyEn.length) problems.push(`仅 en 有 ${onlyEn.length} 个键`)
if (zhCount !== enCount) problems.push(`键数不等（zh ${zhCount} / en ${enCount}）`)
if (missingFiles.length || extraFiles.length) problems.push('两份语言的领域文件不同名')
if (perFileDiff.length) problems.push(`${perFileDiff.length} 个领域文件的键集合不一致`)
for (const locale of LOCALES) {
  if (perLocaleFileKeyCount[locale] !== mergedTotal(locale)) problems.push(`${locale} 存在跨文件重复键`)
}
if (expectArg !== null && zhCount !== expectArg) problems.push(`总键数 ${zhCount} ≠ --expect ${expectArg}`)

if (problems.length) {
  console.error(`\n✖ i18n 键集合不合格：${problems.join('；')}`)
  process.exit(1)
}
console.log('\n✔ i18n 键集合一致（zh = en，领域文件同名同结构）')
