import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTenants,
  unwrapTenants,
  fetchTenant,
  unwrapTenant,
  fetchTenantUsers,
  unwrapTenantUsers,
  fetchTenantKeys,
  unwrapTenantKeys,
  fetchTenantStats,
  unwrapTenantStats,
  tenantStatsDaysClamped,
  tenantMissingAggKeys,
  tenantUsageMayBeDegraded,
  tenantUserNeverLoggedIn,
  tenantUserHasLastLogin,
  tenantUsersNeedingPasswordChange,
  tenantUsersDisabled,
  tenantKeyNeverExpires,
  tenantKeyHasAlias,
  tenantKeyHasOwner,
  tenantDailyIsComplete,
  tenantDailyTzNote,
  TENANT_STATS_DAYS_DEFAULT,
  TENANT_STATS_DAYS_MAX,
  TENANT_STATS_DAY_TZ,
  TENANT_OPTIONAL_AGG_KEYS,
  type TenantInfo,
  type TenantUser,
  type TenantKey,
  type TenantStats,
} from './tenants'

/**
 * tenants 只读面的契约测试（2026-10-08）。
 *
 * ★★ 五条端点**全部返回裸结构**：list / users / keys ⇒ **裸数组**；
 *    get / stats ⇒ **裸对象**。误当 `{items}` 解包会 100% 抛错。
 *
 * 后端逐条对应：
 *   admin/handler.go:926-927      两条注册都是 h.superAdmin(...)
 *   admin/tenants.go:145          sub = SplitN(path,"/",2) 的尾部
 *   admin/tenants.go:145-176      路由分派 + unknown sub-resource 404
 *   admin/tenants.go:213-215      isModelPoliciesSubResource（2026-06-23 修）
 *   admin/tenants.go:288-296      usageCtx 独立 1.5s 预算（主查询 5s）
 *   admin/tenants.go:305-338      attachTenantUsage7d 失败只 slog 然后 return
 *   admin/tenants.go:~40          tenantInfo（7 个 omitempty 聚合键）
 *   admin/tenants.go:~79-110      stats totals：usage_ledger vs logsTable 双源
 *   admin/tenants.go:~198-215     daily 用 generate_series 补零 + Asia/Shanghai
 *   admin/users.go:21-32          userInfo（last_login_at 无 omitempty）
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

/** 抄自 `tenantInfo`（admin/tenants.go）—— 7 个聚合键**带 omitempty**。 */
function tenant(over: Record<string, unknown> = {}): TenantInfo {
  return {
    code: 'acme',
    name: 'Acme Corp',
    status: 'active',
    description: '',
    contact_email: 'ops@acme.test',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    ...over,
  } as TenantInfo
}

/** 抄自 `userInfo`（admin/users.go:21-32）。 */
function user(over: Record<string, unknown> = {}): TenantUser {
  return {
    id: 1,
    tenant_id: 'acme',
    username: 'admin',
    display_name: 'Acme Admin',
    email: 'admin@acme.test',
    role: 'tenant_admin',
    enabled: true,
    must_change_password: false,
    // ★ 指针但**无** omitempty ⇒ 键一定在
    last_login_at: '2026-10-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  } as TenantUser
}

/** 抄自 handler 内联的 `tenantKeyInfo`。 */
function key(over: Record<string, unknown> = {}): TenantKey {
  return {
    id: 5,
    tenant_id: 'acme',
    key_prefix: 'sk-abc…',
    enabled: true,
    status: 'active',
    application_id: 1,
    total_requests: 100,
    total_cost_usd: 1.5,
    created_at: '2026-02-01T00:00:00Z',
    ...over,
  } as TenantKey
}

/** 抄自 handler 内联的 `tenantStats`。 */
function stats(over: Record<string, unknown> = {}): TenantStats {
  return {
    days: 7,
    total_requests: 100,
    total_tokens: 3000,
    total_cost_usd: 1.5,
    unique_keys: 1,
    unique_models: 2,
    unique_apps: 1,
    total_credits: 500,
    input_tokens: 1000,
    output_tokens: 2000,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    avg_latency_ms: 250,
    by_model: [{ model: 'gpt-4o', requests: 100, tokens: 3000, credits: 500, cost_usd: 1.5 }],
    by_application: [{ application_code: 'app1', requests: 100, tokens: 3000, credits: 500, cost_usd: 1.5 }],
    daily: [
      { date: '2026-10-01', requests: 100, success: 98, errors: 2, tokens: 3000, credits: 500, cost_usd: 1.5 },
    ],
    ...over,
  } as TenantStats
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

describe('★★★★★★ 档位：两条注册都是 h.superAdmin', () => {
  it('★★★★★★ 五条端点都在 `/api/admin/tenants` 下', async () => {
    const urls: string[] = []
    fetchMock.mockResolvedValueOnce(jsonResponse([tenant()]))
    await fetchTenants()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(tenant()))
    await fetchTenant('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse([user()]))
    await fetchTenantUsers('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse([key()]))
    await fetchTenantKeys('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(stats()))
    await fetchTenantStats('acme', { days: 7 })
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/tenants',
      '/api/admin/tenants/acme',
      '/api/admin/tenants/acme/users',
      '/api/admin/tenants/acme/keys',
      '/api/admin/tenants/acme/stats?days=7',
    ])
  })

  it('★★★★★ status 筛选只在非空时才发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([tenant()]))
    await fetchTenants()
    expect(lastUrl()).not.toContain('status')

    fetchMock.mockResolvedValueOnce(jsonResponse([tenant()]))
    await fetchTenants({ status: 'active' })
    expect(lastUrl()).toBe('/api/admin/tenants?status=active')
  })

  it('★★★★★ ★ 租户码必须 encode（后端按 `/` 切段）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(tenant()))
    await fetchTenant('a/b')
    expect(lastUrl()).toBe('/api/admin/tenants/a%2Fb')
  })
})

describe('★★★★★★ 五条端点都是**裸结构**', () => {
  it('★★★★★★ list 是**裸数组**，误当 `{items}` ⇒ 抛错', () => {
    expect(() => unwrapTenants([tenant()])).not.toThrow()
    // ★★ 本仓多数 admin 端点返回 `{items}`，这里**不是**
    expect(() => unwrapTenants({ items: [tenant()] })).toThrow(/形状不符/)
    expect(() => unwrapTenants(null)).toThrow(/形状不符/)
  })

  it('★★★★★★ users / keys 也是**裸数组**', () => {
    expect(() => unwrapTenantUsers({ items: [user()] })).toThrow(/形状不符/)
    expect(() => unwrapTenantKeys({ data: [key()] })).toThrow(/形状不符/)
  })

  it('★★★★★ get / stats 是**裸对象**，不能有 `items` 包装', () => {
    expect(() => unwrapTenant({ items: tenant() })).toThrow(/形状不符/)
    expect(() => unwrapTenantStats({ items: [] })).toThrow(/形状不符/)
  })

  it('★★★★★ ★ 列表形状喂给详情解包器必须抛错（互喂）', () => {
    expect(() => unwrapTenant([tenant()])).toThrow(/形状不符/)
    expect(() => unwrapTenantStats(stats().by_model)).toThrow(/形状不符/)
  })

  it('★★★★ stats 三个数组键缺任一 ⇒ 抛错', () => {
    expect(() => unwrapTenantStats({ days: 7, by_application: [], daily: [] })).toThrow(/形状不符/)
    expect(() => unwrapTenantStats({ days: 7, by_model: [], daily: [] })).toThrow(/形状不符/)
    expect(() => unwrapTenantStats({ days: 7, by_model: [], by_application: [] })).toThrow(/形状不符/)
  })
})

describe('★★★★★★ 7 个聚合键带 omitempty ⇒ 缺键不可下结论', () => {
  it('★★★★★★ 默认夹具（无聚合）⇒ 7 个键**全部**缺失', () => {
    const t = tenant()
    const missing = tenantMissingAggKeys(t)
    expect(missing).toHaveLength(7)
    expect(missing).toEqual([...TENANT_OPTIONAL_AGG_KEYS])
  })

  it('★★★★★★ ★★ 「没 7 天用量」这个结论**不可靠**（富化超时降级也是 0）', () => {
    // ★ `attachTenantUsage7d` 有独立 1.5s 预算，失败只 slog.Warn 然后 return
    //   ⇒ 四个字段留在 0，客户端分辨不出「真没用量」与「没跑完」。
    expect(tenantUsageMayBeDegraded(tenant())).toBe(true)
  })

  it('★★★★★ 富化成功（键都在）⇒ 不再提示可能降级', () => {
    const rich = tenant({
      user_count: 3,
      api_key_count: 5,
      requests_7d: 100,
      tokens_7d: 3000,
      credits_7d: 500,
      cost_7d_usd: 1.5,
      total_requests: 1000,
    })
    expect(tenantMissingAggKeys(rich)).toHaveLength(0)
    expect(tenantUsageMayBeDegraded(rich)).toBe(false)
  })

  it('★★★ 只缺 requests_7d 也会被判「可能降级」', () => {
    const partial = tenant({ credits_7d: 500, cost_7d_usd: 1.5 })
    expect('requests_7d' in partial).toBe(false)
    expect(tenantUsageMayBeDegraded(partial)).toBe(true)
  })
})

describe('★★★★★ stats 的限幅：<1 ⇒ 回落 7、>365 ⇒ clamp', () => {
  it('★★★★★ 0 / 负数 ⇒ 回落 7（不是 clamp 到 1）', () => {
    expect(tenantStatsDaysClamped(0)).toBe(TENANT_STATS_DAYS_DEFAULT)
    expect(tenantStatsDaysClamped(-3)).toBe(7)
    expect(tenantStatsDaysClamped(1)).toBe(1)
  })

  it('★★★★★ > 365 ⇒ 365', () => {
    expect(tenantStatsDaysClamped(366)).toBe(TENANT_STATS_DAYS_MAX)
    expect(tenantStatsDaysClamped(30)).toBe(30)
  })
})

describe('★★★★★★ daily：generate_series 补零 + Asia/Shanghai 日切', () => {
  it('★★★★★★ 条数**恒等于** days（后端补零，不会缺格）', () => {
    const full = stats({ days: 3, daily: [1, 2, 3].map((d) => ({ date: `2026-10-0${d}`, requests: 0, success: 0, errors: 0, tokens: 0, credits: 0, cost_usd: 0 })) })
    expect(tenantDailyIsComplete(full)).toBe(true)
  })

  it('★★★★★ 条数少于 days ⇒ 判为**不完整**（后端降级过，不能画折线）', () => {
    expect(tenantDailyIsComplete(stats({ days: 7 }))).toBe(false)
  })

  it('★★★★★★ 日切是 Asia/Shanghai；对账页用 UTC —— **有意分叉**', () => {
    expect(TENANT_STATS_DAY_TZ).toBe('Asia/Shanghai')
    expect(tenantDailyTzNote()).toBe('Asia/Shanghai')
  })
})

describe('★★★ 两种「键缺失」语义并存', () => {
  it('★★★ userInfo.last_login_at **无** omitempty ⇒ 键在值为 null = 从未登录', () => {
    const never = user({ last_login_at: null })
    expect('last_login_at' in never).toBe(true)
    expect(tenantUserNeverLoggedIn(never)).toBe(true)
    expect(tenantUserHasLastLogin(never)).toBe(false)
  })

  it('★★★ ★ 「从未登录」与「键缺失」是两回事', () => {
    // ★★ 不能造一个「键缺失」的对象来证明这件事 —— Go 那边 key 一定在。
    //   判据改为「键存在时，null 与非空串两种取值都能正确判读」。
    expect(tenantUserNeverLoggedIn(user({ last_login_at: null }))).toBe(true)
    expect(tenantUserNeverLoggedIn(user({ last_login_at: '2026-10-01T00:00:00Z' }))).toBe(false)
  })

  it('★★★ tenantKeyInfo 的 4 个指针**带** omitempty ⇒ 键可整个不存在', () => {
    const bare = key()
    for (const k of ['key_alias', 'owner_user', 'application_code', 'expires_at']) {
      expect(k in bare, `不该有 ${k}`).toBe(false)
    }
    expect(tenantKeyNeverExpires(bare)).toBe(true)
    expect(tenantKeyHasAlias(bare)).toBe(false)
    expect(tenantKeyHasOwner(bare)).toBe(false)
  })

  it('★★ 键存在时各判据都能正确判读', () => {
    const k = key({ key_alias: 'prod', owner_user: 'alice', expires_at: '2027-01-01T00:00:00Z' })
    expect(tenantKeyNeverExpires(k)).toBe(false)
    expect(tenantKeyHasAlias(k)).toBe(true)
    expect(tenantKeyHasOwner(k)).toBe(true)
  })

  it('★★ 「expires_at 键不存在」= **永不过期**，不是「没查到」', () => {
    expect(tenantKeyNeverExpires(key())).toBe(true)
  })
})

describe('★★ 用户侧的三个计数', () => {
  it('★★ 被要求改密码 / 停用的用户数', () => {
    const users = [
      user({ id: 1 }),
      user({ id: 2, must_change_password: true }),
      user({ id: 3, enabled: false }),
      user({ id: 4, must_change_password: true, enabled: false }),
    ]
    expect(tenantUsersNeedingPasswordChange(users)).toBe(2)
    expect(tenantUsersDisabled(users)).toBe(2)
  })

  it('★ 空清单 ⇒ 两个计数都是 0（不是 null）', () => {
    expect(tenantUsersNeedingPasswordChange([])).toBe(0)
    expect(tenantUsersDisabled([])).toBe(0)
  })
})

describe('★★ 错误状态码语义', () => {
  it('★★★★★ ★★ 504 是「查询超时，调小 days 重试」，不是「查不到」', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'tenant stats query timed out; retry with a smaller days window' } }, 504),
    )
    await expect(fetchTenantStats('acme', { days: 365 })).rejects.toThrow(/timed out|smaller days/)
  })

  it('★★★★ 404 `tenant not found` ⇒ 直接透出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'tenant not found' } }, 404))
    await expect(fetchTenant('nope')).rejects.toThrow(/tenant not found/)
  })

  it('★★★ 503 `database not configured`（h.db == nil）⇒ 透出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'database not configured' } }, 503))
    await expect(fetchTenants()).rejects.toThrow(/database not configured/)
  })

  it('★★★ 404 `unknown sub-resource: <sub>` ⇒ 透出（未知子资源）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'unknown sub-resource: bogus' } }, 404))
    await expect(fetchTenantUsers('acme')).rejects.toThrow(/unknown sub-resource/)
  })
})