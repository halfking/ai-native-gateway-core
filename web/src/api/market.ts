import { req } from './_core'

// market.ts — 市场面 admin API（2026-11-13 A-17 收口 M1-T3/M3-T2）。
// 平台注册表（958，区域/币种裁定权威 + A-5 我方平台排除）读写 + 价格对比
// 分析（C1/C2/C3 评分行 + 双地板对拍，只读对账）。行情价禁回流路由/计费
// —— 本模块只供运营读面对账，不喂任何路由/计费消费方。

export interface MarketPlatformRow {
  platform_id: string
  platform_name: string
  region: string // 'overseas' | 'domestic'
  native_currency: string // 'USD' | 'CNY'（与 region 联动约束）
  is_ours: boolean
  enabled: boolean
  notes: string
  updated_at: string
}

// 部分更新：只提交要改的字段（PUT 语义 = 未提交字段保持现值）。
export interface MarketPlatformUpdate {
  platform_name?: string
  region?: string
  native_currency?: string
  is_ours?: boolean
  enabled?: boolean
  notes?: string
}

export function getAdminMarketPlatforms() {
  return req<{ items: MarketPlatformRow[] }>('GET', '/api/admin/market/platforms')
}

export function putAdminMarketPlatform(platformId: string, body: MarketPlatformUpdate) {
  return req<{ item: MarketPlatformRow }>(
    'PUT', `/api/admin/market/platforms/${encodeURIComponent(platformId)}`, body)
}

// ── 价格对比（M3-T2，/operations/price-compare 读面） ─────────────────────

export interface MarketCompareRow {
  credential_id: number
  provider_model_id: number
  canonical_name: string
  currency: string
  supplier_in_per_1m: number | null
  supplier_out_per_1m: number | null
  price_confidence: string // trusted | suspect_symmetric | empty | unknown_currency
  discount_vs_vendor: number | null
  vendor_ref_in_per_1m: number | null
  vendor_ref_out_per_1m: number | null
  vendor_ref_currency: string
  fx_used: number | null
  market_position: number | null
  market_ref_in_per_1m: number | null
  market_ref_out_per_1m: number | null
  market_ref_platform_id: string
  market_ref_platform_count: number
  market_ref_used: string // 'overseas' | 'domestic' | ''
  market_advantage_score: number | null
  market_position_vs_overseas: number | null
  market_position_vs_domestic: number | null
  computed_at: string
  overseas_min_in_per_1m: number | null
  overseas_min_out_per_1m: number | null
  overseas_min_platform_id: string
  overseas_min_platform_count: number
  domestic_min_in_per_1m: number | null
  domestic_min_out_per_1m: number | null
  domestic_min_platform_id: string
  domestic_min_platform_count: number
  vendor_list_currency: string
  vendor_list_native_in_per_1m: number | null
  vendor_list_native_out_per_1m: number | null
  vendor_list_cny_in_per_1m: number | null
  vendor_list_cny_out_per_1m: number | null
  mpr_fx_rate: number | null
}

export interface MarketCompareResp {
  available: boolean
  reason?: string
  items: MarketCompareRow[]
  summary: Record<string, number>
}

export function getAdminMarketCompare(q = '', confidence = '', limit = 200) {
  const params = new URLSearchParams({ q, confidence, limit: String(limit) })
  return req<MarketCompareResp>('GET', `/api/admin/market/compare?${params}`)
}

// ── 按模型跨平台现价（970，对齐 salesdecorate Compare） ─────────────────

export interface MarketPlatformQuoteRow {
  canonical_name: string
  platform_id: string
  platform_name: string
  region: string
  native_currency: string
  is_vendor: boolean
  vendor: string
  currency: string
  input_per_1m: number | null
  output_per_1m: number | null
  cny_in_per_1m: number | null
  cny_out_per_1m: number | null
  // ★ 2026-11-17（972）：缓存命中价（真实成本的大头）+ 阶梯 + 模型类别。
  cache_hit_per_1m: number | null
  cny_cache_hit_per_1m: number | null
  tier: string
  model_kind: string
  run_date: string
  source_url: string
}

// 模型目录（不指定 model 时返回）：供模型选择器使用。
export interface MarketPlatformQuoteDirectoryRow {
  canonical_name: string
  display_name: string
  platforms: number
  vendor_rows: number
  min_cny_in_per_1m: number | null
  min_cny_out_per_1m: number | null
  run_date: string
}

export interface MarketModelPlatformsResp {
  available: boolean
  reason?: string
  items: MarketPlatformQuoteRow[]
  directory?: MarketPlatformQuoteDirectoryRow[]
  is_dir?: boolean
  model?: string
  dimension?: string
}

export function getAdminMarketModelPlatforms(model = '', dimension = 'standard', limit = 200) {
  const params = new URLSearchParams({ model, dimension, limit: String(limit) })
  return req<MarketModelPlatformsResp>('GET', `/api/admin/market/model-platforms?${params}`)
}

// ── 定价建议（2026-11-17 定价对比完善轮）────────────────────────────────
// ★ 与采集侧红线的分界：**自动**同步禁止回流计费面；这里是**显式**运营动作
//   （预览 → 一键执行）⇒ 写入零售价并留期次 + 批次留痕，可审计。

export type PricingStrategy = 'copy_platform' | 'average' | 'min' | 'floor_markup'

export interface PricingSuggestionRequest {
  strategy: PricingStrategy
  platform_id?: string
  dimension?: string
  markup_pct?: number
  canonical_ids?: number[]
  min_delta_pct?: number
  confirm?: boolean
}

export interface PricingSuggestionRow {
  canonical_id: number
  canonical_name: string
  display_name: string
  /** 当前零售价：**只表示该模型自己的价**；没有则 null（前端显示「—」，不回落全局基准） */
  current_credits_in_per_1m: number | null
  current_credits_out_per_1m: number | null
  current_cny_in_per_1m: number | null
  current_cny_out_per_1m: number | null
  /** 现价来源：explicit（两轴都有自有价）/ partial（仅一轴）/ none（都没有） */
  current_basis: string
  suggested_credits_in_per_1m: number | null
  suggested_credits_out_per_1m: number | null
  suggested_cny_in_per_1m: number | null
  suggested_cny_out_per_1m: number | null
  delta_pct_in: number | null
  delta_pct_out: number | null
  basis: string
  basis_platforms: number
  basis_platform_id: string
  run_date: string
  applicable: boolean
  skip_reason: string
}

export interface PricingSuggestionStats {
  models: number
  with_market: number
  without_market: number
  changed: number
  applied: number
  skipped: number
}

export interface PricingSuggestionResp {
  strategy: string
  platform_id: string
  dimension: string
  markup_pct: number | null
  items: PricingSuggestionRow[]
  summary: PricingSuggestionStats
  batch_id: number
  warnings: string[]
}

export function postAdminMarketPricingSuggestions(body: PricingSuggestionRequest) {
  return req<PricingSuggestionResp>('POST', '/api/admin/market/pricing-suggestions', body)
}

export function postAdminMarketPricingApply(body: PricingSuggestionRequest) {
  return req<PricingSuggestionResp>('POST', '/api/admin/market/pricing-apply', body)
}

export interface PricingSuggestionBatchRow {
  id: number
  created_at: string
  strategy: string
  platform_id: string
  dimension: string
  markup_pct: number | null
  operator: string
  dry_run: boolean
  requested: number
  applied: number
  skipped: number
  detail: Record<string, unknown>
}

export function getAdminMarketPricingBatches(limit = 50) {
  return req<{ items: PricingSuggestionBatchRow[] }>(
    'GET', `/api/admin/market/pricing-batches?limit=${limit}`)
}
