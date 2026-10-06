import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelTaskIndex,
  isAwaitingFirstRefresh,
  modelTaskIndexTruncated,
  formatModelTaskRatePct,
  formatMs,
  formatCostPer1k,
  avgLatencyMayBeDefault,
  p95LatencyMayBeDefault,
  costMayBeNoData,
  modelTaskIndexKeyOf,
  MODEL_TASK_INDEX_TOP_DEFAULT,
  MODEL_TASK_INDEX_TOP_MAX,
  type ModelTaskIndexItem,
  type ModelTaskIndexResponse,
} from './modelTaskIndex'

/**
 * 自动路由「模型 × 任务」索引的契约测试（2026-10-07）。
 *
 * 四条重点：
 * 1. ★★★ **量纲第 4 处**：`success_rate` 是 0..1 比率，
 *    而时间线那份是已经乘过 100 的 0..100 ⇒ 同名概念、相反量纲；
 * 2. ★★★ 三个数值列的「0」/「1000」是**生产者 COALESCE 兜底值**，
 *    不是真实测量 ⇒ 不能渲染成「0ms」/「免费」；
 * 3. ★★ `bucket === null` 是「后台尚未首刷」，不是「没有数据」；
 * 4. ★ `top` 越界**静默回落 20**（不是 400），与同族 window/metric 相反。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
function lastUrl(): string {
  return decodeURIComponent(String(fetchMock.mock.calls[0]![0]))
}
function ok(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse({ bucket: '2026-10-07T10:05:00Z', items: [] }))
}

function item(over: Partial<ModelTaskIndexItem> = {}): ModelTaskIndexItem {
  return {
    canonical_id: 7,
    canonical_name: 'gpt-4o',
    task_type: 'chat',
    sample_count: 42,
    success_rate: 0.93,
    avg_latency_ms: 820,
    p95_latency_ms: 1900,
    avg_cost_per_1k_usd: 0.0042,
    primary_credential_id: 12,
    updated_at: '2026-10-07T10:05:00Z',
    ...over,
  }
}

describe('URL 与参数', () => {
  it('不发参数 ⇒ 路径裸奔', async () => {
    ok()
    await fetchModelTaskIndex()
    expect(lastUrl()).toContain('/api/admin/auto-route/analytics/model-task-index')
    expect(lastUrl()).not.toContain('?')
  })

  it('★ task_type 照发原样（后端只 TrimSpace，SQL 大小写敏感）', async () => {
    ok()
    await fetchModelTaskIndex({ taskType: 'Code' })
    expect(lastUrl()).toContain('task_type=Code')
  })

  it('★ task_type 前后空格被 trim', async () => {
    ok()
    await fetchModelTaskIndex({ taskType: '  chat  ' })
    expect(lastUrl()).toContain('task_type=chat')
  })

  it('★ task_type 空串 / 纯空格 ⇒ 不发（不是发空值）', async () => {
    ok()
    await fetchModelTaskIndex({ taskType: '   ' })
    expect(lastUrl()).not.toContain('task_type')
  })

  it('★ top=50 发出去', async () => {
    ok()
    await fetchModelTaskIndex({ top: 50 })
    expect(lastUrl()).toContain('top=50')
  })

  it('★★ top=501 **不发**（越界后端静默回落 20，发了会误以为是 501 条的语义）', async () => {
    ok()
    await fetchModelTaskIndex({ top: MODEL_TASK_INDEX_TOP_MAX + 1 })
    expect(lastUrl()).not.toContain('top=')
  })

  it('★ top=0 / 负数 / NaN / Infinity 一律不发', async () => {
    for (const top of [0, -1, NaN, Infinity]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ bucket: '2026-10-07T10:05:00Z', items: [] }))
      await fetchModelTaskIndex({ top })
      expect(lastUrl()).not.toContain('top=')
    }
  })

  it('top=500 边界发得出去', async () => {
    ok()
    await fetchModelTaskIndex({ top: MODEL_TASK_INDEX_TOP_MAX })
    expect(lastUrl()).toContain('top=500')
  })
})

describe('★★ bucket === null = 尚未首刷', () => {
  it('★ bucket=null ⇒ 判为「尚未首刷」', () => {
    const r: ModelTaskIndexResponse = {
      bucket: null,
      items: [],
      warning: 'model_task_index is empty; awaiting first bg worker refresh',
    }
    expect(isAwaitingFirstRefresh(r)).toBe(true)
  })

  it('★ 有 bucket ⇒ 不是「尚未首刷」（证明不是恒真）', () => {
    expect(isAwaitingFirstRefresh({ bucket: '2026-10-07T10:05:00Z', items: [] })).toBe(false)
  })

  it('null / undefined 响应 ⇒ false（不是「首刷中」）', () => {
    expect(isAwaitingFirstRefresh(null)).toBe(false)
    expect(isAwaitingFirstRefresh(undefined)).toBe(false)
  })

  it('★ 有桶但 items 为空 ⇒ 也不叫「尚未首刷」（那是另一个空态）', () => {
    expect(isAwaitingFirstRefresh({ bucket: '2026-10-07T10:05:00Z', items: [] })).toBe(false)
  })
})

describe('★★ 截断判定：top 是客户端自己发的，所以是精确信号', () => {
  it('★ items.length === 请求的 top ⇒ 判为可能截断', () => {
    const r: ModelTaskIndexResponse = { bucket: 'b', items: Array.from({ length: 50 }, () => item()) }
    expect(modelTaskIndexTruncated(r, 50)).toBe(true)
  })

  it('★ items.length < top ⇒ 未截断', () => {
    const r: ModelTaskIndexResponse = { bucket: 'b', items: Array.from({ length: 49 }, () => item()) }
    expect(modelTaskIndexTruncated(r, 50)).toBe(false)
  })

  it('★ 没发 top ⇒ 按后端默认 20 算（items=20 判截断）', () => {
    const r: ModelTaskIndexResponse = { bucket: 'b', items: Array.from({ length: MODEL_TASK_INDEX_TOP_DEFAULT }, () => item()) }
    expect(modelTaskIndexTruncated(r)).toBe(true)
  })

  it('★ 发了非法 top（>500，实际没发出去）⇒ 也按 20 算，不是按 501', () => {
    const r: ModelTaskIndexResponse = { bucket: 'b', items: Array.from({ length: 20 }, () => item()) }
    expect(modelTaskIndexTruncated(r, 501)).toBe(true)
  })

  it('★ 发了非法 top 但只有 20 条、请求的是 501 ⇒ 用 501 判会漏报 ⇒ 用 20 判才对', () => {
    const r: ModelTaskIndexResponse = { bucket: 'b', items: Array.from({ length: 20 }, () => item()) }
    expect(20 >= 501).toBe(false) // 若错用请求值
    expect(modelTaskIndexTruncated(r, 501)).toBe(true) // 正确实现
  })

  it('空响应 ⇒ 不判截断', () => {
    expect(modelTaskIndexTruncated(null, 50)).toBe(false)
  })
})

describe('★★★ 量纲第 4 处：success_rate 是 0..1', () => {
  it('★ 0.9 ⇒ 90.0%（不是 0.9%）', () => {
    expect(formatModelTaskRatePct(0.9)).toBe('90.0%')
  })

  it('★ 0.93 ⇒ 93.0%', () => {
    expect(formatModelTaskRatePct(0.93)).toBe('93.0%')
  })

  it('★ 1 ⇒ 100.0%', () => {
    expect(formatModelTaskRatePct(1)).toBe('100.0%')
  })

  it('★ 0 ⇒ 0.0%（真的是全失败，不是缺失）', () => {
    expect(formatModelTaskRatePct(0)).toBe('0.0%')
  })

  it('★ 90 ⇒ 9000.0%（**证明**本函数只吃 0..1；喂进 0..100 的值会炸）', () => {
    // 这条是刻意留的反向证据：本表是 0..1，
    // 若哪天误把 0..100 的列接进来，这条会立刻暴露量纲用反。
    expect(formatModelTaskRatePct(90)).toBe('9000.0%')
  })

  it('★ 缺失 / null / NaN ⇒ —，不是 0.0%', () => {
    expect(formatModelTaskRatePct(undefined)).toBe('—')
    expect(formatModelTaskRatePct(null)).toBe('—')
    expect(formatModelTaskRatePct(NaN)).toBe('—')
  })
})

describe('★★★ 三个数值列的兜底值', () => {
  it('avg_latency_ms = 0 ⇒ 判为「可能是兜底」', () => {
    expect(avgLatencyMayBeDefault(0)).toBe(true)
  })
  it('avg_latency_ms 有值 ⇒ 不判兜底', () => {
    expect(avgLatencyMayBeDefault(820)).toBe(false)
  })

  it('p95_latency_ms = 1000 ⇒ 判为「可能是兜底」', () => {
    expect(p95LatencyMayBeDefault(1000)).toBe(true)
  })
  it('p95_latency_ms = 999 ⇒ 不判兜底（证明不是「小于 1000 都算」）', () => {
    expect(p95LatencyMayBeDefault(999)).toBe(false)
  })

  it('avg_cost_per_1k_usd = 0 ⇒ 判为「无成本数据」', () => {
    expect(costMayBeNoData(0)).toBe(true)
  })
  it('avg_cost_per_1k_usd 有值 ⇒ 不判', () => {
    expect(costMayBeNoData(0.0042)).toBe(false)
  })

  it('★ 兜底值与真实值不可区分 ⇒ 函数只表达「可能」，不表达「一定」', () => {
    // 真实测量也可能是 0ms / 1000ms / 0 成本（免费模型）
    // ⇒ 判据只能是「可能」，UI 必须据此弱化显示而非改写数值。
    expect(avgLatencyMayBeDefault(0)).toBe(true)
    expect(p95LatencyMayBeDefault(1000)).toBe(true)
    expect(costMayBeNoData(0)).toBe(true)
  })
})

describe('展示格式化与稀疏键', () => {
  it('formatMs 有值 ⇒ 带 ms', () => {
    expect(formatMs(820)).toBe('820ms')
  })
  it('★ formatMs 缺失 ⇒ —（不是 0ms）', () => {
    expect(formatMs(undefined)).toBe('—')
    expect(formatMs(null)).toBe('—')
  })
  it('formatMs=0 ⇒ 0ms（数值照显，是否兜底由另一个判定负责）', () => {
    expect(formatMs(0)).toBe('0ms')
  })

  it('★ formatMs **不加千分位**（全站延时口径是 1200ms，不是 1,200ms）', () => {
    expect(formatMs(1000)).toBe('1000ms')
    expect(formatMs(1900)).toBe('1900ms')
    expect(formatMs(820)).toBe('820ms')
  })

  it('formatCostPer1k 有值 ⇒ $x.xxxx/1k', () => {
    expect(formatCostPer1k(0.0042)).toBe('$0.0042/1k')
  })
  it('formatCostPer1k 缺失 ⇒ —', () => {
    expect(formatCostPer1k(undefined)).toBe('—')
  })

  it('★ 稀疏 item：只给 NOT NULL 列也能过类型与 key 判定', () => {
    const sparse: ModelTaskIndexItem = {
      canonical_id: 3,
      task_type: 'chat',
      sample_count: 1,
      updated_at: '2026-10-07T10:05:00Z',
    }
    expect(modelTaskIndexKeyOf(sparse)).toBe('3|chat')
    expect(formatModelTaskRatePct(sparse.success_rate)).toBe('—')
    expect(formatMs(sparse.avg_latency_ms)).toBe('—')
    expect(formatCostPer1k(sparse.avg_cost_per_1k_usd)).toBe('—')
  })

  it('key 是 canonical_id|task_type，两条同模型不同任务不撞', () => {
    expect(modelTaskIndexKeyOf(item({ task_type: 'chat' }))).not.toBe(
      modelTaskIndexKeyOf(item({ task_type: 'code' })),
    )
  })

  it('★ 两条同模型**同任务**不同桶会撞 key —— 但同一响应只含一个桶（handler 的 WHERE bucket=$1）', () => {
    expect(modelTaskIndexKeyOf(item())).toBe(modelTaskIndexKeyOf(item({ sample_count: 9 })))
  })

  it('null item ⇒ ∅（不崩）', () => {
    expect(modelTaskIndexKeyOf(null)).toBe('∅')
  })
})