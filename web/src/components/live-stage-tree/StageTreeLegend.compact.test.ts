// StageTreeLegend.compact.test.ts —— 图例【字形随模式切换】的组件级钉测（2026-11-18）
//
// ★ 这个文件钉的是一条**真实上线过的缺陷**（F1，验证方在 154 线上发现）：
//   设计规则 15（docs/audit/2026-11-16-live阶段树最终方案说明.md:61）
//     「不要标记勾勾，这样看起来整个紧凑模式上更简洁，不乱」
//     ⇒ 紧凑模式**去掉全部 ✓**，**图例同步**（原型 syncLegendGlyphs() 真的换掉字形，
//       **不做透明化** —— 透明化会让 ✓ 仍留在 DOM / 无障碍文本里）
//     ⇒ 验收口径：`document.body.innerText` 中 **✓ 出现 0 次**；
//       图例 done/pending/unknown 字形为空（**图例与画面对得上**）
//
//   缺陷形态：瓦片用紧凑字形表、**图例用固定数组** ⇒ 同一条规定写了两份 ⇒ 必然漂移
//   ⇒ 生产页面出现「图例说 ✓、画面没 ✓」（实测全页 ✓ 计数 = 1）。
//
// ★ 与 stageTreeGlyphs.designParity.test.ts 的分工：
//   那份钉的是**字形表本身**；这份钉的是**组件真的用了那张表**
//   （变异验证：把图例改回固定数组 ⇒ 本文件变红，而那份仍绿 ⇒ 两份合起来才无盲区）。

import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import zhCN from '../../locales/zh-CN'
import StageTreeLegend from './StageTreeLegend.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})

function glyphsOf(compact: boolean): Record<string, string> {
  const w = mount(StageTreeLegend, {
    props: { compact },
    global: { plugins: [i18n] },
  })
  const out: Record<string, string> = {}
  w.findAll('.legend__glyph').forEach((el) => {
    const cls = String(el.classes().find((c) => c.startsWith('is-')) ?? '')
    out[cls.replace(/^is-/, '')] = (el.text() ?? '').trim()
  })
  w.unmount()
  return out
}

describe('图例字形随模式切换（设计规则 15）', () => {
  it('★ 完整模式：图例七态字形齐全，「完成」是 ✓（对照组）', () => {
    const g = glyphsOf(false)
    expect(Object.keys(g)).toHaveLength(7)
    expect(g.done).toBe('✓')
    expect(g.active).toBe('◐')
    expect(g.pending).toBe('○')
    expect(g.skipped).toBe('⌀')
    expect(g.failed).toBe('✕')
    expect(g.warned).toBe('⚠')
    expect(g.unmeasured).toBe('⋯')
  })

  it('★★ 紧凑模式：done / pending / unmeasured 的字形是【空串】（图例与画面对得上）', () => {
    const g = glyphsOf(true)
    expect(g.done).toBe('')
    expect(g.pending).toBe('')
    expect(g.unmeasured).toBe('')
  })

  it('★ 紧凑模式：active / skipped / failed / warned 保留字形', () => {
    const g = glyphsOf(true)
    expect(g.active).toBe('◐')
    expect(g.skipped).toBe('⌀')
    expect(g.failed).toBe('✕')
    expect(g.warned).toBe('⚠')
  })

  it('★★ 紧凑模式：整行图例文本里【一个 ✓ 都没有】（设计原文：全页 ✓ 出现 0 次）', () => {
    const w = mount(StageTreeLegend, { props: { compact: true }, global: { plugins: [i18n] } })
    const text = w.text() ?? ''
    expect(text.includes('✓')).toBe(false)
    w.unmount()
  })

  it('★ 七态的【文字标签】在两种模式都在（图例不能整个消失）', () => {
    for (const compact of [false, true]) {
      const w = mount(StageTreeLegend, { props: { compact }, global: { plugins: [i18n] } })
      const text = w.text() ?? ''
      expect(text).toContain('已完成')
      expect(text).toContain('进行中')
      expect(text).toContain('未到达')
      expect(text).toContain('未测量')
      w.unmount()
    }
  })
})

describe('结构性钉测：同一条规定只写一份（防漂移）', () => {
  const here = dirname(fileURLToPath(import.meta.url))

  it('★ 图例组件不得自带字形字面量（必须来自 lib/stageTreeGlyphs.ts）', () => {
    const src = readFileSync(join(here, 'StageTreeLegend.vue'), 'utf8')
    // 去掉注释行后再找字面量（注释里可以写「✓」解释设计）
    const code = src
      .split('\n')
      .filter((l) => !l.trim().startsWith('//') && !l.trim().startsWith('*') && !l.trim().startsWith('/*'))
      .join('\n')
    for (const g of ['✓', '◐', '○', '⌀', '✕', '⚠', '⋯']) {
      expect(code.includes(`'${g}'`)).toBe(false)
    }
    expect(code.includes('stageTreeGlyphs')).toBe(true)
  })

  it('★ 带组件（瓦片角标）同样必须来自同一张表', () => {
    const src = readFileSync(join(here, 'StageTreeBand.vue'), 'utf8')
    const code = src
      .split('\n')
      .filter((l) => !l.trim().startsWith('//') && !l.trim().startsWith('*') && !l.trim().startsWith('/*'))
      .join('\n')
    for (const g of ['✓', '◐', '○', '⌀', '✕', '⚠', '⋯']) {
      expect(code.includes(`'${g}'`)).toBe(false)
    }
    expect(code.includes('stageTreeGlyphs')).toBe(true)
  })
})
