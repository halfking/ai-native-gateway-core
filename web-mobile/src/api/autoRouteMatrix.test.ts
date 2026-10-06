import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRouteMatrix,
  fetchRouteFlow,
  metricKind,
  cellMayBeAbsent,
  formatMatrixValue,
  taskLabel,
  layerPrefixOf,
  nodesByLayer,
  linksFrom,
  P95_IS_ALWAYS_APPROXIMATE,
  SPECIFIED_MODEL_TASK_KEY,
  type MatrixResponse,
  type FlowResponse,
} from './autoRouteMatrix'

/**
 * auto-route 分析面（matrix / flow）的契约测试（2026-10-07）。
 *
 * 重点是三条**不能靠猜**的口径：
 * 1. 矩阵单元 0 的歧义（无记录 vs 真 0）；
 * 2. p95 在 7d 下必是近似，且 24h 与 7d 口径不同、响应里没有字段说明；
 * 3. `__specified__` 是合成键不是任务类型。
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

const MATRIX: MatrixResponse = {
  rows: ['gpt-4o', 'claude-sonnet-5'],
  cols: ['code_generation', 'summarization'],
  cells: [
    [120, 30],
    [0, 0],
  ],
  meta: { window: '7d', metric: 'count', row: 'task_type', row_aliases: null },
}

const FLOW: FlowResponse = {
  nodes: [
    { id: 'task:code_generation', label: 'code_generation', layer: 0 },
    { id: 'model:gpt-4o', label: 'gpt-4o', layer: 1 },
    { id: 'prov:openai', label: 'openai', layer: 2 },
  ],
  links: [{ source: 'task:code_generation', target: 'model:gpt-4o', value: 100, task_type: 'code_generation' }],
  meta: { window: '7d' },
}

describe('matrix：只发后端 allowlist 里的值', () => {
  it('全空 ⇒ 完全不发（后端各有默认值）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(MATRIX))
    await fetchRouteMatrix()
    expect(lastUrl()).not.toContain('window=')
    expect(lastUrl()).not.toContain('row=')
    expect(lastUrl()).not.toContain('metric=')
  })

  it('合法值照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(MATRIX))
    await fetchRouteMatrix({ window: '24h', row: 'work_type', metric: 'p95_ms' })
    const u = lastUrl()
    expect(u).toContain('window=24h')
    expect(u).toContain('row=work_type')
    expect(u).toContain('metric=p95_ms')
  })

  // ★ TypeScript 的 `as const` **编译后不存在**，`as any` 就能塞进
  //   `metric: 'latency'`，而后端对非法 metric 是 400 且不做 ToLower
  //   （analytics.go:114-117）。⇒ API 层必须做运行时 allowlist 守卫。
  it('★ 运行时塞进非法 metric ⇒ 不发（退后端默认 count），不吃 400', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(MATRIX))
    await fetchRouteMatrix({ metric: 'latency' as never })
    expect(lastUrl()).not.toContain('metric=')
  })

  it('★ 运行时塞进非法 row / window ⇒ 同样不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(MATRIX))
    await fetchRouteMatrix({ row: 'TASK_TYPE' as never, window: '12h' as never })
    const u = lastUrl()
    expect(u).not.toContain('row=')
    expect(u).not.toContain('window=')
  })

  // ★ 大小写也算非法：后端是 `switch` 精确匹配，没做 ToLower。
  it('★ 大小写错同样被拦（后端不做 ToLower）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(MATRIX))
    await fetchRouteMatrix({ metric: 'P95_MS' as never })
    expect(lastUrl()).not.toContain('metric=')
  })
})

describe('flow：window 也走同一道运行时守卫', () => {
  it('非法 window ⇒ 不发（退后端默认 7d）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(FLOW))
    await fetchRouteFlow({ window: '30d' as never })
    expect(lastUrl()).not.toContain('window=')
  })
})

describe('★ metricKind —— 每种指标口径不同，不能共用一个格式化', () => {
  it('四个指标四种口径', () => {
    expect(metricKind('count')).toBe('count')
    expect(metricKind('success_rate')).toBe('ratio')
    expect(metricKind('p95_ms')).toBe('duration')
    expect(metricKind('cost_usd')).toBe('money')
  })
  it('★ 词表外按 count 兜底（不是 money —— 未知值不该被当钱渲染）', () => {
    expect(metricKind('latency')).toBe('count')
    expect(metricKind(undefined)).toBe('count')
  })
})

describe('★★ 判据 1：矩阵 0 的歧义', () => {
  // ★ 关键推理：count 用 `COUNT(*)`，任何产出组 ≥ 1
  //   ⇒ count=0 ⟺ 该 (模型,任务) 组合无记录。这一个指标能下结论。
  it('count 下 0 可判定为「无记录」', () => {
    expect(cellMayBeAbsent('count')).toBe(false)
  })

  // ★ success_rate 0 可能是「全部失败」也可能是「无记录」——
  //   两者运营含义相反（后者不用管，前者是故障），不能下结论。
  it('success_rate 下 0 不可判定', () => {
    expect(cellMayBeAbsent('success_rate')).toBe(true)
  })
  it('p95_ms / cost_usd 下 0 不可判定', () => {
    expect(cellMayBeAbsent('p95_ms')).toBe(true)
    expect(cellMayBeAbsent('cost_usd')).toBe(true)
  })
})

describe('★ formatMatrixValue', () => {
  it('count 千分位', () => {
    expect(formatMatrixValue('count', 1234)).toBe('1,234')
  })
  // ★ 成功率是 0..1 的比例。×100 之前必须先判 0，否则显示 0.0%。
  it('ratio 渲染成百分比', () => {
    expect(formatMatrixValue('success_rate', 0.8123)).toBe('81.2%')
  })
  it('★ ratio 的 0 显示破折号（不能显示 0.0% —— 那是在说「全失败」）', () => {
    expect(formatMatrixValue('success_rate', 0)).toBe('—')
  })
  it('duration 毫秒', () => {
    expect(formatMatrixValue('p95_ms', 1234.6)).toBe('1235ms')
  })
  it('money 两位小数（极小值四位）', () => {
    expect(formatMatrixValue('cost_usd', 1.5)).toBe('$1.50')
    expect(formatMatrixValue('cost_usd', 0.0001)).toBe('$0.0001')
  })
  it('缺值 / NaN ⇒ 破折号', () => {
    expect(formatMatrixValue('count', null)).toBe('—')
    expect(formatMatrixValue('count', Number.NaN)).toBe('—')
  })
})

describe('★ 判据 2：p95 口径', () => {
  it('p95 标记为恒近似（响应里没有字段能说明走了哪条路）', () => {
    expect(P95_IS_ALWAYS_APPROXIMATE).toBe(true)
  })
})

describe('★ 判据 3：__specified__ 是合成键', () => {
  it('映射成可读文案', () => {
    const tr = (k: string) => (k === 'matrix.specified' ? '指定模型' : k)
    expect(taskLabel(SPECIFIED_MODEL_TASK_KEY, tr)).toBe('指定模型')
  })
  it('unknown 也映射（后端兜底值）', () => {
    const tr = (k: string) => (k === 'matrix.unknownTask' ? '未分类' : k)
    expect(taskLabel('unknown', tr)).toBe('未分类')
  })
  it('真实任务类型原样透出', () => {
    const tr = (k: string) => k
    expect(taskLabel('code_generation', tr)).toBe('code_generation')
  })
  it('★ 绝不把 __specified__ 直接渲染出去', () => {
    const tr = (k: string) => k
    expect(taskLabel(SPECIFIED_MODEL_TASK_KEY, tr)).not.toBe(SPECIFIED_MODEL_TASK_KEY)
  })
})

describe('flow：分层', () => {
  it('只发 window', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(FLOW))
    await fetchRouteFlow({ window: '24h' })
    expect(lastUrl()).toContain('window=24h')
  })

  it('★ 未知前缀归到最后一层（不排中间）', () => {
    expect(layerPrefixOf('task:x')).toBe('task:')
    expect(layerPrefixOf('model:x')).toBe('model:')
    expect(layerPrefixOf('prov:x')).toBe('prov:')
    expect(layerPrefixOf('weird:x')).toBe('prov:')
  })

  it('按层分组，缺层补空数组（不返回 undefined）', () => {
    const g = nodesByLayer([{ id: 'task:a', label: 'a', layer: 0 }])
    expect(g[0]!.length).toBe(1)
    expect(g[1]!.length).toBe(0)
    expect(g[2]!.length).toBe(0)
  })

  it('★ 层号越界归到最后一层（不丢节点）', () => {
    const g = nodesByLayer([
      { id: 'a', label: 'a', layer: 0 },
      { id: 'b', label: 'b', layer: 99 },
    ])
    expect(g.flat().length).toBe(2)
  })

  it('nodes/links 为 null 也不崩', () => {
    expect(nodesByLayer(null).flat().length).toBe(0)
    expect(linksFrom(null, 'task:a')).toEqual([])
  })

  it('linksFrom 按源节点过滤', () => {
    expect(linksFrom(FLOW.links, 'task:code_generation').length).toBe(1)
    expect(linksFrom(FLOW.links, 'model:gpt-4o').length).toBe(0)
  })
})
