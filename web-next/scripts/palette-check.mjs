#!/usr/bin/env node
/**
 * 品牌绿调色板验收（Playwright 无头）。
 *
 * 断言目标：SConfigProvider 的 `preset` 生效后，主按钮（SButton solid primary）的
 * computed background 落在品牌绿色阶里（#16a34a / green.600），而不是 Vean 内置
 * emerald 青绿（#10b981/#059669，色相约 161°）。
 *
 * 另外覆盖三处「品牌面」消除两种绿：登录页主按钮 = rgb(22,163,74)、侧栏 logo 渐变含
 * #22c55e、键盘焦点环 box-shadow 含品牌绿；浅色与深色模式都不许出现 emerald 色值。
 *
 * 做法（hermetic，不需要 Go 后端）：
 *  1. 起静态服务器托管 web-next/dist（hash 路由，index.html 兜底）。
 *  2. 用 page.route() 拦 /api/v1/*，给登录 / me 一套内存假后端。
 *  3. 登录进首页，取「开始使用」主按钮的 computed color，按色阶 + 色相 + 对比度断言，
 *     并截整页图与按钮图。
 *
 * 用法：
 *   node scripts/palette-check.mjs              # 无头 + 默认截图目录
 *   node scripts/palette-check.mjs --headed     # 看画面
 *   node scripts/palette-check.mjs --dist dist --port 0 --out .palette-check
 *
 * 退出码：0 全部通过 / 1 有断言失败 / 2 环境缺失（没装 playwright 或没有 dist）。
 */
import { createReadStream, existsSync, mkdirSync, readdirSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import { dirname, extname, join, normalize, resolve } from 'node:path'
import { homedir, platform } from 'node:os'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const require = createRequire(import.meta.url)

const argv = process.argv.slice(2)
const flag = (name, fallback = null) => {
  const index = argv.indexOf(`--${name}`)
  if (index === -1) return fallback
  const next = argv[index + 1]
  return next && !next.startsWith('--') ? next : true
}
const HEADED = flag('headed', false) === true
const DIST = resolve(WEB_ROOT, String(flag('dist', 'dist')))
const REQUESTED_PORT = Number(flag('port', 0)) || 0
const OUT_DIR = resolve(WEB_ROOT, String(flag('out', '.palette-check')))

/**
 * 品牌绿色阶（唯一同步源：web-next/src/theme/brand.ts 的 BRAND_SCALE）。
 * 改色阶必须两处一起改，脚本不读 .ts 是为了保持零构建依赖。
 */
const BRAND_SCALE = {
  50: '#f0fdf4',
  100: '#dcfce7',
  500: '#22c55e',
  600: '#16a34a',
  700: '#15803d',
  900: '#14532d',
  950: '#052e16',
}
/** 主按钮填充必须命中的档位（浅色模式）。 */
const EXPECTED_LEVEL = 600
/** 深色模式：浅色 600 降亮一阶 → 700。 */
const DARK_EXPECTED_LEVEL = 700
/** emerald 青绿的色相（#10b981 / #059669 ≈ 160–162°），用来证明「不是旧主题」。 */
const EMERALD_HUE_MIN = 156

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
}

const results = []
function check(label, pass, detail = '') {
  results.push({ label, pass })
  console.log(`  ${pass ? '✓' : '✗'} ${label}${detail ? ` — ${detail}` : ''}`)
}

/* ---------- 颜色工具（纯函数，失败给可读 reason） ---------- */

/** 解析 `rgb(22, 163, 74)` / `rgba(22, 163, 74, 0.8)`；失败返回 null。 */
function parseRgb(value) {
  const match = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)$/i.exec(
    String(value || '').trim(),
  )
  if (!match) return null
  const [r, g, b] = [Number(match[1]), Number(match[2]), Number(match[3])]
  if ([r, g, b].some((channel) => Number.isNaN(channel) || channel < 0 || channel > 255)) return null
  const alpha = match[4] === undefined ? 1 : Number(match[4])
  return { r, g, b, a: alpha }
}

/** #rrggbb → {r,g,b}。 */
function hexToRgb(hex) {
  const text = hex.replace('#', '')
  return {
    r: Number.parseInt(text.slice(0, 2), 16),
    g: Number.parseInt(text.slice(2, 4), 16),
    b: Number.parseInt(text.slice(4, 6), 16),
  }
}

/** 和色阶里最近一档的通道最大偏差，用于「落在色阶里」的容差判断。 */
function nearestScale(color) {
  let best = null
  for (const [level, hex] of Object.entries(BRAND_SCALE)) {
    const target = hexToRgb(hex)
    const distance = Math.max(
      Math.abs(color.r - target.r),
      Math.abs(color.g - target.g),
      Math.abs(color.b - target.b),
    )
    if (!best || distance < best.distance) best = { level: Number(level), hex, distance }
  }
  return best
}

/** sRGB → HSL 的色相（0–360），用来把 emerald 青绿与品牌绿区分开。 */
function hueOf({ r, g, b }) {
  const [red, green, blue] = [r / 255, g / 255, b / 255]
  const max = Math.max(red, green, blue)
  const min = Math.min(red, green, blue)
  const delta = max - min
  if (delta === 0) return 0
  let hue
  if (max === red) hue = ((green - blue) / delta) % 6
  else if (max === green) hue = (blue - red) / delta + 2
  else hue = (red - green) / delta + 4
  hue *= 60
  return hue < 0 ? hue + 360 : hue
}

/** WCAG 相对亮度 → 对比度。 */
function relativeLuminance({ r, g, b }) {
  const channel = (value) => {
    const scaled = value / 255
    return scaled <= 0.03928 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}
function contrastRatio(foreground, background) {
  const [lighter, darker] = [relativeLuminance(foreground), relativeLuminance(background)].sort(
    (left, right) => right - left,
  )
  return (lighter + 0.05) / (darker + 0.05)
}

/* ---------- 「两种绿」验收：emerald 旧色值不得出现在任何品牌面 ---------- */

/** emerald 家族（旧主题）色值：hex 与 rgb() 两种写法都要认出来。 */
const EMERALD_RGB = [
  [16, 185, 129], // emerald.500
  [5, 150, 105], // emerald.600
  [4, 120, 87], // emerald.700
  [6, 78, 59], // emerald.900
  [236, 253, 245], // emerald.50
  [209, 250, 229], // emerald.100
]
const EMERALD_HEXES = ['#10b981', '#059669', '#047857', '#064e3b', '#ecfdf5', '#d1fae5']

/** 返回 computed 值里命中的 emerald 色值；空数组表示干净。 */
function emeraldHits(value) {
  const compact = String(value || '').toLowerCase().replace(/\s+/g, '')
  const hits = []
  for (const hex of EMERALD_HEXES) if (compact.includes(hex)) hits.push(hex)
  for (const [r, g, b] of EMERALD_RGB) {
    if (compact.includes(`rgb(${r},${g},${b})`) || compact.includes(`rgba(${r},${g},${b},`)) {
      hits.push(`rgb(${r}, ${g}, ${b})`)
    }
  }
  return hits
}

/** 断言一个 computed 值不含 emerald；失败时把命中色值和实际值一起写进 detail。 */
function checkNoEmerald(label, value) {
  const hits = emeraldHits(value)
  check(label, hits.length === 0, hits.length > 0 ? `命中 ${hits.join(' / ')}：${value}` : String(value))
}

/* ---------- Playwright 定位（本机装全局包，项目不引依赖） ---------- */

function loadPlaywright() {
  const candidates = ['playwright']
  try {
    candidates.push(join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'))
  } catch {
    /* 没有 npm 也能继续尝试本地包 */
  }
  const errors = []
  for (const id of candidates) {
    try {
      return require(id)
    } catch (error) {
      errors.push(`${id}: ${error.message.split('\n')[0]}`)
    }
  }
  try {
    const globalRoot = execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim()
    return { __esm: pathToFileURL(join(globalRoot, 'playwright', 'index.mjs')).href }
  } catch {
    /* ignore */
  }
  const hint = [
    '找不到 playwright —— 这是环境缺失，不是断言失败。',
    '安装：pnpm --dir web-next add -D playwright && pnpm --dir web-next exec playwright install chromium',
    ...errors.map((line) => `  尝试过 ${line}`),
  ].join('\n')
  return { error: hint }
}

/** 本机已有浏览器缓存、但与当前 playwright 版本要求的 revision 不一致时的兜底。 */
function findCachedChromium() {
  const roots = [
    process.env.PLAYWRIGHT_BROWSERS_PATH,
    platform() === 'darwin'
      ? join(homedir(), 'Library', 'Caches', 'ms-playwright')
      : join(homedir(), '.cache', 'ms-playwright'),
  ].filter(Boolean)
  const candidates = []
  for (const root of roots) {
    if (!existsSync(root)) continue
    let entries = []
    try {
      entries = readdirSync(root, { withFileTypes: true }).filter((entry) => entry.isDirectory())
    } catch {
      continue
    }
    for (const entry of entries) {
      const match = /^(?:chromium_headless_shell|chromium)-(\d+)$/.exec(entry.name)
      if (!match) continue
      const base = join(root, entry.name)
      const paths = platform() === 'darwin'
        ? [
            join(base, 'chrome-headless-shell-mac-arm64', 'chrome-headless-shell'),
            join(base, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium'),
          ]
        : [
            join(base, 'chrome-linux', 'chrome-headless-shell'),
            join(base, 'chrome-linux', 'chrome'),
          ]
      for (const path of paths) if (existsSync(path)) candidates.push({ revision: Number(match[1]), path })
    }
  }
  candidates.sort((left, right) => right.revision - left.revision)
  return candidates.length > 0 ? candidates[0].path : null
}

async function launchChromium(chromium) {
  try {
    return await chromium.launch({ headless: !HEADED })
  } catch (error) {
    const message = error && error.message ? error.message : String(error)
    if (!/Executable doesn't exist|is not found at/i.test(message)) throw error
    const cached = findCachedChromium()
    if (!cached) throw error
    console.warn(`! playwright 自带 chromium 缺失，改用缓存版本：${cached}`)
    return await chromium.launch({ headless: !HEADED, executablePath: cached })
  }
}

function createStaticServer(distDir) {
  const indexFile = join(distDir, 'index.html')
  return createServer((request, response) => {
    const urlPath = decodeURIComponent((request.url || '/').split('?')[0].split('#')[0])
    const relative = urlPath === '/' ? '/index.html' : urlPath
    const target = normalize(join(distDir, relative))
    const found = target.startsWith(distDir) && existsSync(target) && statSync(target).isFile()
      ? target
      : indexFile
    if (!existsSync(found)) {
      response.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' })
      response.end('not found')
      return
    }
    response.writeHead(200, {
      'content-type': MIME[extname(found)] || 'application/octet-stream',
      'cache-control': 'no-store',
    })
    createReadStream(found).pipe(response)
  })
}

function startServer(distDir) {
  return new Promise((resolvePromise, reject) => {
    const server = createStaticServer(distDir)
    server.once('error', reject)
    server.listen(REQUESTED_PORT, '127.0.0.1', () => {
      const address = server.address()
      const port = typeof address === 'object' && address ? address.port : REQUESTED_PORT
      resolvePromise({ server, base: `http://127.0.0.1:${port}` })
    })
  })
}

const DEMO_USER = {
  id: 'palette-check',
  username: 'admin',
  role: 'admin',
  role_name: '管理员',
  permissions: ['server.read', 'check.read', 'alert.read', 'task.read', 'deploy.read'],
  scope: { mode: 'all', group_ids: [], server_ids: [] },
}
const DEMO_TOKEN = 'palette-check-token'

/** 假后端：只覆盖登录跳板需要的两个端点，其余 404（不静默放过）。 */
async function installMockApi(context) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: DEMO_TOKEN, user: DEMO_USER })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      const authorization = request.headers().authorization || ''
      if (authorization === `Bearer ${DEMO_TOKEN}`) return json(200, { user: DEMO_USER })
      return json(401, { error: { code: 'unauthorized', message: 'missing or invalid token' } })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error(`✗ 没有构建产物：${DIST}/index.html\n  先跑 pnpm --dir web-next run build`)
    return 2
  }
  const loaded = loadPlaywright()
  if (loaded && loaded.error) {
    console.error(`✗ ${loaded.error}`)
    return 2
  }
  const playwright = loaded && loaded.__esm ? await import(loaded.__esm) : loaded
  const { chromium } = playwright.chromium ? playwright : playwright.default

  mkdirSync(OUT_DIR, { recursive: true })
  const { server, base } = await startServer(DIST)
  const browser = await launchChromium(chromium)
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await installMockApi(context)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))

  try {
    console.log(`\n品牌绿调色板验收：${base}（dist=${DIST}）\n`)

    // 1) 登录进首页（主按钮在 HomeView 的首屏）
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-password', { timeout: 15000 })

    // 1.1) 登录页主按钮：必须命中品牌绿 600，不能是 Vean 内置的 emerald 青绿
    const loginButton = page.locator('#login-submit')
    const readLoginButton = () =>
      loginButton.evaluate((element) => {
        const computed = window.getComputedStyle(element)
        return { background: computed.backgroundColor, color: computed.color }
      })
    const isBrand600 = (value) => {
      const color = parseRgb(value)
      return Boolean(color) && color.r === 22 && color.g === 163 && color.b === 74 && color.a === 1
    }

    // 1.2) 焦点环：用键盘 Tab 走到提交按钮（鼠标点不算 :focus-visible）
    const readFocusRing = () =>
      page.evaluate(() => {
        const element = document.activeElement
        if (!element) return { target: null, shadow: '', outline: '' }
        const computed = window.getComputedStyle(element)
        return {
          target: element.id || element.className || element.tagName,
          shadow: computed.boxShadow,
          outline: computed.outline,
        }
      })
    let focusedSubmit = false
    for (let index = 0; index < 12 && !focusedSubmit; index += 1) {
      await page.keyboard.press('Tab')
      focusedSubmit = await page.evaluate(() => document.activeElement?.id === 'login-submit')
    }
    check('键盘 Tab 可到达登录页主按钮（:focus-visible 生效）', focusedSubmit)

    const loginStyle = await readLoginButton()
    check('登录页主按钮填充 = rgb(22, 163, 74)（品牌 600）', isBrand600(loginStyle.background), loginStyle.background)
    checkNoEmerald('浅色模式登录页主按钮不含 emerald', `${loginStyle.background} ${loginStyle.color}`)

    const lightRing = await readFocusRing()
    check(
      '浅色模式焦点环 box-shadow 含品牌绿',
      /rgb\(34,\s*197,\s*94\)/.test(lightRing.shadow) || /rgb\(22,\s*163,\s*74\)/.test(lightRing.shadow),
      `${lightRing.target} → ${lightRing.shadow}`,
    )
    checkNoEmerald('浅色模式焦点环不含 emerald', `${lightRing.shadow} ${lightRing.outline}`)

    // 1.3) 深色模式下同一批品牌面重跑一遍（emerald 常藏在主题的 dark 分支里）
    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await page.waitForTimeout(250)
    const darkLoginStyle = await readLoginButton()
    check(
      '深色模式登录页主按钮填充 = rgb(22, 163, 74)（品牌 600）',
      isBrand600(darkLoginStyle.background),
      darkLoginStyle.background,
    )
    checkNoEmerald('深色模式登录页主按钮不含 emerald', `${darkLoginStyle.background} ${darkLoginStyle.color}`)
    const darkRing = await readFocusRing()
    check(
      '深色模式焦点环 box-shadow 含品牌绿',
      /rgb\(34,\s*197,\s*94\)/.test(darkRing.shadow) || /rgb\(22,\s*163,\s*74\)/.test(darkRing.shadow),
      `${darkRing.target} → ${darkRing.shadow}`,
    )
    checkNoEmerald('深色模式焦点环不含 emerald', `${darkRing.shadow} ${darkRing.outline}`)
    // 去掉 .dark 后等按钮 150ms 背景过渡走完，否则后面读到的是深色态的起点色
    await page.evaluate(() => document.documentElement.classList.remove('dark'))
    await page.waitForTimeout(300)

    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', 'palette-check')
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    check('登录后进入外壳页面', !page.url().includes('#/login'), page.url().split('#')[1] || '/')

    // 1.4) 侧栏 logo 渐变：品牌 500 → 600，必须含 #22c55e（旧版是纯 emerald 填充）
    const brandMark = page.locator('.ls-brand__mark')
    const readLogo = () =>
      brandMark.evaluate((element) => {
        const computed = window.getComputedStyle(element)
        return { image: computed.backgroundImage, background: computed.backgroundColor }
      })
    const lightLogo = await readLogo()
    check(
      '浅色模式侧栏 logo 渐变含 #22c55e',
      /rgb\(34,\s*197,\s*94\)/.test(lightLogo.image),
      lightLogo.image,
    )
    checkNoEmerald('浅色模式侧栏 logo 不含 emerald', `${lightLogo.image} ${lightLogo.background}`)

    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await page.waitForTimeout(250)
    const darkLogo = await readLogo()
    check(
      '深色模式侧栏 logo 渐变含 #22c55e',
      /rgb\(34,\s*197,\s*94\)/.test(darkLogo.image),
      darkLogo.image,
    )
    checkNoEmerald('深色模式侧栏 logo 不含 emerald', `${darkLogo.image} ${darkLogo.background}`)
    // 同上：等主按钮 150ms 背景/前景过渡回到浅色态再断言
    await page.evaluate(() => document.documentElement.classList.remove('dark'))
    await page.waitForTimeout(300)

    const primary = page.getByRole('button', { name: '开始使用' })
    await primary.waitFor({ state: 'visible', timeout: 10000 })

    // 2) 主按钮 computed background 必须命中品牌绿色阶
    const style = await primary.evaluate((element) => {
      const computed = window.getComputedStyle(element)
      return {
        background: computed.backgroundColor,
        color: computed.color,
        text: (element.textContent || '').trim(),
      }
    })
    const background = parseRgb(style.background)
    check('主按钮 computed background 可解析', Boolean(background), style.background)
    if (!background) throw new Error('无法解析主按钮背景色')

    const nearest = nearestScale(background)
    check(
      `主按钮填充命中品牌色阶 ${nearest.level}`,
      nearest.level === EXPECTED_LEVEL && nearest.distance <= 4,
      `实际 ${style.background} ≈ ${nearest.hex}（偏差 ${nearest.distance}/255）`,
    )

    const hue = hueOf(background)
    check(
      `色相 ${hue.toFixed(1)}° 属于品牌绿（< ${EMERALD_HUE_MIN}°）`,
      hue >= 120 && hue < EMERALD_HUE_MIN,
      'emerald 青绿约 160–162°',
    )

    // 3) 前景/背景对比度：AA 正文 4.5:1（深绿字压在品牌绿上）
    const foreground = parseRgb(style.color)
    check('主按钮 computed color 可解析', Boolean(foreground), style.color)
    if (foreground) {
      const ratio = contrastRatio(foreground, background)
      check('按钮文字对比度达 AA（≥ 4.5:1）', ratio >= 4.5, `${ratio.toFixed(2)}:1`)
    }

    // 4) 截图（静止态）：整页 + 按钮局部，供人工复核
    const pageShot = join(OUT_DIR, 'palette-check-page.png')
    const buttonShot = join(OUT_DIR, 'palette-check-button.png')
    const hoverShot = join(OUT_DIR, 'palette-check-button-hover.png')
    await page.screenshot({ path: pageShot })
    await primary.screenshot({ path: buttonShot })

    // 5) hover 等价阶：Vean solid hover = primary/80，叠在页面上更亮（≈ green.500 同族）
    const pageBackground = await page.evaluate(() => window.getComputedStyle(document.body).backgroundColor)
    await primary.hover()
    // Vean 的 transition-all-150：不等一下会读到过渡起点的颜色
    await page.waitForTimeout(250)
    const hoverStyle = await primary.evaluate((element) => {
      const computed = window.getComputedStyle(element)
      return { background: computed.backgroundColor }
    })
    await primary.screenshot({ path: hoverShot })
    console.log(`\n  截图：\n   · ${pageShot}\n   · ${buttonShot}\n   · ${hoverShot}`)

    const hover = parseRgb(hoverStyle.background)
    check('hover 背景可解析', Boolean(hover), hoverStyle.background)
    if (hover) {
      check('hover 走 primary/80 的提亮档（alpha 0.8）', Math.abs(hover.a - 0.8) <= 0.02, hoverStyle.background)
      // 半透明色要叠到页面底色上才是眼睛看到的颜色
      const surface = parseRgb(pageBackground) || { r: 255, g: 255, b: 255 }
      const composite = {
        r: hover.a * hover.r + (1 - hover.a) * surface.r,
        g: hover.a * hover.g + (1 - hover.a) * surface.g,
        b: hover.a * hover.b + (1 - hover.a) * surface.b,
      }
      const hovered = nearestScale(composite)
      check(
        `hover 合成色落在品牌绿色阶（${hovered.level} 档，偏差 ${hovered.distance.toFixed(1)}/255）且比填充更亮`,
        hovered.distance <= 40 && relativeLuminance(composite) > relativeLuminance(background),
        `rgb(${Math.round(composite.r)} ${Math.round(composite.g)} ${Math.round(composite.b)})`,
      )
    }

    // 6) 深色模式：preset.dark 生效 → 填充降亮一阶（green.700），前景回到近白
    await page.mouse.move(0, 0)
    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await page.waitForTimeout(250)
    const darkStyle = await primary.evaluate((element) => {
      const computed = window.getComputedStyle(element)
      return { background: computed.backgroundColor, color: computed.color }
    })
    const darkPageShot = join(OUT_DIR, 'palette-check-page-dark.png')
    await page.screenshot({ path: darkPageShot })
    console.log(`   · ${darkPageShot}`)

    const darkBackground = parseRgb(darkStyle.background)
    check('深色模式主按钮背景可解析', Boolean(darkBackground), darkStyle.background)
    if (darkBackground) {
      const nearestDark = nearestScale(darkBackground)
      check(
        `深色模式填充命中品牌色阶 ${nearestDark.level}（600 降亮一阶）`,
        nearestDark.level === DARK_EXPECTED_LEVEL && nearestDark.distance <= 4,
        `实际 ${darkStyle.background} ≈ ${nearestDark.hex}（偏差 ${nearestDark.distance}/255）`,
      )
      const darkForeground = parseRgb(darkStyle.color)
      if (darkForeground) {
        const darkRatio = contrastRatio(darkForeground, darkBackground)
        check('深色模式文字对比度达 AA（≥ 4.5:1）', darkRatio >= 4.5, `${darkRatio.toFixed(2)}:1`)
      }
    }
    await page.evaluate(() => document.documentElement.classList.remove('dark'))

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } finally {
    await browser.close()
    await new Promise((resolvePromise) => server.close(resolvePromise))
  }

  const failed = results.filter((result) => !result.pass)
  console.log(`\n结果：${results.length - failed.length}/${results.length} 通过\n`)
  return failed.length === 0 ? 0 : 1
}

process.exitCode = await main()
