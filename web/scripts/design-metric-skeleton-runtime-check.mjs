#!/usr/bin/env node
/**
 * design-metric-skeleton-runtime-check.mjs —— 「同尺寸同构的裸指标卡网格不能当页面骨架」的【运行时】核对（第 30 增量）
 *
 * 这道门治的是哪条明文
 * ────────────────────
 * DESIGN.md §6「卡片」小节有两条硬禁令：
 *   ① 不许用「同尺寸卡片 + 图标 + 标题 + 文字」当页面骨架。
 *   ② 不许用「巨型数字 + 小标签 + 辅助统计 + 强调色」当 hero 指标模板。
 *      指标要有语境：同比、口径、时间窗、异常指向，不是四个数字并排。
 * PRODUCT.md 补一句：「控制台是值班台，不是产品页」「复杂度要显形」。
 * impeccable 的 audit.md §5 给出可判定的问句：这套结构能不能被一个**无关产品**原样搬走？
 *
 * ★★ 判据必须落在「渲染后的事实」上，源码门做不到这件事：
 *   「这张卡是不是巨型数字」取决于它的**字号**与**兄弟卡的尺寸**，
 *   源码里写着 `<p class="oo-card-metric">` 并不能推出它是 24px 的巨数字。
 *   ⇒ 与仓内既有运行时尺（icon-size / action-density / elevation / motion /
 *     primary-clarity / primary-focus / semantic-color / spacing-scale / type-scale）
 *     同族，做成【浏览器运行时尺】。
 *
 * ★★ 什么算「裸指标卡」（逐条都有对应的排除项，判据宁可窄不可宽）
 *   ① 可见（不在隐藏子树里）—— 隐藏子树里的卡片用户根本看不到，不算违规。
 *   ② 卡内**恰好 2 个文本叶子**（标签 + 值）—— 多一个叶子就说明卡里还有说明/口径/时间窗，
 *      那是「有语境」，不判红。
 *   ③ 卡里没有 svg / canvas / progress / table —— 有图标位、有图表、有表格的卡
 *      承载的是别的语义，不属于「裸指标」。
 *   ④ 值字号 **大于** 标签字号，且值字号 >= 20px（`--kx-text-xl` 档）
 *      —— 这就是 DESIGN.md 说的「巨型数字 + 小标签」。
 *   ⑤ 不是药丸（圆角 >= 高度的一半）—— 药丸是 chip，不是卡片骨架。
 *   ⑥ 值是短标量：长度 <= 32，且不含句读（，。；！？、）
 *      —— 一句话不是指标值。
 *
 * ★★ 什么算「违规」
 *   事实源（impeccable `craft-floor.md` 的 "Refuse" 段 + 仓内 DESIGN.md §6）禁的是
 *   「同尺寸卡片……**当页面骨架**」与「巨型数字 + 小标签……当 hero 指标模板」，
 *   并且明写「指标要有语境：同比、口径、时间窗、异常指向，**不是四个数字并排**」。
 *   ⇒ ★★ **「当页面骨架」是页面级判断，不是组件级判断** ⇒ 判据必须落在页面级。
 *   ⇒ 本工具的定稿口径（★ 经全量 88 条静态路由实测 + 目视评审双向核对后收窄）：
 *
 *     硬闸：一页里出现 **>= 2 组**同构的裸指标卡网格 ⇒ 判红
 *           （一组 = 抬头摘要条；两组以上 = 这一页的组织方式就是指标卡 ⇒ 它成了骨架）
 *     棘轮：逐页「涉事卡片数」只许降不许升
 *
 *   ★★★ 收窄过程留档（★ 这是本工具最重要的一段历史）：
 *     第一版按「组」判（每组 >= 3 张就红）⇒ 全量实测命中 **18 组 / 14 页**。
 *     但目视复核（无头 Chrome 出图逐页看）发现：其中 13 页的命中是
 *     **表格/筛选区上方的抬头摘要条** —— /admin/agents、/routing-v2/annotations、
 *     /admin/output-compliance 这类形态在控制台里是**惯例**，它们甚至带状态点、
 *     不带图标、页面骨架是表格 ⇒ ⇒ **那 13 页是「合法的存量」，判据宽过了事实源**
 *     ⇒ ⇒ 收窄成页面级口径后，全量只剩 **「/admin/ops-overview」**（3 组 / 12 张）命中，
 *     **而那正是目视评审认定最像 AI 骨架的一页** ⇒ ⇒ 判据宽度与目视结论一致。
 *   ★ 教训同族（第 29 增量）：★ **判据比事实源宽 ⇒ 批量落地就是在改无辜的代码**。
 *
 * 用法
 * ────
 *   node scripts/design-metric-skeleton-runtime-check.mjs                    # 跑全部页面
 *   node scripts/design-metric-skeleton-runtime-check.mjs --selftest          # 判据自证（不需要浏览器）
 *   node scripts/design-metric-skeleton-runtime-check.mjs --pages=/,/models
 *   node scripts/design-metric-skeleton-runtime-check.mjs --json=out.json
 *   node scripts/design-metric-skeleton-runtime-check.mjs --update-baseline   # 人工降基线
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
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const BASELINE = join(ROOT, 'scripts', 'design-metric-skeleton-runtime-baseline.json')

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
 * ★ 为什么派生而不是写死一份列表：
 *   写死的清单会**悄悄过期** —— 新增路由不在清单里 ⇒ 那一页无人看守，
 *   而「清单少了一页」这件事在门里**看不出来**（只量了清单内的页）。
 *   ⇒ 派生 ⇒ 清单永远等于「当前仓里所有能直接打开的页面」。
 *   ⇒ 派生不到（例如脚本被拷到别处）⇒ 退回一份保底清单，并**显式说明**。
 *   ★ 带路径参数的路由（`:id`）与兜底路由（`pathMatch`）不在清单里：
 *     它们需要具体参数才能渲染，量它们只会量到「加载中」或 404。
 */
export const FALLBACK_PAGES = [
  '/',
  '/free-pool',
  '/model-pricing',
  '/providers',
  '/request-logs',
  '/keys',
  '/admin/proxy',
  '/models',
  '/routing-v2',
  '/admin/ops-overview',
  '/admin/board-operational',
  '/admin/data-lifecycle',
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
    const uniq = [...new Set(paths)].sort()
    return uniq.length ? uniq : null
  } catch {
    return null
  }
}

export const DEFAULT_PAGES = derivePages() ?? FALLBACK_PAGES

/** 一组里至少几张同构卡才算「网格」。两张只是并列，不构成骨架。 */
export const MIN_GROUP = 3

/** 「巨型数字」的下限：`--kx-text-xl` = 20px。 */
export const HERO_VALUE_MIN_PX = 20

/** 几何容差（px）：网格里的卡尺寸允许有 1~2px 的渲染误差。 */
export const GEOMETRY_TOLERANCE_PX = 2

/** 值的最大长度（字符）。 */
export const VALUE_MAX_LEN = 32

/**
 * 一页里有几组同构的裸指标卡网格，就算「这页把指标卡网格当成了骨架」。
 *
 * ★ 理由：事实源禁的是「当**页面骨架**」⇒ 必须是**页面级**判断。
 * ★ 一组 = 抬头摘要条（控制台惯例，目视复核确认合法）；**两组以上** = 这一页的
 *   组织方式就是指标卡 ⇒ 它成了骨架 ⇒ 判红。
 */
export const PAGE_SKELETON_MIN_GROUPS = 2

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ w: number, h: number, radius: number, cls: string,
 *             parentKey: string, childTags: string,
 *             leaves: {text: string, fontSize: number}[],
 *             hasSvg: boolean, hasChart: boolean, hasTable: boolean,
 *             hidden: boolean }} CardDesc
 */

/** 句读：值位里出现它就说明这是句话，不是指标值。 */
const SENTENCE_PUNCT = /[，。；！？、]/

/**
 * 判「这张卡是不是裸指标卡」。
 *
 * @param {CardDesc} card
 * @returns {boolean}
 */
export function isBareMetricCard(card) {
  if (!card || typeof card !== 'object') return false
  // ① 隐藏子树：用户看不到的卡不是骨架。
  if (card.hidden) return false
  // ③ 图标位 / 图表 / 表格：承载别的语义，不属于裸指标。
  if (card.hasSvg || card.hasChart || card.hasTable) return false
  const leaves = Array.isArray(card.leaves) ? card.leaves : []
  // ② 恰好「标签 + 值」两个文本叶子：多一个就有语境。
  if (leaves.length !== 2) return false
  const label = leaves[0] ?? {}
  const value = leaves[1] ?? {}
  const labelText = String(label.text ?? '').trim()
  const valueText = String(value.text ?? '').trim()
  if (!labelText || !valueText) return false
  const labelSize = Number(label.fontSize)
  const valueSize = Number(value.fontSize)
  if (!Number.isFinite(labelSize) || !Number.isFinite(valueSize)) return false
  // ④ 「小标签 + 大数字」，且大数字够巨型。
  if (!(valueSize > labelSize)) return false
  if (valueSize < HERO_VALUE_MIN_PX) return false
  // ⑥ 值是短标量，不是一句话。
  if (valueText.length > VALUE_MAX_LEN) return false
  if (SENTENCE_PUNCT.test(valueText)) return false
  // ⑤ 药丸是 chip，不是卡片骨架。
  const radius = Number(card.radius)
  const h = Number(card.h)
  if (Number.isFinite(radius) && Number.isFinite(h) && radius >= h / 2) return false
  return true
}

/**
 * 找「同尺寸同构的裸指标卡网格」。
 *
 * @param {CardDesc[]} cards
 * @returns {{parentKey: string, w: number, h: number, structure: string,
 *            count: number, classes: string[], labels: string[]}[]}
 */
export function findSkeletonGrids(cards) {
  const list = Array.isArray(cards) ? cards : []
  // 先按父元素分组 —— ★ 按父元素的 class 名分组会把多个容器合并成一组（假红）。
  const byParent = new Map()
  for (const c of list) {
    if (!isBareMetricCard(c)) continue
    const k = String(c.parentKey ?? '')
    if (!byParent.has(k)) byParent.set(k, [])
    byParent.get(k).push(c)
  }
  const out = []
  for (const [parentKey, group] of byParent) {
    // 再按「几何 + 结构」分桶：容差 2px 内视为同尺寸。
    const buckets = []
    for (const c of group) {
      let hit = null
      for (const b of buckets) {
        if (
          Math.abs(b.w - Number(c.w)) <= GEOMETRY_TOLERANCE_PX &&
          Math.abs(b.h - Number(c.h)) <= GEOMETRY_TOLERANCE_PX &&
          b.structure === c.childTags
        ) {
          hit = b
          break
        }
      }
      if (hit) {
        hit.cards.push(c)
      } else {
        buckets.push({
          w: Number(c.w),
          h: Number(c.h),
          structure: c.childTags,
          cards: [c],
        })
      }
    }
    for (const b of buckets) {
      if (b.cards.length < MIN_GROUP) continue
      out.push({
        parentKey,
        w: b.w,
        h: b.h,
        structure: b.structure,
        count: b.cards.length,
        classes: [...new Set(b.cards.map((c) => c.cls))],
        labels: b.cards.map((c) => String(c.leaves?.[0]?.text ?? '').trim()),
      })
    }
  }
  return out
}

/**
 * 页面级判定：这页是不是「拿指标卡网格当了骨架」。
 *
 * @param groups findSkeletonGrids 的结果
 * @returns {{ groups: number, cards: number, isSkeleton: boolean }}
 */
export function judgePageSkeleton(groups) {
  const list = Array.isArray(groups) ? groups : []
  const cards = list.reduce((n, g) => n + (Number(g?.count) || 0), 0)
  return {
    groups: list.length,
    cards,
    isSkeleton: list.length >= PAGE_SKELETON_MIN_GROUPS,
  }
}

/**
 * ★★ **只挑「骨架页」的组**（★ 本轮最值钱的一处自伤自愈）。
 *
 * ★ 背景：这个函数是**退出码的唯一依据**，所以它必须是纯函数、且必须被自证覆盖。
 * ★ 第一版把退出条件写成 `findings.length`（★ `findings` = **所有**组，包括
 *   「抬头摘要条」那些合法存量）⇒ ⇒ ★★ **门在 14 个合法页上判红**，
 *   ★★ 而输出里一个「✘」都没有（★ 因为打印时按 `isSkeleton` 分了级）⇒
 *   ★★ **显示说「没问题」、退出码说「有问题」** ⇒ ★ 这正是第 27 增量记过的那类
 *   ★★ **「消息说不是通过、退出码说通过」** 的镜像版。
 * ★ ⇒ ⇒ ⇒ 修法：**把判定抽成纯函数**（`collectHardGateViolations`），
 *   ★ **退出码只认它的结果**，★ 并且给它加自证（★ S-MS40~S-MS43）。
 *
 * @param pages [{page, isSkeleton, grids:[...]}]
 */
export function collectHardGateViolations(pages) {
  const list = Array.isArray(pages) ? pages : []
  const out = []
  for (const p of list) {
    if (!p || p.isSkeleton !== true) continue
    for (const g of p.grids ?? []) out.push({ page: p.page, ...g })
  }
  return out
}

/**
 * 棘轮：各页「涉事卡片数」不得超过基线（只许降不许升）。
 *
 * @param readings { pages: { page: { bareMetricCards: number } } }
 * @param baseline { bareMetricCards: {page:number} }
 */
export function compareToBaseline(readings, baseline) {
  const regressions = []
  const pages = readings?.pages ?? {}
  for (const [page, r] of Object.entries(pages)) {
    const limit = baseline?.bareMetricCards?.[page]
    if (typeof limit !== 'number') {
      regressions.push(`${page}：不在基线里（基线缺这一页 ⇒ 无法判棘轮）`)
      continue
    }
    if ((r.bareMetricCards ?? 0) > limit) {
      regressions.push(`${page}：裸指标卡数 ${limit} → ${r.bareMetricCards}（升高了）`)
    }
  }
  return regressions
}

/**
 * ★ 基线能不能用（★ 防「基线没了 ⇒ 棘轮整个空转 ⇒ 假绿」）。
 *
 * @param baseline 解析后的基线（可能是 null）
 */
export function baselineUsable(baseline) {
  const s = baseline?.bareMetricCards
  if (!s || typeof s !== 'object') return false
  const values = Object.values(s)
  if (!values.length) return false
  return values.every((v) => typeof v === 'number' && Number.isFinite(v))
}

/**
 * 量到的页面集合是否与基线一致（★ 页面集不一致 ⇒ 棘轮覆盖不全 ⇒ 判红）。
 */
export function pagesMatchBaseline(readings, baseline) {
  const measured = Object.keys(readings?.pages ?? {})
  if (!measured.length) return false
  const known = new Set(Object.keys(baseline?.bareMetricCards ?? {}))
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
// ★ 分组按**父元素**编号（探针里用 WeakMap 发号），不按 class 名。

const PROBE = `(() => {
  const inHiddenSubtree = (el) => {
    let n = el
    while (n && n.nodeType === 1 && n !== document.documentElement) {
      const cs = getComputedStyle(n)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return true
      n = n.parentElement
    }
    return false
  }
  const num = (v) => Math.round((parseFloat(v) || 0) * 10) / 10
  // ★ 父元素编号：分组必须按元素，同名的多个容器不能并成一组。
  const parentIds = new WeakMap()
  let nextId = 0
  const parentKeyOf = (el) => {
    const p = el.parentElement
    if (!p) return 'ROOT'
    if (!parentIds.has(p)) parentIds.set(p, String(nextId++))
    return parentIds.get(p)
  }
  // ★ 候选「卡片」：有自己的背景 或 有 >= 1px 的描边。
  const candidates = [...document.querySelectorAll('body *')].filter((el) => {
    if (el.children.length > 40) return false
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.display === 'contents') return false
    const bg = cs.backgroundColor
    const hasBg = bg && bg !== 'transparent' && !/rgba\\(0, 0, 0, 0\\)/.test(bg)
    // ★ 「卡片」= 一个**盒子**：有自己的底色，或四边都有 >= 1px 描边。
    //   ★ 只有下边框的**行**（列表行 / 指标行）不算卡片 ——
    //   否则「一列 label → value 的行」会被误认成「一列同尺寸指标卡」，
    //   判据就宽过了事实源（★ 第 29 增量踩过的那个坑）。
    // ★★ 2026-10-28 第 31 增量补：描边必须**可见** —— 宽度 >= 1px **且**颜色非透明。
    //   「1px solid transparent」的**占位描边**不是描边（实测 ModulesView.vue 的
    //   .module-card 常态就是无底色 + 占位描边 ⇒ 那是**列表行**不是卡）。
    //   ★ 这条修正**只往保守方向**收窄（卡片候选集只会变小 ⇒ 裸指标卡数只可能降）。
    //   ★ 全量 89 页实测：修正前后**逐页读数零变化**（差异页 = 0）⇒ 这条疏漏是**潜伏的**、
    //     不是活的；补上它是为了让两道门的「卡」定义一致，不是为了让读数变。
    const isTransparent = (c) => !c || c === 'transparent' || /^rgba\\(0,\\s*0,\\s*0,\\s*0\\)$/.test(c)
    const borderSide = (side) =>
      num(cs['border' + side + 'Width']) >= 1 && !isTransparent(cs['border' + side + 'Color'])
    const boxBorder =
      borderSide('Top') && borderSide('Bottom') && borderSide('Left') && borderSide('Right')
    return hasBg || boxBorder
  })
  const cards = candidates.map((el) => {
    const r = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    const leaves = []
    // ★ 卡自己的直接文本（没有子元素包裹的那部分）也算一个叶子。
    let own = ''
    for (const node of el.childNodes) {
      if (node.nodeType === 3) own += node.nodeValue || ''
    }
    if (own.trim()) {
      leaves.push({ text: own.trim().replace(/\\s+/g, ' ').slice(0, 80), fontSize: num(cs.fontSize) })
    }
    for (const n of el.querySelectorAll('*')) {
      if (n.children.length > 0) continue
      const t = (n.innerText || n.textContent || '').trim()
      if (!t) continue
      leaves.push({ text: t.replace(/\\s+/g, ' ').slice(0, 80), fontSize: num(getComputedStyle(n).fontSize) })
    }
    return {
      w: num(r.width),
      h: num(r.height),
      radius: num(cs.borderTopLeftRadius),
      cls: (el.getAttribute('class') || el.tagName).slice(0, 90),
      parentKey: parentKeyOf(el),
      childTags: [...el.children].map((c) => c.tagName.toLowerCase()).join(',').slice(0, 70),
      leaves,
      hasSvg: !!el.querySelector('svg'),
      hasChart: !!el.querySelector('canvas, [role="progressbar"], progress'),
      hasTable: !!el.querySelector('table'),
      hidden: inHiddenSubtree(el),
    }
  }).filter((c) => !c.hidden && c.w > 0 && c.h > 0)
  return JSON.stringify({ cards })
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'metric-skeleton-runtime-'))
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
      const cards = probe.cards ?? []
      const grids = findSkeletonGrids(cards)
      const verdict = judgePageSkeleton(grids)
      report.pages.push({
        page: p,
        totalCards: cards.length,
        skeletonGrids: verdict.groups,
        skeletonCards: verdict.cards,
        isSkeleton: verdict.isSkeleton,
        grids,
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

  /** 一张「典型裸指标卡」：标签 13px + 值 24px。 */
  const mk = (over = {}) => ({
    w: 324.5,
    h: 89.2,
    radius: 8,
    cls: 'oo-card',
    parentKey: 'P1',
    childTags: 'h3,p',
    leaves: [
      { text: '总实例数', fontSize: 13 },
      { text: '0', fontSize: 24 },
    ],
    hasSvg: false,
    hasChart: false,
    hasTable: false,
    hidden: false,
    ...over,
  })

  /** 一组同尺寸同构的卡。 */
  const grp = (n, over = {}) =>
    Array.from({ length: n }, (_, i) =>
      mk({ ...over, parentKey: over.parentKey ?? 'P1', leaves: over.leaves ?? [{ text: `标签${i}`, fontSize: 13 }, { text: String(i), fontSize: 24 }] }),
    )

  check('S-MS1', '3 张同尺寸同构的裸指标卡 ⇒ 判红 1 组', 1, findSkeletonGrids(grp(3)).length)
  check('S-MS2', '2 张 ⇒ 不成网格（两张只是并列）', 0, findSkeletonGrids(grp(2)).length)
  check('S-MS3', '4 张 ⇒ 判红 1 组', 1, findSkeletonGrids(grp(4)).length)
  check(
    'S-MS4',
    '★ 卡里多一个叶子（有口径/说明）⇒ 不算裸指标卡',
    false,
    isBareMetricCard(
      mk({
        leaves: [
          { text: '可路由模型', fontSize: 13 },
          { text: '0', fontSize: 24 },
          { text: '/ 0 offer', fontSize: 11 },
        ],
      }),
    ),
  )
  check('S-MS5', '★ 卡里有 svg（图标位）⇒ 不算裸指标卡', false, isBareMetricCard(mk({ hasSvg: true })))
  check('S-MS6', '★ 卡里有图表 ⇒ 不算裸指标卡', false, isBareMetricCard(mk({ hasChart: true })))
  check('S-MS7', '★ 卡里有表格 ⇒ 不算裸指标卡', false, isBareMetricCard(mk({ hasTable: true })))
  check(
    'S-MS8',
    '★ 值字号不大于标签字号（不是「小标签 + 大数字」）⇒ 不算',
    false,
    isBareMetricCard(
      mk({ leaves: [{ text: '全部', fontSize: 14 }, { text: '8', fontSize: 14 }] }),
    ),
  )
  check(
    'S-MS9',
    '★ 值字号 16px（不够「巨型」）⇒ 不算',
    false,
    isBareMetricCard(
      mk({ leaves: [{ text: '总请求', fontSize: 11 }, { text: '19', fontSize: 16 }] }),
    ),
  )
  check(
    'S-MS10',
    '★ 药丸形态（圆角 >= 高度的一半）⇒ 不算（chip 不是卡）',
    false,
    isBareMetricCard(mk({ radius: 999, h: 38 })),
  )
  check('S-MS11', '★ 隐藏子树里的卡 ⇒ 不算', false, isBareMetricCard(mk({ hidden: true })))
  check(
    'S-MS12',
    '★ 值是一句话（含句读）⇒ 不算',
    false,
    isBareMetricCard(
      mk({ leaves: [{ text: '说明', fontSize: 13 }, { text: '这是一段说明文字。', fontSize: 24 }] }),
    ),
  )
  check(
    'S-MS13',
    '★ 值太长（> 32 字符）⇒ 不算',
    false,
    isBareMetricCard(
      mk({ leaves: [{ text: '说明', fontSize: 13 }, { text: 'x'.repeat(33), fontSize: 24 }] }),
    ),
  )
  check(
    'S-MS14',
    '★ 几何差 3px（超过容差）⇒ 不成网格',
    0,
    findSkeletonGrids([
      mk({ w: 324.5 }),
      mk({ w: 324.5 }),
      mk({ w: 327.5 }),
    ]).length,
  )
  check(
    'S-MS15',
    '★ 几何差 2px（容差内）⇒ 成网格',
    1,
    findSkeletonGrids([
      mk({ w: 324.5 }),
      mk({ w: 325.5 }),
      mk({ w: 326.0 }),
    ]).length,
  )
  check(
    'S-MS16',
    '★ 结构不同（子标签序列不一致）⇒ 不成网格',
    0,
    findSkeletonGrids([
      mk({ childTags: 'h3,p' }),
      mk({ childTags: 'h3,p' }),
      mk({ childTags: 'div,div' }),
    ]).length,
  )
  check(
    'S-MS17',
    '★ 父元素不同 ⇒ 不成网格（★ 这是「按 class 名分组」会踩的坑）',
    0,
    findSkeletonGrids([
      mk({ parentKey: 'P1' }),
      mk({ parentKey: 'P2' }),
      mk({ parentKey: 'P3' }),
    ]).length,
  )
  check(
    'S-MS18',
    '★ 同一 class 名的多个容器 ⇒ 仍按元素分组（2+2 张都不成网格）',
    0,
    findSkeletonGrids([
      mk({ parentKey: 'P1' }),
      mk({ parentKey: 'P1' }),
      mk({ parentKey: 'P2' }),
      mk({ parentKey: 'P2' }),
    ]).length,
  )
  check('S-MS19', '空数组 ⇒ 0 组（不许崩、不许假红）', 0, findSkeletonGrids([]).length)
  check('S-MS20', 'undefined 入参 ⇒ 0 组（不许崩）', 0, findSkeletonGrids(undefined).length)
  check('S-MS21', 'undefined 单卡 ⇒ 不算裸指标卡', false, isBareMetricCard(undefined))
  check(
    'S-MS22',
    '棘轮：涉事卡片数上升 ⇒ 报回归',
    1,
    compareToBaseline({ pages: { '/': { bareMetricCards: 3 } } }, { bareMetricCards: { '/': 2 } }).length,
  )
  check(
    'S-MS23',
    '棘轮：涉事卡片数持平 ⇒ 不报回归',
    0,
    compareToBaseline({ pages: { '/': { bareMetricCards: 2 } } }, { bareMetricCards: { '/': 2 } }).length,
  )
  check(
    'S-MS24',
    '棘轮：涉事卡片数下降 ⇒ 不报回归（只许降）',
    0,
    compareToBaseline({ pages: { '/': { bareMetricCards: 0 } } }, { bareMetricCards: { '/': 2 } }).length,
  )
  check(
    'S-MS25',
    '棘轮：页面不在基线里 ⇒ 报回归（不许默默放过）',
    1,
    compareToBaseline({ pages: { '/new-page': { bareMetricCards: 0 } } }, { bareMetricCards: { '/': 2 } }).length,
  )
  check('S-MS26', '★ 基线文件缺失（null）⇒ 判「不可用」', false, baselineUsable(null))
  check('S-MS27', '★ 基线里 bareMetricCards 是空对象 ⇒ 判「不可用」', false, baselineUsable({ bareMetricCards: {} }))
  check(
    'S-MS28',
    '★ 基线里 bareMetricCards 的值不是数字 ⇒ 判「不可用」',
    false,
    baselineUsable({ bareMetricCards: { '/': 'abc' } }),
  )
  check('S-MS29', '★ 正常基线 ⇒ 判「可用」', true, baselineUsable({ bareMetricCards: { '/': 0 } }))
  check(
    'S-MS30',
    '★ 量到的页面集与基线一致 ⇒ 通过',
    true,
    pagesMatchBaseline({ pages: { '/': { bareMetricCards: 0 } } }, { bareMetricCards: { '/': 0 } }),
  )
  check(
    'S-MS31',
    '★ 量到的页面集比基线多一页 ⇒ 不通过（棘轮覆盖不全）',
    false,
    pagesMatchBaseline(
      { pages: { '/': { bareMetricCards: 0 }, '/new': { bareMetricCards: 0 } } },
      { bareMetricCards: { '/': 0 } },
    ),
  )
  check(
    'S-MS32',
    '★ 一页都没量到 ⇒ 页面集不通过（不许「没量到」等于「通过」）',
    false,
    pagesMatchBaseline({ pages: {} }, { bareMetricCards: { '/': 0 } }),
  )
  check(
    'S-MS33',
    '★ 页面清单含两个真现场（ops-overview / board-operational）',
    true,
    DEFAULT_PAGES.includes('/admin/ops-overview') && DEFAULT_PAGES.includes('/admin/board-operational'),
  )

  // ── 页面级判定（★ 「当页面骨架」是页面级判断）────────────────────────
  check(
    'S-MS34',
    '★ 一页只有 1 组同构网格 ⇒ 不是骨架（抬头摘要条，控制台惯例）',
    false,
    judgePageSkeleton([{ count: 5 }]).isSkeleton,
  )
  check(
    'S-MS35',
    '★ 一页有 2 组同构网格 ⇒ 判红（这页把指标卡网格当成了骨架）',
    true,
    judgePageSkeleton([{ count: 4 }, { count: 4 }]).isSkeleton,
  )
  check(
    'S-MS36',
    '★ 一页有 3 组同构网格 ⇒ 判红',
    true,
    judgePageSkeleton([{ count: 4 }, { count: 4 }, { count: 4 }]).isSkeleton,
  )
  check(
    'S-MS37',
    '★ 页面级判定：涉事卡片数 = 各组张数之和',
    12,
    judgePageSkeleton([{ count: 4 }, { count: 4 }, { count: 4 }]).cards,
  )
  check('S-MS38', '★ 页面级判定：空入参 ⇒ 0 组 0 张、不判红', false, judgePageSkeleton(undefined).isSkeleton)
  check(
    'S-MS39',
    '★ 页面级判定：undefined 的 cards 不许把合计变成 NaN',
    0,
    judgePageSkeleton([{ count: undefined }]).cards,
  )

  // ── 退出码判定（★ 本轮最值钱的一处自伤自愈：显示与退出码必须同源）──────
  check(
    'S-MS40',
    '★ 只有 1 组的页 ⇒ **不**算硬闸违规（退出码必须绿）',
    0,
    collectHardGateViolations([
      { page: '/admin/agents', isSkeleton: false, grids: [{ count: 5 }] },
    ]).length,
  )
  check(
    'S-MS41',
    '★ 有骨架页 ⇒ 硬闸违规数 = 该页的组数',
    3,
    collectHardGateViolations([
      { page: '/admin/agents', isSkeleton: false, grids: [{ count: 5 }] },
      { page: '/admin/ops-overview', isSkeleton: true, grids: [{ count: 4 }, { count: 4 }, { count: 4 }] },
    ]).length,
  )
  check(
    'S-MS42',
    '★ 合法存量组**不得**进入硬闸违规（★ 这是第一版把退出码写废的那个坑）',
    0,
    collectHardGateViolations([
      { page: '/a', isSkeleton: false, grids: [{ count: 5 }] },
      { page: '/b', isSkeleton: false, grids: [{ count: 4 }] },
    ]).length,
  )
  check(
    'S-MS43',
    '★ undefined / 空入参 ⇒ 0 条（不许崩）',
    0,
    collectHardGateViolations(undefined).length,
  )

  const failed = cases.filter((c) => !c.ok)
  return { passed: cases.length - failed.length, total: cases.length, failed, cases }
}

// ── CLI ──────────────────────────────────────────────────────────────────
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
const pages = pagesArg ? pagesArg.slice(8).split(',').filter(Boolean) : DEFAULT_PAGES
const jsonArg = argv.find((a) => a.startsWith('--json='))

if (!CHROME_CANDIDATES.length || !existsSync(CHROME_CANDIDATES[0])) {
  console.log('⏭ SKIP —— 本机没有 Google Chrome ⇒ **未测量**（不是通过）')
  console.log('   安装 Chrome 或设 CHROME_PATH 后重跑。')
  process.exit(3)
}

// ★★ 基线不可用 ⇒ **在测量之前**就判红（★ 2026-10-28 第 30 增量加）。
//   理由：这条判定**不依赖测量** —— 「棘轮的尺没了」这件事本身就已经是红的，
//   再去跑一遍 88 页浏览器只是白花 5 分钟。
//   ★ 顺带好处：仓内 Go 侧「掏空登记册 ⇒ 读方必须红」那道门会**两次**调读方
//   （基线态一次 + 掏空态一次）⇒ 掏空态现在**秒级**返回 ⇒ 不再把那个套件拖长一倍。
const baselineEarly = readBaseline()
if (!baselineUsable(baselineEarly)) {
  console.log(
    '\n✘ [METRIC-SKELETON-BASELINE-UNUSABLE] 基线**不可用**：' +
    (baselineEarly === null
      ? '基线文件缺失或解析失败'
      : '基线里没有可用的 bareMetricCards 数值') +
    ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
  console.log('   ★ 判定不依赖测量 ⇒ 本次**没有**跑浏览器（基线没了，测了也无处判）。')
  process.exit(1)
}

const port = 9400 + Math.floor(Math.random() * 300)
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
    '✘ [METRIC-SKELETON-NOT-MEASURED] 浏览器驱动失败：' + (e?.message || e) +
      '\n   ★ 这是**环境失败**（不是判据失败）⇒ 本次**未测量**（不是通过）',
  )
  process.exit(3)
}

const readings = {
  pages: Object.fromEntries(
    report.pages.map((p) => [
      p.page,
      { bareMetricCards: p.skeletonCards, skeletonGroups: p.skeletonGrids },
    ]),
  ),
}
const baseline = readBaseline()

console.log('页面 | 卡片候选 | 裸指标网格组 | 涉事卡 | 骨架')
for (const p of report.pages) {
  console.log(
    `  ${p.page.padEnd(26)} ${String(p.totalCards).padStart(6)} ${String(p.skeletonGrids).padStart(10)} ${String(p.skeletonCards).padStart(8)} ${p.isSkeleton ? '  ✘ 是' : '  否'}`,
  )
}
for (const e of report.errors) console.log(`  ⚠ ${e.page}: ${e.error}`)

// ★★ **退出码的唯一依据**（★ 纯函数 + 有自证；★ 见 collectHardGateViolations 的注释）。
const findings = collectHardGateViolations(report.pages)
// ★ 全部组（含「抬头摘要条」那些合法存量）⇒ 只作信息展示，**不参与判定**。
const allGroups = report.pages.flatMap((p) =>
  p.grids.map((g) => ({
    page: p.page,
    isSkeleton: p.isSkeleton === true,
    count: g.count,
    w: g.w,
    h: g.h,
    structure: g.structure,
    classes: g.classes,
    labels: g.labels,
  })),
)
if (allGroups.length) {
  console.log('\n★ 同尺寸同构的裸指标卡网格：')
  for (const f of allGroups) {
    console.log(
      `  ${f.isSkeleton ? '✘' : '·'} ${f.page}  ${f.count} 张 ${f.w}×${f.h} 结构「${f.structure}」 类「${f.classes.join(' / ')}」`,
    )
    console.log(`      标签：${f.labels.join(' · ')}`)
  }
  console.log(
    `\n  判据：同一页里 >= ${PAGE_SKELETON_MIN_GROUPS} 组这样的网格 ⇒ 这页把指标卡网格当成了骨架（★ 一组 = 抬头摘要条，是控制台惯例，不判红）。`,
  )
}
if (findings.length) {
  console.log('\n★ 硬闸命中（这页把指标卡网格当成了骨架）：')
  for (const f of findings) console.log(`  ✘ ${f.page}  ${f.count} 张 组「${f.structure}」`)
}

const usable = baselineUsable(baseline)
const pagesMatch = pagesMatchBaseline(readings, baseline)
const regressions = usable ? compareToBaseline(readings, baseline) : []
if (regressions.length) {
  console.log('\n★ 棘轮被突破：')
  for (const r of regressions) console.log(`  ✘ ${r}`)
}
if (!usable) {
  console.log(
    '\n✘ [METRIC-SKELETON-BASELINE-UNUSABLE] 基线**不可用**：' +
    (baseline === null
      ? '基线文件缺失或解析失败'
      : '基线里没有可用的 bareMetricCards 数值') +
    ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
}
if (!pagesMatch) {
  console.log('\n✘ [METRIC-SKELETON-PAGES-MISMATCH] 量到的页面集与基线不一致 ⇒ ⇒ 棘轮覆盖不全 ⇒ ⇒ 判红')
}

if (jsonArg) writeFileSync(jsonArg.slice(7), JSON.stringify({ ...report, readings }, null, 2))

if (argv.includes('--update-baseline')) {
  const prev = baseline ?? { bareMetricCards: {} }
  const next = {
    metric:
      'bareMetricCards = 该页落在「同尺寸同构的裸指标卡网格」里的卡片数（棘轮：不得上升）。★ 硬闸另算：一页里 >= ' +
      String(PAGE_SKELETON_MIN_GROUPS) +
      ' 组这样的网格 ⇒ 这页把指标卡网格当成了页面骨架 ⇒ 判红（与基线无关）。',
    bareMetricCards: Object.fromEntries(report.pages.map((p) => [p.page, p.skeletonCards])),
    updated: '2026-10-28',
    history: [
      ...(prev.history ?? []),
      {
        updated: '2026-10-28',
        note:
          '第 30 增量建立基线。★ 建基线之前用同一把尺实测全量 88 条静态路由：' +
          '/admin/ops-overview = 3 组 / 12 张（中心实例 / 故障 / 运行时指标）⇒ 唯一的页面级违规；' +
          '其余 13 页各有 1 组（4~6 张）⇒ 抬头摘要条，控制台惯例，棘轮钉住不降不许升。★ 修法是【把指标卡网格改成 label → value 行 + 补语境】，不是换皮。',
        bareMetricCards: Object.fromEntries(report.pages.map((p) => [p.page, p.skeletonCards])),
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`\n✔ 基线已更新 → ${BASELINE}`)
}

const measured = report.pages.length
if (!measured) {
  console.log('\n⏭ 一页都没量到 ⇒ **未测量**（不是通过）')
  process.exit(3)
}
if (findings.length || regressions.length || !usable || !pagesMatch) process.exit(1)
// ★ 成功文案必须精确（★ 门的输出也是门的契约）：★ 这里说的是
//   「没有一页把指标卡网格当成骨架」⇒ ★ **不是**「一个裸指标网格都没有」
//   （★ 抬头摘要条那些合法存量照样在 ⇒ 表格里逐条列着）。
const skeletonPages = report.pages.filter((p) => p.isSkeleton).length
const groupPages = report.pages.filter((p) => p.skeletonGrids > 0).length
console.log(
  `\n✅ ${measured} 页量完：把指标卡网格当骨架的页面 ${skeletonPages} 个` +
  `（★ 全量另有 ${groupPages} 页带「抬头摘要条」形态的裸指标卡网格 ⇒ 合法存量，棘轮钉住）；棘轮未突破。`,
)
