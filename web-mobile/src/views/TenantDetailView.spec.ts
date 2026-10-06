import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import TenantDetailView from './TenantDetailView.vue'
import {
  fetchTenant,
  fetchTenantUsers,
  fetchTenantKeys,
  fetchTenantStats,
  tenantUserNeverLoggedIn,
  tenantKeyNeverExpires,
  tenantDailyIsComplete,
  type TenantInfo,
  type TenantUser,
  type TenantKey,
  type TenantStats,
} from '@/api/tenants'
import { setLocale, locale } from '@/i18n'

/**
 * TenantDetailView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ **504** 是「查询超时、调小 days 重试」，**不是**「查不到」。
 * 2. ★★★★ 成本与积分**来自两张不同的表** ⇒ 对不上不是数据错。
 * 3. ★★★★ 日切 Asia/Shanghai vs 对账页 UTC —— **有意分叉**。
 * 4. ★★★★ `daily` 条数少于 days ⇒ 不能画折线。
 * 5. ★★★★ 详情页聚合比列表页更不可信（`_ =` 吞错，连日志都没有）。
 * 6. ★★★ `last_login_at` 键在值为 null = 从未登录；
 *    `expires_at` 键不存在 = **永不过期**。
 */

const pushMock = vi.fn()
const routeParams = { code: 'acme' as string | undefined }
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { get code() { return routeParams.code } } }),
  useRouter: () => ({ push: pushMock }),
}))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/tenants', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/tenants')>()
  return {
    ...actual,
    fetchTenant: vi.fn(),
    fetchTenantUsers: vi.fn(),
    fetchTenantKeys: vi.fn(),
    fetchTenantStats: vi.fn(),
  }
})

const tMock = fetchTenant as unknown as ReturnType<typeof vi.fn>
const uMock = fetchTenantUsers as unknown as ReturnType<typeof vi.fn>
const kMock = fetchTenantKeys as unknown as ReturnType<typeof vi.fn>
const sMock = fetchTenantStats as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function tenant(over: Record<string, unknown> = {}): TenantInfo {
  return { code: 'acme', name: 'Acme Corp', status: 'active', description: '', contact_email: 'ops@acme.test', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z', ...over } as TenantInfo
}

function user(over: Record<string, unknown> = {}): TenantUser {
  return { id: 1, tenant_id: 'acme', username: 'admin', display_name: 'Acme Admin', email: 'a@acme.test', role: 'tenant_admin', enabled: true, must_change_password: false, last_login_at: '2026-10-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z', ...over } as TenantUser
}

function key(over: Record<string, unknown> = {}): TenantKey {
  return { id: 5, tenant_id: 'acme', key_prefix: 'sk-abc…', enabled: true, status: 'active', application_id: 1, total_requests: 100, total_cost_usd: 1.5, created_at: '2026-02-01T00:00:00Z', ...over } as TenantKey
}

function day(n: number): TenantStats['daily'][number] {
  return { date: `2026-10-${String(n).padStart(2, '0')}`, requests: 0, success: 0, errors: 0, tokens: 0, credits: 0, cost_usd: 0 }
}

function stats(over: Record<string, unknown> = {}): TenantStats {
  return {
    days: 7,
    total_requests: 100, total_tokens: 3000, total_cost_usd: 1.5,
    unique_keys: 1, unique_models: 2, unique_apps: 1,
    total_credits: 500, input_tokens: 1000, output_tokens: 2000,
    cache_read_tokens: 0, cache_write_tokens: 0, avg_latency_ms: 250,
    by_model: [{ model: 'gpt-4o', requests: 100, tokens: 3000, credits: 500, cost_usd: 1.5 }],
    by_application: [{ application_code: 'app1', requests: 100, tokens: 3000, credits: 500, cost_usd: 1.5 }],
    daily: [day(1)],
    ...over,
  } as TenantStats
}

// ★ helper 不再无条件覆盖各 mock —— 否则「先设 reject / 先设降级数据」会被吃掉
type Over = {
  tenant?: unknown
  users?: unknown
  keys?: unknown
  stats?: unknown
}

async function mountView(over: Over = {}): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  // ★ 传 Error 就**拒**，传别的就是**resolve**。
  //   `mockResolvedValue(new Error(...))` 会真的 resolve 成一个 Error 对象 ——
  //   那条路径根本不会走 catch，判据就成了恒真。
  const set = (m: ReturnType<typeof vi.fn>, v: unknown, dft: unknown) => {
    if (v instanceof Error) m.mockRejectedValue(v)
    else m.mockResolvedValue(v ?? dft)
  }
  set(tMock, over.tenant, tenant())
  set(uMock, over.users, [user()])
  set(kMock, over.keys, [key()])
  set(sMock, over.stats, stats())
  const w = mount(TenantDetailView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  routeParams.code = 'acme'
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 504 必须与「查不到」分开', () => {
  it('★★★★★★ 504 ⇒ 明说「超时，调小天数重试」，不是「没数据」', async () => {
    const w = await mountView({ stats: new Error('tenant stats query timed out; retry with a smaller days window'), users: [], keys: [] })
    const warns = w.findAll('.tnd__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('504'))).toBe(true)
    expect(warns.some((x) => x.includes('调小统计天数'))).toBe(true)
    // ★★★ 不许被 404 那条文案吃掉
    expect(w.text()).not.toContain('这个租户码在库里不存在')
  })

  it('★★★★ 404 ⇒ 才是「租户不存在」', async () => {
    const w = await mountView({ tenant: new Error('tenant not found'), users: [], keys: [] })
    expect(w.text()).toContain('这个租户码在库里不存在')
    expect(w.text()).not.toContain('504')
  })

  it('★★★★ 503 ⇒ 明说「MaaS/DB 没配置」', async () => {
    const w = await mountView({ tenant: new Error('database not configured'), users: [], keys: [] })
    expect(w.text()).toContain('没有启用 MaaS')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 双源与时区分叉', () => {
  it('★★★★ 明说成本与积分来自**两张不同的表**', async () => {
    const w = await mountView()
    expect(w.text()).toContain('两张不同的表')
  })

  it('★★★★ 明说日切是 Asia/Shanghai、对账页是 UTC，且是**有意分叉**', async () => {
    const w = await mountView()
    const notes = w.findAll('.tnd__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('Asia/Shanghai') && x.includes('UTC'))).toBe(true)
    expect(notes.some((x) => x.includes('有意分叉'))).toBe(true)
  })

  it('★★★ 明说生效天数由后端限幅（<1⇒7、>365⇒365）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('回落 7')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ daily 完整性', () => {
  it('★★★★★ 条数 == days ⇒ 说「完整，可直接画」', async () => {
    const w = await mountView({ stats: stats({ days: 3, daily: [day(1), day(2), day(3)] }) })
    expect(w.text()).toContain('完整')
  })

  it('★★★★★ ★ 条数少于 days ⇒ 明说「不能画折线」', async () => {
    const w = await mountView()
    const warns = w.findAll('.tnd__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('不能画'))).toBe(true)
    // ★★ 条数与天数都出现在提示里，便于核对
    expect(w.text()).toContain('1')
  })

  it('★★★ 对照：服务端补齐时不出该警告', () => {
    const full = stats({ days: 3, daily: [day(1), day(2), day(3)] })
    expect(tenantDailyIsComplete(full)).toBe(true)
    expect(tenantDailyIsComplete(stats({ days: 7 }))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 详情页聚合比列表页更不可信', () => {
  it('★★★★★ 明说「连一行日志都不留」', async () => {
    const w = await mountView()
    const warns = w.findAll('.tnd__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('连一行日志都不留'))).toBe(true)
  })

  it('★★★★ 聚合键齐全 ⇒ 不出该警告', async () => {
    const rich = tenant({ user_count: 3, api_key_count: 5, requests_7d: 100, tokens_7d: 3000, credits_7d: 500, cost_7d_usd: 1.5, total_requests: 1000 })
    const w = await mountView({ tenant: rich })
    const warns = w.findAll('.tnd__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('连一行日志都不留'))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 两种「键缺失」语义', () => {
  it('★★★ last_login_at 为 null ⇒ 明说「从未登录」，并说清是 null 不是「查不到」', async () => {
    const w = await mountView({ users: [user({ last_login_at: null })] })
    const warns = w.findAll('.tnd__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('从未登录过') && x.includes('null'))).toBe(true)
    expect(tenantUserNeverLoggedIn(user({ last_login_at: null }))).toBe(true)
  })

  it('★★★ expires_at 键不存在 ⇒ 明说「**永不过期**」，不是「没查到」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('永不过期')
    expect(tenantKeyNeverExpires(key())).toBe(true)
  })

  it('★★ 有 expires_at ⇒ 显示时间而不是「永不过期」', async () => {
    const w = await mountView({ keys: [key({ expires_at: '2027-01-01T00:00:00Z' })] })
    expect(w.text()).toContain('过期时间')
    expect(w.text()).not.toContain('永不过期')
  })

  it('★★★ key_alias / owner_user 键不存在 ⇒ 显示「未设置」而不是空白', async () => {
    const w = await mountView()
    expect(w.text()).toContain('未设置')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 用户安全指标 + 排序方向', () => {
  it('★★ 待改密码 / 已停用 / 从未登录 三个计数都在', async () => {
    const w = await mountView({
      users: [
        user({ id: 1, must_change_password: true }),
        user({ id: 2, enabled: false }),
        user({ id: 3, last_login_at: null }),
      ],
    })
    expect(w.text()).toContain('待改密码')
    expect(w.text()).toContain('已停用')
    expect(w.text()).toContain('从未登录')
  })

  it('★★ 明说密钥倒序、用户正序（两个列表排序相反）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('倒序')
    expect(w.text()).toContain('正序')
  })

  it('★★ 明说本页只读', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
  })

  it('★★★★★★ ★ 先查成功、再查失败 ⇒ **旧数据必须被清掉**', async () => {
    // ★★★ 只验「首屏就失败」够不着 catch 里的清空路径 ——
    //   首屏本来就是 null，删掉清空语句照样全绿。
    const w = await mountView()
    expect(w.text()).toContain('Acme Admin')

    tMock.mockRejectedValue(new Error('boom'))
    await w.findAll('.tnd__btn')[1]!.trigger('click') // 刷新
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('boom')
    expect(w.text()).not.toContain('Acme Admin')
    expect(w.text()).not.toContain('sk-abc')
    expect(w.find('.tnd__table').exists()).toBe(false)
  })
})