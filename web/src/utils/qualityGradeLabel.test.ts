// qualityGradeLabel.test.ts — 质量等级标签的取值与兜底语义（2026-10-25）
//
// 为什么需要这道测试：等级文案搬进语言包后，取词逻辑集中到
// `utils/qualityGradeLabel.ts`。它有三条**语义**是「设计」而不是「实现细节」：
//   ① 五个已知等级 → 语言包里的词（common.terms.qualityGrade.*）
//   ② 未知等级 → **回显原字母**（字母是稳定 code，永远能显示）
//   ③ 等级缺失 → 用**调用点自己的占位**（表格空串 / 详情页「—」）
//
// ★ 与 i18n 结构门的关系：键在 8 个语种里都存在这件事由 localeKeyParity 管；
//   本测试管的是「**取值与兜底**」——结构对了但语义错了，那些门看不出来。

import { describe, it, expect, beforeAll } from 'vitest'
import { i18n } from '../i18n'
import { qualityGradeLabel } from './qualityGradeLabel'

describe('质量等级标签（qualityGradeLabel）', () => {
  beforeAll(() => {
    // 应用实例的初始语种取决于浏览器语言（jsdom 里 navigator.language = en-US）
    // ⇒ 应用的 defaultLocale() 把它映射成运行时码 'en'。显式钉成 zh-CN，
    // 否则断言会随运行环境变（这正是「半绿不绿」的来源）。
    //
    // ★ 注意：**运行时语种码 ≠ 目录名**。目录叫 en-US / ja-JP / de-DE…，
    //   而 i18n 实例里的码是 en / ja / de / es / fr / ar / zh-TW / zh-CN。
    //   写成 'en-US' 会因为「没这个语种」而落到 fallbackLocale:'en' ——
    //   断言照样绿，但绿的理由是错的（半绿不绿）。
    i18n.global.locale.value = 'zh-CN'
  })

  it('五个已知等级取语言包里的词（zh-CN）', () => {
    expect(qualityGradeLabel('S')).toBe('最优')
    expect(qualityGradeLabel('A')).toBe('优秀')
    expect(qualityGradeLabel('B')).toBe('良好')
    expect(qualityGradeLabel('C')).toBe('一般')
    expect(qualityGradeLabel('D')).toBe('较差')
  })

  it('★ 未知等级回显原字母（稳定 code 永远能显示）', () => {
    expect(qualityGradeLabel('X')).toBe('X')
    expect(qualityGradeLabel('F')).toBe('F')
  })

  it('等级缺失时用调用点给的占位（表格空串 / 详情页「—」）', () => {
    expect(qualityGradeLabel(undefined)).toBe('')
    expect(qualityGradeLabel(null)).toBe('')
    expect(qualityGradeLabel(undefined, '—')).toBe('—')
    expect(qualityGradeLabel('', '—')).toBe('—')
  })

  it('★ 换语种后取到的是该语种的词（不是写死的英文）', () => {
    // 用真实运行时码 'en'（不是目录名 'en-US'）
    i18n.global.locale.value = 'en'
    // 守卫：这个语种的消息**真的加载了**，而不是靠 fallbackLocale 蒙对。
    // 少了这一句，把语种码写错也会绿（半绿不绿）。
    expect(i18n.global.te('common.terms.qualityGrade.s')).toBe(true)
    expect(qualityGradeLabel('S')).toBe('Best')
    expect(qualityGradeLabel('D')).toBe('Poor')
    i18n.global.locale.value = 'zh-CN'
    expect(qualityGradeLabel('S')).toBe('最优')
  })
})
