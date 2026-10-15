import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import ErrorsTrendView from './ErrorsTrendView.vue'
import { setLocale } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchErrorsTrend,
  errorsTrendRoleIsAdminTier,
  errorsTrendRoleSeesAllTenants,
  errorsTrendTenantScopeLabel,
  errorsTrendParamsAreAcceptable,
  errorsTrendGranularityIsDefault,
  errorsTrendGranularityEchoIsConsistent,
  errorsTrendDefaultGranularity,
  errorsTrendSummaryMatchesSeries,
  errorsTrendUniqueRequestsIsUpperBound,
  errorsTrendSeriesIsAscending,
  errorsTrendWindowIsGenuinelyEmpty,
  errorsTrendPreAggregationMissed,
  errorsTrendPointHasSupplierDim,
  errorsTrendPointHasErrorTypeDim,
  errorsTrendFilterIsWildcard,
  ERRORS_TREND_HOURS,
  ERRORS_TREND_GRANULARITIES,
  ERRORS_TREND_BREAKDOWN_LIMIT,
  ERRORS_TREND_TENANT_ROLE,
  ERRORS_TREND_GLOBAL_ROLES,
  type ErrorsTrendResponse,
  type ErrorsTrendPoint,
} from '@/api/errorsTrend'

/**
 * 供应商错误趋势面回归护栏（2026-10-14，第一百一十三批）。
 *
 * ★★★★★★ 本批的**核心结论是一个权限档判断**，不是一句「admin 档 ⇒ 不设
 *   requiresRole」。四步链条全部当场读码核过：
 *   1. `admin/handler.go:1270` `admin(h.errorsTrendHandlers.getErrorsTrend)`
 *      ⇒ `admin.AdminMiddleware`（`admin/auth.go:85-138`）**只认证、不检 role**。
 *      `admin/users.go:307-309` 的注释把这件事写成显式设计：
 *      > AdminMiddleware authenticates but intentionally does not enforce an administrative role.
 *   2. `getErrorsTrend`（`admin/errors_trend.go:75-146`）自己**也不检 role**
 *      （只查 db/hours/granularity/credential_id）。
 *   3. 租户范围取 `EffectiveTenantIDAll(r)` ⇒ `admin/context.go:69-74`：
 *      `IsTenantAdmin(r) ? GetTenantID(r) : ""` —— **非 tenant_admin 一律空串**，
 *      而空串在三处 SQL 里都命中 `AND ($7 = '' OR …)` ⇒ **查全租户**。
 *   4. ⇒★ effective role 既非 super_admin 也非 tenant_admin 的主体
 *      （典型 `user`）**会读到全租户**，而这个角色**可达**：
 *      `admin/shared_principal.go:42` 的 `roleRank` 含 `"user"`，
 *      且 `:46-48` **只在本地角色更低时下调** ⇒ 本地 super_admin + 外来 `user`
 *      ⇒ 生效角色留在 `user`。
 *
 * ⇒★★ 处置是 `requiresRole: 'admin'`：**缺省**会让 `navItemsFor` 的默认分支
 *   `return true` 把它显示给 `user`；**`'super_admin'`** 会连 `tenant_admin`
 *   的本租户数据一起挡掉（拿功能换安全）。
 *
 * ⚠️⚠️ **抽屉隐藏 ≠ 路由拦截**：`src/hyper-wiring.ts:34-45` 的 `beforeEach`
 *   **只查登录态**，`meta.requiresRole` **全仓无人读取** ⇒ 深链照样打开页面
 *   ⇒ 视图内必须自己再兜一层（前端严于后端是**兜底不是修复**）。
 *
 * 判据钉十组不变量：
 *  ① ★★★★★★ 权限档 = `'admin'`：user 挡、tenant_admin/super_admin 不误伤。
 *  ② ★★★★ 参数守卫：hours 三值枚举、granularity 三档、credentialId 正整数。
 *  ③ ★★★★ 三条可自验不变式，**每条都配正控**（正常载荷不得误报）。
 *  ④ ★★★★ `source` 的两档语义：`fallback` **不等于**「没有错误」。
 *  ⑤ ★★★★ 零行是**二义**的：`stats` + 空序列 ≠ 确实没错误。
 *  ⑥ ★★★★ 空 map ⇒ **整个键省略**（不是 `{}` / `null`）。
 *  ⑦ ★★★ `top_*` 是**恒数组**，且满 10 条即被截断。
 *  ⑧ ★★★ 粒度回显只在**没显式指定**时才可核对。
 *  ⑨ ★★★ 已生效过滤条件要**据实回显**（`'all'` 与 `''` 都算不过滤）。
 *  ⑩ ★★ 触控控件 ≥48 CSS px。
 *  ⑪ ★★★★★ 后端租户推导链的结构层钉法（读 Go 源码）。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function point(over: Partial<ErrorsTrendPoint> = {}): ErrorsTrendPoint {
  return {
    timestamp: '2026-10-14T00:00:00Z',
    error_count: 1,
    unique_requests: 1,
    affected_users: 1,
    ...over,
  }
}

function resp(over: Partial<ErrorsTrendResponse> = {}): ErrorsTrendResponse {
  const time_series = over.time_series ?? [point(), point({ timestamp: '2026-10-14T01:00:00Z', error_count: 2 })]
  return {
    source: 'stats',
    granularity: 'hour',
    hours: 24,
    since: '2026-10-13T00:00:00Z',
    until: '2026-10-14T00:00:00Z',
    time_series,
    summary: {
      total_errors: time_series.reduce((a, p) => a + p.error_count, 0),
      unique_requests: time_series.reduce((a, p) => a + p.unique_requests, 0),
      top_error_types: [],
      top_suppliers: [],
      affected_credentials: 2,
    },
    ...over,
  } as ErrorsTrendResponse
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'tenant_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/errorsTrend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/errorsTrend')>()
  return { ...actual, fetchErrorsTrend: vi.fn() }
})

const mFetch = fetchErrorsTrend as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(ErrorsTrendView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/ErrorsTrendView.vue'), 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'tenant_admin'
  mFetch.mockResolvedValue(resp())
})

// ── ① 权限档 = 'admin' ────────────────────────────────────────────────────

describe('① 权限档是 admin（不是缺省，也不是 super_admin）', () => {
  it('①-1 ★★★★ 全租户只对 super_admin / admin_key 成立', () => {
    expect([...ERRORS_TREND_GLOBAL_ROLES]).toEqual(['super_admin', 'admin_key'])
    expect(errorsTrendRoleSeesAllTenants('super_admin')).toBe(true)
    expect(errorsTrendRoleSeesAllTenants('admin_key')).toBe(true)
    expect(errorsTrendRoleSeesAllTenants('tenant_admin')).toBe(false)
    // ★★★ 这条是本批的核心：`user` **不在**特权集合里，
    //   而后端 `EffectiveTenantIDAll` 对它返回空串 = 查全租户。
    expect(errorsTrendRoleSeesAllTenants('user')).toBe(false)
  })

  it('①-2 ★★★★ 可访问集合 = 全租户两个 ∪ tenant_admin（恰好三个）', () => {
    expect(ERRORS_TREND_TENANT_ROLE).toBe('tenant_admin')
    expect(errorsTrendRoleIsAdminTier('super_admin')).toBe(true)
    expect(errorsTrendRoleIsAdminTier('admin_key')).toBe(true)
    expect(errorsTrendRoleIsAdminTier('tenant_admin')).toBe(true)
    expect(errorsTrendRoleIsAdminTier('user')).toBe(false)
    expect(errorsTrendRoleIsAdminTier('')).toBe(false)
    // ★★ 正控：两个集合**不同** —— tenant_admin 能访问但只看得到本租户。
    expect(errorsTrendRoleIsAdminTier('tenant_admin')).not.toBe(errorsTrendRoleSeesAllTenants('tenant_admin'))
  })

  it('①-3 ★★★ 租户口径三值互斥且语义正确', () => {
    expect(errorsTrendTenantScopeLabel('super_admin')).toBe('all')
    expect(errorsTrendTenantScopeLabel('admin_key')).toBe('all')
    expect(errorsTrendTenantScopeLabel('tenant_admin')).toBe('own')
    expect(errorsTrendTenantScopeLabel('user')).toBe('unexpected')
  })

  it("①-4 ★★★★★ 抽屉席必须声明 requiresRole:'admin'，缺省等于对 user 也显示", () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'errors-trend')
    expect(seat).toBeDefined()
    expect(seat!.requiresRole).toBe('admin')
    // ★★ 反向钉法：若退化成缺省，`navItemsFor` 的默认分支恒 true，
    //   等于把跨租户口子做成一键入口。
    expect(seat!.requiresRole).not.toBeUndefined()
  })

  it('①-5 ★★★★ role=user 时页面渲染「无权」且**不发请求**', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(hasId(w, 'et-nope')).toBe(true)
    expect(hasId(w, 'et-nope-msg')).toBe(true)
    // ★★ 兜底的关键：不只是「藏起来」，而是**连请求都不发**
    expect(hasId(w, 'et-load')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-6 ★★★★ role=user 时据实标出这是「非预期口径」', async () => {
    authState.role = 'user'
    const w = await mountView()
    // ★ `unexpected` 直接进文案 —— 页面不假装自己是安全的
    expect(byId(w, 'et-nope-scope').text()).toContain('unexpected')
  })

  it('①-7 ★★★★ 正控：tenant_admin / super_admin / admin_key 都进得了页面', async () => {
    for (const r of ['tenant_admin', 'super_admin', 'admin_key']) {
      authState.role = r
      const w = await mountView()
      expect(hasId(w, 'et-nope')).toBe(false)
      expect(hasId(w, 'et-load')).toBe(true)
    }
  })

  it('①-8 ★★★ 正控：tenant_admin 与 super_admin 的租户口径文案不同', async () => {
    // ★★★ 断言的是**替换后紧跟的那一段**（`：own。` / `：all。`），
    //   不是裸的 `own` / `all` —— 图例里本来就写着这两个词，
    //   用裸子串断言会**恒真**：批 113 变异 S4 当场把这一个顶了出来。
    authState.role = 'tenant_admin'
    const a = await mountView()
    expect(byId(a, 'et-scope-note').text()).toContain('：own。')
    expect(byId(a, 'et-scope-note').text()).not.toContain('：all。')
    authState.role = 'super_admin'
    const b = await mountView()
    expect(byId(b, 'et-scope-note').text()).toContain('：all。')
  })

  it('①-9 ★★★ 图例另起一条（否则口径断言会被图例里的字面量喂成恒真）', async () => {
    const w = await mountView()
    const note = byId(w, 'et-scope-note').text()
    const legend = byId(w, 'et-scope-legend').text()
    expect(legend).toContain('all')
    expect(legend).toContain('own')
    // ★★ 关键：note 里**不得**出现图例里的三档取值，否则断言又变成恒真
    expect(note).not.toContain('unexpected')
  })
})

// ── ② 参数守卫 ────────────────────────────────────────────────────────────

describe('② 参数守卫：越界必然 400，不值得发', () => {
  it('②-1 ★★★★ hours 只接受 1 / 24 / 168', () => {
    expect([...ERRORS_TREND_HOURS]).toEqual([1, 24, 168])
    for (const h of [1, 24, 168]) {
      expect(errorsTrendParamsAreAcceptable({ hours: h })).toBe(true)
    }
    for (const h of [0, 2, -1, 48, 720, 1.5, Number.NaN]) {
      expect(errorsTrendParamsAreAcceptable({ hours: h })).toBe(false)
    }
  })

  it('②-2 ★★★ granularity 只接受三档；空串 = 不显式发', () => {
    expect([...ERRORS_TREND_GRANULARITIES]).toEqual(['minute', 'hour', 'day'])
    expect(errorsTrendParamsAreAcceptable({ granularity: '' })).toBe(true)
    for (const g of ['minute', 'hour', 'day']) {
      expect(errorsTrendParamsAreAcceptable({ granularity: g })).toBe(true)
    }
    expect(errorsTrendParamsAreAcceptable({ granularity: 'week' })).toBe(false)
    expect(errorsTrendParamsAreAcceptable({ granularity: 'MINUTE' })).toBe(false)
  })

  it('②-3 ★★★★ credentialId 一旦给就必须是正整数（0 在后端只作内部哨兵）', () => {
    expect(errorsTrendParamsAreAcceptable({ credentialId: null })).toBe(true)
    expect(errorsTrendParamsAreAcceptable({ credentialId: undefined })).toBe(true)
    expect(errorsTrendParamsAreAcceptable({ credentialId: 7 })).toBe(true)
    expect(errorsTrendParamsAreAcceptable({ credentialId: 0 })).toBe(false)
    expect(errorsTrendParamsAreAcceptable({ credentialId: -1 })).toBe(false)
    expect(errorsTrendParamsAreAcceptable({ credentialId: 1.5 })).toBe(false)
  })

  it('②-4 ★★★ 页面把非法 credentialId 打成「参数不可用」', async () => {
    const w = await mountView()
    expect(hasId(w, 'et-params-bad')).toBe(false)
    await byId(w, 'et-credentialId').setValue('0')
    await flushPromises()
    expect(hasId(w, 'et-params-bad')).toBe(true)
    expect((byId(w, 'et-load').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('②-5 ★★★ 非数字 credentialId 同样被挡（页面不把它当 0）', async () => {
    const w = await mountView()
    await byId(w, 'et-credentialId').setValue('abc')
    await flushPromises()
    expect(hasId(w, 'et-params-bad')).toBe(true)
  })

  it('②-6 ★★★ 正控：合法 credentialId 可查询，且**原样**发给后端', async () => {
    const w = await mountView()
    await byId(w, 'et-credentialId').setValue('42')
    await flushPromises()
    expect(hasId(w, 'et-params-bad')).toBe(false)
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(mFetch).toHaveBeenCalledWith(expect.objectContaining({ credentialId: 42 }))
  })

  it('②-7 ★★★ 空 credentialId 不发这个参数（不过滤）', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(mFetch).toHaveBeenCalledWith(expect.objectContaining({ credentialId: null }))
  })
})

// ── ③ 三条可自验不变式（每条配正控） ──────────────────────────────────────

describe('③ 三条可自验不变式', () => {
  it('③-1 ★★★★ 正控：正常载荷三条都不报', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-summary-mismatch')).toBe(false)
    expect(hasId(w, 'et-unique-not-upper')).toBe(false)
    expect(hasId(w, 'et-not-ascending')).toBe(false)
  })

  it('③-2 ★★★★ 汇总与序列求和不一致 ⇒ 报', async () => {
    mFetch.mockResolvedValue(resp({ summary: { ...resp().summary, total_errors: 99 } }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-summary-mismatch')).toBe(true)
  })

  it('③-3 ★★★★ unique_requests 小于序列求和 ⇒ 报（上界被破坏）', async () => {
    mFetch.mockResolvedValue(resp({ summary: { ...resp().summary, unique_requests: 0 } }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-unique-not-upper')).toBe(true)
  })

  it('③-4 ★★★★ unique_requests **大于**序列求和 ⇒ 不报（桶求和本来就是上界）', () => {
    // ★★★ 这是 ③-3 的正控，方向必须反着取：跨桶重复只让上界**偏大**。
    const r = resp({ summary: { ...resp().summary, unique_requests: 999 } })
    expect(errorsTrendUniqueRequestsIsUpperBound(r)).toBe(true)
    expect(errorsTrendSummaryMatchesSeries(r)).toBe(true)
  })

  it('③-5 ★★★★ 序列非升序 ⇒ 报', async () => {
    mFetch.mockResolvedValue(resp({
      time_series: [
        point({ timestamp: '2026-10-14T02:00:00Z' }),
        point({ timestamp: '2026-10-14T01:00:00Z' }),
      ],
    }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-not-ascending')).toBe(true)
  })

  it('③-6 ★★★ 单点与空序列都算升序（边界不误报）', () => {
    expect(errorsTrendSeriesIsAscending(resp({ time_series: [] }))).toBe(true)
    expect(errorsTrendSeriesIsAscending(resp({ time_series: [point()] }))).toBe(true)
    // ★★ 时间戳相等的两个桶也是升序（`<` 不是 `<=`）
    expect(errorsTrendSeriesIsAscending(resp({ time_series: [point(), point()] }))).toBe(true)
  })

  it('③-7 ★★★ 正控：空序列时汇总恒等（0 === 0）不报', () => {
    const r = resp({ time_series: [] })
    expect(errorsTrendSummaryMatchesSeries(r)).toBe(true)
  })
})

// ── ④ source 的两档语义 ───────────────────────────────────────────────────

describe('④ source：fallback 不等于「没有错误」', () => {
  it('④-1 ★★★★ stats 源不报 fallback', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-s-source').text()).toBe('stats')
    expect(hasId(w, 'et-fallback')).toBe(false)
    expect(hasId(w, 'et-genuinely-empty')).toBe(false)
  })

  it('④-2 ★★★★ fallback + 非空 ⇒ 只报 fallback，**不**报「确实没错误」', async () => {
    mFetch.mockResolvedValue(resp({ source: 'fallback' }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-fallback')).toBe(true)
    expect(hasId(w, 'et-genuinely-empty')).toBe(false)
  })

  it('④-3 ★★★★ fallback + 空序列 ⇒ 两条都报（这才可以确定没错误）', async () => {
    mFetch.mockResolvedValue(resp({ source: 'fallback', time_series: [] }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-fallback')).toBe(true)
    expect(hasId(w, 'et-genuinely-empty')).toBe(true)
    expect(errorsTrendWindowIsGenuinelyEmpty(resp({ source: 'fallback', time_series: [] }))).toBe(true)
  })

  it('④-4 ★★★ 反向：stats + 空序列 **不能**判定「确实没错误」', () => {
    // ★★ 这就是「0 行是被文档化的失败模式」那一条的判据。
    const r = resp({ source: 'stats', time_series: [] })
    expect(errorsTrendPreAggregationMissed(r)).toBe(false)
    expect(errorsTrendWindowIsGenuinelyEmpty(r)).toBe(false)
  })

  it('④-5 ★★★ 页面把两个来源取值都列出来（可自解释）', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-source-set').text()).toContain('stats')
    expect(byId(w, 'et-source-set').text()).toContain('fallback')
  })
})

// ── ⑤ 零行是二义的 ────────────────────────────────────────────────────────

describe('⑤ 零行的二义', () => {
  it('⑤-1 ★★★★ stats + 空序列 ⇒ 报「零行不等于没错误」', async () => {
    mFetch.mockResolvedValue(resp({ source: 'stats', time_series: [] }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-zero-rows-ambiguous')).toBe(true)
    expect(hasId(w, 'et-series-empty')).toBe(true)
  })

  it('⑤-2 ★★★★ fallback + 空序列 ⇒ **不**报二义（明细确实也没有）', async () => {
    mFetch.mockResolvedValue(resp({ source: 'fallback', time_series: [] }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-zero-rows-ambiguous')).toBe(false)
  })

  it('⑤-3 ★★★ 正控：有序列时两条都不报', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-zero-rows-ambiguous')).toBe(false)
    expect(hasId(w, 'et-series-empty')).toBe(false)
  })
})

// ── ⑥ 空 map ⇒ 整个键省略 ─────────────────────────────────────────────────

describe('⑥ 空 map 被整体省略', () => {
  it('⑥-1 ★★★★ 缺键的桶显示「键被省略」而不是 {} 或 null', async () => {
    mFetch.mockResolvedValue(resp({ time_series: [point()] }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    const txt = byId(w, 'et-p-0-by_supplier').text()
    expect(txt).not.toContain('{}')
    expect(txt).not.toContain('null')
    expect(txt).toContain('省略')
  })

  it('⑥-2 ★★★★ 有键的桶逐条列出', async () => {
    mFetch.mockResolvedValue(resp({
      time_series: [point({ by_supplier: { openai: 3, glm: 1 }, by_error_type: { timeout: 4 } })],
    }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-p-0-by_supplier').text()).toContain('openai 3')
    expect(byId(w, 'et-p-0-by_supplier').text()).toContain('glm 1')
    expect(byId(w, 'et-p-0-by_error_type').text()).toContain('timeout 4')
  })

  it('⑥-3 ★★★★ 空对象也算「没有该维度」', () => {
    // ★ 后端不会给 `{}`，但客户端判定不能依赖这一点
    expect(errorsTrendPointHasSupplierDim({ ...point(), by_supplier: {} })).toBe(false)
    expect(errorsTrendPointHasSupplierDim(point())).toBe(false)
    expect(errorsTrendPointHasSupplierDim({ ...point(), by_supplier: { a: 1 } })).toBe(true)
    expect(errorsTrendPointHasErrorTypeDim({ ...point(), by_error_type: {} })).toBe(false)
    expect(errorsTrendPointHasErrorTypeDim({ ...point(), by_error_type: { x: 1 } })).toBe(true)
  })
})

// ── ⑦ 恒数组与截断 ────────────────────────────────────────────────────────

describe('⑦ top_* 是恒数组，且会被截断', () => {
  it('⑦-1 ★★★★ 空数组时页面明确说「为空」而不是「未加载」', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-te-empty')).toBe(true)
    expect(hasId(w, 'et-ts-empty')).toBe(true)
  })

  it('⑦-2 ★★★★ 满 10 条 ⇒ 报截断（被丢掉的数量未知）', async () => {
    const rows = Array.from({ length: ERRORS_TREND_BREAKDOWN_LIMIT }, (_, i) => ({ key: `k${i}`, count: i }))
    mFetch.mockResolvedValue(resp({
      summary: { ...resp().summary, top_error_types: rows, top_suppliers: rows },
    }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-breakdown-truncated')).toBe(true)
    expect(byId(w, 'et-te-k0-count').text()).toBe('0')
    expect(byId(w, 'et-ts-k9-count').text()).toBe('9')
  })

  it('⑦-3 ★★★★ 正控：9 条不报截断（边界是 10 不是 >10）', async () => {
    const rows = Array.from({ length: ERRORS_TREND_BREAKDOWN_LIMIT - 1 }, (_, i) => ({ key: `k${i}`, count: i }))
    mFetch.mockResolvedValue(resp({
      summary: { ...resp().summary, top_error_types: rows, top_suppliers: rows },
    }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-breakdown-truncated')).toBe(false)
    expect(hasId(w, 'et-te-empty')).toBe(false)
  })

  it('⑦-4 ★★ 只有一个榜单满 10 也报（判据是「或」，不是「且」）', async () => {
    const rows = Array.from({ length: ERRORS_TREND_BREAKDOWN_LIMIT }, (_, i) => ({ key: `k${i}`, count: i }))
    mFetch.mockResolvedValue(resp({
      summary: { ...resp().summary, top_error_types: rows, top_suppliers: [] },
    }))
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-breakdown-truncated')).toBe(true)
  })
})

// ── ⑧ 粒度回显 ────────────────────────────────────────────────────────────

describe('⑧ 粒度回显只在「没显式指定」时可核对', () => {
  it('⑧-1 ★★★★ 缺省推导：1→minute / 24→hour / 168→day', () => {
    expect(errorsTrendDefaultGranularity(1)).toBe('minute')
    expect(errorsTrendDefaultGranularity(24)).toBe('hour')
    expect(errorsTrendDefaultGranularity(168)).toBe('day')
    // ★ 三值枚举里没有别的，所以中间值不存在；但函数对 0/负数仍要有确定行为
    expect(errorsTrendDefaultGranularity(0)).toBe('minute')
    expect(errorsTrendDefaultGranularity(48)).toBe('day')
  })

  it('⑧-2 ★★★★ 没显式指定且回显 == 推导值 ⇒ 通过', () => {
    expect(errorsTrendGranularityIsDefault(resp({ hours: 24, granularity: 'hour' }), false)).toBe(true)
    expect(errorsTrendGranularityIsDefault(resp({ hours: 1, granularity: 'minute' }), false)).toBe(true)
    expect(errorsTrendGranularityIsDefault(resp({ hours: 168, granularity: 'day' }), false)).toBe(true)
    expect(errorsTrendGranularityEchoIsConsistent(resp({ hours: 24, granularity: 'hour' }), false)).toBe(true)
  })

  it('⑧-3 ★★★★ 没显式指定但回显不符 ⇒ 报', async () => {
    mFetch.mockResolvedValue(resp({ hours: 24, granularity: 'day' }))
    expect(errorsTrendGranularityEchoIsConsistent(resp({ hours: 24, granularity: 'day' }), false)).toBe(false)
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-gran-echo-bad')).toBe(true)
  })

  it('⑧-4 ★★★★★ 显式指定时这条判定**真空** ⇒ 页面不得因此报错', async () => {
    // ★★★ 这是「恒真判据不提供」的那条配套正控：显式指定后端不再推导，
    //   回显什么都是对的。**注意方向**：`errorsTrendGranularityIsDefault`
    //   在这里答 false 是**正确**的（它问的是「这是不是缺省推导值」），
    //   而 `…EchoIsConsistent` 答 true —— 两个函数方向相反，混用会造出假告警。
    const r = resp({ hours: 24, granularity: 'day' })
    expect(errorsTrendGranularityIsDefault(r, true)).toBe(false)
    expect(errorsTrendGranularityEchoIsConsistent(r, true)).toBe(true)
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await byId(w, 'et-gran-day').trigger('click')
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'et-gran-echo-bad')).toBe(false)
  })

  it('⑧-5 ★★★ 自动档的按钮上直接写出将要推导出的粒度', async () => {
    const w = await mountView()
    expect(byId(w, 'et-gran-auto').text()).toContain('hour')
    await byId(w, 'et-hours-168').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-gran-auto').text()).toContain('day')
    await byId(w, 'et-hours-1').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-gran-auto').text()).toContain('minute')
  })
})

// ── ⑨ 已生效过滤条件据实回显 ──────────────────────────────────────────────

describe('⑨ 已生效过滤条件', () => {
  it('⑨-1 ★★★★ 空串与 all 都算「不过滤」', () => {
    expect(errorsTrendFilterIsWildcard('')).toBe(true)
    expect(errorsTrendFilterIsWildcard('all')).toBe(true)
    expect(errorsTrendFilterIsWildcard('openai')).toBe(false)
    expect(errorsTrendFilterIsWildcard('ALL')).toBe(false)
  })

  it('⑨-2 ★★★★ 页面对空串 / all 显示不过滤，对普通值原样显示', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-applied-supplier').text()).toContain('不过滤')
    await byId(w, 'et-supplier').setValue('all')
    await flushPromises()
    expect(byId(w, 'et-applied-supplier').text()).toContain('不过滤')
    await byId(w, 'et-supplier').setValue('openai')
    await flushPromises()
    expect(byId(w, 'et-applied-supplier').text()).toBe('openai')
  })

  it('⑨-3 ★★★ credentialId 空 ⇒ 不过滤；填了 ⇒ 原样', async () => {
    const w = await mountView()
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'et-applied-credentialId').text()).toContain('不过滤')
    await byId(w, 'et-credentialId').setValue('9')
    await flushPromises()
    expect(byId(w, 'et-applied-credentialId').text()).toBe('9')
  })

  it('⑨-4 ★★★ 空串与 all 都要**照发**给后端（由后端判定，不是前端吞掉）', async () => {
    const w = await mountView()
    await byId(w, 'et-supplier').setValue('all')
    await byId(w, 'et-errorType').setValue('')
    await byId(w, 'et-load').trigger('click')
    await flushPromises()
    // ★ `''` 也发：api 层用 `!= null` 判空，空串是有意义的通配值
    expect(mFetch).toHaveBeenCalledWith(
      expect.objectContaining({ supplier: 'all', errorType: '' }),
    )
  })
})

// ── ⑩ 触控 ────────────────────────────────────────────────────────────────

describe('⑩ 触控目标 ≥48 CSS px', () => {
  it('⑩-1 ★★★ 解析出 min-height 数值再比大小（正则 [1-4]?\\d 会吃掉两位数）', () => {
    const src = viewSrc()
    const mins = [...src.matchAll(/min-height:\s*(\d+)px/g)].map((m) => Number(m[1]))
    expect(mins.length).toBeGreaterThan(0)
    for (const v of mins) expect(v).toBeGreaterThanOrEqual(48)
    // ★ chip 还要有 min-width（横向排布的一排小按钮）
    const widths = [...src.matchAll(/min-width:\s*(\d+)px/g)].map((m) => Number(m[1]))
    expect(widths.length).toBeGreaterThan(0)
    for (const v of widths) expect(v).toBeGreaterThanOrEqual(48)
  })

  it('⑩-2 ★★ 三个 hours chip 与四个粒度 chip 都真的渲染了', async () => {
    const w = await mountView()
    for (const h of ERRORS_TREND_HOURS) expect(hasId(w, `et-hours-${h}`)).toBe(true)
    expect(hasId(w, 'et-gran-auto')).toBe(true)
    for (const g of ERRORS_TREND_GRANULARITIES) expect(hasId(w, `et-gran-${g}`)).toBe(true)
  })
})

// ── ⑪ 后端推导链的结构层钉法 ─────────────────────────────────────────────

describe('⑪ 后端租户推导链（读 Go 源码）', () => {
  // ★★★ 路径写错时**必须硬失败**，不能悄悄跳过后让这条判据变成恒真。
  const ROOT = join(process.cwd(), '..')
  function goSrc(rel: string): string {
    const p = join(ROOT, rel)
    expect(existsSync(p), `找不到 ${rel}（判据路径写错会让本组全部空转）`).toBe(true)
    return readFileSync(p, 'utf8')
  }

  it('⑪-1 ★★★★★ AdminMiddleware 确实不检查 role（这条是整批的根）', () => {
    const auth = goSrc('admin/auth.go')
    const fn = auth.slice(auth.indexOf('func AdminMiddleware'))
    // 取到下一个顶层 func 为止
    const body = fn.slice(0, fn.indexOf('\nfunc '))
    expect(body).toContain('Role:               p.Role')
    // ★★★ 关键否定断言：这段函数体里**不得**出现 role 的比较判断。
    //   （若有人后来加了 role 门，这条会红 ⇒ 那是后端修好了，不是判据坏了。）
    expect(body).not.toMatch(/Role\s*==|Role\s*!=/)
    expect(body).not.toContain('requireUserManager')
    expect(body).not.toContain('RequireSuperAdmin')
  })

  it('⑪-2 ★★★★★ EffectiveTenantIDAll 对非 tenant_admin 返回空串', () => {
    const ctx = goSrc('admin/context.go')
    const fn = ctx.slice(ctx.indexOf('func EffectiveTenantIDAll'))
    const body = fn.slice(0, fn.indexOf('\n}'))
    expect(body).toContain('IsTenantAdmin(r)')
    expect(body).toContain('return ""')
    // ★★ 反向：EffectiveTenantID（非 All）返回的是 "default"，不是空串 —— 两者不可混用
    const fn2 = ctx.slice(ctx.indexOf('func EffectiveTenantID('))
    expect(fn2.slice(0, fn2.indexOf('\n}'))).toContain('return "default"')
  })

  it('⑪-3 ★★★★ getErrorsTrend 自己也不做角色检查', () => {
    const src = goSrc('admin/errors_trend.go')
    const fn = src.slice(src.indexOf('func (h *errorsTrendHandlers) getErrorsTrend'))
    const body = fn.slice(0, fn.indexOf('\n}\n\n'))
    expect(body).toContain('EffectiveTenantIDAll(r)')
    // ★ 入口只查这四样
    expect(body).toContain('h.db == nil')
    expect(body).toContain('parseVendorErrorHours')
    expect(body).toContain('invalid_granularity')
    expect(body).toContain('invalid_credential_id')
    // ★★★ 不得出现角色判断
    expect(body).not.toMatch(/Role\s*==|IsTenantAdmin|RequireSuperAdmin/)
  })

  it('⑪-4 ★★★★ 三条 SQL 的租户条件都是「空串 = 全租户」', () => {
    const src = goSrc('admin/errors_trend.go')
    // stats 腿借 credentials 子查询（因为 supplier_error_stats 没有 tenant_id 列）
    expect(src).toContain("AND ($7 = '' OR credential_id IN (SELECT id FROM credentials WHERE tenant_id = $7))")
    // 明细腿直接用 unified 的 tenant_id
    expect(src).toMatch(/supplier_errors_unified[\s\S]{0,400}tenant_id/)
  })

  it('⑪-5 ★★★★ resolveSharedPrincipal 允许 user 且只在本地更低时下调', () => {
    const sp = goSrc('admin/shared_principal.go')
    expect(sp).toContain('"user": 1')
    expect(sp).toContain('if roleRank[localRole] < roleRank[verified.Role]')
    // ★★ 这是「effective role=user 可达」的唯一依据
    expect(sp).toMatch(/if roleRank\[localRole\] < roleRank\[verified\.Role\] \{\s*resolved\.Role = localRole/)
  })

  it('⑪-6 ★★ 注册点只有一处，且是 admin(...) 包装', () => {
    const h = goSrc('admin/handler.go')
    const hits = h.split('\n').filter((l) => l.includes('"/api/errors/trend"'))
    expect(hits.length).toBe(1)
    expect(hits[0]).toContain('admin(h.errorsTrendHandlers.getErrorsTrend)')
    expect(hits[0]).not.toContain('superAdmin')
  })
})
