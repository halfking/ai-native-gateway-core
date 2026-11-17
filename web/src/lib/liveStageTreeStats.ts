// liveStageTreeStats.ts —— 请求阶段树的**统计层纯函数**（2026-11-17 设计对齐轮）
//
// ★ 为什么单独一份：统计口径必须与被测逻辑同层（仓内反复吃过的亏），
//   且「统计是哪个口径」必须在**数据层**就能被读出来，而不是在组件里现算。
//
// ★ 口径纪律（三条，写死在这里）：
//   1. **未上报 ≠ 0**：缺 cost / token / 耗时的请求**不计入合计**，
//      并单独报「已统计 / 未上报」两个数 ⇒ 读者能判断合计的覆盖面。
//   2. **缓存命中率不可跨口径合并**：OpenAI 是「包含口径」（分母 = 输入），
//      Anthropic 是「并列口径」（分母 = 输入 + 缓存读取）
//      ⇒ 两个口径混在一起求和 = 假数字（2026-10-06 真库事故同族）
//      ⇒ 两种口径同时出现 ⇒ **不给数**（null），只报覆盖面。
//   3. **百分比的分母写清**：成功率的分母是「窗口内可见的请求条数」，
//      不是「已终态的条数」—— 否则在途多时成功率会被抬高。

import type { StageBand } from '../types/liveStageTree'

/** 统计总览（内存窗口口径）。 */
export interface StageStats {
  /** 窗口内可见的请求条数。 */
  total: number
  /** 成功条数（终态且没有失败段）。 */
  success: number
  /** 失败条数。 */
  failed: number
  /** 在途条数。 */
  inFlight: number
  /** 成功率（0..100）；total=0 时 null（不冒充 0%）。 */
  successRate: number | null
  /** 平均耗时（毫秒）；无上报 = null。 */
  avgLatencyMs: number | null
  /** P95 耗时（毫秒）；无上报 = null。 */
  p95LatencyMs: number | null
  /** token 合计；无上报 = null。 */
  totalTokens: number | null
  /** 输入 token 合计；无上报 = null。 */
  inputTokens: number | null
  /** 输出 token 合计；无上报 = null。 */
  outputTokens: number | null
  /** 成本合计（USD）；无上报 = null。 */
  costUsd: number | null
  /** 已上报耗时的条数（分母，说明覆盖度）。 */
  latencyCovered: number
  /** 已上报成本的条数。 */
  costCovered: number
  /** 去重后的供应商数。 */
  providers: number
  /** 去重后的模型数（最终匹配模型 + 上游模型串）。 */
  models: number
  /** 去重后的客户端数。 */
  clients: number
  /** 去重后的凭据数。 */
  credentials: number
  /** 出站尝试次数合计。 */
  attempts: number
}

/** 缓存命中率聚合（含口径与覆盖面）。 */
export interface CacheAggregate {
  /** 命中率（0..100）；不可判定 = null。 */
  pct: number | null
  /** 口径标识（mixed = 两种口径同时出现 ⇒ 不给数）。 */
  convention: 'subset_openai' | 'parallel_anthropic' | 'mixed' | ''
  /** 计入聚合的条数。 */
  covered: number
  /** 上游没报缓存读的条数（不计入，也**不**当作 0%）。 */
  excluded: number
}

/** 分类统计的一行。 */
export interface DimRow {
  key: string
  label: string
  requests: number
  successRate: number | null
  inFlight: number
  tokens: number | null
  costUsd: number | null
  cachePct: number | null
  avgLatencyMs: number | null
}

/** 周期统计的一个时间桶。 */
export interface TrendBucket {
  /** 桶起点（毫秒）。 */
  tsMs: number
  requests: number
  success: number
  failed: number
}

/** 统计维度。 */
export type StatDim = 'model' | 'provider' | 'client' | 'credential' | 'status'

/** 一条带在某维度上的键与显示名。 */
function dimKeyOf(band: StageBand, dim: StatDim): { key: string; label: string } {
  switch (dim) {
    case 'model': {
      const key = band.model.final || band.model.upstream || band.model.requested || '—'
      return { key, label: key }
    }
    case 'provider': {
      const key = band.providerCode || band.credentialLabel || '—'
      return { key, label: band.providerLabel || key }
    }
    case 'client': {
      const key = band.clientName || '—'
      return { key, label: key }
    }
    case 'credential': {
      const key = band.credentialId != null ? String(band.credentialId) : '—'
      return { key, label: band.credentialLabel || key }
    }
    case 'status': {
      const key = band.failedStage ? 'failure' : band.inFlight ? 'in_progress' : 'success'
      return { key, label: key }
    }
    default:
      return { key: '—', label: '—' }
  }
}

function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null
  const sorted = values.slice().sort((a, b) => a - b)
  const idx = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1))
  return sorted[idx]
}

/** 统计总览。 */
export function computeStats(bands: StageBand[]): StageStats {
  const total = bands.length
  let success = 0
  let failed = 0
  let inFlight = 0
  const latencies: number[] = []
  let totalTokens: number | null = null
  let inputTokens: number | null = null
  let outputTokens: number | null = null
  let costUsd: number | null = null
  let latencyCovered = 0
  let costCovered = 0
  const providers = new Set<string>()
  const models = new Set<string>()
  const clients = new Set<string>()
  const credentials = new Set<string>()
  let attempts = 0

  for (const b of bands) {
    if (b.inFlight) inFlight += 1
    else if (b.failedStage) failed += 1
    else success += 1

    if (b.latencyMs != null) {
      latencies.push(b.latencyMs)
      latencyCovered += 1
    }
    if (b.totalTokens != null) totalTokens = (totalTokens ?? 0) + b.totalTokens
    if (b.promptTokens != null) inputTokens = (inputTokens ?? 0) + b.promptTokens
    if (b.completionTokens != null) outputTokens = (outputTokens ?? 0) + b.completionTokens
    if (b.costUsd != null) {
      costUsd = (costUsd ?? 0) + b.costUsd
      costCovered += 1
    }
    if (b.providerCode) providers.add(b.providerCode)
    if (b.credentialId != null) credentials.add(String(b.credentialId))
    if (b.model.final) models.add(b.model.final)
    if (b.clientName && b.clientName !== '—') clients.add(b.clientName)
    attempts += b.attempts
  }

  return {
    total,
    success,
    failed,
    inFlight,
    successRate: total ? Math.round((success / total) * 100) : null,
    avgLatencyMs: latencies.length
      ? Math.round(latencies.reduce((a, b) => a + b, 0) / latencies.length)
      : null,
    p95LatencyMs: percentile(latencies, 95),
    totalTokens,
    inputTokens,
    outputTokens,
    costUsd,
    latencyCovered,
    costCovered,
    providers: providers.size,
    models: models.size,
    clients: clients.size,
    credentials: credentials.size,
    attempts,
  }
}

/** 缓存命中率聚合（★ 不跨口径合并）。 */
export function cacheAggregate(bands: StageBand[]): CacheAggregate {
  let subsetNum = 0
  let subsetDen = 0
  let parallelNum = 0
  let parallelDen = 0
  let covered = 0
  let excluded = 0
  for (const b of bands) {
    const c = b.cache
    if (!c || c.pct === null || c.anomaly) {
      excluded += 1
      continue
    }
    const prompt = b.promptTokens ?? 0
    const read = b.cacheReadTokens ?? 0
    if (c.convention === 'subset_openai') {
      subsetNum += read
      subsetDen += prompt
      covered += 1
    } else if (c.convention === 'parallel_anthropic') {
      parallelNum += read
      parallelDen += prompt + read
      covered += 1
    } else {
      excluded += 1
    }
  }
  const hasSubset = subsetDen > 0
  const hasParallel = parallelDen > 0
  if (hasSubset && hasParallel) {
    return { pct: null, convention: 'mixed', covered, excluded }
  }
  if (hasSubset) {
    return {
      pct: Math.min(100, Math.round((subsetNum / subsetDen) * 100)),
      convention: 'subset_openai',
      covered,
      excluded,
    }
  }
  if (hasParallel) {
    return {
      pct: Math.min(100, Math.round((parallelNum / parallelDen) * 100)),
      convention: 'parallel_anthropic',
      covered,
      excluded,
    }
  }
  return { pct: null, convention: '', covered, excluded }
}

/** 分类统计（按维度分组；请求数降序）。 */
export function dimRows(bands: StageBand[], dim: StatDim): DimRow[] {
  const groups = new Map<string, StageBand[]>()
  for (const b of bands) {
    const { key } = dimKeyOf(b, dim)
    const list = groups.get(key)
    if (list) list.push(b)
    else groups.set(key, [b])
  }
  const rows: DimRow[] = []
  for (const [key, list] of groups) {
    const total = list.length
    const ok = list.filter(b => !b.inFlight && !b.failedStage).length
    const inflight = list.filter(b => b.inFlight).length
    const tokens = list.some(b => b.totalTokens != null)
      ? list.reduce((a, b) => a + (b.totalTokens ?? 0), 0)
      : null
    const cost = list.some(b => b.costUsd != null)
      ? list.reduce((a, b) => a + (b.costUsd ?? 0), 0)
      : null
    const lat = list.filter(b => b.latencyMs != null).map(b => b.latencyMs as number)
    const cache = cacheAggregate(list)
    const first = dimKeyOf(list[0], dim)
    rows.push({
      key,
      label: first.label,
      requests: total,
      successRate: total ? Math.round((ok / total) * 100) : null,
      inFlight: inflight,
      tokens,
      costUsd: cost,
      cachePct: cache.pct,
      avgLatencyMs: lat.length
        ? Math.round(lat.reduce((a, b) => a + b, 0) / lat.length)
        : null,
    })
  }
  rows.sort((a, b) => b.requests - a.requests || a.key.localeCompare(b.key))
  return rows
}

/** 周期统计（按分钟分桶；空桶也保留 ⇒ 断流看得见）。 */
export function trendBuckets(bands: StageBand[], bucketMs = 60_000, windowMs?: number): TrendBucket[] {
  if (bands.length === 0) return []
  const times = bands.map(b => b.tsMs).filter(t => t > 0)
  if (times.length === 0) return []
  const min = Math.min(...times)
  const max = Math.max(...times)
  const start = Math.floor(min / bucketMs) * bucketMs
  const end = Math.floor(max / bucketMs) * bucketMs
  const count = Math.min(240, Math.floor((end - start) / bucketMs) + 1)
  const buckets: TrendBucket[] = []
  for (let i = 0; i < count; i += 1) {
    buckets.push({ tsMs: start + i * bucketMs, requests: 0, success: 0, failed: 0 })
  }
  for (const b of bands) {
    if (!b.tsMs) continue
    const idx = Math.floor((b.tsMs - start) / bucketMs)
    if (idx < 0 || idx >= buckets.length) continue
    const bucket = buckets[idx]
    bucket.requests += 1
    if (b.inFlight) continue
    if (b.failedStage) bucket.failed += 1
    else bucket.success += 1
  }
  if (windowMs && windowMs > 0) {
    // 窗口只影响「铺多少个桶」，不改数据。
    const maxBuckets = Math.ceil(windowMs / bucketMs)
    return buckets.slice(-Math.max(1, Math.min(maxBuckets, buckets.length)))
  }
  return buckets
}
