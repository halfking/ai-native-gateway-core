import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSubscriptionTiers,
  unwrapSubscriptionTiers,
  subscriptionTiersAreSortOrdered,
  subscriptionTierModuleKeysAreUnique,
  subscriptionTierCodesAreUnique,
  subscriptionTierHasNoModules,
  subscriptionTierHasModules,
  subscriptionTierEnabledIsNotExposed,
  subscriptionTierIsFlat,
  subscriptionTierMaxFeaturesIsAbsent,
  subscriptionTierDescriptionIsEmptyString,
  SUBSCRIPTION_TIER_KEYS,
  SUBSCRIPTION_TIER_CORE_KEYS,
  SUBSCRIPTION_TIER_ERROR_OPS,
  type SubscriptionTierRow,
} from './subscriptionTiers'

/**
 * 订阅档的契约测试（2026-10-08，第八十七批）。
 *
 * 后端：**echo group**（不是 admin 的 mux！）
 * `cmd/gateway/main.go:6964` adminGroup := e.Group("/api/admin", requireSuperAdmin)
 * → `:6966` licensing.RegisterModuleRoutes
 * → `licensing/admin_api.go:33-36` → `licensing/module_api.go:22` g.GET("/tiers", …)
 * → 实现 `licensing/module_api.go:64-98`
 *
 * 重点是源文件头写明的十三件事 (1)…(13)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `licensing/types.go:149-155` + `module_api.go:80-83` ──
// ★ 内嵌已扁平 ⇒ 六个键同层。

function tier(over: Partial<SubscriptionTierRow> = {}): SubscriptionTierRow {
  return {
    code: 'pro',
    name: '专业版',
    description: '面向团队',
    price_cents: 9900,
    sort_order: 10,
    module_keys: ['log_ops', 'data_lifecycle'],
    ...over,
  }
}

function rows(): SubscriptionTierRow[] {
  return [
    // ORDER BY sort_order ⇒ 升序
    tier({ code: 'free', name: '免费版', description: '', price_cents: 0, sort_order: 1, module_keys: [] }),
    tier({ code: 'basic', name: '基础版', description: '个人', price_cents: 2900, sort_order: 5 }),
    tier({ code: 'pro', name: '专业版', description: '面向团队', price_cents: 9900, sort_order: 10 }),
  ]
}

// ══════════════════════════════════════════════════════════════════════════
// (1)(2)(3) 顶层裸数组与六键扁平结构
// ══════════════════════════════════════════════════════════════════════════

describe('(1)(2)(3) 顶层裸数组与扁平六键', () => {
  it('★ 顶层是数组 ⇒ 放行（不是 envelope）', () => {
    expect(unwrapSubscriptionTiers(rows())).toHaveLength(3)
  })

  it('★ ★ 空数组 ⇒ 放行（make(…,0,…) 初始化，不是 null）', () => {
    expect(unwrapSubscriptionTiers([])).toEqual([])
  })

  it('★ ★★ 顶层是对象 ⇒ 抛（**不是** {tiers:[…]} 信封）', () => {
    expect(() => unwrapSubscriptionTiers({ tiers: [] })).toThrow(
      /订阅档 响应形状不符：期望顶层裸数组，实得 object/,
    )
  })

  it('★ 顶层 null ⇒ 抛并报 null', () => {
    expect(() => unwrapSubscriptionTiers(null)).toThrow(/实得 null/)
  })

  it('★ 顶层是字符串 ⇒ 抛并报 string', () => {
    expect(() => unwrapSubscriptionTiers('x')).toThrow(/实得 string/)
  })

  it('★ ★ 数组元素不是对象 ⇒ 抛并点名下标', () => {
    expect(() => unwrapSubscriptionTiers(['x'])).toThrow(/订阅档\[0\] 响应形状不符：期望裸对象，实得 string/)
  })

  it('★ ★★★ 数组元素本身是数组 ⇒ 抛（`typeof` 是 object，只有 Array.isArray 能拦）', () => {
    expect(() => unwrapSubscriptionTiers([[]])).toThrow(/订阅档\[0\] 响应形状不符：期望裸对象，实得 array/)
  })
  it('★ ★★ 元素是嵌套形状（tier 包一层）⇒ 抛（内嵌是**扁平**的）', () => {
    // ★ 这是最容易写错的形状：客户端若按 {tier:{…}} 读，这里会缺 5 个键。
    const nested = [{ tier: { code: 'pro', name: '专业版' }, module_keys: [] }]
    expect(() => unwrapSubscriptionTiers(nested)).toThrow(/订阅档\[0\] 缺 5 个键/)
  })

  it('★ ★★★★ 顶层有 code 但**仍带 tier 包装层** ⇒ IsFlat 为 false（专格）', () => {
    // ★ 嵌套样本通常连顶层 code 都没有 ⇒ 那一项先失败，暴露不出这一项。
    //   这一格的要点是：**两层都有** code，只有「没有 tier 包装层」能区分。
    const both = { code: 'pro', name: '专业版', tier: { code: 'pro' }, module_keys: [] }
    expect(subscriptionTierIsFlat(both as unknown as SubscriptionTierRow)).toBe(false)
  })
  it('★ ★ subscriptionTierIsFlat ⇒ true（六键同层、没有 tier 包装层）', () => {
    expect(subscriptionTierIsFlat(tier())).toBe(true)
  })

  it('★ ★★ subscriptionTierIsFlat 对嵌套形状 ⇒ false（专格）', () => {
    const nested = { tier: { code: 'pro' }, module_keys: [] } as unknown as SubscriptionTierRow
    expect(subscriptionTierIsFlat(nested)).toBe(false)
  })

  it('★ 缺一个键 ⇒ 抛并点名（六键恒在）', () => {
    const r = del(tier() as unknown as Record<string, unknown>, 'sort_order')
    expect(() => unwrapSubscriptionTiers([r])).toThrow(/订阅档\[0\] 缺 1 个键（sort_order）/)
  })

  it('★ ★ 缺三个键 ⇒ 抛并报数量与键名顺序', () => {
    let r = tier() as unknown as Record<string, unknown>
    r = del(r, 'description')
    r = del(r, 'price_cents')
    r = del(r, 'module_keys')
    expect(() => unwrapSubscriptionTiers([r])).toThrow(/订阅档\[0\] 缺 3 个键（description, price_cents, module_keys）/)
  })

  it('★ code 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ code: 1 as unknown as string })])).toThrow(
      /\[0\] 的 code 不是字符串/,
    )
  })

  it('★ name 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ name: null as unknown as string })])).toThrow(
      /\[0\] 的 name 不是字符串/,
    )
  })

  it('★ description 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ description: 1 as unknown as string })])).toThrow(
      /\[0\] 的 description 不是字符串/,
    )
  })

  it('★ price_cents 不是数字 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ price_cents: '9900' as unknown as number })])).toThrow(
      /\[0\] 的 price_cents 不是数字/,
    )
  })

  it('★ sort_order 不是数字 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ sort_order: null as unknown as number })])).toThrow(
      /\[0\] 的 sort_order 不是数字/,
    )
  })

  it('★ ★★ module_keys 是 null ⇒ 抛（作者显式补了空数组，永不为 null）', () => {
    expect(() => unwrapSubscriptionTiers([tier({ module_keys: null as unknown as string[] })])).toThrow(
      /\[0\] 的 module_keys 不是数组/,
    )
  })

  it('★ ★★ module_keys 是字符串 ⇒ 抛', () => {
    expect(() => unwrapSubscriptionTiers([tier({ module_keys: 'log_ops' as unknown as string[] })])).toThrow(
      /\[0\] 的 module_keys 不是数组/,
    )
  })

  it('★ ★ module_keys 元素不是字符串 ⇒ 抛并点名内层下标', () => {
    expect(() => unwrapSubscriptionTiers([tier({ module_keys: ['log_ops', 1 as unknown as string] })])).toThrow(
      /\[0\] 的 module_keys\[1\] 不是字符串/,
    )
  })

  it('★ ★ 第二项出错时点名下标 1', () => {
    expect(() => unwrapSubscriptionTiers([tier(), { code: 'x' }])).toThrow(/订阅档\[1\] 缺/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4) module_keys 的两种形态
// ══════════════════════════════════════════════════════════════════════════

describe('(4) module_keys 的空与非空', () => {
  it('★ ★★ 空数组 ⇒ 放行且 HasNoModules 为 true', () => {
    const r = unwrapSubscriptionTiers([tier({ module_keys: [] })])[0]!
    expect(subscriptionTierHasNoModules(r)).toBe(true)
  })

  it('★ ★ HasModules 与 HasNoModules 互斥（反向）', () => {
    const r = tier()
    expect(subscriptionTierHasModules(r)).toBe(true)
    expect(subscriptionTierHasNoModules(r)).toBe(false)
  })

  it('★ ★★ 空数组 ⇒ HasModules 为 false', () => {
    expect(subscriptionTierHasModules(tier({ module_keys: [] }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5)(6)(12) 排序与唯一性
// ══════════════════════════════════════════════════════════════════════════

describe('(5) 行按 sort_order 升序', () => {
  it('★ 升序 ⇒ 为 true', () => {
    expect(subscriptionTiersAreSortOrdered(rows())).toBe(true)
  })

  it('★ ★★ 降序 ⇒ 为 false', () => {
    expect(subscriptionTiersAreSortOrdered([tier({ sort_order: 10 }), tier({ sort_order: 1 })])).toBe(false)
  })

  it('★ ★ 只有第一项递减 ⇒ 为 false', () => {
    expect(subscriptionTiersAreSortOrdered([tier({ sort_order: 10 }), tier({ sort_order: 5 }), tier({ sort_order: 5 })])).toBe(
      false,
    )
  })

  it('★ ★★ 同值并列 ⇒ 仍为 true（SQL 只 ORDER BY，没要求唯一）', () => {
    expect(subscriptionTiersAreSortOrdered([tier({ sort_order: 5 }), tier({ sort_order: 5 })])).toBe(true)
  })

  it('★ 单项数组 ⇒ 为 true', () => {
    expect(subscriptionTiersAreSortOrdered([tier()])).toBe(true)
  })

  it('★ 空数组 ⇒ 为 true', () => {
    expect(subscriptionTiersAreSortOrdered([])).toBe(true)
  })
})

describe('(6)(12) 元素唯一性', () => {
  it('★ ★ module_keys 内无重复 ⇒ 为 true（复合主键保证）', () => {
    expect(subscriptionTierModuleKeysAreUnique([tier({ module_keys: ['a', 'b'] })])).toBe(true)
  })

  it('★ ★★ module_keys 内有重复 ⇒ 为 false（专格）', () => {
    expect(subscriptionTierModuleKeysAreUnique([tier({ module_keys: ['a', 'a'] })])).toBe(false)
  })

  it('★ module_keys 为空数组 ⇒ 唯一性为 true', () => {
    expect(subscriptionTierModuleKeysAreUnique([tier({ module_keys: [] })])).toBe(true)
  })

  it('★ ★ code 无重复 ⇒ 为 true（表上有 UNIQUE）', () => {
    expect(subscriptionTierCodesAreUnique(rows())).toBe(true)
  })

  it('★ ★★ code 有重复 ⇒ 为 false（专格）', () => {
    expect(subscriptionTierCodesAreUnique([tier({ code: 'pro' }), tier({ code: 'pro' })])).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7)(8)(11) 不可见的过滤维度与信息损失
// ══════════════════════════════════════════════════════════════════════════

describe('(7)(8)(11) 表里有、响应里没有的东西', () => {
  it('★ ★★ enabled 不在响应里 ⇒ 为 true（表上有该列但 SELECT 不取）', () => {
    expect(subscriptionTierEnabledIsNotExposed(tier())).toBe(true)
  })

  it('★ ★★ max_features 不在响应里 ⇒ 为 true（SELECT 了但结构体没这个字段）', () => {
    expect(subscriptionTierMaxFeaturesIsAbsent(tier())).toBe(true)
  })

  it('★ ★★ 反向：注入 enabled 后不再判定为「未暴露」', () => {
    const injected = { ...(tier() as unknown as Record<string, unknown>), enabled: false }
    expect(subscriptionTierEnabledIsNotExposed(injected as unknown as SubscriptionTierRow)).toBe(false)
  })

  it('★ ★ 反向：注入 max_features 后不再判定为「缺失」', () => {
    const injected = { ...(tier() as unknown as Record<string, unknown>), max_features: '10' }
    expect(subscriptionTierMaxFeaturesIsAbsent(injected as unknown as SubscriptionTierRow)).toBe(false)
  })

  it('★ ★ description 是空串 ⇒ 为 true（表列 NOT NULL，空串可达但 null 不可达）', () => {
    expect(subscriptionTierDescriptionIsEmptyString(tier({ description: '' }))).toBe(true)
  })

  it('★ ★ description 非空 ⇒ 为 false（反向）', () => {
    expect(subscriptionTierDescriptionIsEmptyString(tier({ description: '面向团队' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(rows()))
    const r = await fetchSubscriptionTiers()
    expect(r).toHaveLength(3)
  })

  it('★ ★ 空响应（无订阅档）被放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    expect(await fetchSubscriptionTiers()).toEqual([])
  })

  it('★ ★★ 嵌套形状 ⇒ 抛错（客户端写 {tier:{…}} 会在这里炸）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([{ tier: { code: 'pro' }, module_keys: [] }]))
    await expect(fetchSubscriptionTiers()).rejects.toThrow(/缺 5 个键/)
  })

  it('★ ★★ 停用档也会出现在响应里且客户端看不出（enabled 未暴露）', async () => {
    // ★ 后端 SELECT 不取 enabled ⇒ 停用档与启用档在响应里**长得一样**。
    fetchMock.mockResolvedValueOnce(jsonResponse(rows()))
    const all = await fetchSubscriptionTiers()
    expect(subscriptionTierEnabledIsNotExposed(all[0]!)).toBe(true)
  })

  it('★ ★ 请求路径固定且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchSubscriptionTiers()
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/tiers')
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('?')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ SUBSCRIPTION_TIER_KEYS 恰是 6 个键', () => {
    expect(SUBSCRIPTION_TIER_KEYS.length).toBe(6)
  })

  it('★ ★ 内嵌部分五键 ⊂ 六键（module_keys 是外面加的那个）', () => {
    const all = SUBSCRIPTION_TIER_KEYS as readonly string[]
    for (const k of SUBSCRIPTION_TIER_CORE_KEYS) expect(all).toContain(k)
    expect(all).toContain('module_keys')
  })

  it('★ ★ 六键里**不含** tier / SubscriptionTier（内嵌已扁平）', () => {
    const all = SUBSCRIPTION_TIER_KEYS as readonly string[]
    expect(all).not.toContain('tier')
    expect(all).not.toContain('SubscriptionTier')
  })

  it('★ ★ 六键里**不含** enabled（表上有但端点不返回）', () => {
    expect(SUBSCRIPTION_TIER_KEYS as readonly string[]).not.toContain('enabled')
  })

  it('★ ★ 六键里**不含** max_features（SELECT 了但结构体没这字段）', () => {
    expect(SUBSCRIPTION_TIER_KEYS as readonly string[]).not.toContain('max_features')
  })

  it('★ SUBSCRIPTION_TIER_ERROR_OPS 两条 500 文案', () => {
    expect([...SUBSCRIPTION_TIER_ERROR_OPS]).toEqual(['list tiers failed', 'list tier module maps failed'])
  })
})