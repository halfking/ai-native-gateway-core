// PI 分析统计卡片的纯汇总函数（2026-11-19 列表↔统计一致性根修）。
//
// ★ 为什么抽纯函数：owner 报障「列表与统计的数据不一致」——旧实现三因：
//   ① 前端只读 by_type 里的 pending/analyzed，failed/processing/skipped 盲区；
//   ② 后端统计限 7 天而列表全量（已由后端同域根修）；
//   ③ 误报率/平均耗时是死零（falsePositives/count 声明后从未累加）。
//   ⇒ 纯函数钉死「响应形状 → 卡片值」的映射，单测可锁。

export interface PiAnalysisStatsSummary {
  total: number
  pending: number
  processing: number
  analyzed: number
  failed: number
  skipped: number
  /** '12.5%'；无分析结果时 '—'（不显示假 0%） */
  falsePositiveRate: string
  /** '1234ms'；无分析结果时 '—'（不显示假 0ms） */
  avgDuration: string
  /** 结果表是否非空（驱动误报率/平均耗时的占位形态） */
  hasResults: boolean
}

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0)

export function summarizePiAnalysisStats(data: Record<string, unknown> | null | undefined): PiAnalysisStatsSummary {
  // 顶层计数由后端单点保证与列表 COUNT 域一致；by_type 求和只作旧形状兜底。
  const sumByType = (key: string): number => {
    const rows = (data?.by_type as Array<Record<string, unknown>> | undefined) || []
    return rows.reduce((acc, row) => acc + num(row?.[key]), 0)
  }
  const top = (key: string, fallbackKey?: string): number => {
    const v = data?.[key]
    if (typeof v === 'number' && Number.isFinite(v)) return v
    return fallbackKey ? sumByType(fallbackKey) : 0
  }

  const resultsTotal = top('results_total')
  const falsePositives = top('false_positive_count')
  const rawAvg = data?.avg_duration_ms

  return {
    total: top('total', 'total'),
    pending: top('pending', 'pending'),
    processing: top('processing', 'processing'),
    analyzed: top('analyzed', 'analyzed'),
    failed: top('failed', 'failed'),
    skipped: top('skipped', 'skipped'),
    hasResults: resultsTotal > 0,
    falsePositiveRate:
      resultsTotal > 0 ? ((falsePositives / resultsTotal) * 100).toFixed(1) + '%' : '—',
    avgDuration:
      typeof rawAvg === 'number' && Number.isFinite(rawAvg)
        ? Math.round(rawAvg) + 'ms'
        : '—',
  }
}
