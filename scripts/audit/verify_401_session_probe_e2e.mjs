#!/usr/bin/env node
// verify_401_session_probe_e2e.mjs — 桌面端 401 会话裁决的端到端验证
//
// ★ 验的是什么（2026-10-24 审计轮修复的语义）：
//   **401 不等于会话过期**。踢回登录页之前必须先确认会话是否真的死了。
//   · 探针 GET /api/auth/me 成功 ⇒ 会话是好的 ⇒ 这个 401 是**端点自己拒**
//     ⇒ **不踢**、不清登录态（走页面级错误态）。
//   · 探针明确 401 ⇒ 会话确实死了 ⇒ 照旧踢（保留吊销后的强制登出）。
//
// ★ 怎么在浏览器里制造一次「端点级 401」：
//   Playwright 的 route 拦截把 **GET /api/admin/topology/plugins** 强制回 401。
//   选它的原因：`web/src/views/LiveTopologyView.vue:113` 在挂载时调
//   `topo.loadPlugins()`，而它走 `web/src/api/topology.ts` 的 `req()`
//   ⇒ **确定性地**触发一次 401，且只影响这一个端点。
//
// ★ 三条判据（缺一不可）：
//   1. 401 真的被注入了（否则下面的结论是空的）；
//   2. **探针打了**（/api/auth/me 的请求数在 401 之后 +1）—— 这是新实现
//      的**专属指纹**：旧实现（任何 401 一律踢）**不会**去探；
//   3. **没被踢**（URL 不含 login=1）且 **登录态没被清**
//      （localStorage 的 llmgw_user_info 还在、id 仍是数字）。
//
// ★ 对照组（必须跑，否则「全绿」说明不了问题）：
//   同一份脚本跑**旧实现**的实例 ⇒ 判据 2/3 必须**红**。
//   实测：245（旧代码，seq 2823）⇒ 2/3 红；154（新代码，seq 2828）⇒ 全绿。
//
// 用法：
//   E2E_PASSWORD=… node scripts/audit/verify_401_session_probe_e2e.mjs
//   E2E_BASE_URL=http://127.0.0.1:18782 node …   # 走隧道直连目标机
//
// 凭据来源：环境变量（不落盘、不进仓库）。

import { createRequire } from 'node:module'
import { existsSync, globSync } from 'node:fs'
import { homedir } from 'node:os'
import path from 'node:path'

const require = createRequire(import.meta.url)

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
const VIEWPORT = { width: 1680, height: 1050 }
const SETTLE_MS = Number(process.env.E2E_SETTLE_MS || 5000)

let failures = 0
function check(name, ok, detail = '') {
  const mark = ok ? '✓' : '✗'
  if (!ok) failures++
  console.log(`  ${mark} ${name}${detail ? `  — ${detail}` : ''}`)
  return ok
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

  console.log(`\n=== 401 会话裁决端到端验证 @ ${BASE} ===\n`)

  // ── 0. 登录取会话 cookie ───────────────────────────────────────────────
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

  // ── 1. 计数器：探针请求数 + 被注入的 401 次数 ──────────────────────────
  // ★ probesAfter401OnLivePage 才是**有区分力**的那个计数，两个污染源都要排掉：
  //   ① 首页水合的那次探针（发生在 401 之前）⇒ 用「401 已注入」做闸门排掉；
  //   ② 旧实现弹回登录页后新页面的**冷启水合**探针（URL 已含 login=1）
  //      ⇒ 用「URL 不含 login=1」排掉。
  //   ⇒ 旧实现：0；新实现：收到 401 后就地探（URL 仍是 /live）⇒ ≥1。
  let probeHits = 0
  let probesAfter401OnLivePage = 0
  let injected401 = 0
  await page.route('**/api/auth/me', async (route) => {
    probeHits++
    if (injected401 > 0 && !/[?&]login=1/.test(page.url())) probesAfter401OnLivePage++
    await route.continue()
  })
  await page.route('**/api/admin/topology/plugins', async (route) => {
    injected401++
    await route.fulfill({
      status: 401,
      contentType: 'application/json',
      body: JSON.stringify({ error: { detail: 'authentication required' } }),
    })
  })

  // ── 2. 先在首页把水合跑完（否则计数的基线不干净）────────────────────
  console.log('[1] 首页水合（把首屏的 /api/auth/me 消耗掉，建立计数基线）')
  await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(3000)
  const probeBaseline = probeHits
  console.log(`    探针基线 = ${probeBaseline}`)

  // ── 3. 进实时拓扑：plugins 端点被强制 401 ────────────────────────────
  console.log('[2] 进入 /live?probe=1 ⇒ GET /api/admin/topology/plugins 被强制 401')
  await page.goto(`${BASE}/live?probe=1`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(SETTLE_MS)

  const probeDelta = probeHits - probeBaseline
  const url = page.url()
  const bounced = /[?&]login=1/.test(url)
  const storedRaw = await page.evaluate(() => localStorage.getItem('llmgw_user_info')).catch(() => null)
  let stored = null
  try { stored = storedRaw ? JSON.parse(storedRaw) : null } catch { stored = null }

  console.log('')
  check('401 确实被注入了（否则以下结论是空的）', injected401 > 0, `注入 ${injected401} 次`)
  check('★ 401 之后在原页上探了 /api/auth/me（新实现的专属指纹）',
    probesAfter401OnLivePage > 0,
    `401 后的原页探针 = ${probesAfter401OnLivePage}（旧实现恒为 0：它那一次是弹回后的冷启水合，URL 已含 login=1）｜总增量 = ${probeDelta}`)
  check('★ 没被弹回登录页', !bounced, url)
  check('★ 登录态没被清空', !!stored && typeof stored.id === 'number',
    stored ? `id=${stored.id} role=${stored.role}` : 'null')

  await browser.close()
  console.log(`\n=== 结果：${failures === 0 ? '全部通过（新语义生效）' : `${failures} 条未通过（若是旧实例，这正是预期）`} ===\n`)
  process.exit(failures === 0 ? 0 : 1)
}

main().catch((e) => {
  console.error('✗ E2E 异常：', e)
  process.exit(3)
})
