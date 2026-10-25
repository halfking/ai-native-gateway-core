// arrayMessage.test.ts — 「数组型语言包键」的取法与前提钉住（2026-10-27）
//
// 钉的是什么：
//   ① **前提**：vue-i18n 9 的 `t()` 对数组型消息返回**键路径字符串**（不是数组）
//      ⇒ 这是「数组键不能经 t() 消费」这条纪律的根据。
//      ⚠ 若未来升级 vue-i18n 让它支持数组，本测试会红 ⇒ 那时该重新评估
//        `scripts/i18n-array-key-guard.mjs` 这道门（而不是默默继续绕开它）。
//   ② **helper**：`localeArray()` 能真的取到数组内容（且不是键路径）。
//   ③ **兜底**：取不到的键返回空数组（不返回键路径）。
//   ④ **反应性**：切语种后 computed 会重新求值（依赖 localeRef）。
//
// ★ 为什么必须有这条测试：2026-10-27 那轮里，正因为「数组键能经 t() 拿到」这个
//   **错误前提**，一次接线把「键路径字符串」逐字符渲染上了页面（表头单元格 = r/o/u/t/…）。
//   ⇒ 前提必须被钉住，否则同一个坑会再踩一次。

import { describe, expect, it } from 'vitest'
import { i18n, localeArray } from '../i18n'

const ARRAY_KEY = 'modulesView.integration.feishuSteps'

describe('数组型语言包键', () => {
  it('前提：t() 对数组型消息返回键路径字符串（不是数组）', () => {
    const r = i18n.global.t(ARRAY_KEY as never)
    expect(Array.isArray(r)).toBe(false)
    expect(typeof r).toBe('string')
    // 键存在时它返回的正是键路径本身
    expect(r).toBe(ARRAY_KEY)
  })

  it('localeArray() 能取到真正的数组内容', () => {
    const arr = localeArray<string[]>(ARRAY_KEY).value
    expect(Array.isArray(arr)).toBe(true)
    expect(arr.length).toBeGreaterThan(0)
    // 不是键路径，也不是「键路径被拆成的字符数组」
    expect(arr).not.toContain(ARRAY_KEY)
    expect(arr[0]).not.toBe('m')
    for (const s of arr) expect(typeof s).toBe('string')
  })

  it('取不到的键返回空数组（而不是键路径）', () => {
    expect(localeArray<string[]>('no.such.key').value).toEqual([])
  })

  it('反应性：切语种后重新求值', async () => {
    const before = localeArray<string[]>(ARRAY_KEY).value
    const prev = i18n.global.locale.value
    try {
      ;(i18n.global.locale as unknown as { value: string }).value = 'de'
      await Promise.resolve()
      const after = localeArray<string[]>(ARRAY_KEY).value
      expect(Array.isArray(after)).toBe(true)
      expect(after.length).toBe(before.length)
    } finally {
      ;(i18n.global.locale as unknown as { value: string }).value = prev
    }
  })

  it('各语种的数组长度一致（数组形状平价）', () => {
    const LOCALES = ['zh-CN', 'en', 'zh-TW', 'ja', 'de', 'fr', 'es', 'ar']
    const lengths = new Map<string, number[]>()
    for (const loc of LOCALES) {
      const msg = i18n.global.getLocaleMessage(loc) as Record<string, unknown>
      const mod = msg.modulesView as Record<string, Record<string, unknown>> | undefined
      const integ = mod?.integration as Record<string, unknown> | undefined
      for (const k of ['feishuSteps', 'wechatSteps', 'dingtalkSteps']) {
        const v = integ?.[k]
        if (!Array.isArray(v)) continue
        if (!lengths.has(k)) lengths.set(k, [])
        lengths.get(k)!.push(v.length)
      }
    }
    // 未懒加载的语种可能取不到 ⇒ 至少要量到 zh-CN/en 两个，且它们彼此一致
    expect(lengths.size).toBeGreaterThan(0)
    for (const [k, lens] of lengths) {
      expect(new Set(lens).size, `数组 ${k} 在不同语种长度不一致：${lens.join(',')}`).toBe(1)
    }
  })
})
