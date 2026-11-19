#!/usr/bin/env node
// 控制台界面截图采集脚本（README / 文档用）—— web-next 版
//
// 自给自足：自己起一个临时控制端（空闲端口从 18090 起）与 DEV-ONLY SSH 端点，
// 通过 REST 造一套演示数据（2 台在线服务器 + 1 台离线、2 个应用、几条发布、
// 五类检测 + 告警、用户与自定义角色），然后登录 web-next 界面逐页截图。
//
// 用法：
//   make build          # 需要 bin/leadstack、bin/leadstack-devssh
//   node scripts/capture-screenshots.mjs
//
// 依赖：全局安装的 playwright（`npm i -g playwright` + `npx playwright install chromium`）。
//
// 环境变量：
//   OUT_DIR     输出目录（默认 docs/images）
//   SHOT_WIDTH  输出图宽（默认 1600，用 macOS sips 等比缩放；设为 0 则保留 3360）
//   KEEP_TMP    1 = 保留临时数据目录（排障用）
//
// 说明：web-next 目前还没有应用内主题切换，dashboard-dark.png 由本脚本在页面里
// 注入深色 token（Vean 的 .dark 变量 + --ls-* 覆盖）后拍摄，用于文档对照；
// 这不代表控制台里已经可以切到深色。

import { spawn, execFileSync, execSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createWriteStream, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createServer as createNetServer } from 'node:net'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const require = createRequire(import.meta.url)
const globalRoot = execSync('npm root -g').toString().trim()
const { chromium } = require(join(globalRoot, 'playwright'))

const ROOT = resolve(import.meta.dirname, '..')
const OUT = resolve(process.env.OUT_DIR || join(ROOT, 'docs/images'))
const SHOT_WIDTH = Number(process.env.SHOT_WIDTH ?? 1600)
const KEEP_TMP = process.env.KEEP_TMP === '1'
const ADMIN_USER = 'admin'
const ADMIN_PASS = 'demo-pass-18090'
const VIEWPORT = { width: 1680, height: 1000 }
const DPR = 2

mkdirSync(OUT, { recursive: true })

/* ------------------------------------------------------------------ */
/* 进程与端口                                                          */
/* ------------------------------------------------------------------ */

function canListen(port) {
  return new Promise((res) => {
    const s = createNetServer()
    s.once('error', () => res(false))
    s.once('listening', () => s.close(() => res(true)))
    s.listen(port, '127.0.0.1')
  })
}

async function pickPort(start) {
  for (let port = start; port < start + 80; port += 1) {
    if (await canListen(port)) return port
  }
  throw new Error(`从 ${start} 起找不到空闲端口`)
}

const children = []
function spawnBin(args, env = {}, logPath) {
  const child = spawn(args[0], args.slice(1), {
    cwd: ROOT,
    env: { ...process.env, ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const log = logPath ? createWriteStream(logPath, { flags: 'a' }) : null
  child.stdout.on('data', (d) => log?.write(d))
  child.stderr.on('data', (d) => log?.write(d))
  child.on('exit', (code) => {
    if (code && code !== 0) console.error(`[${args[0]}] 退出码 ${code}`)
  })
  children.push(child)
  return child
}

function killChildren() {
  for (const child of children) {
    try {
      child.kill('SIGTERM')
    } catch {
      /* ignore */
    }
  }
}

async function waitFor(fn, { timeout = 15000, interval = 200, label = '条件' } = {}) {
  const deadline = Date.now() + timeout
  let lastError
  while (Date.now() < deadline) {
    try {
      const value = await fn()
      if (value) return value
    } catch (err) {
      lastError = err
    }
    await new Promise((r) => setTimeout(r, interval))
  }
  throw new Error(`等待超时：${label}${lastError ? `（${lastError.message}）` : ''}`)
}

/* ------------------------------------------------------------------ */
/* 演示数据                                                            */
/* ------------------------------------------------------------------ */

function sha256of(text) {
  return createHash('sha256').update(text).digest('hex')
}

async function main() {
  const bin = join(ROOT, 'bin/leadstack')
  const devsshBin = join(ROOT, 'bin/leadstack-devssh')
  const tmp = mkdtempSync(join(tmpdir(), 'leadstack-shots-'))
  mkdirSync(join(tmp, 'deploy'), { recursive: true })
  const PORT = await pickPort(18090)
  const BASE = `http://127.0.0.1:${PORT}`

  let artClosed = false
  const closeArtifact = () => {
    if (!artClosed) {
      artClosed = true
      artifactServer.close()
    }
  }
  const demoBody = `leadstack demo artifact ${Date.now()}\n`
  const artifactServer = createServer((req, res) => {
    if (req.url === '/app.txt') {
      res.writeHead(200, { 'Content-Type': 'text/plain' })
      res.end(demoBody)
      return
    }
    res.writeHead(404)
    res.end('not found')
  })
  await new Promise((r) => artifactServer.listen(0, '127.0.0.1', r))
  const ART_PORT = artifactServer.address().port
  const ART_URL = `http://127.0.0.1:${ART_PORT}/app.txt`

  process.on('SIGINT', () => {
    killChildren()
    closeArtifact()
    process.exit(130)
  })

  let token = ''
  async function api(method, path, body) {
    const res = await fetch(BASE + path, {
      method,
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const payload = await res.json().catch(() => ({}))
    if (!res.ok) {
      throw new Error(`${method} ${path} -> ${res.status} ${JSON.stringify(payload).slice(0, 300)}`)
    }
    return payload.data
  }

  // ---- 起 DEV-ONLY SSH 端点 ----
  const sshPortFile = join(tmp, 'ssh.port')
  spawnBin(
    [devsshBin, '--addr', '127.0.0.1:0', '--user', 'devuser', '--password', 'devpass', '--port-file', sshPortFile],
    {},
    join(tmp, 'devssh.log'),
  )
  const sshPort = await waitFor(
    () => {
      try {
        const value = readFileSync(sshPortFile, 'utf8').trim()
        return value ? Number(value) : null
      } catch {
        return null
      }
    },
    { label: 'devssh 端口文件', timeout: 20000 },
  )

  // ---- 起控制端 ----
  spawnBin(
    [bin],
    {
      LEADSTACK_LISTEN: `127.0.0.1:${PORT}`,
      LEADSTACK_DATA_DIR: tmp,
      LEADSTACK_ADMIN_PASSWORD: ADMIN_PASS,
      LEADSTACK_DEPLOY_ROOT: join(tmp, 'deploy'),
      LEADSTACK_SSH_INTERVAL: '3s',
      LEADSTACK_ALERT_EVAL_INTERVAL: '3s',
    },
    join(tmp, 'leadstack.log'),
  )
  await waitFor(
    async () => {
      const res = await fetch(`${BASE}/healthz`).catch(() => null)
      return res?.ok
    },
    { label: '控制端 /healthz', timeout: 30000 },
  )
  console.log(`控制端已就绪：${BASE}（临时目录 ${tmp}）`)

  try {
    const login = await api('POST', '/api/v1/auth/login', { username: ADMIN_USER, password: ADMIN_PASS })
    token = login.token

    /* ---------------- 先把告警规则建好，状态翻转时才会建告警 ---------------- */
    await api('POST', '/api/v1/alert-rules', {
      name: '检测项连续失败',
      enabled: true,
      source: 'check',
      severity: 'critical',
    })
    await api('POST', '/api/v1/alert-rules', {
      name: '服务器离线',
      enabled: true,
      source: 'server',
      severity: 'warning',
    })

    /* ---------------- 凭证 / 分组 / 服务器 ---------------- */
    const cred = await api('POST', '/api/v1/credentials', {
      name: '演示 SSH 凭证',
      kind: 'password',
      username: 'devuser',
      password: 'devpass',
    })
    const prodGroup = await api('POST', '/api/v1/server-groups', {
      name: '生产环境',
      description: '线上业务服务器',
    })
    const stagingGroup = await api('POST', '/api/v1/server-groups', {
      name: '预发布环境',
      description: '上线前验证与压测',
    })

    const makeServer = (payload) => api('POST', '/api/v1/servers', payload)
    const web = await makeServer({
      name: 'prod-web-01',
      host: '127.0.0.1',
      port: sshPort,
      mode: 'ssh',
      credential_id: cred.id,
      group: '生产环境',
      tags: ['prod', 'web'],
    })
    const db = await makeServer({
      name: 'prod-db-01',
      host: '127.0.0.1',
      port: sshPort,
      mode: 'ssh',
      credential_id: cred.id,
      group: '生产环境',
      tags: ['prod', 'db'],
    })
    const staging = await makeServer({
      name: 'staging-web-02',
      host: '127.0.0.1',
      port: 1,
      mode: 'ssh',
      credential_id: cred.id,
      group: '预发布环境',
      tags: ['staging'],
    })
    await api('PUT', `/api/v1/server-groups/${prodGroup.id}/servers`, { server_ids: [web.id, db.id] })
    await api('PUT', `/api/v1/server-groups/${stagingGroup.id}/servers`, { server_ids: [staging.id] })

    for (const server of [web, db]) {
      await api('POST', `/api/v1/servers/${server.id}/test-connection`)
      await api('POST', `/api/v1/servers/${server.id}/refresh-host-info`)
    }
    await waitFor(
      async () => {
        const servers = await api('GET', '/api/v1/servers')
        const target = servers.find((s) => s.id === web.id)
        return target?.metrics_summary && Object.keys(target.metrics_summary).length > 0
      },
      { label: 'prod-web-01 指标入库', timeout: 45000 },
    )
    await waitFor(
      async () => {
        const servers = await api('GET', '/api/v1/servers')
        return servers.find((s) => s.id === staging.id)?.status === 'offline'
      },
      { label: 'staging-web-02 变为离线', timeout: 45000 },
    )

    /* ---------------- 应用 / 模板 / 制品 / 发布 ---------------- */
    const portal = await api('POST', '/api/v1/apps', {
      name: '官网前端',
      slug: 'web-portal',
      description: '面向访客的官网与登录入口',
    })
    const apiApp = await api('POST', '/api/v1/apps', {
      name: '订单服务',
      slug: 'order-api',
      description: '订单创建、查询与对账',
    })
    const template = (name) =>
      api('POST', '/api/v1/templates', {
        name,
        kind: 'binary',
        content: [
          '#!/usr/bin/env bash',
          'set -eu',
          'echo "release {{VERSION}} -> {{RELEASE_DIR}}"',
          'echo "artifact {{ARTIFACT_SHA256}}"',
        ].join('\n'),
      })
    const portalTpl = await template('web-portal-binary')
    const apiTpl = await template('order-api-binary')
    const artifact = () =>
      api('POST', '/api/v1/artifacts', {
        kind: 'url',
        url: ART_URL,
        sha256: sha256of(demoBody),
        size_bytes: Buffer.byteLength(demoBody),
      })
    const artifactA = await artifact()
    const artifactB = await artifact()
    const badArtifact = await api('POST', '/api/v1/artifacts', {
      kind: 'url',
      url: `http://127.0.0.1:${ART_PORT}/missing.txt`,
      sha256: sha256of(demoBody),
      size_bytes: Buffer.byteLength(demoBody),
    })

    // 每条发布只落一台服务器：两台演示服务器共用同一个 devssh 进程（同一文件系统），
    // 同一 release 在两台上并发部署会共用同一个 release 目录而互相删掉下载的制品。
    const releases = [
      { app: portal, tpl: portalTpl, art: artifactA, version: '1.4.2', servers: [web.id] },
      { app: portal, tpl: portalTpl, art: artifactB, version: '1.4.3', servers: [web.id] },
      { app: apiApp, tpl: apiTpl, art: artifactA, version: '2.0.0', servers: [db.id] },
      { app: apiApp, tpl: apiTpl, art: badArtifact, version: '2.1.0-rc.1', servers: [db.id] },
    ]
    const releaseIds = []
    for (const item of releases) {
      const created = await api('POST', '/api/v1/releases', {
        app_id: item.app.id,
        template_id: item.tpl.id,
        artifact_id: item.art.id,
        version: item.version,
        server_ids: item.servers,
      })
      releaseIds.push(created.release.id)
    }
    for (const id of releaseIds) {
      await waitFor(
        async () => {
          const detail = await api('GET', `/api/v1/releases/${id}`)
          const status = detail?.release?.status ?? detail?.status
          return ['success', 'failed'].includes(status) ? status : null
        },
        { label: `发布 ${id.slice(0, 8)} 结束`, timeout: 120000, interval: 500 },
      )
    }

    /* ---------------- 检测项（五类 + 两个会翻转的） ---------------- */
    const createCheck = (payload) => api('POST', '/api/v1/checks', payload)
    const runCheck = (id) => api('POST', `/api/v1/checks/${id}/run`)
    const upChecks = [
      {
        name: 'SSH 端口连通性',
        kind: 'tcp',
        target: `127.0.0.1:${sshPort}`,
        interval_s: 60,
        timeout_s: 3,
        params: { fail_threshold: 3 },
        server_ids: [web.id],
      },
      {
        name: '官网健康检查',
        kind: 'http',
        target: `${BASE}/healthz`,
        interval_s: 60,
        timeout_s: 5,
        params: { fail_threshold: 3, expect_status: 200 },
        server_ids: [web.id],
      },
      {
        name: '控制台端口探测',
        kind: 'tcp',
        target: `127.0.0.1:${PORT}`,
        interval_s: 60,
        timeout_s: 3,
        params: { fail_threshold: 3 },
        server_ids: [db.id],
      },
      {
        name: '控制端进程存活',
        kind: 'process',
        target: 'leadstack',
        interval_s: 60,
        timeout_s: 10,
        params: { fail_threshold: 3, min_count: 1 },
        server_ids: [web.id],
      },
      {
        name: '系统信息采集',
        kind: 'script',
        target: 'uname -s',
        interval_s: 60,
        timeout_s: 10,
        params: { fail_threshold: 3, expect_stdout: 'Darwin' },
        server_ids: [db.id],
      },
    ]
    const upIds = []
    for (const payload of upChecks) {
      const check = await createCheck(payload)
      upIds.push(check.id)
      for (let i = 0; i < 2; i += 1) await runCheck(check.id)
    }
    const downCheck = await createCheck({
      name: '支付网关端口',
      kind: 'tcp',
      target: '127.0.0.1:1',
      interval_s: 60,
      timeout_s: 3,
      params: { fail_threshold: 1 },
      server_ids: [db.id],
    })
    for (let i = 0; i < 3; i += 1) await runCheck(downCheck.id)
    await waitFor(
      async () => {
        const checks = await api('GET', '/api/v1/checks')
        const wanted = upIds.map((id) => checks.find((c) => c.id === id)).filter(Boolean)
        return wanted.length === upIds.length && wanted.every((c) => c.status === 'up')
      },
      { label: '五个检测项状态为 up', timeout: 45000 },
    )

    /* ---------------- 翻转两个检测项 → 产生真实告警 ---------------- */
    const flipTcp = await createCheck({
      name: '官网入口端口',
      kind: 'tcp',
      target: `127.0.0.1:${ART_PORT}`,
      interval_s: 10,
      timeout_s: 2,
      params: { fail_threshold: 2 },
      server_ids: [web.id],
    })
    const flipHttp = await createCheck({
      name: '官网入口探测',
      kind: 'http',
      target: ART_URL,
      interval_s: 10,
      timeout_s: 2,
      params: { fail_threshold: 2, expect_status: 200 },
      server_ids: [db.id],
    })
    for (const id of [flipTcp.id, flipHttp.id]) await runCheck(id)
    await waitFor(
      async () => {
        const checks = await api('GET', '/api/v1/checks')
        return [flipTcp.id, flipHttp.id].every((id) => checks.find((c) => c.id === id)?.status === 'up')
      },
      { label: '翻转检测项先进入 up', timeout: 45000 },
    )
    await new Promise((r) => setTimeout(r, 1200))
    await new Promise((r) => {
      artifactServer.close(r)
      artClosed = true
    })

    const alerts = await waitFor(
      async () => {
        const list = await api('GET', '/api/v1/alerts?limit=20')
        return list.length >= 2 ? list : null
      },
      { label: '两个告警已生成', timeout: 60000, interval: 500 },
    )
    // 确认一条，留下 firing / acked 两种状态给告警页。
    await api('POST', `/api/v1/alerts/${alerts[0].id}/ack`)
    // 翻转检测项只为造告警；删掉它们，检测页只剩稳定状态的数据（告警行保留快照）。
    for (const id of [flipTcp.id, flipHttp.id]) await api('DELETE', `/api/v1/checks/${id}`)
    await waitFor(
      async () => {
        const checks = await api('GET', '/api/v1/checks')
        const list = await api('GET', '/api/v1/alerts?limit=20')
        return checks.length === 6 && list.length >= 2
      },
      { label: '检测项收敛为 6 项且告警保留', timeout: 30000, interval: 500 },
    )

    /* ---------------- 用户 / 角色 ---------------- */
    await api('POST', '/api/v1/users', { username: 'ops-zhang', password: 'demo-pass-1', role: 'operator' })
    await api('POST', '/api/v1/users', { username: 'viewer-li', password: 'demo-pass-2', role: 'viewer' })
    await api('POST', '/api/v1/users', { username: 'audit-wang', password: 'demo-pass-3', role: 'viewer' })
    for (let i = 0; i < 5; i += 1) {
      await fetch(`${BASE}/api/v1/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: 'audit-wang', password: 'wrong-pass' }),
      })
    }
    await waitFor(
      async () => {
        const users = await api('GET', '/api/v1/users')
        return users.find((u) => u.username === 'audit-wang')?.locked === true
      },
      { label: 'audit-wang 被锁定', timeout: 20000 },
    )
    const roles = await api('GET', '/api/v1/roles')
    const operator = roles.find((r) => r.key === 'operator')
    await api('POST', '/api/v1/roles', {
      name: '运维值班',
      description: '仅限生产环境分组的日常巡检与处置',
      permissions: operator.permissions.slice(0, 8),
      scope: { mode: 'groups', group_ids: [prodGroup.id], server_ids: [] },
    })

    /* ---------------- 浏览器 ---------------- */
    const browser = await chromium.launch()
    const context = await browser.newContext({
      viewport: VIEWPORT,
      deviceScaleFactor: DPR,
      locale: 'zh-CN',
      timezoneId: 'Asia/Shanghai',
      reducedMotion: 'reduce',
    })
    await context.addInitScript(() => {
      window.localStorage.setItem('leadstack.lang', 'zh-CN')
    })
    const page = await context.newPage()
    page.on('pageerror', (err) => console.error('[pageerror]', String(err).slice(0, 200)))

    await page.goto(`${BASE}/#/login`, { waitUntil: 'domcontentloaded' })
    await waitFor(() => page.evaluate(() => Boolean(document.querySelector('#login-submit'))), {
      label: '登录表单',
      timeout: 20000,
    })
    await page.fill('#login-username', ADMIN_USER)
    await page.fill('#login-password', ADMIN_PASS)
    await page.click('#login-submit')

    async function waitTitle(title) {
      await page.waitForFunction(
        (expected) => {
          const headings = Array.from(document.querySelectorAll('h1'))
          return headings.some((el) => (el.textContent || '').trim().includes(expected))
        },
        title,
        { timeout: 25000, polling: 200 },
      )
      await page.waitForLoadState('networkidle').catch(() => {})
      await page.waitForFunction(() => !document.querySelector('.ls-skeleton'), null, {
        timeout: 15000,
        polling: 200,
      })
      await page.waitForTimeout(500)
    }

    async function inspect(buffer) {
      const probe = await context.newPage()
      try {
        return await probe.evaluate(async (b64) => {
          const img = new Image()
          img.src = `data:image/png;base64,${b64}`
          await img.decode()
          const canvas = document.createElement('canvas')
          canvas.width = img.width
          canvas.height = img.height
          const ctx = canvas.getContext('2d')
          ctx.drawImage(img, 0, 0)
          const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data
          let nonWhite = 0
          const total = data.length / 4
          for (let i = 0; i < data.length; i += 4) {
            if (!(data[i] > 250 && data[i + 1] > 250 && data[i + 2] > 250)) nonWhite += 1
          }
          return { width: img.width, height: img.height, nonWhiteRatio: nonWhite / total }
        }, buffer.toString('base64'))
      } finally {
        await probe.close()
      }
    }

    const failures = []
    async function shot(file, path, title, prepare) {
      await page.goto(BASE + path, { waitUntil: 'domcontentloaded' })
      await waitTitle(title)
      if (prepare) {
        await prepare()
        await page.waitForTimeout(400)
      }
      const shell = await page.evaluate(() => Boolean(document.querySelector('.ls-shell')))
      if (!shell) failures.push(`${file}: 未渲染出 .ls-shell`)
      const buffer = await page.screenshot({ fullPage: true, type: 'png' })
      const info = await inspect(buffer)
      const cssHeight = info.height / DPR
      const problems = []
      if (cssHeight <= 600) problems.push(`高度 ${cssHeight.toFixed(0)}px <= 600px`)
      if (info.nonWhiteRatio < 0.01) problems.push(`非白像素占比 ${(info.nonWhiteRatio * 100).toFixed(2)}% < 1%`)
      if (problems.length > 0) {
        failures.push(`${file}: ${problems.join('；')}`)
      }
      const target = join(OUT, file)
      writeFileSync(target, buffer)
      if (SHOT_WIDTH > 0) {
        try {
          execFileSync('sips', ['--resampleWidth', String(SHOT_WIDTH), target], { stdio: 'ignore' })
        } catch {
          /* 非 macOS 环境保留原始宽度 */
        }
      }
      console.log(
        `✓ ${file}  ${title}  截图 ${info.width}x${info.height} → ${SHOT_WIDTH > 0 ? `${SHOT_WIDTH}px 宽` : '原宽'}  非白 ${(info.nonWhiteRatio * 100).toFixed(1)}%`,
      )
    }

    await shot('dashboard.png', '/#/', '总览')
    await shot('checks.png', '/#/checks', '检测')
    await shot('alerts.png', '/#/alerts', '告警')
    await shot('roles.png', '/#/roles', '角色与权限')
    await shot('groups.png', '/#/groups', '服务器分组')
    await shot('users.png', '/#/users', '用户管理')
    await shot('server-detail.png', `/#/servers/${web.id}`, web.name)

    // 深色总览（文档配图，见文件头的说明）
    await shot('dashboard-dark.png', '/#/', '总览', async () => {
      await page.addStyleTag({
        content: `
          html.dark {
            color-scheme: dark;
            --ls-bg: #0b1220;
            --ls-surface: #111827;
            --ls-surface-muted: #1a2333;
            --ls-border: #253041;
            --ls-border-strong: #3a4759;
            --ls-text: #e6edf7;
            --ls-text-muted: #a7b4c6;
            --ls-text-subtle: #8896aa;
            --ls-mask: rgb(2 6 23 / 0.62);
            --ls-success: #4ade80;
            --ls-success-soft: #052e16;
            --ls-warning: #fbbf24;
            --ls-warning-soft: #3b2a08;
            --ls-danger: #f87171;
            --ls-danger-soft: #3b1113;
            --ls-danger-border: #5c1f1f;
            --ls-primary: #4ade80;
            --ls-primary-fill: #22c55e;
            --ls-primary-contrast: #052e16;
            --ls-primary-soft: #0f2a1c;
          }
        `,
      })
      await page.evaluate(() => document.documentElement.classList.add('dark'))
    })

    await browser.close()

    if (failures.length > 0) {
      console.error(`\n有 ${failures.length} 张截图未通过断言：`)
      for (const failure of failures) console.error(`  ✗ ${failure}`)
      process.exitCode = 1
    } else {
      console.log(`\n完成：${OUT}/ 共 8 张（1680 视口 / ${DPR}x DPR）`)
    }
  } finally {
    killChildren()
    closeArtifact()
    if (!KEEP_TMP) {
      try {
        rmSync(tmp, { recursive: true, force: true })
      } catch {
        /* ignore */
      }
    } else {
      console.log(`临时目录保留：${tmp}`)
    }
  }
}

main().catch((err) => {
  console.error(`\n截图失败：${err.message}`)
  if (process.env.DEBUG) console.error(err.stack)
  killChildren()
  process.exit(1)
})
