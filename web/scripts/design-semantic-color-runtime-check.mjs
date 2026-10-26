#!/usr/bin/env node
/**
 * design-semantic-color-runtime-check.mjs —— 「颜色只承载语义，不承载分组」的【运行时】核对（第 21 增量）
 *
 * 为什么要有这道工具（它和 design-semantic-color-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * `design-semantic-color-guard.mjs` 读的是**源码里的 CSS 声明**；
 * 本工具读的是**浏览器里真实渲染出来的颜色**。
 *
 * ★ 源码门看得见「这条声明混了状态色」，但它**看不见用户看到的那个颜色**：
 *   ① 计算后的背景色要经过层叠、继承、主题变量才知道；
 *   ② 组件内部的封装样式（子组件自带 `<style>`）在父组件源码里根本看不到；
 *   ③ 「同屏里两个分组长得不一样」这件事，只有在**同一父容器下**比对才看得出来。
 *
 * ★ 所以两把尺是**同一个口径的两个观测层**：
 *   源码层：「这条声明把裸颜色令牌混进了控件组容器的底色」；
 *   运行时层：「这两个同父的控件组容器渲染出来的底色不一样」。
 *
 * ★★★ 运行时判据（与源码门同一口径）：
 *
 *   **同一个父容器下的「控件组容器」，它们的背景色必须一致。**
 *   —— 颜色不一致 ⇒ 这些颜色在做「区分」而不是在表达状态。
 *   —— 这正是真仓 CSS 注释自认的：「不同功能簇使用不同色调做软区分」。
 *
 *   豁免（与源码门同一份名单）：容器自身带**状态词**（active / is-active / error …）
 *   ⇒ 它在表达自己的状态 ⇒ 颜色有意义。
 *
 * ★ 「可见」的口径：**已渲染**（`getClientRects().length > 0`），**不要求在视口内**。
 *
 * ★★ 工具性读数（给人看的，不参与判定）：**工具栏内控件组容器的背景色种类数**
 *   —— 第 21 增量前后，这个数从 4（维度/模式/请求类型/筛选 各一种色调）
 *      降到 1（四组共用中性底色）。这是「颜色不再做区分」最直接的可观测证据。
 *
 * 用法
 * ────
 *   node scripts/design-semantic-color-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-semantic-color-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-semantic-color-runtime-check.mjs --pages /,/models
 *   node scripts/design-semantic-color-runtime-check.mjs --json out.json
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
  '/', '/models', '/examples', '/chat', '/tenant/pricing',
  '/probe-health/detail', '/keys', '/routing-v2',
]

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 「把判据搬到另一个观测层时，上下文依赖要一起搬」（LESSONS.md §456）：
//   下面的判定全部吃**描述符**（纯数据），不碰 DOM ⇒
//   ① 自证可以在没有浏览器的环境里覆盖**判定逻辑本身**；
//   ② 浏览器探针只负责「把 DOM 翻译成描述符」，两边的口径不会各说各话。

/**
 * 状态词（与源码门 `semanticColor.ts` 的 `STATE_WORD` **同一份名单**）。
 * ★ 源码层是「静态类名 + 动态绑定键」，运行时层是「渲染后的 className」——
 *   词表必须一致，否则两层会对同一处给出不同结论。
 */
export const STATE_WORDS = [
  'active',
  'selected',
  'current',
  'checked',
  'disabled',
  'loading',
  'error',
  'danger',
  'warn',
  'warning',
  'success',
  'failed',
  'invalid',
]

/** 类名里是否命中状态词？ */
export function isStateCarrierClass(cls) {
  const tokens = String(cls || '').split(/\s+/).filter(Boolean)
  return tokens.some((t) => {
    const parts = t.split(/[-_]/)
    return parts.some((p) => STATE_WORDS.includes(p.toLowerCase()))
  })
}

/**
 * 裁决一组「同父的控件组容器」。
 *
 * @param {{cls: string, bg: string, stateCarrier: boolean}[]} containers
 * @returns {{ verdict: 'ok'|'conflict', offenders: object[] }}
 *
 * ★ 口径：**同一个父下的控件组容器，其背景色必须一致**。
 *   - 状态载体（自身带状态词）豁免 ⇒ 它在表达自己的状态。
 *   - 背景色不同的那些就是「用颜色做区分」的现场 ⇒ 判红。
 *
 * ★★ **只比对「不透明」的背景**（`transparent` 不参与）：
 *   实测 `/routing-v2` 的 `.seg-tabs`（分段控件，**有**面）与 `.nav-chips`
 *   （链接行，**无**面）是同父兄弟 ⇒ 颜色不同。
 *   但那是「**有没有面**」的形态差异（分段控件本来就是一条带轨道的面），
 *   **不是**「用颜色做区分」⇒ 判红就是假红。
 *   ⇒ 判据要落在「**颜色在做区分**」上，而不是「形态完全一致」上。
 */
export function judgeGroupPalette(containers) {
  const live = containers.filter((c) => !c.stateCarrier)
  // 只比对不透明的背景（`transparent` = 没有面 ⇒ 形态差异，不是颜色差异）
  const opaque = live.filter((c) => c.bg !== 'transparent')
  const offenders = []
  for (let i = 0; i < opaque.length; i++) {
    for (let j = i + 1; j < opaque.length; j++) {
      if (opaque[i].bg !== opaque[j].bg) {
        offenders.push({ a: opaque[i], b: opaque[j] })
      }
    }
  }
  return { verdict: offenders.length ? 'conflict' : 'ok', offenders }
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
const PROBE = String.raw`
(() => {
  const STATE_WORDS = ${JSON.stringify(STATE_WORDS)};
  const CONTROL_SEL = 'button, a[href], input, [role="button"], [role="tab"], [role="radio"], [role="switch"], [role="checkbox"], [role="option"], [role="menuitemradio"], .el-radio-button, .el-checkbox-button, .el-segmented-item, .el-switch';
  const clsOf = (el) => {
    const c = el.className;
    if (!c) return '';
    return (typeof c === 'string' ? c : (c.baseVal !== undefined ? c.baseVal : Array.from(c).join(' '))) || '';
  };
  const visible = (el) => {
    const rects = el.getClientRects();
    if (!rects || rects.length === 0) return false;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    return true;
  };
  const isStateCarrier = (cls) => cls.split(/\s+/).filter(Boolean).some((t) =>
    t.split(/[-_]/).some((p) => STATE_WORDS.includes(p.toLowerCase())));
  // 背景色归一：alpha=0 ⇒ 'transparent'（= 没有面）
  const normBg = (raw) => {
    const s = String(raw || '').replace(/\s+/g, '');
    const m = s.match(/^rgba?\(([^)]+)\)$/);
    if (m) {
      const parts = m[1].split(',').map((x) => x.trim());
      if (parts.length === 4 && parseFloat(parts[3]) === 0) return 'transparent';
    }
    return s;
  };

  const containers = [];
  const all = Array.from(document.querySelectorAll('*'));
  let pid = 0;
  const idOf = (el) => {
    if (!el) return 'root';
    if (el.__scPid === undefined) el.__scPid = ++pid;
    return String(el.__scPid);
  };
  for (const el of all) {
    if (!visible(el)) continue;
    const kids = Array.from(el.children).filter((c) => c.matches(CONTROL_SEL));
    if (kids.length < 2) continue;
    const cs = getComputedStyle(el);
    const bgNorm = normBg(cs.backgroundColor);
    const cls = clsOf(el);
    containers.push({
      tag: el.tagName.toLowerCase(),
      cls: cls.slice(0, 90),
      bg: bgNorm,
      stateCarrier: isStateCarrier(cls),
      controlCount: kids.length,
      parentKey: idOf(el.parentElement),
    });
  }

  // 工具栏读数：**工具栏直接子级里那些「控件组容器」**的背景色种类数
  const toolbars = [];
  for (const tb of Array.from(document.querySelectorAll('[role="toolbar"], .stream-controls, .toolbar, [class*="controls"]'))) {
    if (!visible(tb)) continue;
    const groups = [];
    for (const k of Array.from(tb.children)) {
      const kids = Array.from(k.children).filter((c) => c.matches(CONTROL_SEL));
      if (kids.length < 2) continue;
      const cs = getComputedStyle(k);
      groups.push(normBg(cs.backgroundColor));
    }
    const uniq = [...new Set(groups)];
    toolbars.push({ cls: clsOf(tb).slice(0, 70), groups: groups.length, distinctBg: uniq.length, bgs: uniq.slice(0, 8) });
  }

  return { containers, toolbars };
})()
`

// ── CDP 驱动 ────────────────────────────────────────────────────────────
async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'sc-runtime-'))
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

// ── 自证 ────────────────────────────────────────────────────────────────
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
  const c = (cls, bg, stateCarrier = false) => ({ cls, bg, stateCarrier })

  t('状态载体识别：类名里带状态词就算', () => {
    eq(isStateCarrierClass('turn-card active'), true, 'active')
    eq(isStateCarrierClass('cp__row is-active'), true, 'is-active')
    eq(isStateCarrierClass('alert-error'), true, 'error')
    eq(isStateCarrierClass('control-group'), false, '普通类名')
    eq(isStateCarrierClass('control-group control-group--mode'), false, 'modifier 不算状态')
  })

  t('★ 同父的分组容器背景色一致 ⇒ 合规', () => {
    const r = judgeGroupPalette([c('a', 'rgb(234,240,255)'), c('b', 'rgb(234,240,255)')])
    eq(r.verdict, 'ok', 'verdict')
  })

  t('★ 同父的分组容器背景色不同 ⇒ 违规（颜色在做区分）', () => {
    const r = judgeGroupPalette([c('a', 'rgb(234,240,255)'), c('b', 'rgb(255,240,230)')])
    eq(r.verdict, 'conflict', 'verdict')
    eq(r.offenders.length, 1, 'offender 数')
  })

  t('★ 状态载体豁免：带状态词的那个不参与比对', () => {
    const r = judgeGroupPalette([
      c('a', 'rgb(234,240,255)'),
      c('b', 'rgb(255,240,230)', true),
    ])
    eq(r.verdict, 'ok', 'verdict')
  })

  t('★ 豁免只豁免「它自己」：两个状态载体颜色不同也不判红', () => {
    const r = judgeGroupPalette([
      c('a', 'rgb(1,1,1)', true),
      c('b', 'rgb(2,2,2)', true),
    ])
    eq(r.verdict, 'ok', 'verdict')
  })

  t('★ 一个透明一个不透明 ⇒ 合规（「有没有面」的形态差异，不是颜色差异）', () => {
    const r = judgeGroupPalette([c('seg-tabs', 'rgb(234,240,255)'), c('nav-chips', 'transparent')])
    eq(r.verdict, 'ok', 'verdict')
    eq(r.offenders.length, 0, 'offender 数')
  })

  t('★ 两个都是不透明但颜色不同 ⇒ 违规', () => {
    const r = judgeGroupPalette([c('a', 'rgb(234,240,255)'), c('b', 'rgb(255,240,230)')])
    eq(r.verdict, 'conflict', 'verdict')
  })

  t('★ 只有一个容器 ⇒ 合规（无从比较）', () => {
    eq(judgeGroupPalette([c('a', 'rgb(1,1,1)')]).verdict, 'ok', 'verdict')
  })

  t('★ 判据不含全局状态（同一输入两次结果一致）', () => {
    const args = [c('a', 'rgb(1,1,1)'), c('b', 'rgb(2,2,2)')]
    eq(judgeGroupPalette(args).verdict, judgeGroupPalette(args).verdict, '两次结果')
  })

  t('★ 判据对「颜色是否相同」敏感（改一个像素即改结论）', () => {
    const same = judgeGroupPalette([c('a', 'rgb(1,1,1)'), c('b', 'rgb(1,1,1)')])
    const diff = judgeGroupPalette([c('a', 'rgb(1,1,1)'), c('b', 'rgb(1,1,2)')])
    eq(same.verdict === diff.verdict, false, '两个结论必须不同')
  })

  t('★ 三个容器只有两种颜色 ⇒ 判红，并报出「两种颜色各自的容器」（两对交叉）', () => {
    const r = judgeGroupPalette([
      c('a', 'rgb(1,1,1)'),
      c('b', 'rgb(1,1,1)'),
      c('c', 'rgb(9,9,9)'),
    ])
    eq(r.verdict, 'conflict', 'verdict')
    // a-b 同色不报；a-c、b-c 两对交叉 ⇒ 2 对
    eq(r.offenders.length, 2, 'offender 数')
    eq(r.offenders.every((o) => o.a.bg !== o.b.bg), true, '每对都必须颜色不同')
  })

  const failed = cases.filter((c) => !c.ok)
  for (const c of cases) console.log(`  ${c.ok ? '✓' : '✘'} ${c.name}${c.ok ? '' : ' —— ' + c.msg}`)
  console.log(
    `\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`,
  )
  return failed.length === 0
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
const port = 9455

const report = await runChrome(port, pages)

// ── 裁决 ──
let totalConflicts = 0
let totalContainers = 0
const lines = []
for (const p of report.pages) {
  const parts = []
  for (const theme of ['light', 'dark']) {
    const data = p.themes[theme]
    if (!data) {
      parts.push(`${theme}: 未测量`)
      continue
    }
    const byParent = {}
    for (const c of data.containers) {
      ;(byParent[c.parentKey] ||= []).push(c)
    }
    const conflicts = []
    for (const [pk, list] of Object.entries(byParent)) {
      const r = judgeGroupPalette(list)
      if (r.verdict === 'conflict') conflicts.push({ parentKey: pk, list, offenders: r.offenders })
    }
    totalConflicts += conflicts.length
    totalContainers += data.containers.length
    const toolbarPalette = (data.toolbars || [])
      .map((tb) => `${tb.cls || '(无类名)'}→${tb.distinctBg}`)
      .join(' | ')
    parts.push(
      `${theme}: 控件组容器 ${data.containers.length} 个 / 冲突 ${conflicts.length} 处` +
        (toolbarPalette ? ` · 工具栏配色种类 ${toolbarPalette}` : ''),
    )
    for (const c of conflicts) {
      const detail = c.offenders
        .map((o) => `${o.a.cls || o.a.tag}(${o.a.bg}) ≠ ${o.b.cls || o.b.tag}(${o.b.bg})`)
        .join(' ; ')
      lines.push(`${p.page} [${theme}] 同父分组容器配色不一致：${detail}`)
    }
  }
  console.log(`\n=== ${p.page} ===`)
  for (const x of parts) console.log('  ' + x)
}
if (report.errors.length) {
  console.log('\n⚠️ 页面级失败（未测量）：')
  for (const e of report.errors) console.log(`  ${e.page}: ${e.error}`)
}
console.log(
  `\n读数：控件组容器 ${totalContainers} 个 · 同父配色冲突 ${totalConflicts} 处`,
)
for (const l of lines) console.log('  ✘ ' + l)

if (jsonOut) writeFileSync(jsonOut, JSON.stringify(report, null, 2))

if (totalConflicts) {
  console.error(`\nFAIL: ${totalConflicts} 处「同父的控件组容器配色不一致」⇒ 颜色在做区分而不是表达状态。`)
  process.exit(1)
}
console.log('\n✅ 同父的控件组容器配色一致 —— PASS')
