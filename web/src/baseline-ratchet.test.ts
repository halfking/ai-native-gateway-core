import { describe, expect, it } from 'vitest'

import { localDate, mergeRatchetBaseline } from '../scripts/lib/baseline-ratchet.mjs'

// 2026-11-03 12h 审计 P2-3：基线棘轮写回口径**此前没有任何门覆盖**——
// 两把运行时尺各写一份实现，只验「跑起来没报错」，不验语义。
// 本文件把三条口径钉死：棘轮只许降、新页按实测登记、未量到的页**保留**。

describe('mergeRatchetBaseline', () => {
  it('棘轮只许降：实测高于旧值时保持旧值', () => {
    const { next, keptHigher } = mergeRatchetBaseline({ a: 3 }, { a: 9 })
    expect(next.a).toBe(3)
    expect(keptHigher).toEqual([{ page: 'a', measured: 9, kept: 3 }])
  })

  it('实测低于旧值时按实测写回（可以降）', () => {
    const { next, keptHigher } = mergeRatchetBaseline({ a: 9 }, { a: 2 })
    expect(next.a).toBe(2)
    expect(keptHigher).toEqual([])
  })

  it('新页按实测值登记', () => {
    const { next, added } = mergeRatchetBaseline({ a: 3 }, { b: 4 })
    expect(next.b).toBe(4)
    expect(added).toEqual(['b'])
  })

  it('★ 未量到的页保留旧条目（删除基线条目 = 悄悄摘掉那页的约束）', () => {
    const { next, preserved, added } = mergeRatchetBaseline(
      { a: 3, gone: 5 },
      { a: 1 },
    )
    expect(next.gone).toBe(5)
    expect(preserved).toEqual(['gone'])
    // 保留的页不算「新登记」。
    expect(added).toEqual([])
  })

  it('空基线 + 空实测不炸', () => {
    expect(mergeRatchetBaseline().next).toEqual({})
    expect(mergeRatchetBaseline({}, {}).preserved).toEqual([])
  })
})

describe('localDate', () => {
  it('按本地时区取值（不是 UTC）', () => {
    // 构造一个本地日期与 UTC 日期**不同**的瞬间：本地 2026-11-03 00:30
    // （UTC+8）= UTC 2026-11-02 16:30 ⇒ toISOString 会写成 11-02。
    const d = new Date(2026, 10, 3, 0, 30, 0)
    expect(localDate(d)).toBe('2026-11-03')
    // 与 UTC 口径对照：证明这正是原实现会写错的一天。
    expect(new Date(d).toISOString().slice(0, 10)).not.toBe('2026-11-03')
  })

  it('月份/日期补零', () => {
    expect(localDate(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
})
