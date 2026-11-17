// liveStageTreeStats.test.ts —— 全屏统计层的口径判据（2026-11-17 设计对齐轮）
//
// ★ 统计口径必须与被测逻辑同层；三条纪律逐条钉：
//   ① 未上报 ≠ 0（缺数据的请求不进合计，且覆盖率单独报）；
//   ② 缓存命中率**不可跨口径合并**（OpenAI 包含口径 / Anthropic 并列口径）；
//   ③ 成功率的分母是「窗口内可见的请求条数」（不是「已终态的条数」）。

import { describe, expect, it } from 'vitest'
import {
  cacheAggregate,
  computeStats,
  dimRows,
  trendBuckets,
} from './liveStageTreeStats'
import type { StageBand } from '../types/liveStageTree'

function band(over: Partial<StageBand> = {}): StageBand {
  return {
    requestId: 'r',
    clientName: 'c',
    clientType: 'sdk',
    clientProtocol: 'openai',
    stages: [],
    model: { requested: '', isAuto: false, taskType: '', autoConfidence: null, final: 'm', upstream: '', source: 'frame', reason: 'direct', switched: null, candidates: null },
    cache: null,
    chunks: null,
    ttfbMs: null,
    latencyMs: null,
    totalTokens: null,
    costUsd: null,
    providerCode: 'p',
    providerLabel: 'p',
    credentialLabel: 'c',
    credentialId: 1,
    rawModel: '',
    errorKind: '',
    errorSource: '',
    failedStage: null,
    attempts: 0,
    queueMs: null,
    promptTokens: null,
    completionTokens: null,
    cacheReadTokens: null,
    sessionId: '',
    isProbe: false,
    inFlight: false,
    tsMs: 0,
    ...over,
  }
}

describe('统计总览', () => {
  it('★ 成功率的分母是「窗口内可见的条数」（在途也算在分母里）', () => {
    const s = computeStats([
      band({ requestId: 'a' }),
      band({ requestId: 'b' }),
      band({ requestId: 'c', inFlight: true }),
    ])
    expect(s.total).toBe(3)
    expect(s.success).toBe(2)
    expect(s.inFlight).toBe(1)
    expect(s.successRate).toBe(67)
  })

  it('★ 未上报的耗时/token/成本不进合计（不是 0）', () => {
    const s = computeStats([
      band({ requestId: 'a', latencyMs: 100, totalTokens: 10, costUsd: 0.5 }),
      band({ requestId: 'b' }),
    ])
    expect(s.avgLatencyMs).toBe(100)
    expect(s.latencyCovered).toBe(1)
    expect(s.totalTokens).toBe(10)
    expect(s.costUsd).toBe(0.5)
    expect(s.costCovered).toBe(1)
  })

  it('★ 一条都没有时成功率是 null（不冒充 0%）', () => {
    const s = computeStats([])
    expect(s.successRate).toBeNull()
    expect(s.avgLatencyMs).toBeNull()
  })
})

describe('缓存命中率聚合：不可跨口径合并', () => {
  const cache = (
    pct: number | null,
    convention: 'subset_openai' | 'parallel_anthropic' | 'unknown',
  ) => ({ pct, convention, why: '', anomaly: false })

  it('★ 同口径（OpenAI 包含口径）⇒ 给出合计量', () => {
    const agg = cacheAggregate([
      band({ cache: cache(25, 'subset_openai'), promptTokens: 100, cacheReadTokens: 25 }),
      band({ cache: cache(25, 'subset_openai'), promptTokens: 100, cacheReadTokens: 25 }),
    ])
    expect(agg.convention).toBe('subset_openai')
    expect(agg.pct).toBe(25)
    expect(agg.covered).toBe(2)
  })

  it('★ 两种口径同时出现 ⇒ 不给数（混算 = 假数字）', () => {
    const agg = cacheAggregate([
      band({ cache: cache(25, 'subset_openai'), promptTokens: 100, cacheReadTokens: 25 }),
      band({ cache: cache(50, 'parallel_anthropic'), promptTokens: 100, cacheReadTokens: 100 }),
    ])
    expect(agg.convention).toBe('mixed')
    expect(agg.pct).toBeNull()
  })

  it('★ 上游没报缓存读 ⇒ 不计入，也**不**当成 0%（只报覆盖面）', () => {
    const agg = cacheAggregate([
      band({ cache: null }),
      band({ cache: cache(25, 'subset_openai'), promptTokens: 100, cacheReadTokens: 25 }),
    ])
    expect(agg.pct).toBe(25)
    expect(agg.covered).toBe(1)
    expect(agg.excluded).toBe(1)
  })
})

describe('分类统计', () => {
  it('★ 按模型分组，请求数降序', () => {
    const rows = dimRows(
      [
        band({ requestId: 'a', model: { ...band().model, final: 'm1' } }),
        band({ requestId: 'b', model: { ...band().model, final: 'm2' } }),
        band({ requestId: 'c', model: { ...band().model, final: 'm2' } }),
      ],
      'model',
    )
    expect(rows[0].key).toBe('m2')
    expect(rows[0].requests).toBe(2)
    expect(rows[1].requests).toBe(1)
  })

  it('★ 状态维度按 成功/进行中/失败 分组', () => {
    const rows = dimRows(
      [
        band({ requestId: 'a' }),
        band({ requestId: 'b', inFlight: true }),
        band({ requestId: 'c', failedStage: 'route' }),
      ],
      'status',
    )
    expect(rows.map(r => r.key).sort()).toEqual(['failure', 'in_progress', 'success'])
  })
})

describe('周期统计', () => {
  it('★ 按分钟分桶；空桶也保留（断流看得见）', () => {
    const t0 = Date.parse('2026-11-17T10:00:00Z')
    const buckets = trendBuckets([
      band({ requestId: 'a', tsMs: t0 }),
      band({ requestId: 'b', tsMs: t0 + 120_000 }),
    ])
    expect(buckets.length).toBe(3)
    expect(buckets[0].requests).toBe(1)
    expect(buckets[1].requests).toBe(0)
    expect(buckets[2].requests).toBe(1)
  })

  it('★ 没有数据 ⇒ 没有桶（不编一排空桶）', () => {
    expect(trendBuckets([])).toEqual([])
  })
})
