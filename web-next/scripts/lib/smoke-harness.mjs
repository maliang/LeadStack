#!/usr/bin/env node
/**
 * 发布域冒烟脚本共用的 hermetic 基座：静态站点 + Playwright 无头浏览器。
 *
 * 从 smoke-servers.mjs 抽出来，避免 apps / releases 两个脚本各抄一份加载与启动逻辑；
 * 行为与原来一致：playwright 先按本地包找，再退到全局包，最后退到缓存里的 chromium。
 */
import { createReadStream, existsSync, readdirSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { homedir, platform } from 'node:os'
import { extname, join, normalize, resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

const require = createRequire(import.meta.url)

export const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.map': 'application/json; charset=utf-8',
}

/** 解析 `--name value` / `--flag` 形式的参数。 */
export function parseFlags(argv) {
  return (name, fallback = null) => {
    const index = argv.indexOf(`--${name}`)
    if (index === -1) return fallback
    const next = argv[index + 1]
    return next && !next.startsWith('--') ? next : true
  }
}

export function log(step, message) {
  console.log(`  [${step}] ${message}`)
}

/** 检查器：逐条打印结果，末尾汇总并给出退出码。 */
export function makeChecker() {
  const results = []
  return {
    results,
    check(name, ok, detail = '') {
      results.push({ name, ok: Boolean(ok), detail })
      console.log(`${ok ? '✓' : '✗'} ${name}${detail ? `：${detail}` : ''}`)
    },
    finish() {
      const failed = results.filter((item) => !item.ok)
      console.log(`\n${failed.length === 0 ? '✔' : '✗'} 共 ${results.length} 项，失败 ${failed.length} 项`)
      return failed.length === 0 ? 0 : 1
    },
  }
}

/** 托管 dist 的静态服务器；未知路径回 index.html（hash 路由）。 */
export function createStaticServer(distDir) {
  const indexFile = join(distDir, 'index.html')
  return createServer((request, response) => {
    const urlPath = decodeURIComponent((request.url || '/').split('?')[0].split('#')[0])
    const relative = urlPath === '/' ? '/index.html' : urlPath
    const target = normalize(join(distDir, relative))
    const inside = target.startsWith(distDir)
    const found = inside && existsSync(target) && statSync(target).isFile() ? target : indexFile
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

export async function startStaticServer(distDir, port = 0) {
  const server = createStaticServer(distDir)
  await new Promise((done, fail) => {
    server.once('error', fail)
    server.listen(port, '127.0.0.1', done)
  })
  return { server, base: `http://127.0.0.1:${server.address().port}` }
}

/** 从本地或全局 node_modules 载入 playwright；找不到按环境缺失处理（退出码 2）。 */
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
      errors.push(`${id}: ${error.message}`)
    }
  }
  try {
    const globalRoot = execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim()
    return { __esm: pathToFileURL(join(globalRoot, 'playwright', 'index.mjs')).href }
  } catch {
    /* ignore */
  }
  return {
    error: ['找不到 playwright —— 这是环境缺失，不是断言失败。', ...errors.map((line) => `  尝试过 ${line}`)].join(
      '\n',
    ),
  }
}

export async function loadChromium() {
  const loaded = loadPlaywright()
  if (loaded && loaded.error) throw new Error(loaded.error)
  if (loaded && loaded.__esm) {
    const mod = await import(loaded.__esm)
    const playwright = mod.chromium ? mod : mod.default
    if (playwright && playwright.chromium) return playwright.chromium
  } else if (loaded && loaded.chromium) {
    return loaded.chromium
  }
  throw new Error('playwright 已加载但没有 chromium 导出 —— 环境异常。')
}

/** playwright 自带的 revision 与缓存不一致时，退到缓存里最新的 chromium。 */
export function findCachedChromium() {
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
      const paths =
        platform() === 'darwin'
          ? [
              join(base, 'chrome-headless-shell-mac-arm64', 'chrome-headless-shell'),
              join(base, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium'),
            ]
          : [join(base, 'chrome-linux', 'chrome-headless-shell'), join(base, 'chrome-linux', 'chrome')]
      for (const path of paths) {
        if (existsSync(path)) candidates.push({ revision: Number(match[1]), path })
      }
    }
  }
  candidates.sort((left, right) => right.revision - left.revision)
  return candidates.length > 0 ? candidates[0].path : null
}

export async function launchChromium(chromium, headless) {
  try {
    return await chromium.launch({ headless })
  } catch (error) {
    const message = error && error.message ? error.message : String(error)
    if (!/Executable doesn't exist|is not found at/i.test(message)) throw error
    const cached = findCachedChromium()
    if (!cached) throw error
    console.warn(`! playwright 自带 chromium 缺失，改用缓存版本：${cached}`)
    return await chromium.launch({ headless, executablePath: cached })
  }
}

/** 登录：mock token 落到 localStorage 后回到业务页（与 smoke-servers 相同的入口）。 */
export async function login(page, base, { username = 'admin', password = 'smoke-password' } = {}) {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 15000 })
  await page.fill('#login-username', username)
  await page.fill('#login-password', password)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 15000 })
}

/** 记录所有 API 请求，便于断言「无权限时 0 写请求」。 */
export function watchApiCalls(page) {
  const calls = []
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/')) {
      const url = new URL(request.url())
      calls.push({ method: request.method(), path: url.pathname, url: request.url() })
    }
  })
  return calls
}

export function writes(calls) {
  return calls.filter((call) => ['POST', 'PUT', 'PATCH', 'DELETE'].includes(call.method) && !call.path.endsWith('/auth/login'))
}

export function resolveDist(webRoot, flag) {
  const dist = resolve(webRoot, String(flag('dist', 'dist')))
  return { dist, ready: existsSync(join(dist, 'index.html')) }
}
