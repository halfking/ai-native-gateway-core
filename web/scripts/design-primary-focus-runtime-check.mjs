#!/usr/bin/env node
/**
 * design-primary-focus-runtime-check.mjs —— 「主色只留给主动作」的【运行时】核对（第 20 增量）
 *
 * 为什么要有这道工具（它和 design-primary-focus-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * `design-primary-focus-guard.mjs` 读的是**源码文本**；
 * 本工具读的是**浏览器里真实渲染出来的样子**。
 *
 * ★ 源码门看得见「声明在数据行里」，但它**看不见渲染出来几个**：
 *   `views/ModelsView.vue` 只有**一处** `<button class="btn btn-primary btn-sm">`，
 *   实测渲染出 **53 个**蓝底「查看详情」按钮 —— 这才是用户看到的东西。
 *   ⇒ 反过来，源码门看不见的还有：动态 class（`:class="cond ? 'btn-primary' : 'btn-ghost'"`）
 *   与组件内封装的按钮。
 *
 * ★ 所以两把尺是**同一个口径的两个观测层**：
 *   源码层：「实心主色**声明**在数据行里」；
 *   运行时层：「实心主色**渲染**在数据行里」。
 *
 * 判据（与源码门同一个口径）
 * ──────────────────────────
 *   对每个**可见的实心主色按钮**：
 *     ① 在焦点区域（模态 / 抽屉）内          ⇒ 豁免（那是浮层自己的主动作）
 *     ② 在**数据行**（td/th/tr/li/Element 表格行）内 ⇒ 违规
 *     ③ 在**重复网格**内，且该网格**之外**还有别的实心主色 ⇒ 违规（在与别的动作抢主色）
 *     ④ 其余                                ⇒ 合规（页面级主动作 / 唯一的刻意区域）
 *
 * ★ 「可见」的口径：**已渲染**（`getClientRects().length > 0`），**不要求在视口内** ——
 *   「53 个蓝按钮」的危害在于**这一页**长这样，不在于首屏能不能看全。
 *
 * 用法
 * ────
 *   node scripts/design-primary-focus-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-primary-focus-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-primary-focus-runtime-check.mjs --pages /models,/examples
 *   node scripts/design-primary-focus-runtime-check.mjs --json out.json
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（用于无头模式 + CDP）。
 *   **没有 Chrome 时本工具不产出任何判定** ⇒ 打印 ⏭ SKIP 并说明「未测量」，
 *   ★ 绝不把「没量到」写成「通过」——两者必须分开（LESSONS.md §450 形态八）。
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
  '/models', '/examples', '/chat', '/tenant/pricing',
  '/probe-health/detail', '/keys', '/routing-v2',
]

// ── 纯函数：元素描述符 → 判定 ────────────────────────────────────────────
//
// ★ 「把判据搬到另一个观测层时，上下文依赖要一起搬」（LESSONS.md §456）：
//   下面的判定全部吃**描述符**（纯数据），不碰 DOM ⇒
//   ① 自证可以在没有浏览器的环境里覆盖**判定逻辑本身**；
//   ② 浏览器探针只负责「把 DOM 翻译成描述符」，两边的口径不会各说各话。

/** 实心主色的类名（两种实现形态）。 */
export const SOLID_PRIMARY_CLASSES = ['btn-primary', 'el-button--primary']

/**
 * 焦点区域的判定：组件壳 + 语义角色（与源码门同一份名单）。
 *
 * ★★★ **标签名 ≠ 类名** —— 这是「把判据搬到另一个观测层」时最容易踩的坑：
 *   源码层看到的是 `<AppModal>`（**标签名** `appmodal`），
 *   运行时看到的是 `<div class="modal-overlay app-modal">`（**类名** `app-modal`）。
 *   ⇒ 只按标签名判 ⇒ 运行时**一个焦点区域都认不出**；
 *     只按类名判 ⇒ 源码层认不出。
 *   ⇒ 两层都按【标签名 + 类名 + 语义角色】三条一起判，名单保持一致。
 */
export const FOCUS_REGION_TAGS = ['el-dialog', 'el-drawer', 'appmodal', 'appdrawer']
export const FOCUS_REGION_CLASSES = ['app-modal', 'app-drawer', 'modal-panel', 'el-dialog', 'el-drawer']
export const FOCUS_REGION_ROLES = ['dialog', 'alertdialog']

/** 数据行的判定：原生表格行 / 列表项 / Element Plus 表格行。 */
export const DATA_ROW_TAGS = ['td', 'th', 'tr', 'li']
export const DATA_ROW_CLASSES = ['el-table__row', 'el-table__cell']

/**
 * 一个祖先的描述符。
 * @typedef {{ tag: string, cls: string, role: string, ariaModal: string }} AncestorDesc
 */

/**
 * 一个候选按钮的描述符。
 * @typedef {{ tag: string, cls: string, text: string, ancestors: AncestorDesc[],
 *   repeatedAncestorIndex: number | null }} SolidPrimaryDesc
 */

export function isSolidPrimaryClass(cls) {
  const tokens = String(cls || '').split(/\s+/).filter(Boolean)
  return SOLID_PRIMARY_CLASSES.some((c) => tokens.includes(c))
}

/** 祖先里有没有焦点区域（组件壳标签 / 组件壳类名 / 语义角色 / `aria-modal`）？ */
export function isFocusRegion(ancestors) {
  return ancestors.some((a) => {
    const clsTokens = String(a.cls || '').split(/\s+/).filter(Boolean)
    return (
      FOCUS_REGION_TAGS.includes(a.tag) ||
      FOCUS_REGION_CLASSES.some((c) => clsTokens.some((t) => matchesClassBase(t, c))) ||
      FOCUS_REGION_ROLES.includes(String(a.role || '').toLowerCase()) ||
      String(a.ariaModal || '').toLowerCase() === 'true'
    )
  })
}

/**
 * 类名是否命中「基名」？
 * ★ 认 BEM 派生（`app-modal__panel` / `app-modal--fullscreen` / `modal-panel__head`），
 *   但**不认前缀相同的其它词**（`app-modalish` 不算）——
 *   ⇒ 判别式是「基名 + 分隔符 + 派生」，不是「startsWith」。
 */
export function matchesClassBase(token, base) {
  if (token === base) return true
  if (!token.startsWith(base)) return false
  const rest = token.slice(base.length)
  return rest.startsWith('__') || rest.startsWith('--')
}

/** 祖先里有没有数据行？ */
export function isDataRow(ancestors) {
  return ancestors.some(
    (a) =>
      DATA_ROW_TAGS.includes(a.tag) ||
      DATA_ROW_CLASSES.some((c) => String(a.cls || '').split(/\s+/).includes(c)),
  )
}

/**
 * 裁决一个候选。
 * @param {{ inFocusRegion: boolean, inDataRow: boolean, inRepeatedGrid: boolean, competingOutside: number }} args
 * @returns {'exempt-focus' | 'violation' | 'ok'}
 */
export function judgePlacement({
  inFocusRegion,
  inDataRow,
  inRepeatedGrid,
  competingOutside,
}) {
  if (inFocusRegion) return 'exempt-focus'
  if (inDataRow) return 'violation'
  if (inRepeatedGrid && competingOutside > 0) return 'violation'
  return 'ok'
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
  const anc = (tag, cls = '', role = '', ariaModal = '') => ({ tag, cls, role, ariaModal })

  t('实心主色的类名：仓内自有类 + Element Plus 类', () => {
    eq(isSolidPrimaryClass('btn btn-primary btn-sm'), true, '自有类')
    eq(isSolidPrimaryClass('el-button el-button--primary'), true, 'EP 类')
    eq(isSolidPrimaryClass('btn btn-ghost'), false, 'ghost 不是实心')
    eq(isSolidPrimaryClass('btn btn-primary-ghost'), false, '前缀匹配不算')
  })

  t('焦点区域：组件壳 / 语义角色 / aria-modal 三种形态都认', () => {
    eq(isFocusRegion([anc('appmodal')]), true, 'AppModal')
    eq(isFocusRegion([anc('div', '', 'dialog')]), true, 'role=dialog')
    eq(isFocusRegion([anc('div', '', '', 'true')]), true, 'aria-modal')
    eq(isFocusRegion([anc('div', 'modal-panel')]), true, '自制模态面板（类名）')
    eq(isFocusRegion([anc('div', 'modal-overlay app-modal')]), true, 'AppModal 运行时类名')
    eq(isFocusRegion([anc('div', 'app-drawer__panel')]), true, 'AppDrawer 运行时类名（后缀）')
    eq(isFocusRegion([anc('div', 'app-modalish')]), false, '前缀相同不算')
    eq(isFocusRegion([anc('section')]), false, '普通容器不算')
  })

  t('数据行：td / tr / li / Element 表格行 都认', () => {
    eq(isDataRow([anc('td')]), true, 'td')
    eq(isDataRow([anc('tr')]), true, 'tr')
    eq(isDataRow([anc('li')]), true, 'li')
    eq(isDataRow([anc('div', 'el-table__row')]), true, 'EP 表格行')
    eq(isDataRow([anc('div', 'section-card')]), false, '区块容器不算')
  })

  t('★ 对照：焦点区域内的数据行 ⇒ 豁免（对话框里的表格行不判）', () => {
    const v = judgePlacement({
      inFocusRegion: true,
      inDataRow: true,
      inRepeatedGrid: true,
      competingOutside: 3,
    })
    eq(v, 'exempt-focus', 'verdict')
  })

  t('数据行内的实心主色 ⇒ 违规', () => {
    const v = judgePlacement({
      inFocusRegion: false,
      inDataRow: true,
      inRepeatedGrid: true,
      competingOutside: 0,
    })
    eq(v, 'violation', 'verdict')
  })

  t('★ 重复网格内 + 网格外无别的实心主色 ⇒ 合规（刻意区域）', () => {
    const v = judgePlacement({
      inFocusRegion: false,
      inDataRow: false,
      inRepeatedGrid: true,
      competingOutside: 0,
    })
    eq(v, 'ok', 'verdict')
  })

  t('★ 重复网格内 + 网格外还有别的实心主色 ⇒ 违规（在抢主色）', () => {
    const v = judgePlacement({
      inFocusRegion: false,
      inDataRow: false,
      inRepeatedGrid: true,
      competingOutside: 1,
    })
    eq(v, 'violation', 'verdict')
  })

  t('页面级主动作（不在任何重复网格内）⇒ 合规', () => {
    const v = judgePlacement({
      inFocusRegion: false,
      inDataRow: false,
      inRepeatedGrid: false,
      competingOutside: 5,
    })
    eq(v, 'ok', 'verdict')
  })

  t('★ 判据不含全局状态（同一输入两次结果一致）', () => {
    const args = { inFocusRegion: false, inDataRow: true, inRepeatedGrid: true, competingOutside: 2 }
    eq(judgePlacement(args), judgePlacement(args), '两次结果')
  })

  t('★ 判据对「数据行 vs 非数据行」敏感（改一处必须改结论）', () => {
    const a = judgePlacement({ inFocusRegion: false, inDataRow: true, inRepeatedGrid: false, competingOutside: 0 })
    const b = judgePlacement({ inFocusRegion: false, inDataRow: false, inRepeatedGrid: false, competingOutside: 0 })
    eq(a === b, false, '两个结论必须不同')
  })

  const failed = cases.filter((c) => !c.ok)
  for (const c of cases) console.log(`  ${c.ok ? '✓' : '✘'} ${c.name}${c.ok ? '' : ' —— ' + c.msg}`)
  console.log(
    `\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`,
  )
  return failed.length === 0
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
//
// ★ 探针只做一件事：把 DOM 翻译成**描述符**（纯数据）⇒ 判定交给上面的纯函数。
const PROBE = String.raw`
(() => {
  const SOLID = ${JSON.stringify(SOLID_PRIMARY_CLASSES)};
  const FOCUS_TAGS = ${JSON.stringify(FOCUS_REGION_TAGS)};
  const FOCUS_CLASSES = ${JSON.stringify(FOCUS_REGION_CLASSES)};
  const FOCUS_ROLES = ${JSON.stringify(FOCUS_REGION_ROLES)};
  const ROW_TAGS = ${JSON.stringify(DATA_ROW_TAGS)};
  const ROW_CLASSES = ${JSON.stringify(DATA_ROW_CLASSES)};

  const clsOf = (el) => {
    const c = el.className;
    if (!c) return '';
    return (typeof c === 'string' ? c : (c.baseVal !== undefined ? c.baseVal : Array.from(c).join(' '))) || '';
  };
  const sig = (el) => el.tagName + '|' + clsOf(el);

  const all = Array.from(document.querySelectorAll('button, a, [role="button"], .el-button'));
  const cands = [];
  for (const el of all) {
    const cls = clsOf(el);
    const tokens = cls.split(/\s+/).filter(Boolean);
    if (!SOLID.some((c) => tokens.includes(c))) continue;
    const rects = el.getClientRects();
    if (!rects || rects.length === 0) continue;              // 未渲染 ⇒ 不可见
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;
    if (el.hasAttribute('disabled') || cs.opacity === '0') continue;

    // 祖先描述符
    const ancestors = [];
    let repeatedAncestorIndex = null;
    let p = el.parentElement;
    let idx = 0;
    while (p && p !== document.body) {
      const a = {
        tag: p.tagName.toLowerCase(),
        cls: clsOf(p),
        role: p.getAttribute('role') || '',
        ariaModal: p.getAttribute('aria-modal') || '',
      };
      ancestors.push(a);
      // 「重复项」：它的父下有 ≥2 个同签名兄弟
      if (repeatedAncestorIndex === null) {
        const gp = p.parentElement;
        if (gp) {
          const sibs = Array.from(gp.children).filter((c) => sig(c) === sig(p));
          if (sibs.length >= 2) repeatedAncestorIndex = idx;
        }
      }
      p = p.parentElement;
      idx++;
    }
    const text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim();
    cands.push({ tag: el.tagName.toLowerCase(), cls, text, ancestors, repeatedAncestorIndex });
  }

  // 分组：按「重复项」分组（没有重复项的 ⇒ 每个自成一组，id = 'solo:'+index）
  const groupKey = (c) => (c.repeatedAncestorIndex === null ? 'solo:' + c.tag + '|' + c.cls : 'grid:' + c.repeatedAncestorIndex + '|' + (c.ancestors[c.repeatedAncestorIndex] ? c.ancestors[c.repeatedAncestorIndex].cls : ''));
  const groups = {};
  for (const c of cands) {
    const k = groupKey(c);
    if (!groups[k]) groups[k] = [];
    groups[k].push(c);
  }
  const groupKeys = Object.keys(groups);

  const results = cands.map((c) => {
    const inFocus = c.ancestors.some((a) => {
      const toks = String(a.cls || '').split(/\s+/).filter(Boolean);
      const baseHit = (tok, base) => tok === base || (tok.startsWith(base) && (tok.slice(base.length).startsWith('__') || tok.slice(base.length).startsWith('--')));
      return FOCUS_TAGS.includes(a.tag) || FOCUS_CLASSES.some((x) => toks.some((tk) => baseHit(tk, x))) ||
        FOCUS_ROLES.includes(String(a.role || '').toLowerCase()) ||
        String(a.ariaModal || '').toLowerCase() === 'true';
    });
    const inRow = c.ancestors.some((a) =>
      ROW_TAGS.includes(a.tag) || ROW_CLASSES.some((x) => String(a.cls || '').split(/\s+/).includes(x)));
    const inGrid = c.repeatedAncestorIndex !== null;
    const myKey = groupKey(c);
    const competingOutside = cands.filter((o) => groupKey(o) !== myKey).length;
    return { tag: c.tag, cls: c.cls.slice(0, 90), text: c.text.slice(0, 40), inFocus, inRow, inGrid, competingOutside, ancestors: c.ancestors.length };
  });

  // 按可见文案分组（用于报告「同一个主色按钮出现了几次」）
  const byText = {};
  for (const c of cands) { const k = c.text || '(无文字)'; byText[k] = (byText[k] || 0) + 1; }

  return { candidates: cands.length, results, byText, groups: groupKeys.length };
})()
`

// ── CDP 驱动 ────────────────────────────────────────────────────────────
async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'pf-runtime-'))
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
      await sleep(2800)
      const themes = {}
      for (const theme of ['light', 'dark']) {
        await evaluate(
          `(() => { document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); return true })()`,
        )
        await sleep(500)
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

console.log(`运行时主色核对 —— ${BASE} · ${pages.length} 页 × 2 主题 · Chrome: ${chrome}\n`)
const report = await runChrome(9335 + (Math.floor(process.pid) % 50), pages)

let violations = 0
let scanned = 0
const uncovered = []
for (const pg of report.pages) {
  for (const theme of ['light', 'dark']) {
    const t = pg.themes[theme]
    const judged = t.results.map((r) => ({
      ...r,
      verdict: judgePlacement({
        inFocusRegion: r.inFocus,
        inDataRow: r.inRow,
        inRepeatedGrid: r.inGrid,
        competingOutside: r.competingOutside,
      }),
    }))
    const bad = judged.filter((j) => j.verdict === 'violation')
    violations += bad.length
    scanned += 1
    console.log(
      `[${pg.page}/${theme}] 实心主色 ${t.candidates} 个 · 文案分组 ${Object.keys(t.byText).length} 个 · 违规 ${bad.length}`,
    )
    // ★★★ 「量具挂起」是【读不到】，不是【读到了 0】：
    //   一页上**一个实心主色都没有** ⇒ 这一族在该页**根本没渲染出来**
    //   （数据没到 / 未登录 / 状态没进到）⇒ 必须单独标出来，
    //   否则「0 违规」会被读成「这一族验过了」。
    if (t.candidates === 0) {
      uncovered.push({ page: pg.page, theme })
      console.log('   ⚠ 该页未渲染出任何实心主色 ⇒ **这一族未被覆盖**（不是「通过了」）')
    }
    for (const k of Object.entries(t.byText)) {
      if (k[1] >= 2) console.log(`   · 同一文案出现 ${k[1]} 次：「${k[0]}」`)
    }
    for (const v of bad.slice(0, 6)) {
      console.log(`   ✘ ${v.tag}.${v.cls} 「${v.text}」 dataRow=${v.inRow} grid=${v.inGrid} 竞争=${v.competingOutside}`)
    }
  }
}
if (jsonOut) writeFileSync(jsonOut, JSON.stringify(report, null, 2))

console.log(`\n════ 汇总 ════`)
console.log(`扫描 ${scanned} 次（页 × 主题）· 违规 ${violations} 处 · 页级失败 ${report.errors.length} 个`)
if (report.errors.length) for (const e of report.errors) console.log(`   ✘ ${e.page}: ${e.error}`)
if (uncovered.length) {
  console.log(`⚠ 未覆盖 ${uncovered.length} 次（页 × 主题）：该页没渲染出实心主色 ⇒ 这些族**未测到**，不是「通过」`)
  for (const u of uncovered) console.log(`   · ${u.page} / ${u.theme}`)
}
if (violations === 0 && report.errors.length === 0 && scanned > 0) {
  console.log('✅ 运行时的每一枚实心主色都在该待的地方 —— PASS')
  if (uncovered.length) {
    console.log('   ⚠ 但有上列的族**未被覆盖** —— 结论只对「量到过的那些族」成立。')
  }
  process.exit(0)
}
console.log('❌ 运行时存在「主色被重复消费」或页面没测到 —— FAIL')
process.exit(1)
