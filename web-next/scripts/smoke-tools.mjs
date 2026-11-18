#!/usr/bin/env node
/**
 * 终端 / 文件 / 日志三个入口页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：三个 kind 的标题与深链 kind / 点行跳到 `#/servers/<id>?tab=<kind>` /
 * 关键字筛选与计数 / 无 terminal.use 时 0 请求并落 /forbidden / 空态与「去添加服务器」入口 /
 * 首屏失败可重试 / 320px 无横向滚动。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-tools.mjs [--headed]
 */
import { createServer } from 'node:http'
import { createReadStream, existsSync, readdirSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { homedir, platform } from 'node:os'
import { dirname, extname, join, normalize, resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
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

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.map': 'application/json; charset=utf-8',
}

const TOKEN = 'tools-smoke-token'
const PASSWORD = 'tools-smoke-password'

function user(terminalUse) {
  return {
    id: 'smoke-tools-user',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions: terminalUse ? ['server.read', 'terminal.use'] : ['server.read'],
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

function serverFixture(overrides) {
  return {
    id: 'srv-alpha',
    name: 'alpha-node',
    host: '10.0.0.11',
    port: 22,
    mode: 'agent',
    status: 'online',
    last_seen_at: Date.now() - 30_000,
    agent_version: '1.4.0',
    os: 'linux',
    arch: 'amd64',
    kernel: '6.8.0',
    tags: ['prod', 'edge'],
    metrics_summary: { cpu_pct: 12.5, mem_pct: 33.1, disk_pct: 40 },
    created_at: Date.now() - 86_400_000,
    group: '默认分组',
    group_ids: ['grp-default'],
    group_names: ['默认分组'],
    ...overrides,
  }
}

const SERVERS = [
  serverFixture({ id: 'srv-alpha' }),
  serverFixture({
    id: 'srv-beta',
    name: 'beta-node',
    host: '10.0.0.12',
    status: 'offline',
    last_seen_at: null,
    tags: ['staging'],
    group: '测试分组',
    group_ids: ['grp-test'],
    group_names: ['测试分组'],
    os: 'darwin',
    arch: 'arm64',
  }),
]

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

async function resolvePlaywright() {
  const loaded = loadPlaywright()
  if (loaded && loaded.error) throw new Error(loaded.error)
  if (loaded && loaded.__esm) {
    const mod = await import(loaded.__esm)
    return mod.chromium ? mod : mod.default
  }
  return loaded
}

/** playwright 自带的 revision 与缓存不一致时，退到缓存里最新的 chromium。 */
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
      for (const path of paths) {
        if (existsSync(path)) candidates.push({ revision: Number(match[1]), path })
      }
    }
  }
  candidates.sort((left, right) => right.revision - left.revision)
  return candidates.length > 0 ? candidates[0].path : null
}

async function launchChromium(chromium, headless) {
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

/** 托管 dist 的静态服务器；未知路径回 index.html（hash 路由）。 */
function createStaticServer(distDir) {
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

/** 假后端：只覆盖入口页需要的端点，其他一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: user(state.terminalUse) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      state.meCalls += 1
      const current = user(state.terminalUse)
      state.mePermissions = current.permissions.join(',')
      return json(200, { data: current })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }

    // 只统计精确的 /servers 列表请求：详情页的 /servers/<id> 不算。
    if (path === '/api/v1/servers' && method === 'GET') {
      state.serverCalls += 1
      if (state.serverFail) {
        return json(500, { error: { code: 'internal', message: 'mock servers failure' } })
      }
      return json(200, { data: state.serversEmpty ? [] : SERVERS })
    }

    const detail = /^\/api\/v1\/servers\/([^/]+)$/.exec(path)
    if (detail && method === 'GET') {
      const found = SERVERS.find((item) => item.id === decodeURIComponent(detail[1]))
      return found ? json(200, { data: found }) : json(404, { error: { code: 'not_found' } })
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

async function run() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error(`✗ 没有构建产物：${join(DIST, 'index.html')}\n  先跑：pnpm --dir web-next run build`)
    process.exitCode = 2
    return
  }

  let chromium
  try {
    const playwright = await resolvePlaywright()
    chromium = playwright && playwright.chromium
  } catch (error) {
    console.error(`✗ ${error.message}`)
    process.exitCode = 2
    return
  }
  if (!chromium) {
    console.error('✗ playwright 已加载但没有 chromium 导出 —— 环境异常。')
    process.exitCode = 2
    return
  }

  const results = []
  const check = (name, ok, detail = '') => {
    results.push({ name, ok, detail })
    console.log(`${ok ? '✓' : '✗'} ${name}${detail ? `：${detail}` : ''}`)
  }

  const server = createStaticServer(DIST)
  await new Promise((done, fail) => {
    server.once('error', fail)
    server.listen(REQUESTED_PORT, '127.0.0.1', done)
  })
  const base = `http://127.0.0.1:${server.address().port}`

  let browser
  try {
    browser = await launchChromium(chromium, !HEADED)
  } catch (error) {
    console.error(`✗ 启动 chromium 失败：${error && error.message ? error.message.split('\n')[0] : error}`)
    await new Promise((done) => server.close(done))
    process.exitCode = 2
    return
  }

  const state = { terminalUse: false, serverCalls: 0, serversEmpty: false, serverFail: false, meCalls: 0, mePermissions: '' }
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  await installMockApi(context, state)
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))

  const hash = () => page.evaluate(() => window.location.hash)
  const titleText = async () => (await page.locator('.ls-toolspage__title').innerText()).trim()
  const waitTitle = () => page.waitForSelector('.ls-toolspage__title', { timeout: 15000 })
  const go = async (path) => {
    await page.goto(`${base}/#${path}`, { waitUntil: 'domcontentloaded' })
  }
  // 同文档只有 hash 变化时不会重新挂载应用：先离开文档再进目标 hash，
  // 否则旧的会话状态会先把人弹到 /forbidden，重载回来的就是 forbidden 页了。
  const goFresh = async (path) => {
    await page.goto('about:blank')
    await go(path)
  }

  try {
    // 0) 无 terminal.use 登录：登录后落 /（不请求 /servers）
    await page.goto(`${base}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#login-username', { timeout: 15000 })
    await page.fill('#login-username', 'admin')
    await page.fill('#login-password', PASSWORD)
    await page.click('#login-submit')
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.waitForTimeout(300)

    // 1) 无权限：路由守卫挡到 /forbidden，且一个 /servers 请求都不发
    const callsBeforeDenied = state.serverCalls
    await go('/terminal')
    await page.waitForSelector('.ls-forbidden', { timeout: 15000 })
    check('无 terminal.use 时进入 /forbidden', (await hash()).startsWith('#/forbidden'), await hash())
    check(
      '无权限时一个 /servers 请求都不发',
      state.serverCalls === callsBeforeDenied,
      `${callsBeforeDenied} → ${state.serverCalls}`,
    )
    check('无权限时不渲染入口页列表', (await page.locator('.ls-toolspage').count()) === 0)

    // 2) 有权限：/terminal 标题与 kind
    state.terminalUse = true
    const callsBeforeTerminal = state.serverCalls
    await goFresh('/terminal')
    await waitTitle()
    check('terminal 标题正确', (await titleText()) === '选择服务器打开终端', await titleText())
    check(
      'terminal 路由带 data-tool-kind',
      (await page.locator('.ls-toolspage').getAttribute('data-tool-kind')) === 'terminal',
    )
    check('entry 页只拉一次 /servers', state.serverCalls - callsBeforeTerminal === 1, `${state.serverCalls - callsBeforeTerminal} 次`)
    check('两台服务器都渲染成可点行', (await page.locator('.ls-toolspage__row').count()) === 2)

    // 3) 关键字筛选与计数
    await page.fill('#tools-search-input', 'beta')
    await page.waitForTimeout(150)
    check('关键字筛选只留 beta', (await page.locator('.ls-toolspage__row').count()) === 1)
    check(
      '计数文案跟随筛选',
      (await page.locator('.ls-toolspage__count').innerText()).includes('1 / 2'),
      await page.locator('.ls-toolspage__count').innerText(),
    )
    await page.fill('#tools-search-input', 'no-such-server')
    await page.waitForTimeout(150)
    check('无匹配时显示空搜索提示', (await page.locator('#tools-no-match').count()) === 1)
    await page.click('#tools-no-match .ls-btn')
    await page.waitForTimeout(150)
    check('清空搜索恢复两行', (await page.locator('.ls-toolspage__row').count()) === 2)

    // 4) 点行 → 详情页深链 ?tab=terminal
    await page.click('.ls-toolspage__row[data-server-id="srv-alpha"]')
    await page.waitForFunction(() => window.location.hash === '#/servers/srv-alpha?tab=terminal', null, {
      timeout: 10000,
    })
    check('点行后 hash 为 #/servers/srv-alpha?tab=terminal', true, await hash())

    // 5) /files 与 /logs 复用同一页、各自 kind
    await goFresh('/files')
    await waitTitle()
    check('files 标题正确', (await titleText()) === '选择服务器浏览文件', await titleText())
    await page.click('.ls-toolspage__row[data-server-id="srv-beta"]')
    await page.waitForFunction(() => window.location.hash === '#/servers/srv-beta?tab=files', null, {
      timeout: 10000,
    })
    check('files 点行后 hash 带 tab=files', true, await hash())

    await goFresh('/logs')
    await waitTitle()
    check('logs 标题正确', (await titleText()) === '选择服务器查看日志', await titleText())
    check(
      'logs 路由带 data-tool-kind',
      (await page.locator('.ls-toolspage').getAttribute('data-tool-kind')) === 'logs',
    )

    // 6) 有权限但没服务器：空态 + 去添加服务器入口
    state.serversEmpty = true
    await goFresh('/files')
    await page.waitForSelector('#tools-empty', { timeout: 15000 })
    check('没有服务器时显示空态', (await page.locator('#tools-empty').count()) === 1)
    check(
      '空态给出「去添加服务器」入口',
      (await page.locator('#tools-add-server').innerText()).trim() === '去添加服务器',
      (await page.locator('#tools-add-server').count()) === 0
        ? '入口不存在'
        : (await page.locator('#tools-add-server').innerText()).trim(),
    )
    check(
      '添加入口指向服务器列表',
      (await page.locator('#tools-add-server').getAttribute('href')) === '#/servers',
      String(await page.locator('#tools-add-server').getAttribute('href')),
    )

    // 7) 首屏失败：错误态可见，重试后恢复
    state.serversEmpty = false
    state.serverFail = true
    await goFresh('/terminal')
    await page.waitForSelector('#tools-error', { timeout: 15000 })
    check('首屏失败显示错误态与重试', (await page.locator('#tools-retry').count()) >= 1)
    state.serverFail = false
    await page.click('#tools-error #tools-retry')
    await page.waitForSelector('.ls-toolspage__row', { timeout: 15000 })
    check('重试后列表恢复', (await page.locator('.ls-toolspage__row').count()) === 2)

    // 8) 320px：不横向滚动、行仍在视口内
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(200)
    const narrow = await page.evaluate(() => {
      const row = document.querySelector('.ls-toolspage__row')
      const rect = row ? row.getBoundingClientRect() : null
      return {
        overflow: document.documentElement.scrollWidth - window.innerWidth,
        rowVisible: Boolean(rect && rect.width > 0 && rect.right <= window.innerWidth + 1),
      }
    })
    check('320px 入口页无横向滚动', narrow.overflow <= 0, `溢出 ${narrow.overflow}px`)
    check('320px 服务器行仍在视口内', narrow.rowVisible)

    check('页面无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))
  } catch (error) {
    const snapshot = await page
      .evaluate(() => ({
        hash: window.location.hash,
        text: (document.querySelector('#app')?.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 200),
        token: window.localStorage.length > 0,
      }))
      .catch(() => ({ hash: '?', text: '?', token: '?' }))
    check(
      '冒烟执行未抛异常',
      false,
      `${error && error.message ? error.message.split('\n')[0] : error} | hash=${snapshot.hash} token=${snapshot.token} me=${state.meCalls}[${state.mePermissions}] body=${snapshot.text}`,
    )
  } finally {
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const failed = results.filter((item) => !item.ok)
  console.log(
    `\n${failed.length === 0 ? '✓ 入口页冒烟全部通过' : `✗ ${failed.length}/${results.length} 项失败`}（${results.length} 项）`,
  )
  if (failed.length > 0) {
    for (const item of failed) console.log(`  - ${item.name}${item.detail ? `：${item.detail}` : ''}`)
    process.exitCode = 1
  }
}

run().catch((error) => {
  console.error(error && error.stack ? error.stack : error)
  process.exitCode = 1
})
