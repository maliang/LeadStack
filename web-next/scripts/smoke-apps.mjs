#!/usr/bin/env node
/**
 * 应用页冒烟（hermetic：Playwright 无头 + 内存 mock API，不碰真后端）。
 *
 * 覆盖：列表渲染 / 空态 / 搜索过滤 / 新建与编辑表单的必填与格式校验（不发请求） /
 * 删除二次确认（取消不删、确认才删） / 无 deploy.write 时写按钮隐藏且 0 写请求 /
 * 320px 无横向滚动 / 无未捕获异常。
 * 用法：pnpm --dir web-next run build && node web-next/scripts/smoke-apps.mjs [--headed]
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
  writes,
} from './lib/smoke-harness.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const flag = parseFlags(process.argv.slice(2))
const HEADED = flag('headed', false) === true
const TOKEN = 'smoke-password-smoke-token'
const PASSWORD = 'smoke-servers-password'

function makeApps() {
  return [
    {
      id: 'app-shop',
      name: 'shop-web',
      slug: 'shop-web',
      root: '/srv/shop-web',
      description: '',
      deploy_mode: 'release',
      server_count: 2,
      release_count: 5,
      last_release: {
        id: 'rel-shop-1',
        app_id: 'app-shop',
        app_name: 'shop-web',
        app_slug: 'shop-web',
        version: '1.4.2',
        status: 'success',
        created_at: Date.now() - 3_600_000,
        runs: [],
      },
    },
    {
      id: 'app-api',
      name: 'api-service',
      slug: 'api-service',
      root: '/srv/api',
      description: '',
      deploy_mode: 'inplace',
      server_count: 1,
      release_count: 2,
      last_release: null,
    },
  ]
}

function mockUser(state) {
  const permissions =
    state.permissions ?? (state.readOnly ? ['deploy.read'] : ['deploy.read', 'deploy.write'])
  return {
    id: 'user-smoke',
    username: state.readOnly ? 'viewer' : 'admin',
    role: state.readOnly ? 'viewer' : 'admin',
    role_name: state.readOnly ? '只读' : '管理员',
    permissions,
    scope: { mode: 'all', group_ids: [], server_ids: [] },
  }
}

/** 假后端：只覆盖应用页需要的端点，其他一律 404，避免静默放过。 */
async function installMockApi(context, state) {
  await context.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })

    if (path === '/api/v1/auth/login' && method === 'POST') {
      return json(200, { token: TOKEN, user: mockUser(state) })
    }
    if (path === '/api/v1/auth/me' && method === 'GET') {
      return json(200, { data: mockUser(state) })
    }
    if (path === '/api/v1/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/v1/apps' && method === 'GET') {
      state.appListCalls += 1
      return json(200, { data: state.empty ? [] : state.apps })
    }
    if (path === '/api/v1/releases' && method === 'GET') {
      // 应用详情页会按 app_id 拉最近发布；这里只需要不是 404，避免详情页进入失败态。
      return json(200, { data: [] })
    }
    if (path === '/api/v1/apps' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      const created = {
        id: `app-new-${state.apps.length + 1}`,
        name: body.name ?? '',
        slug: body.slug ?? '',
        root: body.root ?? null,
        description: body.description ?? '',
        deploy_mode: body.deploy_mode ?? 'release',
        server_count: 0,
        release_count: 0,
        last_release: null,
      }
      state.apps.push(created)
      return json(200, { data: created })
    }

    const detail = /^\/api\/v1\/apps\/([^/]+)$/.exec(path)
    if (detail) {
      const id = decodeURIComponent(detail[1])
      const found = state.apps.find((item) => item.id === id)
      if (method === 'GET') {
        return found ? json(200, { data: found }) : json(404, { error: { code: 'not_found' } })
      }
      if (method === 'PUT' || method === 'PATCH') {
        if (!found) return json(404, { error: { code: 'not_found' } })
        Object.assign(found, JSON.parse(request.postData() || '{}'))
        return json(200, { data: found })
      }
      if (method === 'DELETE') {
        state.apps = state.apps.filter((item) => item.id !== id)
        return route.fulfill({ status: 204, body: '' })
      }
    }

    return json(404, { error: { code: 'not_found', message: `mock api 未覆盖 ${method} ${path}` } })
  })
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

  const pageErrors = []
  const state = { apps: makeApps(), empty: false, readOnly: false, appListCalls: 0, permissions: null }

  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(context, state)
    const page = await context.newPage()
    page.on('pageerror', (error) => pageErrors.push(error.message))
    const calls = watchApiCalls(page)

    await login(page, base, { password: PASSWORD })
    await page.goto(`${base}/#/apps`, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#apps-title', { timeout: 15000 })

    // 1) 列表渲染
    await page.waitForSelector('.ls-deploy__row[data-app-id="app-shop"]', { timeout: 15000 })
    check('应用列表渲染两行', (await page.locator('.ls-deploy__row').count()) === 2)
    check('计数文案显示 2 / 2', (await page.locator('.ls-deploy__count').innerText()).includes('2 / 2'))
    check(
      '最近发布列显示版本徽标',
      (await page.locator('.ls-deploy__row[data-app-id="app-shop"]').innerText()).includes('1.4.2'),
    )
    check('未发布应用显示暂无发布', (await page.locator('.ls-deploy__row[data-app-id="app-api"]').innerText()).includes('暂无发布'))

    // 2) 搜索过滤
    await page.fill('#apps-search', 'shop')
    await page.waitForTimeout(150)
    check('搜索 shop 只剩一行', (await page.locator('.ls-deploy__row').count()) === 1)
    check('搜索后计数跟随为 1 / 2', (await page.locator('.ls-deploy__count').innerText()).includes('1 / 2'))
    await page.fill('#apps-search', 'zzz')
    await page.waitForTimeout(150)
    check('无匹配时显示空态文案', (await page.locator('.ls-empty__title').innerText()).includes('没有匹配'))
    check('无匹配空态带清空搜索入口', (await page.locator('.ls-empty .ls-btn').count()) === 1)
    await page.click('.ls-empty .ls-btn')
    await page.waitForTimeout(150)
    check('清空搜索恢复全部', (await page.locator('.ls-deploy__row').count()) === 2)

    // 3) 新建：必填校验不发请求，填齐才提交
    const posts = () => writes(calls).filter((call) => call.method === 'POST' && call.path === '/api/v1/apps').length
    await page.click('#apps-new')
    await page.waitForSelector('[data-testid="app-form-dialog"]', { timeout: 5000 })
    check('新建弹窗标题为新建应用', (await page.locator('[data-testid="app-form-dialog"] .ls-modal__title').innerText()).includes('新建应用'))
    await page.click('#app-form-submit')
    await page.waitForTimeout(150)
    check('空表单提交出现字段错误', (await page.locator('[data-testid="app-form-dialog"] .ls-field__error').count()) === 2)
    check('空表单提交不发 POST /apps', posts() === 0, `${posts()} 次`)
    await page.fill('#app-form-name', 'blog-site')
    await page.fill('#app-form-slug', 'Blog App!')
    await page.fill('#app-form-root', '/srv/blog')
    await page.click('#app-form-submit')
    await page.waitForTimeout(150)
    check('非法标识仍拦在表单里', (await page.locator('[data-testid="app-form-dialog"] .ls-field__error').count()) >= 1 && posts() === 0)
    await page.fill('#app-form-slug', 'blog-site')
    await page.click('#app-form-submit')
    await page.waitForSelector('[data-testid="app-form-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForSelector('.ls-deploy__row[data-app-id^="app-new"]', { timeout: 5000 })
    check('填齐后提交发出 1 次 POST /apps', posts() === 1, `${posts()} 次`)
    check('新建成功后列表出现新行', (await page.locator('.ls-deploy__row').count()) === 3)

    // 4) 编辑：表单预填 + PUT
    const shopRow = page.locator('.ls-deploy__row[data-app-id="app-shop"]')
    await shopRow.locator('button[aria-label^="编辑"]').click()
    await page.waitForSelector('[data-testid="app-form-dialog"]', { timeout: 5000 })
    check(
      '编辑弹窗预填应用名',
      (await page.locator('#app-form-name').inputValue()) === 'shop-web',
      await page.locator('#app-form-name').inputValue(),
    )
    await page.fill('#app-form-name', 'shop-web-v2')
    await page.click('#app-form-submit')
    await page.waitForSelector('[data-testid="app-form-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(200)
    check(
      '编辑后行内名称更新',
      (await page.locator('.ls-deploy__row[data-app-id="app-shop"]').innerText()).includes('shop-web-v2'),
    )
    check('编辑发出 1 次 PATCH /apps/app-shop', writes(calls).some((c) => c.method === 'PATCH' && c.path === '/api/v1/apps/app-shop'))

    // 5) 删除二次确认：取消不删，确认才删
    const deletes = () => writes(calls).filter((call) => call.method === 'DELETE').length
    await page.locator('.ls-deploy__row[data-app-id="app-shop"] .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="app-delete-dialog"]', { timeout: 5000 })
    const deleteText = await page.locator('[data-testid="app-delete-dialog"]').innerText()
    check('删除弹窗写明对象名', deleteText.includes('shop-web-v2'), deleteText.replace(/\s+/g, ' ').slice(0, 60))
    check('删除弹窗写明影响范围', deleteText.includes('5') && deleteText.includes('不可撤销'))
    await page.locator('[data-testid="app-delete-dialog"] .ls-modal__foot .ls-btn:not(.ls-btn--danger)').click()
    await page.waitForSelector('[data-testid="app-delete-dialog"]', { state: 'detached', timeout: 5000 })
    await page.waitForTimeout(200)
    check('取消删除不发 DELETE', deletes() === 0, `${deletes()} 次`)
    check('取消后行仍在', (await page.locator('.ls-deploy__row[data-app-id="app-shop"]').count()) === 1)

    await page.locator('.ls-deploy__row[data-app-id="app-shop"] .ls-btn--danger').click()
    await page.waitForSelector('[data-testid="app-delete-dialog"]', { timeout: 5000 })
    await page.locator('[data-testid="app-delete-dialog"] .ls-modal__foot .ls-btn--danger').click()
    await page.waitForSelector('.ls-deploy__row[data-app-id="app-shop"]', { state: 'detached', timeout: 5000 })
    check('确认删除发出 DELETE 且行消失', deletes() === 1, `${deletes()} 次`)
    check('删除后剩两行', (await page.locator('.ls-deploy__row').count()) === 2)

    // 6) 320px：页面无横向滚动，操作按钮仍可点到
    await page.setViewportSize({ width: 320, height: 800 })
    await page.waitForTimeout(250)
    const narrow = await page.evaluate(() => {
      const doc = document.documentElement
      const row = document.querySelector('.ls-deploy__row')
      return {
        overflow: doc.scrollWidth - window.innerWidth,
        rows: row ? row.getBoundingClientRect().width : 0,
        scrollBox: document.querySelector('.ls-deploy__scroll')
          ? document.querySelector('.ls-deploy__scroll').scrollWidth >
            document.querySelector('.ls-deploy__scroll').clientWidth
          : false,
      }
    })
    check('320px 页面无横向滚动', narrow.overflow <= 1, `溢出 ${narrow.overflow}px`)
    check('320px 表格由容器内滚承接', narrow.scrollBox)
    await page.setViewportSize({ width: 1280, height: 900 })

    // 7) 空态
    const listCallsBefore = state.appListCalls
    state.empty = true
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.ls-empty__title', { timeout: 15000 })
    check('空列表显示空态标题', (await page.locator('.ls-empty__title').innerText()).includes('还没有应用'))
    check('空列表不再请求旧数据', state.appListCalls > listCallsBefore)

    // 8) 无 deploy.write：写按钮隐藏且 0 写请求
    state.empty = false
    state.readOnly = true
    const viewerContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(viewerContext, state)
    const viewer = await viewerContext.newPage()
    viewer.on('pageerror', (error) => pageErrors.push(error.message))
    const viewerCalls = watchApiCalls(viewer)
    await login(viewer, base, { username: 'viewer', password: PASSWORD })
    await viewer.goto(`${base}/#/apps`, { waitUntil: 'domcontentloaded' })
    await viewer.waitForSelector('.ls-deploy__row', { timeout: 15000 })
    check('只读账号渲染列表', (await viewer.locator('.ls-deploy__row').count()) === 2)
    check('只读账号隐藏新建按钮', (await viewer.locator('#apps-new').count()) === 0)
    check('只读账号隐藏行内写按钮', (await viewer.locator('.ls-deploy__row .ls-btn').count()) === 0)
    check('只读账号显示只读文案', (await viewer.locator('.ls-deploy__row').first().innerText()).includes('只读'))
    check('只读账号 0 写请求', writes(viewerCalls).length === 0, `${writes(viewerCalls).length} 次`)
    await viewerContext.close()

    // 9) 权限一致性：详情页与列表页同点 deploy.read；仅 server.read 不得绕过
    state.permissions = ['deploy.read']
    const deployOnlyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(deployOnlyContext, state)
    const deployOnly = await deployOnlyContext.newPage()
    deployOnly.on('pageerror', (error) => pageErrors.push(error.message))
    const deployOnlyCalls = watchApiCalls(deployOnly)
    await login(deployOnly, base, { username: 'deploy-only', password: PASSWORD })
    await deployOnly.goto(`${base}/#/apps/app-api`, { waitUntil: 'domcontentloaded' })
    await deployOnly.waitForSelector('#app-detail-title', { timeout: 15000 })
    await deployOnly.waitForFunction(
      () => (document.querySelector('#app-detail-title')?.textContent ?? '').includes('api-service'),
      null,
      { timeout: 15000 },
    )
    check('仅 deploy.read 可进应用详情（未被重定向）', !deployOnly.url().includes('/forbidden'), deployOnly.url())
    check(
      '仅 deploy.read 应用详情渲染应用名',
      (await deployOnly.locator('#app-detail-title').innerText()).includes('api-service'),
    )
    const appDetailGets = deployOnlyCalls.filter((call) => call.method === 'GET' && call.path === '/api/v1/apps/app-api')
    check('仅 deploy.read 应用详情发出 1 次 GET /apps/app-api', appDetailGets.length === 1, `${appDetailGets.length} 次`)
    await deployOnlyContext.close()

    state.permissions = ['server.read']
    const serverOnlyContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await installMockApi(serverOnlyContext, state)
    const serverOnly = await serverOnlyContext.newPage()
    serverOnly.on('pageerror', (error) => pageErrors.push(error.message))
    const serverOnlyCalls = watchApiCalls(serverOnly)
    await login(serverOnly, base, { username: 'server-only', password: PASSWORD })
    await serverOnly.goto(`${base}/#/apps/app-api`, { waitUntil: 'domcontentloaded' })
    await serverOnly.waitForFunction(() => window.location.hash.startsWith('#/forbidden'), null, { timeout: 15000 })
    check('仅 server.read 进应用详情被重定向到 /forbidden', serverOnly.url().includes('/forbidden'), serverOnly.url())
    check(
      '仅 server.read 不请求 GET /apps/app-api',
      serverOnlyCalls.every((call) => !(call.method === 'GET' && call.path === '/api/v1/apps/app-api')),
      serverOnlyCalls.map((call) => call.path).join(','),
    )
    await serverOnlyContext.close()
    state.permissions = null

    check('无未捕获异常', pageErrors.length === 0, pageErrors.join(' / ').slice(0, 160))
    await context.close()
  } catch (error) {
    console.error(`✗ 冒烟中断：${error && error.stack ? error.stack.split('\n').slice(0, 4).join('\n') : error}`)
    process.exitCode = 1
  } finally {
    await browser.close()
    await new Promise((done) => server.close(done))
  }

  if (process.exitCode !== 1) process.exitCode = finish()
}

await run()
