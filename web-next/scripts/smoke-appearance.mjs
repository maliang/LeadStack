#!/usr/bin/env node
/**
 * 「外观设置」Hermetic 冒烟（本地静态服务 + playwright context.route 内存 mock，不碰真后端）。
 *
 * 覆盖 web-next/src/lib/appearance.ts + App.vue + SettingsPage.vue 的外观卡：
 *   ① 深色令牌真的生效：切深色后 <html> 出现实测类名 dark，body 背景与 --ls-bg 实际算出色值变深，color-scheme=dark；
 *   ② 刷新后保持：深色 + en-US + 紧凑，reload 后三者都还是原值（<html> 类、color-scheme、lang/侧栏、data-density、字号 token 不变、envelope 齐全）；
 *   ③ 首屏无闪白：不靠截图——addInitScript 里用 rAF 记录首帧 className、用 MutationObserver 在 #app 首次出现子节点时快照，
 *      断言 Vue 挂载完成时 dark 已在 <html>；并静态检查 dist/index.html 内联脚本位于 #app 挂载点之前且读同一个 storage key；
 *   ④ 切语言后界面文字变化：en-US 后 html lang=en、侧栏「服务器」→ Servers，切回 zh-CN 恢复；
 *   ⑤ 跟随系统：emulateMedia 浅色 + mode=auto 不加 dark，深色下加 dark，再从深切浅实时移除；
 *   ⑥ 非外观设置不受影响：切主题/语言/密度后设置页版本卡与升级卡、备份页列表与备份策略卡仍在，且全程 0 条写请求；
 *   ⑦ 320px 窄宽无横向滚动；⑧ 全程 pageerror 0。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-appearance.mjs [--headed] [--shots dir]
 */
import { mkdirSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  launchChromium,
  loadChromium,
  log,
  login,
  makeChecker,
  parseFlags,
  resolveDist,
  startStaticServer,
  watchApiCalls,
  writes,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const SHOTS = typeof flag('shots', null) === 'string' ? resolve(String(flag('shots'))) : null

const TOKEN = '{{SECRET_xpfxtgql}}'
const PASSWORD = '{{SECRET_pn4deyln}}'
const THEME_KEY = '__VEAN_THEME'
const LANGUAGE_KEY = 'leadstack.lang'
const LIGHT_BG = '#f8fafc'
const DARK_BG = '#0b1220'
const NOW = Date.now()

const ALL_PERMISSIONS = [
  'server.read',
  'server.write',
  'group.write',
  'token.manage',
  'audit.read',
  'backup.read',
  'backup.write',
  'backup.delete',
  'system.manage',
  'user.manage',
  'role.manage',
]

function userFixture() {
  return {
    id: 'u-admin',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: ALL_PERMISSIONS,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

function updateInfoFixture() {
  return {
    current: '1.8.0',
    latest: '2.0.0',
    has_update: true,
    published_at: NOW - 3_600_000,
    release_url: 'https://example.com/releases/2.0.0',
    notes: '外观冒烟用的静态升级信息。',
    schema_changed: false,
    assets: [],
    upgradable: true,
    upgrade_note: null,
    checked_at: NOW,
    error: null,
    upgrade_enabled: true,
    check_interval: '24h',
  }
}

function backupListFixture() {
  return [
    {
      name: 'leadstack-20261119-120000.tar.gz',
      size_bytes: 1024,
      created_at: NOW - 60_000,
      includes_artifacts: false,
      schema_version: 3,
      sha256: 'a'.repeat(64),
    },
  ]
}

/** 假后端：只覆盖外观冒烟会碰到的端点，其余一律 404，避免静默放过。 */
async function installMockApi(context) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const noContent = () => route.fulfill({ status: 204, body: '' })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: userFixture() })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: userFixture() })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }

    /* 设置页：版本卡、网络卡、升级卡都依赖这些端点，缺失会进失败态干扰断言。 */
    if (path === '/api/v1/system/info' && method === 'GET') {
      return json(200, { data: { version: '1.8.0', uptime_s: 3_600, server_count: 1, online_count: 1 } })
    }
    if (path === '/api/v1/system/network' && method === 'GET') {
      return json(200, {
        data: {
          listen: '127.0.0.1:8443',
          active_listen: '127.0.0.1:8443',
          public_url: '',
          default_ssh_port: 22,
          listen_source: 'default',
        },
      })
    }
    if (path === '/api/v1/system/update' && method === 'GET') {
      return json(200, { data: updateInfoFixture() })
    }
    if (path === '/api/v1/system/update/status' && method === 'GET') {
      return json(200, {
        data: { current_version: '1.8.0', running: false, job: null, phase: 'idle', history: [] },
      })
    }

    /* 备份页：列表与自动备份策略卡。 */
    if (path === '/api/v1/backups' && method === 'GET') {
      return json(200, { data: { backups: backupListFixture() } })
    }
    if (path === '/api/v1/backup-settings' && method === 'GET') {
      return json(200, { data: { enabled: true, interval: '24h', retention: 7, include_artifacts: false } })
    }

    return json(404, { error: { code: 'not_found', message: `smoke-appearance 未 mock ${method} ${path}` } })
  })
}

/** 首屏探针：rAF 记录首帧状态；MutationObserver 在 #app 首次出现子节点时快照。 */
const FIRST_PAINT_PROBE = () => {
  const probe = { startedAt: performance.now(), frames: [], appMounted: null }
  window.__appearanceProbe = probe

  const read = () => {
    const root = document.documentElement
    const app = document.getElementById('app')
    return {
      t: Math.round(performance.now()),
      cls: root ? root.className : '',
      colorScheme: root ? getComputedStyle(root).colorScheme : '',
      appChildren: app ? app.childNodes.length : -1,
      readyState: document.readyState,
    }
  }

  const recordFrame = () => {
    probe.frames.push(read())
  }
  requestAnimationFrame(() => {
    recordFrame()
    requestAnimationFrame(recordFrame)
  })

  const observer = new MutationObserver(() => {
    if (probe.appMounted) return
    const app = document.getElementById('app')
    if (!app || app.childNodes.length === 0) return
    probe.appMounted = read()
  })
  observer.observe(document, { childList: true, subtree: true })
}

const DARK_TOKEN_RE = /(^|\s)dark(\s|$)/
const hasDarkClass = (className) => DARK_TOKEN_RE.test(String(className || ''))

function parseRgb(value) {
  const match = /rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/.exec(String(value || ''))
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null
}

/** WCAG 相对亮度，用来断言「色值真的变深」而不是只换了个类名。 */
function relativeLuminance(rgb) {
  if (!rgb) return null
  const [r, g, b] = rgb.map((value) => {
    const channel = value / 255
    return channel <= 0.03928 ? channel / 12.92 : Math.pow((channel + 0.055) / 1.055, 2.4)
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

async function readAppearance(page) {
  return page.evaluate(
    ({ themeKey, languageKey }) => {
      const root = document.documentElement
      const rootStyle = getComputedStyle(root)
      const bodyStyle = getComputedStyle(document.body)
      let envelope = null
      try {
        envelope = JSON.parse(window.localStorage.getItem(themeKey) || 'null')
      } catch (error) {
        envelope = { error: String(error) }
      }
      return {
        cls: root.className,
        classTokens: Array.from(root.classList),
        bodyBg: bodyStyle.backgroundColor,
        lsBg: rootStyle.getPropertyValue('--ls-bg').trim(),
        colorScheme: rootStyle.colorScheme,
        lang: root.getAttribute('lang'),
        density: root.getAttribute('data-density'),
        space4: rootStyle.getPropertyValue('--ls-space-4').trim(),
        space5: rootStyle.getPropertyValue('--ls-space-5').trim(),
        textMd: rootStyle.getPropertyValue('--ls-text-md').trim(),
        navLabels: Array.from(document.querySelectorAll('.ls-nav__label')).map((node) =>
          (node.textContent || '').trim(),
        ),
        storedLanguage: window.localStorage.getItem(languageKey),
        envelope,
      }
    },
    { themeKey: THEME_KEY, languageKey: LANGUAGE_KEY },
  )
}

const THEME_LABELS = {
  'zh-CN': { auto: '自动', light: '浅色', dark: '深色' },
  en: { auto: 'auto', light: 'light', dark: 'dark' },
}

/** 用 provider 自带的 SThemeModeSelect 选模式（不手写 light/dark 逻辑）。 */
async function chooseThemeMode(page, mode) {
  const lang = await page.evaluate(() => document.documentElement.getAttribute('lang') || 'zh-CN')
  const labels = String(lang).startsWith('en') ? THEME_LABELS.en : THEME_LABELS['zh-CN']
  const label = labels[mode]
  const trigger = page.locator('.ls-appearance__row').first().locator('[role="combobox"]')
  await trigger.click()
  const option = page.getByRole('option', { name: new RegExp(`^${label}$`, 'i'), exact: false }).first()
  await option.waitFor({ state: 'visible', timeout: 5000 })
  await option.click()
  await page.waitForTimeout(200)
}

async function waitForBoot(page, base) {
  await login(page, base, { password: PASSWORD })
  await page.goto(`${base}/#/settings`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#settings-appearance-language', { timeout: 15000 })
  await page.waitForSelector('#settings-version', { timeout: 15000 })
  await page.waitForSelector('#upgrade-card', { timeout: 15000 })
  await page.waitForTimeout(300)
}

async function main() {
  const chromium = await loadChromium()
  const { dist, ready } = resolveDist(WEB_ROOT, flag)
  if (!ready) {
    console.error(`✗ ${dist}/index.html 不存在，先执行 pnpm run build`)
    process.exitCode = 2
    return
  }

  const checker = makeChecker()
  const verify = ({ check, ok, detail = '' }) => checker.check(check, ok, detail)

  if (SHOTS) mkdirSync(SHOTS, { recursive: true })

  const { server, base } = await startStaticServer(dist, Number(flag('port', 0)) || 0)
  log('serve', `${dist} → ${base}`)

  let browser
  try {
    browser = await launchChromium(chromium, !HEADED)
  } catch (error) {
    console.error(`✗ 启动 chromium 失败：${error && error.message ? error.message.split('\n')[0] : error}`)
    await new Promise((done) => server.close(done))
    process.exitCode = 2
    return
  }

  const pageErrors = []

  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await context.addInitScript(FIRST_PAINT_PROBE)
    await installMockApi(context)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)

    await waitForBoot(page, base)

    /* ================= ① 深色令牌真的生效 ================= */
    const light = await readAppearance(page)
    verify({
      check: '深色令牌真的生效：初始浅色态 --ls-bg 为 #f8fafc 且无 dark 类',
      ok: light.lsBg === LIGHT_BG && !hasDarkClass(light.cls) && light.colorScheme === 'light',
      detail: `--ls-bg=${light.lsBg} cls=${JSON.stringify(light.cls)} color-scheme=${light.colorScheme}`,
    })

    await chooseThemeMode(page, 'dark')
    await page.waitForFunction(() => document.documentElement.classList.contains('dark'), null, {
      timeout: 5000,
    })
    const dark = await readAppearance(page)
    const darkTokens = dark.classTokens.filter((token) => !light.classTokens.includes(token))
    const measuredDarkToken = darkTokens.find((token) => /dark/i.test(token)) || null
    const lightRgb = parseRgb(light.bodyBg)
    const darkRgb = parseRgb(dark.bodyBg)
    const lightLum = relativeLuminance(lightRgb)
    const darkLum = relativeLuminance(darkRgb)

    verify({
      check: '深色令牌真的生效：切深色后 <html> 类名含实测深色类 dark',
      ok: hasDarkClass(dark.cls) && measuredDarkToken === 'dark',
      detail: `新增类=${JSON.stringify(darkTokens)} className=${JSON.stringify(dark.cls)}`,
    })
    verify({
      check: '深色令牌真的生效：body 背景实际算出色值变深（相对亮度显著下降）',
      ok: Boolean(lightRgb && darkRgb && lightLum !== null && darkLum !== null) && darkLum < lightLum * 0.5,
      detail: `${light.bodyBg}（亮度 ${lightLum?.toFixed(3)}）→ ${dark.bodyBg}（亮度 ${darkLum?.toFixed(3)}）`,
    })
    verify({
      check: `深色令牌真的生效：--ls-bg 实际算出色值为 ${DARK_BG}`,
      ok: dark.lsBg === DARK_BG && light.lsBg === LIGHT_BG,
      detail: `${light.lsBg} → ${dark.lsBg}`,
    })
    verify({
      check: '深色令牌真的生效：color-scheme 同步为 dark（原生控件跟随深色）',
      ok: dark.colorScheme === 'dark',
      detail: `color-scheme=${dark.colorScheme}`,
    })

    /* ================= ④ 切语言后界面文字变化 ================= */
    await page.selectOption('#settings-appearance-language', 'en-US')
    await page.waitForFunction(() => document.documentElement.getAttribute('lang') === 'en', null, {
      timeout: 5000,
    })
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll('.ls-nav__label')).some((node) => node.textContent.trim() === 'Servers'),
      null,
      { timeout: 5000 },
    )
    const english = await readAppearance(page)
    verify({
      check: '切语言后界面文字变化：切 en-US 后 <html lang> 同步为 en',
      ok: english.lang === 'en' && english.storedLanguage === 'en-US',
      detail: `lang=${english.lang} 存储=${english.storedLanguage}`,
    })
    verify({
      check: '切语言后界面文字变化：侧栏「服务器」变为 Servers',
      ok: english.navLabels.includes('Servers') && !english.navLabels.includes('服务器'),
      detail: JSON.stringify(english.navLabels.slice(0, 6)),
    })

    await page.selectOption('#settings-appearance-language', 'zh-CN')
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll('.ls-nav__label')).some((node) => node.textContent.trim() === '服务器'),
      null,
      { timeout: 5000 },
    )
    const backToChinese = await readAppearance(page)
    verify({
      check: '切语言后界面文字变化：切回 zh-CN 后侧栏恢复「服务器」',
      ok: backToChinese.navLabels.includes('服务器') && backToChinese.lang === 'zh-CN',
      detail: `lang=${backToChinese.lang} ${JSON.stringify(backToChinese.navLabels.slice(0, 6))}`,
    })

    /* 再切回 en-US 并压成紧凑，供「刷新后保持」一次性验证三者。 */
    await page.selectOption('#settings-appearance-language', 'en-US')
    await page.waitForFunction(() => document.documentElement.getAttribute('lang') === 'en', null, {
      timeout: 5000,
    })
    await page.selectOption('#settings-appearance-density', 'compact')
    await page.waitForFunction(() => document.documentElement.getAttribute('data-density') === 'compact', null, {
      timeout: 5000,
    })
    await page.waitForFunction(
      ({ themeKey, languageKey }) => {
        try {
          const envelope = JSON.parse(window.localStorage.getItem(themeKey) || 'null')
          return (
            envelope &&
            envelope.mode === 'dark' &&
            envelope.extra &&
            envelope.extra.language === 'en-US' &&
            envelope.extra.density === 'compact' &&
            window.localStorage.getItem(languageKey) === 'en-US'
          )
        } catch (error) {
          return false
        }
      },
      { themeKey: THEME_KEY, languageKey: LANGUAGE_KEY },
      { timeout: 5000 },
    )
    const changed = await readAppearance(page)
    verify({
      check: '深色令牌真的生效：紧凑密度只压缩 --ls-space-4，字号 --ls-text-md 不变',
      ok:
        Math.abs(parseFloat(changed.space4) - 0.75) < 1e-6 &&
        Math.abs(parseFloat(backToChinese.space4) - 1) < 1e-6 &&
        Math.abs(parseFloat(changed.textMd) - parseFloat(backToChinese.textMd)) < 1e-6,
      detail: `--ls-space-4 ${backToChinese.space4} → ${changed.space4}；--ls-text-md ${changed.textMd}`,
    })
    verify({
      check: '刷新后保持：切完 深色 + en-US + 紧凑 时持久化 envelope 三项齐全',
      ok:
        changed.envelope &&
        changed.envelope.mode === 'dark' &&
        changed.envelope.extra &&
        changed.envelope.extra.language === 'en-US' &&
        changed.envelope.extra.density === 'compact',
      detail: JSON.stringify(changed.envelope && { mode: changed.envelope.mode, extra: changed.envelope.extra }),
    })

    /* ================= ⑥ 非外观设置不受影响 ================= */
    const writesBefore = writes(calls)
    verify({
      check: '非外观设置不受影响：切主题/语言/密度后设置页版本卡与升级卡仍在',
      ok:
        (await page.locator('#settings-version').count()) === 1 &&
        (await page.locator('#upgrade-card').count()) === 1 &&
        (await page.locator('#settings-version').innerText()).trim().length > 0,
      detail: `version=${(await page.locator('#settings-version').innerText()).trim()}`,
    })

    if (SHOTS) {
      await page.screenshot({ path: join(SHOTS, 'appearance-dark-en-compact.png') })
    }

    await page.evaluate(() => {
      window.location.hash = '#/backups'
    })
    await page.waitForSelector('#backups-count', { timeout: 15000 })
    await page.waitForSelector('#backups-settings', { timeout: 15000 })
    await page.waitForSelector('#backups-settings-form', { timeout: 15000 })
    const backupCount = (await page.locator('#backups-count').innerText()).trim()
    verify({
      check: '非外观设置不受影响：切主题后备份页备份列表与备份策略卡仍在',
      ok:
        (await page.locator('#backups-count').count()) === 1 &&
        (await page.locator('#backups-settings').count()) === 1 &&
        (await page.locator('#backups-settings-form').count()) === 1 &&
        /\d/.test(backupCount),
      detail: `count=${JSON.stringify(backupCount)}`,
    })

    await page.evaluate(() => {
      window.location.hash = '#/settings'
    })
    await page.waitForSelector('#settings-appearance-language', { timeout: 15000 })
    const writesAfter = writes(calls)
    verify({
      check: '非外观设置不受影响：切主题/语言/密度全程 0 条额外写请求',
      ok: JSON.stringify(writesAfter) === JSON.stringify(writesBefore),
      detail: `before=${writesBefore.length} after=${writesAfter.length} ${JSON.stringify(writesAfter.slice(0, 3))}`,
    })

    /* ================= ②③ 刷新后保持 + 首屏无闪白 ================= */
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.waitForSelector('#settings-appearance-language', { timeout: 15000 })
    await page.waitForFunction(() => document.documentElement.classList.contains('dark'), null, {
      timeout: 5000,
    })
    const kept = await readAppearance(page)
    const probe = await page.evaluate(() => window.__appearanceProbe || null)

    verify({
      check: '刷新后保持：<html> 仍有深色类 dark',
      ok: hasDarkClass(kept.cls),
      detail: `className=${JSON.stringify(kept.cls)}`,
    })
    verify({
      check: '刷新后保持：color-scheme 仍为 dark',
      ok: kept.colorScheme === 'dark',
      detail: `color-scheme=${kept.colorScheme}`,
    })
    verify({
      check: '刷新后保持：界面语言仍是 en-US（html lang=en 且侧栏显示 Servers）',
      ok:
        kept.lang === 'en' &&
        kept.navLabels.includes('Servers') &&
        kept.storedLanguage === 'en-US' &&
        kept.envelope &&
        kept.envelope.extra &&
        kept.envelope.extra.language === 'en-US',
      detail: `lang=${kept.lang} 侧栏=${JSON.stringify(kept.navLabels.slice(0, 6))}`,
    })
    verify({
      check: '刷新后保持：密度仍是 compact（data-density=compact 且 --ls-space-4=0.75rem）',
      ok: kept.density === 'compact' && Math.abs(parseFloat(kept.space4) - 0.75) < 1e-6 && kept.envelope?.extra?.density === 'compact',
      detail: `data-density=${kept.density} --ls-space-4=${kept.space4}`,
    })
    verify({
      check: '刷新后保持：字号 token 未随密度改变（--ls-text-md 仍是 0.875rem）',
      ok:
        Math.abs(parseFloat(kept.textMd) - 0.875) < 1e-6 &&
        Math.abs(parseFloat(kept.textMd) - parseFloat(backToChinese.textMd)) < 1e-6,
      detail: `--ls-text-md=${kept.textMd}（标准态 ${backToChinese.textMd}）`,
    })
    verify({
      check: '刷新后保持：持久化 envelope 仍同时含 mode=dark / language=en-US / density=compact',
      ok:
        kept.envelope &&
        kept.envelope.mode === 'dark' &&
        kept.envelope.extra &&
        kept.envelope.extra.language === 'en-US' &&
        kept.envelope.extra.density === 'compact',
      detail: JSON.stringify(kept.envelope && { mode: kept.envelope.mode, extra: kept.envelope.extra }),
    })

    const frames = (probe && probe.frames) || []
    const firstFrame = frames[0] || null
    const appMounted = (probe && probe.appMounted) || null
    verify({
      check: '首屏无闪白：刷新后首个可见帧 <html> 已是深色类（不靠截图）',
      ok: Boolean(firstFrame) && hasDarkClass(firstFrame.cls) && frames.every((frame) => hasDarkClass(frame.cls)),
      detail: firstFrame
        ? `首帧 t=${firstFrame.t}ms cls=${JSON.stringify(firstFrame.cls)} color-scheme=${firstFrame.colorScheme} 帧数=${frames.length}`
        : '未采集到首帧',
    })
    verify({
      check: '首屏无闪白：#app 挂载完成时 dark 已在 <html>（内联脚本先于 Vue 挂载）',
      ok: Boolean(appMounted) && hasDarkClass(appMounted.cls) && appMounted.colorScheme === 'dark',
      detail: appMounted
        ? `挂载帧 t=${appMounted.t}ms cls=${JSON.stringify(appMounted.cls)} color-scheme=${appMounted.colorScheme} appChildren=${appMounted.appChildren}`
        : '未采集到 #app 挂载帧',
    })

    let distHtml = ''
    try {
      distHtml = readFileSync(join(dist, 'index.html'), 'utf8')
    } catch (error) {
      distHtml = ''
    }
    const keyPos = distHtml.indexOf(THEME_KEY)
    const appPos = distHtml.indexOf('<div id="app">')
    verify({
      check: '首屏无闪白：dist/index.html 内联脚本在 #app 挂载点之前且读同一个 storage key',
      ok: keyPos !== -1 && appPos !== -1 && keyPos < appPos && distHtml.includes('classList.add(\'dark\')'),
      detail: `key@${keyPos} app@${appPos}`,
    })

    /* ================= ⑤ 跟随系统 ================= */
    await page.emulateMedia({ colorScheme: 'light' })
    await chooseThemeMode(page, 'auto')
    await page.waitForFunction(() => !document.documentElement.classList.contains('dark'), null, {
      timeout: 5000,
    })
    const autoLight = await readAppearance(page)
    verify({
      check: '跟随系统：mode=auto 且系统浅色时不加 dark',
      ok: !hasDarkClass(autoLight.cls) && autoLight.colorScheme === 'light' && autoLight.lsBg === LIGHT_BG,
      detail: `cls=${JSON.stringify(autoLight.cls)} --ls-bg=${autoLight.lsBg} color-scheme=${autoLight.colorScheme}`,
    })

    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForFunction(() => document.documentElement.classList.contains('dark'), null, {
      timeout: 5000,
    })
    const autoDark = await readAppearance(page)
    verify({
      check: '跟随系统：mode=auto 且系统深色时加 dark（令牌与 color-scheme 生效）',
      ok: hasDarkClass(autoDark.cls) && autoDark.colorScheme === 'dark' && autoDark.lsBg === DARK_BG,
      detail: `cls=${JSON.stringify(autoDark.cls)} --ls-bg=${autoDark.lsBg} color-scheme=${autoDark.colorScheme}`,
    })

    await page.emulateMedia({ colorScheme: 'light' })
    await page.waitForFunction(() => !document.documentElement.classList.contains('dark'), null, {
      timeout: 5000,
    })
    const autoBackToLight = await readAppearance(page)
    verify({
      check: '跟随系统：系统深→浅实时切换后移除 dark 并回落浅色',
      ok:
        !hasDarkClass(autoBackToLight.cls) &&
        autoBackToLight.colorScheme === 'light' &&
        autoBackToLight.lsBg === LIGHT_BG &&
        autoBackToLight.envelope?.mode === 'auto',
      detail: `cls=${JSON.stringify(autoBackToLight.cls)} --ls-bg=${autoBackToLight.lsBg} mode=${autoBackToLight.envelope?.mode}`,
    })

    /* ================= ⑦ 窄宽 & ⑧ pageerror ================= */
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(400)
    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }))
    verify({
      check: '非外观设置不受影响：外观卡在 320px 窄宽下无横向溢出',
      ok: overflow.scrollWidth <= overflow.clientWidth + 1,
      detail: `scrollWidth=${overflow.scrollWidth} clientWidth=${overflow.clientWidth}`,
    })

    verify({
      check: '非外观设置不受影响：整轮 0 条 pageerror',
      ok: pageErrors.length === 0,
      detail: pageErrors.slice(0, 3).join(' | '),
    })

    verify({
      check: '非外观设置不受影响：切主题未触发任何设置写入（基线写请求数为 0）',
      ok: writesBefore.length === 0 && writesAfter.length === 0,
      detail: `baseline=${writesBefore.length} after=${writesAfter.length}`,
    })

    const exit = checker.finish()
    process.exitCode = exit
    await context.close()
  } catch (error) {
    console.error(`✗ 冒烟执行失败：${error && error.stack ? error.stack : error}`)
    process.exitCode = 2
  } finally {
    if (browser) await browser.close()
    await new Promise((done) => server.close(done))
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 2
})
