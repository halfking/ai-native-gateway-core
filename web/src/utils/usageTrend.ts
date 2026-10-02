// usageTrend.ts — 按模型拆分的用量趋势纯函数层（2026-10-02 看板轮）。
// 看板「用量趋势」卡与全页用量趋势视图共用：指标选择、桶标签、序列→数据集透视。
import type { UsageTrendMetric, UsageTrendModelSeries, UsageTrendPoint } from '../api/usage'

export const USAGE_TREND_OTHERS = '__others__'

export const USAGE_TREND_METRICS: UsageTrendMetric[] = ['requests', 'tokens', 'credits', 'cost']

export function usageTrendMetricLabelKey(metric: UsageTrendMetric): string {
  switch (metric) {
    case 'requests': return 'dashboard.board.trendRequests'
    case 'tokens': return 'dashboard.board.trendTokens'
    case 'credits': return 'dashboard.board.trendCredits'
    case 'cost': return 'dashboard.board.trendCost'
  }
}

export function usageTrendPointValue(p: UsageTrendPoint, metric: UsageTrendMetric): number {
  switch (metric) {
    case 'requests': return p.requests
    case 'tokens': return p.tokens
    case 'credits': return p.credits
    case 'cost': return p.cost_usd
  }
}

export function usageTrendSeriesTotal(s: UsageTrendModelSeries, metric: UsageTrendMetric): number {
  switch (metric) {
    case 'requests': return s.total_requests
    case 'tokens': return s.total_tokens
    case 'credits': return s.total_credits
    case 'cost': return s.total_cost_usd
  }
}

/** 桶标签：<60m 粒度带分钟（M/D HH:mm），≥60m 只到小时（M/D HH:00）。 */
export function formatUsageTrendBucketLabel(bucket: string, bucketMinutes: number): string {
  const d = new Date(bucket)
  if (Number.isNaN(d.getTime())) return bucket
  const md = `${d.getMonth() + 1}/${d.getDate()}`
  const hh = String(d.getHours()).padStart(2, '0')
  if (bucketMinutes >= 60) return `${md} ${hh}:00`
  return `${md} ${hh}:${String(d.getMinutes()).padStart(2, '0')}`
}

export interface UsageTrendPivot {
  /** 全序列并集桶（升序）格式化后的 x 轴标签。 */
  labels: string[]
  /** 每模型一行，data 与 labels 对齐（缺失桶补 0）。 */
  rows: { model: string; data: number[] }[]
}

/** 把后端按模型分组的序列透视成 chart 数据集；保持入参顺序（后端已按总量降序、
 * __others__ 垫底）。缺失桶补 0：rollup 表无行即该桶用量为 0。 */
export function pivotUsageTrendSeries(
  series: UsageTrendModelSeries[],
  metric: UsageTrendMetric,
  bucketMinutes: number,
): UsageTrendPivot {
  const bucketIndex = new Map<string, number>()
  const buckets: string[] = []
  for (const s of series) {
    for (const p of s.points ?? []) {
      if (!bucketIndex.has(p.bucket)) {
        bucketIndex.set(p.bucket, buckets.length)
        buckets.push(p.bucket)
      }
    }
  }
  buckets.sort()
  const labels = buckets.map((b) => formatUsageTrendBucketLabel(b, bucketMinutes))
  const rows = (series ?? []).map((s) => {
    const byBucket = new Map<string, number>((s.points ?? []).map((p) => [p.bucket, usageTrendPointValue(p, metric)]))
    const data = buckets.map((b) => byBucket.get(b) ?? 0)
    return { model: s.model, data }
  })
  return { labels, rows }
}

/** y 轴紧凑刻度（12.3K / 4.5M），避免长数字挤压图区。 */
export function compactTickValue(v: number): string {
  if (Math.abs(v) >= 1_000_000) return `${+(v / 1_000_000).toFixed(1)}M`
  if (Math.abs(v) >= 1_000) return `${+(v / 1_000).toFixed(1)}K`
  return String(v)
}
