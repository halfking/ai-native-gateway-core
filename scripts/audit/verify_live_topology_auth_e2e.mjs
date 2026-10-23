#!/usr/bin/env node
// verify_live_topology_auth_e2e.mjs — 实时拓扑「登录态 + 详情开新页」端到端验证
//
// ★ 验的是**用户可见的行为**，不是单元测试里的函数调用：
//   1. 冷启 /live 后**仍是登录态**（没被弹回 /?login=1，localStorage 里的
//      用户身份没被清）—— 这一条直接对应 2026-10-23 的线上事故
//      （冷启水合形状误判 ⇒ clearJwt ⇒ 「点实时拓扑被登出」）。
//   2. 请求列表点「↗」⇒ **新标签页**打开 /request-detail/<id>，
//      当前标签页仍停在 /live（不再是同页跳 /live/request/:id，也不是浮层）。
//   3. 新标签页**仍是登录态**，且**刷新后仍是登录态**（新页 = 一次冷启，
//      是这条链路上最容易被清状态的一跳）。
//
// 用法：
//   E2E_PASSWORD=… node scripts/audit/verify_live_topology_auth_e2e.mjs
//   E2E_BASE_URL=http://127.0.0.1:18781 node …   # 走隧道直连目标机
//
// ⚠ 两个实测得来的坑（改脚本前先读）：
//   a) 实时拓扑的初始帧**很大**：245 实测 `initial_data` 单帧 ≈ 3.5MB
//      / 200 条请求，**t+29s 才到**。等固定 5~9s 只会看到空面板 ——
//      那是**量具的问题**，不是产品缺陷。必须等条目出现。
//   b) 实时拓扑默认**隐藏探测请求**（showProbe 默认 false，owner 2026-10-18
//      口径），而窗口内可见流量大多带 is_probe ⇒ 直接用 `?probe=1` 进页面，
//      不要靠点开关（点开关会改 URL 触发重算，与首帧到达竞态）。
//
// 凭据来源：环境变量（不落盘、不进仓库）。仓内 deploy 脚本的登录冒烟
// 走的是同一条路（admin 登录 → admin 端点）。

import { createRequire } from 'node:module'
import { existsSync, globSync } from 'node:fs'
import { homedir } from 'node:os'
import path from 'node:path'

const require = createRequire(import.meta.url)

/** playwright 可能装在本地 node_modules，也可能只在 npx 缓存里 —— 两处都找。 */
function loadPlaywright() {
  for (const c of ['playwright', 'playwright-core']) {
    try {
      return require(c)
    } catch { /* 继续找 */ }
  }
  const npxRoot = path.join(homedir(), '.npm', '_npx')
  if (existsSync(npxRoot)) {
    for (const dir of globSync(path.join(npxRoot, '*', 'node_modules', 'playwright'))) {
      try {
        return require(dir)
      } catch { /* 继续找 */ }
    }
  }
  throw new Error('playwright 不可用：npm i -D playwright 或 npx playwright --version 先装上')
}

/** 本机已缓存的 Chrome for Testing（找不到就交给 playwright 自带版本）。 */
function findChrome() {
  const cache = path.join(homedir(), 'Library', 'Caches', 'ms-playwright')
  if (!existsSync(cache)) return ''
  const hit = globSync(path.join(cache, 'chromium-*', 'chrome-mac-arm64', 'Google Chrome for Testing.app', 'Contents', 'MacOS', '*'))
  return hit[0] || ''
}

const BASE = (process.env.E2E_BASE_URL || 'https://llmgo.kxpms.cn').replace(/\/$/, '')
const USER = process.env.E2E_USER || 'admin'
const PASSWORD = process.env.E2E_PASSWORD || process.env.LLM_GATEWAY_ADMIN_PASSWORD || ''
const CHROME_PATH = process.env.E2E_CHROME_PATH || findChrome()
// 视口要够放下三栏（客户端 / 拓扑 / 供应商）。1280 宽下右栏会盖住左栏的
// 「↗」按钮（pointer-events 被拦截）—— 那是窄屏形态，不是本轮要验的点。
const VIEWPORT = {
  width: Number(process.env.E2E_VIEWPORT_WIDTH || 1680),
  height: Number(process.env.E2E_VIEWPORT_HEIGHT || 1050),
}
const PANEL_TIMEOUT_MS = Number(process.env.E2E_PANEL_TIMEOUT_MS || 150000)

let failures = 0
function check(name, ok, detail = '') {
  const mark = ok ? '✓' : '✗'
  if (!ok) failures++
  console.log(`  ${mark} ${name}${detail ? `  — ${detail}` : ''}`)
  return ok
}

/** 页面是否仍处于登录态：URL 未被弹回登录页，且 localStorage 里身份还在。 */
async function authState(page) {
  const url = page.url()
  const bounced = /[?&]login=1/.test(url)
  const stored = await page.evaluate(() => localStorage.getItem('llmgw_user_info')).catch(() => null)
  let user = null
  try {
    user = stored ? JSON.parse(stored) : null
  } catch { user = null }
  return {
    url,
    bounced,
    storedUser: user,
    loggedIn: !bounced && !!user && typeof user.id === 'number' && !!user.role,
  }
}

async function main() {
  if (!PASSWORD) {
    console.error('✗ 缺凭据：请设置 E2E_PASSWORD（或 LLM_GATEWAY_ADMIN_PASSWORD）')
    process.exit(2)
  }
  const { chromium } = loadPlaywright()
  const browser = await chromium.launch({ headless: true, ...(CHROME_PATH ? { executablePath: CHROME_PATH } : {}) })
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: VIEWPORT })
  const page = await context.newPage()

  console.log(`\n=== 实时拓扑端到端验证 @ ${BASE} ===`)
  console.log(`    视口 ${VIEWPORT.width}×${VIEWPORT.height}；面板等待上限 ${PANEL_TIMEOUT_MS / 1000}s\n`)

  // ── 0. 登录取会话 cookie（浏览器里它是 HttpOnly）────────────────────────
  const loginRes = await context.request.post(`${BASE}/api/auth/token`, {
    data: { username: USER, password: PASSWORD },
  })
  if (!loginRes.ok()) {
    console.error(`✗ 登录失败：HTTP ${loginRes.status()}`)
    await browser.close()
    process.exit(2)
  }
  const setCookie = loginRes.headers()['set-cookie'] || ''
  const cookiePair = setCookie.split(';')[0]
  const host = new URL(BASE).hostname
  await context.addCookies([
    { name: 'llmgw_session', value: cookiePair.split('=').slice(1).join('='), domain: host, path: '/', httpOnly: true, secure: true },
  ])

  // ── 1. 冷启 /live（probe=1 让探测请求也可见，见文件头注 b）─────────────
  console.log('[1] 冷启 /live?probe=1（首屏 / 深链 / 新标签页都是这一跳）')
  await page.goto(`${BASE}/live?probe=1`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.live-topology', { timeout: 30000 }).catch(() => {})
  const st1 = await authState(page)
  check('URL 未被弹回登录页', !st1.bounced, st1.url)
  check('localStorage 身份未被清空', !!st1.storedUser, st1.storedUser ? `id=${st1.storedUser.id} role=${st1.storedUser.role}` : 'null')
  check('实时拓扑页已渲染', (await page.locator('.live-topology').count()) > 0)

  // 等请求列表填充（首帧 3.5MB，t+29s 量级 —— 见文件头注 a）
  const filled = await page.waitForSelector('.cp__item', { timeout: PANEL_TIMEOUT_MS }).then(() => true).catch(() => false)
  check('左栏请求列表已填充（等条目，不等固定时长）', filled,
    filled ? `${await page.locator('.cp__item').count()} 条` : `${PANEL_TIMEOUT_MS / 1000}s 内未出现条目`)
  if (!filled) {
    await browser.close()
    console.log('\n=== 结果：面板未填充，步骤 2/3 无法在本轮验证 ===\n')
    process.exit(1)
  }

  // ── 2. 请求列表点「↗」⇒ 新页 /request-detail/<id> ─────────────────────
  console.log('\n[2] 请求列表点「↗」⇒ 新标签页打开 /request-detail/<id>')
  const openBtn = page.locator('.cp__open').first()
  check('左栏存在「↗ 全页打开」入口', (await openBtn.count()) > 0)
  const popupPromise = context.waitForEvent('page', { timeout: 30000 }).catch(() => null)
  await openBtn.click({ timeout: 20000 }).catch(async (e) => {
    console.log('    ⚠ 常规点击被遮挡，改用 force 点击：', String(e.message).split('\n')[0])
    await openBtn.click({ force: true, timeout: 10000 })
  })
  const popup = await popupPromise
  check('确实开出了新标签页', !!popup)
  if (popup) {
    await popup.waitForLoadState('domcontentloaded').catch(() => {})
    const purl = popup.url()
    check('新页 URL 形如 /request-detail/<id>', /\/request-detail\/[^/?#]+/.test(purl), purl)
    check('新页不是 /live/request/… （旧的同页路由）', !purl.includes('/live/request'), purl)
    check('当前标签页仍停在 /live（没有被同页跳走）', /\/live/.test(page.url()), page.url())
    await popup.waitForSelector('#app', { timeout: 20000 }).catch(() => {})
    await popup.waitForTimeout(2000)
    const st2 = await authState(popup)
    check('新标签页仍是登录态（未丢登录态）', st2.loggedIn, st2.url)

    // ── 3. 新标签页刷新：仍是登录态（详情页也是一次冷启）─────────────────
    console.log('\n[3] 新标签页刷新（详情页也是一次冷启）')
    await popup.reload({ waitUntil: 'domcontentloaded' })
    await popup.waitForTimeout(3000)
    const st3 = await authState(popup)
    check('刷新后仍未被登出', st3.loggedIn, st3.url)
    check('刷新后身份仍在', !!st3.storedUser, st3.storedUser ? `id=${st3.storedUser.id}` : 'null')
  }

  // ── 4. 主标签页刷新：仍是登录态 ────────────────────────────────────────
  console.log('\n[4] 主标签页刷新 /live（又一次冷启）')
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.live-topology', { timeout: 30000 }).catch(() => {})
  await page.waitForTimeout(2500)
  const st4 = await authState(page)
  check('刷新后仍未被登出', st4.loggedIn, st4.url)
  check('刷新后身份仍在', !!st4.storedUser, st4.storedUser ? `id=${st4.storedUser.id}` : 'null')

  await browser.close()
  console.log(`\n=== 结果：${failures === 0 ? '全部通过' : `${failures} 条未通过`} ===\n`)
  process.exit(failures === 0 ? 0 : 1)
}

main().catch(async (e) => {
  console.error('✗ 脚本异常：', e?.message || e)
  process.exit(2)
})
