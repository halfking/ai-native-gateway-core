#!/usr/bin/env node
/**
 * design-nested-card-runtime-check.mjs —— 「卡片套卡片永远不对」的【运行时】核对（第 31 增量）
 *
 * 这道门治的是哪条明文
 * ────────────────────
 * DESIGN.md §6「卡片」小节的第三条硬禁令：
 *   「不许用『同尺寸卡片 + 图标 + 标题 + 文字』当页面骨架；
 *     卡片是懒容器；**卡片套卡片永远不对**。」
 * impeccable `skill/reference/craft-floor.md:25` 同源：
 *   "Same-size cards of icon plus heading plus text as the page structure.
 *    **Cards are the lazy container; nested cards are always wrong.**"
 *   （规则标签 skill-ban-identical-card-grids / skill-layout-cards-lazy）
 *
 * ★★ 为什么这条禁令**没有**被现有的门管住
 *   DESIGN.md §6 三条卡片禁令里，前两条（第 30 增量）已经被
 *   `design-metric-skeleton-runtime-check.mjs` 管住（「同尺寸卡网格当骨架」、
 *   「巨型数字 + 小标签当 hero 指标」），**这一条完全没有门**。
 *   而 `web/src/design/aiSmells.ts` 的 V1~V11 全是**静态 CSS 扫描**，
 *   「卡片套卡片」是**运行时结构属性**（一个盒子里面有没有卡）⇒ 静态扫描器看不见
 *   ⇒ ⇒ 与 icon-size / action-density / elevation / motion / primary-clarity /
 *     primary-focus / semantic-color / spacing-scale / type-scale 同族，
 *     做成【浏览器运行时尺】。
 *
 * ★★ 为什么判据落在【元素级】而不是页面级（第 30 增量通式一的直接应用）
 *   前两条禁令的原文都带限定语：「当**页面骨架**」「当 **hero 指标模板**」
 *   ⇒ 那两条必须落在页面级（第 30 增量因此收窄成「一页 >= 2 组」）。
 *   而这一条原文是「**永远不对**」，**没有任何限定语** ⇒ ⇒
 *   判据必须落在**元素级**：任意一张卡被套进一个盒子里就是违规。
 *   ⇒ 硬闸 = 任意一页出现 1 对「卡 ⊂ 盒」即判红；棘轮 = 逐页对数只降不升。
 *
 * ★★ 什么算「卡面 / 盒子」（判据宁可窄，不可宽）
 *   一个元素要算「卡面」，必须**同时**满足：
 *     ① 可见（不在隐藏子树里，宽 > 0、高 > 0）
 *     ② 不是交互控件（button / a / input / select / textarea / option / label）
 *     ③ 不是药丸（圆角 < 高度的一半）—— 药丸是 chip，不是卡
 *     ④ 够大：宽 >= 120px（★ 高度下限**分两级**，★ 见下面「盒子」与「卡」两节）
 *     ⑤ 有内边距：四边内边距之和 >= 8px
 *     ⑥ **有底色**（background-color 非透明）
 *     ⑦ **四边都有可见描边**（宽度 >= 1px **且** 颜色非透明）
 *   ★★ ⑥⑦ **必须同时成立**，这是本门最关键的一处设计：
 *     · 只看描边宽度不看描边颜色 ⇒ `1px solid transparent` 的**占位描边**会被当成描边
 *       （实测 `ModulesView.vue` 的 `.module-card` 就是占位描边 + 常态无底色 ⇒ 那是**列表行**不是卡）
 *     · 只看底色不看描边 ⇒ 页面外壳（有底色、无描边）会被当成盒子 ⇒ 整页每一张卡都成了「嵌套」
 *     · 两者都看 ⇒ 卡片在运行时必须**同时**具备「面」与「边」，拆掉任一它就不再是卡
 *       ⇒ ⇒ 门不会被「只去掉描边留底色」这种半修法骗过（★ 本轮刻意堵的洞）。
 *   另加一条：圆角 > 0（卡片是圆角的；页面外壳不是）。
 *
 * ★★ 什么算「盒子」（外层）—— **够大**才有资格装一批卡
 *   卡面 ∧ 宽 >= 120px ∧ **高 >= 48px**
 *   ★ 外层**不要求** class 名里有 `card` —— 被诟病的恰恰是「拿一个**没有卡片名**的盒子
 *     把一组卡装起来」（config-section / list-pane / bo-section / tri-section / section）。
 *
 * ★★ 什么算「卡」（内层）—— 仓内命名约定 + **可以很矮**
 *   卡面 ∧ class 名里含 **`card` 这个词**（`card` / `xxx-card` / `card-xxx`）
 *     ∧ 宽 >= 120px ∧ **高 >= 24px**
 *   ⇒ 这是**仓内自己的命名约定**（`.card` 全局类 + `*-card` 组件类），
 *     用它把「卡」与「同样有底有框的别的东西」分开：
 *       · `pre.code-block`（代码块）—— 有底有框，但它是**内容**，不是卡
 *       · `div.kpi-chip`（指标 chip）—— 药丸，本来就被 ③ 排除
 *       · `button.link-card` —— 名字里有 card，但它是**控件**，被 ② 排除
 *     ★ 判据宁可窄不可宽：class 名没写 card 的东西，本门**不管**它。
 *   ★★ **高度下限为什么是 24 而不是 48**：★ 矮卡也是卡 ——
 *     实测 `.reg-card` 高 **37.6px**、`.probe-card` 高 **37.6px 与 84px 两种**
 *     ⇒ 若一刀切用 48，★ **真违规会被判绿**（★ 本轮第一版就是这样漏掉 25 对的）。
 *
 * ★★ 什么算「嵌套」
 *   一张卡的**最近的卡面祖先**存在 ⇒ 就是一对嵌套。
 *   （取最近祖先 ⇒ 同一条链上只记一次，不重复计数。）
 *
 * 用法
 * ────
 *   node scripts/design-nested-card-runtime-check.mjs                    # 跑全部页面
 *   node scripts/design-nested-card-runtime-check.mjs --selftest          # 判据自证（不需要浏览器）
 *   node scripts/design-nested-card-runtime-check.mjs --pages=/,/models
 *   node scripts/design-nested-card-runtime-check.mjs --json=out.json
 *   node scripts/design-nested-card-runtime-check.mjs --update-baseline   # 人工降基线
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（无头模式 + CDP）。
 *   没有 Chrome 时本工具**不产出任何判定** ⇒ 打印 SKIP 并说明「未测量」，
 *   绝不把「没量到」写成「通过」。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const BASELINE = join(ROOT, 'scripts', 'design-nested-card-runtime-baseline.json')

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'

/**
 * 页面清单：**从 `src/router.ts` 自动派生**（全部静态路由）。
 *
 * ★ 与第 30 增量的门共用同一份派生逻辑（**判据/清单只存一份**）：
 *   写死的清单会**悄悄过期** —— 新增路由不在清单里 ⇒ 那一页无人看守，
 *   而「清单少了一页」这件事在门里**看不出来**。
 * ⇒ 带路径参数的路由（`:id`）与兜底路由（`pathMatch`）不在清单里。
 */
export const FALLBACK_PAGES = [
  '/',
  '/free-pool',
  '/admin/prompt-injection',
  '/admin/modules',
  '/admin/approval-config',
  '/admin/board-operational',
  '/system-monitor',
]

function derivePages() {
  const routerFile = join(ROOT, 'src', 'router.ts')
  if (!existsSync(routerFile)) return null
  try {
    const src = readFileSync(routerFile, 'utf8')
    const paths = [...src.matchAll(/path:\s*'([^']+)'/g)]
      .map((m) => m[1])
      .filter((x) => !x.includes(':') && !x.includes('pathMatch'))
    // ★ 必须去重：同一个 path 可能在 router.ts 里登记多次（★ 第 30 增量踩过：
    //   临时脚本按「出现次数」数，得 129/110，门自己的尺重数是 89 ⇒ 那 21 条是重复项，
    //   不是新路由 ⇒ 若信了它就会给基线补 21 条**编造数据**）。
    const uniq = [...new Set(paths)].sort()
    return uniq.length ? uniq : null
  } catch {
    return null
  }
}

export const DEFAULT_PAGES = derivePages() ?? FALLBACK_PAGES

/** 卡/盒子最小宽度（px）。 */
export const MIN_SURFACE_W = 120
/** 外层「盒子」最小高度（px）—— 盒子要装得下一批卡。 */
export const MIN_CONTAINER_H = 48
/** 内层「卡」最小高度（px）—— ★ 矮卡也是卡（实测 reg-card / probe-card 高约 34px）。 */
export const MIN_CARD_H = 24
/** 卡面四边内边距之和的下限（px）。 */
export const MIN_SURFACE_PAD = 8

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ id: string, parentId: string, cls: string, tag: string,
 *             w: number, h: number, radius: number, pad: number,
 *             hasBg: boolean, borderVisible: boolean,
 *             interactive: boolean, hidden: boolean }} SurfaceDesc
 */

/**
 * 判「这个元素是不是一张卡面」（**形状**判定，不含尺寸下限）。
 *
 * ★ 尺寸下限分两级（见 isContainer / isCard）：
 *   · 外层「盒子」必须够大（它要装得下一批卡）
 *   · 内层「卡」可以很矮（★ 实测 `.reg-card` 高约 34px、`.probe-card` 同量级 ——
 *     它们是货真价实的卡，只是矮 ⇒ **拿「高 >= 48」一刀切会把真违规判绿**）。
 *
 * @param {SurfaceDesc} d
 * @returns {boolean}
 */
export function isSurface(d) {
  if (!d || typeof d !== 'object') return false
  // ① 隐藏子树：用户看不到的东西不是卡。
  if (d.hidden) return false
  // ② 交互控件：按钮/链接/输入框有底有框，但它们是控件，不是卡。
  if (d.interactive) return false
  const h = Number(d.h)
  if (!Number.isFinite(h)) return false
  // ⑤ 内边距下限：卡片有呼吸空间。
  const pad = Number(d.pad)
  if (!Number.isFinite(pad) || pad < MIN_SURFACE_PAD) return false
  // ③ 药丸：圆角 >= 高度的一半 ⇒ chip，不是卡。
  const radius = Number(d.radius)
  if (Number.isFinite(radius) && radius >= h / 2) return false
  // 圆角 > 0：卡片是圆角的；页面外壳不是（★ 用来把「整页容器」排除掉）。
  if (!(radius > 0)) return false
  // ⑥ 有底色。
  if (d.hasBg !== true) return false
  // ⑦ 四边都有**可见**描边（宽度 >= 1px 且颜色非透明）。
  //    ★ 只看宽度不看颜色 ⇒ `1px solid transparent` 的占位描边会被误当成描边。
  if (d.borderVisible !== true) return false
  return true
}

/**
 * 判「这个元素是不是一个**外层盒子**」（卡面 + 够大 + 不像卡）。
 *
 * ★ 外层不要求 class 名里有 `card` —— 被诟病的恰恰是「拿一个**没有卡片名**的盒子
 *   把一组卡装起来」（config-section / list-pane / bo-section / tri-section / section）。
 *
 * @param {SurfaceDesc} d
 * @returns {boolean}
 */
export function isContainer(d) {
  if (!isSurface(d)) return false
  const w = Number(d.w)
  const h = Number(d.h)
  if (!Number.isFinite(w) || !Number.isFinite(h)) return false
  if (w < MIN_SURFACE_W || h < MIN_CONTAINER_H) return false
  return true
}

/**
 * 判「这个元素是不是一张**卡**」（卡面 + 仓内命名约定命中 `card` 这个词 + 够大）。
 *
 * ★ 高卡可以很矮（矮卡也是卡，★ 见 isSurface 的注释）。
 *
 * @param {SurfaceDesc} d
 * @returns {boolean}
 */
export function isCard(d) {
  if (!isSurface(d)) return false
  if (!hasCardWordInClass(d.cls)) return false
  const w = Number(d.w)
  const h = Number(d.h)
  if (!Number.isFinite(w) || !Number.isFinite(h)) return false
  if (w < MIN_SURFACE_W || h < MIN_CARD_H) return false
  return true
}

/**
 * ★★ class 名里含不含 `card` 这个词（**仓内命名约定**）。
 *
 * ★ 这里**从 class 字符串现算**，不让浏览器探针另算一遍 ——
 *   否则同一条判据会有两份实现，探针改了就悄悄分叉，而且**正则不被自证覆盖**。
 *
 * 命中：`card` / `xxx-card` / `card-xxx` / `stat-card app-stat-card`
 * 不命中：`discard-ticket`（card 只是别的词的一部分）
 */
export function hasCardWordInClass(cls) {
  return /(^|[\s_-])card([\s_-]|$)/.test(String(cls ?? ''))
}

/**
 * 找出所有「卡 ⊂ 盒」的嵌套对。
 *
 * ★ 取**最近**的卡面祖先 ⇒ 同一条祖先链上只记一次。
 *
 * @param {SurfaceDesc[]} descs
 * @returns {{cardId: string, cardCls: string, containerId: string, containerCls: string}[]}
 */
export function findNestedCards(descs) {
  const list = Array.isArray(descs) ? descs : []
  const byId = new Map()
  for (const d of list) {
    if (d && typeof d === 'object' && d.id != null) byId.set(String(d.id), d)
  }
  const out = []
  for (const card of list) {
    if (!isCard(card)) continue
    let pid = card.parentId == null ? 'ROOT' : String(card.parentId)
    let nearest = null
    while (pid && pid !== 'ROOT') {
      const anc = byId.get(pid)
      if (!anc) break
      if (isContainer(anc)) {
        nearest = anc
        break
      }
      pid = anc.parentId == null ? 'ROOT' : String(anc.parentId)
    }
    if (nearest) {
      out.push({
        cardId: String(card.id),
        cardCls: String(card.cls ?? ''),
        containerId: String(nearest.id),
        containerCls: String(nearest.cls ?? ''),
      })
    }
  }
  return out
}

/**
 * 页面级判定：这页有没有「卡片套卡片」。
 *
 * ★ 事实源是「**永远不对**」⇒ 没有限定语 ⇒ **1 对就判红**（元素级禁令）。
 *
 * @param pairs findNestedCards 的结果
 * @returns {{ pairs: number, isNested: boolean }}
 */
export function judgePageNested(pairs) {
  const list = Array.isArray(pairs) ? pairs : []
  return { pairs: list.length, isNested: list.length > 0 }
}

/**
 * ★★ **退出码的唯一依据**（纯函数 + 有自证；★ 见下面 selftest 的 S-NC40~S-NC43）。
 *
 * @param pages [{page, isNested, pairs:[...]}]
 */
export function collectHardGateViolations(pages) {
  const list = Array.isArray(pages) ? pages : []
  const out = []
  for (const p of list) {
    if (!p || p.isNested !== true) continue
    for (const pair of p.pairs ?? []) out.push({ page: p.page, ...pair })
  }
  return out
}

/**
 * 棘轮：各页「嵌套对数」不得超过基线（只许降不许升）。
 *
 * @param readings { pages: { page: { nestedCards: number } } }
 * @param baseline { nestedCards: {page:number} }
 */
export function compareToBaseline(readings, baseline) {
  const regressions = []
  const pages = readings?.pages ?? {}
  for (const [page, r] of Object.entries(pages)) {
    const limit = baseline?.nestedCards?.[page]
    if (typeof limit !== 'number') {
      regressions.push(`${page}：不在基线里（基线缺这一页 ⇒ 无法判棘轮）`)
      continue
    }
    if ((r.nestedCards ?? 0) > limit) {
      regressions.push(`${page}：嵌套卡片对数 ${limit} → ${r.nestedCards}（升高了）`)
    }
  }
  return regressions
}

/** ★ 基线能不能用（★ 防「基线没了 ⇒ 棘轮整个空转 ⇒ 假绿」）。 */
export function baselineUsable(baseline) {
  const s = baseline?.nestedCards
  if (!s || typeof s !== 'object') return false
  const values = Object.values(s)
  if (!values.length) return false
  return values.every((v) => typeof v === 'number' && Number.isFinite(v))
}

/** 量到的页面集合是否与基线一致（★ 页面集不一致 ⇒ 棘轮覆盖不全 ⇒ 判红）。 */
export function pagesMatchBaseline(readings, baseline) {
  const measured = Object.keys(readings?.pages ?? {})
  if (!measured.length) return false
  const known = new Set(Object.keys(baseline?.nestedCards ?? {}))
  return measured.every((p) => known.has(p))
}

/** 读基线（没有就返回 null —— 不自己造一个「全 0」的假基线）。 */
export function readBaseline() {
  if (!existsSync(BASELINE)) return null
  try {
    return JSON.parse(readFileSync(BASELINE, 'utf8'))
  } catch {
    return null
  }
}

// ── 浏览器探针 ──────────────────────────────────────────────────────────────
//
// ★ 只量**真实渲染后的盒子**（getBoundingClientRect / getComputedStyle）。
// ★ 祖先链用**元素 id** 表达（探针里用 WeakMap 发号），判定函数据此走链。

export const PROBE = `(() => {
  // ★ 注意：parseFloat('0') 是 0（falsy）⇒ 用 || 10 之类的兜底会把「描边宽度 0」
  //   「圆角 0」都变成 10 ⇒ 判据直接翻车 ⇒ 这里必须显式判 Number.isFinite。
  const num = (v) => {
    const n = parseFloat(v)
    return Number.isFinite(n) ? Math.round(n * 10) / 10 : 0
  }
  const INTERACTIVE = new Set(['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA', 'OPTION', 'LABEL'])
  const isTransparent = (c) => !c || c === 'transparent' || /^rgba\\(0,\\s*0,\\s*0,\\s*0\\)$/.test(c)
  const inHiddenSubtree = (el) => {
    let n = el
    while (n && n.nodeType === 1 && n !== document.documentElement) {
      const cs = getComputedStyle(n)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return true
      n = n.parentElement
    }
    return false
  }
  let nextId = 0
  const ids = new WeakMap()
  const idFor = (el) => {
    if (!ids.has(el)) ids.set(el, 'e' + (nextId++))
    return ids.get(el)
  }
  const descs = []
  for (const el of document.querySelectorAll('body *')) {
    // ★ 先发 id（元素与它的父），保证祖先链在「父元素被跳过」时也不断。
    const id = idFor(el)
    const parentId = el.parentElement ? idFor(el.parentElement) : 'ROOT'
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.display === 'contents') continue
    const r = el.getBoundingClientRect()
    const bg = cs.backgroundColor
    const hasBg = !!bg && !isTransparent(bg)
    // ★ 描边必须**可见**：宽度 >= 1px **且** 颜色非透明。
    const bv = (side) => num(cs['border' + side + 'Width']) >= 1 && !isTransparent(cs['border' + side + 'Color'])
    const borderVisible = bv('Top') && bv('Bottom') && bv('Left') && bv('Right')
    const cls = (el.getAttribute('class') || '').trim()
    descs.push({
      id, parentId,
      cls: cls.slice(0, 200),
      tag: el.tagName.toLowerCase(),
      w: num(r.width), h: num(r.height),
      radius: num(cs.borderTopLeftRadius),
      pad: num(cs.paddingTop) + num(cs.paddingRight) + num(cs.paddingBottom) + num(cs.paddingLeft),
      hasBg,
      borderVisible,
      interactive: INTERACTIVE.has(el.tagName),
      hidden: inHiddenSubtree(el),
    })
  }
  return JSON.stringify({ descs })
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'nested-card-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
    '--window-size=1440,1400',
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
      msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result)
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
    if (r.exceptionDetails) {
      throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    }
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { pages: [], errors: [] }
  for (const p of pages) {
    try {
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(3200)
      const probe = JSON.parse(await evaluate(PROBE))
      const descs = probe.descs ?? []
      const pairs = findNestedCards(descs)
      const verdict = judgePageNested(pairs)
      report.pages.push({
        page: p,
        totalDescs: descs.length,
        nestedPairs: verdict.pairs,
        isNested: verdict.isNested,
        pairs,
      })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── 判据自证 ────────────────────────────────────────────────────────────────
//
// ★ 必须**双向**：会判红 + 不误判各半 —— 否则恒绿门与恒红门同样没用。

export function runRuntimeSelftest() {
  const cases = []
  const check = (id, desc, expected, actual) =>
    cases.push({ id, desc, expected, actual, ok: expected === actual })

  /** 一张典型的「卡面」：有底色 + 四边可见描边 + 圆角 + 内边距。 */
  const surface = (over = {}) => ({
    id: 'c1',
    parentId: 'ROOT',
    cls: 'rule-card',
    tag: 'div',
    w: 320,
    h: 96,
    radius: 8,
    pad: 20,
    hasBg: true,
    borderVisible: true,
    interactive: false,
    hidden: false,
    ...over,
  })

  /** 一个典型的「盒子」：有底有框，但 class 名里没有 card。 */
  const box = (over = {}) => surface({ id: 'b1', cls: 'config-section', ...over })

  check('S-NC1', '★ 一张卡（底色 + 四边可见描边 + card 词）⇒ 是卡面', true, isSurface(surface()))
  check('S-NC2', '★ 只有底色、没有描边 ⇒ 不是卡面（★ 页面外壳就是这种）', false, isSurface(surface({ borderVisible: false })))
  check('S-NC3', '★ 只有描边、没有底色 ⇒ 不是卡面（★ 列表行就是这种）', false, isSurface(surface({ hasBg: false })))
  check('S-NC4', '★ 药丸（圆角 >= 高度的一半）⇒ 不是卡面', false, isSurface(surface({ radius: 999, h: 48 })))
  check('S-NC5', '★ 隐藏子树里的东西 ⇒ 不是卡面', false, isSurface(surface({ hidden: true })))
  check('S-NC6', '★ 交互控件（button / a / input）⇒ 不是卡面', false, isSurface(surface({ interactive: true })))
  check('S-NC7', '★ 太窄（宽 < 120）⇒ 不是盒子、也不是卡', false, isContainer(surface({ w: 100 })) && isCard(surface({ w: 100 })))
  check('S-NC8', '★ 矮（高 < 48）⇒ 不是**盒子**（盒子要装得下一批卡）', false, isContainer(surface({ h: 40 })))
  check(
    'S-NC8b',
    '★ 矮（高 34px）但**是卡**（★ 实测 .reg-card / .probe-card 就是这个高度 ⇒ 拿 48 一刀切会判绿）',
    true,
    isCard(surface({ h: 34 })),
  )
  check('S-NC8c', '★ 极矮（高 < 24）⇒ 连卡都不是（碎片）', false, isCard(surface({ h: 20 })))
  check('S-NC9', '★ 内边距不够（< 8）⇒ 不是卡面', false, isSurface(surface({ pad: 4 })))
  check('S-NC10', '★ 圆角为 0（页面外壳）⇒ 不是卡面', false, isSurface(surface({ radius: 0 })))
  check('S-NC11', 'undefined 入参 ⇒ 不是卡面（不许崩）', false, isSurface(undefined))
  check('S-NC12', '★ class 名含 card 词 ⇒ 是卡', true, isCard(surface({ cls: 'platform-card' })))
  check('S-NC13', '★ class 名不含 card 词（但有底有框）⇒ 是盒子，不是卡', false, isCard(box()))
  check('S-NC14', '★ `card` 出现在词中间（stat-card / card-xxx）⇒ 命中', true, isCard(surface({ cls: 'stat-card app-stat-card' })))
  check('S-NC15', '★ `card` 只是别的词的一部分（discard / postcard）⇒ 不命中', false, isCard(surface({ cls: 'discard-ticket' })))

  // ── 嵌套判定 ───────────────────────────────────────────────────────────
  check(
    'S-NC16',
    '★ 卡 ⊂ 盒子 ⇒ 1 对嵌套',
    1,
    findNestedCards([box({ id: 'b1' }), surface({ id: 'c1', parentId: 'b1' })]).length,
  )
  check(
    'S-NC17',
    '★ 卡 ⊂ 盒子 ⊂ 盒子 ⇒ **只记最近的那个祖先**（1 对，不重复计数）',
    1,
    findNestedCards([
      box({ id: 'b1', parentId: 'ROOT' }),
      box({ id: 'b2', parentId: 'b1' }),
      surface({ id: 'c1', parentId: 'b2' }),
    ]).length,
  )
  check(
    'S-NC18',
    '★ 卡 ⊁ 盒子（没有祖先）⇒ 0 对',
    0,
    findNestedCards([box({ id: 'b1', parentId: 'ROOT' }), surface({ id: 'c1', parentId: 'ROOT' })]).length,
  )
  check(
    'S-NC19',
    '★ 盒子里放的是**不是卡**的东西（class 名没有 card）⇒ 0 对',
    0,
    findNestedCards([box({ id: 'b1' }), surface({ id: 'c1', parentId: 'b1', cls: 'setting-item' })]).length,
  )
  check(
    'S-NC20',
    '★ 盒子里放的是**没有底色**的占位描边行（列表行）⇒ 0 对',
    0,
    findNestedCards([
      box({ id: 'b1' }),
      surface({ id: 'c1', parentId: 'b1', hasBg: false, borderVisible: false }),
    ]).length,
  )
  check(
    'S-NC21',
    '★ 盒子自己**也在**一个盒子里 ⇒ 盒子（名字里没有 card）不算卡 ⇒ 只记里层那一对',
    1,
    findNestedCards([
      box({ id: 'b1', parentId: 'ROOT' }),
      box({ id: 'b2', parentId: 'b1' }),
      surface({ id: 'c1', parentId: 'b2' }),
    ]).length,
  )
  check(
    'S-NC22',
    '★ 外层盒子自己名字里含 card（.card.platform-hub）⇒ 它本身也是一张卡，' +
      '但它**没有**卡面祖先 ⇒ 只记「里层卡 ⊂ 外层卡」这一对',
    1,
    findNestedCards([
      surface({ id: 'b1', cls: 'card platform-hub', parentId: 'ROOT' }),
      surface({ id: 'c1', cls: 'platform-card', parentId: 'b1' }),
    ]).length,
  )
  check(
    'S-NC22b',
    '★ 外层卡自己**也在**一张卡里 ⇒ 两对都要记（外层卡 ⊂ 祖先卡、内层卡 ⊂ 外层卡）',
    2,
    findNestedCards([
      surface({ id: 'a1', cls: 'outer-card', parentId: 'ROOT' }),
      surface({ id: 'b1', cls: 'card platform-hub', parentId: 'a1' }),
      surface({ id: 'c1', cls: 'platform-card', parentId: 'b1' }),
    ]).length,
  )
  check('S-NC23', '空数组 ⇒ 0 对（不许崩、不许假红）', 0, findNestedCards([]).length)
  check('S-NC24', 'undefined 入参 ⇒ 0 对（不许崩）', 0, findNestedCards(undefined).length)
  check('S-NC25', 'undefined 单个描述符 ⇒ 不是卡', false, isCard(undefined))
  check(
    'S-NC26',
    '★ 祖先链断裂（父 id 不在描述符里）⇒ 不得抛异常，且不记这一对',
    0,
    findNestedCards([surface({ id: 'c1', parentId: 'ghost' })]).length,
  )

  // ── 页面级判定 ─────────────────────────────────────────────────────────
  check('S-NC27', '★ 1 对嵌套 ⇒ 这页违规（禁令没有限定语 ⇒ 元素级）', true, judgePageNested([{ a: 1 }]).isNested)
  check('S-NC28', '★ 0 对 ⇒ 不违规', false, judgePageNested([]).isNested)
  check('S-NC29', '★ 页面级判定：undefined ⇒ 0 对、不判红', false, judgePageNested(undefined).isNested)

  // ── 棘轮 ───────────────────────────────────────────────────────────────
  check(
    'S-NC30',
    '棘轮：对数上升 ⇒ 报回归',
    1,
    compareToBaseline({ pages: { '/': { nestedCards: 2 } } }, { nestedCards: { '/': 1 } }).length,
  )
  check(
    'S-NC31',
    '棘轮：对数持平 ⇒ 不报回归',
    0,
    compareToBaseline({ pages: { '/': { nestedCards: 1 } } }, { nestedCards: { '/': 1 } }).length,
  )
  check(
    'S-NC32',
    '棘轮：对数下降 ⇒ 不报回归（只许降）',
    0,
    compareToBaseline({ pages: { '/': { nestedCards: 0 } } }, { nestedCards: { '/': 1 } }).length,
  )
  check(
    'S-NC33',
    '棘轮：页面不在基线里 ⇒ 报回归（不许默默放过）',
    1,
    compareToBaseline({ pages: { '/new-page': { nestedCards: 0 } } }, { nestedCards: { '/': 1 } }).length,
  )
  check('S-NC34', '★ 基线文件缺失（null）⇒ 判「不可用」', false, baselineUsable(null))
  check('S-NC35', '★ 基线里 nestedCards 是空对象 ⇒ 判「不可用」', false, baselineUsable({ nestedCards: {} }))
  check('S-NC36', '★ 基线里 nestedCards 的值不是数字 ⇒ 判「不可用」', false, baselineUsable({ nestedCards: { '/': 'abc' } }))
  check('S-NC37', '★ 正常基线 ⇒ 判「可用」', true, baselineUsable({ nestedCards: { '/': 0 } }))
  check(
    'S-NC38',
    '★ 量到的页面集与基线一致 ⇒ 通过',
    true,
    pagesMatchBaseline({ pages: { '/': { nestedCards: 0 } } }, { nestedCards: { '/': 0 } }),
  )
  check(
    'S-NC39',
    '★ 量到的页面集比基线多一页 ⇒ 不通过（棘轮覆盖不全）',
    false,
    pagesMatchBaseline(
      { pages: { '/': { nestedCards: 0 }, '/new': { nestedCards: 0 } } },
      { nestedCards: { '/': 0 } },
    ),
  )
  check('S-NC40', '★ 一页都没量到 ⇒ 页面集不通过（不许「没量到」等于「通过」）', false, pagesMatchBaseline({ pages: {} }, { nestedCards: { '/': 0 } }))

  // ── 退出码判定（★ 显示与退出码必须同源）────────────────────────────────
  check(
    'S-NC41',
    '★ 有嵌套页 ⇒ 硬闸违规数 = 该页的对数',
    2,
    collectHardGateViolations([
      { page: '/a', isNested: false, pairs: [] },
      { page: '/b', isNested: true, pairs: [{ cardId: 'c1' }, { cardId: 'c2' }] },
    ]).length,
  )
  check(
    'S-NC42',
    '★ 无嵌套页 ⇒ 硬闸违规 0 条（退出码必须绿）',
    0,
    collectHardGateViolations([{ page: '/a', isNested: false, pairs: [] }]).length,
  )
  check(
    'S-NC43',
    '★ isNested=false 但 pairs 非空 ⇒ 不进硬闸（判定只认 isNested，避免两处口径分叉）',
    0,
    collectHardGateViolations([{ page: '/a', isNested: false, pairs: [{ cardId: 'c1' }] }]).length,
  )
  check('S-NC44', '★ undefined / 空入参 ⇒ 0 条（不许崩）', 0, collectHardGateViolations(undefined).length)
  check(
    'S-NC45',
    '★ 页面清单含两个真现场（prompt-injection / free-pool）',
    true,
    DEFAULT_PAGES.includes('/admin/prompt-injection') && DEFAULT_PAGES.includes('/free-pool'),
  )

  const failed = cases.filter((c) => !c.ok)
  return { passed: cases.length - failed.length, total: cases.length, failed, cases }
}

// ── CLI ──────────────────────────────────────────────────────────────────
//
// ★★ **只有直接跑本文件时才执行下面的 CLI**（`import.meta.url === argv[1]`）。
//   否则别的脚本 `import` 本模块来复用判据 / 探针时，会把整段 CLI 一起跑起来
//   —— 第 31 增量的勘察脚本就踩过这个坑（import 上一轮的门 ⇒ 连带跑了 89 页浏览器）。

const IS_MAIN = (() => {
  try {
    return !!process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
  } catch {
    return false
  }
})()

async function main() {
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  const r = runRuntimeSelftest()
  for (const f of r.failed) console.log('❌ ' + f.id + ' ' + f.desc)
  console.log(
    `\n自证 ${r.passed}/${r.total}` + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'),
  )
  process.exit(r.failed.length ? 2 : 0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const jsonArg = argv.find((a) => a.startsWith('--json='))
const pages = pagesArg ? pagesArg.slice('--pages='.length).split(',').filter(Boolean) : DEFAULT_PAGES

if (!CHROME_CANDIDATES.length || !existsSync(CHROME_CANDIDATES[0])) {
  console.log('⏭ SKIP —— 本机没有 Google Chrome ⇒ **未测量**（不是通过）')
  console.log('   安装 Chrome 或设 CHROME_PATH 后重跑。')
  process.exit(3)
}

// ★★ 基线不可用 ⇒ **在测量之前**就判红（★ 与第 30 增量的门同款）。
//   「棘轮的尺没了」这件事本身就已经是红的，再跑一遍浏览器只是白花几分钟。
// ★★ 但 `--update-baseline` 是**显式重建基线**的动作 ⇒ 它必须能跑（否则新门第一次
//   建基线时会被自己的早退挡住 ⇒ 死锁）。
//   ⇒⇒ 而它写回去的值**只许降不许升**（★ 见下面的 nextCount）⇒ 重建基线这条路
//      也不能把棘轮放松 ⇒ ★★ 「--update-baseline 能调高基线」这个洞从设计上堵死。
const wantUpdate = argv.includes('--update-baseline')
const baselineEarly = readBaseline()
if (!wantUpdate && !baselineUsable(baselineEarly)) {
  console.log(
    '\n✘ [NESTED-CARD-BASELINE-UNUSABLE] 基线**不可用**：' +
    (baselineEarly === null
      ? '基线文件缺失或解析失败'
      : '基线里没有可用的 nestedCards 数值') +
    ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
  console.log('   ★ 判定不依赖测量 ⇒ 本次**没有**跑浏览器（基线没了，测了也无处判）。')
  console.log('   ★ 要重建基线：加 --update-baseline（★ 它写回的值只许降不许升）。')
  process.exit(1)
}

const port = 9700 + Math.floor(Math.random() * 300)
let report
try {
  report = await runChrome(port, pages)
} catch (e) {
  // ★★ 2026-10-29：**环境失败 ≠ 判据失败**（★ 与 icon-size 兄弟同款）。
  //   旧写法 exit 2 —— 而 exit 2 在本工具里是「自证失败」的码 ⇒ 仓内 Go 侧
  //   「掏空登记册 ⇒ 读方必须红且红因正确」那道门会把「浏览器连不上」判成
  //   「红了但红因不对」⇒ **在负载下随机误判「门没牙」**。
  //   ⇒ 环境失败走 exit 3（= 本工具既有的「未测量」约定）＋ 专属探针 token。
  console.error(
    '✘ [NESTED-CARD-NOT-MEASURED] 浏览器驱动失败：' + (e?.message || e) +
      '\n   ★ 这是**环境失败**（不是判据失败）⇒ 本次**未测量**（不是通过）',
  )
  process.exit(3)
}

const readings = {
  pages: Object.fromEntries(
    report.pages.map((p) => [
      p.page,
      { nestedCards: p.nestedPairs },
    ]),
  ),
}
console.log('页面 | 元素 | 嵌套对 | 判定')
for (const p of report.pages) {
  console.log(
    `  ${p.page.padEnd(26)} ${String(p.totalDescs).padStart(6)} ${String(p.nestedPairs).padStart(6)} ${p.isNested ? '  ✘ 是' : '  否'}`,
  )
}
for (const e of report.errors) console.log(`  ⚠ ${e.page}: ${e.error}`)

// ★★ **退出码的唯一依据**（纯函数 + 有自证）。
const findings = collectHardGateViolations(report.pages)
if (findings.length) {
  console.log('\n★ 硬闸命中（卡片套卡片 —— 事实源明写「永远不对」）：')
  for (const f of findings) console.log(`  ✘ ${f.page}  ${f.containerCls}  ▸  ${f.cardCls}`)
}

if (wantUpdate) {
  const prev = baselineEarly ?? { nestedCards: {} }
  const prevCounts = (prev && typeof prev === 'object' && prev.nestedCards) ? prev.nestedCards : {}
  const measuredCounts = Object.fromEntries(report.pages.map((p) => [p.page, p.nestedPairs]))
  // ★★ **只许降不许升**：已有页的新基线 = **min(旧值, 实测值)**。
  //   ⇒ 实测值比旧值**低**（我们把违规修好了）⇒ 基线跟着降 ⇒ 上限收紧 ✓
  //   ⇒ 实测值比旧值**高**（有人把代码改坏了）⇒ 基线**保持旧值** ⇒ 上限不放宽 ✓
  //   ⇒⇒ 两条合起来就是 `min(old, now)`；**重建基线这条路放松不了棘轮**。
  //   ★★ （★ 这一段第一版写反了：`old > now` 时保留旧的高值 ⇒ 那是「只许升不许降」，
  //     恰好把棘轮做反了 ⇒ ★ 真仓变异臂 M6 当场把它抓出来 ⇒ 见审计档 §6.4。）
  //   ★ 新页按实测值登记 —— 新页必须被棘轮看见。
  const nextCounts = {}
  const keptHigher = []
  for (const [page, now] of Object.entries(measuredCounts)) {
    const old = prevCounts[page]
    if (typeof old === 'number' && Number.isFinite(old)) {
      if (now > old) {
        nextCounts[page] = old
        keptHigher.push(`${page}：实测 ${now} > 基线旧值 ${old} ⇒ **保持旧值 ${old}**（棘轮不许升）`)
        continue
      }
      nextCounts[page] = now
      continue
    }
    nextCounts[page] = now
  }
  // ★★ note 必须是**动态**的（★ 静态模板会让两次重建的记录一模一样 ⇒
  //   看不出「上限从 111 降到 0」⇒ 变更史就失去了意义）。
  const sumOf = (obj) =>
    Object.values(obj ?? {}).reduce((s, n) => s + (Number(n) || 0), 0)
  const prevTotal = sumOf(prevCounts)
  const nextTotal = sumOf(nextCounts)
  const direction = prevTotal > nextTotal ? '下降' : prevTotal < nextTotal ? '上升' : '持平'
  const next = {
    metric:
      'nestedCards = 该页「卡 ⊂ 盒」的嵌套对数（棘轮：不得上升）。★ 硬闸另算：任意一页出现 1 对 ⇒ 判红' +
      '（事实源「卡片套卡片永远不对」没有「当页面骨架」那种限定语 ⇒ 元素级禁令）。' +
      '★ 重建基线（--update-baseline）写回的值 = min(旧值, 实测值) ⇒ 只许降不许升。',
    nestedCards: nextCounts,
    updated: '2026-10-28',
    history: [
      ...(prev.history ?? []),
      {
        updated: '2026-10-28',
        note:
          '第 31 增量重建基线。★ 上限合计 ' + prevTotal + ' → ' + nextTotal + '（' + direction + '）。' +
          '★ 判据：卡面 = 有底色 ∧ 四边**可见**描边 ∧ 圆角>0 ∧ 内边距和>=8 ∧ 非药丸 ∧ 非交互控件；' +
          '外层盒子 = 卡面 ∧ 宽>=120 ∧ 高>=48；内层卡 = 卡面 ∧ class 名含 card 词 ∧ 宽>=120 ∧ 高>=24。' +
          '★ 修法是【把外层那个懒容器降级成「标题 + 内容区」】⇒ 内层卡直接落在页面底上，不是换皮。' +
          (keptHigher.length
            ? '★ 本次重建时有页试图调高 ⇒ 已按棘轮保持旧值：' + keptHigher.join('；') + '。'
            : '★ 本次重建没有页试图调高。'),
        nestedCards: nextCounts,
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`\n✔ 基线已更新 → ${BASELINE}`)
  if (keptHigher.length) {
    console.log('★ 棘轮保护（这些页的基线没有被调高）：')
    for (const k of keptHigher) console.log('  · ' + k)
  }
}

// ★★ 基线在**写完之后**再读一次（★ 否则 bootstrap 跑完会自相矛盾地打「基线不可用」——
//   判定读的是写入前的那个还不存在的基线）。⇒ 显示与退出码同源，且不自相矛盾。
const baseline = readBaseline()
const usable = baselineUsable(baseline)
const pagesMatch = pagesMatchBaseline(readings, baseline)
const regressions = usable ? compareToBaseline(readings, baseline) : []
if (regressions.length) {
  console.log('\n★ 棘轮被突破：')
  for (const r of regressions) console.log(`  ✘ ${r}`)
}
if (!usable) {
  console.log(
    '\n✘ [NESTED-CARD-BASELINE-UNUSABLE] 基线**不可用**：' +
    (baseline === null
      ? '基线文件缺失或解析失败'
      : '基线里没有可用的 nestedCards 数值') +
    ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
}
if (!pagesMatch) {
  console.log('\n✘ [NESTED-CARD-PAGES-MISMATCH] 量到的页面集与基线不一致 ⇒ ⇒ 棘轮覆盖不全 ⇒ ⇒ 判红')
}

if (jsonArg) writeFileSync(jsonArg.slice(7), JSON.stringify({ ...report, readings }, null, 2))

const measured = report.pages.length
if (!measured) {
  console.log('\n⏭ 一页都没量到 ⇒ **未测量**（不是通过）')
  process.exit(3)
}
if (findings.length || regressions.length || !usable || !pagesMatch) process.exit(1)
// ★ 成功文案必须精确（★ 门的输出也是门的契约）。
const nestedPages = report.pages.filter((p) => p.isNested).length
console.log(
  `\n✅ ${measured} 页量完：存在「卡片套卡片」的页面 ${nestedPages} 个` +
  `；棘轮未突破。`,
)
}

if (IS_MAIN) {
  await main()
}
