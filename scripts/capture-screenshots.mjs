#!/usr/bin/env node
// 控制台界面截图采集脚本（README / 文档用）
//
// 用途：登录真实控制端，逐页截图到 docs/images/。
// 依赖：全局安装的 playwright（`npm i -g playwright` + `npx playwright install chromium`）。
//
// 用法：
//   # 1) 先起一个带演示数据的控制端（示例）
//   LEADSTACK_LISTEN=127.0.0.1:18090 LEADSTACK_DATA_DIR=/tmp/leadstack-demo \
//   LEADSTACK_ADMIN_PASSWORD=demo-pass ./bin/leadstack &
//   # 2) 采集
//   node scripts/capture-screenshots.mjs
//
// 环境变量：
//   BASE        控制端地址（默认 http://127.0.0.1:18090）
//   ADMIN_USER  管理员用户名（默认 admin）
//   ADMIN_PASS  管理员密码（必填，或用 DEMO_PASS）
//   OUT_DIR     输出目录（默认 docs/images）
//   DPR         设备像素比（默认 2，出图后建议缩到 1600px 宽）

import { createRequire } from 'node:module'
import { execSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

const require = createRequire(import.meta.url)
const globalRoot = execSync('npm root -g').toString().trim()
const { chromium } = require(join(globalRoot, 'playwright'))

const BASE = process.env.BASE || 'http://127.0.0.1:18090'
const USER = process.env.ADMIN_USER || 'admin'
const PASS = process.env.ADMIN_PASS || process.env.DEMO_PASS || ''
const OUT = process.env.OUT_DIR || 'docs/images'
const DPR = Number(process.env.DPR || 2)

if (!PASS) {
  console.error('缺少管理员密码：请设置 ADMIN_PASS（或 DEMO_PASS）')
  process.exit(1)
}
mkdirSync(OUT, { recursive: true })

// ---------- 1. 取登录令牌与演示对象 ----------
async function api(path, init = {}) {
  const res = await fetch(BASE + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init.headers || {}) },
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(`${path} -> ${res.status} ${JSON.stringify(body).slice(0, 200)}`)
  return body.data
}

const login = await api('/api/v1/auth/login', {
  method: 'POST',
  body: JSON.stringify({ username: USER, password: PASS }),
})
const token = login.token
const servers = await api('/api/v1/servers', { headers: { Authorization: `Bearer ${token}` } })
const featured = servers.find((s) => s.name === 'prod-web-01') || servers[0]
if (!featured) throw new Error('演示数据为空：请先准备服务器')
console.log(`登录成功，服务器 ${servers.length} 台，详情页用 ${featured.name}`)

// ---------- 2. 浏览器 ----------
const browser = await chromium.launch()
const context = await browser.newContext({
  viewport: { width: 1680, height: 1000 },
  deviceScaleFactor: DPR,
  locale: 'zh-CN',
  timezoneId: 'Asia/Shanghai',
  reducedMotion: 'reduce',
})
await context.addInitScript(
  ([t]) => {
    window.localStorage.setItem('leadstack_token', t)
    window.localStorage.setItem('leadstack.lang', 'zh-CN')
    window.localStorage.setItem('leadstack.accent', 'indigo')
    window.localStorage.setItem('leadstack.fontSize', 'normal')
    window.localStorage.setItem('leadstack.density', 'comfortable')
    if (!window.localStorage.getItem('leadstack.theme')) {
      window.localStorage.setItem('leadstack.theme', 'light')
    }
  },
  [token],
)
const page = await context.newPage()
page.on('pageerror', (err) => console.error('[pageerror]', String(err).slice(0, 200)))

// ---------- 3. 逐页截图 ----------
const shots = [
  // 前端使用 HashRouter：深链接形如 /#/roles（直接访问 /roles 会落到首页）。
  { file: 'dashboard.png', path: '/#/', title: '总览：服务器状态、检测与告警概览', fullPage: true },
  { file: 'server-detail.png', path: `/#/servers/${featured.id}`, title: '服务器详情：指标曲线与主机信息', fullPage: true },
  { file: 'checks.png', path: '/#/checks', title: '检测项：五类探测与结果时间线', fullPage: true },
  { file: 'alerts.png', path: '/#/alerts', title: '告警：进行中 / 已确认 / 已恢复', fullPage: true },
  { file: 'roles.png', path: '/#/roles', title: '角色与权限：24 个权限点 + 资源范围', fullPage: true },
  { file: 'groups.png', path: '/#/groups', title: '服务器分组：授权单元与成员', fullPage: true },
  { file: 'users.png', path: '/#/users', title: '用户：角色指派、锁定与解锁', fullPage: true },
]

async function settle(selector) {
  await page.waitForLoadState('networkidle').catch(() => {})
  await page.waitForSelector(selector, { timeout: 15000 }).catch(() => {})
  await page.waitForTimeout(600)
}

// 截一张：先量「真实内容底部」（布局有 min-height，按 scrollHeight 截会留大片空白），
// 再按内容高度调整视口，最后整屏截图。
async function capture(path, file) {
  await page.goto(BASE + path, { waitUntil: 'domcontentloaded' })
  await settle('header')
  const contentHeight = await page.evaluate(() => {
    const root = document.querySelector('main') || document.body
    let bottom = 0
    for (const el of root.querySelectorAll('*')) {
      const rect = el.getBoundingClientRect()
      if (rect.height > 0 && rect.bottom + window.scrollY > bottom) {
        bottom = rect.bottom + window.scrollY
      }
    }
    return Math.ceil(bottom)
  })
  const targetHeight = Math.min(Math.max(contentHeight + 24, 640), 2400)
  await page.setViewportSize({ width: 1680, height: targetHeight })
  await page.waitForTimeout(400)
  await page.screenshot({ path: join(OUT, file) })
}

for (const shot of shots) {
  await capture(shot.path, shot.file)
  console.log(`✓ ${shot.file}  ${shot.title}`)
}

// 深色主题（主题能力演示）
await page.evaluate(() => window.localStorage.setItem('leadstack.theme', 'dark'))
await capture('/#/', 'dashboard-dark.png')
console.log('✓ dashboard-dark.png  深色主题总览')

await browser.close()
console.log(`\n完成：${OUT}/ 共 ${shots.length + 1} 张`)
