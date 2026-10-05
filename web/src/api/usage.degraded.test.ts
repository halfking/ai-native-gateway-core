// usage.degraded.test.ts —— 降级列表信封的解包契约。
//
// ## 挡住的是什么
//
// 2026-10-03 起 hot-keys / by-model / by-provider 三个端点从裸数组改为
// { items, degraded, ... }。前端若忘了 `.items` 解包，页面会拿到一个对象
// 当数组用（v-for 渲染 0 行、.length 是 undefined）—— 缺陷还在，只是换了形状。
// 这条测试盯住「解包」这一步本身。
//
// ## 反向对照
//
// 每个断言都有对应的错误实现：直接把信封当数组、忽略 degraded 标志、
// 老后端（裸数组）时崩溃。三者都必须被本测试抓住，见 test 内的反例注释。

import { describe, it, expect } from 'vitest'
import { unwrapDegradedList, type DegradedListEnvelope } from './usage'

interface Row { name: string }

describe('unwrapDegradedList', () => {
  it('解出 items 并读出降级标志（正向）', () => {
    const env: DegradedListEnvelope<Row> = {
      items: [{ name: 'a' }, { name: 'b' }],
      degraded: true,
      degraded_reason: 'usage_ledger_with_current_month',
    }
    const res = unwrapDegradedList<Row>(env)
    expect(res.items).toHaveLength(2)
    expect(res.items[0].name).toBe('a')
    expect(res.degraded).toBe(true)
    expect(res.reason).toBe('usage_ledger_with_current_month')
  })

  it('降级时 items 是空数组而不是 null（反例：Items 写成 nil 会序列化成 null）', () => {
    // 旧实现若让 degradedList 的 Items 为 nil，JSON 里就是 null 而不是 []，
    // 这里 `?? []` 的兜底保证前端不会崩。
    const env = { items: null, degraded: true } as unknown as DegradedListEnvelope<Row>
    const res = unwrapDegradedList<Row>(env)
    expect(res.items).toEqual([])
    expect(res.degraded).toBe(true)
  })

  it('degraded 缺字段时按未降级处理，且不把字段缺失当成降级（正向）', () => {
    // 反例：实现成 `payload?.degraded ?? true` 时，缺字段会被误报成降级，
    // 于是健康页面天天挂降级横幅 —— 恒真的告警等于没有告警。
    const env: DegradedListEnvelope<Row> = { items: [{ name: 'x' }] }
    const res = unwrapDegradedList<Row>(env)
    expect(res.degraded).toBe(false)
    expect(res.items).toHaveLength(1)
  })

  it('兼容老后端的裸数组形状（灰度期两者并存）', () => {
    // 反例：去掉这个分支，灰度期间前端会 throw —— 未解包的对象不是数组。
    const res = unwrapDegradedList<Row>([{ name: 'legacy' }])
    expect(res.items).toHaveLength(1)
    expect(res.degraded).toBe(false)
    expect(res.reason).toBe('')
  })

  it('降级原因优先取 degraded_reason，回退到 missing_view', () => {
    // 两个字段后端都可能只填其一；都不填时是空串而不是 undefined。
    const onlyView: DegradedListEnvelope<Row> = { items: [], degraded: true, missing_view: 'v1' }
    expect(unwrapDegradedList<Row>(onlyView).reason).toBe('v1')
    const neither: DegradedListEnvelope<Row> = { items: [], degraded: true }
    expect(unwrapDegradedList<Row>(neither).reason).toBe('')
  })

  it('区分三态：降级 ≠ 失败 ≠ 空（判据的核心）', () => {
    // 同一个 items 长度在三种情形下都是 0，只有 degraded 能把它们分开。
    const degraded = unwrapDegradedList<Row>({ items: [], degraded: true })
    const empty = unwrapDegradedList<Row>({ items: [], degraded: false })
    expect(degraded.items).toEqual(empty.items)
    expect(degraded.degraded).not.toBe(empty.degraded)
  })
})
