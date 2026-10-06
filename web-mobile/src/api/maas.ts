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
export function maasEffectiveSource(
  row: MaasModelRateRow,
  settings: MaasSettings,
  dim: MaasRateDim,
): 'custom' | 'global_configured' | 'global_hardcoded' {
  if (maasDimUsesCustom(row, dim)) return 'custom'
  return maasGlobalRate(settings, dim).source === 'hardcoded' ? 'global_hardcoded' : 'global_configured'
}
