import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRequestDetail,
  unwrapRequestDetail,
  isValidRequestId,
  bodyStatusOf,
  bodiesPresent,
  isInFlightSource,
  notFoundIsAlsoTenantDenied,
  REQUEST_DETAIL_SOURCES,
  REQUEST_DETAIL_PERSISTENCE,
  REQUEST_DETAIL_BODY_STATUSES,
  REQUEST_DETAIL_BODY_STATUS_UNKNOWN,
  REQUEST_DETAIL_MAX_BODY_BYTES,
  type RequestDetail,
} from './requestDetail'

/**
 * 统一请求详情的契约测试（2026-10-08）。
 *
 * ★★ 夹具逐字抄自 `domains/requestdetail/types.go:12-80` 的 json tag
 *   与 `admin/unified_detail.go:474` 的 `writeJSON(w, 200, detail)`。
 *
 * 后端逐条对应：
 *   types.go:12-18   Source 5 值
 *   types.go:21-24   Persistence 2 值
 *   types.go:38-71   Meta（全部指针字段带 omitempty ⇒ 键可能整个不存在）
 *   types.go:74-80   Detail（**顶层无包装键**）
 *   store.go:43-45   safeRequestIDPattern（三形态）
 *   store.go:440-451 ValidateRequestID（长度 8..128）
 *   unified_detail.go:433/447/455/468/472  五个状态码
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

/** 抄自 `Meta`（types.go:65-74）——**全部指针字段都是 omitempty**。 */
function meta(over: Record<string, unknown> = {}) {
  return {
    request_id: 'req-abcdef12',
    tenant_id: 't-1',
    gw_session_id: 'sess-1',
    gw_task_id: 'task-1',
    client_model: 'gpt-4o',
    request_status: 'success',
    success: true,
    latency_ms: 1200,
    turn_number: 3,
    body_status: 'available',
    ...over,
  }
}

/** 抄自 `Detail`（types.go:74-80）——顶层**没有**包装键。 */
function detailBody(over: Record<string, unknown> = {}) {
  return {
    source: 'request_logs',
    persistence: 'persisted',
    meta: meta(),
    bodies: { request_body: { a: 1 }, response_body: { b: 2 }, outbound_body: { c: 3 } },
    ...over,
  }
}

const HEX32 = 'a'.repeat(32)
const UUID = '123e4567-e89b-12d3-a456-426614174000'
const PREFIXED = 'req-abcdef12'

beforeEach(() => {
  // ★ stub 必须在 beforeEach：afterEach 的 unstubAllGlobals 会摘掉它
  vi.stubGlobal('fetch', fetchMock)
  vi.clearAllMocks()
  fetchMock.mockResolvedValue(jsonResponse(detailBody()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 路径与形状（逐字对 writeJSON）', () => {
  it('★★★★★★ 打 `/api/admin/request-detail/{id}`，默认**不带** omit_body', async () => {
    const d = await fetchRequestDetail(PREFIXED)
    expect(lastUrl()).toBe(`/api/admin/request-detail/${PREFIXED}`)
    expect(d.meta.request_id).toBe(PREFIXED)
  })

  it('★★★★★★ 响应**顶层无包装键**（writeJSON 传的是 detail 本身）', async () => {
    const d = unwrapRequestDetail(detailBody())
    expect(d.source).toBe('request_logs')
    expect(d.persistence).toBe('persisted')
    // ★ 关键：不存在 `data` / `detail` / `result` 之类的包装
    expect(Object.prototype.hasOwnProperty.call(d, 'data')).toBe(false)
    expect(Object.prototype.hasOwnProperty.call(d, 'detail')).toBe(false)
  })

  it('★★★★★★ 包一层 `{data:…}` 一律抛错（后端没有这一层）', () => {
    expect(() => unwrapRequestDetail({ data: detailBody() })).toThrow(/形状不符/)
    expect(() => unwrapRequestDetail({ result: detailBody() })).toThrow(/形状不符/)
  })

  it('★★★★★★ 顶层是数组/null/字符串 ⇒ 抛错', () => {
    for (const bad of [[], null, 'x', 42]) {
      expect(() => unwrapRequestDetail(bad)).toThrow(/形状不符/)
    }
  })

  it('★★★★★★ `meta` 缺失或 `request_id` 不是字符串 ⇒ 抛错', () => {
    expect(() => unwrapRequestDetail({ source: 'memory', persistence: 'in_flight' })).toThrow(/形状不符/)
    expect(() => unwrapRequestDetail({ meta: { request_id: 123 } })).toThrow(/形状不符/)
  })

  it('★ 抛错文案说清期望的形状', () => {
    expect(() => unwrapRequestDetail(null)).toThrow(/\{source, persistence, meta:\{request_id,…\}\}/)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ request_id 三形态（与 ValidateRequestID 同规则）', () => {
  it('★★★★★★ 三种合法形态都认', () => {
    expect(isValidRequestId(HEX32)).toBe(true)
    expect(isValidRequestId(UUID)).toBe(true)
    expect(isValidRequestId(PREFIXED)).toBe(true)
  })

  it('★★★★★★ hex32 大小写皆可（`(?i)`）', () => {
    expect(isValidRequestId('A'.repeat(32))).toBe(true)
    expect(isValidRequestId('0123456789abcdef0123456789ABCDEF')).toBe(true)
  })

  it('★★★★★★ 短于 8 或长于 128 ⇒ 拒', () => {
    expect(isValidRequestId('abcdefg')).toBe(false)      // 7
    expect(isValidRequestId('a'.repeat(129))).toBe(false)
    expect(isValidRequestId('a'.repeat(128))).toBe(true)
  })

  it('★★★★★★ 非法字符 ⇒ 拒（空格、斜杠、冒号…）', () => {
    for (const bad of ['req abcdefg', 'req/abcdef', 'req:abcdef', 'req%2Fx']) {
      expect(isValidRequestId(bad)).toBe(false)
    }
  })

  it('★★★★★★ **分隔符不可连用** —— `abc..def` 必拒（路径穿越向量）', () => {
    // ★ 正则注释自陈：旧式 `^[A-Za-z0-9._-]{8,128}$` 会接受 `abc..def`，
    //   而 `..` 在下游漏调 filepath.Base 时是路径穿越向量。
    expect(isValidRequestId('abcdef..')).toBe(false)
    expect(isValidRequestId('abc..defgh')).toBe(false)
    expect(isValidRequestId('abc--defgh')).toBe(false)
  })

  it('★★★★★★ 前缀形态**必须以字母开头**（拒 `.hidden` / `..` / `1abc`）', () => {
    expect(isValidRequestId('.abcdefgh')).toBe(false)
    expect(isValidRequestId('..abcdefgh')).toBe(false)
    expect(isValidRequestId('1abcdefgh')).toBe(false)
  })

  it('★★★★★★ 非法 id **不发请求**，直接本地拒（不拿它去换一个 400）', async () => {
    await expect(fetchRequestDetail('short')).rejects.toThrow(/非法 request_id/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★ id 会被 trim 后再发', async () => {
    await fetchRequestDetail(`  ${PREFIXED}  `)
    expect(lastUrl()).toBe(`/api/admin/request-detail/${PREFIXED}`)
  })

  it('★ id 会被 URL 编码（防止把特殊字符拼进路径）', async () => {
    const encoded = 'a'.repeat(32)
    await fetchRequestDetail(encoded)
    expect(lastUrl()).toContain(encoded)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ omit_body 只认 1 / true', () => {
  it('★★★★★★ omitBody=true ⇒ 只发 `omit_body=1`', async () => {
    await fetchRequestDetail(PREFIXED, { omitBody: true })
    expect(lastUrl()).toBe(`/api/admin/request-detail/${PREFIXED}?omit_body=1`)
  })

  it('★★★★★★ omitBody=false ⇒ **完全不带这个参数**（不发 `omit_body=0`）', async () => {
    // ★ 后端判定是 `== "1" || == "true"` ⇒ 发 `0` 不等于「要 body」，
    //   它只是「不过滤」，发出去会让人误以为语义被覆盖了。
    await fetchRequestDetail(PREFIXED, { omitBody: false })
    expect(lastUrl()).toBe(`/api/admin/request-detail/${PREFIXED}`)
    expect(lastUrl()).not.toContain('omit_body')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ source / persistence 字面量', () => {
  it('★★★★★ 五个 source 全在（内存/落盘/实时流/审计表/轮次表）', () => {
    expect([...REQUEST_DETAIL_SOURCES]).toEqual([
      'memory',
      'file',
      'live_stream',
      'request_logs',
      'session_turns',
    ])
  })

  it('★★★★★ 两个 persistence 值', () => {
    expect([...REQUEST_DETAIL_PERSISTENCE]).toEqual(['in_flight', 'persisted'])
  })

  it('★★★★ 在途源（memory / live_stream）能被识别出来', () => {
    expect(isInFlightSource({ source: 'memory' } as RequestDetail)).toBe(true)
    expect(isInFlightSource({ source: 'live_stream' } as RequestDetail)).toBe(true)
    expect(isInFlightSource({ source: 'request_logs' } as RequestDetail)).toBe(false)
    expect(isInFlightSource({ source: 'session_turns' } as RequestDetail)).toBe(false)
  })

  it('★ 未知 source 也不报错（后端加新源时不至于页面崩）', () => {
    expect(isInFlightSource({ source: 'brand_new' } as RequestDetail)).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ body_status 是三态，键缺失 ≠ 两个取值之一', () => {
  it('★★★★★ `available` / `unavailable` 照常返回', () => {
    expect(bodyStatusOf(meta({ body_status: 'available' }))).toBe('available')
    expect(bodyStatusOf(meta({ body_status: 'unavailable' }))).toBe('unavailable')
  })

  it('★★★★★ **键缺失 ⇒ unknown**（不是 available、也不是 unavailable）', () => {
    const m = meta()
    delete (m as Record<string, unknown>).body_status
    expect(bodyStatusOf(m)).toBe(REQUEST_DETAIL_BODY_STATUS_UNKNOWN)
  })

  it('★★★★★ 空串 / 非字符串 ⇒ unknown', () => {
    expect(bodyStatusOf(meta({ body_status: '' }))).toBe(REQUEST_DETAIL_BODY_STATUS_UNKNOWN)
    expect(bodyStatusOf(meta({ body_status: 1 }))).toBe(REQUEST_DETAIL_BODY_STATUS_UNKNOWN)
    expect(bodyStatusOf(null)).toBe(REQUEST_DETAIL_BODY_STATUS_UNKNOWN)
    expect(bodyStatusOf(undefined)).toBe(REQUEST_DETAIL_BODY_STATUS_UNKNOWN)
  })

  it('★ 契约里**只有**两个取值；`dropped` 有意不在其中', () => {
    expect([...REQUEST_DETAIL_BODY_STATUSES]).toEqual(['available', 'unavailable'])
    // ★★ 即便上游哪天发了 `dropped`，也不要当成合法值渲染成「已清理」
    expect((REQUEST_DETAIL_BODY_STATUSES as readonly string[]).includes('dropped')).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ bodies 整块可能不存在（omit_body / 无载荷）', () => {
  it('★★★★★ `bodies` 缺失 ⇒ bodiesPresent=false', () => {
    const d = unwrapRequestDetail(detailBody({ bodies: undefined }))
    expect(bodiesPresent(d)).toBe(false)
  })

  it('★★★★★ 三块都没有 ⇒ false', () => {
    const d = unwrapRequestDetail(detailBody({ bodies: {} }))
    expect(bodiesPresent(d)).toBe(false)
  })

  it('★ 任一块存在 ⇒ true（只看**键是否存在**，不解析内容）', () => {
    expect(bodiesPresent(unwrapRequestDetail(detailBody({ bodies: { response_body: { b: 2 } } })))).toBe(true)
  })

  it('★★ **不能**用 body_status 反推有无内容（JSON null 判无载荷、空容器判有载荷）', () => {
    // ★ 实测口径：available + bodies 缺失 = 矛盾但**合法**的响应
    const d = unwrapRequestDetail(detailBody({ meta: meta({ body_status: 'available' }), bodies: {} }))
    expect(bodyStatusOf(d.meta)).toBe('available')
    expect(bodiesPresent(d)).toBe(false) // ★ 判据只看键，不看 body_status
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 404 身兼两职：不存在 / 跨租户被拒', () => {
  it('★★★★★ `request detail not found` ⇒ 必须按「也可能是跨租户」处理', () => {
    expect(notFoundIsAlsoTenantDenied(new Error('request detail not found'))).toBe(true)
  })

  it('★★★★ 其它错误**不**被误判成租户问题', () => {
    for (const msg of [
      'request detail store not configured',
      'request body exceeds 10MB limit',
      'failed to load request detail',
      'invalid request id',
    ]) {
      expect(notFoundIsAlsoTenantDenied(new Error(msg))).toBe(false)
    }
  })

  it('★ 413 的上限就是 10MB', () => {
    expect(REQUEST_DETAIL_MAX_BODY_BYTES).toBe(10 * 1024 * 1024)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 503 / 413 原样透出，不被当成「查不到」', () => {
  it('★★ 未接线（503）⇒ 错误原样冒到调用方', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'request detail store not configured' } }, 503),
    )
    await expect(fetchRequestDetail(PREFIXED)).rejects.toThrow(/store not configured/)
  })

  it('★★ 超大 body（413，不是 500）⇒ 原样冒出', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'request body exceeds 10MB limit' } }, 413),
    )
    await expect(fetchRequestDetail(PREFIXED)).rejects.toThrow(/exceeds 10MB/)
  })
})
