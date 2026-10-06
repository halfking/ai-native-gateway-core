// maas.ts — MaaS（Model-as-a-Service）管理面的只读端点。
//   GET /api/admin/maas/model-rates   每个 canonical 模型的**生效**积分价
//
// 鉴权：`admin/maas_handlers.go:18` 是 `h.superAdmin(...)` ⇒ **superAdmin 档**。
//   ★ 这一族**全部 11 条**注册都是 superAdmin（maas_handlers.go:14-24），
//     其中已有一条 superAdmin 抽屉席在先。
//
// ⚠️⚠️⚠️⚠️ 本端点的字段名**全部**会骗人。响应里 7 个 `credits_per_1m_*`
// **不是「库里存的值」**，而是**算出来的生效价**。
//
// (1) ★★★★★★ 每一维**独立**回落，判据是三条件与运算：
//     `effectiveModelRates`（maas/rates.go:96-102）里的
//         pick := func(manual bool, val *int64, fallback int64) int64 {
//             if manual && val != nil && *val > 0 { return *val }
//             return fallback        // ← global
//         }
//     ⇒ `manual_X = true` **不保证**用的是自定义值：
//       `val == nil`（没存）或 `*val <= 0` 时，**照样回落到 global**。
//     ⇒ 而 in / out / cache_in / cache_out / image / audio / video
//       **七维各判各的** —— 同一行可能是「输入走自定义、输出走全局」。
//
// (2) ★★★★★ 折扣**只作用于全局价，不作用于自定义价**，而且两个口径不同：
//     · `normalizeDiscount`（rates.go:28-33）：`d <= 0 || d > 1 ⇒ 返回 1`
//       ⇒ ★★ `global_discount = 0` 的含义是「**不打折**」，**不是「全免」**。
//         同理 `1.5` 也被归一成 1。
//     · `applyDiscount`（rates.go:36-41）：`base <= 0 ⇒ 0`，
//       否则 `int64(math.Ceil(float64(base) * disc))` ⇒ **向上取整**。
//     · 而 `pick()` 命中手动分支时返回的是 **`*val` 原值，不打折**。
//     ⇒ 同一个 300 的自定义价，在打八折时生效价仍是 300；
//       而全局 400 打八折后是 320（不是 300.8 也不是 400）。
//
// (2b) ★★★★ `globalEffective` 的回落链**七维不同**（rates.go:42-70）：
//       baseIn = base_credits_per_1m_in ?? base_credits_per_1m ?? **10000**
//       out     = base_credits_per_1m_out     ?? baseIn
//       cacheIn = base_credits_per_1m_cache_in ?? baseIn
//       cacheOut= base_credits_per_1m_cache_out?? baseIn
//       ★ image / audio / video = **baseIn** —— `Settings` 里**根本没有**这三个字段
//         （`BaseRateSet` 注释自陈「多模态默认取输入价，简单文本模型不受影响」）。
//       最后七维**全部** × discount（Ceil）。
//
// (2c) ★★★★ 上一条里的「10000」是**硬编码字面量**，不是「没配」：
//         baseIn := st.BaseCreditsPer1MIn
//         if baseIn <= 0 { baseIn = st.BaseCreditsPer1M }
//         if baseIn <= 0 { baseIn = 10000 }        // ← 不是「没配」，是 10000
//     ⇒ 详见上面的 2b/2c。
//
// (3) ★★★★ `custom_credits_per_1m_*` **不是**「自定义值 vs 生效值」的对，
//     它是 **stored 的原样拷贝**（model_rates.go:130-136：`r.CustomIn = stored.In` …）。
//     ⇒ 于是完全可能出现「**生效价 = 全局 10000**、**自定义值 = 300**」
//       —— 因为 `manual_in = false` 时生效走 global，而 stored 里的 300 照样被吐出来。
//     ★ 页面**必须**说明「改了但没启用」，否则看起来像数据自相矛盾。
//
// (4) ★★★ `is_custom` 是七个 `manual_*` 的**或**
//     （`storedIsManual`，rates.go:119-122）⇒ 它的含义是
//     「**有没有任何一维**开过手动」，**不是**「这一行整体被定制过」。
//     ⇒ `is_custom = true` 时**可能七维里只有一维**生效。
//
// (5) ★★★ `vendor` 是**三级回落**（model_rates.go:77）：
//         COALESCE(NULLIF(TRIM(mf.vendor), ''), NULLIF(TRIM(mc.family), ''), '其他')
//     ⇒ 字面量 **'其他' 是兜底值**，不表示真有个叫「其他」的厂商。
//     ★ 而 `mf` 的 LEFT JOIN 条件里带
//       `AND COALESCE(mf.status,'active') = 'active'`
//       ⇒ **family 被停用时这层回落整层失效**，直接掉到 `mc.family` / '其他'。
//     同理 `display_name` 回落 `canonical_name`、`modality` 回落字面量 `'text'`。
//
// (6) ★★ **只列 active 模型**：`WHERE COALESCE(mc.status,'active') = 'active'`
//     ⇒ inactive / 已下架的 canonical model 在这个列表里**完全不可见**。
//     清单看着不完整，不代表库里没有。
//
// (7) ★★ **没有分页**：SQL 里**没有 LIMIT**，只有 `ORDER BY mc.canonical_name`。
//     ⇒ 「共 N 个」只能数 `items.length`，且**没有后端总数**可比对。
//
// (8) ★★ 指针字段**没有** `omitempty`，所以**键一定存在、值可能为 `null`**：
//     `family`（`*string`）与 `updated_at`（`*time.Time`）。
//     ★★ 这与 request-detail 那一族的 omitempty **正好相反**。
//     ⇒ `updated_at` 为 null 的行 = `model_credit_rates` 里**没有对应行**（LEFT JOIN 未命中）。
//
// (9) ★ `s.Enabled()` 为假 ⇒ **503 `database not configured`**
//     （maas_handlers.go:85-88）⇒ 是「MaaS 没开/没接库」，不是「没有数据」。
//
// (10) ★★ 错误信封：`writeError` ⇒ `{"error":{"detail":"…"}}`（error.detail 族）；
//     `writeInternalErr(w, "internal error (see server logs)", err)` 是另一条文案。
//
// ★ 写操作本页一律不碰：settings(PUT)、model-rates 的 POST/PUT/DELETE、
//   batch / batch-reset / batch-fill-global、plans、topup-packages、orders。

import { req, type RequestOptions } from './client'

// ── 形状（逐字抄自 maas/model_rates.go 与 maas/service.go） ────────────────

/** ★ `Settings`（maas/service.go:60-73）。 */
export interface MaasSettings {
  cents_per_credit: number
  /** ★ ★ 见坑 2：两个都是 0 时生效价是**硬编码 10000**。 */
  base_credits_per_1m: number
  base_credits_per_1m_in: number
  base_credits_per_1m_out: number
  base_credits_per_1m_cache_in: number
  base_credits_per_1m_cache_out: number
  global_discount: number
  currency_display: string
  alipay_account: string
  wechat_mch_id: string
  stub_alipay_qr_url: string
  stub_wechat_qr_url: string
}

/** ★ `vendor` 的**末级兜底**字面量（model_rates.go:77）。 */
export const MAAS_VENDOR_FALLBACK = '其他'

/** ★ `modality` 的兜底字面量（model_rates.go:79）。 */
export const MAAS_MODALITY_FALLBACK = 'text'

/** ★ `globalEffective` 末位的**硬编码**基价（rates.go:48）。 */
export const MAAS_HARDCODED_BASE_IN = 10000

/** 七个价位的 json 后缀（7 个维度各自独立回落，见坑 1）。 */
export const MAAS_RATE_DIMS = [
  { key: 'in', label: '输入' },
  { key: 'out', label: '输出' },
  { key: 'cache_in', label: '缓存读' },
  { key: 'cache_out', label: '缓存写' },
  { key: 'image', label: '图像' },
  { key: 'audio', label: '音频' },
  { key: 'video', label: '视频' },
] as const

export type MaasRateDim = (typeof MAAS_RATE_DIMS)[number]['key']

/**
 * ★ 生效价的来源。**`hardcoded` 不是一个「没配」**，而是字面量 10000。
 * - `configured`       —— 该维度自己的设置项有值
 * - `configured_legacy`—— 回落到旧字段 `base_credits_per_1m`
 * - `hardcoded`        —— 回落到**硬编码 10000**
 */
export type MaasRateSource = 'configured' | 'configured_legacy' | 'hardcoded'

/** ★ `AdminModelRateRow`（model_rates.go:11-36）。 */
export interface MaasModelRateRow {
  canonical_id: number
  canonical_name: string
  /** ★ 回落 canonical_name。 */
  display_name: string
  /** ★ 三级回落，末级是字面量 '其他'。见坑 5。 */
  vendor: string
  /** ★★ 指针**无 omitempty** ⇒ 键一定存在，值可能为 `null`。见坑 8。 */
  family: string | null
  /** ★ 兜底字面量 'text'。 */
  modality: string
  status: string
  /** ★★ 这 7 个是**生效价**（算出来的），不是库里的值。见坑 1。 */
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  credits_per_1m_image_tokens: number
  credits_per_1m_audio_tokens: number
  credits_per_1m_video_tokens: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
  manual_image: boolean
  manual_audio: boolean
  manual_video: boolean
  /** ★★ 「有没有任何一维开过手动」，不是「整行被定制」。见坑 4。 */
  is_custom: boolean
  /** ★★ stored 的**原样拷贝**，不是「覆盖值 vs 生效值」。见坑 3。 */
  custom_credits_per_1m_in: number | null
  custom_credits_per_1m_out: number | null
  custom_credits_per_1m_cache_in: number | null
  custom_credits_per_1m_cache_out: number | null
  custom_credits_per_1m_image_tokens: number | null
  custom_credits_per_1m_audio_tokens: number | null
  custom_credits_per_1m_video_tokens: number | null
  /** ★★ 指针**无 omitempty**；为 `null` = 压根没有 rate 行。 */
  updated_at: string | null
}

/** ★ `AdminModelRatesResponse` = `{settings, items}`（model_rates.go:39-42）。 */
export interface MaasModelRatesResponse {
  settings: MaasSettings
  items: MaasModelRateRow[]
}

// ── 取数 ──────────────────────────────────────────────────────────────────

export function fetchMaasModelRates(options?: RequestOptions): Promise<MaasModelRatesResponse> {
  return req<unknown>('GET', '/api/admin/maas/model-rates', undefined, options).then(unwrapMaasModelRates)
}

/**
 * ★ 后端 `writeJSON(w, 200, resp)`（maas_handlers.go:96）传的是
 *   `AdminModelRatesResponse` ⇒ 响应键是 **`settings` + `items`**，两个都要在。
 * 形状不符**抛错**，绝不当成「没有配价」。
 */
export function unwrapMaasModelRates(resp: unknown): MaasModelRatesResponse {
  if (
    resp &&
    typeof resp === 'object' &&
    Array.isArray((resp as MaasModelRatesResponse).items) &&
    (resp as MaasModelRatesResponse).settings &&
    typeof (resp as MaasModelRatesResponse).settings === 'object'
  ) {
    return resp as MaasModelRatesResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/model-rates 响应形状不符：期望 {settings:{…}, items:[…]}，实得 ${actual}`)
}

// ── 页面侧的判读工具 ────────────────────────────────────────────────────

/** 七个「生效价」字段的读取器（注意 image/audio/video 的键名带 `_tokens`）。 */
export const MAAS_EFFECTIVE_KEYS = {
  in: 'credits_per_1m_in',
  out: 'credits_per_1m_out',
  cache_in: 'credits_per_1m_cache_in',
  cache_out: 'credits_per_1m_cache_out',
  image: 'credits_per_1m_image_tokens',
  audio: 'credits_per_1m_audio_tokens',
  video: 'credits_per_1m_video_tokens',
} as const

export const MAAS_CUSTOM_KEYS = {
  in: 'custom_credits_per_1m_in',
  out: 'custom_credits_per_1m_out',
  cache_in: 'custom_credits_per_1m_cache_in',
  cache_out: 'custom_credits_per_1m_cache_out',
  image: 'custom_credits_per_1m_image_tokens',
  audio: 'custom_credits_per_1m_audio_tokens',
  video: 'custom_credits_per_1m_video_tokens',
} as const

export const MAAS_MANUAL_KEYS = {
  in: 'manual_in',
  out: 'manual_out',
  cache_in: 'manual_cache_in',
  cache_out: 'manual_cache_out',
  image: 'manual_image',
  audio: 'manual_audio',
  video: 'manual_video',
} as const

/**
 * ★★★ **这一维**到底有没有在用自定义价。
 *
 * 复刻 `pick()` 的三条件（rates.go:97-101）：`manual && val != nil && *val > 0`。
 * ⇒ ★ `manual_` 为 true **仍可能返回 false**（没存值 / 存的是 0 或负数）。
 * ⇒ 页面据此显示「已开手动但实际没生效」，而不是笼统的「手动/自动」。
 */
export function maasDimUsesCustom(row: MaasModelRateRow, dim: MaasRateDim): boolean {
  const manual = (row as unknown as Record<string, unknown>)[MAAS_MANUAL_KEYS[dim]]
  const custom = (row as unknown as Record<string, number | null>)[MAAS_CUSTOM_KEYS[dim]]
  return manual === true && typeof custom === 'number' && custom > 0
}

/**
 * ★★ 「改了但没启用」的判据：库里有值、生效却不是它。
 * 出现这一格时界面**必须**说明，否则看起来像自相矛盾（见坑 3）。
 */
export function maasDimHasDormantCustom(row: MaasModelRateRow, dim: MaasRateDim): boolean {
  const custom = (row as unknown as Record<string, number | null>)[MAAS_CUSTOM_KEYS[dim]]
  if (typeof custom !== 'number') return false
  return (row as unknown as Record<string, unknown>)[MAAS_EFFECTIVE_KEYS[dim]] !== custom
}

/** ★ `updated_at` 为 `null` ⇒ `model_credit_rates` 里**没有这一行**。 */
export function maasRowHasNoRateRecord(row: MaasModelRateRow): boolean {
  return row.updated_at === null || row.updated_at === ''
}

/**
 * ★★ 全局输入基价的三段回落（rates.go:42-49）**在客户端复算一遍**，
 * 让页面能说出「这个 10000 是配置来的还是硬编码兜底的」。
 * 注意 `out` 只回落到 `baseIn`（两级），不回落 `base_credits_per_1m`。
 */
export function maasGlobalBaseIn(s: MaasSettings): { value: number; source: MaasRateSource } {
  if (s.base_credits_per_1m_in > 0) return { value: s.base_credits_per_1m_in, source: 'configured' }
  if (s.base_credits_per_1m > 0) return { value: s.base_credits_per_1m, source: 'configured_legacy' }
  return { value: MAAS_HARDCODED_BASE_IN, source: 'hardcoded' }
}

/**
 * ★★ `normalizeDiscount`（rates.go:28-33）的客户端复刻。
 * `d <= 0 || d > 1 ⇒ 1`。
 * ⇒ ★★ `global_discount = 0` 的真实含义是「**不打折**」，**不是「全免」**；
 *   配成 `1.5`（150%）也等价于不打折，而且**响应里不会告诉你**。
 */
export function maasEffectiveDiscount(s: MaasSettings): number {
  const d = s.global_discount
  if (typeof d !== 'number' || Number.isNaN(d) || d <= 0 || d > 1) return 1
  return d
}

/** ★ `applyDiscount`（rates.go:36-41）的客户端复刻：`base<=0 ⇒ 0`，否则 `ceil(base*disc)`。 */
export function maasApplyDiscount(base: number, disc: number): number {
  if (base <= 0) return 0
  return Math.ceil(base * disc)
}

/**
 * ★★ `globalEffective`（rates.go:42-70）的**逐维**客户端复刻。
 *
 * ★ image / audio / video **没有**自己的 settings 字段，直接取 `baseIn`。
 * ★ 折扣只在这一层施加；**自定义价不走这里**（见 `maasDimUsesCustom`）。
 */
export function maasGlobalRate(s: MaasSettings, dim: MaasRateDim): { value: number; source: MaasRateSource } {
  const disc = maasEffectiveDiscount(s)
  const baseInSource = maasGlobalBaseIn(s).source
  const baseIn = maasGlobalBaseIn(s).value

  const pickOne = (raw: number): { value: number; source: MaasRateSource } =>
    raw > 0
      ? { value: maasApplyDiscount(raw, disc), source: 'configured' }
      : { value: maasApplyDiscount(baseIn, disc), source: baseInSource }

  switch (dim) {
    case 'in':
      return { value: maasApplyDiscount(baseIn, disc), source: baseInSource }
    case 'out':
      return pickOne(s.base_credits_per_1m_out)
    case 'cache_in':
      return pickOne(s.base_credits_per_1m_cache_in)
    case 'cache_out':
      return pickOne(s.base_credits_per_1m_cache_out)
    // ★★ image/audio/video：Settings 里没有这三个字段 ⇒ 恒取 baseIn
    case 'image':
    case 'audio':
    case 'video':
      return { value: maasApplyDiscount(baseIn, disc), source: baseInSource }
  }
}

/**
 * ★★ 某一维的生效价到底**从哪来**。
 * - `custom`            —— 命中手动且值 > 0 ⇒ **原值，不打折**
 * - `global_configured` —— 走了全局，且全局价是配出来的
 * - `global_hardcoded`  —— 走了全局，且全局价来自**硬编码 10000**
 *
 * ⇒ 页面据此说明「这个 10000 是硬编码兜底还是你们配的」。
 */
/** ★★ `maasEffectiveSource` 的三个返回值（视图用 `maas.src_` + 它拼键）。 */
export const MAAS_EFFECTIVE_SOURCES = ['custom', 'global_configured', 'global_hardcoded'] as const
export type MaasEffectiveSource = (typeof MAAS_EFFECTIVE_SOURCES)[number]

export function maasEffectiveSource(
  row: MaasModelRateRow,
  settings: MaasSettings,
  dim: MaasRateDim,
): 'custom' | 'global_configured' | 'global_hardcoded' {
  if (maasDimUsesCustom(row, dim)) return 'custom'
  return maasGlobalRate(settings, dim).source === 'hardcoded' ? 'global_hardcoded' : 'global_configured'
}

// ══════════════════════════════════════════════════════════════════════
// orders —— 账单订单（本文件第一批只读端点是 model-rates）
//
// (11) ★★★★★★ `limit` 有**两层**限流，语义完全不同：
//     · handler 层（maas_handlers.go:627-632）是**裸 `strconv.Atoi`**，
//       **不校验**：0 / -5 / 99999 / "abc" 都能原样传下去。
//     · 真正生效的是 `ListOrders` 里的第二层（orders.go:222-224）：
//           if limit <= 0 || limit > 100 { limit = 20 }
//       ⇒ **有效范围 1..100，越界回落 20**（**不是 clamp**，本仓第六种语义）。
//     ★ handler 的默认 50 只在 1..100 内才有意义；
//       发 `limit=200` 得到的是 **20 条**，不是 200 也不是 100。
//
// (12) ★★★★ admin 侧传的是 `ListOrders(ctx, "", limit)` ⇒ **tenantID 为空**
//     ⇒ 走 else 分支（orders.go:241-252），**不按租户过滤**。
//     超级管理员看到的是**全部租户**的订单 —— 这是设计如此，不是泄漏。
//
// (13) ★★★★★ `payment_hint` / `stub_mode` 在**列表里恒不出现**。
//     `enrichOrderPaymentHint`（orders.go:284）**只在 `GetOrder`（:216）里调**，
//     `ListOrders` 那一圈**没有调**。
//     ⇒ 列表响应里 `payment_hint` 缺键、`stub_mode` 为 false 因 omitempty 也缺键。
//     ★ 所以「列表看不到支付提示」是**端点差异**，不是「这单没有支付信息」。
//     ★★ 两个字段都是 omitempty ⇒ **键可能整个不存在**。
//
// (14) ★★★ `plan_name` / `package_name` 是 `COALESCE(sp.name,'')` 的结果
//     ⇒ LEFT JOIN 未命中时是**空字符串**（不是 null、不是「未知」）。
//     ★ 而 `plan_id` / `package_id` 是 `*int` + omitempty ⇒ 键可能整个不存在。
//     ⇒ 「有 package_id 但 package_name 是空」是**正常**组合（套餐被删了）。
//
// (15) ★★★ `amount_cents` 的单位是**分**，不是元。
//     换算靠 settings 里的 `cents_per_credit`，但那是**积分**的单价，不是金额显示。
//
// (16) ★★ 响应**只有 `items`**：没有 `total`、没有 `limit` 回显
//     ⇒ 分页只能是**近似**的（数返回条数 + 看是否排满）。
//
// (17) ★★ 详情端点 `GetOrder` 出错时**一律 404**
//     （`writeError(w, 404, "order not found")`，不区分「不存在」与「查询失败」）。
//
// (18) ★ 错误信封：`writeError` ⇒ `{"error":{"detail":"…"}}`。
//
// ★★ 写操作 `POST /orders/{id}/confirm` 本页**不碰**。

/** ★ `OrderType` 两个值（maas/orders.go:18-20）。 */
export const MAAS_ORDER_TYPES = ['subscribe', 'topup'] as const
export type MaasOrderType = (typeof MAAS_ORDER_TYPES)[number]

/** ★ `OrderStatus` 四个值（maas/orders.go:26-30）。 */
export const MAAS_ORDER_STATUSES = ['pending', 'paid', 'cancelled', 'expired'] as const
export type MaasOrderStatus = (typeof MAAS_ORDER_STATUSES)[number]

/** ★ `PaymentChannel` 三个值（maas/payment.go:12-14）。 */
export const MAAS_PAYMENT_CHANNELS = ['alipay', 'wechat', 'manual'] as const
export type MaasPaymentChannel = (typeof MAAS_PAYMENT_CHANNELS)[number]

/** ★ `ListOrders` 的有效范围 1..100，越界回落 20（orders.go:222-224）。 */
export const MAAS_ORDERS_LIMIT_DEFAULT = 20
export const MAAS_ORDERS_LIMIT_MAX = 100

/** ★ `BillingOrder`（maas/orders.go:33-55）。 */
export interface MaasOrder {
  id: number
  order_no: string
  tenant_id: string
  order_type: string
  status: string
  /** ★ 单位是**分**。 */
  amount_cents: number
  credits: number
  /** ★ 指针 + omitempty ⇒ 键可能整个不存在。 */
  plan_id?: number
  package_id?: number
  payment_channel: string
  qr_payload: string
  qr_url: string
  /** ★ 指针 + omitempty。 */
  paid_at?: string
  /** ★ 非指针 ⇒ 键一定存在。 */
  expires_at: string
  note: string
  created_at: string
  updated_at: string
  /** ★★ **列表里恒不出现**（enrich 只在详情调）。见坑 13。 */
  payment_hint?: string
  /** ★★ 同上；且 bool + omitempty ⇒ false 时连键都没有。 */
  stub_mode?: boolean
  /** ★ COALESCE 出来的**空字符串**表示 LEFT JOIN 未命中。 */
  plan_name?: string
  package_name?: string
}

/** ★ 列表响应**只有 `items`**（maas_handlers.go:638）。 */
export interface MaasOrdersResponse {
  items: MaasOrder[]
}

export function fetchMaasOrders(
  params: { limit?: number } = {},
  options?: RequestOptions,
): Promise<MaasOrdersResponse> {
  const qs = new URLSearchParams()
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    // ★ 与 ListOrders 同规则：越界**回落 20**，宁可不发会被后端改写的值
    const eff = n < 1 || n > MAAS_ORDERS_LIMIT_MAX ? MAAS_ORDERS_LIMIT_DEFAULT : n
    qs.set('limit', String(eff))
  }
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/maas/orders${s ? '?' + s : ''}`, undefined, options).then(
    unwrapMaasOrders,
  )
}

/** ★ `writeJSON(w, 200, map[string]any{"items": items})`（maas_handlers.go:638）。 */
export function unwrapMaasOrders(resp: unknown): MaasOrdersResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as MaasOrdersResponse).items)) {
    return resp as MaasOrdersResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/orders 响应形状不符：期望 {items:[…]}，实得 ${actual}`)
}

/**
 * 订单详情。★ 后端 `writeJSON(w, 200, order)`（maas_handlers.go:683）传的是
 * **裸对象**，**没有** `items` 也没有任何包装键。
 * ★ 与列表的响应形状**不同**（列表是 `{items:[…]}`，详情是裸对象）。
 */
export function fetchMaasOrder(id: number, options?: RequestOptions): Promise<MaasOrder> {
  if (!Number.isInteger(id) || id <= 0) {
    // ★ 与后端 `ParseInt` + `id <= 0 ⇒ 400` 同规则
    return Promise.reject(new Error(`maas/orders/${id}：order id 必须是正整数`))
  }
  return req<unknown>('GET', `/api/admin/maas/orders/${id}`, undefined, options).then(unwrapMaasOrder)
}

export function unwrapMaasOrder(resp: unknown): MaasOrder {
  // ★ 裸对象：**不能**去读 `resp.items`（那是列表的形状）
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const o = resp as MaasOrder
    if (typeof o.order_no === 'string' && typeof o.id === 'number') {
      return o
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/orders/{id} 响应形状不符：期望裸订单对象 {id, order_no, …}，实得 ${actual}`)
}

// ── 页面侧判读 ─────────────────────────────────────────────────────────

/** ★ 金额单位是**分** ⇒ 显示时换算（不是乘 `cents_per_credit`，那是积分单价）。 */
export function maasAmountYuan(o: MaasOrder): number {
  return (o.amount_cents ?? 0) / 100
}

/**
 * ★★ 列表里**恒**没有支付提示（enrich 只在详情调）。
 * 返回 true 时页面要说「这是端点差异，去详情页看」，而不是「这单没有支付信息」。
 */
export function maasListLacksPaymentHint(o: MaasOrder): boolean {
  return o.payment_hint === undefined
}

/** ★ 「有 id 但名字是空串」= 关联的套餐/包**已被删**（LEFT JOIN 未命中）。 */
export function maasNameOrphaned(id: number | undefined, name: string | undefined): boolean {
  return typeof id === 'number' && (!name || name === '')
}

/** ★ 列表响应没有 total ⇒ 只能按「这页排满」近似判断还有没有下一页。 */
export function maasOrdersMaybeMore(items: MaasOrder[], limit: number): boolean {
  return items.length >= limit
}

// ════════════════════════════════════════════════════════════════════════
// 第三段：MaaS **租户/客户面**（admin 档，**不是** superAdmin）
// ════════════════════════════════════════════════════════════════════════
//
// ★★★ 这些端点**不在** `/api/admin/maas/` 下，鉴权是 `h.admin(...)`
//   （maas_handlers.go:26-30）⇒ **admin 档**，抽屉席**不设** requiresRole。
//   ★ 与上面两段（全部 superAdmin）是**同一族但不同档** —— 档位必须分清，
//     混起来就会出现「tenant_admin 403」的接线事故。
//
// (19) ★★★★★★ `/api/maas/settings` **只有 3 个键**：
//     {cents_per_credit, base_credits_per_1m, currency_display}
//     后端自陈「Tenants see conversion knobs only, not internal cost data」
//     ⇒ 租户**看不到** base_credits_per_1m_in/out、global_discount 等成本数据。
//     ★★ 这与 admin 档 `/api/admin/maas/settings`（裸 `Settings`，**全量**）
//       **键名有重叠但形状不同** ⇒ 两条解包必须独立，且互喂必须抛错。
//     ★★★ 而且 `base_credits_per_1m` 是**旧字段**：
//       §11.73 的 `maasGlobalBaseIn` 会先看 `base_credits_per_1m_in`，
//       这里租户面**只有** legacy 那一个 ⇒ 它显示的值**可能不是生效基价**。
//
// (20) ★★★★★ `/api/maas/models` 的行结构是 **`ModelRateRow`（service.go:536）**，
//     与 admin 档的 **`AdminModelRateRow`（model_rates.go:11-36）是两个不同结构**：
//     - 只有 **12 个键**（4 个指针字段带 omitempty ⇒ 可能缺键）；
//     - **只有 4 维**（in / out / cache_in / cache_out），
//       **没有** credits_per_1m_image / audio / video；
//     - **没有** manual_* / custom_* / is_custom / updated_at。
//     ⇒ ★★★ 拿 admin 档那套七维判读去读它，五个维度会**读到 undefined**
//       然后被渲染成 0，看起来像「这几维免费」。
//
// (21) ★★★★ 模态是**盖章 / 猜的**两态，但**盖章这件事被抹掉了**：
//     后端 `catalog.EffectiveModality(name, stored, modality_source)`（display.go:220）
//     —— `modality_source` 是 semantic/manual 时**原样返回 stored**（按名字猜的
//     没资格推翻它）；否则先放行 {multimodal,vision,audio,embedding,video}，
//     **再**按名字猜，最后才回 stored / 'text'。
//     ★★ 后果：一个 `modality='text'` 且**未盖章**的 gemini-* 模型会被报成 **multimodal**
//       （stored='text' 不在放行名单里 ⇒ 掉进按名推断分支）。这正是该函数注释
//       警告的「把『核实判负降级成 text』与『运维手工设成 text』双双翻回 multimodal」。
//     ★★★ 而 `ModelRateRow` **没有** modality_source 字段
//       ⇒ 租户**无从分辨**这个模态是盖过章的库值还是猜出来的。
//       ⇒ 页面**不许**说「这就是配置的模态」。
//
// (22) ★★★★ `vendor` 也与 admin 档**不同源**：public 走 `catalog.ResolveVendor`
//     （display.go:118）：dbVendor → familyVendor 映射 → **按名字推断** →
//     HumanizeFamilyID → 字面量「其他」。★ 比 admin 档**多一层「按名字推断」**。
//
// (23) ★★★★★★ `/api/maas/wallet` 是**裸对象**（无包装键），且：
//     - `tenantID = GetTenantID(r)` ⇒ **只看本租户**（与 superAdmin 侧
//       `ListOrders(ctx, "", ...)` 的跨租户语义**正好相反**）；
//     - ★★★ **GET 会写库**：`GetWallet` 第一行就是 `ensureWalletDirect`
//       （`INSERT INTO tenant_credit_wallets … ON CONFLICT DO NOTHING`）
//       ⇒ 这是一个「看着只读、实际会建行」的端点。
//     - ★★★ `balance_credits` **不是原始列**：`if w.BalanceCredits == 0 { w.BalanceCredits = Granted + Purchased }`
//       ⇒ 列值是 0 时会被两个余额之和**顶替**，客户端**不得**把它当原始列读。
//     - ★★★ `total_available = quota_remaining + granted + purchased` ——
//       它把**订阅额度**（quota）和**积分余额**（两种不同单位）**相加**。
//     - ★ `subscription` 是 `*SubscriptionView` + omitempty ⇒ 没有生效订阅时**键不存在**。
//
// (24) ★★★ `/api/maas/plans` 与 `/api/maas/topup-packages` 走的是
//     `ListPlans(ctx, enabledOnly=true)` / `ListTopupPackages(ctx, enabledOnly=true)`
//     ⇒ **只列 enabled = TRUE**（admin 档那两个是 `false` ⇒ 含停用行）。
//     ★★ 两者返回 `jsonSlice(out)`（maas/json_slice.go:4-9），
//       nil 切片被换成 `[]T{}` ⇒ **`items` 永远是数组，永不为 null**。
//     ★ `Plan` / `TopupPackage` 是**无 omitempty** 的普通结构
//       ⇒ 8 个键**一定都在**（连 `enabled: false` 都有键）。

/** ★★ 租户面 `/api/maas/settings` 的响应（maas_handlers.go:275-283）。 */
export interface MaasPublicSettings {
  cents_per_credit: number
  /** ★★ 这是**旧字段**；租户面拿不到 `base_credits_per_1m_in`。 */
  base_credits_per_1m: number
  currency_display: string
}

export function fetchMaasPublicSettings(options?: RequestOptions): Promise<MaasPublicSettings> {
  return req<unknown>('GET', '/api/maas/settings', undefined, options).then(unwrapMaasPublicSettings)
}

/**
 * ★★ 只有 3 个键。**必须**与 admin 档的裸 `Settings`（全量）区分开。
 *
 * ★★ 顺带说明为什么**不能**靠「喂全量 Settings 应当抛错」来守这条：
 *   admin 的 `Settings`（12 键）是租户面这 3 键的**超集**
 *   ⇒ 任何子集形状检查都**必然**接受它，抛错在这条边上不可能成立。
 *   真正要守的是另一头：即便服务端多给了字段，返回值也**只投影这 3 个键**，
 *   于是页面读不到 `global_discount` / `base_credits_per_1m_in` 等成本数据。
 */
export function unwrapMaasPublicSettings(resp: unknown): MaasPublicSettings {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const s = resp as Record<string, unknown>
    if (
      typeof s.cents_per_credit === 'number' &&
      typeof s.base_credits_per_1m === 'number' &&
      typeof s.currency_display === 'string'
    ) {
      // ★ 显式投影：多出来的键一律**不带出去**（租户面看不到内部成本数据）
      return {
        cents_per_credit: s.cents_per_credit,
        base_credits_per_1m: s.base_credits_per_1m,
        currency_display: s.currency_display,
      }
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/public settings 响应形状不符：期望 {cents_per_credit, base_credits_per_1m, currency_display}，实得 ${actual}`)
}

/** ★ 租户面模型行：`ModelRateRow`（maas/service.go:536），**只有 4 维**。 */
export interface MaasPublicModel {
  canonical_name: string
  display_name: string
  vendor: string
  /** ★ 以下 4 个是 `*T` + omitempty ⇒ 键可能整个不存在。 */
  family?: string
  family_display_name?: string
  context_window?: number
  modality: string
  /** ★ 后端硬编码字面量 `"token"`，**永远不会**是别的值。 */
  billing_mode: string
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
}

export interface MaasPublicModelsResponse {
  items: MaasPublicModel[]
}

export function fetchMaasPublicModels(options?: RequestOptions): Promise<MaasPublicModelsResponse> {
  return req<unknown>('GET', '/api/maas/models', undefined, options).then(unwrapMaasPublicModels)
}

export function unwrapMaasPublicModels(resp: unknown): MaasPublicModelsResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as MaasPublicModelsResponse).items)) {
    return resp as MaasPublicModelsResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/public models 响应形状不符：期望 {items:[…]}，实得 ${actual}`)
}

/** ★ `Plan`（maas/service.go）—— 8 个键**一定都在**（无 omitempty）。 */
export interface MaasPlan {
  id: number
  code: string
  tier: string
  name: string
  /** ★ 单位是**分**。 */
  price_cents: number
  monthly_credits: number
  enabled: boolean
  sort_order: number
}

/** ★ `TopupPackage`（maas/service.go）—— 同样 8 键、无 omitempty。 */
export interface MaasTopupPackage {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  credits_amount: number
  enabled: boolean
  sort_order: number
}

export interface MaasItemsResponse<T> {
  items: T[]
}

export function fetchMaasPublicPlans(options?: RequestOptions): Promise<MaasItemsResponse<MaasPlan>> {
  return req<unknown>('GET', '/api/maas/plans', undefined, options).then(
    unwrapMaasPlans as (r: unknown) => MaasItemsResponse<MaasPlan>,
  )
}

/** ★ `writeJSON(w, 200, map[string]any{"items": items})`（maas_handlers.go:236）。 */
export function unwrapMaasPlans(resp: unknown): MaasItemsResponse<MaasPlan> {
  if (resp && typeof resp === 'object' && Array.isArray((resp as MaasItemsResponse<MaasPlan>).items)) {
    return resp as MaasItemsResponse<MaasPlan>
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/public plans 响应形状不符：期望 {items:[…]}，实得 ${actual}`)
}

export function fetchMaasPublicTopupPackages(
  options?: RequestOptions,
): Promise<MaasItemsResponse<MaasTopupPackage>> {
  return req<unknown>('GET', '/api/maas/topup-packages', undefined, options).then(
    unwrapMaasTopupPackages as (r: unknown) => MaasItemsResponse<MaasTopupPackage>,
  )
}

export function unwrapMaasTopupPackages(resp: unknown): MaasItemsResponse<MaasTopupPackage> {
  if (
    resp &&
    typeof resp === 'object' &&
    Array.isArray((resp as MaasItemsResponse<MaasTopupPackage>).items)
  ) {
    return resp as MaasItemsResponse<MaasTopupPackage>
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/public topup-packages 响应形状不符：期望 {items:[…]}，实得 ${actual}`)
}

/** ★ `SubscriptionView`（maas/service.go）—— 5 键，键一定都在。 */
export interface MaasSubscription {
  plan_id: number
  plan_name: string
  status: string
  period_start: string
  period_end: string
}

/** ★ `WalletView`（maas/service.go）—— **裸对象**，`subscription` 可能缺键。 */
export interface MaasWallet {
  tenant_id: string
  quota_remaining: number
  granted_balance: number
  purchased_balance: number
  /** ★★ 不是原始列：列值为 0 时被 granted+purchased 顶替。 */
  balance_credits: number
  /** ★★ = quota + granted + purchased（**两种单位相加**）。 */
  total_available: number
  subscription?: MaasSubscription
}

export function fetchMaasWallet(options?: RequestOptions): Promise<MaasWallet> {
  return req<unknown>('GET', '/api/maas/wallet', undefined, options).then(unwrapMaasWallet)
}

export function unwrapMaasWallet(resp: unknown): MaasWallet {
  // ★ 裸对象：**不能**去读 `resp.items`（那是 plans/topup 的形状）
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const w = resp as Record<string, unknown>
    if (typeof w.tenant_id === 'string' && typeof w.total_available === 'number') {
      return w as unknown as MaasWallet
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/public wallet 响应形状不符：期望裸钱包对象 {tenant_id, total_available, …}，实得 ${actual}`)
}

// ── 页面侧判读 ─────────────────────────────────────────────────────────

/**
 * ★★★ 租户面 `/api/maas/models` 的**全部 4 个维度**。
 * 响应里**没有** image / audio / video 的键 —— 那不是 0，是**根本不存在**。
 * 导出到 API 模块是为了让 i18n 动态键判据 import **真常量**而不是手抄后缀。
 */
export const MAAS_PUBLIC_DIMS = ['in', 'out', 'cache_in', 'cache_out'] as const
export type MaasPublicDim = (typeof MAAS_PUBLIC_DIMS)[number]

/** ★ 维 → 响应字段的映射（`noUncheckedIndexedAccess` 下索引要显式兜底）。 */
export const MAAS_PUBLIC_DIM_FIELDS = {
  in: 'credits_per_1m_in',
  out: 'credits_per_1m_out',
  cache_in: 'credits_per_1m_cache_in',
  cache_out: 'credits_per_1m_cache_out',
} as const satisfies Record<MaasPublicDim, keyof MaasPublicModel>

export function maasPublicDimValue(m: MaasPublicModel, dim: MaasPublicDim): number {
  const v = m[MAAS_PUBLIC_DIM_FIELDS[dim]]
  return typeof v === 'number' ? v : 0
}

/** ★ 金额单位是**分**（套餐/充值包通用）。 */
export function maasCatalogPriceYuan(priceCents: number): number {
  return (priceCents ?? 0) / 100
}

/** ★★ 单价 = 金额 / 积分。**分**与积分相除得到「分/积分」，页面要么 /100 要么明说单位。 */
export function maasUnitPriceFenPerCredit(priceCents: number, credits: number): number | null {
  if (!Number.isFinite(priceCents) || !Number.isFinite(credits) || credits <= 0) return null
  return priceCents / credits
}

/**
 * ★★★ 租户面模型行**没有** image/audio/video 三维的键。
 * 客户端若沿用 admin 档那套七维判读，会读到 undefined 并渲染成 0
 * ⇒ 「这一维免费」是**假的**。本函数明确告诉页面：这三维**不在这条端点上**。
 */
export function maasPublicModelLacksMultiDims(m: MaasPublicModel): boolean {
  return !('credits_per_1m_image_tokens' in m)
}

/**
 * ★★ 没有生效订阅 ⇒ `subscription` 键**不存在**（指针 + omitempty）。
 * 与「有订阅但 status 不是 active」是两回事，后者键存在但要按 status 读。
 */
export function maasWalletHasSubscription(w: MaasWallet): boolean {
  return w.subscription !== undefined
}

/** ★★ `total_available` 把订阅额度与积分余额**相加**（两种单位），页面必须标注。 */
export function maasWalletTotalIsMixedUnit(w: MaasWallet): boolean {
  return w.quota_remaining !== 0
}

/**
 * ★★★ 复算 `balance_credits` 的兜底规则（GetWallet 尾部）：
 * `if w.BalanceCredits == 0 { w.BalanceCredits = Granted + Purchased }`
 * 返回 true 表示「这一栏不是列里的原始值，是被兜底顶替过的」。
 */
export function maasWalletBalanceIsSubstituted(w: MaasWallet): boolean {
  return w.balance_credits === w.granted_balance + w.purchased_balance && w.balance_credits !== 0
}

// ════════════════════════════════════════════════════════════════════════
// 第四段：MaaS **superAdmin 租户运维面**（第三+四十轮）
// ════════════════════════════════════════════════════════════════════════
//
// GET /api/admin/maas/settings
// GET /api/admin/maas/plans
// GET /api/admin/maas/topup-packages
// GET /api/admin/maas/tenants/{code}/wallet
// GET /api/admin/maas/tenants/{code}/account
// GET /api/admin/maas/tenants/{code}/usage/summary?days=&limit=
// GET /api/admin/maas/tenants/{code}/usage/detail?owner_user=&days=
// GET /api/admin/maas/tenants/{code}/ledger?limit=
//
// ★★ 全部 `h.superAdmin(...)`（maas_handlers.go:14-24）⇒ 抽屉席须设
//   requiresRole: 'super_admin'。**别**照抄第三段那 5 条 admin 席的写法。
//
// (25) ★★★★★★ ★★ 这两个 usage 端点**按 days 换物理表**：
//     `requestLogsSource(days)`（maas/usage.go）：
//         days <= 7 ⇒ "request_logs_hot AS r"
//         days >  7 ⇒ "request_logs_with_current_month AS r"
//     ⇒ days=7 与 days=8 **读的不是同一张表** —— 这不是「窗口更长」，
//       是**换了数据源**（保留期/新鲜度都可能不同）。
//     页面必须把「本次读的是哪张表」显式标出来。
//
// (26) ★★★★★ 两个端点的**天数/条数**各有一套限幅：
//     `ClampUsageDays`：<1 ⇒ 1、>90 ⇒ 90（**两端都 clamp**）
//     `ClampUsageLimit`：<1 ⇒ **10**、>50 ⇒ 50（★ **两端不对称**：
//         下界是**回落 10** 而不是 clamp 到 1）
//     ⇒ 第七种分页语义。本仓已确认七种，别再假设同族一致。
//
// (27) ★★★★★ `UsageSummary` **回显 `days` / `tenant_id`**（`out.Days = days`
//     在 clamp **之后**赋值）⇒ 客户端**能**读回生效值。
//     ★ 与 orders 段（`{items}` 无任何回显）正好相反，这里反而该用回显做校验。
//
// (28) ★★★★ `total_cost_usd` / `cost_usd` 是 **float64 + omitempty**
//     ⇒ **成本恰好为 0 时键整个不存在**。
//     ⇒ 页面读不到 `cost_usd` 时**必须**显示 0，而不是「—」（那会把
//       「真·零成本」误报成「数据缺失」）。
//
// (29) ★★★★ `gross_margin_rate` 只在 `revenue != 0` 时才算
//     （`if row.TenantRevenueUSD != 0 { … }`），否则保持零值 **0**。
//     ⇒ ★★★ 响应里「rate = 0」有**两种**含义：真的是零毛利，
//       **或者**根本没收入（此时 rate **无定义**），两者**不可区分**。
//
// (30) ★★★★ 收入/毛利是**算出来的**：
//     `revenue = credits_charged * cents_per_credit / 100`
//     `margin  = revenue - upstream_cost_usd`
//     而 `cents_per_credit` 直接取自 `maas_settings WHERE id = 1`
//     ⇒ 详情响应**回显** `cents_per_credit`，客户端可**独立复算**核对。
//
// (31) ★★★ `ConsumptionDetailRow` **同一个结构里 omitempty 混用**：
//     `owner_user` / `provider_id` / `credential_id` / `canonical_id` 带 omitempty
//     （指针 ⇒ 仅 nil 时省略）；其余 15 个键**无** omitempty ⇒ 一定存在。
//     ★★ 而 `LedgerEntry` 的 `pool` / `ref_type` / `ref_id` 是
//     **指针但无** omitempty ⇒ 键一定在，值为 `null`。
//     ⇒ **三种「键缺失」语义并存**，不能一套判读通吃。
//
// (32) ★★★ `cancelled_billed_requests` 是 SQL `FILTER` 出来的计数：
//     已扣积分 **且** 流被中断 **且** 失败码 ∈ {client_cancel, client_disconnected}
//     ⇒ 「已计费但被客户端取消」的请求数，是收入侧的已知漏点，页面要点破。
//
// (33) ★★★ `ListLedger`：`if limit <= 0 || limit > 200 { limit = 50 }`
//     ⇒ 有效 1..200，越界**回落 50**；handler 默认也是 50。
//
// (34) ★★★ 三个 query 参数的解析都是
//     `if n, err := strconv.Atoi(v); err == nil { x = n }`
//     ⇒ **解析失败静默沿用默认值**（days=7 / limit=10 / ledger limit=50），
//     **不是 400**。发 `days=abc` 不会报错，会得到一份 7 天的数据。
//
// (35) ★★ `tenant_id` 为空 ⇒ service 直接 `fmt.Errorf("tenant_id required")`
//     ⇒ handler 走 `writeInternalErr` ⇒ **500**，不是 400。
//     ★ 而 `/tenants/` 后面**少于两段路径**时是 **404 `not found`**。
//
// (36) ★ admin 档的 plans / topup 传 `enabledOnly=false` ⇒ **含停用行**，
//     与第三段租户面（`true` ⇒ 只列 enabled）形成对照，
//     **响应形状完全相同**（都是 `{items}`）—— 差异只在**内容**。

/** ★ `ClampUsageDays`：<1 ⇒ 1、>90 ⇒ 90（usage.go）。 */
export const MAAS_USAGE_DAYS_MIN = 1
export const MAAS_USAGE_DAYS_MAX = 90

/** ★★ `days <= 7` 读 `request_logs_hot`，`> 7` 换另一张表（usage.go）。 */
export const MAAS_USAGE_HOT_DAYS_MAX = 7

/** ★★ `ClampUsageLimit`：<1 ⇒ **回落 10**、>50 ⇒ clamp 50（**两端不对称**）。 */
export const MAAS_USAGE_LIMIT_DEFAULT = 10
export const MAAS_USAGE_LIMIT_MAX = 50

/** ★ `ListLedger`：<1 或 >200 ⇒ **回落 50**。 */
export const MAAS_LEDGER_LIMIT_DEFAULT = 50
export const MAAS_LEDGER_LIMIT_MAX = 200

/** ★ handler 侧的默认值（解析失败时静默沿用它们，不是 400）。 */
export const MAAS_USAGE_DAYS_DEFAULT = 7

/** ★ `GetAccount` 里写死的一次取数条数（service 层，**不可调**）。 */
export const MAAS_ACCOUNT_LEDGER_COUNT = 10
export const MAAS_ACCOUNT_ORDERS_COUNT = 5

/** ★★ admin 档 `writeJSON(w, 200, st)`：裸 `Settings`，**12 键、无 omitempty**。 */
export function fetchMaasSettings(options?: RequestOptions): Promise<MaasSettings> {
  return req<unknown>('GET', '/api/admin/maas/settings', undefined, options).then(unwrapMaasSettings)
}

export function unwrapMaasSettings(resp: unknown): MaasSettings {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const s = resp as Record<string, unknown>
    // ★ 12 个键**无 omitempty** ⇒ 键一定都在（含空串与 0）
    if (typeof s.cents_per_credit === 'number' && typeof s.global_discount === 'number' && typeof s.currency_display === 'string') {
      return s as unknown as MaasSettings
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/admin settings 响应形状不符：期望裸 Settings（含 global_discount 等 12 键），实得 ${actual}`)
}

export interface MaasAdminPlansResponse {
  items: MaasPlan[]
}

/** ★ `enabledOnly=false` ⇒ **含停用行**；形状与租户面 `{items}` 完全相同。 */
export function fetchMaasAdminPlans(options?: RequestOptions): Promise<MaasAdminPlansResponse> {
  return req<unknown>('GET', '/api/admin/maas/plans', undefined, options).then(
    unwrapMaasPlans as (r: unknown) => MaasAdminPlansResponse,
  )
}

export interface MaasAdminTopupResponse {
  items: MaasTopupPackage[]
}

export function fetchMaasAdminTopupPackages(options?: RequestOptions): Promise<MaasAdminTopupResponse> {
  return req<unknown>('GET', '/api/admin/maas/topup-packages', undefined, options).then(
    unwrapMaasTopupPackages as (r: unknown) => MaasAdminTopupResponse,
  )
}

function tenantsBase(tenantCode: string, action: string): string {
  // ★ 与 orders 段同一条纪律：租户码原样拼进路径（后端 `strings.Trim` 后取 parts[0]）
  return `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/${action}`
}

/** ★ 与租户面**同形状、同 handler**，但这里 tenantCode 由调用方给（可跨租户）。 */
export function fetchMaasTenantWallet(
  tenantCode: string,
  options?: RequestOptions,
): Promise<MaasWallet> {
  return req<unknown>('GET', tenantsBase(tenantCode, 'wallet'), undefined, options).then(unwrapMaasWallet)
}

export interface MaasLedgerEntry {
  id: number
  entry_type: string
  amount: number
  balance_after: number
  /** ★ 指针但**无** omitempty ⇒ 键一定在，值可能为 `null`。 */
  pool: string | null
  ref_type: string | null
  ref_id: string | null
  note: string
  created_at: string
}

export interface MaasLedgerResponse {
  items: MaasLedgerEntry[]
}

/** ★ `GetAccount` = wallet + 最近 10 条流水 + 最近 5 条订单（条数写死在 service 里）。 */
export interface MaasTenantAccount {
  wallet: MaasWallet
  recent_ledger: MaasLedgerEntry[]
  recent_orders: MaasOrder[]
}

export function fetchMaasTenantAccount(
  tenantCode: string,
  options?: RequestOptions,
): Promise<MaasTenantAccount> {
  return req<unknown>('GET', tenantsBase(tenantCode, 'account'), undefined, options).then(
    unwrapMaasTenantAccount,
  )
}

export function unwrapMaasTenantAccount(resp: unknown): MaasTenantAccount {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const a = resp as Record<string, unknown>
    const w = a.wallet as Record<string, unknown> | undefined
    if (
      w &&
      typeof w === 'object' &&
      typeof w.tenant_id === 'string' &&
      Array.isArray(a.recent_ledger) &&
      Array.isArray(a.recent_orders)
    ) {
      return a as unknown as MaasTenantAccount
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/tenants/account 响应形状不符：期望 {wallet, recent_ledger, recent_orders}，实得 ${actual}`)
}

export interface MaasUsageModelRow {
  model: string
  requests: number
  credits: number
  /** ★ float64 + omitempty ⇒ **恰好 0 时键不存在**。 */
  cost_usd?: number
}

export interface MaasUsageTrendRow {
  date: string
  requests: number
  credits: number
  cost_usd?: number
}

export interface MaasUsageSummary {
  /** ★ clamp **之后**回显 ⇒ 客户端能读回生效天数。 */
  days: number
  tenant_id: string
  total_requests: number
  total_credits: number
  total_cost_usd?: number
  by_model: MaasUsageModelRow[]
  trend: MaasUsageTrendRow[]
}

export function fetchMaasUsageSummary(
  tenantCode: string,
  params: { days?: number; limit?: number } = {},
  options?: RequestOptions,
): Promise<MaasUsageSummary> {
  const qs = new URLSearchParams()
  if (typeof params.days === 'number' && Number.isFinite(params.days)) qs.set('days', String(Math.trunc(params.days)))
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) qs.set('limit', String(Math.trunc(params.limit)))
  const s = qs.toString()
  return req<unknown>('GET', `${tenantsBase(tenantCode, 'usage/summary')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapMaasUsageSummary,
  )
}

export function unwrapMaasUsageSummary(resp: unknown): MaasUsageSummary {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const u = resp as Record<string, unknown>
    if (
      typeof u.days === 'number' &&
      typeof u.tenant_id === 'string' &&
      Array.isArray(u.by_model) &&
      Array.isArray(u.trend)
    ) {
      return u as unknown as MaasUsageSummary
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/tenants/usage/summary 响应形状不符：期望 {days, tenant_id, by_model, trend}，实得 ${actual}`)
}

export interface MaasConsumptionRow {
  tenant_id: string
  /** ★ omitempty ⇒ 仅在非空时才有键。 */
  owner_user?: string
  /** ★ 指针 + omitempty ⇒ 仅 nil 时省略（指向 0 仍然有键）。 */
  provider_id?: number
  provider_name: string
  credential_id?: number
  credential_label: string
  canonical_id?: number
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits_charged: number
  upstream_cost_usd: number
  /** ★ 算出来的：`credits_charged * cents_per_credit / 100`。 */
  tenant_revenue_usd: number
  gross_margin_usd: number
  /** ★★ 收入为 0 时**保持零值 0**（无定义）⇒ 与「真·零毛利」不可区分。 */
  gross_margin_rate: number
  /** ★ SQL FILTER 计数：已计费 + 流中断 + 失败码 ∈ {client_cancel, client_disconnected}。 */
  cancelled_billed_requests: number
}

export interface MaasConsumptionDetail {
  tenant_id: string
  owner_user?: string
  days: number
  /** ★ 从 `maas_settings WHERE id = 1` 直读 ⇒ 客户端可据此复算 revenue。 */
  cents_per_credit: number
  rows: MaasConsumptionRow[]
}

export function fetchMaasConsumptionDetail(
  tenantCode: string,
  params: { ownerUser?: string; days?: number } = {},
  options?: RequestOptions,
): Promise<MaasConsumptionDetail> {
  const qs = new URLSearchParams()
  if (params.ownerUser) qs.set('owner_user', params.ownerUser)
  if (typeof params.days === 'number' && Number.isFinite(params.days)) qs.set('days', String(Math.trunc(params.days)))
  const s = qs.toString()
  return req<unknown>('GET', `${tenantsBase(tenantCode, 'usage/detail')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapMaasConsumptionDetail,
  )
}

export function unwrapMaasConsumptionDetail(resp: unknown): MaasConsumptionDetail {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const d = resp as Record<string, unknown>
    if (typeof d.tenant_id === 'string' && typeof d.days === 'number' && Array.isArray(d.rows)) {
      return d as unknown as MaasConsumptionDetail
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/tenants/usage/detail 响应形状不符：期望 {tenant_id, days, rows}，实得 ${actual}`)
}

export function fetchMaasTenantLedger(
  tenantCode: string,
  params: { limit?: number } = {},
  options?: RequestOptions,
): Promise<MaasLedgerResponse> {
  const qs = new URLSearchParams()
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) qs.set('limit', String(Math.trunc(params.limit)))
  const s = qs.toString()
  return req<unknown>('GET', `${tenantsBase(tenantCode, 'ledger')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapMaasTenantLedger,
  )
}

export function unwrapMaasTenantLedger(resp: unknown): MaasLedgerResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as MaasLedgerResponse).items)) {
    return resp as MaasLedgerResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`maas/tenants/ledger 响应形状不符：期望 {items:[…]}，实得 ${actual}`)
}

// ── 页面侧判读 ─────────────────────────────────────────────────────────

/** ★ `ClampUsageDays`：两端都 clamp。 */
export function maasUsageDaysClamped(days: number): number {
  if (!Number.isFinite(days)) return MAAS_USAGE_DAYS_DEFAULT
  const n = Math.trunc(days)
  if (n < MAAS_USAGE_DAYS_MIN) return MAAS_USAGE_DAYS_MIN
  if (n > MAAS_USAGE_DAYS_MAX) return MAAS_USAGE_DAYS_MAX
  return n
}

/** ★★ `ClampUsageLimit`：下界**回落 10**、上界 clamp 50 —— 两端不对称。 */
export function maasUsageLimitEffective(limit: number): number {
  if (!Number.isFinite(limit)) return MAAS_USAGE_LIMIT_DEFAULT
  const n = Math.trunc(limit)
  if (n < 1) return MAAS_USAGE_LIMIT_DEFAULT
  if (n > MAAS_USAGE_LIMIT_MAX) return MAAS_USAGE_LIMIT_MAX
  return n
}

/** ★ `ListLedger`：越界**回落 50**（不是 clamp 到上下界）。 */
export function maasLedgerLimitEffective(limit: number): number {
  if (!Number.isFinite(limit)) return MAAS_LEDGER_LIMIT_DEFAULT
  const n = Math.trunc(limit)
  if (n < 1 || n > MAAS_LEDGER_LIMIT_MAX) return MAAS_LEDGER_LIMIT_DEFAULT
  return n
}

/**
 * ★★★ 这两个 usage 端点**按 days 换物理表**。
 * 返回 true 表示本次读的是 `request_logs_hot`（近期热表），
 * false 表示读的是 `request_logs_with_current_month`。
 */
export function maasUsageReadsHotTable(days: number): boolean {
  return maasUsageDaysClamped(days) <= MAAS_USAGE_HOT_DAYS_MAX
}

/**
 * ★★ `cost_usd` 是 float64 + omitempty ⇒ **恰好 0 时键不存在**。
 * 页面读不到时**必须**显示 0，而不是「—」——
 * 否则「真·零成本」会被误报成「数据缺失」。
 */
export function maasCostUsd(row: { cost_usd?: number }): number {
  return typeof row.cost_usd === 'number' ? row.cost_usd : 0
}

/**
 * ★★★ 复算 `tenant_revenue_usd = credits_charged * cents_per_credit / 100`
 * （service.go 里逐行算的）。客户端据此核对服务端给的数字。
 */
export function maasTenantRevenueUsd(creditsCharged: number, centsPerCredit: number): number {
  return (creditsCharged * centsPerCredit) / 100
}

/**
 * ★★★ `gross_margin_rate` 在 `revenue == 0` 时**保持零值**（无定义）。
 * 返回 true 表示「这个 0 是无定义，不是零毛利」。
 */
export function maasMarginRateUndefined(r: { tenant_revenue_usd: number; gross_margin_rate: number }): boolean {
  return r.tenant_revenue_usd === 0
}

/** ★★ 「已计费但被客户端取消」的请求数 —— 收入侧的已知漏点。 */
export function maasHasCancelledBilled(r: MaasConsumptionRow): boolean {
  return r.cancelled_billed_requests > 0
}
