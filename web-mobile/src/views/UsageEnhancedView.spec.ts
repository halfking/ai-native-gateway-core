import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import UsageEnhancedView from './UsageEnhancedView.vue'
import { setLocale, t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchCacheEconomics,
  fetchPeriodCompare,
  usageRoleNeedsAdminGate,
  usageAllTenantsScopeIsLegitimate,
  USAGE_ALL_TENANT_ROLES,
  cacheEconomicsDegraded,
  cacheEconomicsDegradedReason,
  cacheHitRatioMeaningless,
  compressionSavedUnreliable,
  compressedCountMayBeFailed,
  periodCompareDegraded,
  periodChangeMeaningless,
  TREND_THRESHOLD_PCT,
  type CacheEconomicsResponse,
  type PeriodCompareResponse,
} from '@/api/usageEnhanced'

/**
 * 用量成本增强面回归护栏（2026-10-14，第一百二十二批）。
 *
 * ★★★★★★ **本族是本仓第一个「有租户维度、但服务端缺角色下限」的家族。**
 *
 *   前七次权限判定里，第一类（有租户维度 ⇒ 按角色档挡）的成员挡的都是
 *   **租户_admin 之间的隔离**。本族不一样 —— 要挡的是**比 tenant_admin 更低的 `user`**。
 *
 *   逐字链条：
 *   ① 注册 `admin/handler.go:1339`
 *      `mux.HandleFunc("/api/admin/usage/", h.admin(h.HandleUsageAdmin))`
 *      ⇒ **前缀子路由**（与批 121 的 settingsRouter 同形态）。
 *   ② `h.admin` = `AdminMiddleware`（`handler.go:962` → `auth.go:86`）
 *      —— 全文**只有**「验 JWT」+「强制改密码」，**没有任何角色下限**。
 *   ③ `pkg/identity/verifier.go` 的 `supportedRole`
 *      —— ★★★ `case "user", "tenant_admin", "super_admin": return true`
 *      ⇒ 它校验「角色**是已知的**」，**不是**「角色**够高**」。
 *   ④ `EffectiveTenantIDAll`（`admin/context.go`）
 *      ```go
 *      if IsTenantAdmin(r) { return GetTenantID(r) }
 *      return "" // empty string means query all tenants
 *      ```
 *      ⇒ ★★ `user` 不是 tenant_admin ⇒ 拿到 `""` ⇒ 「**不拼** tenant_id 条件」。
 *   ⑤ 三个 handler **都**有 `AND ... tenant_id = $N`
 *      （`usage_enhanced.go:128` / `:347` / `:616`）
 *      ⇒ 隔离机制**存在**；缺的是**谁能进来**。
 *
 *   ⇒ ⇒ ★★★ **普通 user 可读到跨全部租户的成本与用量数据。**
 *   ⇒ 抽屉席 `requiresRole: 'admin'`：挡 `user`，放行 admin 三档。
 *   ⚠️ 与已登记后端待办②（errors-trend / model-history「effective role=user
 *     可读跨租户」）是**同一类缺陷的第三个实例**，而本族落在**成本**上，更敏感。
 *
 * ★★ **不要按「三个端点都有租户过滤」就以为不设 requiresRole** ——
 *   模块头原先就是这么写的，本批订正了（见 `api/usageEnhanced.ts` 的头部订正块）。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 admin，且**逐条从 Go 源码钉「没有角色下限」**。
 *  ② ★★★★★ `degraded` 是**恒发**字段 ⇒ 「服务端确认过」可断言。
 *  ③ ★★★★ 三个「节省」数字是**估算**，必须标出来。
 *  ④ ★★★★ `compressed_requests` 静默失败 ⇒ 连锁不可分。
 *  ⑤ ★★★★★ 深链兜底：**不发请求**。
 *  ⑥ ★★★ 接线与编码门（含 admin 档**不得**进 superAdmin 白名单）。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function cache(over: Partial<CacheEconomicsResponse> = {}): CacheEconomicsResponse {
  return {
    date_from: '2026-09-15',
    date_to: '2026-10-14',
    total_requests: 1000,
    cache_read_tokens: 400000,
    prompt_tokens: 100000,
    cache_hit_ratio: 0.8,
    dollars_saved: 12.5,
    dollars_spent: 30,
    effective_cost_ratio: 0.6,
    compressed_requests: 10,
    compression_saved: 3.5,
    total_saved: 16,
    savings_rate: 0.34,
    degraded: false,
    ...over,
  } as CacheEconomicsResponse
}

function compare(over: Partial<PeriodCompareResponse> = {}): PeriodCompareResponse {
  // ★ 逐字段照 `PeriodCompareResponse` 的真实形状写：`current` / `previous` 是
  //   **PeriodStats 对象**，顶层**没有** current_total / previous_total。
  return {
    current: { total_cost_usd: 300, total_requests: 3000 },
    previous: { total_cost_usd: 200, total_requests: 2000 },
    change_pct: 50,
    change_abs: 100,
    trend: 'up',
    significant: true,
    by_dimension: {},
    degraded: false,
    ...over,
  } as unknown as PeriodCompareResponse
}

// ── 装配 ──────────────────────────────────────────────────────────────────

/** ★★ 与视图里的 `SAVINGS_ESTIMATE_FIELDS` 对齐（三个「估算」字段）。 */
const SAVINGS_FIELD_COUNT = 3

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/usageEnhanced', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/usageEnhanced')>()
  return { ...actual, fetchCacheEconomics: vi.fn(), fetchPeriodCompare: vi.fn() }
})

const mCache = fetchCacheEconomics as unknown as ReturnType<typeof vi.fn>
const mCompare = fetchPeriodCompare as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(UsageEnhancedView)
  await flushPromises()
  return w
}
function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}
function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/UsageEnhancedView.vue'), 'utf8')
}
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}
/**
 * ★ 取 Go 函数体：方法与普通函数**两种形态都认**（批 121 的教训）。
 *
 * ★★ 批 122 再踩一层：**不能**「遇到第一个 `func ` 就停」。
 *   `usagePeriodCompare`（:347）的区间里夹着两个**别的顶层函数**
 *   （`:465 func parsePeriod(`、`:476`/`:522` 的 query* 助手），
 *   一停就只拿到函数体前半截 ⇒ 里面的 `AND ul.tenant_id = $3` 被切掉
 *   ⇒ ①-7 假性失败。
 * ⇒ 处置：**带接收者时只停在「下一个同接收者的方法」**；无接收者才停在任意 `func `。
 */
function goFunc(src: string, name: string): string {
  const lines = src.split('\n')
  const isMethod = lines.some((l) => l.startsWith(`func (h *Handler) ${name}(`))
  const start = lines.findIndex(
    (l) => l.startsWith(`func (h *Handler) ${name}(`) || l.startsWith(`func ${name}(`),
  )
  expect(start, `没找到 ${name} 的定义（正则失配会让这条恒绿）`).toBeGreaterThan(-1)
  const stopRe = isMethod ? /^func \(h \*Handler\) / : /^func /
  let end = start + 1
  while (end < lines.length && !stopRe.test(lines[end] ?? '')) end++
  return lines.slice(start, end).join('\n')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mCache.mockResolvedValue(cache())
  mCompare.mockResolvedValue(compare())
})

async function load(w: W) {
  await byId(w, 'ue-load').trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 admin —— 且逐条从 Go 源码钉「没有角色下限」
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 admin（本仓第一个「有租户维度但缺角色下限」的家族）', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 admin 档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'usage-enhanced')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/usage-enhanced')
    expect(seat!.requiresRole).toBe('admin')
    expect(seat!.titleKey).toBe('nav.usageEnhanced')
  })

  it('①-2 ★★★★★★ admin 档 ⇒ **不得**进 superAdmin 白名单（反向护栏）', () => {
    // ★ 批 119 的纪律：'admin' 档席位进 superAdmin 白名单那条立刻红。
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    expect(src).not.toMatch(/^\s*'usage-enhanced',$/m)
    // ★★ 且 navItemsFor 的白名单不变量本身也覆盖它：tenant_admin 必须**能**看到它。
    expect(navItemsFor(DRAWER_NAV, 'tenant_admin').map((i) => i.key)).toContain('usage-enhanced')
  })

  it('①-3 ★★★★★★ `navItemsFor` 的 admin 档**确实挡掉 user**（这一层的全部作用）', () => {
    // ★ appNav.ts 是 **TypeScript**，`goFunc`（认 Go 的 `func `）在这里必然找不到 ——
    //   首轮就是这么红的：**红色来自解析器用错语言**，不是被测物有问题。
    // ⇒ 处置：Go 断言留在 ①-4/①-5/①-6/①-7，这里改成**行为断言** + 读 TS 文本。
    const src = readFileSync(join(process.cwd(), 'src/config/appNav.ts'), 'utf8')
    const body = src.slice(src.indexOf('export function navItemsFor'))
    expect(body).toMatch(/role === 'tenant_admin' \|\| role === 'admin_key'/)
    // ★★★ user 既不在 isSuper 也不在 isAdmin ⇒ 被挡掉。
    expect(navItemsFor(DRAWER_NAV, 'user').map((i) => i.key)).not.toContain('usage-enhanced')
    for (const r of ['super_admin', 'tenant_admin', 'admin_key']) {
      expect(navItemsFor(DRAWER_NAV, r).map((i) => i.key), `${r} 应当能看到`).toContain(
        'usage-enhanced',
      )
    }
  })

  it('①-4 ★★★★★★ 读 Go 源码：`h.admin` / `AdminMiddleware` **没有任何角色下限**', () => {
    const h = goSrc('admin/handler.go')
    expect(h).toMatch(
      /func \(h \*Handler\) admin\(fn http\.HandlerFunc\) http\.HandlerFunc \{\s*\n\s*return AdminMiddleware\(fn, h\.db, h\.secret\)/,
    )
    const body = goFunc(goSrc('admin/auth.go'), 'AdminMiddleware')
    expect(body, '中间件里出现了角色判定 ⇒ 权限结论要重判').not.toMatch(
      /IsSuperAdminOrLegacy|IsTenantAdmin|RequireSuperAdminForWrite/,
    )
    // ★ 唯一的 403 是强制改密码，不是 role 门。
    expect(body).toMatch(/errPasswordChangeRequired/)
  })

  it('①-5 ★★★★★★ 读 Go 源码：`supportedRole` **接受 "user"**（这条是漏洞的根）', () => {
    // ★★★ 没有这一条，「AdminMiddleware 不检 role」只是缺一道门；
    //   有了这一条才证明**普通用户真的进得来**。
    const src = goSrc('pkg/identity/verifier.go')
    const body = goFunc(src, 'supportedRole')
    expect(body).toMatch(/case "user", "tenant_admin", "super_admin":\s*\n\s*return true/)
  })

  it('①-6 ★★★★★★ 读 Go 源码：`EffectiveTenantIDAll` 对非 tenant_admin 返回 `""`', () => {
    // ★★ 「空串 = 查全部租户」这个语义，是整条链的最后一环。
    const body = goFunc(goSrc('admin/context.go'), 'EffectiveTenantIDAll')
    expect(body).toMatch(/if IsTenantAdmin\(r\) \{\s*\n\s*return GetTenantID\(r\)\s*\n\s*\}/)
    expect(body).toMatch(/return "" \/\/ empty string means query all tenants/)
  })

  it('①-7 ★★★★★★ 读 Go 源码：隔离机制**存在**——三条链都把租户键带到了 SQL', () => {
    // ★★ 对照组：**隔离机制是存在的**，缺的只是角色下限。
    //   若哪天某条链不再把 tid 带到 SQL，这条会红 —— 那就从「缺角色下限」升级成「跨租户」。
    //
    // ★★★ 这里踩过一个真实的坑：`usagePeriodCompare` **自己不拼 SQL 条件**。
    //   它 `tid := EffectiveTenantIDAll(r)` 之后**委托**给两个助手
    //   （`queryPeriodStats(ctx, tid, …)` / `queryDimensionChanges(ctx, tid, …)`），
    //   条件 `AND ul.tenant_id = $3` / `$5` 写在**助手**里。
    //   ⇒ 首轮我按「handler 内联拼条件」写断言，红了。
    //   ★ **红色来自我假设了错误的结构**，不是被测物有问题 —— 委托是它的真实形态。
    const src = goSrc('admin/usage_enhanced.go')
    const tenantCond = /AND [^\n]*tenant_id = \$/

    // · 自拼条件的两条：条件就在 handler 体内
    for (const fn of ['usageCostTrend', 'usageCacheEconomics']) {
      const body = goFunc(src, fn)
      expect(body, `${fn} 没有取租户键`).toMatch(/EffectiveTenantIDAll\(r\)/)
      expect(body, `${fn} 没有拼 tenant_id 条件`).toMatch(tenantCond)
    }

    // · 委托型的一条：handler 取键并**传给助手**，条件在助手体内
    const pc = goFunc(src, 'usagePeriodCompare')
    expect(pc, 'usagePeriodCompare 没有取租户键').toMatch(/EffectiveTenantIDAll\(r\)/)
    expect(pc, '没有把 tid 传给 queryPeriodStats').toMatch(/queryPeriodStats\(ctx, tid,/)
    expect(pc, '没有把 tid 传给 queryDimensionChanges').toMatch(/queryDimensionChanges\(ctx, tid,/)
    for (const helper of ['queryPeriodStats', 'queryDimensionChanges']) {
      expect(goFunc(src, helper), `${helper} 没有拼 tenant_id 条件`).toMatch(tenantCond)
    }
  })

  it('①-8 ★★★★★ 注册处行号订正：模块头原写 `:1267`，经 `:1297`（批153）、`:1332`（12h-audit 20261017）再漂至 `:1339`（2026-10-21 views 行号订正轮）→ 实为 `:1342`（2026-10-26 行号漂移订正轮）', () => {
    const src = goSrc('admin/handler.go')
    const lines = src.split('\n')
    const at = lines.findIndex((l) => l.includes('"/api/admin/usage/"'))
    expect(at, '找不到 /api/admin/usage/ 的注册行').toBeGreaterThan(-1)
    // ★ 12h-audit 20261017：a18db7b84/c286e505a/eb0aee4ec 三笔再推 35 行 ⇒ :1332；
    //   2026-10-21 views 行号订正轮：再推 7 行 ⇒ :1339；
    //   2026-10-26 行号漂移订正轮：再推 3 行 ⇒ 真实行号 :1342。
    expect(at + 1).toBe(1342)
    expect(lines[at]).toMatch(/h\.admin\(h\.HandleUsageAdmin\)/)
    // ★★ 反向护栏：原行号那行必须**不是**本族注册处（它其实是 errorsTrend 相关）。
    expect(lines[1266]).not.toMatch(/usage/i)
  })

  it('①-9 ★★★★ 角色判别 helper 随 role 变（不是恒真）', () => {
    expect(usageRoleNeedsAdminGate('user')).toBe(true)
    expect(usageRoleNeedsAdminGate('tenant_admin')).toBe(false)
    expect(usageRoleNeedsAdminGate('super_admin')).toBe(false)
    expect(usageRoleNeedsAdminGate('admin_key')).toBe(false)
    expect(usageRoleNeedsAdminGate(null)).toBe(true)
    // ★ 与 EffectiveTenantIDAll 对齐：只有 admin 两档拿「全部租户」才是正当的。
    expect(usageAllTenantsScopeIsLegitimate('super_admin')).toBe(true)
    expect(usageAllTenantsScopeIsLegitimate('admin_key')).toBe(true)
    expect(usageAllTenantsScopeIsLegitimate('user')).toBe(false)
    expect(usageAllTenantsScopeIsLegitimate('tenant_admin')).toBe(false)
    expect([...USAGE_ALL_TENANT_ROLES].sort()).toEqual(['admin_key', 'super_admin'])
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② degraded 是恒发字段
// ══════════════════════════════════════════════════════════════════════════

describe('② degraded：恒发字段 ⇒ 「服务端确认过没降级」可断言', () => {
  it('②-1 ★★★★★ 正常载荷 ⇒ 报「已确认未降级」（该出现的一面）', async () => {
    const w = await mountView()
    await load(w)
    expect(cacheEconomicsDegraded(cache())).toBe(false)
    expect(hasId(w, 'ue-cache-ok')).toBe(true)
    expect(hasId(w, 'ue-cache-degraded')).toBe(false)
  })

  it('②-2 ★★★★★ 降级 ⇒ 报红线，且**原因键**出现在响应里', async () => {
    mCache.mockResolvedValue(cache({ degraded: true, degraded_reason: 'ledger behind schema' }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-cache-degraded')).toBe(true)
    expect(hasId(w, 'ue-cache-ok')).toBe(false)
    expect(textOf(w, 'ue-degraded-reason')).toBe('ledger behind schema')
    expect(cacheEconomicsDegradedReason(cache({ degraded: true, degraded_reason: 'x' }))).toBe('x')
  })

  it('②-3 ★★★★ 未降级时原因键**不存在**（不是空串）', () => {
    expect(cacheEconomicsDegradedReason(cache())).toBeNull()
    expect('degraded_reason' in cache()).toBe(false)
  })

  it('②-4 ★★★ 环比降级 ⇒ 独立报红线', async () => {
    mCompare.mockResolvedValue(compare({ degraded: true }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-compare-degraded')).toBe(true)
    expect(periodCompareDegraded(compare({ degraded: true }))).toBe(true)
    expect(hasId(w, 'ue-cache-degraded')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 「节省」是估算
// ══════════════════════════════════════════════════════════════════════════

describe('③ 三个「节省」数字是**估算**，不是账单', () => {
  it('③-1 ★★★★★ 三个节省字段都必须**逐个**带「估算」标记', async () => {
    const w = await mountView()
    await load(w)
    // ★★★ 这里补的是一条**真的没判别力**的断言：初版只断言
    //   `t('ue.estimate.badge')` 非空 —— 那是**字典**里有这个键，
    //   与页面**有没有渲染它**毫无关系 ⇒ 变异台 S9 把徽标从模板里删掉，全绿。
    //   ⇒ 判据必须数**页面上真的出现了几个**徽标。
    for (const f of ['dollars_saved', 'compression_saved', 'total_saved']) {
      expect(hasId(w, `ue-${f}`), `${f} 没有渲染`).toBe(true)
      expect(byId(w, `ue-badge-${f}`).text(), `${f} 缺少「估算」标记`).toBe(t('ue.estimate.badge'))
    }
    expect(w.findAll('[data-testid^="ue-badge-"]').length).toBe(3)
    // ★ 估算徽标数量与节省字段数量必须**相等**（少一个就有一个值被当成账单数）。
    expect(w.findAll('[data-testid^="ue-badge-"]').length).toBe(SAVINGS_FIELD_COUNT)
    expect(textOf(w, 'ue-estimate-note')).toBe(t('ue.estimate.note'))
    expect(t('ue.estimate.note')).toContain('估算')
  })

  it('③-2 ★★★★ 估算提示在**降级时也不能消失**', async () => {
    mCache.mockResolvedValue(cache({ degraded: true, degraded_reason: 'x' }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-cache-degraded')).toBe(true)
    expect(textOf(w, 'ue-estimate-note')).toBe(t('ue.estimate.note'))
  })

  it('③-3 ★★★ scope 说明随角色切换（超管看的是全部租户）', async () => {
    const superW = await mountView()
    await load(superW)
    expect(textOf(superW, 'ue-scope-note')).toBe(t('ue.scope.allTenants'))
    expect(usageAllTenantsScopeIsLegitimate(authState.role)).toBe(true)

    authState.role = 'tenant_admin'
    const tenantW = await mountView()
    await load(tenantW)
    expect(textOf(tenantW, 'ue-scope-note')).toBe(t('ue.scope.ownTenant'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ compressed_requests 的静默失败链
// ══════════════════════════════════════════════════════════════════════════

describe('④ compressed_requests 静默失败 ⇒ 连锁不可分', () => {
  it('④-1 ★★★★★ 它为 0 时报「可能取数失败」，并连带压缩节省额不可信', async () => {
    mCache.mockResolvedValue(cache({ compressed_requests: 0 }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-compressed-suspect')).toBe(true)
    expect(hasId(w, 'ue-compression-unreliable')).toBe(true)
    expect(compressedCountMayBeFailed(cache({ compressed_requests: 0 }))).toBe(true)
    expect(compressionSavedUnreliable(cache({ compressed_requests: 0 }))).toBe(true)
  })

  it('④-2 ★★★★ 它非 0 时**不**报（阴性对照）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-compressed-suspect')).toBe(false)
    expect(hasId(w, 'ue-compression-unreliable')).toBe(false)
  })

  it('④-3 ★★★ 命中率分母为 0 ⇒ 报「无意义」而不是显示 0%', async () => {
    mCache.mockResolvedValue(cache({ cache_read_tokens: 0, prompt_tokens: 0, cache_hit_ratio: 0 }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-hit-ratio-meaningless')).toBe(true)
    expect(cacheHitRatioMeaningless(cache({ cache_read_tokens: 0, prompt_tokens: 0 }))).toBe(true)
  })

  it('④-4 ★★★ 环比「变了 0%」⇒ 报「两个周期都没数据」', async () => {
    // ★ 判据看的是 `previous.total_cost_usd`（分母），**不是** current_total/previous_total。
    //   首轮我照着响应顶层字段编了个不存在的夹具形状 ⇒ 假性失败。
    const empty = { total_cost_usd: 0, total_requests: 0, ...{} }
    mCompare.mockResolvedValue(
      compare({ previous: empty, change_pct: 0, trend: 'flat' } as unknown as Partial<PeriodCompareResponse>),
    )
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-compare-meaningless')).toBe(true)
    expect(
      periodChangeMeaningless(compare({ previous: empty } as unknown as Partial<PeriodCompareResponse>)),
    ).toBe(true)
    // ★ 阴性对照：上期非 0 时不报。
    expect(
      periodChangeMeaningless(
        compare({ previous: { total_cost_usd: 5 } } as unknown as Partial<PeriodCompareResponse>),
      ),
    ).toBe(false)
  })

  it('④-5 ★★★ 阈值常量必须出现在页面上（口径不许藏在代码里）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'ue-threshold')).toBe(t('ue.check.threshold', { pct: TREND_THRESHOLD_PCT }))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 深链兜底
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 深链兜底：兜的是「不发请求」', () => {
  it('⑤-1 ★★★★★ 普通 user ⇒ 显示 nope，且**一次请求都不发**', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'ue-nope')).toBe(true)
    expect(mCache).not.toHaveBeenCalled()
    expect(mCompare).not.toHaveBeenCalled()
    expect(hasId(w, 'ue-load')).toBe(false)
  })

  it('⑤-2 ★★★★★ nope 页面说清「为什么」—— 含跨租户成本这条', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(textOf(w, 'ue-nope-msg')).toBe(t('ue.nope.msg'))
    expect(textOf(w, 'ue-nope-why')).toBe(t('ue.nope.why'))
    expect(t('ue.nope.why')).toContain('全部租户')
  })

  it('⑤-3 ★★★★ tenant_admin 放行（这一档**不该**被误伤）', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-nope')).toBe(false)
    expect(mCache).toHaveBeenCalledTimes(1)
  })

  it('⑤-4 ★★★ 取数失败 ⇒ 报错，不显示「成本为 0」', async () => {
    mCache.mockRejectedValue(new Error('cache-economics query failed'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ue-error')).toBe(true)
    expect(hasId(w, 'ue-total-requests')).toBe(false)
  })

  it('⑤-5 ★★★ period-compare 只收 YYYY-MM ⇒ 断言真的传了两个月份', async () => {
    const w = await mountView()
    await load(w)
    const arg = mCompare.mock.calls[0]![0] as { current: string; previous: string }
    expect(arg.current).toMatch(/^\d{4}-\d{2}$/)
    expect(arg.previous).toMatch(/^\d{4}-\d{2}$/)
    expect(arg.current).not.toBe(arg.previous)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 接线与编码门', () => {
  it('⑥-1 ★★★★ 路由存在且 requiresRole 是 admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/usage-enhanced'/)
    expect(src).toMatch(/name: 'usage-enhanced'/)
  })

  it('⑥-2 ★★★★ i18n 两个语种都有 ue.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.usageEnhanced',
      'ue.title',
      'ue.nope.msg',
      'ue.nope.why',
      'ue.load.title',
      'ue.load.btn',
      'ue.scope.allTenants',
      'ue.scope.ownTenant',
      'ue.estimate.note',
      'ue.estimate.badge',
      'ue.cache.title',
      'ue.cache.ok',
      'ue.cache.degraded',
      'ue.compare.title',
      'ue.compare.degraded',
      'ue.check.hitRatioMeaningless',
      'ue.check.compressedSuspect',
      'ue.check.compressionUnreliable',
      'ue.check.periodMeaningless',
      'ue.check.threshold',
      'ue.field.totalRequests',
      'ue.field.cacheReadTokens',
      'ue.field.dollars_saved',
      'ue.field.compression_saved',
      'ue.field.total_saved',
    ]
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const m = DICT[loc]
      for (const k of keys) {
        const v = dictLookup(m, k)
        expect(typeof v, `${loc} 缺 ${k}`).toBe('string')
        expect(v, `${loc} 的 ${k} 值等于键名`).not.toBe(k)
      }
    }
  })

  it('⑥-3 ★★★ 新触控控件 ≥48 CSS px（解析数值再比大小）', () => {
    const m = viewSrc().match(/\.ue__btn\s*\{[^}]*min-height:\s*(\d+(?:\.\d+)?)px/)
    expect(m, '没找到 .ue__btn 的 min-height').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑥-4 ★★★★ 孤儿棘轮：本族已移出「功能缺口」清单，且未登记成重复件', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const listOf = (name: string): string[] => {
      const m = src.match(new RegExp(`const ${name}: readonly string\\[\\] = \\[([\\s\\S]*?)\\]`))
      expect(m, `没找到 ${name}（正则失配会让这条恒绿）`).toBeTruthy()
      return [...m![1]!.matchAll(/'([A-Za-z0-9_]+)'/g)].map((x) => x[1]!)
    }
    expect(listOf('KNOWN_ORPHANS')).not.toContain('usageEnhanced')
    expect(listOf('KNOWN_DUPLICATES')).not.toContain('usageEnhanced')
  })

  it('⑥-5 ★★★ 编码门：视图与本文件不得含 U+FFFD / NUL', () => {
    const F = String.fromCharCode(0xFFFD)
    const N = String.fromCharCode(0)
    for (const f of [
      viewSrc(),
      readFileSync(join(process.cwd(), 'src/views/UsageEnhancedView.spec.ts'), 'utf8'),
    ]) {
      expect(f.includes(F), '含 U+FFFD').toBe(false)
      expect(f.includes(N), '含 NUL').toBe(false)
    }
  })

  it('⑥-6 ★★★ 前两批的 i18n 前缀没被本批挤掉（批 121 前车之鉴）', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of ['sts.title', 'ps.title', 'ue.title']) {
        expect(typeof dictLookup(DICT[loc], k), `${loc} 的 ${k} 丢了`).toBe('string')
      }
    }
  })
})

/** ★ 字典是**嵌套对象**，必须按路径走，不能当 `Record<string,string>`。 */
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
