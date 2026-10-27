// primaryClarity.ts — 「主色必须唯一」的可执行判据（2026-10-27 · 第 22 增量）
//
// ★ 要治的病（仓内明文规范 vs 真仓现状的偏离）：
//   impeccable `skill/reference/distill.md`：
//     「**Clear hierarchy: ONE primary action, few secondary actions,
//       everything else tertiary or hidden**」
//     「**Clear next action: ONE obvious next action, not five competing ones**」
//   impeccable `skill/reference/colorize.md`：
//     「**Keep the primary action easy to find; do not spend its color on decoration.**」
//   仓内 `DESIGN.md` §视觉世界：「状态用最小的颜色量表达、**每个元素都承担信息职责**」
//
//   而真仓现状（截图取证）：
//     · `views/FreePoolView.vue` 的页头工具栏里**四个并排的实心主色按钮**
//       （快速录入 / 写入 DB 凭据 / 添加 Provider / 多 Key 批量池化）
//       ⇒ 四个等重的「主动作」= **没有主动作**；
//     · `views/ProxyView.vue` 的「状态概览」动作区里**两个并排的实心主色按钮**
//       （批量探活全部节点 / 强制切流到最优节点）
//       ⇒ 而**同一个文件的另外三个动作区**（订阅管理 / 节点管理 / 选择策略）
//         都是「一个主色 + 其余次级」⇒ 状态概览这处是**该文件内部的不一致**。
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **一个「动作区」里，最多一个实心主色。**
//   —— 主色是**稀缺资源**；两个并排的实心主色 = 两个「主动作」⇒ 焦点被稀释回「每行都一样」。
//
// ★ 「动作区」的口径 = **直接父容器**（一组并排按钮的共同父节点）。
//   ★★★ 为什么不用更宽的口径（实测过三套）：
//     ① **祖先口径**（最近的含 ≥2 个实心主色后代的祖先）⇒ 命中 32 个「动作区」/ 107 个按钮
//        —— 但那多半是**整页容器**（`.users-page` / `.work-types-view` / `.cred-models` …）
//        ⇒ 「一页多区、每区各有自己的主动作」是**合法的**（`colorize.md` 允许
//          「the strongest color own a **deliberate region** or role」）⇒ 假阳性爆炸；
//     ② **类名口径**（最近的 class 含 toolbar/actions 的祖先）⇒ 只命中 1 处
//        —— 因为 `FreePoolView` 的工具栏 div **没有类名** ⇒ **靠类名找动作区会漏**；
//     ③ **含控件祖先口径**（最近的含 ≥2 个交互控件的祖先）⇒ 命中 3 处，
//        但第三处 `components/RequestTracePanel.vue` 是**假阳性**：
//        「生成提示词」在 `.ai-actions`、「复制」在 `.ai-result-header`
//        （且后者只有生成结果后才出现）⇒ 分属**不同子区域**，不构成竞争。
//     ⇒ **直接父**既是「一排并排按钮」的最严格定义，又不依赖类名 ⇒ 改类名不会让门失明。
//
// ★ 排除（复用第 20 增量的两条，不新增）：
//   · **焦点区域**（模态 / 抽屉）内 —— 那里主色属于「这个浮层的主动作」；
//   · **数据行**内 —— 第 20 增量已治（`primaryFocus.ts` 的行容器形态）。
//
// ★ 「刻意区域」**天然满足**：卡片自己的主色按钮独自占据一个动作区 ⇒ ≤1 ⇒ 不判。
//
// ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-primary-clarity-guard.mjs`，
//   运行时尺在 `web/scripts/design-primary-clarity-runtime-check.mjs`。
//
// ★ **复用** `primaryFocus.ts` 的实心主色判定（`isSolidPrimary` / `inFocusRegion` /
//   `rowContainerForm`）—— ★ 同一口径只能有一份实现（LESSONS.md §411）。

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
import { maskSource } from '../i18n/hardcodedCjk.ts'
import {
  type TagNode,
  inFocusRegion,
  isSolidPrimary,
  listVueFiles,
  parseTagTree,
  rowContainerForm,
  templateBlocks,
} from './primaryFocus.ts'

// ───────────────────────── 判定 ─────────────────────────
export interface PrimaryClarityFinding {
  file: string
  /** 主色按钮所在行（1 基） */
  line: number
  /** 动作区（直接父）的类名（可能为空） */
  zoneClass: string
  /** 动作区（直接父）的标签名 */
  zoneTag: string
  /** 该动作区里的实心主色按钮数 */
  primaryCount: number
  /** 每个实心主色按钮的行号与开标签 */
  buttons: Array<{ line: number; snippet: string }>
  reason: string
}

export interface PrimaryClarityFacts {
  /** 违规动作区数（= 判红数） */
  competingZones: number
  /** 涉及的动作区里实心主色按钮总数 */
  competingButtons: number
  /** 全仓实心主色按钮总数（读数用） */
  totalSolidPrimary: number
  /** 落在焦点区域内的数量（排除，读数用） */
  focusRegionSolidPrimary: number
  /** 落在数据行内的数量（排除，读数用） */
  dataRowSolidPrimary: number
}

/**
 * 扫描一个已遮罩的源文件，找出「同一动作区里并排多个实心主色」的位置。
 */
export function findCompetingPrimaries(masked: string, file: string): PrimaryClarityFinding[] {
  const out: PrimaryClarityFinding[] = []
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

  // 按【直接父】分组实心主色
  const byParent = new Map<TagNode, TagNode[]>()
  for (const n of nodes) {
    if (!isSolidPrimary(n)) continue
    if (inFocusRegion(n)) continue
    if (rowContainerForm(n) !== null) continue
    const p = n.parent
    if (!p) continue
    if (!byParent.has(p)) byParent.set(p, [])
    byParent.get(p)!.push(n)
  }

  for (const [zone, list] of byParent) {
    if (list.length < 2) continue
    const buttons = list.map((n) => {
      const line = masked.slice(0, n.openStart).split('\n').length
      return {
        line,
        snippet: masked.slice(n.openStart, n.openEnd).replace(/\s+/g, ' ').slice(0, 120),
      }
    })
    const zoneClass = (zone.attrs.match(/class="([^"]*)"/)?.[1] ?? '').replace(/\s+/g, ' ').trim()
    out.push({
      file,
      line: buttons[0].line,
      zoneClass,
      zoneTag: zone.name,
      primaryCount: list.length,
      buttons,
      reason:
        `同一个动作区（<${zone.name}${zoneClass ? ' class="' + zoneClass + '"' : ''}>）里并排 ` +
        `${list.length} 个实心主色按钮 ⇒ ${list.length} 个「主动作」= 没有主动作`,
    })
  }
  return out
}

/**
 * 扫描目录，返回违规明细与读数。
 */
export function scanPrimaryClarity(files: string[], rootDir: string): {
  findings: PrimaryClarityFinding[]
  facts: PrimaryClarityFacts
} {
  const findings: PrimaryClarityFinding[] = []
  let total = 0
  let focus = 0
  let row = 0
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
    for (const n of nodes) {
      if (!isSolidPrimary(n)) continue
      total++
      if (inFocusRegion(n)) {
        focus++
        continue
      }
      if (rowContainerForm(n) !== null) row++
    }
    findings.push(...findCompetingPrimaries(masked, rel))
  }
  return {
    findings,
    facts: {
      competingZones: findings.length,
      competingButtons: findings.reduce((s, f) => s + f.primaryCount, 0),
      totalSolidPrimary: total,
      focusRegionSolidPrimary: focus,
      dataRowSolidPrimary: row,
    },
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  name: string
  ok: boolean
}

/** 最小可用模板：把一段按钮片段包进文件里。 */
const wrap = (body: string) => `<template>${body}</template>`

export const PRIMARY_CLARITY_SELFTEST: SelftestCase[] = [
  {
    name: '同一直接父里两个实心主色 ⇒ 判红',
    ok: (() => {
      const src = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></div>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 1
    })(),
  },
  {
    name: '★ 对照：一个主色 + 一个次级 ⇒ 判绿',
    ok: (() => {
      const src = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-secondary">B</button></div>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 对照：两个主色但**直接父不同**（分属不同子区域）⇒ 判绿',
    ok: (() => {
      const src = wrap(
        `<div class="panel"><div class="a"><button class="btn btn-primary">A</button></div>` +
          `<div class="b"><button class="btn btn-primary">B</button></div></div>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ Element Plus 的 `link` 变体不算实心主色 ⇒ 判绿',
    ok: (() => {
      const src = wrap(
        `<div class="actions"><el-button type="primary">A</el-button><el-button type="primary" link>B</el-button></div>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 焦点区域（模态）内的两个实心主色 ⇒ 判绿（浮层自己的主动作）',
    ok: (() => {
      const src = wrap(
        `<AppModal><div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></div></AppModal>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 数据行内的实心主色不参与本判据（第 20 增量已治）⇒ 判绿',
    ok: (() => {
      const src = wrap(
        `<table><tr><td><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></td></tr></table>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 三个实心主色 ⇒ 判红且计数为 3',
    ok: (() => {
      const src = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button><button class="btn btn-primary">C</button></div>`,
      )
      const f = findCompetingPrimaries(src, 'x.vue')
      return f.length === 1 && f[0].primaryCount === 3
    })(),
  },
  {
    name: '★ 两个互相独立的动作区各有一个主色 ⇒ 判绿（一页多区各有一个主动作是合法的）',
    ok: (() => {
      const src = wrap(
        `<div class="card-a"><button class="btn btn-primary">A</button></div><div class="card-b"><button class="btn btn-primary">B</button></div>`,
      )
      return findCompetingPrimaries(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 判据不含全局状态（同一输入两次结果一致）',
    ok: (() => {
      const src = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></div>`,
      )
      const a = findCompetingPrimaries(src, 'x.vue')
      const b = findCompetingPrimaries(src, 'x.vue')
      return JSON.stringify(a) === JSON.stringify(b)
    })(),
  },
  {
    name: '★ 判据对「第二个按钮是否为主色」敏感（改一处必须改结论）',
    ok: (() => {
      const both = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></div>`,
      )
      const one = wrap(
        `<div class="actions"><button class="btn btn-primary">A</button><button class="btn btn-secondary">B</button></div>`,
      )
      return (
        findCompetingPrimaries(both, 'x.vue').length === 1 &&
        findCompetingPrimaries(one, 'x.vue').length === 0
      )
    })(),
  },
  {
    name: '★ 动作区没有类名也能认出来（不靠类名找动作区）',
    ok: (() => {
      const src = wrap(
        `<div><button class="btn btn-primary">A</button><button class="btn btn-primary">B</button></div>`,
      )
      const f = findCompetingPrimaries(src, 'x.vue')
      return f.length === 1 && f[0].zoneClass === ''
    })(),
  },
]

export function runPrimaryClaritySelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of PRIMARY_CLARITY_SELFTEST) {
    if (c.ok) passed++
    else failed.push(c.name)
  }
  return { passed, total: PRIMARY_CLARITY_SELFTEST.length, failed }
}

// ───────────────────────── 目录扫描便捷函数 ─────────────────────────

export { listVueFiles }
