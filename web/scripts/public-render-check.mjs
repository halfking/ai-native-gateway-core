#!/usr/bin/env node
/**
 * public-render-check.mjs —— 公开页的**渲染**验证（2026-10-25）
 *
 * 为什么要有这道门
 * ────────────────
 * 本仓的静态门（craft-floor / copy-quality / ui-audit）只能证明**源码**合规，
 * 证不了「渲染出来是对的」—— 而本轮真正抓到的两个问题**都是渲染期才暴露的**：
 *   ① 403 页标题渲染成「403 · 403 - 访问被拒绝」⇒ **组件给标题加的前缀**与
 *      语言包里的标题**各带了一次 403**（8 种语言全中）。
 *   ② 落地页的 H1 到底说不说人话、RTL 镜像对不对、字体有没有真下载 ——
 *      静态门一个字都答不了。
 *
 * ★ 为什么单独一道「公开页」的门（而不是扩展 ui-sweep.mjs）：
 *   ui-sweep 需要**完整后端**（登录态、租户上下文）；
 *   而**公开页是唯一不需要后端就能验的面** —— 落地页恰恰是设计最敏感的那一面。
 *   ⇒ 这一道门在 `vite preview` 上就能跑，进了 CI 也能当冒烟。
 *
 * 断言清单（每条都有 --selftest 自证）
 * ────────────────────────────────
 *   ① 无「非 API 类」控制台错误（后端未起 ⇒ /api/* 500 属预期，不算）
 *   ② 无资源加载失败（字体/CSS/JS）
 *   ③ 自托管字体真的下载并生效（--kx-font-sans 解析到 IBM Plex Sans）
 *   ④ 浏览器表面带主题（::selection 有底色）
 *   ⑤ 无 kicker / eyebrow、渐变文字、emoji、全大写标题
 *   ⑥ **标题里没有重复的状态码**（「403 · 403 - …」这类拼接重复）
 *
 * 用法
 * ────
 *   # 终端 A：起预览
 *   cd web && pnpm exec vite preview --port 5899 --strictPort
 *   # 终端 B：跑检查
 *   BASE_URL=http://127.0.0.1:5899 node scripts/public-render-check.mjs
 *   BASE_URL=http://127.0.0.1:5899 node scripts/public-render-check.mjs --selftest
 *   BASE_URL=http://127.0.0.1:5899 node scripts/public-render-check.mjs --json out.json
 *
 * ★★ 为什么默认等 13 秒（不是 1 秒）
 *   路由守卫（src/router.ts）会等 `/api/auth/me` settle 再放行，
 *   上限 AUTH_HYDRATE_MAX_WAIT_MS = 10_000 ms。
 *   等不够 ⇒ 守卫还在等 ⇒ 主体是空的 ⇒ **会误判成「页面没渲染」**。
 *   本轮就踩过：2 秒等待 ⇒ /login 与 /forbidden 看起来是空壳；
 *   13 秒 ⇒ 两者都正常渲染。⇒ 这个等待时间是**量具的一部分**，不是可有可无的调参。
 */
import { readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const BASE = process.env.BASE_URL || 'http://127.0.0.1:5899'
/** 必须 > 路由守卫的 AUTH_HYDRATE_MAX_WAIT_MS(10s)，否则主体还没渲染。 */
const WAIT_MS = Number(process.env.RENDER_WAIT_MS || 13000)

// ── 解析 playwright ────────────────────────────────────────────────
// 沿用 ui-sweep.mjs 的约定：web/ 不装 playwright（不想为一次审计给产品加依赖），
// 从 npx 缓存 / 兄弟项目里找。
// ★ 候选的可用性判据是「**能不能真启动一个浏览器**」，不是「能不能 require 到」
//   （npx 缓存里几个 playwright 的 chromium 修订号可能没装）。
function playwrightCandidates() {
  const roots = []
  const npxRoot = join(process.env.HOME || '', '.npm', '_npx')
  let entries = []
  try {
    entries = readdirSync(npxRoot)
  } catch (e) {
    if (e.code !== 'ENOENT') throw e
  }
  for (const d of entries) roots.push(join(npxRoot, d, 'node_modules', 'playwright'))
  roots.push(
    '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/ai-native-maintain/maintain-web/node_modules/playwright',
  )
  return roots
}

async function launchBrowser() {
  const tried = []
  for (const p of playwrightCandidates()) {
    let chromium
    try {
      chromium = createRequire(import.meta.url)(p).chromium
    } catch (e) {
      tried.push(`${p} → require 失败（${e.code || String(e.message).slice(0, 50)}）`)
      continue
    }
    // 优先用 playwright 自带的 chromium；没装则退到系统 Chrome（channel: 'chrome'）。
    for (const opts of [{ headless: true }, { headless: true, channel: 'chrome' }]) {
      try {
        const browser = await chromium.launch(opts)
        return { browser, from: `${p}（${opts.channel || 'chromium'}）` }
      } catch (e) {
        tried.push(`${p} ${opts.channel || 'chromium'} → launch 失败（${String(e.message).split('\n')[0].slice(0, 80)}）`)
      }
    }
  }
  throw new Error(
    `没有可用的 playwright。试过：\n  - ${tried.join('\n  - ') || '（无候选路径）'}` +
      '\n若版本对不上，跑 `npx playwright install chromium` 补齐。',
  )
}

// ─────────────────────────────────────────────────────────────
// 判据（纯函数，自证与实跑共用同一份）
// ─────────────────────────────────────────────────────────────

/**
 * 标题里是否出现了**重复的状态码**。
 *   形态：组件写 `:title="`403 · ${t('forbidden.title')}`"`，而语言包里
 *   的 `forbidden.title` 本身就是「403 - 访问被拒绝」⇒ 渲染出「403 · 403 - …」。
 *   ⇒ 判据：同一个 3 位数字码，在标题里出现两次（中间隔着非数字）。
 * ★ 只看**标题/副标题**：那才是状态码该出现的地方；
 *   正文里出现两个同样的数字是正常内容。
 */
export function hasRepeatedStatusCode(text) {
  if (!text) return false
  return /(\d{3})\D+?\1/.test(text)
}

/** 控制台错误里哪些是「后端未起」造成的（不计入失败）。 */
export function isBackendNoise(text) {
  return /\/api\/|\/maintain-api\/|Failed to load resource/.test(String(text || ''))
}

/** 一页的断言。probe = 在页面里 evaluate 出来的读数。 */
export function checkPage(probe, consoleErrors, resourceFailures) {
  const problems = []
  for (const e of consoleErrors) {
    if (!isBackendNoise(e)) problems.push(`控制台错误：${e}`)
  }
  for (const r of resourceFailures) problems.push(`资源加载失败：${r}`)
  if (!probe) { problems.push('拿不到页面读数'); return problems }
  // ★ 「主体空」的判据是**语义**的（有没有渲染出标题），不是字符数。
  //   第一版用 `innerText < 300` ⇒ 403 页（166 字符：小标题 + 说明 + 两个按钮）
  //   被判成「没渲染」—— 那是**假红**：门自己的阈值比目标页面的实际体量还大。
  //   ⇒ 改成「一个标题都没渲染出来」才算空（空壳时 h1/h2/h3 一个都没有）。
  if (!probe.headings || probe.headings.length === 0) {
    problems.push(`主体没有渲染出任何标题（innerText ${probe.textLen} 字符）—— 等待不够或页面没渲染`)
  }
  if (probe.fontsLoaded === 0) problems.push('自托管字体一个都没下载')
  if (!/IBM Plex Sans/i.test(probe.bodyFont || '')) {
    problems.push(`正文没用上自托管字体（实际：${probe.bodyFont}）`)
  }
  if (!probe.selectionColor || probe.selectionColor === 'rgba(0, 0, 0, 0)') {
    problems.push(`选区没有主题色（实际：${probe.selectionColor}）`)
  }
  if (probe.kickerish > 0) problems.push(`还有 ${probe.kickerish} 处 kicker/eyebrow`)
  if (probe.gradientText > 0) problems.push(`还有 ${probe.gradientText} 处渐变文字`)
  if (probe.emoji > 0) problems.push(`还有 ${probe.emoji} 个 emoji 字形`)
  if (probe.upperCaseHeadings > 0) problems.push(`还有 ${probe.upperCaseHeadings} 个全大写标题`)
  for (const h of probe.headings || []) {
    if (hasRepeatedStatusCode(h)) problems.push(`标题里状态码重复：「${h}」`)
  }
  return problems
}

/** 在页面里采集读数。 */
const PROBE_FN = () => {
  const root = getComputedStyle(document.documentElement)
  const sel = getComputedStyle(document.documentElement, '::selection')
  const body = getComputedStyle(document.body)
  const txt = document.body.innerText || ''
  const headings = [...document.querySelectorAll('h1,h2,h3')]
    .map((el) => (el.innerText || '').trim())
    .filter(Boolean)
  return {
    lang: document.documentElement.lang,
    dir: document.documentElement.dir || 'ltr',
    textLen: txt.length,
    bodyFont: body.fontFamily,
    fontSansVar: root.getPropertyValue('--kx-font-sans'),
    selectionColor: sel.backgroundColor,
    radiusCard: root.getPropertyValue('--radius-card').trim(),
    kickerish: document.querySelectorAll('[class*="kicker"], [class*="eyebrow"], [class*="deco-orb"], .orb').length,
    gradientText: [...document.querySelectorAll('h1,h2,h3,p,span,div')].filter((el) => {
      const cs = getComputedStyle(el)
      return cs.webkitBackgroundClip === 'text' || cs.backgroundClip === 'text'
    }).length,
    emoji: (txt.match(/[\u{1F300}-\u{1FAFF}\u{2705}\u{274C}\u{2B50}\u{26A1}\u{23F3}\u{23F1}]/gu) || []).length,
    upperCaseHeadings: headings.filter((h) => {
      const el = [...document.querySelectorAll('h1,h2,h3')].find((x) => (x.innerText || '').trim() === h)
      return el && getComputedStyle(el).textTransform === 'uppercase'
    }).length,
    headings,
  }
}

// ─────────────────────────────────────────────────────────────
// 自证
// ─────────────────────────────────────────────────────────────

const CLEAN_PROBE = {
  textLen: 1400, fontsLoaded: 3, bodyFont: '"IBM Plex Sans", sans-serif',
  selectionColor: 'rgba(30, 79, 214, 0.26)', kickerish: 0, gradientText: 0, emoji: 0,
  upperCaseHeadings: 0, headings: ['403 - 访问被拒绝', '核心能力'],
}
const FIXTURES = [
  {
    name: '干净页面 → 必须判绿',
    expect: 'green',
    probe: CLEAN_PROBE, errors: [], resources: [],
  },
  {
    name: '标题里状态码重复（403 · 403 - …）→ 必须判红',
    expect: 'red',
    probe: { ...CLEAN_PROBE, headings: ['403 · 403 - 访问被拒绝'] }, errors: [], resources: [],
  },
  {
    name: '后端未起的 API 500 → 必须判绿（属预期噪声）',
    expect: 'green',
    probe: CLEAN_PROBE,
    errors: ['Failed to load resource: the server responded with a status of 500'],
    resources: [],
  },
  {
    name: '非 API 类控制台错误 → 必须判红',
    expect: 'red',
    probe: CLEAN_PROBE, errors: ['Uncaught TypeError: x is not a function'], resources: [],
  },
  {
    name: '资源加载失败（字体 404）→ 必须判红',
    expect: 'red',
    probe: CLEAN_PROBE, errors: [], resources: ['404 /assets/ibm-plex-sans-latin-400-normal.woff2'],
  },
  {
    name: '字体一个都没下载 → 必须判红',
    expect: 'red',
    probe: { ...CLEAN_PROBE, fontsLoaded: 0 }, errors: [], resources: [],
  },
  {
    name: '正文没用自托管字体 → 必须判红',
    expect: 'red',
    probe: { ...CLEAN_PROBE, bodyFont: '-apple-system, sans-serif' }, errors: [], resources: [],
  },
  {
    name: '选区没有主题色 → 必须判红',
    expect: 'red',
    probe: { ...CLEAN_PROBE, selectionColor: 'rgba(0, 0, 0, 0)' }, errors: [], resources: [],
  },
  {
    name: '还有 kicker / 渐变文字 / emoji → 必须判红',
    expect: 'red',
    probe: { ...CLEAN_PROBE, kickerish: 2, gradientText: 1, emoji: 3 }, errors: [], resources: [],
  },
  {
    name: '主体是空的（一个标题都没有）→ 必须判红（等待不够 / 没渲染）',
    expect: 'red',
    probe: { ...CLEAN_PROBE, textLen: 60, headings: [] }, errors: [], resources: [],
  },
  {
    name: '小页面（403 只有 166 字符但有标题）→ 必须判绿（别按字符数判空）',
    expect: 'green',
    probe: { ...CLEAN_PROBE, textLen: 166, headings: ['403 - 访问被拒绝'] }, errors: [], resources: [],
  },
]

function selftest() {
  const results = []
  for (const fx of FIXTURES) {
    const problems = checkPage(fx.probe, fx.errors, fx.resources)
    const ok = fx.expect === 'green' ? problems.length === 0 : problems.length > 0
    results.push({
      name: fx.name, ok,
      detail: problems.length ? problems.join('；').slice(0, 160) : '无问题',
    })
  }
  return results
}

// ─────────────────────────────────────────────────────────────
// 主流程
// ─────────────────────────────────────────────────────────────

const CASES = [
  { name: 'landing-1440-zhCN', url: '/?login=1', locale: 'zh-CN', w: 1440, h: 900, closeModal: true },
  { name: 'landing-390-zhCN', url: '/?login=1', locale: 'zh-CN', w: 390, h: 844, closeModal: true },
  { name: 'landing-1440-en', url: '/?login=1', locale: 'en', w: 1440, h: 900, closeModal: true },
  { name: 'landing-1440-ja', url: '/?login=1', locale: 'ja', w: 1440, h: 900, closeModal: true },
  { name: 'landing-1440-ar', url: '/?login=1', locale: 'ar', w: 1440, h: 900, closeModal: true },
  { name: 'forbidden-1440-zhCN', url: '/forbidden', locale: 'zh-CN', w: 1440, h: 900 },
  { name: 'login-1440-zhCN', url: '/login', locale: 'zh-CN', w: 1440, h: 900 },
]

/**
 * 环回地址有两种写法（127.0.0.1 / localhost），而 vite 的 preview / dev
 * 只会绑到其中一种（本机实测：vite 绑 `localhost` ⇒ 解析到 ::1 ⇒
 * 127.0.0.1 直接 ERR_CONNECTION_REFUSED）。
 *
 * ★ 不处理的后果：门会给出「0/7 通过 + 连接被拒」，★ 看起来像**应用坏了**，
 *   而实际是**地址写法不对** ⇒ 一个把量具指错地方的假红。
 * ⇒ 连接被拒时自动换另一种写法重试，并**打印实际用的那个**。
 *   其它错误（页面本身的问题）不触发切换 —— 那种情况地址是通的。
 */
const LOOPBACK_ALT = { '127.0.0.1': 'localhost', 'localhost': '127.0.0.1' }

async function resolveBase(page, base) {
  const probe = async (b) => {
    try {
      await page.goto(b + '/', { waitUntil: 'domcontentloaded', timeout: 15000 })
      return { reachable: true }
    } catch (e) {
      const m = String(e.message || '')
      if (/ERR_CONNECTION_REFUSED|ERR_ADDRESS_UNREACHABLE|ERR_INVALID_URL/.test(m)) {
        return { reachable: false, refused: true }
      }
      return { reachable: true } // 其它错误 ⇒ 地址是通的，问题在页面本身
    }
  }
  const first = await probe(base)
  if (first.reachable) return { base, switched: false }
  let u
  try { u = new URL(base) } catch { return { base, switched: false } }
  const alt = LOOPBACK_ALT[u.hostname]
  if (!alt) return { base, switched: false }
  const altBase = base.replace(u.hostname, alt)
  const second = await probe(altBase)
  return second.reachable ? { base: altBase, switched: true } : { base, switched: false }
}

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const results = selftest()
    let failed = 0
    for (const r of results) {
      console.log(`  ${r.ok ? '✓' : '✗'} ${r.name} —— ${r.detail}`)
      if (!r.ok) failed++
    }
    console.log(`\n${failed === 0 ? '✅' : '❌'} 判据自证：${results.length - failed}/${results.length} 通过`)
    process.exit(failed === 0 ? 0 : 1)
  }

  const { browser, from } = await launchBrowser()
  // ★ 先定地址：环回写法不对时自动换一种，别让门把「地址写错」报成「应用坏了」。
  const probeCtx = await browser.newContext()
  const probePage = await probeCtx.newPage()
  const { base: RESOLVED_BASE, switched } = await resolveBase(probePage, BASE)
  await probeCtx.close()
  if (switched) {
    console.log(`⚠ ${BASE} 连不上（vite 只绑了另一种环回写法）⇒ 改用 ${RESOLVED_BASE}`)
  }
  console.log(`浏览器：${from}\n目标：${RESOLVED_BASE}\n等待：${WAIT_MS} ms（须 > 路由守卫的 10s auth 水位）\n`)

  const results = []
  let failed = 0
  for (const c of CASES) {
    const ctx = await browser.newContext({ viewport: { width: c.w, height: c.h }, deviceScaleFactor: 2 })
    const page = await ctx.newPage()
    await page.addInitScript((loc) => {
      try { localStorage.setItem('llmgw_locale', loc) } catch (e) { /* 无痕模式等 */ }
    }, c.locale)

    const errors = []
    const resources = []
    let fontsLoaded = 0
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
    page.on('pageerror', (e) => errors.push('pageerror: ' + e.message))
    page.on('response', (r) => {
      const u = r.url()
      if (/\.woff2?(\?|$)/.test(u)) { if (r.status() < 400) fontsLoaded++ }
      else if (r.status() >= 400 && !/\/api\/|\/maintain-api\//.test(u)) {
        resources.push(`${r.status()} ${u.replace(RESOLVED_BASE, '').slice(0, 100)}`)
      }
    })

    await page.goto(RESOLVED_BASE + c.url, { waitUntil: 'domcontentloaded', timeout: 30000 })
      .catch((e) => errors.push('goto: ' + e.message))
    await page.waitForTimeout(WAIT_MS)

    if (c.closeModal) {
      const cancel = page.locator('button:has-text("取消"), button:has-text("Cancel")').first()
      if (await cancel.count()) await cancel.click({ timeout: 3000 }).catch(() => {})
      else await page.keyboard.press('Escape').catch(() => {})
      await page.waitForTimeout(700)
    }

    const probe = await page.evaluate(PROBE_FN).catch((e) => null)
    if (probe) probe.fontsLoaded = fontsLoaded
    const problems = checkPage(probe, errors, resources)
    if (problems.length) failed++
    results.push({ page: c.name, ok: problems.length === 0, problems })
    console.log(`  ${problems.length ? '✗' : '✓'} ${c.name}`)
    for (const p of problems) console.log(`      ${p}`)
    await ctx.close()
  }
  await browser.close()

  if (argv.includes('--json')) {
    const outArg = argv[argv.indexOf('--json') + 1]
    const target = outArg && !outArg.startsWith('--')
      ? outArg : resolve(WEB, 'reports/public-render-check.json')
    mkdirSync(dirname(target), { recursive: true })
    writeFileSync(target, JSON.stringify({ base: RESOLVED_BASE, at: new Date().toISOString(), results }, null, 2))
    console.log(`\nJSON → ${target}`)
  }

  console.log(`\n${failed === 0 ? '✅' : '❌'} 公开页渲染检查：${results.length - failed}/${results.length} 通过`)
  process.exit(failed === 0 ? 0 : 1)
}

main().catch((e) => { console.error(String(e.message || e)); process.exit(2) })
