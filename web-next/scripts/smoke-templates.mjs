#!/usr/bin/env node
/**
 * 应用模板页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖 /templates：
 *   —— 列表：3 条 mock 渲染名称/类型（中文标签）/更新时间、计数文案、表头四列；
 *   —— 新建：提交体恰为 {name,kind,content}、POST 恰 1 次、弹窗自动关闭；
 *   —— 校验：name 为空时不发请求且就地报必填错误；
 *   —— 编辑：回填表单、只发 PATCH 恰 1 次、请求体只含变更字段；
 *   —— 删除：二次确认含模板名与「发布历史不删除」影响说明，取消 0 次 DELETE、确认恰 1 次且行消失；
 *   —— 权限：缺 deploy.write 时三个写入口都不渲染且零写请求（列表仍可读）；
 *   —— 320px 无横向滚动（先等根类与行数就绪再量 scrollWidth）、pageerror 0。
 *
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-templates.mjs [--headed] [--shots dir]
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

const TOKEN = '{{SECRET_9tpl5m0c}}'
const PASSWORD = 'smoke-password'
const NOW = Date.now()

/** 模板页只需要 deploy.read/write；其余权限按最小集合给，避免误判「有写权限」。 */
const ALL_PERMISSIONS = ['deploy.read', 'deploy.write']

function userFixture(permissions) {
  return {
    id: 'u-templates',
    username: 'admin',
    role: 'admin',
    role_name: '管理员',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

/** 三条覆盖三种 kind；updated_at 各不相同，用来验证「更新时间」列不是同一个兜底值。 */
const TEMPLATE_FIXTURES = [
  {
    id: 'tmpl-1',
    name: 'shop-web-compose',
    kind: 'compose',
    content: 'services:\n  web:\n    image: nginx:1.27\n    ports:\n      - "8080:80"',
    created_at: NOW - 3 * 86_400_000,
    updated_at: NOW - 2 * 3_600_000,
  },
  {
    id: 'tmpl-2',
    name: 'agent-systemd',
    kind: 'systemd',
    content: '[Unit]\nDescription=agent\n\n[Service]\nExecStart=/usr/local/bin/agent',
    created_at: NOW - 5 * 86_400_000,
    updated_at: NOW - 86_400_000,
  },
  {
    id: 'tmpl-3',
    name: 'cli-binary',
    kind: 'binary',
    content: '#!/bin/sh\nset -eu\necho deploy',
    created_at: NOW - 7 * 86_400_000,
    updated_at: NOW - 10 * 60_000,
  },
]

function makeState() {
  return {
    permissions: [...ALL_PERMISSIONS],
    templates: TEMPLATE_FIXTURES.map((item) => ({ ...item })),
    listGets: 0,
    creates: [],
    patches: [],
    deletes: [],
  }
}

/** 假后端：只覆盖模板页用到的端点，其余一律 404，避免静默放过。 */
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

    if (path === '/api/v1/templates' && method === 'GET') {
      state.listGets += 1
      return json(200, { data: state.templates.map((item) => ({ ...item })) })
    }
    if (path === '/api/v1/templates' && method === 'POST') {
      const payload = body()
      state.creates.push(payload)
      const created = {
        id: 'tmpl-new',
        name: String(payload.name ?? ''),
        kind: String(payload.kind ?? 'compose'),
        content: String(payload.content ?? ''),
        created_at: NOW,
        updated_at: NOW,
      }
      state.templates.unshift(created)
      return json(201, { data: { ...created } })
    }

    const templateMatch = /^\/api\/v1\/templates\/([^/]+)$/.exec(path)
    if (templateMatch && method === 'PATCH') {
      const id = decodeURIComponent(templateMatch[1])
      const payload = body()
      state.patches.push({ id, body: payload })
      const target = state.templates.find((item) => item.id === id)
      if (!target) {
        return json(404, { error: { code: 'not_found', message: '模板不存在' } })
      }
      Object.assign(target, payload, { updated_at: NOW })
      return json(200, { data: { ...target } })
    }
    if (templateMatch && method === 'DELETE') {
      state.deletes.push(decodeURIComponent(templateMatch[1]))
      state.templates = state.templates.filter((item) => item.id !== decodeURIComponent(templateMatch[1]))
      return noContent()
    }

    return json(404, { error: { code: 'not_found', message: `${method} ${path} 未在冒烟 mock 中实现` } })
  })
}

/** hash 路由换页：等根类就绪，再等目标文本（懒加载 chunk 未到时可能读到旧页）。 */
async function gotoPage(page, base, hash, readySelector) {
  await page.goto(`${base}/#${hash}`, { waitUntil: 'domcontentloaded' })
  if (readySelector) await page.waitForSelector(readySelector, { timeout: 15000 })
}

function expectedDateTime(ts) {
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

async function run() {
  const { check, finish } = makeChecker()
  const distInfo = resolveDist(WEB_ROOT, flag)
  if (!distInfo.ready) {
    console.error(`✗ 未找到构建产物：${distInfo.dist}/index.html（先跑 pnpm run build）`)
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

  const { server, base } = await startStaticServer(distInfo.dist, Number(flag('port', 0)) || 0)
  log('serve', `${distInfo.dist} → ${base}`)

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
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)

    await login(page, base, { password: PASSWORD })
    await gotoPage(page, base, '/templates', '.ls-templates__row')

    /* ============ ① 列表渲染 ============ */
    check('部署预设页标题为「部署预设」', (await page.locator('#templates-title').innerText()).trim() === '部署预设')
    check('列表渲染 mock 的 3 条模板', (await page.locator('.ls-templates__row').count()) === 3)

    const names = await page.locator('.ls-templates__name').allInnerTexts()
    check(
      '名称列按 mock 顺序渲染 3 条',
      names.map((text) => text.trim()).join('|') === 'shop-web-compose|agent-systemd|cli-binary',
      names.join('|'),
    )

    const kinds = await page.locator('.ls-templates__kind').allInnerTexts()
    check(
      '类型列显示中文/约定标签（Compose/systemd/二进制）',
      kinds.map((text) => text.trim()).join('|') === 'Compose|systemd|二进制',
      kinds.join('|'),
    )

    const times = await page.locator('.ls-templates__time').allInnerTexts()
    const expectedTimes = TEMPLATE_FIXTURES.map((item) => expectedDateTime(item.updated_at))
    check(
      '更新时间列渲染各自的 updated_at（非兜底 —）',
      times.map((text) => text.trim()).join('|') === expectedTimes.join('|'),
      `${times.join('|')} ≠ ${expectedTimes.join('|')}`,
    )
    check(
      '顶部计数文案为「共 3 个预设」',
      (await page.locator('.ls-templates__count').innerText()).trim() === '共 3 个预设',
      (await page.locator('.ls-templates__count').innerText()).trim(),
    )

    const headers = (await page.locator('.ls-templates__table thead th').allInnerTexts()).map((text) => text.trim())
    check('表头四列为 名称/类型/更新时间/操作', headers.join('|') === '名称|类型|更新时间|操作', headers.join('|'))

    /* ============ ② 新建 ============ */
    await page.click('#templates-new')
    await page.waitForSelector('#template-form-name', { timeout: 5000 })
    const kindOptions = (await page.locator('#template-form-kind option').allInnerTexts()).map((text) => text.trim())
    check(
      '新建弹窗类型下拉为三选一中文标签',
      kindOptions.join('|') === 'Compose|systemd|二进制',
      kindOptions.join('|'),
    )

    await page.fill('#template-form-name', 'nightly-backup')
    await page.selectOption('#template-form-kind', 'binary')
    await page.fill('#template-form-content', '#!/bin/bash\nrsync -a /srv /backup')
    await page.click('#template-form-submit')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 8000 })

    const created = state.creates[0]
    check(
      '新建提交体恰为 {name,kind,content}',
      state.creates.length === 1 &&
        JSON.stringify(Object.keys(created).sort()) === JSON.stringify(['content', 'kind', 'name']) &&
        created.name === 'nightly-backup' &&
        created.kind === 'binary' &&
        created.content === '#!/bin/bash\nrsync -a /srv /backup',
      JSON.stringify(created),
    )
    check('新建 POST /api/v1/templates 恰 1 次', state.creates.length === 1, String(state.creates.length))
    await page.waitForSelector('[data-template-id="tmpl-new"]', { timeout: 8000 })
    check(
      '新建成功后弹窗关闭且列表出现新模板',
      (await page.locator('#template-form-name').count()) === 0 &&
        (await page.locator('[data-template-id="tmpl-new"] .ls-templates__name').innerText()).trim() === 'nightly-backup',
    )

    /* ============ ③ name 为空：0 请求 + 就地报错 ============ */
    await page.click('#templates-new')
    await page.waitForSelector('#template-form-name', { timeout: 5000 })
    await page.fill('#template-form-content', '#!/bin/sh\necho empty-name')
    await page.click('#template-form-submit')
    await page.waitForTimeout(400)
    check(
      'name 为空时 0 请求（POST 次数不变）',
      state.creates.length === 1 && state.patches.length === 0,
      `creates=${state.creates.length}`,
    )
    check(
      'name 为空时就地显示必填错误且弹窗不关闭',
      (await page.locator('#template-form-name').getAttribute('aria-invalid')) === 'true' &&
        (await page.locator('#template-form-name + .ls-field__error').innerText()).trim() === '请填写模板名称' &&
        (await page.locator('#template-form-name').count()) === 1,
    )
    await page.click('.ls-modal__close')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 5000 })

    /* ============ ④ 编辑：只发 PATCH 且只含变更字段 ============ */
    await page.click('[data-template-id="tmpl-1"] .ls-templates__row-actions button >> nth=0')
    await page.waitForSelector('#template-form-name', { timeout: 5000 })
    check(
      '编辑弹窗回填名称/类型/内容',
      (await page.inputValue('#template-form-name')) === 'shop-web-compose' &&
        (await page.inputValue('#template-form-kind')) === 'compose' &&
        (await page.inputValue('#template-form-content')) === TEMPLATE_FIXTURES[0].content,
    )

    await page.fill('#template-form-name', 'shop-web-compose-v2')
    await page.click('#template-form-submit')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 8000 })

    const patch = state.patches[0] ?? { id: '', body: {} }
    check(
      '编辑只发 PATCH 且恰 1 次（无 POST/DELETE）',
      state.patches.length === 1 && state.creates.length === 1 && state.deletes.length === 0,
      `patches=${state.patches.length}`,
    )
    check(
      '编辑请求体只含变更字段 name',
      patch.id === 'tmpl-1' &&
        JSON.stringify(Object.keys(patch.body)) === JSON.stringify(['name']) &&
        patch.body.name === 'shop-web-compose-v2',
      JSON.stringify(patch),
    )
    await page.waitForFunction(
      () =>
        document.querySelector('[data-template-id="tmpl-1"] .ls-templates__name')?.textContent?.trim() ===
        'shop-web-compose-v2',
      null,
      { timeout: 8000 },
    )
    check(
      '编辑后行内名称更新为新值',
      (await page.locator('[data-template-id="tmpl-1"] .ls-templates__name').innerText()).trim() ===
        'shop-web-compose-v2',
    )

    /* ============ ④.1 kind-only：只改类型 → PATCH 体键集合恰为 ['kind'] ============ */
    // 判别性：若 templatePatchFromForm 退化成无条件全量 payload，键集合会变成 name/kind/content，本段必红。
    const patchesBeforeKind = state.patches.length
    await page.click('[data-template-id="tmpl-2"] .ls-templates__row-actions button >> nth=0')
    await page.waitForSelector('#template-form-kind', { timeout: 5000 })
    await page.selectOption('#template-form-kind', 'compose')
    await page.click('#template-form-submit')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 8000 })
    const kindPatch = state.patches[patchesBeforeKind] ?? { id: '', body: {} }
    check(
      "只改类型时 PATCH 体键集合恰为 ['kind']",
      state.patches.length === patchesBeforeKind + 1 &&
        kindPatch.id === 'tmpl-2' &&
        JSON.stringify(Object.keys(kindPatch.body)) === JSON.stringify(['kind']) &&
        kindPatch.body.kind === 'compose',
      JSON.stringify(kindPatch),
    )

    /* ============ ④.2 content-only：只改内容 → PATCH 体键集合恰为 ['content'] ============ */
    const patchesBeforeContent = state.patches.length
    await page.click('[data-template-id="tmpl-3"] .ls-templates__row-actions button >> nth=0')
    await page.waitForSelector('#template-form-content', { timeout: 5000 })
    const NEW_CONTENT = '#!/bin/sh\necho cli-binary-content-only'
    await page.fill('#template-form-content', NEW_CONTENT)
    await page.click('#template-form-submit')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 8000 })
    const contentPatch = state.patches[patchesBeforeContent] ?? { id: '', body: {} }
    check(
      "只改内容时 PATCH 体键集合恰为 ['content']",
      state.patches.length === patchesBeforeContent + 1 &&
        contentPatch.id === 'tmpl-3' &&
        JSON.stringify(Object.keys(contentPatch.body)) === JSON.stringify(['content']) &&
        contentPatch.body.content === NEW_CONTENT,
      JSON.stringify(contentPatch),
    )

    /* ============ ④.3 无变更：0 次 PATCH 且弹窗直接收起 ============ */
    // 判别性：全量 payload 变异时这里会多发一次 PATCH（键集合非空），计数立刻变红。
    const patchesBeforeNoop = state.patches.length
    await page.click('[data-template-id="tmpl-1"] .ls-templates__row-actions button >> nth=0')
    await page.waitForSelector('#template-form-submit', { timeout: 5000 })
    await page.click('#template-form-submit')
    await page.waitForSelector('#template-form-name', { state: 'detached', timeout: 8000 })
    await page.waitForTimeout(300)
    check(
      '编辑无变更时 0 次 PATCH 且弹窗直接收起',
      state.patches.length === patchesBeforeNoop &&
        (await page.locator('#template-form-name').count()) === 0,
      `patches=${state.patches.length - patchesBeforeNoop}`,
    )

    /* ============ ⑤ 删除：二次确认 ============ */
    await page.click('[data-template-id="tmpl-2"] .ls-templates__row-actions button >> nth=1')
    await page.waitForSelector('#template-delete-confirm', { timeout: 5000 })
    const deleteText = await page.locator('.ls-templates__delete').innerText()
    check('删除确认弹窗写明模板名', deleteText.includes('模板：agent-systemd'), deleteText.replace(/\n/g, ' / '))
    check(
      '删除确认弹窗写明影响范围（发布历史不删除）',
      deleteText.includes('引用该模板的发布历史不会删除，但新建发布将无法选择它。'),
      deleteText.replace(/\n/g, ' / '),
    )

    await page.click('.ls-modal__foot button >> nth=0')
    await page.waitForSelector('#template-delete-confirm', { state: 'detached', timeout: 5000 })
    check('取消二次确认后 0 次 DELETE 且弹窗关闭', state.deletes.length === 0 && (await page.locator('[data-template-id="tmpl-2"]').count()) === 1)

    await page.click('[data-template-id="tmpl-2"] .ls-templates__row-actions button >> nth=1')
    await page.waitForSelector('#template-delete-confirm', { timeout: 5000 })
    await page.click('#template-delete-confirm')
    await page.waitForSelector('[data-template-id="tmpl-2"]', { state: 'detached', timeout: 8000 })
    check(
      '确认删除恰 1 次 DELETE 且行消失',
      state.deletes.join(',') === 'tmpl-2' && (await page.locator('.ls-templates__row').count()) === 3,
      state.deletes.join(','),
    )

    /* ============ ⑥ 权限：缺 deploy.write 三写入口 0 个且零写请求 ============ */
    const writesBefore = state.creates.length + state.patches.length + state.deletes.length
    const callsBefore = calls.length
    state.permissions = ['deploy.read']
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await gotoPage(page, base, '/templates', '.ls-templates__row')
    const readonlyRows = await page.locator('.ls-templates__row').count()
    check(
      '缺 deploy.write 时列表仍可读但三个写入口 0 个',
      readonlyRows === 3 &&
        (await page.locator('#templates-new').count()) === 0 &&
        (await page.locator('.ls-templates__row-actions').count()) === 0 &&
        (await page.locator('.ls-templates__readonly').count()) === 3,
      `rows=${readonlyRows}`,
    )
    await page.waitForTimeout(400)
    const writeCallsDuringReadonly = calls
      .slice(callsBefore)
      .filter((call) => ['POST', 'PATCH', 'PUT', 'DELETE'].includes(call.method) && call.path.includes('/templates'))
    check(
      '缺 deploy.write 时模板写请求 0 次',
      state.creates.length + state.patches.length + state.deletes.length === writesBefore &&
        writeCallsDuringReadonly.length === 0,
      writeCallsDuringReadonly.map((call) => `${call.method} ${call.path}`).join(','),
    )

    /* ============ ⑦ 320px 无横向滚动 ============ */
    state.permissions = [...ALL_PERMISSIONS]
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-shell', { timeout: 15000 })
    await page.setViewportSize({ width: 320, height: 900 })
    await gotoPage(page, base, '/templates', '.ls-templates')
    // 根类存在还不够：懒加载未完成时量到的是空壳，必须等表格行数与目标页标题都就绪。
    const narrowReady = await page
      .waitForFunction(
        () =>
          document.querySelector('.ls-templates') !== null &&
          document.querySelector('#templates-title')?.textContent?.trim() === '部署预设' &&
          document.querySelectorAll('.ls-templates__row').length === 3,
        null,
        { timeout: 15000 },
      )
      .then(() => true)
      .catch(() => false)
    const narrowOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    )
    check(
      '320px 模板页已渲染（行数 3）且无横向滚动',
      narrowReady && narrowOverflow <= 0,
      `rowsReady=${narrowReady} overflow=${narrowOverflow}`,
    )

    check('全程 pageerror 0（无未捕获异常）', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))

    if (SHOTS) {
      await page.screenshot({ path: resolve(SHOTS, 'templates-320.png'), fullPage: true }).catch(() => {})
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
