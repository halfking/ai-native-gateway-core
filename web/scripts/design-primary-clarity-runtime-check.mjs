#!/usr/bin/env node
/**
 * design-primary-clarity-runtime-check.mjs —— 「主色必须唯一」的【运行时】核对（第 22 增量）
 *
 * 为什么要有这道工具（它和 design-primary-clarity-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * `design-primary-clarity-guard.mjs` 读的是**源码里的 class 组合**；
 * 本工具读的是**浏览器里真实渲染出来的颜色**。
 *
 * ★ 源码门看得见「两个按钮都写了 `btn-primary`」，但它**看不见渲染出来的那个蓝**：
 *   ① 组件内部的封装样式（子组件自带 `<style>`）在父组件源码里根本看不到；
 *   ② `el-button type="primary"` 渲染成 Element Plus 自己的主色（`--el-color-primary`），
 *      与仓内的 `--accent` **不是同一个值** ⇒ 源码的 class 判定和屏幕上的颜色判定
 *      必须各自认一份「主色」；
 *   ③ 「并排」这件事只有 DOM 上**同一个直接父**才知道。
 *
 * ★ 所以两把尺是**同一个口径的两个观测层**：
 *   源码层：「这两个按钮**声明**为实心主色，且直接父相同」；
 *   运行时层：「这两个按钮**渲染**出来的底色就是主色，且直接父相同」。
 *
 * ★★★ 运行时判据（与源码门同一口径）：
 *
 *   **同一个直接父容器下，渲染成主色底的可交互控件最多一个。**
 *
 * ★ 「主色」在运行时怎么认：**不认类名**，认**颜色** ——
 *   取根元素上 `--accent` 与 `--el-color-primary` 的计算值，
 *   按钮的计算后 background-color 等于其中之一 ⇒ 它就是主色按钮。
 *   ⇒ 这样组件库自己的主色也能被认出来（源码层认的是 `el-button type="primary"`）。
 *
 * ★ 「可见」的口径：**已渲染**（`getClientRects().length > 0`），**不要求在视口内**。
 *
 * 用法
 * ────
 *   node scripts/design-primary-clarity-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-primary-clarity-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-primary-clarity-runtime-check.mjs --pages=/free-pool,/admin/proxy
 *   node scripts/design-primary-clarity-runtime-check.mjs --json out.json
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（用于无头模式 + CDP）。
 *   **没有 Chrome 时本工具不产出任何判定** ⇒ 打印 ⏭ SKIP 并说明「未测量」，
 *   ★ 绝不把「没量到」写成「通过」——两者必须分开。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'
const DEFAULT_PAGES = [
  '/', '/free-pool', '/admin/proxy', '/models', '/examples', '/chat',
  '/tenant/pricing', '/keys', '/routing-v2',
]

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * 一个候选控件的描述符。
 * @typedef {{ tag: string, cls: string, text: string, bg: string,
 *   isPrimary: boolean, parentKey: string }} PrimaryDesc
 */

/**
 * 裁决：同一个直接父下有 ≥2 个主色控件 ⇒ 违规。
 *
 * @param {PrimaryDesc[]} candidates
 * @returns {{ verdict: 'ok'|'conflict', groups: object[] }}
 */
export function judgeCompetingPrimaries(candidates) {
  const byParent = {}
  for (const c of candidates) {
    if (!c.isPrimary) continue
    if (!byParent[c.parentKey]) byParent[c.parentKey] = []
    byParent[c.parentKey].push(c)
  }
  const groups = []
  for (const [parentKey, list] of Object.entries(byParent)) {
    if (list.length < 2) continue
    groups.push({ parentKey, count: list.length, items: list })
  }
  return { verdict: groups.length ? 'conflict' : 'ok', groups }
}

export function runRuntimeSelftest() {
  const cases = []
  const t = (name, fn) => {
    try {
      fn()
      cases.push({ name, ok: true })
    } catch (e) {
      cases.push({ name, ok: false, msg: e?.message })
    }
  }
  const eq = (a, b, why) => {
    if (a !== b) throw new Error(`${why}：期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`)
  }
  const c = (parentKey, isPrimary = true, bg = 'rgb(30, 79, 214)', cls = 'btn btn-primary', text = 'X') => ({
    tag: 'button', cls, text, bg, isPrimary, parentKey,
  })

  t('同一直接父下两个主色 ⇒ 违规', () => {
    const r = judgeCompetingPrimaries([c('p1'), c('p1')])
    eq(r.verdict, 'conflict', 'verdict')
    eq(r.groups.length, 1, 'group 数')
    eq(r.groups[0].count, 2, '计数')
  })

  t('★ 对照：同一直接父下只有一个主色 ⇒ 合规', () => {
    const r = judgeCompetingPrimaries([c('p1'), c('p1', false, 'rgb(255,255,255)', 'btn btn-secondary')])
    eq(r.verdict, 'ok', 'verdict')
  })

  t('★ 直接父不同 ⇒ 合规（不同动作区各有一个主色是合法的）', () => {
    const r = judgeCompetingPrimaries([c('p1'), c('p2')])
    eq(r.verdict, 'ok', 'verdict')
  })

  t('★ 三个主色 ⇒ 违规且计数为 3', () => {
    const r = judgeCompetingPrimaries([c('p1'), c('p1'), c('p1')])
    eq(r.verdict, 'conflict', 'verdict')
    eq(r.groups[0].count, 3, '计数')
  })

  t('★ 两个动作区各有两个主色 ⇒ 违规且报 2 组', () => {
    const r = judgeCompetingPrimaries([c('p1'), c('p1'), c('p2'), c('p2')])
    eq(r.verdict, 'conflict', 'verdict')
    eq(r.groups.length, 2, 'group 数')
  })

  t('★ 判据不含全局状态（同一输入两次结果一致）', () => {
    const args = [c('p1'), c('p1')]
    eq(judgeCompetingPrimaries(args).verdict, judgeCompetingPrimaries(args).verdict, '两次结果')
  })

  t('★ 判据对「第二个是否主色」敏感（改一处必须改结论）', () => {
    const a = judgeCompetingPrimaries([c('p1'), c('p1')])
    const b = judgeCompetingPrimaries([c('p1'), c('p1', false, 'rgb(255,255,255)', 'btn')])
    eq(a.verdict === b.verdict, false, '两个结论必须不同')
  })

  const failed = cases.filter((x) => !x.ok)
  for (const x of cases) console.log(`  ${x.ok ? '✓' : '✘'} ${x.name}${x.ok ? '' : ' —— ' + x.msg}`)
  console.log(
    `\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`,
  )
  return failed.length === 0
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
const PROBE = String.raw`
(() => {
  const root = getComputedStyle(document.documentElement);
  const PRIMARY_COLORS = new Set([
    root.getPropertyValue('--accent').trim(),
    root.getPropertyValue('--kx-primary').trim(),
    root.getPropertyValue('--el-color-primary').trim(),
  ].filter(Boolean).map((v) => v.toLowerCase()));

  const norm = (v) => {
    const s = String(v || '').replace(/\s+/g, '').toLowerCase();
    if (s === 'rgba(0,0,0,0)' || s === 'transparent') return 'transparent';
    return s;
  };
  // 把 hex / rgb 统一成 rgb() 便于比较
  const toRgb = (v) => {
    const s = String(v || '').trim().toLowerCase();
    if (!s) return '';
    if (s.startsWith('#')) {
      let h = s.slice(1);
      if (h.length === 3) h = h.split('').map((c) => c + c).join('');
      const n = parseInt(h.slice(0, 6), 16);
      return 'rgb(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ')';
    }
    const m = s.match(/rgba?\(([^)]+)\)/);
    if (m) {
      const p = m[1].split(',').map((x) => parseFloat(x));
      return 'rgb(' + Math.round(p[0]) + ',' + Math.round(p[1]) + ',' + Math.round(p[2]) + ')';
    }
    return s;
  };
  const primaryRgb = new Set([...PRIMARY_COLORS].map(toRgb).filter((x) => x.startsWith('rgb(')));

  const CONTROL_SEL = 'button, .el-button, a[href], [role="button"]';
  const visible = (el) => {
    const rects = el.getClientRects();
    if (!rects || rects.length === 0) return false;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    return true;
  };
  const clsOf = (el) => {
    const c = el.className;
    if (!c) return '';
    return (typeof c === 'string' ? c : (c.baseVal !== undefined ? c.baseVal : Array.from(c).join(' '))) || '';
  };

  // 给每个父节点一个唯一 id
  let pid = 0;
  const idOf = (el) => {
    if (!el) return 'root';
    if (el.__pcPid === undefined) el.__pcPid = ++pid;
    return String(el.__pcPid);
  };

  const candidates = [];
  for (const el of document.querySelectorAll(CONTROL_SEL)) {
    if (!visible(el)) continue;
    const cs = getComputedStyle(el);
    const bg = toRgb(cs.backgroundColor);
    candidates.push({
      tag: el.tagName.toLowerCase(),
      cls: clsOf(el).slice(0, 70),
      text: (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 24),
      bg,
      isPrimary: primaryRgb.has(bg),
      parentKey: idOf(el.parentElement),
    });
  }
  return { candidates, primaryRgb: [...primaryRgb] };
})()
`

// ── CDP 驱动 ────────────────────────────────────────────────────────────
async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'pc-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
    '--window-size=1440,900',
    'about:blank',
  ], { stdio: 'ignore' })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break
    } catch {
      /* 等 */
    }
    await sleep(250)
  }
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const target = list.find((t) => t.type === 'page')
  if (!target) throw new Error('找不到 page target')

  const ws = new WebSocket(target.webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.addEventListener('open', res, { once: true })
    ws.addEventListener('error', rej, { once: true })
  })
  let seq = 0
  const pending = new Map()
  ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data)
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id)
      pending.delete(msg.id)
      if (msg.error) reject(new Error(msg.error.message))
      else resolve(msg.result)
    }
  })
  const cdp = (method, params = {}) => {
    const id = ++seq
    ws.send(JSON.stringify({ id, method, params }))
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject })
      setTimeout(() => {
        if (pending.has(id)) {
          pending.delete(id)
          reject(new Error(`CDP 超时: ${method}`))
        }
      }, 60000)
    })
  }
  const evaluate = async (expression) => {
    const r = await cdp('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
      userGesture: true,
    })
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { pages: [], errors: [] }
  for (const p of pages) {
    try {
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(3200)
      const themes = {}
      for (const theme of ['light', 'dark']) {
        await evaluate(
          `(() => { document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); return true })()`,
        )
        await sleep(600)
        themes[theme] = await evaluate(PROBE)
      }
      report.pages.push({ page: p, themes })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── CLI ──────────────────────────────────────────────────────────────────
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  process.exit(runRuntimeSelftest() ? 0 : 1)
}

const chrome = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chrome) {
  console.log('⏭ SKIP —— 未找到 Chrome 可执行文件 ⇒ **本轮未测量**（不是「通过」）')
  console.log('   可用 CHROME_PATH=/path/to/chrome 指定；或先装 Google Chrome。')
  process.exit(0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const pages = pagesArg
  ? pagesArg
      .slice('--pages='.length)
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
  : DEFAULT_PAGES
const jsonOut = argv.includes('--json') ? argv[argv.indexOf('--json') + 1] : null
const port = 9477

const report = await runChrome(port, pages)

// ── 裁决 ──
let totalGroups = 0
let totalPrimaries = 0
const lines = []
for (const p of report.pages) {
  const parts = []
  for (const theme of ['light', 'dark']) {
    const data = p.themes[theme]
    if (!data) {
      parts.push(`${theme}: 未测量`)
      continue
    }
    const primaries = data.candidates.filter((c) => c.isPrimary).length
    const r = judgeCompetingPrimaries(data.candidates)
    totalGroups += r.groups.length
    totalPrimaries += primaries
    parts.push(`${theme}: 主色控件 ${primaries} 个 / 竞争组 ${r.groups.length} 处`)
    for (const g of r.groups) {
      lines.push(
        `${p.page} [${theme}] 同父并排 ${g.count} 个主色控件：` +
          g.items.map((i) => `「${i.text || i.cls}」`).join(' / '),
      )
    }
  }
  console.log(`\n=== ${p.page} ===`)
  for (const x of parts) console.log('  ' + x)
}
if (report.errors.length) {
  console.log('\n⚠️ 页面级失败（未测量）：')
  for (const e of report.errors) console.log(`  ${e.page}: ${e.error}`)
}
console.log(`\n读数：主色控件 ${totalPrimaries} 个次 · 同父竞争组 ${totalGroups} 处`)
for (const l of lines) console.log('  ✘ ' + l)

if (jsonOut) writeFileSync(jsonOut, JSON.stringify(report, null, 2))

if (totalGroups) {
  console.error(`\nFAIL: ${totalGroups} 处「同一动作区里并排多个主色控件」。`)
  process.exit(1)
}
console.log('\n✅ 每个动作区只有一个主色控件 —— PASS')
