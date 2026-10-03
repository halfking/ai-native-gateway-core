// useDegradationMarker.test.ts —— 降级标记的判据
//
// 这条门守住的是**一次真实事故的复发**：降级载荷（200 + 全 0）被页面
// 当成真实测量值渲染，period-compare 把 1139.62 美元显示成 0。
//
// 判据必须有鉴别力，所以除了正向用例外，每条都配了**反向对照**：
// 断言「把实现改坏时这条测试会红」。

import { describe, it, expect } from 'vitest'
import {
  emptyDegradation,
  isDegraded,
  mergeDegradation,
  type DegradationState,
} from './useDegradationMarker'

describe('isDegraded', () => {
  it('显式 true 才是降级', () => {
    expect(isDegraded({ degraded: true })).toBe(true)
  })

  // ★ 本门最关键的反向对照：字段**缺失**必须与 false 分开处理。
  // 反例很具体：把实现改成 `payload?.degraded ?? false` 再配一个
  // 「服务端确认过就设 true，否则设 false」的写入端，两者会一起塌掉 ——
  // 老版本服务端根本没有这个字段，缺字段会被读成 false（健康）。
  // 本断言在那种塌法下仍然绿（都是 false），所以它守的不是 `=== true`
  // 本身，而是**缺失项必须落到非降级分支且不得触发提示**。
  it('字段缺失不等于健康：undefined 必须与 false 分开', () => {
    expect(isDegraded(undefined)).toBe(false)
    expect(isDegraded(null)).toBe(false)
    expect(isDegraded({})).toBe(false)
    expect(isDegraded({ degraded: false })).toBe(false)
  })

  it('true 与 false 不可混淆', () => {
    expect(isDegraded({ degraded: true })).not.toBe(isDegraded({ degraded: false }))
  })
})

describe('mergeDegradation', () => {
  const src = (payload: Parameters<typeof mergeDegradation>[2]) => payload

  it('健康响应不改变状态', () => {
    const s0 = emptyDegradation()
    const s1 = mergeDegradation(s0, '缓存经济学', src({ degraded: false }))
    expect(s1).toEqual({ active: false, reasons: [] })
    // 反向对照：状态对象必须原样返回，不能每次都造新对象
    // （否则 Vue 依赖会无谓地触发重渲染）。
    expect(s1).toBe(s0)
  })

  it('降级响应置位并记录来源', () => {
    const s = mergeDegradation(
      emptyDegradation(),
      '缓存经济学',
      src({ degraded: true, degraded_reason: 'missing column: usage_ledger.compression_strategy' }),
    )
    expect(s.active).toBe(true)
    expect(s.reasons).toEqual(['缓存经济学: missing column: usage_ledger.compression_strategy'])
  })

  it('缺少 reason 时仍要能说明是降级，不能给出空条目', () => {
    const s = mergeDegradation(emptyDegradation(), '成本对比', src({ degraded: true }))
    expect(s.active).toBe(true)
    expect(s.reasons).toEqual(['成本对比: schema 落后于代码'])
    // 鉴别力：条目不能退化成 "成本对比: " 这种看不出发生了什么的字符串
    expect(s.reasons[0].trim()).not.toBe('成本对比:')
  })

  it('多源降级逐条累积（用户要知道是哪些指标不可信）', () => {
    let s: DegradationState = emptyDegradation()
    s = mergeDegradation(s, '成本对比', src({ degraded: true, degraded_reason: 'r1' }))
    s = mergeDegradation(s, '缓存经济学', src({ degraded: true, degraded_reason: 'r2' }))
    expect(s.active).toBe(true)
    expect(s.reasons).toHaveLength(2)
    expect(s.reasons[0]).toContain('成本对比')
    expect(s.reasons[1]).toContain('缓存经济学')
  })

  it('同一来源重复上报不产生重复条目', () => {
    const payload = src({ degraded: true, degraded_reason: 'r1' })
    let s = mergeDegradation(emptyDegradation(), '成本对比', payload)
    s = mergeDegradation(s, '成本对比', payload)
    expect(s.reasons).toHaveLength(1)
  })

  it('不原地修改入参（避免 Vue 深层代理下 UI 不更新）', () => {
    const s0 = emptyDegradation()
    const snapshot = JSON.stringify(s0)
    mergeDegradation(s0, 'x', src({ degraded: true }))
    expect(JSON.stringify(s0)).toBe(snapshot)
    expect(s0.active).toBe(false)
  })
})
