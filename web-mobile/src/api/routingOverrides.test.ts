import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRoutingOverrides,
  createRoutingOverride,
  deleteRoutingOverride,
  extendRoutingOverride,
  isExpired,
  knownProfiles,
  knownModes,
  overrideSummary,
  type RoutingOverridesResponse,
} from './routingOverrides'

/**
 * 路由覆盖规则契约测试（2026-10-07）。
 * 重点：`active` 必须发字符串 "true"、DELETE 是**软删**、
 *       create 的 201 带 1-min reload 延迟说明。
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

function lastCall(): { url: string; method: string; body: Record<string, unknown> } {
  const [url, init] = fetchMock.mock.calls[0] as [string, { method: string; body?: string }]
  return {
    url: String(url),
    method: init.method,
    body: init.body ? (JSON.parse(init.body) as Record<string, unknown>) : {},
  }
}

const RESP: RoutingOverridesResponse = {
  overrides: [],
  count: 0,
  // ★ active 是字符串 "false"，不是 boolean
  filter: { task_type: '', profile: '', active: 'false' },
}

const OVERRIDE = {
  id: 1,
  task_type: 'code',
  profile: 'default',
  mode: 'auto',
  model_chosen: 'gpt-4o',
  reason: '测试',
  created_by: 'admin',
  expires_at: null,
  created_at: '2026-10-07T00:00:00Z',
  updated_at: '2026-10-07T00:00:00Z',
}

describe('active 参数必须发字符串 "true"', () => {
  // ★ 后端判的是 `r.URL.Query().Get("active") == "true"`。
  //   发 `active=false` 不会「只看过期的」，它**根本不过滤** ——
  //   与发 `active=1` / `active=yes` 一样是「不过滤」。
  it('active=true ⇒ 发出 "true"', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingOverrides({ active: true })
    expect(lastCall().url).toContain('active=true')
  })

  it('★ active=false ⇒ 完全不发（发 "false" 与不过滤等价，会误导用户）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingOverrides({ active: false })
    expect(lastCall().url).not.toContain('active=')
  })

  it('task_type / profile 照传', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(RESP))
    await fetchRoutingOverrides({ task_type: 'code', profile: 'default' })
    const u = decodeURIComponent(lastCall().url)
    expect(u).toContain('task_type=code')
    expect(u).toContain('profile=default')
  })
})

describe('create —— 只有 task_type 与 reason 必填', () => {
  it('发全字段', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ id: 9, status: 'created', message: 'refreshes on the next 1-min reload' }, 201),
    )
    await createRoutingOverride({
      task_type: 'code',
      profile: 'default',
      mode: 'auto',
      model_chosen: 'gpt-4o',
      reason: '临时兜底',
      expires_at: '2026-10-08T00:00:00Z',
    })
    const c = lastCall()
    expect(c.method).toBe('POST')
    expect(c.body.task_type).toBe('code')
    expect(c.body.reason).toBe('临时兜底')
  })

  // ★ 真正要锁的不是「发 null」——JSON.stringify 会把 undefined 键直接丢掉，
  //   所以实际发出去的是「键不存在」。对 Go 的 `*string` 而言，
  //   键缺失 ≡ 显式 null ≡ nil（都表示「没指定模型」）。
  //   ★ 但**空串完全不同**：`""` 会解成「指向空串的非 nil 指针」，
  //   即「指定了一个名字为空的模型」，规则永远不会匹配。
  //   ⇒ 判据锁的是「绝不能是空串」。
  it('★ 省略 model_chosen ⇒ 键不存在（Go 解为 nil），绝不是空串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: 9, status: 'created', message: 'ok' }, 201))
    await createRoutingOverride({ task_type: 'code', profile: 'default', mode: 'auto', reason: 'x' })
    const body = lastCall().body
    // 键不存在（而非 null 也不是 ""）
    expect('model_chosen' in body).toBe(false)
    expect(body.model_chosen).not.toBe('')
  })

  it('显式传空串会原样发出去（所以调用方不要这么传）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: 9, status: 'created', message: 'ok' }, 201))
    await createRoutingOverride({ task_type: 'code', profile: 'default', mode: 'auto', reason: 'x', model_chosen: '' })
    // 反向锁定：证明上一条断言测的不是「值恰好是 undefined」
    expect(lastCall().body.model_chosen).toBe('')
  })
})

describe('delete 是软删 —— 行还在，只是过期了', () => {
  it('★ 走 DELETE /{id}', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'deleted' }))
    await deleteRoutingOverride(7)
    const c = lastCall()
    expect(c.method).toBe('DELETE')
    expect(c.url).toContain('/api/admin/routing/overrides/7')
  })

  // 后端 SQL 是 `SET expires_at = NOW() - INTERVAL '1 second'` ⇒ 行仍在表里。
  // ⇒ 用不带 active=true 的列表查，它还会出现。UI 必须在删除后用 active=true 刷新。
  it('已过期的规则仍会被不带 active 的列表返回，所以 isExpired 是必需的', () => {
    const past = { ...OVERRIDE, expires_at: '2026-10-01T00:00:00Z' }
    const now = Date.parse('2026-10-07T00:00:00Z')
    expect(isExpired(past, now)).toBe(true)
  })
  it('expires_at 缺失 = 永不过期（不是「已过期」）', () => {
    expect(isExpired({ ...OVERRIDE, expires_at: null })).toBe(false)
    expect(isExpired({ ...OVERRIDE, expires_at: undefined })).toBe(false)
  })
  it('未来时间 ⇒ 未过期', () => {
    expect(isExpired({ ...OVERRIDE, expires_at: '2026-12-01T00:00:00Z' })).toBe(false)
  })
})

describe('extend 走 PATCH /{id}/extend', () => {
  it('发 expires_at', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'extended' }))
    await extendRoutingOverride(7, '2026-10-09T00:00:00Z')
    const c = lastCall()
    expect(c.method).toBe('PATCH')
    expect(c.url).toContain('/api/admin/routing/overrides/7/extend')
    expect(c.body.expires_at).toBe('2026-10-09T00:00:00Z')
  })
})

describe('候选值只能从现有规则取（后端无枚举）', () => {
  const rows = [
    { ...OVERRIDE, profile: 'default', mode: 'auto' },
    { ...OVERRIDE, id: 2, profile: 'fast', mode: 'pinned' },
    { ...OVERRIDE, id: 3, profile: 'default', mode: 'auto' },
  ]

  it('profile 去重排序', () => {
    expect(knownProfiles(rows)).toEqual(['default', 'fast'])
  })
  it('mode 去重排序', () => {
    expect(knownModes(rows)).toEqual(['auto', 'pinned'])
  })
  // 反向锁定：空 profile 不能进候选（后端接受空串，会建出一条永远不匹配的规则）
  it('空 profile 不进候选', () => {
    expect(knownProfiles([{ ...OVERRIDE, profile: '' }])).toEqual([])
  })
})

describe('overrideSummary', () => {
  it('有 model_chosen ⇒ 显示指向', () => {
    expect(overrideSummary(OVERRIDE)).toBe('code / default / auto → gpt-4o')
  })
  it('无 model_chosen ⇒ 不留尾部箭头', () => {
    expect(overrideSummary({ ...OVERRIDE, model_chosen: null })).toBe('code / default / auto')
  })
})
