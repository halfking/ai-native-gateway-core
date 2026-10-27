// glyphIconsLocale.test.ts —— 第 29 增量的门与纯函数核心的 vitest 覆盖。
//
// ★ 覆盖两件东西：
//   ① `glyphIconCore.ts` 的纯函数（字符判定 / 图标位判定 / 清理）；
//   ② `glyphIconsLocale.ts` 的门口径（图标位 vs 文字标记）与自证。
//
// ★ 为什么要有这一份：门脚本的 `--selftest` 走的是 spawn + strip-types 路径，
//   而 vitest 走的是**直接 import** 路径 ⇒ 两条路径都要被覆盖
//   （★ 第 11 增量的教训：「同一口径只能有一份实现」⇒ 但**两条加载路径**也要各自成立）。

import { describe, expect, it } from 'vitest'
import {
  glyphIconCandidates,
  glyphIconSlotViolation,
  stripGlyphIcons,
} from './glyphIconCore.ts'
import {
  detectLocaleGlyphIcons,
  runLocaleGlyphSelftest,
} from './glyphIconsLocale.ts'

describe('glyphIconCore —— 字形字符判定', () => {
  it('emoji 本体算字形', () => {
    expect(glyphIconCandidates('🧹 VACUUM')).toEqual(['🧹'])
    expect(glyphIconCandidates('✅ 可达')).toEqual(['✅'])
    expect(glyphIconCandidates('⚠️ 警告')).toEqual(['⚠'])
  })

  it('★ 流程箭头不是字形（协议互转语义不能被当图标）', () => {
    expect(glyphIconCandidates('Anthropic ↔ OpenAI')).toEqual([])
    expect(glyphIconCandidates('↑ 3% / ↓ 1%')).toEqual([])
  })

  it('★ 排印符号不是字形', () => {
    expect(glyphIconCandidates('延迟 — 正常…')).toEqual([])
  })

  it('★ ★(U+2605) 不是 Extended_Pictographic ⇒ 仓内 172 处「★ 作危险标记」天然不命中', () => {
    expect(glyphIconCandidates('★ 危险操作')).toEqual([])
  })

  it('修饰符（U+FE0F）不单独算命中', () => {
    expect(glyphIconCandidates('️')).toEqual([])
  })
})

describe('glyphIconCore —— 图标位判定（仓内既有口径）', () => {
  it('整句就是一个字形 ⇒ 是图标位', () => {
    expect(glyphIconSlotViolation('🧹')).toEqual(['🧹'])
    expect(glyphIconSlotViolation('📤 ')).toEqual(['📤'])
  })

  it('★ 字形 + 文本 ⇒ 是文字标记（仓内既有惯例，判绿）', () => {
    expect(glyphIconSlotViolation('✅ 可达')).toEqual([])
    expect(glyphIconSlotViolation('⚠️ 警告')).toEqual([])
    expect(glyphIconSlotViolation('⏭️ 跳过 {n}')).toEqual([])
  })
})

describe('glyphIconCore —— 清理', () => {
  it('删掉 emoji 且不留残渣空格', () => {
    expect(stripGlyphIcons('🧹 VACUUM')).toBe('VACUUM')
    expect(stripGlyphIcons('⚠️ 警告')).toBe('警告')
    expect(stripGlyphIcons('是（⚠️ 锁表影响所有子表）')).toBe('是（锁表影响所有子表）')
  })

  it('★ 清理不动流程箭头与占位符', () => {
    expect(stripGlyphIcons('Anthropic ↔ OpenAI {n}')).toBe('Anthropic ↔ OpenAI {n}')
  })
})

describe('语言包侧门', () => {
  it('★ 自证全过（量具有牙）', () => {
    const r = runLocaleGlyphSelftest()
    expect(r.failed).toEqual([])
    expect(r.passed).toBe(r.total)
  })

  it('★ 真仓现状：0 处图标位（门是防回归闸，不是清存量）', () => {
    const findings = detectLocaleGlyphIcons()
    expect(findings.map((f) => `${f.locale}/${f.key}`)).toEqual([])
  })
})
