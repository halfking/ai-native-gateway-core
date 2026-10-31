// actionDensity.ts — 「动作区必须可扫读」的可执行判据（2026-10-27 · 第 25 增量）
//
// ★ 要治的病（impeccable 规范里逐条点名的「AI 界面的标志」）：
//   impeccable `skill/reference/distill.md`：
//     「**Clear hierarchy: ONE primary action, few secondary actions,
//       everything else tertiary or hidden**」
//     「**Clear next action: ONE obvious next action, not five competing ones**」
//   impeccable `skill/reference/layout.md`（Density）：
//     「**Does the amount of information per region fit use frequency,
//       decision complexity, and visitor mode?**」
//   impeccable `skill/reference/craft-floor.md`（Verify · Coverage）：
//     「**every brief requirement present and findable within seconds**」
//   impeccable `skill/reference/craft-floor.md`（Refuse）：
//     「A modal for a task that needs neither interruption nor protected focus.」
//
//   而真仓现状（截图 + 普查取证）：
//     · `views/FreePoolView.vue` 的页头动作区里**并列 9 个等重药丸**
//       （刷新 / 一键建设 / OAuth 桥接 / 导入环境变量 Key / 自动学习并注册 /
//         快速录入 / 写入 DB 凭据 / 添加 Provider / 多 Key 批量池化）
//       ⇒ 10 颗药丸横排一行 ⇒ 用户找不到「那个我要的」；
//       ★ 其中「快速录入」还与页面自己的 tab「注册助手」**完全重复**。
//     · `views/RequestDetailFullscreenView.vue` 的页头 5 个动作里，
//       「返回箭头」与「关闭」**调的是同一个 `goBack`** ⇒ 两个入口同一件事。
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **一个「动作区」里，最坏情况下的可见动作数 ≤ 4。**
//   —— 并列动作超过 4 个 ⇒ 眼睛必须扫一遍才能定位 ⇒ 必须把低频动作收进「更多」。
//
// ★ 「动作区」的口径 = **直接父容器**（沿用第 22 增量已验证的口径，不靠类名）。
//
// ★★★ **本轮最要紧的一处量具修正：静态计数会把「互斥分支」数成多个。**
//   `KeysView.vue` 的抽屉底栏有 5 个按钮，但「审批 / 禁用 / 启用」是
//   `v-if / v-else-if / v-else` **互斥链** ⇒ 渲染时**最多只出现一个**
//   ⇒ 静态读数 5 是**虚高**的，真实可见数是 3。
//   ⇒ 判据按 **Vue 的相邻规则**把 `v-if / v-else-if / v-else` 折叠成**一条链**，
//     一条链只贡献 1 个可见动作。
//   ★ 通式：**静态计数必须回答「最坏情况下同时可见几个」**，而不是
//     「源码里有几个按钮」—— 前者是界面上真实发生的事，后者不是。
//
// ★ 排除（每条都要能用读数证明是**活分支**，不是空转）：
//   · **焦点区域**（模态 / 抽屉）内 —— 那里是单一任务的收尾动作条；
//   · **数据行**内 —— 每行的行内动作（第 20 增量已治）；
//   · **导航区 / 过滤区 / 批量区** —— 那是**导航与查询构造器**，不是竞争动作；
//     ★ 判定走**容器类名**，方向是**收窄**（排除错了 ⇒ 漏判一处违规，
//       不是凭空造出一处假红）；★ 另有**结构性**排除：位于 `<Teleport>` 内的
//       动作区是**浮层**（下拉菜单 / 抽屉内容），逐项弹出，不构成同屏竞争。
//
// ★ 本文件是**判据**（纯函数 + 文件扫描），门在
//   `web/scripts/design-action-density-guard.mjs`。
//
// ★ **复用** `primaryFocus.ts` 的标签树解析（`parseTagTree` / `templateBlocks` /
//   `inFocusRegion` / `rowContainerForm` / `listVueFiles`）—— ★ 同一口径只能有
//   一份实现（LESSONS.md §411）。

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
import { maskSource } from '../i18n/hardcodedCjk.ts'
import {
  type TagNode,
  inFocusRegion,
  listVueFiles,
  parseTagTree,
  rowContainerForm,
  templateBlocks,
} from './primaryFocus.ts'

// ★ 门脚本只从本模块取入口 ⇒ 文件枚举也在此再导出一次，
//   避免门里出现第二个「文件列表」来源（同一口径只能有一份实现）。
export { listVueFiles }

// ───────────────────────── 常量 ─────────────────────────

/**
 * 一个动作区里允许的**最坏情况可见动作数**。
 *
 * ★ 这个 4 不是拍脑袋 —— 它是**实测分布的断层**（`scanActionDensity` 的读数）：
 *   全仓 441 个动作区里，动作数分布是
 *     1 → 281 · 2 → 130 · 3 → 18 · 4 → 7 · 5 → 4 · 9 → 1
 *   ⇒ 1~4 共 436 个（98.9%），从 5 开始是**长尾**（5 个区）⇒
 *   **4 与 5 之间就是那条断层线** ⇒ 「≤4 可扫读、≥5 必须收纳」有实测依据。
 *   ★ 门槛若定 5，超阈值只剩 4 个区且要动 modal 底栏（收益小、风险大）；
 *     定 3 会把 25 个区拖进整改（判据变宽 ⇒ 假红风险）。4 是**窄而准**的那一档。
 */
export const MAX_VISIBLE_ACTIONS = 4

/** 算作「动作」的标签。 */
export const ACTION_TAGS: ReadonlySet<string> = new Set([
  'button',
  'el-button',
  'a',
  'el-link',
  'el-dropdown',
])

/**
 * **会渲染出动作的组件**（组件名 → 它渲染几个可见动作）。
 *
 * ★ 为什么必须显式登记：
 *   源码门读的是**父组件的模板** ⇒ 组件内部的按钮对门是不可见的。
 *   若不登记，`<ActionOverflowMenu>` 的触发按钮就**不计数** ⇒
 *   「把按钮包进组件」会变成绕过门的**后门**（而这个后门还是我们自己开的）。
 *   ⇒ 登记它渲染 **1 个**可见动作（一个「更多」触发器）⇒
 *     父动作区的真实可见数 = 明面按钮数 + 1。
 *   ★ 登记是**白名单**：没登记的组件一律**不计数**（判据宁可窄，不可宽）。
 */
export const ACTION_COMPONENTS: ReadonlyMap<string, number> = new Map([
  // 溢出菜单：渲染 1 个触发器（菜单项在浮层里，逐项弹出，不计入同屏竞争）
  ['actionoverflowmenu', 1],
])

/**
 * 非动作区的容器类名（保守排除）。
 *
 * ★ 按**类名 token** 判定（`-`/`_`/空格分隔），不是子串匹配 ——
 *   子串匹配会把 `navigate`、`filterable`、`tabsomething` 一并误排除。
 */
export const NON_ACTION_ZONE_TOKENS: ReadonlySet<string> = new Set([
  'tab', 'tabs', 'nav', 'navbar', 'menu', 'filter', 'filters',
  'bulk', 'breadcrumb', 'pagination',
  // ★ 「筛选片」形状（class token 含 chip）：一排 chip 是**筛选器**，
  //   不是一组竞争动作（仓内既有：family-quick-row 的厂商快捷筛选、
  //   ActiveFilterChips 的已选条件）。判据按**形状**排除，不看文案。
  'chip',
])

// ───────────────────────── 判定 ─────────────────────────

export interface ActionSlot {
  /** 1 基行号 */
  line: number
  /** 可见文案（取不到就退化成类名/无文案） */
  label: string
  /** 该动作是否处在一条条件链里（互斥分支） */
  conditional: boolean
}

export interface ActionZoneFinding {
  file: string
  /** 动作区（直接父）的起始行（1 基） */
  line: number
  zoneTag: string
  zoneClass: string
  /** 最坏情况下的可见动作数 */
  visibleCount: number
  /** 参与计数的位置（每个「可见位」一条） */
  slots: ActionSlot[]
  reason: string
}

export interface ActionDensityFacts {
  /** 全仓动作控件总数（含被排除的） */
  totalActionControls: number
  /** 参与判定的动作区数 */
  actionZones: number
  /** 超阈值的动作区数（= 判红数） */
  overloadedZones: number
  /** 落在焦点区域内的动作数（排除） */
  excludedFocusRegion: number
  /** 落在数据行内的动作数（排除） */
  excludedDataRow: number
  /** 落在导航/过滤/批量区内的动作数（排除） */
  excludedNonActionZone: number
  /** 落在浮层（Teleport）内的动作数（排除） */
  excludedOverlay: number
  /** 动作数分布（键 = 可见动作数） */
  histogram: Record<string, number>
}

// ── 小工具 ──

/** 取节点上第一个匹配的 v- 指令类型。 */
export function directiveOf(node: TagNode): 'if' | 'else-if' | 'else' | 'show' | null {
  // ★ `v-else-if` 必须排在 `v-else` 前面 —— 否则 `v-else` 会先命中
  //   `v-else-if` 的前缀（`v-else` 后面紧跟 `-` 也是词边界）。
  const m = /\bv-(else-if|if|else|show)\b/.exec(node.attrs)
  if (!m) return null
  return m[1] as 'if' | 'else-if' | 'else' | 'show'
}

/** 节点的 class token 集合。 */
function classTokens(node: TagNode): string[] {
  const cls = node.attrs.match(/class="([^"]*)"/)?.[1] ?? ''
  return cls.split(/[\s_-]+/).filter(Boolean).map((s) => s.toLowerCase())
}

/** 动作区是否属于「导航 / 过滤 / 批量」这类非动作区。 */
export function isNonActionZone(node: TagNode): boolean {
  const tokens = classTokens(node)
  return tokens.some((t) => NON_ACTION_ZONE_TOKENS.has(t))
}

/**
 * 节点是否属于**菜单**（逐项呈现 ⇒ 不与页面上的动作同屏竞争）。
 *
 * ★★★ 这里有一个被真仓变异臂抓出来的**判据缺陷修正**（2026-10-27）：
 *   第一版写的是「节点在 `<Teleport>` 内就排除」。**太宽了** ——
 *   仓里的抽屉（KeysView 的 `drawer-actions`）也是 Teleport 到 body 的，
 *   而它的底栏按钮是**同屏并排**的（5 个）⇒ 那正是本轮要治的形态，
 *   却被整区排除掉了 ⇒ 门对抽屉**完全失明**。
 *   ⇒ 收窄为「**在 `role="menu"` 子树内**」：菜单是**逐项**呈现的（一次只显示一层），
 *     抽屉/浮层面板不是（它们整块同屏）。
 *   ★ 通式：**排除项要按【形状】写，不能按【实现机制】写** ——
 *     「Teleport」是实现机制，「菜单」才是语义。
 */
export function inOverlay(node: TagNode): boolean {
  let p = node.parent
  while (p) {
    const role = (p.attrs.match(/\brole\s*=\s*"([^"]*)"/)?.[1] ?? '').toLowerCase()
    if (role === 'menu' || role === 'listbox') return true
    p = p.parent
  }
  return false
}

/** 节点是否算作一个「动作」。 */
export function isActionNode(node: TagNode): boolean {
  return ACTION_TAGS.has(node.name) || ACTION_COMPONENTS.has(node.name)
}

/** 动作的可见文案。 */
export function labelOf(masked: string, node: TagNode): string {
  if (ACTION_COMPONENTS.has(node.name)) return `<${node.name}>`
  const text = masked
    .slice(node.innerStart, node.innerEnd)
    .replace(/<[^>]*>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  if (text) return text.slice(0, 24)
  const aria = node.attrs.match(/aria-label="([^"]*)"/)?.[1] ?? ''
  const title = node.attrs.match(/title="([^"]*)"/)?.[1] ?? ''
  if (aria) return `[${aria}]`.slice(0, 24)
  if (title) return `[${title}]`.slice(0, 24)
  const cls = node.attrs.match(/class="([^"]*)"/)?.[1] ?? ''
  return cls ? `[${cls.replace(/\s+/g, ' ').trim()}]`.slice(0, 24) : '(无文案)'
}

/**
 * 按 Vue 的**相邻规则**把动作区的子元素折叠成「可见位」。
 *
 * ★ 规则（来自 Vue 3 模板编译器的 `v-else` / `v-else-if` 处理）：
 *   · `v-if` 开一条新链；
 *   · `v-else-if` / `v-else` **紧跟**上一条链时接进同一条链（不新开）；
 *   · 中间夹了别的元素 ⇒ 链断开（Vue 编译期会报错，保守按 2 个可见位算）。
 *   · 一条链**只贡献 1 个可见位**（无论链里有几个动作）。
 *   · `v-show` 不折叠 —— 它只是隐藏，**可以再显示** ⇒ 计入可见。
 *
 * @returns 每个「可见位」对应的代表节点与是否来自条件链
 */
export function visibleSlotsOf(
  zone: TagNode,
  counted: (n: TagNode) => boolean = () => true,
): Array<{ node: TagNode; conditional: boolean }> {
  const slots: Array<{ node: TagNode; conditional: boolean }> = []
  let chainOpen = false
  let chainHasAction = false
  let chainRepresentative: TagNode | null = null

  const flushChain = () => {
    if (chainHasAction && chainRepresentative) slots.push({ node: chainRepresentative, conditional: true })
    chainOpen = false
    chainHasAction = false
    chainRepresentative = null
  }

  for (const child of zone.children) {
    const isAction = isActionNode(child)
    const dir = directiveOf(child)
    if (dir === 'if') {
      flushChain()
      chainOpen = true
      if (isAction && counted(child)) {
        chainHasAction = true
        chainRepresentative = child
      }
    } else if ((dir === 'else-if' || dir === 'else') && chainOpen) {
      // ★ 代表节点取**第一个被计数的**成员 —— 若链首被排除（例如落在数据行里），
      //   仍要由后面的成员代表这条链，否则整条链的可见位会凭空消失。
      if (isAction && counted(child)) {
        chainHasAction = true
        if (!chainRepresentative) chainRepresentative = child
      }
    } else {
      flushChain()
      if (isAction && counted(child)) slots.push({ node: child, conditional: false })
    }
  }
  flushChain()
  return slots
}

/**
 * 扫描一个已遮罩的源文件，找出「可见动作数超阈值」的动作区。
 */
export function findOverloadedZones(masked: string, file: string): ActionZoneFinding[] {
  const out: ActionZoneFinding[] = []
  const nodes: TagNode[] = []
  for (const [s, e] of templateBlocks(masked)) {
    const walk = (ns: TagNode[]) => {
      for (const n of ns) {
        nodes.push(n)
        walk(n.children)
      }
    }
    walk(parseTagTree(masked, s, e))
  }
  if (!nodes.length) return out

  // 按【直接父】分组
  const byParent = new Map<TagNode, TagNode[]>()
  for (const n of nodes) {
    if (!isActionNode(n)) continue
    const p = n.parent
    if (!p) continue
    if (!byParent.has(p)) byParent.set(p, [])
    byParent.get(p)!.push(n)
  }

  for (const [zone, members] of byParent) {
    if (members.length === 0) continue
    // ★ 排除：焦点区域 / 数据行 / 浮层 —— 逐个动作判
    const kept: TagNode[] = []
    for (const m of members) {
      if (inFocusRegion(m) || rowContainerForm(m) !== null || inOverlay(m)) continue
      kept.push(m)
    }
    if (kept.length === 0) continue
    // ★ 排除：导航 / 过滤 / 批量区
    if (isNonActionZone(zone)) continue

    const slots = visibleSlotsOf(zone, (n) => kept.includes(n))
    if (slots.length <= MAX_VISIBLE_ACTIONS) continue

    out.push({
      file,
      line: masked.slice(0, zone.openStart).split('\n').length,
      zoneTag: zone.name,
      zoneClass: (zone.attrs.match(/class="([^"]*)"/)?.[1] ?? '').replace(/\s+/g, ' ').trim(),
      visibleCount: slots.length,
      slots: slots.map((s) => ({
        line: masked.slice(0, s.node.openStart).split('\n').length,
        label: labelOf(masked, s.node),
        conditional: s.conditional,
      })),
      reason:
        `动作区里最坏情况下同时可见 ${slots.length} 个动作（上限 ${MAX_VISIBLE_ACTIONS}）` +
        ` ⇒ 眼睛必须扫一遍才能定位 ⇒ 低频动作要收进「更多」` +
        (slots.some((s) => s.conditional)
          ? '（已折叠互斥条件分支；本计数是**最坏情况**下的同时可见数）'
          : ''),
    })
  }
  return out
}

/**
 * 扫描目录，返回违规明细与读数。
 */
export function scanActionDensity(files: string[], rootDir: string): {
  findings: ActionZoneFinding[]
  facts: ActionDensityFacts
} {
  const findings: ActionZoneFinding[] = []
  let total = 0
  let exFocus = 0
  let exRow = 0
  let exOverlay = 0
  let exNonActionZone = 0
  const histogram: Record<string, number> = {}

  for (const abs of files) {
    const src = readFileSync(abs, 'utf8')
    const masked = maskSource(abs, src)
    const rel = relative(rootDir, abs).replace(/\\/g, '/')

    const nodes: TagNode[] = []
    for (const [s, e] of templateBlocks(masked)) {
      const walk = (ns: TagNode[]) => {
        for (const n of ns) {
          nodes.push(n)
          walk(n.children)
        }
      }
      walk(parseTagTree(masked, s, e))
    }

    const byParent = new Map<TagNode, TagNode[]>()
    for (const n of nodes) {
      if (!isActionNode(n)) continue
      total++
      if (inFocusRegion(n)) { exFocus++; continue }
      if (rowContainerForm(n) !== null) { exRow++; continue }
      if (inOverlay(n)) { exOverlay++; continue }
      const p = n.parent
      if (!p) continue
      if (!byParent.has(p)) byParent.set(p, [])
      byParent.get(p)!.push(n)
    }

    for (const [zone, members] of byParent) {
      if (members.length === 0) continue
      if (isNonActionZone(zone)) {
        // ★ 排除分支的读数要**可见**（负向臂纪律：证明排除是活分支）
        const kept = members.filter(
          (m) => !inFocusRegion(m) && rowContainerForm(m) === null && !inOverlay(m),
        )
        if (kept.length === 0) continue
        exNonActionZone += kept.length
        continue
      }
      const slots = visibleSlotsOf(zone, (n) => members.includes(n))
      const k = String(slots.length)
      histogram[k] = (histogram[k] ?? 0) + 1
    }

    findings.push(...findOverloadedZones(masked, rel))
  }

  return {
    findings,
    facts: {
      totalActionControls: total,
      actionZones: Object.values(histogram).reduce((a, b) => a + b, 0),
      overloadedZones: findings.length,
      excludedFocusRegion: exFocus,
      excludedDataRow: exRow,
      excludedNonActionZone: exNonActionZone,
      excludedOverlay: exOverlay,
      histogram,
    },
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  name: string
  ok: boolean
}

const wrap = (body: string) => `<template>${body}</template>`

export const ACTION_DENSITY_SELFTEST: SelftestCase[] = [
  {
    name: '同一直接父里 5 个并列动作 ⇒ 判红且计数为 5',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 1 && f[0].visibleCount === 5
    })(),
  },
  {
    name: '★ 对照：4 个并列动作 ⇒ 判绿（阈值边界）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★★ 互斥链 v-if / v-else-if / v-else 只算 1 个可见位（静态计数不得虚高）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button v-if="x">B</button><button v-else-if="y">C</button><button v-else>D</button></div>`,
      )
      // A + 一条链 = 2 个可见位 ⇒ 判绿
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 0
    })(),
  },
  {
    name: '★ 对照：把互斥链换成 4 个独立 v-if ⇒ 判红（改一处必须改结论）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button v-if="a">A</button><button v-if="b">B</button><button v-if="c">C</button><button v-if="d">D</button><button v-if="e">E</button></div>`,
      )
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 1 && f[0].visibleCount === 5
    })(),
  },
  {
    name: '★ 链里第一个元素不是动作、后一个是 ⇒ 仍算 1 个可见位',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><span v-if="x">t</span><button v-else>B</button><button>C</button><button>D</button></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 动作区分属不同直接父 ⇒ 各算各的（不合并）',
    ok: (() => {
      const src = wrap(
        `<div class="panel"><div class="a"><button>A</button><button>B</button></div>` +
          `<div class="b"><button>C</button><button>D</button></div></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 导航区（类名 tab / tabs / nav）不参与判定',
    ok: (() => {
      const src = wrap(
        `<div class="tabs"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 过滤区（类名 filter）与批量区（bulk）不参与判定',
    ok: (() => {
      const a = wrap(
        `<div class="filter-group"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      const b = wrap(
        `<div class="bulk-bar"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      return findOverloadedZones(a, 'x.vue').length === 0 && findOverloadedZones(b, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 焦点区域（模态）内的 5 个动作不参与判定',
    ok: (() => {
      const src = wrap(
        `<AppModal><div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div></AppModal>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 数据行内的 5 个动作不参与判定（第 20 增量已治）',
    ok: (() => {
      const src = wrap(
        `<table><tr><td><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></td></tr></table>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 菜单（role=menu）内的动作不参与判定（逐项呈现，不构成同屏竞争）',
    ok: (() => {
      const src = wrap(
        `<Teleport to="body"><div class="action-overflow__menu" role="menu"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div></Teleport>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★★ 抽屉（Teleport 到 body，但**同屏并排**）内的动作**要**参与判定',
    ok: (() => {
      // ★ 这条断言钉住「排除项要按【形状】写，不能按【实现机制】写」：
      //   第一版按「在 Teleport 内」排除 ⇒ 整个抽屉失明（真仓变异臂抓出来的缺陷）。
      const src = wrap(
        `<Teleport to="body"><div class="drawer"><div class="drawer-actions"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div></div></Teleport>`,
      )
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 1 && f[0].visibleCount === 5
    })(),
  },
  {
    name: '★ 判据不含全局状态（同一输入两次结果一致）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      return (
        JSON.stringify(findOverloadedZones(src, 'x.vue')) ===
        JSON.stringify(findOverloadedZones(src, 'x.vue'))
      )
    })(),
  },
  {
    name: '★ 动作区没有类名也能认出来（不靠类名找动作区）',
    ok: (() => {
      const src = wrap(
        `<div><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 1 && f[0].zoneClass === ''
    })(),
  },
  {
    name: '★★ 会渲染动作的组件按 1 个可见动作计（否则「包进组件」就是绕过门的后门）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><ActionOverflowMenu :items="[]" /></div>`,
      )
      const f = findOverloadedZones(src, 'x.vue')
      return f.length === 1 && f[0].visibleCount === 5
    })(),
  },
  {
    name: '★ 对照：3 个按钮 + 溢出菜单组件 ⇒ 判绿（4 个可见位，正好在阈值内）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><ActionOverflowMenu :items="[]" /></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 未登记的组件不计数（白名单：判据宁可窄，不可宽）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><SomeUnknownWidget :items="[]" /></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 判据对「第 5 个动作」敏感（少一个 ⇒ 不判红）',
    ok: (() => {
      const five = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button><button>E</button></div>`,
      )
      const four = wrap(
        `<div class="bar"><button>A</button><button>B</button><button>C</button><button>D</button></div>`,
      )
      return (
        findOverloadedZones(five, 'x.vue').length === 1 &&
        findOverloadedZones(four, 'x.vue').length === 0
      )
    })(),
  },
  {
    name: '★ `v-else-if` 不被误读成 `v-else`（指令解析顺序）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button v-if="a">A</button><button v-else-if="b">B</button><button v-else>C</button><button>D</button><button>E</button></div>`,
      )
      // A + (B/C 互斥链) + D + E = 4 ⇒ 判绿
      return findOverloadedZones(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ `v-show` 不折叠（可以再显示 ⇒ 计入可见）',
    ok: (() => {
      const src = wrap(
        `<div class="bar"><button v-show="a">A</button><button v-show="b">B</button><button v-show="c">C</button><button v-show="d">D</button><button v-show="e">E</button></div>`,
      )
      return findOverloadedZones(src, 'x.vue').length === 1
    })(),
  },
]

export function runActionDensitySelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of ACTION_DENSITY_SELFTEST) {
    if (c.ok) passed++
    else failed.push(c.name)
  }
  return { passed, total: ACTION_DENSITY_SELFTEST.length, failed }
}
