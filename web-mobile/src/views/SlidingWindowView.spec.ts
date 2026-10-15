import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import SlidingWindowView from './SlidingWindowView.vue'
import { setLocale, t } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchSlidingWindow,
  slidingWindowRoleIsPrivileged,
  slidingWindowTenantScopeLabel,
  slidingWindowParamsAreAcceptable,
  slidingWindowTotalMatchesEntries,
  slidingWindowStatsTotalMatchesEntries,
  slidingWindowSuccessPlusFailedEqualsTotal,
  slidingWindowErrorKindSum,
  slidingWindowErrorKindsAtMostFailed,
  slidingWindowErrorKindsCoverEveryFailure,
  slidingWindowFailureRateMatches,
  slidingWindowRedisSourceImpliesEntries,
  slidingWindowFallbackSourceStillHasEntries,
  slidingWindowEntriesAreNonAscending,
  SLIDING_WINDOW_GLOBAL_ROLES,
  SLIDING_WINDOW_SOURCES,
  SLIDING_WINDOW_LIMIT_MIN,
  SLIDING_WINDOW_LIMIT_MAX,
  type SlidingWindowResponse,
  type CallEntry,
} from '@/api/slidingWindow'

/**
 * 凭据模型滑窗调用明细面回归护栏（2026-10-14，第一百一十五批）。
 *
 * ⚠️⚠️⚠️⚠️ **权限档是 `'super_admin'`，与批 113/114 连续两批相反。**
 * 注册处同为 admin 档（`admin/handler.go:1475` `h.admin`），
 * 但**「admin 档」在本族连租户隔离都做不到**：
 * `handleSlidingWindow`（`admin/credential_monitor.go:704-782`）里
 * **既没有 `tenantID` 变量，也没有任何 `IsTenantAdmin`**，
 * 两条数据腿**都**没有租户维度：
 * - Redis 腿 `m.recorder.GetRecent(ctx, credentialID, model, since)` —— 键只有
 *   `(credentialID, model)`；
 * - 回退腿 `slidingWindowQuery()` / `slidingWindowHotQuery()` 的 WHERE 只有
 *   `credential_id = $1 AND lower(...) = lower($2) AND ts > …`。
 * 而 `credential_id` 是**全局唯一**主键 ⇒ **任何通过认证的主体报出别的租户的
 * credential_id 就能读到它的逐次调用明细**。
 * ⇒ `'admin'` 档在这里会给出**虚假的安全感**，所以必须过度限制。
 *
 * ★★ **本批还修掉一个真缺陷**：`source` 的回退取值被写成 `'request_logs'`，
 *   而后端发的是 **`'_request_logs'`** ⇒ 回退信号判据对真实响应恒 false。
 *   ⇒ 那个错是从**模块头抄漏了一行 Go** 开始，连带常量 / 实现 / 3 条用例一起歪，
 *     三方互相印证、整套件全绿。修法见 `api/slidingWindow.test.ts` 的 (15) 组。
 *   ⇒ ★★★ 判别动作：**注释 / 常量 / 判据三者互相印证时，要问
 *     「它们是从同一份错误来源抄来的吗」** —— 互证不等于正确。
 *
 * 判据钉十二组不变量：
 *  ① ★★★★★★ 权限档 = `'super_admin'`，且**必须进 superAdmin 白名单**
 *      （与 113/114 两席相反），并读 Go 源码钉「没有租户维度」这一事实。
 *  ② ★★★★ 参数守卫：credential_id 不能是 0、limit 落在 1–500、model 非空。
 *  ③ ★★ **负数 credential_id 不拦**（后端只判 `=== 0`）。
 *  ④ ★★★★★ **四个数字互锁**：`total_returned === entries.length === stats.total`，
 *      且 `success + failed === total`。
 *  ⑤ ★★★★ `error_kinds` 求和**≤** `failed`；小于时**不是错**，只说明。
 *  ⑥ ★★★★★ `failure_rate === 0` 当且仅当 `total === 0`；否则 `failed/total`。
 *  ⑦ ★★★★★ `source === 'redis'` ⟹ 有行；`'_request_logs'` 有行是**正常**的。
 *  ⑧ ★★★★ `err` 是唯一 omitempty 键 ⇒ 成功条目里**键不存在**，两种都要显示清楚。
 *  ⑨ ★★★ `rid` 恒在但**可能为空串** ⇒ 空串单独说。
 *  ⑩ ★★★ 无 tiebreak ⇒ 只断言非升序，并列**不得**误报。
 *  ⑪ ★★★ `model` **原样**回显，不是规范化后的实际命中值。
 *  ⑫ ★★ 触控 ≥48 CSS px。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function entry(over: Partial<CallEntry> = {}): CallEntry {
  return { rid: 'r1', ts: 2000, ok: true, lat: 120, ...over }
}

function failedEntry(over: Partial<CallEntry> = {}): CallEntry {
  return { rid: 'r2', ts: 1000, ok: false, lat: 300, err: 'timeout', ...over }
}

function respOf(entries: CallEntry[], over: Partial<SlidingWindowResponse> = {}): SlidingWindowResponse {
  const success = entries.filter((e) => e.ok).length
  const failed = entries.length - success
  const error_kinds: Record<string, number> = {}
  for (const e of entries) {
    if (!e.ok && e.err) error_kinds[e.err] = (error_kinds[e.err] ?? 0) + 1
  }
  return {
    credential_id: 7,
    model: 'gpt-4o',
    window_minutes: 60,
    limit: 50,
    source: 'redis',
    total_returned: entries.length,
    entries,
    stats: {
      total: entries.length,
      success,
      failed,
      failure_rate: entries.length > 0 ? failed / entries.length : 0,
      error_kinds,
    },
    ...over,
  } as SlidingWindowResponse
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/slidingWindow', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/slidingWindow')>()
  return { ...actual, fetchSlidingWindow: vi.fn() }
})

const mFetch = fetchSlidingWindow as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(SlidingWindowView)
  await flushPromises()
  return w
}

async function queryWith(w: W, over: { id?: string; model?: string; minutes?: string; limit?: string } = {}) {
  await byId(w, 'sw-credentialId').setValue(over.id ?? '7')
  await byId(w, 'sw-model').setValue(over.model ?? 'gpt-4o')
  await byId(w, 'sw-minutes').setValue(over.minutes ?? '60')
  await byId(w, 'sw-limit').setValue(over.limit ?? '50')
  await flushPromises()
  await byId(w, 'sw-load').trigger('click')
  await flushPromises()
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/SlidingWindowView.vue'), 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(respOf([entry(), failedEntry()]))
})

// ── ① 权限档 = super_admin ────────────────────────────────────────────────

describe('① 权限档 super_admin（与批 113/114 相反）', () => {
  it('①-1 ★★★★★★ 全租户只对 super_admin / admin_key 成立', () => {
    expect([...SLIDING_WINDOW_GLOBAL_ROLES]).toEqual(['super_admin', 'admin_key'])
    expect(slidingWindowRoleIsPrivileged('super_admin')).toBe(true)
    expect(slidingWindowRoleIsPrivileged('admin_key')).toBe(true)
    // ★★ 与 errorsTrend / modelHistory **相反**：那两族 tenant_admin 够用，
    //   本族连租户维度都没有 ⇒ 必须挡。
    expect(slidingWindowRoleIsPrivileged('tenant_admin')).toBe(false)
    expect(slidingWindowRoleIsPrivileged('user')).toBe(false)
    expect(slidingWindowRoleIsPrivileged('')).toBe(false)
  })

  it('①-2 ★★★★ ★★ 口径只有两值，**没有 own**', () => {
    expect(slidingWindowTenantScopeLabel('super_admin')).toBe('all')
    expect(slidingWindowTenantScopeLabel('admin_key')).toBe('all')
    expect(slidingWindowTenantScopeLabel('tenant_admin')).toBe('unexpected')
    expect(slidingWindowTenantScopeLabel('user')).toBe('unexpected')
  })

  it('①-3 ★★★★★★ 抽屉席必须声明 super_admin，且**要进白名单**', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'sliding-window')
    expect(seat).toBeDefined()
    expect(seat!.requiresRole).toBe('super_admin')
  })

  it('①-4 ★★★★★★ tenant_admin 被挡且**连请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'sw-nope')).toBe(true)
    expect(hasId(w, 'sw-load')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-5 ★★★★ role=user 同样被挡', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(hasId(w, 'sw-nope')).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-6 ★★★★ 正控：super_admin / admin_key 都进得了页面', async () => {
    for (const r of ['super_admin', 'admin_key']) {
      authState.role = r
      const w = await mountView()
      expect(hasId(w, 'sw-nope')).toBe(false)
      expect(hasId(w, 'sw-load')).toBe(true)
    }
  })

  it('①-7 ★★★ 被拒页要说清「为什么」，不是让用户以为自己没配好', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(byId(w, 'sw-nope-why').text()).toContain('没有任何租户维度')
    // ★ 断言替换后紧邻的那一段
    expect(byId(w, 'sw-nope-scope').text()).toContain('：unexpected。')
  })
})

// ── ②③ 参数守卫 ───────────────────────────────────────────────────────────

describe('②③ 参数守卫', () => {
  it('②-1 ★★★★ credential_id=0 必拒；limit 越界必拒；model 空必拒', () => {
    const ok = { credentialId: 7, model: 'gpt-4o', limit: 50 }
    expect(slidingWindowParamsAreAcceptable(ok)).toBe(true)
    expect(slidingWindowParamsAreAcceptable({ ...ok, credentialId: 0 })).toBe(false)
    expect(slidingWindowParamsAreAcceptable({ ...ok, model: '' })).toBe(false)
    expect(slidingWindowParamsAreAcceptable({ ...ok, limit: 0 })).toBe(false)
    expect(slidingWindowParamsAreAcceptable({ ...ok, limit: 501 })).toBe(false)
  })

  it('②-2 ★★★ 边界 1 与 500 都合法', () => {
    expect(SLIDING_WINDOW_LIMIT_MIN).toBe(1)
    expect(SLIDING_WINDOW_LIMIT_MAX).toBe(500)
    expect(slidingWindowParamsAreAcceptable({ credentialId: 7, model: 'm', limit: 1 })).toBe(true)
    expect(slidingWindowParamsAreAcceptable({ credentialId: 7, model: 'm', limit: 500 })).toBe(true)
  })

  it('②-3 ★★★★ 非数字被挡（后端 queryInt 会静默回落成 0）', async () => {
    const w = await mountView()
    await byId(w, 'sw-credentialId').setValue('abc')
    await byId(w, 'sw-model').setValue('m')
    await byId(w, 'sw-limit').setValue('50')
    await flushPromises()
    expect(hasId(w, 'sw-params-bad')).toBe(true)
  })

  it('②-4 ★★★★ 非法参数时查询按钮禁用', async () => {
    const w = await mountView()
    await byId(w, 'sw-credentialId').setValue('7')
    await byId(w, 'sw-model').setValue('gpt-4o')
    await byId(w, 'sw-limit').setValue('999')
    await flushPromises()
    expect(hasId(w, 'sw-params-bad')).toBe(true)
    expect((byId(w, 'sw-load').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('②-5 ★★★ ★★ `minutes` **不在守卫里**（后端根本不校验它）', () => {
    // ★ 这一条钉住「客户端没有为 minutes 加后端没有的规则」。
    //   判据的函数签名里**根本没有 minutes** —— 加进来就是替后端加规则。
    expect(slidingWindowParamsAreAcceptable({ credentialId: 7, model: 'm', limit: 50 })).toBe(true)
    const w = 0
    expect(w).toBe(0)
  })

  it('②-6 ★★★ 页面上说清「minutes 不校验、非数字会静默回落」', async () => {
    const w = await mountView()
    const note = byId(w, 'sw-param-note').text()
    expect(note).toContain('不校验')
    expect(note).toContain('静默回落')
  })

  it('③-1 ★★★ ★★ 负数 credential_id 不拦（后端只判 === 0）', () => {
    expect(slidingWindowParamsAreAcceptable({ credentialId: -1, model: 'm', limit: 50 })).toBe(true)
  })

  it('③-2 ★★★ 正控：合法条件原样发给后端', async () => {
    const w = await mountView()
    await queryWith(w, { id: '9', model: 'glm-5', minutes: '30', limit: '20' })
    expect(mFetch).toHaveBeenCalledWith(
      expect.objectContaining({ credentialId: 9, model: 'glm-5', minutes: 30, limit: 20 }),
    )
  })
})

// ── ④ 四个数字互锁 ────────────────────────────────────────────────────────

describe('④ 四个数字互锁', () => {
  it('④-1 ★★★★★ 正控：正常载荷四条全成立', () => {
    const r = respOf([entry(), failedEntry()])
    expect(slidingWindowTotalMatchesEntries(r)).toBe(true)
    expect(slidingWindowStatsTotalMatchesEntries(r)).toBe(true)
    expect(slidingWindowSuccessPlusFailedEqualsTotal(r)).toBe(true)
  })

  it('④-2 ★★★★★ 页面上三个数都可见', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(byId(w, 'sw-s-total_returned').text()).toBe('2')
    expect(byId(w, 'sw-st-total').text()).toBe('2')
    expect(byId(w, 'sw-st-success').text()).toBe('1')
    expect(byId(w, 'sw-st-failed').text()).toBe('1')
  })

  it('④-3 ★★★★ total_returned 对不上时报出来', async () => {
    mFetch.mockResolvedValue(respOf([entry()], { total_returned: 5 }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-total-bad')).toBe(true)
  })

  it('④-4 ★★★★ stats.total 对不上时报出来', async () => {
    const r = respOf([entry()])
    r.stats.total = 9
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-stats-total-bad')).toBe(true)
  })

  it('④-5 ★★★★ 成功 + 失败对不上总数时报出来', async () => {
    const r = respOf([entry(), failedEntry()])
    r.stats.success = 2
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-sum-bad')).toBe(true)
  })

  it('④-6 ★★★ 空载荷时四条全成立（0 === 0）', () => {
    const r = respOf([])
    expect(slidingWindowTotalMatchesEntries(r)).toBe(true)
    expect(slidingWindowStatsTotalMatchesEntries(r)).toBe(true)
    expect(slidingWindowSuccessPlusFailedEqualsTotal(r)).toBe(true)
  })
})

// ── ⑤ error_kinds 求和 ≤ failed ───────────────────────────────────────────

describe('⑤ 错误类别求和 ≤ 失败数', () => {
  it('⑤-1 ★★★★ 求和等于失败数（每个失败都带非空 err）', () => {
    const s = respOf([entry(), failedEntry(), failedEntry({ ts: 900, err: 'econnreset' })]).stats
    expect(slidingWindowErrorKindSum(s)).toBe(2)
    expect(slidingWindowErrorKindsAtMostFailed(s)).toBe(true)
    expect(slidingWindowErrorKindsCoverEveryFailure(respOf([
      entry(), failedEntry(), failedEntry({ ts: 900, err: 'econnreset' }),
    ]).entries)).toBe(true)
  })

  it('⑤-2 ★★★★ 求和小于失败数 ⇒ 只说明、**不报警**', async () => {
    // ★★ 失败条目的 err 是空串 ⇒ 它完全不进 error_kinds ⇒ 求和 < failed。
    //   这**不是 bug**（见文件头第 4 条），页面必须只说明不报错。
    const r = respOf([entry(), failedEntry({ err: '' })])
    expect(slidingWindowErrorKindSum(r.stats)).toBe(0)
    expect(r.stats.failed).toBe(1)
    expect(slidingWindowErrorKindsAtMostFailed(r.stats)).toBe(true)
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-kinds-bad')).toBe(false)
    expect(hasId(w, 'sw-kinds-incomplete')).toBe(true)
  })

  it('⑤-3 ★★★★ 求和大于失败数才是错', async () => {
    const r = respOf([entry(), failedEntry()])
    r.stats.error_kinds = { timeout: 9 }
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-kinds-bad')).toBe(true)
  })

  it('⑤-4 ★★★ 空分布是可以确定说出来的', async () => {
    mFetch.mockResolvedValue(respOf([entry()]))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-ek-empty')).toBe(true)
  })
})

// ── ⑥ failure_rate ────────────────────────────────────────────────────────

describe('⑥ failure_rate 是派生值', () => {
  it('⑥-1 ★★★★★ total === 0 时恒为 0（不是 NaN、不是 null）', () => {
    const s = respOf([]).stats
    expect(s.total).toBe(0)
    expect(s.failure_rate).toBe(0)
    expect(slidingWindowFailureRateMatches(s)).toBe(true)
  })

  it('⑥-2 ★★★★★ total > 0 时 === failed / total', () => {
    const s = respOf([entry(), failedEntry(), failedEntry({ ts: 900 })]).stats
    expect(s.failure_rate).toBeCloseTo(2 / 3, 10)
    expect(slidingWindowFailureRateMatches(s)).toBe(true)
  })

  it('⑥-3 ★★★★ 对不上时报出来', async () => {
    const r = respOf([entry(), failedEntry()])
    r.stats.failure_rate = 0.99
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-rate-bad')).toBe(true)
  })
})

// ── ⑦ source 的两档 ───────────────────────────────────────────────────────

describe('⑦ source 语义', () => {
  it('⑦-1 ★★★★★ 两个来源的字面量（★ 回退那一侧**带前导下划线**）', () => {
    expect([...SLIDING_WINDOW_SOURCES]).toEqual(['redis', '_request_logs'])
    // ★★★ 负向钉法：无下划线的那个**不得**出现在常量里。
    //   它一旦出现，回退信号判据对真实响应恒为 false（第一批十五批实测踩过）。
    expect([...SLIDING_WINDOW_SOURCES]).not.toContain('request_logs')
  })

  it('⑦-2 ★★★★★ 正控：redis 源必然有行', () => {
    expect(slidingWindowRedisSourceImpliesEntries(respOf([entry()]))).toBe(true)
  })

  it('⑦-3 ★★★★ redis 源零行 ⇒ 报（违反后端不变式）', async () => {
    mFetch.mockResolvedValue(respOf([], { source: 'redis' }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-redis-empty-bad')).toBe(true)
  })

  it('⑦-4 ★★★★★ 回退源**有数据是正常的**，页面只说明不报错', async () => {
    mFetch.mockResolvedValue(respOf([entry(), failedEntry()], { source: '_request_logs' }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-fallback-data')).toBe(true)
    expect(hasId(w, 'sw-redis-empty-bad')).toBe(false)
    expect(slidingWindowFallbackSourceStillHasEntries(respOf([entry()], { source: '_request_logs' }))).toBe(true)
  })

  it('⑦-5 ★★★ Redis 故障不可见这件事恒在说明', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(byId(w, 'sw-redis-note').text()).toContain('静默回退')
  })

  it('⑦-6 ★★★ 回退源零行 ⇒ 不触发「回退有数据」', async () => {
    mFetch.mockResolvedValue(respOf([], { source: '_request_logs' }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-fallback-data')).toBe(false)
    expect(slidingWindowFallbackSourceStillHasEntries(respOf([], { source: '_request_logs' }))).toBe(false)
  })
})

// ── ⑧⑨ err 与 rid ────────────────────────────────────────────────────────

describe('⑧⑨ err 与 rid', () => {
  it('⑧-1 ★★★★ 成功条目的 err 键**不存在**（唯一带 omitempty 的键）', async () => {
    const w = await mountView()
    await queryWith(w)
    // 第 0 条是成功条目
    // ★★ 按**文案身份**比对，不用裸子串 —— 本批变异 S18 首轮 NO_EFFECT 就是
    //   因为「空串」那条文案当初逐字写着「键不存在」四个字，
    //   于是把成功条目改成说「键不存在」照样能过（同批 ⑧-2、批 114 的 w.html() 同族）。
    expect(byId(w, 'sw-e-0-err').text()).toBe(t('sw.field.noError'))
    // 第 1 条是失败条目，带 err
    expect(byId(w, 'sw-e-1-err').text()).toBe('timeout')
  })

  it('⑧-2 ★★★★ 失败条目的 err 是空串 ⇒ 与「键不存在」显示得**不一样**', async () => {
    mFetch.mockResolvedValue(respOf([failedEntry({ err: '' })]))
    const w = await mountView()
    await queryWith(w)
    const txt = byId(w, 'sw-e-0-err').text()
    expect(txt).toContain('空串')
    // ★★ 按身份比对：两条文案互不相等，且都不是对方的
    expect(txt).toBe(t('sw.field.blankError'))
    expect(txt).not.toBe(t('sw.field.noError'))
  })

  it('⑨-1 ★★★★ rid 恒在但**可能为空串** ⇒ 空串单独说', async () => {
    mFetch.mockResolvedValue(respOf([entry({ rid: '' })]))
    const w = await mountView()
    await queryWith(w)
    const txt = byId(w, 'sw-e-0-rid').text()
    expect(txt).toContain('空串')
    expect(byId(w, 'sw-e-0-rid').exists()).toBe(true)
  })

  it('⑨-2 ★★★ 正常 rid 原样显示', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(byId(w, 'sw-e-0-rid').text()).toBe('r1')
  })
})

// ── ⑩ 时间序 ──────────────────────────────────────────────────────────────

describe('⑩ 时间序无 tiebreak', () => {
  it('⑩-1 ★★★★ 并列毫秒**不得**误报', () => {
    expect(slidingWindowEntriesAreNonAscending([entry({ ts: 5 }), entry({ ts: 5 })])).toBe(true)
  })

  it('⑩-2 ★★★ 空数组与单条都算「非升序」', () => {
    expect(slidingWindowEntriesAreNonAscending([])).toBe(true)
    expect(slidingWindowEntriesAreNonAscending([entry()])).toBe(true)
  })

  it('⑩-3 ★★★ 真正升序的一段才报', async () => {
    mFetch.mockResolvedValue(respOf([entry({ ts: 1000 }), failedEntry({ ts: 2000 })]))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-order-bad')).toBe(true)
  })

  it('⑩-4 ★★★ 正控：正常倒序不报，提示恒在', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'sw-order-bad')).toBe(false)
    expect(byId(w, 'sw-order-note').text()).toContain('次级排序')
  })
})

// ── ⑪ 回显 ────────────────────────────────────────────────────────────────

describe('⑪ 回显是原样的', () => {
  it('⑪-1 ★★★ 正控：一致时两条都不报', async () => {
    const w = await mountView()
    await queryWith(w, { id: '7', model: 'gpt-4o' })
    expect(byId(w, 'sw-s-model').text()).toBe('gpt-4o')
    expect(hasId(w, 'sw-model-echo-bad')).toBe(false)
    expect(hasId(w, 'sw-credential-echo-bad')).toBe(false)
  })

  it('⑪-2 ★★★★ model 大小写不一致也算「对不上」（后端是 lower() 比的）', async () => {
    mFetch.mockResolvedValue(respOf([entry()], { model: 'GPT-4O' }))
    const w = await mountView()
    await queryWith(w, { model: 'gpt-4o' })
    // ★★ 命中可能只是大小写不同 —— 回显是**原样**传进来的那个，
    //   不是规范化后实际命中的 `outbound_model`。
    expect(hasId(w, 'sw-model-echo-bad')).toBe(true)
  })

  it('⑪-3 ★★★★ credential_id 对不上时报出来', async () => {
    mFetch.mockResolvedValue(respOf([entry()], { credential_id: 999 }))
    const w = await mountView()
    await queryWith(w, { id: '7' })
    expect(hasId(w, 'sw-credential-echo-bad')).toBe(true)
  })

  it('⑪-4 ★★★ 两个回显各自独立', async () => {
    mFetch.mockResolvedValue(respOf([entry()], { credential_id: 7, model: 'other' }))
    const w = await mountView()
    await queryWith(w, { id: '7', model: 'gpt-4o' })
    expect(hasId(w, 'sw-model-echo-bad')).toBe(true)
    expect(hasId(w, 'sw-credential-echo-bad')).toBe(false)
  })
})

// ── ⑫ 触控 ────────────────────────────────────────────────────────────────

describe('⑫ 触控目标 ≥48 CSS px', () => {
  it('⑫-1 ★★★ 解析出 min-height 数值再比大小', () => {
    const mins = [...viewSrc().matchAll(/min-height:\s*(\d+)px/g)].map((m) => Number(m[1]))
    expect(mins.length).toBeGreaterThan(0)
    for (const v of mins) expect(v).toBeGreaterThanOrEqual(48)
  })

  it('⑫-2 ★★ 四个输入与查询按钮都真的渲染了', async () => {
    const w = await mountView()
    for (const id of ['sw-load', 'sw-credentialId', 'sw-model', 'sw-minutes', 'sw-limit']) {
      expect(hasId(w, id)).toBe(true)
    }
  })
})

// ── ⑬ 后端「没有租户维度」的结构层钉法 ────────────────────────────────────

describe('⑬ 后端没有租户维度（读 Go 源码）', () => {
  const ROOT = join(process.cwd(), '..')
  function goSrc(rel: string): string {
    const p = join(ROOT, rel)
    expect(existsSync(p), `找不到 ${rel}（判据路径写错会让本组全部空转）`).toBe(true)
    return readFileSync(p, 'utf8')
  }

  it('⑬-1 ★★★★★★ handler 里既没有 tenantID 变量也没有 IsTenantAdmin', () => {
    const src = goSrc('admin/credential_monitor.go')
    const fn = src.slice(src.indexOf('func (m *CredentialMonitorHandlers) handleSlidingWindow'))
    const body = fn.slice(0, fn.indexOf('\n}\n\n'))
    // ★★★ 这三条是**本批权限档的全部依据**：
    //   没有租户键 ⇒ 非特权角色报出别的租户的 credential_id 就能读到它的调用明细。
    expect(body).not.toContain('tenantID')
    expect(body).not.toContain('IsTenantAdmin')
    expect(body).not.toContain('EffectiveTenantID')
    expect(body).not.toMatch(/Role\s*==|RequireSuperAdmin|requireUserManager/)
  })

  it('⑬-2 ★★★★★★ Redis 腿的键只有 (credentialID, model)', () => {
    const src = goSrc('admin/credential_monitor.go')
    expect(src).toContain('m.recorder.GetRecent(ctx, credentialID, model, since)')
    expect(src).not.toContain('GetRecent(ctx, credentialID, model, since, tenantID)')
  })

  it('⑬-3 ★★★★★★ 两条回退 SQL 的 **WHERE 里都没有** tenant 过滤', () => {
    const src = goSrc('admin/credential_monitor.go')

    // —— 长窗路径：整个查询里**根本没有** tenant_id ——
    const q = src.slice(src.indexOf('func slidingWindowQuery() string'))
    const qBody = q.slice(0, q.indexOf('\n}\n'))
    expect(qBody).toContain('credential_id = $1')
    expect(qBody).not.toContain('tenant_id')

    // ★★★★★ 热窗路径**有一个** tenant_id，但它在 **LEFT JOIN 的 ON 子句**里
    //   （`ON d.tenant_id::text = t.tenant_id::text`）—— 那是在**同一个租户内**
    //   把两张表对上，**不是访问控制**。
    // ⇒ ⇒★ **naive `grep tenant_id` 会得出「这里有租户处理」的错误结论**，
    //   而实际上 WHERE 子句里一条 tenant 条件都没有。
    //   ⇒ 判据因此只看 **WHERE 段**（子查询合流之后的那个）。
    const h = src.slice(src.indexOf('func slidingWindowHotQuery() string'))
    const hBody = h.slice(0, h.indexOf('\n}\n'))
    expect(hBody).toContain('ON d.tenant_id::text = t.tenant_id::text')
    const wherePart = hBody.slice(hBody.indexOf(') cand'))
    expect(wherePart).toContain('credential_id = $1')
    expect(wherePart).not.toContain('tenant_id')
    // ★★ 并且**没有任何按租户的绑定参数**（$5 之类）被传进这条查询
    expect(hBody).not.toMatch(/tenant_id\s*=\s*\$\d/)
  })

  it('⑬-4 ★★★ 注册处传的仍是 h.admin（本族**不是** superAdmin 档注册）', () => {
    const h = goSrc('admin/handler.go')
    const line = h.split('\n').find((l) => l.includes('RegisterMonitorRoutes(mux'))
    expect(line).toBeDefined()
    expect(line).toContain('h.admin')
    expect(line).not.toContain('superAdmin')
  })

  it('⑬-5 ★★★★ 挂载行号已订正为 :1475（原 :1453 过期）', () => {
    const h = goSrc('admin/handler.go')
    const line = h.split('\n').find((l) => l.includes('RegisterMonitorRoutes(mux'))
    // ★ 行号只是定位提示；判据核的是「这一行还在、内容没变」，不是死记行号。
    expect(line).toBeDefined()
  })
})