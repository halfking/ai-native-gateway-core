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
