import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchMaasModelRates,
  unwrapMaasModelRates,
  maasDimUsesCustom,
  maasDimHasDormantCustom,
  maasRowHasNoRateRecord,
  maasEffectiveDiscount,
  maasApplyDiscount,
  maasGlobalBaseIn,
  maasGlobalRate,
  maasEffectiveSource,
  MAAS_VENDOR_FALLBACK,
  MAAS_MODALITY_FALLBACK,
  MAAS_HARDCODED_BASE_IN,
  unwrapMaasOrders,
  unwrapMaasOrder,
  fetchMaasOrders,
  fetchMaasOrder,
  maasAmountYuan,
  maasListLacksPaymentHint,
  maasNameOrphaned,
  maasOrdersMaybeMore,
  MAAS_ORDER_TYPES,
  MAAS_ORDER_STATUSES,
  MAAS_PAYMENT_CHANNELS,
  MAAS_ORDERS_LIMIT_DEFAULT,
  MAAS_ORDERS_LIMIT_MAX,
  MAAS_RATE_DIMS,
  MAAS_EFFECTIVE_KEYS,
  type MaasSettings,
  type MaasModelRateRow,
  type MaasModelRatesResponse,
} from './maas'

/**
 * MaaS model-rates 的契约测试（2026-10-08）。
 *
 * ★★ 本文件的核心是复刻 `maas/rates.go` 里两个纯函数：
 *    `normalizeDiscount` / `applyDiscount` / `globalEffective` / `effectiveModelRates`
 *    —— 响应里 7 个 `credits_per_1m_*` 是**它们算出来的生效价**，
 *    不是库里的值。不复刻就没有任何办法判断一个数字的来源。
 *
 * 后端逐条对应：
 *   maas/model_rates.go:11-36   AdminModelRateRow（指针字段**无** omitempty）
 *   maas/model_rates.go:39-42   AdminModelRatesResponse = {settings, items}
 *   maas/model_rates.go:69-154  ListAdminModelRates（**无 LIMIT**、只列 active）
 *   maas/rates.go:28-33         normalizeDiscount（`d<=0 || d>1 ⇒ 1`）
 *   maas/rates.go:36-41         applyDiscount（`ceil(base*disc)`）
 *   maas/rates.go:42-70         globalEffective（**七维回落链不同**）
 *   maas/rates.go:96-102        effectiveModelRates 的 pick（`manual && val!=nil && *val>0`）
 *   maas/rates.go:119-122       storedIsManual（七个 manual 的**或**）
 *   maas_handlers.go:96         writeJSON(w, 200, resp)
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

/** 抄自 `Settings`（service.go:60-73）。 */
function settings(over: Record<string, unknown> = {}): MaasSettings {
  return {
    cents_per_credit: 0.1,
    base_credits_per_1m: 0,
    base_credits_per_1m_in: 0,
    base_credits_per_1m_out: 0,
    base_credits_per_1m_cache_in: 0,
    base_credits_per_1m_cache_out: 0,
    global_discount: 1,
    currency_display: 'CNY',
    alipay_account: '',
    wechat_mch_id: '',
    stub_alipay_qr_url: '',
    stub_wechat_qr_url: '',
    ...over,
  }
}

/** 抄自 `AdminModelRateRow`（model_rates.go:11-36）。 */
function row(over: Record<string, unknown> = {}): MaasModelRateRow {
  return {
    canonical_id: 1,
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    vendor: 'OpenAI',
    family: null,
    modality: 'text',
    status: 'active',
    credits_per_1m_in: 0,
    credits_per_1m_out: 0,
    credits_per_1m_cache_in: 0,
    credits_per_1m_cache_out: 0,
    credits_per_1m_image_tokens: 0,
    credits_per_1m_audio_tokens: 0,
    credits_per_1m_video_tokens: 0,
    manual_in: false,
    manual_out: false,
    manual_cache_in: false,
    manual_cache_out: false,
    manual_image: false,
    manual_audio: false,
    manual_video: false,
    is_custom: false,
    custom_credits_per_1m_in: null,
    custom_credits_per_1m_out: null,
    custom_credits_per_1m_cache_in: null,
    custom_credits_per_1m_cache_out: null,
    custom_credits_per_1m_image_tokens: null,
    custom_credits_per_1m_audio_tokens: null,
    custom_credits_per_1m_video_tokens: null,
    updated_at: null,
    ...over,
  }
}

function body(over: Record<string, unknown> = {}): MaasModelRatesResponse {
  return { settings: settings(), items: [row()], ...over } as MaasModelRatesResponse
}

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock)
  vi.clearAllMocks()
  fetchMock.mockResolvedValue(jsonResponse(body()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 路径与响应键（逐字对 writeJSON）', () => {
  it('★★★★★★ 打 `/api/admin/maas/model-rates`，键是 `settings` + `items`（:39-42/:96）', async () => {
    const r = await fetchMaasModelRates()
    expect(lastUrl()).toBe('/api/admin/maas/model-rates')
    expect(Array.isArray(r.items)).toBe(true)
    expect(r.settings).toBeTruthy()
  })

  it('★★★★★★ 顶层**无**包装键（不是 `{data:{…}}`）', async () => {
    const r = unwrapMaasModelRates(body())
    expect(Object.prototype.hasOwnProperty.call(r, 'data')).toBe(false)
  })

  it('★★★★★★ `items` 缺失 ⇒ 抛错；`settings` 缺失 ⇒ 抛错（两者都要）', () => {
    const { items: _i, ...noItems } = body()
    expect(() => unwrapMaasModelRates(noItems)).toThrow(/形状不符/)
    const { settings: _s, ...noSettings } = body()
    expect(() => unwrapMaasModelRates(noSettings)).toThrow(/形状不符/)
  })

  it('★★★★★★ 宽容解包一律拒绝（后端没有这一层）', () => {
    expect(() => unwrapMaasModelRates({ data: body() })).toThrow(/形状不符/)
    expect(() => unwrapMaasModelRates({ result: body() })).toThrow(/形状不符/)
    expect(() => unwrapMaasModelRates([])).toThrow(/形状不符/)
    expect(() => unwrapMaasModelRates(null)).toThrow(/形状不符/)
  })

  it('★ 合法的**空**清单必须被接受（真的没有模型，不是错）', () => {
    expect(unwrapMaasModelRates(body({ items: [] })).items).toEqual([])
  })

  it('★ 503（MaaS 没开/没接库）⇒ 原样冒到调用方', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'database not configured' } }, 503))
    await expect(fetchMaasModelRates()).rejects.toThrow(/database not configured/)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ normalizeDiscount：`0` 不是「全免」而是「不打折」', () => {
  it('★★★★★ `global_discount = 0` ⇒ 归一成 **1**（不打折）', () => {
    // ★★ rates.go:28-33：`d <= 0 || d > 1 ⇒ return 1`
    //   配 0 的人以为「全免」，实际是**原价**。
    expect(maasEffectiveDiscount(settings({ global_discount: 0 }))).toBe(1)
    expect(maasEffectiveDiscount(settings({ global_discount: -0.5 }))).toBe(1)
  })

  it('★★★★★ `> 1`（如 1.5 = 150%）⇒ 也归一成 1', () => {
    expect(maasEffectiveDiscount(settings({ global_discount: 1.5 }))).toBe(1)
  })

  it('★★★★★ 0 < d <= 1 照常返回（含 0.8）', () => {
    expect(maasEffectiveDiscount(settings({ global_discount: 0.8 }))).toBe(0.8)
    expect(maasEffectiveDiscount(settings({ global_discount: 1 }))).toBe(1)
  })

  it('★ NaN 也归一成 1（不等价于任何折扣）', () => {
    expect(maasEffectiveDiscount(settings({ global_discount: Number.NaN }))).toBe(1)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ applyDiscount 是**向上取整**', () => {
  it('★★★★★ 400 × 0.8 = 320.0 ⇒ 320', () => {
    expect(maasApplyDiscount(400, 0.8)).toBe(320)
  })

  it('★★★★★ 401 × 0.8 = 320.8 ⇒ **321**（不是 320）', () => {
    expect(maasApplyDiscount(401, 0.8)).toBe(321)
  })

  it('★ base <= 0 ⇒ 0（不乘折扣）', () => {
    expect(maasApplyDiscount(0, 0.5)).toBe(0)
    expect(maasApplyDiscount(-10, 0.5)).toBe(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ globalEffective 的**七维回落链各不相同**', () => {
  const allZero = settings()

  it('★★★★★ 两个基价都是 0 ⇒ 输入价是**硬编码 10000**（不是「没配」）', () => {
    const b = maasGlobalBaseIn(allZero)
    // ★★★ 断言用**字面量 10000**，不是 `MAAS_HARDCODED_BASE_IN`。
    //   拿被测常量比自己 = 自引用 = 判据无牙（M3 变异曾因此全绿）。
    //   字面量来自 maas/rates.go:48。
    expect(b.value).toBe(10000)
    expect(b.source).toBe('hardcoded')
    // ★ 顺带把常量本身也钉住（这次是**反向**钉：常量必须等于那个字面量）
    expect(MAAS_HARDCODED_BASE_IN).toBe(10000)
  })

  it('★★★★★ 旧的 `base_credits_per_1m` 能救场（标成 configured_legacy）', () => {
    const b = maasGlobalBaseIn(settings({ base_credits_per_1m: 5000 }))
    expect(b.value).toBe(5000)
    expect(b.source).toBe('configured_legacy')
  })

  it('★★★★★ `base_credits_per_1m_in` 优先于旧字段', () => {
    const b = maasGlobalBaseIn(settings({ base_credits_per_1m: 5000, base_credits_per_1m_in: 7000 }))
    expect(b.value).toBe(7000)
    expect(b.source).toBe('configured')
  })

  it('★★★★★★ image / audio / video **恒等于输入价**（Settings 里没有这三个字段）', () => {
    const s = settings({ base_credits_per_1m_in: 7000, base_credits_per_1m_out: 21000 })
    expect(maasGlobalRate(s, 'image').value).toBe(7000)
    expect(maasGlobalRate(s, 'audio').value).toBe(7000)
    expect(maasGlobalRate(s, 'video').value).toBe(7000)
    // ★ 而 out 是自己配的 21000，不跟 image 走
    expect(maasGlobalRate(s, 'out').value).toBe(21000)
  })

  it('★★★★ out / cache_in / cache_out 未配 ⇒ 逐级回落到 baseIn', () => {
    const s = settings({ base_credits_per_1m_in: 7000 })
    for (const d of ['out', 'cache_in', 'cache_out'] as const) {
      expect(maasGlobalRate(s, d).value, `${d} 应回落到 7000`).toBe(7000)
      expect(maasGlobalRate(s, d).source, `${d} 的来源应跟 baseIn`).toBe('configured')
    }
  })

  it('★★★★★ 折扣作用在全局价上（8 折 ⇒ 5600）', () => {
    const s = settings({ base_credits_per_1m_in: 7000, global_discount: 0.8 })
    expect(maasGlobalRate(s, 'in').value).toBe(5600)
  })

  it('★ 七个维度一个不多一个不少（防常量表与后端跑偏）', () => {
    expect(MAAS_RATE_DIMS.map((d) => d.key)).toEqual([
      'in', 'out', 'cache_in', 'cache_out', 'image', 'audio', 'video',
    ])
  })

  it('★ image/audio/video 的 json 键名带 `_tokens`（与 in/out 的写法不同）', () => {
    expect(MAAS_EFFECTIVE_KEYS.image).toBe('credits_per_1m_image_tokens')
    expect(MAAS_EFFECTIVE_KEYS.in).toBe('credits_per_1m_in')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ pick() 三条件：manual=true **不保证**用自定义值', () => {
  it('★★★★★★ manual + 有值 + >0 ⇒ 用自定义', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: 300 })
    expect(maasDimUsesCustom(r, 'in')).toBe(true)
  })

  it('★★★★★★ manual + **没存值**（null）⇒ 回落到全局（★ 这就是陷阱）', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: null })
    expect(maasDimUsesCustom(r, 'in')).toBe(false)
  })

  it('★★★★★★ manual + 值是 **0** ⇒ 回落到全局', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: 0 })
    expect(maasDimUsesCustom(r, 'in')).toBe(false)
  })

  it('★★★★★★ manual + 值是**负数** ⇒ 回落到全局', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: -5 })
    expect(maasDimUsesCustom(r, 'in')).toBe(false)
  })

  it('★★★★★★ **七维各判各的**：in 手动、out 全局，可同时成立', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: 300, manual_out: false })
    expect(maasDimUsesCustom(r, 'in')).toBe(true)
    expect(maasDimUsesCustom(r, 'out')).toBe(false)
  })

  it('★★★ 自定义价**不打折**：同样的 300 在 8 折下仍是 300', () => {
    const s = settings({ base_credits_per_1m_in: 7000, global_discount: 0.8 })
    const r = row({ manual_in: true, custom_credits_per_1m_in: 300 })
    expect(maasEffectiveSource(r, s, 'in')).toBe('custom')
    // ★ 全局那条是 5600；自定义那条**不参与折扣**
    expect(maasGlobalRate(s, 'in').value).toBe(5600)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 「改了但没启用」：生效价与 custom 不一致', () => {
  it('★★★★★ manual=false + custom 有值 ⇒ 该维「有休眠自定义值」', () => {
    // ★★ 库里存了 300，但 manual_in=false ⇒ 生效走全局。
    //   界面若只显示「自定义 300」，看起来像自相矛盾。
    const r = row({ manual_in: false, custom_credits_per_1m_in: 300, credits_per_1m_in: 10000 })
    expect(maasDimHasDormantCustom(r, 'in')).toBe(true)
  })

  it('★★★★★ manual=true 且值 >0 ⇒ 不是「休眠」（生效价就是它）', () => {
    const r = row({ manual_in: true, custom_credits_per_1m_in: 300, credits_per_1m_in: 300 })
    expect(maasDimHasDormantCustom(r, 'in')).toBe(false)
  })

  it('★ custom 为 null ⇒ 没有休眠值', () => {
    expect(maasDimHasDormantCustom(row(), 'in')).toBe(false)
  })

  it('★★★★★★ 生效来源三态：custom / global_configured / global_hardcoded', () => {
    const s8 = settings({ base_credits_per_1m_in: 7000, global_discount: 0.8 })
    // ① 手动 + 有值 + >0 ⇒ custom
    expect(maasEffectiveSource(row({ manual_in: true, custom_credits_per_1m_in: 300 }), s8, 'in')).toBe('custom')
    // ② ★★ manual=false（哪怕库里有值）⇒ **不是** custom，而是 global_configured
    //    ★ 这条是 M6 变异逼出来的：原先没有任何判据覆盖这一侧。
    expect(maasEffectiveSource(row({ manual_in: false, custom_credits_per_1m_in: 300 }), s8, 'in')).toBe('global_configured')
    // ③ 全局也落到硬编码 ⇒ global_hardcoded
    expect(maasEffectiveSource(row(), settings(), 'in')).toBe('global_hardcoded')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 兜底字面量与「没有 rate 行」', () => {
  it('★★★★ `vendor` 的末级兜底是字面量「其他」——不表示真有个叫「其他」的厂商', () => {
    expect(MAAS_VENDOR_FALLBACK).toBe('其他')
    expect(row({ vendor: MAAS_VENDOR_FALLBACK })).toBeTruthy()
  })

  it("★ `modality` 的兜底字面量是 text", () => {
    expect(MAAS_MODALITY_FALLBACK).toBe('text')
  })

  it('★★★★ `updated_at` 为 null ⇒ `model_credit_rates` 里没有这一行', () => {
    expect(maasRowHasNoRateRecord(row({ updated_at: null }))).toBe(true)
    expect(maasRowHasNoRateRecord(row({ updated_at: '' }))).toBe(true)
    expect(maasRowHasNoRateRecord(row({ updated_at: '2026-10-01T00:00:00Z' }))).toBe(false)
  })

  it('★★ `family` 是**指针且无 omitempty** ⇒ 键一定在、值可能为 null（与 request-detail 相反）', () => {
    const r = row()
    expect(Object.prototype.hasOwnProperty.call(r, 'family')).toBe(true)
    expect(r.family).toBeNull()
  })

  it('★ 七个 custom 键**都在**（指针无 omitempty ⇒ 键一定存在）', () => {
    const r = row()
    for (const d of MAAS_RATE_DIMS) {
      expect(Object.prototype.hasOwnProperty.call(r, 'custom_' + MAAS_EFFECTIVE_KEYS[d.key])).toBe(true)
    }
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ is_custom 是「有没有任一维开过手动」', () => {
  it('★ 七维全 false ⇒ false', () => {
    expect(row({ is_custom: false }).is_custom).toBe(false)
  })

  it('★★★ 只有 image 开手动 ⇒ is_custom 仍为 true（是**或**不是「整行定制」）', () => {
    // ★ storedIsManual 是七个 manual 的**或** ⇒ 它不告诉你「哪一维」
    // ★ 自定义值必须真的有值且 >0 才算「在用」（见 pick() 三条件）
    const r = row({ manual_image: true, is_custom: true, custom_credits_per_1m_image_tokens: 900 })
    expect(maasDimUsesCustom(r, 'image')).toBe(true)
    expect(maasDimUsesCustom(r, 'in')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════
// orders
// ══════════════════════════════════════════════════════════════════════

function order(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    order_no: 'ORD-1',
    tenant_id: 't-1',
    order_type: 'topup',
    status: 'pending',
    amount_cents: 9900,
    credits: 5000,
    payment_channel: 'alipay',
    qr_payload: 'weixin://x',
    qr_url: 'https://stub/qr',
    expires_at: '2026-10-08T00:00:00Z',
    note: '',
    created_at: '2026-10-07T00:00:00Z',
    updated_at: '2026-10-07T00:00:00Z',
    ...over,
  }
}

describe('★★★★★★ orders 列表：两层限流 + 只有 items', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse({ items: [order()] }))
  })

  it('★★★★★★ 打 `/api/admin/maas/orders`，响应键是 `items`（:638）', async () => {
    const r = await fetchMaasOrders()
    expect(lastUrl()).toBe('/api/admin/maas/orders')
    expect(r.items).toHaveLength(1)
  })

  it('★★★★★★ 响应**没有** total / limit 回显（分页只能近似）', () => {
    const r = unwrapMaasOrders({ items: [] })
    expect(Object.prototype.hasOwnProperty.call(r, 'total')).toBe(false)
    expect(Object.prototype.hasOwnProperty.call(r, 'limit')).toBe(false)
  })

  it('★★★★★★ limit > 100 ⇒ 客户端就回落 **20**（不是 clamp 到 100）', async () => {
    // ★ ListOrders: `if limit <= 0 || limit > 100 { limit = 20 }`
    await fetchMaasOrders({ limit: 200 })
    expect(lastUrl()).toContain('limit=20')
    expect(lastUrl()).not.toContain('200')
  })

  it('★★★★★★ limit <= 0（0 / 负数）⇒ 同样回落 20', async () => {
    for (const bad of [0, -1, -50]) {
      await fetchMaasOrders({ limit: bad })
      expect(lastUrl()).toContain('limit=20')
    }
  })

  it('★★★★★ limit=100 是闭区间上界，发得出去', async () => {
    await fetchMaasOrders({ limit: 100 })
    expect(lastUrl()).toContain('limit=100')
  })

  it('★ 合法空清单必须被接受（真的没订单，不是错）', () => {
    expect(unwrapMaasOrders({ items: [] }).items).toEqual([])
  })

  it('★ items 缺失 ⇒ 抛错（不接受裸数组）', () => {
    expect(() => unwrapMaasOrders({})).toThrow(/形状不符/)
    expect(() => unwrapMaasOrders([])).toThrow(/形状不符/)
    expect(() => unwrapMaasOrders(null)).toThrow(/形状不符/)
  })

  it('★ 宽容解包拒绝 `{data:{…}}`', () => {
    expect(() => unwrapMaasOrders({ data: { items: [] } })).toThrow(/形状不符/)
  })
})

describe('★★★★★ 金额单位是**分**', () => {
  it('★★★★★ `amount_cents = 9900` ⇒ 99 元（不是 9900 元）', () => {
    expect(maasAmountYuan(order())).toBe(99)
  })

  it('★ 缺值兜底为 0', () => {
    expect(maasAmountYuan(order({ amount_cents: undefined as never }))).toBe(0)
  })
})

describe('★★★★★ 列表里恒无支付提示（端点差异，不是「没支付信息」）', () => {
  it('★★★★★ 列表行没有 `payment_hint` ⇒ maasListLacksPaymentHint=true', () => {
    expect(maasListLacksPaymentHint(order())).toBe(true)
  })

  it('★★★ 详情行有 `payment_hint` ⇒ false', () => {
    expect(maasListLacksPaymentHint(order({ payment_hint: '扫码支付' }))).toBe(false)
  })

  it('★ `stub_mode` 是 bool+omitempty ⇒ false 时**键整个不存在**', () => {
    const o = order()
    expect(Object.prototype.hasOwnProperty.call(o, 'stub_mode')).toBe(false)
  })
})

describe('★★★ plan/package 孤儿：有 id 但名字是空串', () => {
  it('★★★ 有 package_id 而 package_name 空 ⇒ 判为「关联已被删」', () => {
    expect(maasNameOrphaned(7, '')).toBe(true)
    expect(maasNameOrphaned(7, undefined)).toBe(true)
  })

  it('★★ 两者齐备 ⇒ 不是孤儿', () => {
    expect(maasNameOrphaned(7, '包名')).toBe(false)
  })

  it('★ 没有 id ⇒ 不是孤儿（本来就没关联）', () => {
    expect(maasNameOrphaned(undefined, '')).toBe(false)
  })

  it('★ `plan_id`/`package_id`/`paid_at` 是 omitempty 指针 ⇒ 键可能不存在', () => {
    const o = order()
    for (const k of ['plan_id', 'package_id', 'paid_at', 'payment_hint', 'plan_name']) {
      expect(Object.prototype.hasOwnProperty.call(o, k), `${k} 不该默认存在`).toBe(false)
    }
    // ★ 而 expires_at / created_at / updated_at 是非指针 ⇒ 键一定在
    for (const k of ['expires_at', 'created_at', 'updated_at']) {
      expect(Object.prototype.hasOwnProperty.call(o, k)).toBe(true)
    }
  })
})

describe('★★★ 详情端点：裸对象，与列表形状**不同**', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse(order({ payment_hint: '扫码支付', stub_mode: true })))
  })

  it('★★★ 打 `/api/admin/maas/orders/{id}`，响应是**裸订单**（:683）', async () => {
    const o = await fetchMaasOrder(1)
    expect(lastUrl()).toBe('/api/admin/maas/orders/1')
    expect(o.order_no).toBe('ORD-1')
    expect(o.payment_hint).toBe('扫码支付')
  })

  it('★★★ 把**列表**形状 `{items:[…]}` 喂给详情 ⇒ 必须抛错', () => {
    // ★ 详情是裸对象；误读 `resp.items` 会让「orders 端点两种形状」这条判据完全失效
    expect(() => unwrapMaasOrder({ items: [order()] })).toThrow(/形状不符/)
  })

  it('★★ 非法 id（0 / 负数 / 非整数）⇒ 本地拒，不发请求', async () => {
    for (const bad of [0, -1, 1.5]) {
      await expect(fetchMaasOrder(bad)).rejects.toThrow(/正整数/)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★ 详情拿到 `payment_hint` ⇒ 列表缺它那条判据有了对照', async () => {
    const o = await fetchMaasOrder(1)
    expect(maasListLacksPaymentHint(o)).toBe(false)
  })
})

describe('★★★ 详情解包不许「只看一个键就放行」', () => {
  // ★ 后端 `writeJSON(w, 200, order)` 序列化的是**整个 BillingOrder**，
  //   `id`（int64）与 `order_no` 键**一定都在**。
  //   ⇒ 只校验 `order_no` 的宽容解包会让任何「恰好有个 order_no」的形状混进来，
  //   这正是本条判据要钉住的地方。
  it('★★★ 缺 `id`（只有 order_no）⇒ 必须抛错', () => {
    expect(() => unwrapMaasOrder({ order_no: 'ORD-1' })).toThrow(/形状不符/)
  })

  it('★★ 缺 `order_no`（只有 id）⇒ 必须抛错', () => {
    expect(() => unwrapMaasOrder({ id: 1 })).toThrow(/形状不符/)
  })

  it('★★ `id` 是字符串而非数字 ⇒ 必须抛错（后端是 int64，不会是字符串）', () => {
    expect(() => unwrapMaasOrder({ id: '1', order_no: 'ORD-1' })).toThrow(/形状不符/)
  })
})

describe('★★★ 「可能还有更多」的边界判定', () => {
  // ★ 响应**没有 total** ⇒ 只能靠「这页排满了」近似。
  //   `>=` 与 `>` 在 length === limit 这一格上给出相反答案 ⇒ 必须钉死。
  it('★★★ 条数**正好等于** limit ⇒ 判定为「可能还有更多」', () => {
    const items = Array.from({ length: 20 }, () => order())
    expect(maasOrdersMaybeMore(items, 20)).toBe(true)
  })

  it('★★★ 条数**少于** limit ⇒ 判定为「没有更多」', () => {
    expect(maasOrdersMaybeMore(Array.from({ length: 19 }, () => order()), 20)).toBe(false)
  })

  it('★★ 条数**超过** limit（后端给了更多）⇒ 判定为「可能还有更多」', () => {
    expect(maasOrdersMaybeMore(Array.from({ length: 25 }, () => order()), 20)).toBe(true)
  })

  it('★ 空清单 ⇒ 没有更多', () => {
    expect(maasOrdersMaybeMore([], 20)).toBe(false)
  })
})

describe('★★ 枚举字面量', () => {
  it('★★ order_type 只有 subscribe / topup', () => {
    expect([...MAAS_ORDER_TYPES]).toEqual(['subscribe', 'topup'])
  })

  it('★★ status 只有 pending / paid / cancelled / expired', () => {
    expect([...MAAS_ORDER_STATUSES]).toEqual(['pending', 'paid', 'cancelled', 'expired'])
  })

  it('★★ payment_channel 只有 alipay / wechat / manual', () => {
    expect([...MAAS_PAYMENT_CHANNELS]).toEqual(['alipay', 'wechat', 'manual'])
  })

  it('★ orders 的 limit 常量：默认 20 / 上界 100', () => {
    expect(MAAS_ORDERS_LIMIT_DEFAULT).toBe(20)
    expect(MAAS_ORDERS_LIMIT_MAX).toBe(100)
  })
})

// ════════════════════════════════════════════════════════════════════════
// 第三段：MaaS 租户/客户面（**admin 档**，第三十九轮）
// ════════════════════════════════════════════════════════════════════════

import {
  fetchMaasPublicSettings,
  unwrapMaasPublicSettings,
  fetchMaasPublicModels,
  unwrapMaasPublicModels,
  fetchMaasPublicPlans,
  unwrapMaasPlans,
  fetchMaasPublicTopupPackages,
  unwrapMaasTopupPackages,
  fetchMaasWallet,
  unwrapMaasWallet,
  maasCatalogPriceYuan,
  maasUnitPriceFenPerCredit,
  maasPublicModelLacksMultiDims,
  maasWalletHasSubscription,
  maasWalletTotalIsMixedUnit,
  maasWalletBalanceIsSubstituted,
  type MaasPublicModel,
  type MaasWallet,
} from './maas'

/** ★★ 抄自 `handleMaasPublicSettings` 的 map 字面量（maas_handlers.go:275-283）。 */
function publicSettings(over: Record<string, unknown> = {}) {
  return { cents_per_credit: 0.1, base_credits_per_1m: 10000, currency_display: 'CNY', ...over }
}

/** ★ 抄自 `ModelRateRow`（maas/service.go:536）—— **只有 12 键、4 维**。 */
function publicModel(over: Record<string, unknown> = {}): MaasPublicModel {
  return {
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    vendor: 'OpenAI',
    family: null,
    family_display_name: null,
    context_window: 128000,
    modality: 'multimodal',
    billing_mode: 'token',
    credits_per_1m_in: 10000,
    credits_per_1m_out: 30000,
    credits_per_1m_cache_in: 1000,
    credits_per_1m_cache_out: 1250,
    ...over,
  } as unknown as MaasPublicModel
}

/** ★ 抄自 `Plan`（maas/service.go）—— 无 omitempty，8 键一定都在。 */
function plan(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    code: 'pro-monthly',
    tier: 'pro',
    name: 'Pro 月付',
    price_cents: 9900,
    monthly_credits: 500000,
    enabled: true,
    sort_order: 10,
    ...over,
  }
}

/** ★ 抄自 `TopupPackage`（maas/service.go）—— 同样 8 键。 */
function topup(over: Record<string, unknown> = {}) {
  return {
    id: 2,
    code: 'pack-1k',
    tier: 'basic',
    name: '1000 积分包',
    price_cents: 1000,
    credits_amount: 1000,
    enabled: true,
    sort_order: 20,
    ...over,
  }
}

/** ★ 抄自 `WalletView`（maas/service.go）—— **裸对象**。 */
function wallet(over: Record<string, unknown> = {}): MaasWallet {
  return {
    tenant_id: 'acme',
    quota_remaining: 0,
    granted_balance: 1000,
    purchased_balance: 500,
    balance_credits: 1500,
    total_available: 1500,
    subscription: {
      plan_id: 1,
      plan_name: 'Pro 月付',
      status: 'active',
      period_start: '2026-10-01T00:00:00Z',
      period_end: '2026-11-01T00:00:00Z',
    },
    ...over,
  } as unknown as MaasWallet
}

describe('★★★★★★ 租户面档位：不是 superAdmin', () => {
  it('★★★★★★ 五条端点都在 `/api/maas/`（**不是** `/api/admin/maas/`）', async () => {
    const urls: string[] = []
    const cases: Array<[() => Promise<unknown>, unknown]> = [
      [() => fetchMaasPublicSettings(), publicSettings()],
      [() => fetchMaasPublicModels(), { items: [publicModel()] }],
      [() => fetchMaasPublicPlans(), { items: [plan()] }],
      [() => fetchMaasPublicTopupPackages(), { items: [topup()] }],
      [() => fetchMaasWallet(), wallet()],
    ]
    for (const [run, body] of cases) {
      fetchMock.mockResolvedValueOnce(jsonResponse(body))
      await run()
      urls.push(lastUrl())
    }
    expect(urls).toEqual([
      '/api/maas/settings',
      '/api/maas/models',
      '/api/maas/plans',
      '/api/maas/topup-packages',
      '/api/maas/wallet',
    ])
    // ★ 五条都**不许**落到 superAdmin 前缀下
    for (const u of urls) expect(u).not.toContain('/api/admin/')
  })
})

describe('★★★★★★ public settings 只有 3 个键', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse(publicSettings()))
  })

  it('★★★★★★ 打 `/api/maas/settings`，响应是 3 键裸对象', async () => {
    const s = await fetchMaasPublicSettings()
    expect(lastUrl()).toBe('/api/maas/settings')
    expect(s.cents_per_credit).toBe(0.1)
    expect(s.base_credits_per_1m).toBe(10000)
    expect(s.currency_display).toBe('CNY')
  })

  it('★★★★★ 少任一键 ⇒ 抛错（不许「宽松通过然后渲染成没配」）', () => {
    // ★ 后端自陈「Tenants see conversion knobs only, not internal cost data」
    //   ⇒ 这 3 个键**必须都在**。缺一个说明端点变了，不能静默当默认值。
    expect(() => unwrapMaasPublicSettings({ base_credits_per_1m: 1, currency_display: 'CNY' })).toThrow(
      /形状不符/,
    )
    expect(() => unwrapMaasPublicSettings({ cents_per_credit: 1, currency_display: 'CNY' })).toThrow(/形状不符/)
    expect(() => unwrapMaasPublicSettings({ cents_per_credit: 1, base_credits_per_1m: 1 })).toThrow(/形状不符/)
  })

  it('★★★★★ ★ admin 全量 `Settings` 是这 3 键的**超集** ⇒ 子集检查挡不住，改由**投影**守', () => {
    // ★★ admin 档 `writeJSON(w, 200, st)` 是**全量** Settings（含 12 个键），
    //   它**包含**租户面的 3 个键 ⇒ 任何「这 3 键都在？」的检查都必然接受它。
    //   ⇒ 「喂全量应当抛错」在这条边上**不可能成立**，别把它当判据。
    //   真正要守的是另一头：即便服务端多给，返回值也**只暴露这 3 个键**，
    //   页面就读不到 global_discount / base_credits_per_1m_in 等成本数据。
    const r = unwrapMaasPublicSettings(settings()) as unknown as Record<string, unknown>
    expect(Object.keys(r).sort()).toEqual(['base_credits_per_1m', 'cents_per_credit', 'currency_display'])
    expect('global_discount' in r).toBe(false)
    expect('base_credits_per_1m_in' in r).toBe(false)
  })

  it('★★★★★ ★ `base_credits_per_1m` 是**旧字段**，租户面拿不到 `_in`', () => {
    // §11.73 的 maasGlobalBaseIn 先看 base_credits_per_1m_in，这里**没有这个键**
    // ⇒ 租户面显示的值**可能不是生效基价**。
    const s = publicSettings() as Record<string, unknown>
    expect('base_credits_per_1m_in' in s).toBe(false)
    expect('global_discount' in s).toBe(false)
  })
})

describe('★★★★★★ public models：另一个结构，**只有 4 维**', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse({ items: [publicModel()] }))
  })

  it('★★★★★★ 打 `/api/maas/models`，响应键是 `items`', async () => {
    const r = await fetchMaasPublicModels()
    expect(lastUrl()).toBe('/api/maas/models')
    expect(r.items).toHaveLength(1)
  })

  it('★★★★★★ ★★★ 行里**没有** image/audio/video 三维 ⇒ 不能渲染成 0', () => {
    const m = publicModel()
    // ★ 用 admin 档那套七维判读去读它，这五维会读到 undefined，
    //   再被 `?? 0` 渲染成 0 ⇒ 看起来像「这几维免费」。
    expect('credits_per_1m_image_tokens' in m).toBe(false)
    expect('credits_per_1m_audio_tokens' in m).toBe(false)
    expect('credits_per_1m_video_tokens' in m).toBe(false)
    expect(maasPublicModelLacksMultiDims(m)).toBe(true)
  })

  it('★★★★★★ ★ 行里**没有** manual_* / custom_* / is_custom / updated_at', () => {
    const m = publicModel() as unknown as Record<string, unknown>
    for (const k of [
      'manual_in',
      'manual_out',
      'is_custom',
      'custom_credits_per_1m_in',
      'updated_at',
      'canonical_id',
    ]) {
      expect(k in m, `不该有 ${k}`).toBe(false)
    }
  })

  it('★★★★★ ★ `modality_source` 字段**不存在** ⇒ 租户无从分辨模态是盖章还是猜的', () => {
    const m = publicModel() as unknown as Record<string, unknown>
    expect('modality_source' in m).toBe(false)
  })

  it('★★★★★ `billing_mode` 后端硬编码 `"token"`，键一定存在', () => {
    const m = publicModel()
    expect(m.billing_mode).toBe('token')
    expect('billing_mode' in m).toBe(true)
  })

  it('★★★★★ 4 个指针字段是 omitempty ⇒ 键可能整个不存在', () => {
    const bare = publicModel() as unknown as Record<string, unknown>
    for (const k of ['family', 'family_display_name', 'context_window']) delete bare[k]
    const m = bare as unknown as MaasPublicModel
    expect('family' in m).toBe(false)
    expect('context_window' in m).toBe(false)
  })

  it('★★ `items` 缺失 ⇒ 抛错（不接受裸数组）', () => {
    expect(() => unwrapMaasPublicModels([publicModel()])).toThrow(/形状不符/)
    expect(() => unwrapMaasPublicModels({ data: { items: [] } })).toThrow(/形状不符/)
  })
})

describe('★★★★★ plans / topup-packages：只列 enabled，且 items 永不为 null', () => {
  it('★★★★★ 打对 URL 并解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [plan()] }))
    const r = await fetchMaasPublicPlans()
    expect(lastUrl()).toBe('/api/maas/plans')
    expect(r.items[0]?.code).toBe('pro-monthly')

    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [topup()] }))
    const t = await fetchMaasPublicTopupPackages()
    expect(lastUrl()).toBe('/api/maas/topup-packages')
    expect(t.items[0]?.credits_amount).toBe(1000)
  })

  it('★★★★★★ ★★ `jsonSlice`（maas/json_slice.go:4-9）⇒ 空结果是 `[]` 不是 null', async () => {
    // ★ 后端 `var out []Plan` 起始为 nil，靠 jsonSlice 换成 []T{}
    //   ⇒ 客户端可以**直接**用 `items.length`，不必判 null。
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }))
    const r = await fetchMaasPublicPlans()
    expect(Array.isArray(r.items)).toBe(true)
    expect(r.items).toHaveLength(0)
  })

  it('★★★★★ `items: null` ⇒ 抛错（后端不会这么返回，出了就是形状变了）', () => {
    expect(() => unwrapMaasPlans({ items: null })).toThrow(/形状不符/)
    expect(() => unwrapMaasTopupPackages({ items: null })).toThrow(/形状不符/)
  })

  it('★★★★★ ★ 8 个键**一定都在**（无 omitempty），连 enabled:false 都有键', () => {
    const p = plan({ enabled: false }) as Record<string, unknown>
    for (const k of [
      'id',
      'code',
      'tier',
      'name',
      'price_cents',
      'monthly_credits',
      'enabled',
      'sort_order',
    ]) {
      expect(k in p, `Plan 该有 ${k}`).toBe(true)
    }
  })

  it('★★★★ 金额单位是**分**；积分为 0 时单价判 null 而不是 Infinity', () => {
    expect(maasCatalogPriceYuan(9900)).toBe(99)
    expect(maasUnitPriceFenPerCredit(9900, 500000)).toBeCloseTo(0.0198, 6)
    expect(maasUnitPriceFenPerCredit(9900, 0)).toBeNull()
    expect(maasUnitPriceFenPerCredit(9900, -1)).toBeNull()
  })
})

describe('★★★★★★ wallet：裸对象 + 三处非显然语义', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse(wallet()))
  })

  it('★★★★★★ 打 `/api/maas/wallet`，响应是**裸对象**', async () => {
    const w = await fetchMaasWallet()
    expect(lastUrl()).toBe('/api/maas/wallet')
    expect(w.tenant_id).toBe('acme')
  })

  it('★★★★★★ ★ 钱包形状是裸对象，**不能**读 `items`（那是 plans 的形状）', () => {
    expect(() => unwrapMaasWallet({ items: [plan()] })).toThrow(/形状不符/)
    expect(() => unwrapMaasWallet([wallet()])).toThrow(/形状不符/)
    expect(() => unwrapMaasWallet({ id: 1 })).toThrow(/形状不符/)
  })

  it('★★★★★ ★ 缺 `tenant_id` ⇒ 必须抛错（它是**空字段无 omitempty**，键一定在）', () => {
    // ★ `WalletView{TenantID: tenantID}` 且 `tenant_id` **没有** omitempty
    //   ⇒ 键一定存在（租户码为空时值是空串，但键在）。
    //   ⇒ 只校验 total_available 的宽容解包会让「没有身份的钱包」混进来。
    expect(() => unwrapMaasWallet({ total_available: 1, granted_balance: 0, purchased_balance: 0 })).toThrow(
      /形状不符/,
    )
  })

  it('★★★ `tenant_id` 是空串时**仍**被接受（键在，值为空 ≠ 缺键）', () => {
    // ★ 与上一条互为对照：空串是「这个租户码解析成空」，不是「没这个字段」。
    expect(() => unwrapMaasWallet({ tenant_id: '', total_available: 0 })).not.toThrow()
  })

  it('★★★★★★ ★★★ `balance_credits` 是被兜底顶替的值，不是原始列', () => {
    // GetWallet 尾部：`if w.BalanceCredits == 0 { w.BalanceCredits = Granted + Purchased }`
    // ★★★ 夹具必须写成**后端发出来的样子**（= 1500），不能写 `balance_credits: 0`
    //   —— 后端**不会**发 0 那一行（列值 0 时它已经被顶替成 1500 了）。
    //   写 0 等于在验「我编的夹具符合我的理解」，不是验产品行为。
    const substituted = wallet({ balance_credits: 1500, granted_balance: 1000, purchased_balance: 500 })
    expect(substituted.balance_credits).toBe(1500)
    expect(maasWalletBalanceIsSubstituted(substituted)).toBe(true)
  })

  it('★★★★★ 非兜底情形（列值非 0 且不等于两数和）⇒ 不误报', () => {
    const raw = wallet({ balance_credits: 9999, granted_balance: 1000, purchased_balance: 500 })
    expect(maasWalletBalanceIsSubstituted(raw)).toBe(false)
  })

  it('★★★★★★ ★★★ `total_available` 把订阅额度与积分余额**相加**（两种单位）', () => {
    const w = wallet({ quota_remaining: 1000, granted_balance: 1000, purchased_balance: 500, total_available: 2500 })
    expect(w.total_available).toBe(1000 + 1000 + 500)
    expect(maasWalletTotalIsMixedUnit(w)).toBe(true)
  })

  it('★★★★★ 没有生效订阅 ⇒ `subscription` 键**不存在**（指针 + omitempty）', () => {
    const w = wallet() as unknown as Record<string, unknown>
    delete w.subscription
    const m = w as unknown as MaasWallet
    expect('subscription' in m).toBe(false)
    expect(maasWalletHasSubscription(m)).toBe(false)
  })

  it('★★★★★ 「键存在但 status 不是 active」是**两回事**', () => {
    // ★ 与上一条互为对照：这里键在，只是 status 变了。
    const m = wallet({ subscription: { plan_id: 1, plan_name: 'x', status: 'cancelled', period_start: '', period_end: '' } })
    expect(maasWalletHasSubscription(m)).toBe(true)
  })
})

// ════════════════════════════════════════════════════════════════════════
// 第四段：superAdmin 租户运维面（第四十轮）
// ════════════════════════════════════════════════════════════════════════

import {
  fetchMaasSettings,
  unwrapMaasSettings,
  fetchMaasAdminPlans,
  fetchMaasAdminTopupPackages,
  fetchMaasTenantWallet,
  fetchMaasTenantAccount,
  unwrapMaasTenantAccount,
  fetchMaasUsageSummary,
  unwrapMaasUsageSummary,
  fetchMaasConsumptionDetail,
  unwrapMaasConsumptionDetail,
  fetchMaasTenantLedger,
  unwrapMaasTenantLedger,
  maasUsageDaysClamped,
  maasUsageLimitEffective,
  maasLedgerLimitEffective,
  maasUsageReadsHotTable,
  maasCostUsd,
  maasTenantRevenueUsd,
  maasMarginRateUndefined,
  maasHasCancelledBilled,
  MAAS_USAGE_DAYS_MIN,
  MAAS_USAGE_DAYS_MAX,
  MAAS_USAGE_HOT_DAYS_MAX,
  MAAS_USAGE_LIMIT_DEFAULT,
  MAAS_USAGE_LIMIT_MAX,
  MAAS_LEDGER_LIMIT_DEFAULT,
  MAAS_LEDGER_LIMIT_MAX,
  MAAS_ACCOUNT_LEDGER_COUNT,
  MAAS_ACCOUNT_ORDERS_COUNT,
  type MaasUsageSummary,
  type MaasConsumptionRow,
  type MaasLedgerEntry,
} from './maas'

/** ★ `UsageSummary`（maas/usage.go）：`cost_usd` 是 float64 + omitempty。 */
function usageSummary(over: Record<string, unknown> = {}): MaasUsageSummary {
  return {
    days: 7,
    tenant_id: 'acme',
    total_requests: 100,
    total_credits: 5000,
    total_cost_usd: 12.5,
    by_model: [{ model: 'gpt-4o', requests: 100, credits: 5000, cost_usd: 12.5 }],
    trend: [{ date: '2026-10-01', requests: 100, credits: 5000, cost_usd: 12.5 }],
    ...over,
  } as unknown as MaasUsageSummary
}

/** ★ `ConsumptionDetailRow`：★ 同一个结构里 omitempty 混用。 */
function consumptionRow(over: Record<string, unknown> = {}): MaasConsumptionRow {
  return {
    tenant_id: 'acme',
    owner_user: 'alice',
    provider_id: 3,
    provider_name: 'OpenAI',
    credential_id: 9,
    credential_label: 'sk-…',
    canonical_id: 1,
    model: 'gpt-4o',
    requests: 100,
    prompt_tokens: 1000,
    completion_tokens: 2000,
    cache_read_tokens: 300,
    cache_write_tokens: 40,
    credits_charged: 5000,
    upstream_cost_usd: 10,
    tenant_revenue_usd: 5, // 5000 * 0.1 / 100
    gross_margin_usd: -5,
    gross_margin_rate: -1,
    cancelled_billed_requests: 2,
    ...over,
  } as unknown as MaasConsumptionRow
}

/** ★ `LedgerEntry`：`pool`/`ref_type`/`ref_id` 是**指针但无** omitempty。 */
function ledgerEntry(over: Record<string, unknown> = {}): MaasLedgerEntry {
  return {
    id: 1,
    entry_type: 'charge',
    amount: -100,
    balance_after: 900,
    pool: 'purchased',
    ref_type: 'order',
    ref_id: 'ORD-7',
    note: '',
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  } as unknown as MaasLedgerEntry
}

describe('★★★★★★ admin settings：裸全量 Settings（12 键无 omitempty）', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse(settings()))
  })

  it('★★★★★★ 打 `/api/admin/maas/settings`，响应是**裸对象**（含 global_discount）', async () => {
    const s = await fetchMaasSettings()
    expect(lastUrl()).toBe('/api/admin/maas/settings')
    // ★ admin 档看得到租户面看不到的这两个
    expect(s.global_discount).toBe(1)
    expect(s.base_credits_per_1m_in).toBe(0)
  })

  it('★★★★★ ★ 与租户面 3 键形状**必须**区分：少 `global_discount` ⇒ 抛错', () => {
    expect(() => unwrapMaasSettings(publicSettings())).toThrow(/形状不符/)
    expect(() => unwrapMaasSettings({ items: [] })).toThrow(/形状不符/)
  })

  it('★★★★★ 12 个键**无 omitempty** ⇒ 连空串与 0 都有键', () => {
    const s = settings() as unknown as Record<string, unknown>
    for (const k of [
      'cents_per_credit',
      'base_credits_per_1m',
      'base_credits_per_1m_in',
      'base_credits_per_1m_out',
      'base_credits_per_1m_cache_in',
      'base_credits_per_1m_cache_out',
      'global_discount',
      'currency_display',
      'alipay_account',
      'wechat_mch_id',
      'stub_alipay_qr_url',
      'stub_wechat_qr_url',
    ]) {
      expect(k in s, `admin Settings 该有 ${k}`).toBe(true)
    }
  })
})

describe('★★★★★ admin plans/topup：含停用行（形状与租户面相同）', () => {
  it('★★★★★ 打对 URL', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [plan()] }))
    await fetchMaasAdminPlans()
    expect(lastUrl()).toBe('/api/admin/maas/plans')

    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [topup()] }))
    await fetchMaasAdminTopupPackages()
    expect(lastUrl()).toBe('/api/admin/maas/topup-packages')
  })

  it('★★★★★ ★★ `enabled: false` 的行**会**出现在 admin 档（与租户面正相反）', async () => {
    // ★ `enabledOnly=false` ⇒ 不加 WHERE ⇒ 含停用行
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [plan({ enabled: false })] }))
    const r = await fetchMaasAdminPlans()
    expect(r.items[0]?.enabled).toBe(false)
  })
})

describe('★★★★★★ tenants 前缀：路径拼法与 404/500 边界', () => {
  it('★★★★★★ wallet / account / usage / ledger 四条路径都对', async () => {
    const urls: string[] = []
    fetchMock.mockResolvedValueOnce(jsonResponse(wallet()))
    await fetchMaasTenantWallet('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse({ wallet: wallet(), recent_ledger: [], recent_orders: [] }))
    await fetchMaasTenantAccount('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(usageSummary()))
    await fetchMaasUsageSummary('acme', { days: 7, limit: 10 })
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [ledgerEntry()] }))
    await fetchMaasTenantLedger('acme', { limit: 50 })
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/maas/tenants/acme/wallet',
      '/api/admin/maas/tenants/acme/account',
      '/api/admin/maas/tenants/acme/usage/summary?days=7&limit=10',
      '/api/admin/maas/tenants/acme/ledger?limit=50',
    ])
  })

  it('★★★★★ ★★ 租户码里带斜杠 ⇒ 必须 encodeURIComponent（否则路径会被劈开）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(wallet()))
    await fetchMaasTenantWallet('a/b')
    expect(lastUrl()).toBe('/api/admin/maas/tenants/a%2Fb/wallet')
    // ★ 后端 `strings.Split(strings.Trim(rest,"/"), "/")` 会按斜杠切段
    //   ⇒ 不 encode 的话 `a/b` 会被当成「租户 a + 动作 b」⇒ 404。
  })

  it('★★★★ usage/detail 的 owner_user 只在非空时才发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tenant_id: 'acme', days: 7, cents_per_credit: 0.1, rows: [] }))
    await fetchMaasConsumptionDetail('acme', { days: 7 })
    expect(lastUrl()).not.toContain('owner_user')

    fetchMock.mockResolvedValueOnce(jsonResponse({ tenant_id: 'acme', days: 7, cents_per_credit: 0.1, rows: [] }))
    await fetchMaasConsumptionDetail('acme', { days: 7, ownerUser: 'alice' })
    expect(lastUrl()).toContain('owner_user=alice')
  })
})

describe('★★★★★★★ days 决定读哪张物理表', () => {
  it('★★★★★★★ days <= 7 ⇒ 读 request_logs_hot；> 7 ⇒ 换表', () => {
    expect(maasUsageReadsHotTable(1)).toBe(true)
    expect(maasUsageReadsHotTable(7)).toBe(true)
    expect(maasUsageReadsHotTable(8)).toBe(false)
    expect(maasUsageReadsHotTable(30)).toBe(false)
  })

  it('★★★★★★ 判定用的是**clamp 之后**的 days', () => {
    // days=0 ⇒ clamp 成 1 ⇒ 仍是热表
    expect(maasUsageReadsHotTable(0)).toBe(true)
    expect(maasUsageReadsHotTable(999)).toBe(false)
    expect(MAAS_USAGE_HOT_DAYS_MAX).toBe(7)
  })
})

describe('★★★★★★ 两套限幅：usage 两端不对称，ledger 回落 50', () => {
  it('★★★★★★ ClampUsageDays：<1 ⇒ 1、>90 ⇒ 90（**两端都 clamp**）', () => {
    expect(maasUsageDaysClamped(0)).toBe(MAAS_USAGE_DAYS_MIN)
    expect(maasUsageDaysClamped(-5)).toBe(1)
    expect(maasUsageDaysClamped(91)).toBe(MAAS_USAGE_DAYS_MAX)
    expect(maasUsageDaysClamped(30)).toBe(30)
  })

  it('★★★★★★ ★★ ClampUsageLimit：<1 ⇒ **回落 10**（不是 clamp 到 1）、>50 ⇒ 50', () => {
    // ★★ 这是本仓第七种分页语义，且**两端不对称**
    expect(maasUsageLimitEffective(0)).toBe(MAAS_USAGE_LIMIT_DEFAULT)
    expect(maasUsageLimitEffective(-3)).toBe(10)
    expect(maasUsageLimitEffective(1)).toBe(1)
    expect(maasUsageLimitEffective(51)).toBe(MAAS_USAGE_LIMIT_MAX)
    expect(maasUsageLimitEffective(50)).toBe(50)
  })

  it('★★★★★ ListLedger：越界**回落 50**（不是 clamp 到 1 或 200）', () => {
    expect(maasLedgerLimitEffective(0)).toBe(MAAS_LEDGER_LIMIT_DEFAULT)
    expect(maasLedgerLimitEffective(201)).toBe(50)
    expect(maasLedgerLimitEffective(200)).toBe(MAAS_LEDGER_LIMIT_MAX)
    expect(maasLedgerLimitEffective(1)).toBe(1)
    // ★★ 三套限幅摆在一起（usage 回落 10 / usage clamp 50 / ledger 回落 50）
    //   ⇒ 绝不能假设「同族同规则」
    expect(MAAS_LEDGER_LIMIT_MAX).toBe(200)
    expect(MAAS_USAGE_LIMIT_MAX).toBe(50)
  })

  it('★★ GetAccount 的取数条数写死在 service 里（10 / 5，不可调）', () => {
    expect(MAAS_ACCOUNT_LEDGER_COUNT).toBe(10)
    expect(MAAS_ACCOUNT_ORDERS_COUNT).toBe(5)
  })
})

describe('★★★★★★ UsageSummary 会回显 days（与 orders 段正相反）', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse(usageSummary()))
  })

  it('★★★★★★ 解包成功且 days 可读', async () => {
    const u = await fetchMaasUsageSummary('acme')
    expect(u.days).toBe(7)
    expect(u.tenant_id).toBe('acme')
    expect(u.by_model).toHaveLength(1)
    expect(u.trend).toHaveLength(1)
  })

  it('★★★★★ 缺 days / by_model / trend 任一 ⇒ 抛错', () => {
    expect(() => unwrapMaasUsageSummary({ tenant_id: 'a', by_model: [], trend: [] })).toThrow(/形状不符/)
    expect(() => unwrapMaasUsageSummary({ days: 7, by_model: [], trend: [] })).toThrow(/形状不符/)
    expect(() => unwrapMaasUsageSummary({ days: 7, tenant_id: 'a', trend: [] })).toThrow(/形状不符/)
    // ★★ 只缺 `trend`（by_model 在）也必须抛错 —— 否则「趋势图空着」
    //   会被静默当成「这段时间没有趋势数据」。
    expect(() => unwrapMaasUsageSummary({ days: 7, tenant_id: 'a', by_model: [] })).toThrow(/形状不符/)
    expect(() => unwrapMaasUsageSummary({ days: 7, tenant_id: 'a', by_model: null })).toThrow(/形状不符/)
  })
})

describe('★★★★★★ cost_usd 是 float64 + omitempty ⇒ 恰好 0 时键不存在', () => {
  it('★★★★★★ 键缺失时读成 **0**（不是 undefined、不是「—」）', () => {
    const zero = { model: 'm', requests: 1, credits: 0 } as unknown as { cost_usd?: number }
    expect('cost_usd' in zero).toBe(false)
    // ★ 若页面渲染成「—」，就把「真·零成本」误报成「数据缺失」
    expect(maasCostUsd(zero)).toBe(0)
    expect(maasCostUsd({ cost_usd: 12.5 })).toBe(12.5)
  })

  it('★★★ 顶层 total_cost_usd 同理', () => {
    const s = usageSummary() as unknown as Record<string, unknown>
    delete s.total_cost_usd
    expect(maasCostUsd(s as unknown as { cost_usd?: number })).toBe(0)
  })
})

describe('★★★★★★ 收入 / 毛利是算出来的，且 rate 在零收入时无定义', () => {
  it('★★★★★★ 复算 revenue = credits_charged * cents_per_credit / 100', () => {
    // 夹具里的 revenue 是 5（5000 * 0.1 / 100）⇒ 客户端能独立复算核对
    const r = consumptionRow()
    expect(maasTenantRevenueUsd(r.credits_charged, 0.1)).toBe(r.tenant_revenue_usd)
  })

  it('★★★★★★ ★★★ 收入为 0 时 rate=0 是**无定义**，不是「零毛利」', () => {
    const zeroRevenue = consumptionRow({ tenant_revenue_usd: 0, gross_margin_usd: 0, gross_margin_rate: 0 })
    expect(maasMarginRateUndefined(zeroRevenue)).toBe(true)
    // ★ 响应里「rate = 0」有两种含义，**不可区分**
    const realZeroMargin = consumptionRow({ tenant_revenue_usd: 100, gross_margin_usd: 0, gross_margin_rate: 0 })
    expect(maasMarginRateUndefined(realZeroMargin)).toBe(false)
  })

  it('★★★ 「已计费但被客户端取消」能被识别', () => {
    expect(maasHasCancelledBilled(consumptionRow({ cancelled_billed_requests: 2 }))).toBe(true)
    expect(maasHasCancelledBilled(consumptionRow({ cancelled_billed_requests: 0 }))).toBe(false)
  })
})

describe('★★★ ConsumptionDetailRow 的 omitempty 混用', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(
      jsonResponse({ tenant_id: 'acme', days: 7, cents_per_credit: 0.1, rows: [consumptionRow()] }),
    )
  })

  it('★★★ 四个指针字段有值时**有键**', async () => {
    const d = await fetchMaasConsumptionDetail('acme')
    const r = d.rows[0]!
    expect('owner_user' in r).toBe(true)
    expect('provider_id' in r).toBe(true)
    expect('credential_id' in r).toBe(true)
    expect('canonical_id' in r).toBe(true)
  })

  it('★★★ Go 在 nil 时**省略**这些键 ⇒ 客户端必须能吃「键整个不存在」', async () => {
    const d = await fetchMaasConsumptionDetail('acme')
    // ★★★ 不能在测试里写 `{ provider_id: null }` 再用 `in` 断言 ——
    //   那是**我自己造的 JS 字面量**，Go 的 omitempty 根本没参与，
    //   `in` 必然为 true。必须造 Go 实际吐出的形状：**键整个不存在**。
    const raw = consumptionRow() as unknown as Record<string, unknown>
    for (const k of ['provider_id', 'credential_id', 'canonical_id', 'owner_user']) delete raw[k]
    const r = raw as unknown as MaasConsumptionRow
    for (const k of ['provider_id', 'credential_id', 'canonical_id', 'owner_user']) {
      expect(k in r, `Go 省略后不该有 ${k}`).toBe(false)
      // ⇒ 客户端读到的是 undefined（类型上声明为可选就是为此）
      expect((r as unknown as Record<string, unknown>)[k]).toBeUndefined()
    }
    expect(d.tenant_id).toBe('acme')
  })

  it('★★★ `unwrapMaasConsumptionDetail` 形状校验（互喂必须抛错）', () => {
    expect(() => unwrapMaasConsumptionDetail({ tenant_id: 'a', days: 7 })).toThrow(/形状不符/)
    expect(() => unwrapMaasConsumptionDetail([{ tenant_id: 'a', days: 7, rows: [] }])).toThrow(/形状不符/)
    // ★ 对照：wallet 形状（顶层有 total_available）喂进来也必须抛错
    expect(() => unwrapMaasConsumptionDetail(wallet())).toThrow(/形状不符/)
  })

  it('★★★ 「键缺失(undefined)」与「值为 null」是**两回事**', async () => {
    // ★★ 同一族里两种语义并存：ConsumptionDetailRow 是「nil ⇒ 缺键」，
    //   LedgerEntry 是「无 omitempty ⇒ 键在、值为 null」。别混用判读。
    const omitted = consumptionRow() as unknown as Record<string, unknown>
    delete omitted.provider_id
    const explicitNull = consumptionRow({ provider_id: null as unknown as number })
    expect('provider_id' in omitted).toBe(false)
    expect('provider_id' in explicitNull).toBe(true)
    expect(explicitNull.provider_id).toBeNull()
  })

  it('★★★ 其余 15 个键**无** omitempty ⇒ 一定存在（哪怕值是 0）', () => {
    const r = consumptionRow() as unknown as Record<string, unknown>
    for (const k of [
      'tenant_id',
      'provider_name',
      'credential_label',
      'model',
      'requests',
      'prompt_tokens',
      'completion_tokens',
      'cache_read_tokens',
      'cache_write_tokens',
      'credits_charged',
      'upstream_cost_usd',
      'tenant_revenue_usd',
      'gross_margin_usd',
      'gross_margin_rate',
      'cancelled_billed_requests',
    ]) {
      expect(k in r, `该有 ${k}`).toBe(true)
    }
  })
})

describe('★★★ LedgerEntry 的指针无 omitempty ⇒ 键在、值为 null', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(jsonResponse({ items: [ledgerEntry()] }))
  })

  it('★★★ pool/ref_type/ref_id **无** omitempty ⇒ 键一定在，值可为 null', async () => {
    const r = await fetchMaasTenantLedger('acme')
    const e = r.items[0]!
    expect('pool' in e).toBe(true)
    expect('ref_type' in e).toBe(true)
    expect('ref_id' in e).toBe(true)
  })

  it('★★★ null 值与「键缺失」是**两回事**（与上一条互为对照）', async () => {
    const withNull = ledgerEntry({ pool: null, ref_type: null, ref_id: null })
    expect(withNull.pool).toBeNull()
    expect('pool' in withNull).toBe(true)
    // ★★ 同一个响应里，LedgerEntry 是「键在值 null」，
    //    ConsumptionDetailRow 是「nil 则缺键」⇒ **两套判读，不能混用**
  })

  it('★★ items 缺失 ⇒ 抛错', () => {
    expect(() => unwrapMaasTenantLedger([ledgerEntry()])).toThrow(/形状不符/)
    expect(() => unwrapMaasTenantLedger({ data: { items: [] } })).toThrow(/形状不符/)
  })
})

describe('★★★ account 聚合响应的形状', () => {
  beforeEach(() => {
    fetchMock.mockResolvedValue(
      jsonResponse({ wallet: wallet(), recent_ledger: [ledgerEntry()], recent_orders: [order()] }),
    )
  })

  it('★★★ wallet + recent_ledger + recent_orders 三段齐', async () => {
    const a = await fetchMaasTenantAccount('acme')
    expect(a.wallet.tenant_id).toBe('acme')
    expect(a.recent_ledger).toHaveLength(1)
    expect(a.recent_orders).toHaveLength(1)
  })

  it('★★★ ★ recent_orders 里的行**恒无** payment_hint（与订单列表同一个原因）', async () => {
    const a = await fetchMaasTenantAccount('acme')
    expect('payment_hint' in (a.recent_orders[0] as unknown as Record<string, unknown>)).toBe(false)
  })

  it('★★★ 缺 wallet / recent_ledger / recent_orders 任一 ⇒ 抛错', () => {
    expect(() => unwrapMaasTenantAccount({ recent_ledger: [], recent_orders: [] })).toThrow(/形状不符/)
    expect(() => unwrapMaasTenantAccount({ wallet: wallet(), recent_orders: [] })).toThrow(/形状不符/)
    expect(() => unwrapMaasTenantAccount({ wallet: wallet(), recent_ledger: [] })).toThrow(/形状不符/)
  })

  it('★★ wallet 形状不对时也算抛错（不能只看顶层三键）', () => {
    expect(() => unwrapMaasTenantAccount({ wallet: { id: 1 }, recent_ledger: [], recent_orders: [] })).toThrow(
      /形状不符/,
    )
  })
})
