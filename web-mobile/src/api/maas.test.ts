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
