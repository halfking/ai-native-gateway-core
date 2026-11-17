// stageTreeGlyphs.designParity.test.ts —— 阶段树【状态字形】的设计对齐钉测（2026-11-18）
//
// ★ 这个文件钉的是一条**真实上线过的缺陷**（验证方在 154 线上发现的 F1）：
//   设计规则 15（docs/audit/2026-11-16-live阶段树最终方案说明.md:61）
//     「不要标记勾勾，这样看起来整个紧凑模式上更简洁，不乱」
//     ⇒ 紧凑模式**去掉全部 ✓**：瓦片角标改用 CGLYPH（done:''）
//     ⇒ **图例同步**（原型 syncLegendGlyphs() 真的换掉字形，**不做透明化**——
//       透明化会让 ✓ 仍留在 DOM / 无障碍文本里 ⇒ 屏幕阅读器仍读出「完成」）
//     ⇒ 验收口径：`document.body.innerText` 中 **✓ 出现 0 次**；
//       图例 done/pending/unknown 字形为空（**图例与画面对得上**）
//
//   缺陷形态：瓦片用了紧凑字形表，**图例却是固定数组** ⇒ 同一条规定写了两份
//   ⇒ 必然漂移 ⇒ 生产页面出现「图例说 ✓、画面没 ✓」（实测：全页 ✓ 计数 = 1）。
//
// ★ 判据纪律：每条都必须满足「**把被测逻辑改回缺陷形态，它立刻变红**」。

import { describe, expect, it } from 'vitest'
import { COMPACT_GLYPH, FULL_GLYPH, LEGEND_ORDER, glyphFor, glyphSetFor } from './stageTreeGlyphs'
import type { StageState } from '../types/liveStageTree'

const ALL_STATES: StageState[] = [
  'done',
  'active',
  'pending',
  'skipped',
  'failed',
  'warned',
  'unmeasured',
]

describe('完整模式字形（设计第 4 轮：七态各有一形）', () => {
  it('★ 七态都有非空字形，且互不相同', () => {
    const glyphs = ALL_STATES.map((s) => FULL_GLYPH[s])
    expect(glyphs.every((g) => g.length > 0)).toBe(true)
    expect(new Set(glyphs).size).toBe(7)
  })

  it('★ 完成态是 ✓（完整模式的对照组）', () => {
    expect(FULL_GLYPH.done).toBe('✓')
  })
})

describe('紧凑模式字形（设计规则 15：不要标记勾勾）', () => {
  it('★ done / pending / unmeasured 换成【空串】（不是透明化 —— 透明化会让 ✓ 留在 DOM 与无障碍文本里）', () => {
    expect(COMPACT_GLYPH.done).toBe('')
    expect(COMPACT_GLYPH.pending).toBe('')
    expect(COMPACT_GLYPH.unmeasured).toBe('')
  })

  it('★ active / skipped / failed / warned 保留字形（一行里只出现一两次，留着不乱）', () => {
    expect(COMPACT_GLYPH.active).toBe('◐')
    expect(COMPACT_GLYPH.skipped).toBe('⌀')
    expect(COMPACT_GLYPH.failed).toBe('✕')
    expect(COMPACT_GLYPH.warned).toBe('⚠')
  })

  it('★★ 紧凑模式【没有 ✓】：整张表里一个 ✓ 都没有（设计原文：全页 ✓ 出现 0 次）', () => {
    expect(Object.values(COMPACT_GLYPH).some((g) => g.includes('✓'))).toBe(false)
  })

  it('★ 图例与瓦片共用同一张表 ⇒ 同一条规定只有一份 ⇒ 不会再漂移（F1 的根因）', () => {
    // 缺陷形态：图例自己写一份固定数组（done:'✓'）⇒ 紧凑模式下与瓦片不一致。
    for (const s of ALL_STATES) {
      expect(glyphFor(s, true)).toBe(COMPACT_GLYPH[s])
      expect(glyphFor(s, false)).toBe(FULL_GLYPH[s])
    }
  })
})

describe('取表口径', () => {
  it('★ glyphSetFor(compact) 返回对应的那张表', () => {
    expect(glyphSetFor(true)).toBe(COMPACT_GLYPH)
    expect(glyphSetFor(false)).toBe(FULL_GLYPH)
  })

  it('★ 图例顺序是七态且不重不漏', () => {
    expect(LEGEND_ORDER).toHaveLength(7)
    expect(new Set(LEGEND_ORDER).size).toBe(7)
    for (const s of ALL_STATES) expect(LEGEND_ORDER).toContain(s)
  })
})
