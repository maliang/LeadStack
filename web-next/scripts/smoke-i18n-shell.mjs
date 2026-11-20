#!/usr/bin/env node
/**
 * 「英文界面不得出现中文」回归冒烟（本地静态服务 + playwright context.route 内存 mock）。
 *
 * 为什么要有这一条：i18n 改造最容易的失败不是「漏翻一句」，而是她把中文写死在
 * shell / 标题 / skip link 这些「用户天天看见但没人截图」的位置；m1 把外壳文案收进
 * t()、m5 修了「切语言后标签题不跟变」，本脚本就是防止这两件事回潮的护栏。
 *
 * 覆盖（共 22 项，全部用 page 真实渲染结果断言，不读源码）：
 *   ① 登录后切 en-US：document.title 不含任何 CJK（/[一-鿿]/，失败时把实际 title 打出来）；
 *   ② 逐页导航 #/servers #/apps #/releases #/users #/settings：每页 title 不含 CJK 且以「· LeadStack」结尾；
 *   ③ 侧栏品牌副标题（.ls-brand__tagline，即 useShellCopy 的 appTagline）en-US 下不含 CJK；
 *   ④ 跳转链接（.ls-skip-link，skip to content）en-US 下不含 CJK；
 *   ⑤ 不导航、只切语言：在 #/settings 外观卡把中文切成 English，≤2s 内 title 变英文，
 *      且 location.hash / 导航条目录入数均未变化（证明靠 watch(language) 而不是导航间接触发）；
 *   ⑥ 切回 zh-CN 后 title 恢复成切换前那一串中文（防「一刀切成英文」的过度修复）；
 *   ⑦ page.on('pageerror') 收集异常，0 条；
 *   ⑧ #/about 与 #/forbidden 的面包屑落在各自译文（中文「关于」/「无权访问」、英文 About/Forbidden），
 *      不再回落到「总览/Overview」；中英文标签题分别为「关于 · LeadStack 领栈」/
 *      「无权限 · LeadStack 领栈」与「About · LeadStack」/「No permission · LeadStack」。
 *   ⑨ en-US 词典 chunk 加载失败（route.abort）时不写 localStorage、界面与 <html lang> 保持 zh-CN，
 *      外观卡出现失败提示且下拉框回滚（S3 回归）。
 *
 * 说明：品牌名「LeadStack 领栈」在侧栏是商标、不翻译；标签题里英文界面只保留
 * 拉丁商标「LeadStack」（见 src/router/index.ts 的 brandSuffix），
 * 副作用是英文 title 才可能满足「0 CJK 且以 · LeadStack 结尾」。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-i18n-shell.mjs [--headed]
 */
import { dirname, resolve } from 'node:path'
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
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const { dist, ready } = resolveDist(WEB_ROOT, flag)

if (!ready) {
  console.error(`找不到构建产物 ${dist}/index.html —— 先跑 pnpm run build（m1/m5 的改动必须先产出 dist）。`)
  process.exit(2)
}

const LANGUAGE_KEY = 'leadstack.lang'
const TOKEN = 'smoke-i18n-token'

/** 覆盖本脚本会经过的页面所需权限；缺一个就会落到 /forbidden，标题断言随即失效。 */
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
  'deploy.read',
  'deploy.write',
  'check.read',
  'alert.read',
  'terminal.use',
  'task.read',
]

/** 只统计 CJK 统一表意文字（U+4E00–U+9FFF），与任务卡口径一致。 */
const CJK_RE = /[一-鿿]/
const CJK_ALL_RE = /[一-鿿]/g

function cjkChars(text) {
  return Array.from(new Set(String(text || '').match(CJK_ALL_RE) || []))
}

function userFixture() {
  return {
    id: 'u-admin',
    username: 'admin',
    // 英文界面里 role_name 也会出现在顶栏，fixture 用英文，避免把 mock 数据的中文算到界面头上。
    role: 'admin',
    role_name: 'Administrator',
    permissions: ALL_PERMISSIONS,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

function updateInfoFixture() {
  return {
    current: '1.8.0',
    latest: '1.8.0',
    has_update: false,
    published_at: Date.now() - 3_600_000,
    release_url: 'https://example.com/releases/1.8.0',
    notes: 'i18n smoke fixture',
    schema_changed: false,
    assets: [],
    upgradable: false,
    upgrade_note: null,
    checked_at: Date.now(),
    error: null,
    upgrade_enabled: true,
    check_interval: '24h',
  }
}

/** 假后端：只覆盖本脚本会碰到的端点，其余一律 404，避免静默放过。 */
async function installMockApi(context) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const noContent = () => route.fulfill({ status: 204, body: '' })
    const emptyList = () => json(200, { data: [] })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: userFixture() })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: userFixture() })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }

    /* 设置页：版本卡、网络卡、升级卡、备份卡；缺一个就进失败态、外观卡可能不渲染。 */
    if (path === '/api/v1/system/info' && method === 'GET') {
      return json(200, { data: { version: '1.8.0', uptime_s: 3_600, server_count: 0, online_count: 0 } })
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
      return json(200, { data: { current_version: '1.8.0', running: false, job: null, phase: 'idle', history: [] } })
    }
    if (path === '/api/v1/backups' && method === 'GET') {
      return json(200, { data: { backups: [] } })
    }
    if (path === '/api/v1/backup-settings' && method === 'GET') {
      return json(200, { data: { enabled: false, interval: '24h', retention: 7, include_artifacts: false } })
    }

    /* 列表页：空列表即可，断言只关心标签题与外壳文案，不关心业务数据。 */
    if (method === 'GET' && /^\/api\/v1\/(servers|groups|apps|releases|templates|users|roles|credentials|checks)$/.test(path)) {
      return emptyList()
    }

    return json(404, { error: { code: 'not_found', message: `smoke-i18n-shell 未 mock ${method} ${path}` } })
  })
}

/** 逐页标题断言：hash 路由跳转（不整页 reload），等标签题落到该页译文再读。 */
async function navigateAndReadTitle(page, hash, expectedTitle) {
  await page.evaluate((target) => {
    window.location.hash = target
  }, hash)
  try {
    await page.waitForFunction(
      (expected) => document.title.startsWith(`${expected} · `),
      expectedTitle,
      { timeout: 5000, polling: 50 },
    )
  } catch {
    /* 超时也继续读实际 title，让 check 输出真实值而不是直接抛错 */
  }
  return await page.title()
}

/** 独立页面包屑断言：hash 路由跳转后等 .ls-crumbs 落到该页译文，再读面包屑与标签题。 */
async function navigateAndReadCrumb(page, hash, expectedCrumb) {
  await page.evaluate((target) => {
    window.location.hash = target
  }, hash)
  try {
    await page.waitForFunction(
      (expected) => ((document.querySelector('.ls-crumbs')?.textContent ?? '').trim()) === expected,
      expectedCrumb,
      { timeout: 5000, polling: 50 },
    )
  } catch {
    /* 超时也继续读实际面包屑，让 check 输出真实值而不是直接抛错 */
  }
  const crumb = (await page.locator('.ls-crumbs').first().textContent()) || ''
  return { crumb: crumb.trim(), title: await page.title() }
}

async function main() {
  const checks = makeChecker()
  const pageErrors = []
  const chromium = await loadChromium()
  const browser = await launchChromium(chromium, !HEADED)
  const { server, base } = await startStaticServer(dist)
  const context = await browser.newContext()

  try {
    // 起始语言固定 zh-CN：⑤ 要的是「中文 → English」，⑥ 要的是「再切回来」。
    await context.addInitScript(
      ([key, value]) => {
        try {
          window.localStorage.setItem(key, value)
        } catch {
          /* ignore */
        }
      },
      [LANGUAGE_KEY, 'zh-CN'],
    )
    await installMockApi(context)

    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))

    /* ---------- 基线：登录 + 中文标签题 ---------- */
    await login(page, base)
    checks.check('登录后进入控制台外壳', (await page.locator('.ls-shell').count()) === 1)

    await page.goto(`${base}/#/settings`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#settings-appearance-language', { timeout: 15000 })
    const zhTitle = await page.title()
    checks.check(
      '中文界面基线：document.title 含中文',
      CJK_RE.test(zhTitle) && zhTitle.endsWith('· LeadStack 领栈'),
      `title=${JSON.stringify(zhTitle)}`,
    )

    /* ---------- ①⑤：不导航、只切语言，≤2s 内标题变英文 ---------- */
    const hashBefore = await page.evaluate(() => window.location.hash)
    const navEntriesBefore = await page.evaluate(() => performance.getEntriesByType('navigation').length)
    const switchStartedAt = Date.now()
    let switched = true
    try {
      await page.selectOption('#settings-appearance-language', 'en-US')
      // 关键：selectOption 之后才等待，计时也只从这里开始 —— 若靠导航间接触发，
      // 这里会因为「点完就同步变了」而看不出差别，所以另用 hash/导航条目数断言原地未动。
      await page.waitForFunction(() => !/[一-鿿]/.test(document.title), null, { timeout: 2000, polling: 50 })
    } catch {
      switched = false
    }
    const elapsedMs = Date.now() - switchStartedAt
    const enTitle = await page.title()
    const hashAfter = await page.evaluate(() => window.location.hash)
    const navEntriesAfter = await page.evaluate(() => performance.getEntriesByType('navigation').length)
    const noNavigation = hashBefore === hashAfter && navEntriesBefore === navEntriesAfter

    checks.check(
      '切 en-US 后 document.title 不含 CJK',
      !CJK_RE.test(enTitle) && enTitle.trim() !== '',
      `title=${JSON.stringify(enTitle)} CJK=${JSON.stringify(cjkChars(enTitle))}`,
    )
    checks.check(
      '不导航、只切语言：≤2s 内标签题变英文（watch(language) 生效）',
      switched && elapsedMs <= 2000 && noNavigation,
      `耗时=${elapsedMs}ms hash=${hashAfter} 导航条目=${navEntriesBefore}→${navEntriesAfter}`,
    )
    checks.check(
      '切 en-US 后标签题以「· LeadStack」结尾',
      enTitle.endsWith('· LeadStack'),
      `title=${JSON.stringify(enTitle)}`,
    )
    checks.check(
      '切 en-US 后 <html lang> 同步为 en',
      (await page.evaluate(() => document.documentElement.getAttribute('lang'))) === 'en',
    )
    checks.check(
      '切 en-US 后语言已持久化（leadstack.lang=en-US）',
      (await page.evaluate((key) => window.localStorage.getItem(key), LANGUAGE_KEY)) === 'en-US',
    )

    /* ---------- ②：逐页导航，title 无 CJK 且以「· LeadStack」结尾 ---------- */
    const PAGES = [
      ['#/servers', 'Servers'],
      ['#/apps', 'Apps'],
      ['#/releases', 'Releases'],
      ['#/users', 'Users'],
      ['#/settings', 'Settings'],
    ]
    for (const [hash, expected] of PAGES) {
      const title = await navigateAndReadTitle(page, hash, expected)
      checks.check(
        `en 页面 ${hash}：title 无 CJK 且以「· LeadStack」结尾`,
        !CJK_RE.test(title) && title.endsWith('· LeadStack'),
        `title=${JSON.stringify(title)} CJK=${JSON.stringify(cjkChars(title))}`,
      )
    }

    /* ---------- ③④：外壳上两处「容易被写死中文」的节点 ---------- */
    const brandTagline = (await page.locator('.ls-brand__tagline').first().textContent()) || ''
    checks.check(
      '侧栏品牌副标题（.ls-brand__tagline）en-US 下无 CJK',
      brandTagline.trim() !== '' && !CJK_RE.test(brandTagline),
      `text=${JSON.stringify(brandTagline.trim())} CJK=${JSON.stringify(cjkChars(brandTagline))}`,
    )

    const skipLink = (await page.locator('.ls-skip-link').first().textContent()) || ''
    checks.check(
      '跳转链接（skip to content）en-US 下无 CJK',
      skipLink.trim() !== '' && !CJK_RE.test(skipLink),
      `text=${JSON.stringify(skipLink.trim())} CJK=${JSON.stringify(cjkChars(skipLink))}`,
    )

    const navLabels = await page.locator('.ls-nav__item').allTextContents()
    const navText = navLabels.join(' | ')
    checks.check(
      '侧栏导航文案 en-US 下无 CJK',
      navLabels.length > 0 && !CJK_RE.test(navText),
      `条目=${navLabels.length} text=${JSON.stringify(navText.slice(0, 160))}`,
    )

    /* ---------- ⑥：切回 zh-CN 必须恢复中文（不过度修复） ---------- */
    await page.selectOption('#settings-appearance-language', 'zh-CN')
    let restored = true
    try {
      await page.waitForFunction(
        (expected) => document.title === expected,
        zhTitle,
        { timeout: 2000, polling: 50 },
      )
    } catch {
      restored = false
    }
    const backTitle = await page.title()
    checks.check(
      '切回 zh-CN 后 title 恢复中文（非一刀切成英文）',
      restored && CJK_RE.test(backTitle),
      `title=${JSON.stringify(backTitle)} 期望=${JSON.stringify(zhTitle)}`,
    )

    /* ---------- ⑧：/about 与 /forbidden 的面包屑不再回落「总览」 ---------- */
    for (const [hash, crumbText, fullTitle] of [
      ['#/about', '关于', '关于 · LeadStack 领栈'],
      ['#/forbidden', '无权访问', '无权限 · LeadStack 领栈'],
    ]) {
      const { crumb, title } = await navigateAndReadCrumb(page, hash, crumbText)
      checks.check(
        `${hash} 面包屑为「${crumbText}」且不含「总览」、标签题为「${fullTitle}」`,
        crumb === crumbText && !crumb.includes('总览') && title === fullTitle,
        `crumb=${JSON.stringify(crumb)} title=${JSON.stringify(title)}`,
      )
    }

    /* ---------- ⑧-en：切到 en-US 后 /about 与 /forbidden 落在各自英文面包屑 ---------- */
    // 中文面包屑只证「键接上了」；英文面包屑才能锁住「独立文案没有被首页/总览顶替」。
    await page.evaluate(() => {
      window.location.hash = '#/settings'
    })
    await page.waitForSelector('#settings-appearance-language', { timeout: 15000 })
    await page.selectOption('#settings-appearance-language', 'en-US')
    let enSwitched = true
    try {
      await page.waitForFunction(() => !/[一-鿿]/.test(document.title), null, { timeout: 2000, polling: 50 })
    } catch {
      enSwitched = false
    }
    for (const [hash, crumbText, fullTitle] of [
      ['#/about', 'About', 'About · LeadStack'],
      ['#/forbidden', 'Forbidden', 'No permission · LeadStack'],
    ]) {
      const { crumb, title } = await navigateAndReadCrumb(page, hash, crumbText)
      checks.check(
        `en ${hash} 面包屑为「${crumbText}」且不含「总览/Overview」、标签题为「${fullTitle}」`,
        enSwitched &&
          crumb === crumbText &&
          !crumb.includes('总览') &&
          !crumb.includes('Overview') &&
          title === fullTitle,
        `crumb=${JSON.stringify(crumb)} title=${JSON.stringify(title)} switched=${enSwitched}`,
      )
    }

    /* ---------- ⑦：全程无未捕获异常 ---------- */
    checks.check('全程 pageerror 0 条', pageErrors.length === 0, pageErrors.slice(0, 3).join(' / ') || '无')

    /* ---------- ⑨：词典 chunk 加载失败时不落盘、保持原语言（S3 回归） ---------- */
    // 断网时若仍把 en-US 写进 localStorage，下次冷启动会反复重试失败的 chunk；
    // 这里故意 abort 掉 en-US 的独立 chunk，验证失败时存储与界面都留在 zh-CN。
    const failContext = await browser.newContext()
    try {
      await failContext.addInitScript(
        ([key, value]) => {
          try {
            window.localStorage.setItem(key, value)
          } catch {
            /* ignore */
          }
        },
        [LANGUAGE_KEY, 'zh-CN'],
      )
      await installMockApi(failContext)
      await failContext.route(/\/assets\/en-US-[^/]+\.js(?:\?.*)?$/, (route) => route.abort())
      const failPage = await failContext.newPage()
      await login(failPage, base)
      await failPage.goto(`${base}/#/settings`, { waitUntil: 'domcontentloaded' })
      await failPage.waitForSelector('#settings-appearance-language', { timeout: 15000 })
      await failPage.selectOption('#settings-appearance-language', 'en-US')

      let noticeShown = true
      try {
        await failPage.waitForSelector('#settings-language-error', { timeout: 5000 })
      } catch {
        noticeShown = false
      }
      const failedStored = await failPage.evaluate((key) => window.localStorage.getItem(key), LANGUAGE_KEY)
      const failedSelected = await failPage.$eval('#settings-appearance-language', (el) => el.value)
      const failedHtmlLang = await failPage.evaluate(() => document.documentElement.getAttribute('lang'))
      const failedTitle = await failPage.title()
      checks.check(
        '词典 chunk 加载失败不落盘：存储仍 zh-CN、下拉框回滚、失败提示可见',
        noticeShown &&
          failedStored === 'zh-CN' &&
          failedSelected === 'zh-CN' &&
          failedHtmlLang === 'zh-CN' &&
          CJK_RE.test(failedTitle),
        `notice=${noticeShown} stored=${JSON.stringify(failedStored)} selected=${JSON.stringify(failedSelected)} htmlLang=${JSON.stringify(failedHtmlLang)} title=${JSON.stringify(failedTitle)}`,
      )
    } finally {
      await failContext.close()
    }

    const residue = checks.results.filter((item) => !item.ok)
    console.log(
      residue.length === 0
        ? '✔ 英文界面无中文残留：标题 / 品牌副标题 / 跳转链接 / 侧栏导航 均无 CJK'
        : `✗ 英文界面仍有中文残留：${residue.map((item) => item.name).join('；')}`,
    )
    log('i18n', `中文残留检查覆盖 ${checks.results.length} 项断言`)
  } finally {
    await context.close()
    await browser.close()
    await new Promise((done) => server.close(done))
  }

  return checks.finish()
}

main()
  .then((code) => {
    process.exitCode = code
  })
  .catch((error) => {
    console.error(`✗ smoke-i18n-shell 执行失败：${error && error.stack ? error.stack : error}`)
    process.exitCode = 2
  })
