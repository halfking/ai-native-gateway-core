import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTurnsSessions,
  fetchSessionTurns,
  latencyOf,
  costNumber,
  TURNS_SESSIONS_MAX_LIMIT,
  SESSION_TURNS_MAX_LIMIT,
  type TurnsSessionsResponse,
} from './turnsSessions'
import {
  fetchRoutingAudit,
  describeAuditChange,
  ROUTING_AUDIT_MAX_DAYS,
  ROUTING_AUDIT_MAX_LIMIT,
  type RoutingAuditResponse,
} from './routingAudit'

/**
 * 会话/轮次 + 路由覆盖审计的契约测试（2026-10-07）。
 *
 * 这两个端点各有「同一份数据在两处用不同键名 / 两处用不同越界语义」的问题，
 * 都不会报错，只会静默取不到值或让用户吃 400。
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

const SESSIONS: TurnsSessionsResponse = { items: [], has_more: false, next_cursor: '' }

describe('latencyOf —— ★ 两个端点的延迟键名不同', () => {
  // sessions 列表：TurnGroupItem.latency_ms（omitempty）
  it('读 sessions 列表的 latency_ms', () => {
    expect(latencyOf({ latency_ms: 820 })).toBe(820)
  })

  // turns 树：SessionTurnTreeItem.latency（无 omitempty，可为 null）
  it('读 turns 树的 latency', () => {
    expect(latencyOf({ latency: 1200 })).toBe(1200)
  })

  // ★★ session_turns_tree.go:49 `LatencyMs *int \`json:"latency"\`` ——
  //   Go 字段名是 LatencyMs，JSON 键是 latency。照抄任一边到另一边都取不到，
  //   而取不到的表现是「延迟永远 undefined」而不是报错。
  it('★ latency_ms 为 0 时不得回退去读 latency（0 是真值不是缺失）', () => {
    expect(latencyOf({ latency_ms: 0, latency: 999 })).toBe(0)
  })

  // ★ null = 未知。用 0 冒充会让「这一轮慢」被误读成「这一轮很快」。
  it('latency 显式为 null ⇒ 返回 null（未知），不是 0', () => {
    expect(latencyOf({ latency: null })).toBeNull()
  })

  it('两个键都没有 ⇒ null', () => {
    expect(latencyOf({})).toBeNull()
    expect(latencyOf(null)).toBeNull()
    expect(latencyOf(undefined)).toBeNull()
  })
})

describe('turns sessions 分页是游标', () => {
  it('第 1 页不带 cursor', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(SESSIONS))
    await fetchTurnsSessions({ limit: 20 })
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('cursor=')
  })

  it('有 cursor 时原样回传（游标是会话签名的，不能改写）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(SESSIONS))
    await fetchTurnsSessions({ cursor: 'c1:abc' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('cursor=c1%3Aabc')
  })

  // ★ 后端越界是**静默回落 20**（turns_sessions.go:244-249），不是 400。
  //   发 999 用户看不到任何提示，只会拿到 20 条 ⇒ 必须前端夹。
  it('limit 越界被夹到 50（1..50）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(SESSIONS))
    await fetchTurnsSessions({ limit: 999 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain(`limit=${TURNS_SESSIONS_MAX_LIMIT}`)
  })

  it('limit=0 被夹到 1', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(SESSIONS))
    await fetchTurnsSessions({ limit: 0 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('limit=1')
  })

  it('turns 树 limit 夹到 100（上限由 NormalizePaginationParams 施加）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 's1', turns: [], count: 0, has_more: false, next_cursor: '' }))
    await fetchSessionTurns('s1', { limit: 9999 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain(`limit=${SESSION_TURNS_MAX_LIMIT}`)
  })

  it('session id 转义', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 's1', turns: [], count: 0, has_more: false, next_cursor: '' }))
    await fetchSessionTurns('a/b')
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/sessions/a%2Fb/turns')
  })
})

describe('costNumber —— 空串必须先挡掉', () => {
  // `Number('')` 是 0 不是 NaN。只判 Number.isFinite 会把「没有成本数据」
  // 渲染成 $0.0000 —— 那是在断言一个我们没有依据的数字（17 §11.20 同族）。
  it('空串 ⇒ null', () => {
    expect(costNumber('')).toBeNull()
    expect(costNumber('   ')).toBeNull()
  })
  it('0 成本是真值，必须返回 0', () => {
    expect(costNumber(0)).toBe(0)
    expect(costNumber('0')).toBe(0)
  })
  it('null / undefined ⇒ null', () => {
    expect(costNumber(null)).toBeNull()
    expect(costNumber(undefined)).toBeNull()
  })
  it('非数值文本 ⇒ null', () => {
    expect(costNumber('abc')).toBeNull()
  })
})

describe('routing audit 越界是 400，必须在发出前夹住', () => {
  const RESP: RoutingAuditResponse = {
    entries: [],
    count: 0,
    filter: { action: '', actor: '', override_id: '', days: '7' },
  }

  // ★ 与本仓库另一处语义相反：admin/audit_operations.go:95-103 的 limit
  //   越界是**静默 clamp**。两处不能互相照抄 —— 照抄会让这里直接 400。
  it('days 超 90 被夹到 90', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({ days: 365 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain(`days=${ROUTING_AUDIT_MAX_DAYS}`)
  })

  it('limit 超 1000 被夹到 1000', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({ limit: 99999 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain(`limit=${ROUTING_AUDIT_MAX_LIMIT}`)
  })

  it('limit=0 被夹到 1 而不是发出（后端 0 判越界 400）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({ limit: 0 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('limit=1')
  })

  // ★ 后端对 override_id 解析失败是**静默忽略该过滤条件**（:377-381）而不是报错。
  //   发非数字 ⇒ 用户以为在按 ID 过滤、实际没过滤。
  it('override_id 非数字时不发（避免静默忽略造成的假过滤）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({ override_id: Number.NaN })
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('override_id')
  })

  it('override_id 合法数字照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({ override_id: 42 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('override_id=42')
  })

  it('走 /api/admin/routing/overrides/audit', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingAudit({})
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/routing/overrides/audit')
  })
})

describe('describeAuditChange —— 字段缺失时返回 null，不拼残句', () => {
  const base = { id: 1, ts: '2026-10-07T00:00:00Z', action: 'update' as const }

  it('有内容时拼出可读摘要', () => {
    expect(describeAuditChange({ ...base, task_type: 'code', mode: 'auto', model_chosen: 'gpt-4o' })).toBe(
      'code · auto · gpt-4o',
    )
  })

  // ★ 多数字段是 omitempty。缺就**不要**拼出「从 到 」这种读不通的残句，
  //   让调用方走降级呈现。
  it('全缺时返回 null 而不是空串', () => {
    expect(describeAuditChange(base)).toBeNull()
  })
})
