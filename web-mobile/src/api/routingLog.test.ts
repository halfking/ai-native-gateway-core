import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRoutingLog,
  buildRoutingLogWindow,
  ROUTING_LOG_MAX_WINDOW_HOURS,
  ROUTING_LOG_MAX_LIMIT,
  type RoutingLogResponse,
} from './routingLog'
import {
  fetchWaterfall,
  fetchWaterfallByRequest,
  isWaterfallUnavailable,
  spanMs,
  laneLabel,
  type WaterfallResponse,
} from './dispatchWaterfall'

/**
 * 排障线后两件的契约测试（2026-10-07）。
 * 重点：routing-log 的**前缀**与**7 天硬窗**，waterfall 的**非 degraded 降级语义**。
 */

const fetchMock = vi.fn()

/** 测试夹具：后端 meta 恒发全字段（credential_routing_log.go:48-52）。 */
const META = {
  time_start: '2026-10-07T00:00:00Z',
  time_end: '2026-10-07T01:00:00Z',
  kind: 'all',
  model: '',
  result: 'all',
  limit: 50,
  offset: 0,
  duration_ms: 12,
}
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

describe('routing-log 路径前缀', () => {
  it('★ 走 /api/credentials/ 而不是 /api/admin/ —— 写错就是 404', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ meta: META, entries: [], total: 0 } satisfies RoutingLogResponse),
    )
    await fetchRoutingLog({ kind: 'routing' })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url.startsWith('/api/credentials/routing-log')).toBe(true)
    expect(url).not.toContain('/api/admin/')
  })

  it('limit > 500 由前端封顶（后端是静默 clamp，回显会与用户输入不符）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: META, entries: [], total: 0 }))
    await fetchRoutingLog({ limit: 9999 })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain(`limit=${ROUTING_LOG_MAX_LIMIT}`)
    expect(url).not.toContain('limit=9999')
  })

  it('limit <= 0 不发（后端会回落 100，发 0 只会让 meta 与预期不符）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: META, entries: [], total: 0 }))
    await fetchRoutingLog({ limit: 0, offset: -3 })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).not.toContain('limit=')
    expect(url).not.toContain('offset=')
  })
})

describe('buildRoutingLogWindow —— 7 天是 400 硬失败，必须发之前就夹', () => {
  const NOW = Date.parse('2026-10-07T12:00:00Z')

  it('正常窗口按小时回推', () => {
    const w = buildRoutingLogWindow(24, NOW)
    expect(w.time_end).toBe('2026-10-07T12:00:00.000Z')
    expect(w.time_start).toBe('2026-10-06T12:00:00.000Z')
  })

  // ★ 后端 credential_routing_log.go:235-237 对 >7d 直接 400，不是 clamp。
  //   不夹的话用户选「30 天」会直接吃一个 400。
  it('超 7 天的请求被夹到 7 天', () => {
    const w = buildRoutingLogWindow(30 * 24, NOW)
    const spanH = (Date.parse(w.time_end) - Date.parse(w.time_start)) / 3600_000
    expect(spanH).toBe(ROUTING_LOG_MAX_WINDOW_HOURS)
  })

  it('0 或负数夹到 1 小时（不发非法窗口）', () => {
    const w = buildRoutingLogWindow(0, NOW)
    expect(Date.parse(w.time_end) - Date.parse(w.time_start)).toBe(3600_000)
  })
})

describe('waterfall 降级判据 —— 本面没有 degraded 字段', () => {
  const base: Pick<WaterfallResponse, 'wired' | 'source'> = { wired: true, source: 'memory' }

  it('wired:false ⇒ 不可观测（投影没接上，不是「没有请求」）', () => {
    expect(isWaterfallUnavailable({ wired: false, source: 'memory' })).toBe(true)
  })

  it("source:'none' ⇒ 不可观测", () => {
    expect(isWaterfallUnavailable({ wired: true, source: 'none' })).toBe(true)
  })

  it('正常态不判不可观测', () => {
    expect(isWaterfallUnavailable(base)).toBe(false)
  })

  // ★ 反向锁定：空数组不是降级。「没有请求」是一个真结论，
  //   把它渲染成「观测面不可用」会掩盖真实问题。
  it('requests 为空但 wired:true ⇒ 不是不可观测', () => {
    const r: WaterfallResponse = {
      requests: [],
      bottleneck_diagnosis: { bottleneck: '', message: '' },
      enabled: true,
      wired: true,
      source: 'memory',
    }
    expect(isWaterfallUnavailable(r)).toBe(false)
  })
})

describe('spanMs —— 缺一端必须 null，不能 0', () => {
  it('两端齐全返回差值', () => {
    expect(spanMs('2026-10-07T00:00:00Z', '2026-10-07T00:00:03Z')).toBe(3000)
  })

  // ★★ 用 0 冒充「没记录到这一跳」会直接得出错误的瓶颈结论：
  //   「等待 0ms」和「没测到」在排障里是相反的指示。
  it('缺起点或终点返回 null', () => {
    expect(spanMs(null, '2026-10-07T00:00:03Z')).toBeNull()
    expect(spanMs('2026-10-07T00:00:00Z', undefined)).toBeNull()
    expect(spanMs(undefined, undefined)).toBeNull()
  })

  it('时间戳非法返回 null 而不是 NaN', () => {
    expect(spanMs('not-a-time', '2026-10-07T00:00:03Z')).toBeNull()
  })
})

describe('laneLabel —— 后端字段是 credential 不是 credential_id', () => {
  it('取 credential', () => {
    expect(laneLabel({ credential: 'prod-key-a', depth: 3 })).toBe('prod-key-a')
  })
  it('退回 credential_label', () => {
    expect(laneLabel({ credential_label: '备用', depth: 1 })).toBe('备用')
  })
  it('退回 model', () => {
    expect(laneLabel({ model: 'gpt-4o', depth: 0 })).toBe('gpt-4o')
  })
  // 反向锁定：没有标识时必须 null —— 渲染成字符串 'undefined' 是已知事故形态
  //  （17 §11.x 修过 ModelsView 的 undefined 计数）
  it('都没有时返回 null 而不是 undefined 字面量', () => {
    expect(laneLabel({ depth: 2 })).toBeNull()
  })
})

describe('waterfall URL 构造', () => {
  it('列表走 /api/admin/dispatch/waterfall', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ requests: [], enabled: true, wired: true }))
    await fetchWaterfall({ limit: 20, model: 'gpt-4o' })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('/api/admin/dispatch/waterfall?')
    expect(url).toContain('limit=20')
    expect(url).toContain('model=gpt-4o')
  })

  // 后端注释宣称 max 200 但实现无 clamp（main_dispatch.go:124-134）⇒ 前端自己封顶
  it('limit 由前端封到 200，不依赖后端那个不存在的 clamp', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ requests: [], enabled: true, wired: true }))
    await fetchWaterfall({ limit: 100000 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('limit=200')
  })

  it('单条瀑布的 request_id 转义', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ request: { request_id: 'r1', result: 'ok' } }))
    await fetchWaterfallByRequest('req/../x')
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('/api/admin/dispatch/waterfall/request/req%2F..%2Fx')
  })
})
