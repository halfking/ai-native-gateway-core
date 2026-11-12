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
