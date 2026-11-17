// stageTreeGlyphs.ts —— 阶段树【状态字形】的唯一真源（2026-11-18）
//
// ★★ 为什么单独一个文件（而不是各组件各写一份）：
//   设计规则 15（docs/audit/2026-11-16-live阶段树最终方案说明.md:61）：
//   「不要标记勾勾，这样看起来整个紧凑模式上更简洁，不乱」
//     ⇒ 紧凑模式**去掉全部 ✓**：瓦片角标改用 `CGLYPH`（done:'')
//     ⇒ **图例同步**（原型 `syncLegendGlyphs()` 真的换掉字形，**不是透明化**
//       ——透明化会让 ✓ 仍留在 DOM / 无障碍文本里）
//     ⇒ 实测口径：`document.body.innerText` 中 **✓ 出现 0 次**；
//       图例 done/pending/unknown 字形为空（**图例与画面对得上**）
//
//   ★ 本轮实测到线上缺陷（F1）：瓦片用了紧凑字形表，**图例却仍是固定数组**
//     ⇒ 同一条规定写了两份 ⇒ 必然漂移 ⇒ 生产页面出现「图例说 ✓、画面没 ✓」
//     ⇒ **结构性修法＝单一真源**：瓦片与图例都从这里取。
//
// ★ 为什么「透明化」不算数（原型注释的原文理由）：
//   `color: transparent` 时 ✓ **仍在 DOM 与无障碍文本里**
//   ⇒ 屏幕阅读器仍会读出「完成」⇒ 而画面上什么都没有 ⇒ **图例与画面对不上**。
//   ⇒ 正确做法是**把字形换成空串**（真的没有那个字符）。

import type { StageState } from '../types/liveStageTree'

/** 完整模式的七态字形（设计第 4 轮：七态各有一形）。 */
export const FULL_GLYPH: Readonly<Record<StageState, string>> = {
  done: '✓',
  active: '◐',
  pending: '○',
  skipped: '⌀',
  failed: '✕',
  warned: '⚠',
  unmeasured: '⋯',
}

/**
 * 紧凑模式的七态字形（设计规则 15 / 原型第 8 轮）：
 *   done / pending / unmeasured **换成空串**（不是透明化）；
 *   active ◐ / skipped ⌀ / failed ✕ / warned ⚠ **保留**
 *   （一行里只出现一两次，留着不乱）。
 */
export const COMPACT_GLYPH: Readonly<Record<StageState, string>> = {
  done: '',
  active: '◐',
  pending: '',
  skipped: '⌀',
  failed: '✕',
  warned: '⚠',
  unmeasured: '',
}

/** 按模式取字形表。 */
export function glyphSetFor(compact: boolean): Readonly<Record<StageState, string>> {
  return compact ? COMPACT_GLYPH : FULL_GLYPH
}

/** 单态字形（紧凑模式下未在表内的态 ⇒ 空串，不兜底成 '·'）。 */
export function glyphFor(state: StageState, compact: boolean): string {
  return glyphSetFor(compact)[state] ?? ''
}

/** 图例用：七态的展示顺序（图例行按「完成→进行中→未到达→…」排列）。 */
export const LEGEND_ORDER: readonly StageState[] = [
  'done',
  'active',
  'pending',
  'skipped',
  'failed',
  'warned',
  'unmeasured',
]
