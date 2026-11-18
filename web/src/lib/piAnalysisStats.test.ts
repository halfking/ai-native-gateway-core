// ★ 2026-11-19 列表↔统计一致性根修：统计卡汇总的纯函数钉测。
// 三个历史病灶都要有负控：
//   ① failed/processing/skipped 不再盲区（211 实测 2 条 failed ⇒ 旧实现统计全 0）；
//   ② 误报率/平均耗时不再死零（旧实现 falsePositives/count 声明后从未累加）；
//   ③ 空结果表显示 '—'，不是假 '0%'/'0ms'。
import { describe, expect, it } from 'vitest'
import { summarizePiAnalysisStats } from './piAnalysisStats'

describe('summarizePiAnalysisStats', () => {
  it('顶层计数全状态直读（failed 不再盲区）', () => {
    const s = summarizePiAnalysisStats({
      total: 4, pending: 1, processing: 0, analyzed: 1, failed: 2, skipped: 0,
      results_total: 1, false_positive_count: 1, avg_duration_ms: 1234.6,
    })
    expect(s.total).toBe(4)
    expect(s.pending).toBe(1)
    expect(s.analyzed).toBe(1)
    expect(s.failed).toBe(2)
    expect(s.processing).toBe(0)
  })

  it('误报率 = false_positive_count / results_total（非死零）', () => {
    const s = summarizePiAnalysisStats({
      total: 2, pending: 0, analyzed: 2, failed: 0, skipped: 0,
      results_total: 8, false_positive_count: 1, avg_duration_ms: 100,
    })
    expect(s.falsePositiveRate).toBe('12.5%')
  })

  it('平均耗时四舍五入取整', () => {
    const s = summarizePiAnalysisStats({
      total: 1, analyzed: 1, results_total: 3, false_positive_count: 0,
      avg_duration_ms: 1234.6,
    })
    expect(s.avgDuration).toBe('1235ms')
  })

  it('空结果表：误报率/平均耗时显示 —（不是假 0%/0ms）', () => {
    const s = summarizePiAnalysisStats({
      total: 0, pending: 0, analyzed: 0, failed: 0, skipped: 0,
      results_total: 0, false_positive_count: 0, avg_duration_ms: null,
    })
    expect(s.hasResults).toBe(false)
    expect(s.falsePositiveRate).toBe('—')
    expect(s.avgDuration).toBe('—')
  })

  it('旧形状兜底：顶层缺键时回退 by_type 求和', () => {
    const s = summarizePiAnalysisStats({
      by_type: [
        { error_type: 'a', pending: 1, analyzed: 2, total: 3, failed: 0 },
        { error_type: 'b', pending: 0, analyzed: 1, total: 1, failed: 1 },
      ],
    })
    expect(s.pending).toBe(1)
    expect(s.analyzed).toBe(3)
    expect(s.failed).toBe(1)
    expect(s.falsePositiveRate).toBe('—')
  })

  it('null/undefined 输入不炸且全零', () => {
    for (const input of [null, undefined]) {
      const s = summarizePiAnalysisStats(input as never)
      expect(s.total).toBe(0)
      expect(s.falsePositiveRate).toBe('—')
      expect(s.avgDuration).toBe('—')
    }
  })
})
