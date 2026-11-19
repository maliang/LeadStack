#!/usr/bin/env node
/**
 * 系统杂项六页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖 /tokens /audit /backups /settings /credentials /groups：
 *   ① 六页都能渲染且标题正确；
 *   ② 令牌签发后明文只出现一次，关闭弹窗与刷新页面后都读不到；吊销令牌发出 DELETE；
 *   ③ 审计页无任何编辑/删除按钮，筛选参数与 cursor 分页正确，详情可展开；
 *   ④ 备份列表显示大小/时间/schema 版本，创建带 include_artifacts，下载触发下载，删除二次确认；
 *   ⑤ 设置页保存网络后从服务端读回新值，密码不一致时不发请求；
 *   ⑥ 凭据列表不含明文密码/私钥，新建 key 凭据提交后仍未回显；删除二次确认；
 *   ⑦ 分组新建/编辑/删除与成员加入、移除（整包替换 PUT 的 server_ids）；
 *   ⑧ 六页在 320px 下无横向滚动，全程无未捕获异常。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-ops.mjs [--headed] [--shots dir]
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
  watchApiCalls,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const SHOTS = typeof flag('shots', null) === 'string' ? resolve(String(flag('shots'))) : null

const TOKEN = 'smoke-password-smoke-token'
const PASSWORD = 'smoke-password'

/** 只在一次性弹窗里出现过的令牌明文：任何持久渲染都不该包含它。 */
const PLAINTEXT_TOKEN = '{{SECRET_fj4zdk3d}}'
/** 已存在的凭据密文：mock 的 GET /credentials 从不返回，DOM 里也不该出现。 */
const SEEDED_PASSWORD = 'root-Pa55-secret'
const NEW_PASSWORD = '{{SECRET_qy2xkd8w}}'
const SEEDED_KEY = '{{SECRET_8d9dsk3q}}'
const NEW_KEY = '{{SECRET_6c2mkz9p}}'

const NOW = Date.now()

function userFixture(permissions) {
  return {
    id: 'u-admin',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

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

function tokenFixture(overrides = {}) {
  return {
    id: 'tok-1',
    name: 'ci-deploy',
    role: 'operator',
    created_by: 'admin',
    created_at: NOW - 86_400_000,
    last_used_at: NOW - 3_600_000,
    expires_at: NOW + 86_400_000,
    revoked_at: null,
    ...overrides,
  }
}

function backupName() {
  return 'leadstack-backup-20260119-120000.tar.gz.enc'
}

function backupFixture(overrides = {}) {
  return {
    name: backupName(),
    size_bytes: 12_582_912,
    created_at: NOW - 7_200_000,
    includes_artifacts: true,
    schema_version: 12,
    sha256: 'a'.repeat(64),
    ...overrides,
  }
}

function serverGroupFixture(overrides = {}) {
  return {
    id: 'grp-prod',
    name: '生产分组',
    description: '生产环境服务器',
    member_count: 2,
    created_at: NOW - 172_800_000,
    updated_at: NOW - 3_600_000,
    ...overrides,
  }
}

function serverFixture(id, name, host, port) {
  return {
    id,
    name,
    host,
    port,
    mode: 'ssh',
    status: 'online',
    last_seen_at: NOW - 30_000,
    agent_version: null,
    os: 'linux',
    arch: 'amd64',
    kernel: '6.8.0',
    tags: [],
    metrics_summary: null,
    created_at: NOW - 86_400_000,
    group: '生产分组',
    group_ids: ['grp-prod'],
    group_names: ['生产分组'],
  }
}

function makeState() {
  return {
    permissions: ALL_PERMISSIONS,
    tokens: [
      tokenFixture(),
      tokenFixture({
        id: 'tok-2',
        name: 'legacy-script',
        role: 'viewer',
        last_used_at: null,
        expires_at: NOW - 86_400_000,
      }),
    ],
    tokenCreates: [],
    tokenDeletes: [],
    auditQueries: [],
    auditPages: [
      [
        {
          id: 1,
          ts: NOW - 60_000,
          actor: 'admin',
          actor_role: 'admin',
          action: 'auth.login',
          resource_type: 'session',
          resource_id: 'sess-1',
          method: 'POST',
          path: '/api/v1/auth/login',
          status_code: 200,
          ip: '10.0.0.1',
          user_agent: 'Mozilla/5.0 smoke',
          detail: JSON.stringify({ username: 'admin' }),
        },
        {
          id: 2,
          ts: NOW - 120_000,
          actor: 'admin',
          actor_role: 'admin',
          action: 'server.create',
          resource_type: 'server',
          resource_id: 'srv-a',
          method: 'POST',
          path: '/api/v1/servers',
          status_code: 201,
          ip: '10.0.0.1',
          user_agent: 'curl/8.4',
          detail: null,
        },
        {
          id: 3,
          ts: NOW - 180_000,
          actor: 'admin',
          actor_role: 'admin',
          action: 'token.create',
          resource_type: 'api_token',
          resource_id: 'tok-1',
          method: 'POST',
          path: '/api/v1/api-tokens',
          status_code: 201,
          ip: '10.0.0.2',
          user_agent: 'curl/8.4',
          detail: JSON.stringify({ name: 'ci-deploy' }),
        },
      ],
      [
        {
          id: 4,
          ts: NOW - 240_000,
          actor: 'admin',
          actor_role: 'admin',
          action: 'backup.create',
          resource_type: 'backup',
          resource_id: backupName(),
          method: 'POST',
          path: '/api/v1/backups',
          status_code: 201,
          ip: '10.0.0.1',
          user_agent: 'curl/8.4',
          detail: null,
        },
        {
          id: 5,
          ts: NOW - 300_000,
          actor: 'admin',
          actor_role: 'admin',
          action: 'check.run',
          resource_type: 'server',
          resource_id: 'srv-a',
          method: 'POST',
          path: '/api/v1/checks',
          status_code: 200,
          ip: '10.0.0.1',
          user_agent: 'curl/8.4',
          detail: null,
        },
      ],
    ],
    backups: [backupFixture(), backupFixture({ name: 'leadstack-backup-20260118-120000.tar.gz.enc', size_bytes: 1_048_576, includes_artifacts: false, schema_version: 11, created_at: NOW - 93_600_000 })],
    backupCreates: [],
    backupDeletes: [],
    downloads: [],
    networkGets: 0,
    networkPuts: [],
    network: {
      listen: '127.0.0.1:8080',
      listen_source: 'settings',
      active_listen: '127.0.0.1:8080',
      public_url: 'https://panel.example.com',
      public_url_source: 'settings',
      default_ssh_port: 22,
      default_ssh_port_source: 'default',
      fallback_reason: null,
      restart_required: false,
    },
    passwordChanges: [],
    credentials: [
      {
        id: 'cred-1',
        name: 'prod-root',
        kind: 'password',
        username: 'root',
        fingerprint: 'SHA256:9f2c4b7e5a1d3c6f8b0e2a4c6d8f1b3e5a7c9d1f',
        created_at: NOW - 86_400_000,
        server_count: 2,
        panel_url: null,
        insecure_skip_verify: false,
      },
      {
        id: 'cred-2',
        name: 'bastion-key',
        kind: 'key',
        username: 'deploy',
        fingerprint: 'SHA256:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
        created_at: NOW - 172_800_000,
        server_count: 1,
        panel_url: null,
        insecure_skip_verify: false,
      },
    ],
    credentialCreates: [],
    credentialDeletes: [],
    groups: [
      serverGroupFixture(),
      serverGroupFixture({ id: 'grp-stage', name: '预发分组', description: '预发环境', member_count: 0 }),
    ],
    groupMembers: { 'grp-prod': ['srv-a', 'srv-b'], 'grp-stage': [] },
    groupCreates: [],
    groupPatches: [],
    groupDeletes: [],
    memberPuts: [],
    servers: [
      serverFixture('srv-a', 'alpha-node', '10.0.0.11', 22),
      serverFixture('srv-b', 'beta-node', '10.0.0.12', 22),
      serverFixture('srv-c', 'gamma-node', '10.0.0.13', 2222),
    ],
  }
}

/** 假后端：只覆盖这六页用到的端点，其余一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
    const noContent = () => route.fulfill({ status: 204, body: '' })
    const body = () => {
      try {
        return request.postDataJSON() ?? {}
      } catch {
        return {}
      }
    }

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: userFixture(state.permissions) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: userFixture(state.permissions) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return noContent()
    }

    /* ---------------- 令牌 ---------------- */
    if (path === '/api/v1/api-tokens' && method === 'GET') {
      return json(200, { tokens: state.tokens.map((token) => ({ ...token })) })
    }
    if (path === '/api/v1/api-tokens' && method === 'POST') {
      const payload = body()
      state.tokenCreates.push(payload)
      const created = tokenFixture({
        id: 'tok-new',
        name: String(payload.name ?? ''),
        role: String(payload.role ?? 'viewer'),
        created_at: NOW,
        last_used_at: null,
        expires_at: payload.expires_in ? NOW + 86_400_000 : null,
      })
      state.tokens.unshift(created)
      return json(201, { ...created, token: PLAINTEXT_TOKEN })
    }
    const tokenMatch = /^\/api\/v1\/api-tokens\/([^/]+)$/.exec(path)
    if (tokenMatch && method === 'DELETE') {
      const id = decodeURIComponent(tokenMatch[1])
      state.tokenDeletes.push(id)
      const target = state.tokens.find((token) => token.id === id)
      if (target) target.revoked_at = NOW
      return noContent()
    }

    /* ---------------- 审计 ---------------- */
    if (path === '/api/v1/audit-logs' && method === 'GET') {
      state.auditQueries.push(url.search)
      const cursor = url.searchParams.get('cursor')
      const page = cursor === 'c2' ? state.auditPages[1] : state.auditPages[0]
      return json(200, {
        data: page.map((entry) => ({ ...entry })),
        page: cursor === 'c2' ? { next_cursor: null, has_more: false } : { next_cursor: 'c2', has_more: true },
      })
    }

    /* ---------------- 备份 ---------------- */
    if (path === '/api/v1/backups' && method === 'GET') {
      return json(200, { backups: state.backups.map((backup) => ({ ...backup })) })
    }
    if (path === '/api/v1/backups' && method === 'POST') {
      const payload = body()
      state.backupCreates.push(payload)
      const created = backupFixture({
        name: 'leadstack-backup-20260120-090000.tar.gz.enc',
        size_bytes: 8_388_608,
        created_at: NOW,
        includes_artifacts: Boolean(payload.include_artifacts),
        schema_version: 12,
      })
      state.backups.unshift(created)
      return json(201, created)
    }
    const downloadMatch = /^\/api\/v1\/backups\/([^/]+)\/download$/.exec(path)
    if (downloadMatch && method === 'GET') {
      const name = decodeURIComponent(downloadMatch[1])
      state.downloads.push(name)
      return route.fulfill({
        status: 200,
        headers: {
          'content-type': 'application/octet-stream',
          'content-disposition': `attachment; filename="${name}"`,
        },
        body: 'leadstack-backup-bytes',
      })
    }
    const backupMatch = /^\/api\/v1\/backups\/([^/]+)$/.exec(path)
    if (backupMatch && method === 'DELETE') {
      const name = decodeURIComponent(backupMatch[1])
      state.backupDeletes.push(name)
      state.backups = state.backups.filter((backup) => backup.name !== name)
      return noContent()
    }

    /* ---------------- 系统 / 设置 ---------------- */
    if (path === '/api/v1/system/info' && method === 'GET') {
      return json(200, {
        version: '1.9.3',
        uptime_s: 7_320,
        server_count: 3,
        online_count: 2,
      })
    }
    if (path === '/api/v1/system/network' && method === 'GET') {
      state.networkGets += 1
      return json(200, { ...state.network })
    }
    if (path === '/api/v1/system/network' && method === 'PUT') {
      const payload = body()
      state.networkPuts.push(payload)
      if (payload.listen !== undefined) state.network.listen = String(payload.listen)
      if (payload.public_url !== undefined) state.network.public_url = String(payload.public_url)
      if (payload.default_ssh_port !== undefined) state.network.default_ssh_port = Number(payload.default_ssh_port)
      state.network.active_listen = state.network.listen
      return json(200, { ...state.network })
    }
    if (path === '/api/v1/users/me/password' && method === 'POST') {
      state.passwordChanges.push(body())
      return noContent()
    }

    /* ---------------- 凭据 ---------------- */
    if (path === '/api/v1/credentials' && method === 'GET') {
      // 契约：列表永不回显密文；这里连 password/private_key 字段都不返回。
      return json(200, state.credentials.map((credential) => ({ ...credential })))
    }
    if (path === '/api/v1/credentials' && method === 'POST') {
      const payload = body()
      state.credentialCreates.push(payload)
      const created = {
        id: 'cred-new',
        name: String(payload.name ?? ''),
        kind: String(payload.kind ?? 'password'),
        username: String(payload.username ?? ''),
        fingerprint: 'SHA256:latestfingerprint000000000000000000000000000',
        created_at: NOW,
        server_count: 0,
        panel_url: null,
        insecure_skip_verify: false,
      }
      state.credentials.push(created)
      return json(201, created)
    }
    const credentialMatch = /^\/api\/v1\/credentials\/([^/]+)$/.exec(path)
    if (credentialMatch && method === 'DELETE') {
      const id = decodeURIComponent(credentialMatch[1])
      state.credentialDeletes.push(id)
      state.credentials = state.credentials.filter((credential) => credential.id !== id)
      return noContent()
    }

    /* ---------------- 分组 ---------------- */
    if (path === '/api/v1/server-groups' && method === 'GET') {
      const groups = state.groups.map((group) => ({
        ...group,
        member_count: state.groupMembers[group.id]?.length ?? group.member_count,
      }))
      return json(200, { groups })
    }
    if (path === '/api/v1/server-groups' && method === 'POST') {
      const payload = body()
      state.groupCreates.push(payload)
      const created = serverGroupFixture({
        id: 'grp-new',
        name: String(payload.name ?? ''),
        description: String(payload.description ?? ''),
        member_count: 0,
        created_at: NOW,
        updated_at: NOW,
      })
      state.groups.push(created)
      state.groupMembers[created.id] = []
      return json(201, created)
    }
    const groupMatch = /^\/api\/v1\/server-groups\/([^/]+)$/.exec(path)
    if (groupMatch && method === 'PATCH') {
      const id = decodeURIComponent(groupMatch[1])
      const payload = body()
      state.groupPatches.push({ id, ...payload })
      const target = state.groups.find((group) => group.id === id)
      if (target) {
        if (payload.name !== undefined) target.name = String(payload.name)
        if (payload.description !== undefined) target.description = String(payload.description)
        target.updated_at = NOW
      }
      return json(200, target ? { ...target } : {})
    }
    if (groupMatch && method === 'DELETE') {
      const id = decodeURIComponent(groupMatch[1])
      state.groupDeletes.push(id)
      state.groups = state.groups.filter((group) => group.id !== id)
      delete state.groupMembers[id]
      return noContent()
    }
    const groupServersMatch = /^\/api\/v1\/server-groups\/([^/]+)\/servers$/.exec(path)
    if (groupServersMatch && method === 'GET') {
      const id = decodeURIComponent(groupServersMatch[1])
      const ids = state.groupMembers[id] ?? []
      return json(200, state.servers.filter((server) => ids.includes(server.id)).map((server) => ({ ...server })))
    }
    if (groupServersMatch && method === 'PUT') {
      const id = decodeURIComponent(groupServersMatch[1])
      const payload = body()
      const ids = Array.isArray(payload.server_ids) ? payload.server_ids.map(String) : []
      state.memberPuts.push({ id, serverIds: ids })
      state.groupMembers[id] = ids
      const target = state.groups.find((group) => group.id === id)
      if (target) target.member_count = ids.length
      return noContent()
    }

    /* ---------------- 服务器（成员选择用） ---------------- */
    if (path === '/api/v1/servers' && method === 'GET') {
      return json(200, state.servers.map((server) => ({ ...server })))
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
}

const TITLES = {
  '/tokens': 'API 令牌',
  '/audit': '审计日志',
  '/backups': '备份',
  '/settings': '设置',
  '/credentials': '凭证管理',
  '/groups': '服务器分组',
}

// 各页根类：懒加载 chunk 未到时旧页 DOM 仍在，标题相等是「已换页」的最强信号；
// 根类 + 分组行数作为第二重证据，防止标题恰好相同/页面仍是旧壳。
const PAGE_ROOT = {
  '/tokens': '.ls-tokenspage',
  '/audit': '.ls-auditpage',
  '/backups': '.ls-backupspage',
  '/settings': '.ls-settingspage',
  '/credentials': '.ls-credentialspage',
  '/groups': '.ls-groupspage',
}

async function gotoPage(page, base, hash, waitFor) {
  await page.goto(`${base}/#${hash}`, { waitUntil: 'domcontentloaded' })
  if (!waitFor) return
  if (typeof waitFor === 'string') {
    await page.waitForSelector(waitFor, { timeout: 15000 })
    return
  }
  // 标题等待：hash 导航是同文档切换，`.ls-adminpage__title` 每页都有，
  // 只等选择器会读到上一页；只有标题文本切到目标页才算真正换页。
  await page
    .waitForFunction(
      (want) => document.querySelector('.ls-adminpage__title')?.textContent?.trim() === want,
      waitFor.title,
      { timeout: 15000 },
    )
    .catch(() => {})
}

/**
 * 等路由真正换页再读标题：hash 切换是同文档导航，`.ls-adminpage__title`
 * 在所有管理页都存在，只等选择器会读到上一页的标题（曾出现 /groups 读到「凭证管理」）。
 * 超时也继续读一次，让断言给出真实标题而不是抛异常。
 */
async function readHeading(page, expected) {
  await page
    .waitForFunction(
      (want) => document.querySelector('.ls-adminpage__title')?.textContent?.trim() === want,
      expected,
      { timeout: 15000 },
    )
    .catch(() => {})
  return (await page.locator('.ls-adminpage__title').first().innerText()).trim()
}

function bodyText(page) {
  return page.evaluate(() => document.body.innerText)
}

async function run() {
  const { dist, ready } = resolveDist(WEB_ROOT, flag)
  if (!ready) {
    console.error(`✗ 没有构建产物：${resolve(dist, 'index.html')}\n  先跑：pnpm --dir web-next run build`)
    process.exitCode = 2
    return
  }

  let chromium
  try {
    chromium = await loadChromium()
  } catch (error) {
    console.error(`✗ ${error.message}`)
    process.exitCode = 2
    return
  }

  const { check, finish } = makeChecker()
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

  const state = makeState()
  const pageErrors = []

  try {
    const context = await browser.newContext({
      viewport: { width: 1280, height: 900 },
      acceptDownloads: true,
    })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)

    if (SHOTS) await page.screenshot({ path: resolve(SHOTS, '00-login.png'), fullPage: true }).catch(() => {})

    await login(page, base, { password: PASSWORD })

    /* ============ ① 六页渲染与标题 ============ */
    for (const [hash, title] of Object.entries(TITLES)) {
      // 通用类 `.ls-adminpage__title` 等不到换页（每页都有）；按标题文本等换页。
      await gotoPage(page, base, hash, { title })
      const heading = await readHeading(page, title)
      check(`${hash} 渲染且标题为「${title}」`, heading === title, heading)
    }

    /* ============ ② 令牌 ============ */
    await gotoPage(page, base, '/tokens', '[data-token-id="tok-1"]')
    check('令牌列表渲染两行', (await page.locator('.ls-tokenspage__row').count()) === 2)

    await page.click('#tokens-create-open')
    await page.waitForSelector('#tokens-create-form', { timeout: 5000 })
    await page.fill('#tokens-create-name', 'smoke-ci')
    await page.selectOption('#tokens-create-role', 'operator')
    await page.selectOption('#tokens-create-expiry', '720h')
    await page.click('#tokens-create-submit')
    await page.waitForSelector('#tokens-created-secret', { timeout: 8000 })

    const secretShown = (await page.locator('#tokens-created-secret').innerText()).trim()
    check('签发后明文出现在一次性弹窗', secretShown === PLAINTEXT_TOKEN, secretShown.slice(0, 12))
    check(
      '签发请求带 role 与 expires_in',
      state.tokenCreates.length === 1 &&
        state.tokenCreates[0].role === 'operator' &&
        state.tokenCreates[0].expires_in === '720h',
      JSON.stringify(state.tokenCreates[0]),
    )

    await page.click('#tokens-created-close')
    await page.waitForSelector('#tokens-created-secret', { state: 'detached', timeout: 5000 })
    check('关闭弹窗后明文不再出现在页面', !(await page.content()).includes(PLAINTEXT_TOKEN))

    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('[data-token-id="tok-new"]', { timeout: 15000 })
    const afterReload = await page.content()
    check('刷新页面后明文不可再读', !afterReload.includes(PLAINTEXT_TOKEN))
    check('刷新后新令牌仍在列表中', (await page.locator('.ls-tokenspage__row').count()) === 3)

    await page.click('[data-token-id="tok-2"] .ls-tokenspage__revoke')
    await page.waitForSelector('#tokens-revoke-confirm', { timeout: 5000 })
    await page.click('#tokens-revoke-confirm')
    await page.waitForSelector('#tokens-action-notice', { timeout: 8000 })
    check('吊销令牌发出 DELETE 且行变已撤销', state.tokenDeletes.join(',') === 'tok-2', state.tokenDeletes.join(','))
    check(
      '已撤销行显示撤销状态',
      (await page.locator('[data-token-id="tok-2"]').innerText()).includes('已撤销'),
    )

    /* ============ ③ 审计 ============ */
    await gotoPage(page, base, '/audit', '.ls-auditpage__row')
    check('审计列表渲染三行', (await page.locator('.ls-auditpage__row').count()) === 3)

    const auditButtons = (await page.locator('.ls-auditpage button').allInnerTexts()).map((text) => text.trim())
    const forbidden = auditButtons.filter((text) => /编辑|删除|保存|清空记录/.test(text))
    check('审计页没有编辑/删除按钮', forbidden.length === 0, forbidden.join('/'))
    check(
      '审计页只发 GET 请求',
      calls.filter((call) => call.path.startsWith('/api/v1/audit-logs')).every((call) => call.method === 'GET'),
    )

    await page.fill('#audit-actor', 'admin')
    await page.waitForTimeout(400)
    check(
      '审计筛选把 actor 带进查询串',
      state.auditQueries.at(-1)?.includes('actor=admin'),
      String(state.auditQueries.at(-1)),
    )
    await page.selectOption('#audit-action', 'auth.login')
    await page.waitForTimeout(400)
    check(
      '审计筛选把 action 带进查询串',
      state.auditQueries.at(-1)?.includes('action=auth.login'),
      String(state.auditQueries.at(-1)),
    )

    await page.click('#audit-clear-filters')
    await page.waitForTimeout(400)
    check('清除筛选后查询串不再带 actor', !(state.auditQueries.at(-1) ?? '').includes('actor='), String(state.auditQueries.at(-1)))

    await page.click('.ls-auditpage__expand')
    await page.waitForSelector('.ls-auditpage__detail', { timeout: 5000 })
    const detailText = await page.locator('.ls-auditpage__detail').first().innerText()
    check('展开详情显示 method/path 与请求 JSON', detailText.includes('/api/v1/auth/login') && detailText.includes('username'))

    await page.click('#audit-load-more')
    await page.waitForTimeout(500)
    check('cursor 分页后追加到五行', (await page.locator('.ls-auditpage__row').count()) === 5)
    check(
      '分页请求携带 cursor',
      state.auditQueries.some((query) => query.includes('cursor=c2')),
    )

    /* ============ ④ 备份 ============ */
    await gotoPage(page, base, '/backups', '.ls-backupspage__row')
    const firstBackupRow = await page.locator(`[data-backup-name="${backupName()}"]`).innerText()
    check(
      '备份行显示大小与 schema 版本',
      firstBackupRow.includes('MB') && firstBackupRow.includes('12'),
      firstBackupRow.replace(/\s+/g, ' ').slice(0, 80),
    )

    await page.check('#backups-include-artifacts')
    await page.click('#backups-create')
    await page.waitForSelector('[data-backup-name="leadstack-backup-20260120-090000.tar.gz.enc"]', { timeout: 8000 })
    check(
      '创建备份提交 include_artifacts=true',
      state.backupCreates.length === 1 && state.backupCreates[0].include_artifacts === true,
      JSON.stringify(state.backupCreates[0]),
    )
    check('创建后列表新增一行', (await page.locator('.ls-backupspage__row').count()) === 3)

    await page.click(`[data-backup-name="${backupName()}"] .ls-backupspage__download`)
    await page.waitForTimeout(400)
    check('下载按钮触发下载请求', state.downloads.length === 1 && state.downloads[0] === backupName(), state.downloads.join(','))

    await page.click(`[data-backup-name="${backupName()}"] .ls-backupspage__delete`)
    await page.waitForSelector('#backups-delete-confirm-submit', { timeout: 5000 })
    check('备份删除二次确认列出归档名', (await page.locator('.ls-modal__desc').innerText()).includes(backupName()))
    await page.click('#backups-delete-cancel')
    await page.waitForSelector('#backups-delete-confirm-submit', { state: 'detached', timeout: 5000 })
    check('取消删除不发 DELETE', state.backupDeletes.length === 0)

    await page.click(`[data-backup-name="${backupName()}"] .ls-backupspage__delete`)
    await page.waitForSelector('#backups-delete-confirm-submit', { timeout: 5000 })
    await page.click('#backups-delete-confirm-submit')
    await page.waitForSelector(`[data-backup-name="${backupName()}"]`, { state: 'detached', timeout: 8000 })
    check('确认删除发出 DELETE 且行消失', state.backupDeletes.join(',') === backupName(), state.backupDeletes.join(','))

    /* ============ ⑤ 设置 ============ */
    await gotoPage(page, base, '/settings', '#settings-version')
    check('设置页显示运行版本', (await page.locator('#settings-version').innerText()).includes('1.9.3'))

    await page.waitForSelector('#settings-network-form', { timeout: 8000 })
    check(
      '网络表单回填当前监听地址',
      (await page.inputValue('#settings-listen')) === '127.0.0.1:8080',
      await page.inputValue('#settings-listen'),
    )

    const getsBeforeSave = state.networkGets
    await page.fill('#settings-listen', '0.0.0.0:8090')
    await page.click('#settings-network-save')
    await page.waitForSelector('#settings-network-notice', { timeout: 8000 })
    check('保存网络发出 PUT 且只带变更字段', state.networkPuts.length === 1 && state.networkPuts[0].listen === '0.0.0.0:8090', JSON.stringify(state.networkPuts[0]))
    check('保存后重新 GET 读回服务端值', state.networkGets > getsBeforeSave, `${getsBeforeSave} → ${state.networkGets}`)
    check(
      '读回的生效监听地址是新值',
      (await page.locator('#settings-active-listen').innerText()).includes('0.0.0.0:8090'),
      await page.locator('#settings-active-listen').innerText(),
    )

    await page.fill('#settings-old-password', 'old-pass')
    await page.fill('#settings-new-password', 'another-pass')
    await page.fill('#settings-confirm-password', 'mismatch-pass')
    await page.click('#settings-password-submit')
    await page.waitForSelector('#settings-password-error', { timeout: 5000 })
    check('两次新密码不一致时不发请求', state.passwordChanges.length === 0)

    await page.fill('#settings-confirm-password', 'another-pass')
    await page.click('#settings-password-submit')
    await page.waitForSelector('#settings-password-notice', { timeout: 8000 })
    check(
      '密码一致时提交一次修改请求',
      state.passwordChanges.length === 1 && state.passwordChanges[0].new_password === 'another-pass',
      JSON.stringify(state.passwordChanges[0]),
    )

    /* ============ ⑥ 凭据 ============ */
    await gotoPage(page, base, '/credentials', '.ls-credentialspage__row')
    const credentialHtml = await page.content()
    check('凭据列表渲染两行', (await page.locator('.ls-credentialspage__row').count()) === 2)
    check(
      '凭据列表不含明文密码/私钥',
      !credentialHtml.includes(SEEDED_PASSWORD) && !credentialHtml.includes(SEEDED_KEY),
    )
    check(
      '凭据行只显示类型与指纹',
      (await page.locator('[data-credential-id="cred-1"]').innerText()).includes('密码') &&
        (await page.locator('#credentials-count').innerText()).includes('2'),
    )

    await page.click('#credentials-create-open')
    await page.waitForSelector('#credentials-create-form', { timeout: 5000 })
    await page.fill('#credentials-name', 'smoke-password')
    await page.fill('#credentials-username', 'root')
    await page.fill('#credentials-password', NEW_PASSWORD)
    await page.click('#credentials-create-submit')
    await page.waitForTimeout(500)
    check(
      '新建密码凭据的请求体含密码',
      state.credentialCreates.length === 1 &&
        state.credentialCreates[0].kind === 'password' &&
        state.credentialCreates[0].password === NEW_PASSWORD,
    )
    check('新建成功后创建弹窗自动关闭', (await page.locator('#credentials-create-form').count()) === 0)
    check('新建后弹窗关闭且明文未回显', !(await page.content()).includes(NEW_PASSWORD))
    check('新建后列表出现新凭证', (await page.locator('[data-credential-id="cred-new"]').count()) === 1)

    await page.click('#credentials-create-open')
    await page.waitForSelector('#credentials-create-form', { timeout: 5000 })
    await page.check('#credentials-kind-key')
    await page.waitForSelector('#credentials-key', { timeout: 5000 })
    await page.fill('#credentials-name', 'smoke-key')
    await page.fill('#credentials-username', 'deploy')
    await page.fill('#credentials-key', SEEDED_KEY)
    await page.click('#credentials-create-submit')
    await page.waitForTimeout(500)
    check(
      '新建私钥凭据的请求体含 private_key',
      state.credentialCreates.length === 2 &&
        state.credentialCreates[1].kind === 'key' &&
        state.credentialCreates[1].private_key === SEEDED_KEY,
    )
    check('私钥提交后仍未回显到页面', !(await page.content()).includes(SEEDED_KEY))

    await page.click('[data-credential-id="cred-2"] .ls-credentialspage__delete')
    await page.waitForSelector('#credentials-delete-confirm', { timeout: 5000 })
    await page.click('#credentials-delete-confirm')
    await page.waitForSelector('[data-credential-id="cred-2"]', { state: 'detached', timeout: 8000 })
    check('删除凭据发出 DELETE 且行消失', state.credentialDeletes.join(',') === 'cred-2', state.credentialDeletes.join(','))

    /* ============ ⑦ 分组 ============ */
    await gotoPage(page, base, '/groups', '.ls-groupspage__members')
    check('分组列表渲染两行', (await page.locator('.ls-groupspage__row').count()) === 2)

    const memberHeader = (await page.locator('.ls-adminpage__table thead th').nth(2).innerText()).trim()
    check('分组表头成员数列为静态文案（不含占位符）', !memberHeader.includes('{') && memberHeader === '服务器数', memberHeader)

    const memberCells = await page.locator('.ls-groupspage__row td:nth-child(3)').allInnerTexts()
    check(
      '分组行内成员数显示数字而非占位符',
      memberCells.length === 2 && memberCells.every((text) => /^\d+$/.test(text.trim())) && !memberCells.join('').includes('{'),
      memberCells.join(','),
    )
    const memberLabels = await page
      .locator('.ls-groupspage__row td:nth-child(3)')
      .evaluateAll((cells) => cells.map((cell) => cell.getAttribute('data-label')))
    check(
      '分组行内成员数标签不含占位符',
      memberLabels.length === 2 && memberLabels.every((label) => typeof label === 'string' && label.length > 0 && !label.includes('{')),
      memberLabels.join(','),
    )
    check('分组页全程未出现 {count} 字面量', !(await page.content()).includes('{count}'))

    await page.click('[data-group-id="grp-prod"] .ls-groupspage__members')
    await page.waitForSelector('#groups-members-list', { timeout: 8000 })
    check(
      '成员弹窗回填当前成员数',
      (await page.locator('#groups-members-count').innerText()).includes('2'),
      await page.locator('#groups-members-count').innerText(),
    )
    await page.check('[data-server-id="srv-c"]')
    await page.click('#groups-members-save')
    await page.waitForTimeout(500)
    const joined = state.memberPuts.at(-1)
    check(
      '加入成员后 PUT 整包包含新成员',
      joined?.id === 'grp-prod' && joined.serverIds.includes('srv-c') && joined.serverIds.length === 3,
      JSON.stringify(joined),
    )

    await page.click('[data-group-id="grp-prod"] .ls-groupspage__members')
    await page.waitForSelector('#groups-members-list', { timeout: 8000 })
    await page.uncheck('[data-server-id="srv-a"]')
    await page.click('#groups-members-save')
    await page.waitForTimeout(500)
    const removed = state.memberPuts.at(-1)
    check(
      '移除成员后 PUT 整包不含该成员',
      removed?.serverIds.includes('srv-a') === false && removed.serverIds.includes('srv-c'),
      JSON.stringify(removed),
    )

    await page.click('#groups-create-open')
    await page.waitForSelector('#groups-form-name', { timeout: 5000 })
    await page.fill('#groups-form-name', '冒烟分组')
    await page.fill('#groups-form-description', 'smoke 创建')
    await page.click('#groups-form-submit')
    await page.waitForSelector('[data-group-id="grp-new"]', { timeout: 8000 })
    check(
      '新建分组提交名称与描述',
      state.groupCreates.length === 1 &&
        state.groupCreates[0].name === '冒烟分组' &&
        state.groupCreates[0].description === 'smoke 创建',
      JSON.stringify(state.groupCreates[0]),
    )

    await page.click('[data-group-id="grp-new"] .ls-groupspage__edit')
    await page.waitForSelector('#groups-form-name', { timeout: 5000 })
    await page.fill('#groups-form-name', '冒烟分组-改名')
    await page.click('#groups-form-submit')
    await page.waitForTimeout(500)
    check(
      '编辑分组发出 PATCH 且改名生效',
      state.groupPatches.length === 1 &&
        state.groupPatches[0].id === 'grp-new' &&
        state.groupPatches[0].name === '冒烟分组-改名',
      JSON.stringify(state.groupPatches[0]),
    )

    await page.click('[data-group-id="grp-new"] .ls-groupspage__delete')
    await page.waitForSelector('#groups-delete-confirm', { timeout: 5000 })
    await page.click('#groups-delete-cancel')
    await page.waitForSelector('#groups-delete-confirm', { state: 'detached', timeout: 5000 })
    check('取消删除分组不发 DELETE', state.groupDeletes.length === 0)

    await page.click('[data-group-id="grp-new"] .ls-groupspage__delete')
    await page.waitForSelector('#groups-delete-confirm', { timeout: 5000 })
    await page.click('#groups-delete-confirm')
    await page.waitForSelector('[data-group-id="grp-new"]', { state: 'detached', timeout: 8000 })
    check('确认删除分组发出 DELETE 且行消失', state.groupDeletes.join(',') === 'grp-new', state.groupDeletes.join(','))

    /* ============ ⑧ 320px 窄宽 + 异常 ============ */
    await page.setViewportSize({ width: 320, height: 800 })
    const overflow = {}
    const narrowPages = []
    for (const hash of Object.keys(TITLES)) {
      // 只等选择器会在懒加载 chunk 未到时读到旧页（`.ls-adminpage__title` 每页都有），
      // 必须等标题文本切到目标页，再测 scrollWidth，避免假绿。
      await gotoPage(page, base, hash, { title: TITLES[hash] })
      const heading = await readHeading(page, TITLES[hash])
      // 目标页根类（分组再加行数）就绪才量宽度，否则量到的可能是上一页的 DOM。
      const rootReady = await page
        .waitForFunction(
          (selector) => {
            const root = document.querySelector(selector)
            if (!root) return false
            return selector !== '.ls-groupspage' || root.querySelectorAll('.ls-groupspage__row').length > 0
          },
          PAGE_ROOT[hash],
          { timeout: 15000 },
        )
        .then(() => true)
        .catch(() => false)
      narrowPages.push({ hash, heading, rootReady })
      await page.waitForTimeout(250)
      overflow[hash] = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    }
    const narrowWrong = narrowPages.filter((entry) => entry.heading !== TITLES[entry.hash] || !entry.rootReady)
    check(
      '320px 循环均停留在目标页（标题匹配且根类存在，groups 行数 >0）',
      narrowWrong.length === 0,
      JSON.stringify(narrowWrong),
    )
    const overflowing = Object.entries(overflow).filter(([, value]) => value > 0)
    check(
      '六页在 320px 下均无横向滚动',
      overflowing.length === 0,
      overflowing.map(([hash, value]) => `${hash} 溢出 ${value}px`).join(' / '),
    )

    const hits = await page.evaluate(() => {
      const selectors = ['#groups-create-open', '.ls-groupspage__edit', '.ls-groupspage__delete', '.ls-groupspage__members']
      return selectors.map((selector) => {
        const element = document.querySelector(selector)
        if (!element) return { selector, ok: false }
        const rect = element.getBoundingClientRect()
        return { selector, ok: rect.width > 0 && rect.height >= 24 && rect.right <= window.innerWidth + 1 }
      })
    })
    check('320px 下分组页按钮可见且命中区 ≥24px', hits.every((hit) => hit.ok), JSON.stringify(hits.filter((hit) => !hit.ok)))

    check('全程无未捕获异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))

    if (SHOTS) {
      await page.setViewportSize({ width: 320, height: 900 })
      await gotoPage(page, base, '/tokens')
      await readHeading(page, TITLES['/tokens'])
      await page.screenshot({ path: resolve(SHOTS, '01-tokens-320.png'), fullPage: true }).catch(() => {})
    }
  } catch (error) {
    check('冒烟执行未抛异常', false, error && error.message ? error.message : String(error))
  } finally {
    await browser.close().catch(() => {})
    await new Promise((done) => server.close(done))
  }

  const code = finish()
  process.exitCode = code
}

run().catch((error) => {
  console.error(error && error.stack ? error.stack : error)
  process.exitCode = 1
})
