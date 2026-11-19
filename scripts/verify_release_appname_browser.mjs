#!/usr/bin/env node
/**
 * 发布应用名「真机」断言（r3，配合 scripts/verify_release_appname.sh 使用）。
 *
 * 覆盖脚本里无法只用 curl 证明的两条：
 *   ④ 首页（/#/）与 /#/releases 页渲染出的应用名逐字相同，且等于所建应用名；
 *   ⑤ 旧后端兼容：反代把 GET /api/v1/releases 响应里的 app_name 删掉（保留 app_slug），
 *      前端必须靠 /apps 映射出应用名 —— 若还显示 slug，说明 releaseAppLabel 的
 *      优先级仍是 app_name || app_slug || appNames，本断言会红。
 *
 * 反代是进程内的，不需要额外依赖；playwright 从本地或全局 node_modules 解析，
 * chromium 缺失时退到 ms-playwright 缓存里最新的版本（与 smoke-alerts.mjs 同口径）。
 *
 * 独立运行：
 *   VERIFY_BASE=http://127.0.0.1:17660 \
 *   VERIFY_APP_ID=<app-id> VERIFY_RELEASE_ID=<release-id> \
 *   VERIFY_APP_NAME='应用名' VERIFY_APP_SLUG=app-slug \
 *   VERIFY_PROXY_PORT=17661 VERIFY_ADMIN_PASS='<admin-pass>' \
 *   node scripts/verify_release_appname_browser.mjs
 *
 * 输出：每个断言一行 `RESULT <id> PASS|FAIL <detail>`，失败退出码 1，环境缺失退出码 2。
 */
import { createServer, request } from 'node:http'
import { existsSync, readdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { homedir, platform } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { brotliDecompressSync, gunzipSync, inflateSync } from 'node:zlib'

const HERE = dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)

const cfg = {
  base: (process.env.VERIFY_BASE || '').replace(/\/$/, ''),
  appId: process.env.VERIFY_APP_ID || '',
  releaseId: process.env.VERIFY_RELEASE_ID || '',
  appName: process.env.VERIFY_APP_NAME || '',
  appSlug: process.env.VERIFY_APP_SLUG || '',
  proxyPort: Number(process.env.VERIFY_PROXY_PORT || 0),
  password: process.env.VERIFY_ADMIN_PASS || '',
}

const results = []
function record(id, ok, detail) {
  results.push({ id, ok })
  console.log(`RESULT ${id} ${ok ? 'PASS' : 'FAIL'} ${detail}`)
}
function info(message) {
  console.log(`INFO ${message}`)
}

// ---------------------------------------------------------------- HTTP 小工具

function httpJSON(urlStr, { method = 'GET', token = '', body = null } = {}) {
  return new Promise((resolvePromise, reject) => {
    const url = new URL(urlStr)
    const payload = body === null ? null : Buffer.from(JSON.stringify(body))
    const headers = { accept: 'application/json' }
    if (payload) {
      headers['content-type'] = 'application/json'
      headers['content-length'] = String(payload.length)
    }
    if (token) headers.authorization = `Bearer ${token}`
    const req = request(
      { host: url.hostname, port: url.port, path: `${url.pathname}${url.search}`, method, headers },
      (res) => {
        const chunks = []
        res.on('data', (chunk) => chunks.push(chunk))
        res.on('end', () => {
          const raw = decodeBody(Buffer.concat(chunks), res.headers)
          try {
            resolvePromise({ status: res.statusCode, json: JSON.parse(raw.toString('utf8')) })
          } catch {
            reject(new Error(`${method} ${urlStr} 返回非 JSON（HTTP ${res.statusCode}）`))
          }
        })
      },
    )
    req.on('error', reject)
    if (payload) req.write(payload)
    req.end()
  })
}

function decodeBody(buffer, headers) {
  const encoding = String(headers['content-encoding'] || '').toLowerCase()
  try {
    if (encoding === 'gzip') return gunzipSync(buffer)
    if (encoding === 'deflate') return inflateSync(buffer)
    if (encoding === 'br') return brotliDecompressSync(buffer)
  } catch {
    /* 解压失败时按原文交给 JSON.parse 报错，保留原始错误现场 */
  }
  return buffer
}

async function loginToken(base) {
  const { status, json } = await httpJSON(`${base}/api/v1/auth/login`, {
    method: 'POST',
    body: { username: 'admin', password: cfg.password },
  })
  if (status !== 200 || !json?.data?.token) throw new Error(`HTTP 登录失败：status=${status}`)
  return json.data.token
}

// ---------------------------------------------------------------- 反代：删 app_name，保留 app_slug

function stripAppName(value) {
  if (Array.isArray(value)) {
    for (const item of value) stripAppName(item)
    return
  }
  if (value && typeof value === 'object') {
    delete value.app_name
    for (const key of Object.keys(value)) stripAppName(value[key])
  }
}

function startProxy({ targetPort, port }) {
  let stripped = 0
  const server = createServer((req, res) => {
    const url = req.url || '/'
    const isReleasesRead = req.method === 'GET' && /^\/api\/v1\/releases(\/|\?|$)/.test(url)
    const headers = { ...req.headers }
    if (isReleasesRead) headers['accept-encoding'] = 'identity'
    const upstream = request(
      { host: '127.0.0.1', port: targetPort, path: url, method: req.method, headers },
      (up) => {
        if (!isReleasesRead) {
          res.writeHead(up.statusCode || 502, up.headers)
          up.pipe(res)
          return
        }
        const chunks = []
        up.on('data', (chunk) => chunks.push(chunk))
        up.on('end', () => {
          const decoded = decodeBody(Buffer.concat(chunks), up.headers).toString('utf8')
          try {
            const json = JSON.parse(decoded)
            stripAppName(json)
            stripped += 1
            const out = Buffer.from(JSON.stringify(json), 'utf8')
            const outHeaders = { ...up.headers }
            delete outHeaders['content-encoding']
            delete outHeaders['content-length']
            delete outHeaders['transfer-encoding']
            outHeaders['content-type'] = 'application/json; charset=utf-8'
            outHeaders['content-length'] = String(out.length)
            res.writeHead(up.statusCode || 200, outHeaders)
            res.end(out)
          } catch {
            // 非 JSON（理论上不会发生）就原样回传，让浏览器侧断言暴露问题。
            res.writeHead(up.statusCode || 502, up.headers)
            res.end(decoded)
          }
        })
      },
    )
    upstream.on('error', (error) => {
      res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' })
      res.end(`proxy error: ${error.message}`)
    })
    req.pipe(upstream)
  })
  return new Promise((resolvePromise, reject) => {
    server.once('error', reject)
    server.listen(port, '127.0.0.1', () => resolvePromise({ server, strippedCount: () => stripped }))
  })
}

// ---------------------------------------------------------------- playwright

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
  throw new Error(['找不到 playwright —— 这是环境缺失，不是断言失败。', ...errors.map((l) => `  尝试过 ${l}`)].join('\n'))
}

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
      const paths =
        platform() === 'darwin'
          ? [
              join(base, 'chrome-headless-shell-mac-arm64', 'chrome-headless-shell'),
              join(base, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium'),
            ]
          : [join(base, 'chrome-linux', 'chrome-headless-shell'), join(base, 'chrome-linux', 'chrome')]
      for (const candidate of paths) {
        if (existsSync(candidate)) candidates.push({ revision: Number(match[1]), path: candidate })
      }
    }
  }
  candidates.sort((left, right) => right.revision - left.revision)
  return candidates.length > 0 ? candidates[0].path : null
}

async function launchChromium(chromium) {
  try {
    return await chromium.launch({ headless: true })
  } catch (error) {
    const message = error && error.message ? error.message : String(error)
    if (!/Executable doesn't exist|is not found at/i.test(message)) throw error
    const cached = findCachedChromium()
    if (!cached) throw error
    info(`playwright 自带 chromium 缺失，改用缓存版本 ${cached}`)
    return chromium.launch({ headless: true, executablePath: cached })
  }
}

async function login(page, base) {
  await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('#login-username', { timeout: 20000 })
  await page.fill('#login-username', 'admin')
  await page.fill('#login-password', cfg.password)
  await page.click('#login-submit')
  await page.waitForSelector('.ls-shell', { timeout: 20000 })
}

const dashSelector = () => `.ls-dashboard__release[data-release-id="${cfg.releaseId}"] .ls-dashboard__release-app`
const rowSelector = () => `.ls-deploy__row[data-release-id="${cfg.releaseId}"] .ls-deploy__name`

async function readTrimmed(page, selector) {
  const text = await page.locator(selector).first().innerText({ timeout: 20000 })
  return text.trim()
}

// ---------------------------------------------------------------- 主流程

async function main() {
  const missing = ['base', 'appId', 'releaseId', 'appName', 'appSlug', 'password'].filter((key) => !cfg[key])
  if (missing.length > 0 || !cfg.proxyPort) {
    console.error(`✗ 参数缺失：${[...missing, cfg.proxyPort ? '' : 'proxyPort'].filter(Boolean).join(', ')}`)
    process.exitCode = 2
    return
  }
  if (cfg.appName === cfg.appSlug) {
    console.error('✗ 应用名与 slug 相同，无法区分「走 /apps 映射」与「退回 slug」')
    process.exitCode = 2
    return
  }

  const playwright = loadPlaywright()
  const browser = await launchChromium(playwright.chromium)
  let proxy
  try {
    // ---- ④ 真机：首页 vs /releases，逐字相同且等于应用名
    const contextA = await browser.newContext()
    const pageA = await contextA.newPage()
    await login(pageA, cfg.base)

    await pageA.goto(`${cfg.base}/#/`, { waitUntil: 'domcontentloaded' })
    await pageA.waitForSelector(dashSelector(), { timeout: 20000 })
    const dashLabel = await readTrimmed(pageA, dashSelector())
    record('direct-dashboard', dashLabel === cfg.appName, `首页显示 ${JSON.stringify(dashLabel)}，期望 ${JSON.stringify(cfg.appName)}`)

    await pageA.goto(`${cfg.base}/#/releases`, { waitUntil: 'domcontentloaded' })
    await pageA.waitForSelector(rowSelector(), { timeout: 20000 })
    const rowLabel = await readTrimmed(pageA, rowSelector())
    record('direct-releases', rowLabel === cfg.appName, `/releases 显示 ${JSON.stringify(rowLabel)}，期望 ${JSON.stringify(cfg.appName)}`)
    record('direct-identical', dashLabel === rowLabel && dashLabel.length > 0, `首页=${JSON.stringify(dashLabel)} / 列表=${JSON.stringify(rowLabel)}`)
    await contextA.close()

    // ---- ⑤ 反代删掉 app_name（保留 app_slug），前端必须靠 /apps 映射
    proxy = await startProxy({ targetPort: new URL(cfg.base).port || 80, port: cfg.proxyPort })
    const proxyBase = `http://127.0.0.1:${cfg.proxyPort}`
    const token = await loginToken(proxyBase)
    const proxied = await httpJSON(`${proxyBase}/api/v1/releases?limit=50`, { token })
    const direct = await httpJSON(`${cfg.base}/api/v1/releases?limit=50`, { token })
    const proxiedRelease = (proxied.json?.data || []).find((rel) => rel.id === cfg.releaseId)
    const directRelease = (direct.json?.data || []).find((rel) => rel.id === cfg.releaseId)
    if (!proxiedRelease) throw new Error('反代响应里找不到本次发布单')
    if (!directRelease) throw new Error('直连响应里找不到本次发布单')

    const stripOK =
      !Object.prototype.hasOwnProperty.call(proxiedRelease, 'app_name') &&
      proxiedRelease.app_slug === cfg.appSlug &&
      Object.prototype.hasOwnProperty.call(directRelease, 'app_name') &&
      directRelease.app_name === cfg.appName
    record(
      'proxy-strip',
      stripOK,
      `反代 app_name=${JSON.stringify(proxiedRelease.app_name)} app_slug=${JSON.stringify(proxiedRelease.app_slug)}；` +
        `直连 app_name=${JSON.stringify(directRelease.app_name)}；已改写 ${proxy.strippedCount()} 个响应`,
    )

    const contextB = await browser.newContext()
    const pageB = await contextB.newPage()
    await login(pageB, proxyBase)
    await pageB.goto(`${proxyBase}/#/releases`, { waitUntil: 'domcontentloaded' })
    await pageB.waitForSelector(rowSelector(), { timeout: 20000 })
    const fallbackLabel = await readTrimmed(pageB, rowSelector())
    record(
      'proxy-fallback',
      fallbackLabel === cfg.appName,
      `反代页显示 ${JSON.stringify(fallbackLabel)}，期望应用名 ${JSON.stringify(cfg.appName)}（slug=${JSON.stringify(cfg.appSlug)}；` +
        `显示 slug 即说明仍在用旧优先级）`,
    )
    await contextB.close()
  } finally {
    await browser.close().catch(() => {})
    if (proxy) await new Promise((done) => proxy.server.close(done))
  }

  if (results.some((entry) => !entry.ok)) process.exitCode = 1
}

main().catch((error) => {
  console.error(`✗ 浏览器断言异常：${error && error.stack ? error.stack : error}`)
  process.exitCode = 1
})
