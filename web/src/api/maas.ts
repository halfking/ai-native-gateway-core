import { req } from './_core'

// maas.ts — v6.0 audit T12 (2026-06-22)
// MaaS = "Model as a Service" (积分计费, credit-based billing).
//
// Public surfaces (under /api/maas/...):  settings (cents per credit,
// base rate), model list, plans, top-up packages, current wallet,
// account (wallet + ledger + orders), order creation, usage summary.
//
// Admin surfaces (under /api/admin/maas/...):  private settings (Alipay
// account, stub QR URLs), per-model rate overrides, cross-tenant
// wallet/account/orders access, manual credit adjust + grant, order
// confirmation (for manual / stub payment channels).
//
// The trailing constants (MAAS_LEDGER_TYPE_LABELS etc.) are static UI
// label maps; keeping them here avoids a separate strings file.

export interface MaasPublicSettings {
  cents_per_credit: number
  base_credits_per_1m: number
  base_credits_per_1m_in?: number
  base_credits_per_1m_out?: number
  base_credits_per_1m_cache_in?: number
  base_credits_per_1m_cache_out?: number
  global_discount?: number
  currency_display: string
}

export interface MaasAdminSettings extends MaasPublicSettings {
  alipay_account?: string
  wechat_mch_id?: string
  stub_alipay_qr_url?: string
  stub_wechat_qr_url?: string
}

export interface AdminMaasModelRate {
  canonical_id: number
  canonical_name: string
  display_name: string
  vendor: string
  family: string | null
  modality: string
  status: string
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
  custom_credits_per_1m_in: number | null
  custom_credits_per_1m_out: number | null
  custom_credits_per_1m_cache_in: number | null
  custom_credits_per_1m_cache_out: number | null
  custom_credits_per_1m_image_tokens: number | null
  custom_credits_per_1m_audio_tokens: number | null
  custom_credits_per_1m_video_tokens: number | null
  is_custom: boolean
  updated_at: string | null
}

export interface MaasModelRateUpsert {
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  credits_per_1m_image_tokens?: number
  credits_per_1m_audio_tokens?: number
  credits_per_1m_video_tokens?: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
  manual_image?: boolean
  manual_audio?: boolean
  manual_video?: boolean
}

export interface AdminMaasModelRatesResponse {
  settings: MaasAdminSettings
  items: AdminMaasModelRate[]
}

export interface MaasModel {
  canonical_name: string
  display_name: string
  vendor: string
  family?: string | null
  family_display_name?: string | null
  context_window?: number | null
  modality: string
  billing_mode: string
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in?: number
  credits_per_1m_cache_out?: number
}

export interface MaasPlan {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  monthly_credits: number
  enabled: boolean
  sort_order: number
}

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

export interface MaasWallet {
  tenant_id: string
  quota_remaining: number
  granted_balance: number
  purchased_balance: number
  balance_credits: number
  total_available: number
  subscription?: {
    plan_id: number
    plan_name: string
    status: string
    period_start: string
    period_end: string
  }
}

export interface MaasBillingOrder {
  id: number
  order_no: string
  tenant_id: string
  order_type: 'subscribe' | 'topup'
  status: 'pending' | 'paid' | 'cancelled' | 'expired'
  amount_cents: number
  credits: number
  plan_id?: number
  package_id?: number
  plan_name?: string
  package_name?: string
  payment_channel: 'alipay' | 'wechat' | 'manual'
  qr_payload: string
  qr_url: string
  payment_hint?: string
  stub_mode?: boolean
  paid_at?: string
  expires_at: string
  note: string
  created_at: string
  updated_at: string
}

export interface MaasAccount {
  wallet: MaasWallet
  recent_ledger: MaasLedgerEntry[]
  recent_orders: MaasBillingOrder[]
}

export interface MaasLedgerEntry {
  id: number
  entry_type: string
  amount: number
  balance_after: number
  pool: string | null
  ref_type: string | null
  ref_id: string | null
  note: string
  created_at: string
}

export function getMaasSettings() {
  return req<MaasPublicSettings>('GET', '/api/maas/settings')
}

export function getAdminMaasSettings() {
  return req<MaasAdminSettings>('GET', '/api/admin/maas/settings')
}

export function updateAdminMaasSettings(body: {
  cents_per_credit: number
  base_credits_per_1m?: number
  base_credits_per_1m_in?: number
  base_credits_per_1m_out?: number
  base_credits_per_1m_cache_in?: number
  base_credits_per_1m_cache_out?: number
  global_discount?: number
  currency_display: string
}) {
  return req<{ status: string }>('PUT', '/api/admin/maas/settings', body)
}

export function getAdminMaasModelRates() {
  return req<AdminMaasModelRatesResponse>('GET', '/api/admin/maas/model-rates')
}

export function upsertAdminMaasModelRate(canonicalId: number, body: MaasModelRateUpsert) {
  return req<{ status: string }>('PUT', `/api/admin/maas/model-rates/${canonicalId}`, body)
}

export function resetAdminMaasModelRateFields(canonicalId: number, fields: string[]) {
  return req<{ status: string }>('PATCH', `/api/admin/maas/model-rates/${canonicalId}`, { fields })
}

export function batchUpsertAdminMaasModelRates(updates: Array<{
  canonical_id: number
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  credits_per_1m_image_tokens?: number
  credits_per_1m_audio_tokens?: number
  credits_per_1m_video_tokens?: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
  manual_image?: boolean
  manual_audio?: boolean
  manual_video?: boolean
}>) {
  return req<{ updated: number }>('POST', '/api/admin/maas/model-rates/batch', { updates })
}

export function batchResetAdminMaasModelRates(items: Array<{
  canonical_id: number
  fields: string[]
}>) {
  return req<{ updated: number }>('POST', '/api/admin/maas/model-rates/batch-reset', { items })
}

export function batchFillGlobalAdminMaasModelRates(items: Array<{
  canonical_id: number
  all_dimensions: boolean
}>) {
  return req<{ updated: number }>('POST', '/api/admin/maas/model-rates/batch-fill-global', { items })
}

export function deleteAdminMaasModelRate(canonicalId: number) {
  return req<{ status: string }>('DELETE', `/api/admin/maas/model-rates/${canonicalId}`)
}

// 2026-11-09（运营审计轮项2）：价格期版本账（迁移 952）——额效期 + 自动闭前期 + 订价变化流。
export interface ModelPriceVersion {
  canonical_id: number
  canonical_name: string
  display_name: string
  billing_mode: 'pay_as_you_go' | 'token_plan' | 'booster_pack'
  effective_from: string
  effective_to: string | null
  credits_per_1m_in: number | null
  credits_per_1m_out: number | null
  credits_per_1m_cache_in: number | null
  credits_per_1m_cache_out: number | null
  credits_per_1m_image_tokens: number | null
  credits_per_1m_audio_tokens: number | null
  credits_per_1m_video_tokens: number | null
  source: string
  note: string
}

export function getAdminMaasModelPriceHistory(canonicalId: number, billingMode = '', limit = 100) {
  const q = new URLSearchParams({ canonical_id: String(canonicalId), limit: String(limit) })
  if (billingMode) q.set('billing_mode', billingMode)
  return req<{ items: ModelPriceVersion[] }>('GET', `/api/admin/maas/model-price-history?${q}`)
}

export function getAdminMaasModelPriceChanges(days = 30, limit = 100) {
  return req<{ items: ModelPriceVersion[] }>('GET', `/api/admin/maas/model-price-changes?days=${days}&limit=${limit}`)
}

// 2026-11-09（运营审计轮项4）：营销管理面（套餐/加油包/优惠码/活动）。
export interface AdminMaasPlan {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  monthly_credits: number
  enabled: boolean
  sort_order: number
  billing_period?: string
  quota_credits?: number | null
  overage_price_cents?: number | null
  overage_allowed?: boolean
  effective_from?: string | null
  effective_to?: string | null
  auto_renew?: boolean
  sla?: string
}

export interface AdminMaasTopup {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  credits_amount: number
  enabled: boolean
  sort_order: number
  bonus_credits?: number
  effective_from?: string | null
  effective_to?: string | null
  first_time_only?: boolean
}

export interface AdminMaasCoupon {
  id: number
  code: string
  credits_amount: number
  max_redemptions: number
  redemption_count: number
  expires_at: string | null
  status: string
}

export interface AdminMaasCampaign {
  id: number
  code: string
  name: string
  description: string
  benefit_type: 'credits' | 'discount' | 'gift'
  benefit_value: Record<string, number>
  status: string
  starts_at: string | null
  ends_at: string | null
  max_redemptions_per_tenant: number
  total_budget_credits: number
  granted_credits: number
}

export function getAdminMaasPlans() {
  return req<{ items: AdminMaasPlan[] }>('GET', '/api/admin/maas/plans')
}

export function putAdminMaasPlan(body: {
  code: string
  tier: string
  name: string
  price_cents: number
  monthly_credits: number
  billing_period: string
  enabled: boolean
  sort_order: number
}) {
  return req<{ status: string }>('PUT', '/api/admin/maas/plans-upsert', body)
}

export function patchAdminMaasPlan(id: number, enabled: boolean) {
  return req<{ status: string }>('PATCH', `/api/admin/maas/plans/${id}`, { enabled })
}

export function getAdminMaasTopupPackages() {
  return req<{ items: AdminMaasTopup[] }>('GET', '/api/admin/maas/topup-packages')
}

export function putAdminMaasTopup(body: {
  code: string
  tier: string
  name: string
  price_cents: number
  credits_amount: number
  bonus_credits: number
  first_time_only: boolean
  enabled: boolean
  sort_order: number
}) {
  return req<{ status: string }>('PUT', '/api/admin/maas/topup-packages-upsert', body)
}

export function patchAdminMaasTopup(id: number, enabled: boolean) {
  return req<{ status: string }>('PATCH', `/api/admin/maas/topup-packages/${id}`, { enabled })
}

export function getAdminMaasCoupons(limit = 50) {
  return req<{ items: AdminMaasCoupon[] }>('GET', `/api/admin/maas/coupons?limit=${limit}`)
}

export function patchAdminMaasCoupon(id: number, status: 'active' | 'disabled') {
  return req<{ status: string }>('PATCH', `/api/admin/maas/coupons/${id}`, { status })
}

// 2026-11-10（行情整合轮）：940 行情面只读消费——市场参考价 vs 我方价格对比。
export interface MarketPriceOverviewRow {
  canonical_name: string
  canonical_id: number | null
  display_name: string
  market_min_in_cny_per_1m: number | null
  market_min_out_cny_per_1m: number | null
  market_min_platform_count: number
  market_min_platform_id: string
  vendor_list_in_cny_per_1m: number | null
  vendor_list_out_cny_per_1m: number | null
  // A-17（959）：原厂锚原生币事实源 + 双地板（overseas USD / domestic CNY）
  vendor_list_currency: string
  vendor_list_native_in_per_1m: number | null
  vendor_list_native_out_per_1m: number | null
  overseas_min_in_per_1m: number | null
  overseas_min_out_per_1m: number | null
  overseas_min_platform_id: string
  overseas_min_platform_count: number
  domestic_min_in_per_1m: number | null
  domestic_min_out_per_1m: number | null
  domestic_min_platform_id: string
  domestic_min_platform_count: number
  fx_rate: number | null
  vendor_vendor: string
  supplier_in_per_1m: number | null
  supplier_out_per_1m: number | null
  supplier_currency: string
  retail_credits_in_per_1m: number | null
  retail_credits_out_per_1m: number | null
  retail_cny_in_per_1m: number | null
  retail_cny_out_per_1m: number | null
  cents_per_credit: number
  currency_display: string
  market_position: number | null
  retail_vs_market: number | null
  source_run_date: string
  resolved: boolean
}

export interface MarketPriceSyncRun {
  id: number
  run_date: string
  source_platforms: number
  source_rows: number
  landed_rows: number
  rejected_rows: number
  conflicts: Record<string, number>
  detail: Record<string, unknown>
  created_at: string
}

export function getAdminMaasMarketPriceOverview(q = '', onlyResolved = false, limit = 200) {
  const params = new URLSearchParams({ q, limit: String(limit) })
  if (onlyResolved) params.set('resolved', 'true')
  return req<{ items: MarketPriceOverviewRow[]; sync: MarketPriceSyncRun[] }>(
    'GET', `/api/admin/maas/market-price-overview?${params}`)
}

export function getAdminMaasMarketPriceSyncStatus(limit = 10) {
  return req<{ items: MarketPriceSyncRun[] }>(
    'GET', `/api/admin/maas/market-price-sync-status?limit=${limit}`)
}

export function getAdminMaasCampaigns() {
  return req<{ items: AdminMaasCampaign[] }>('GET', '/api/admin/maas/campaigns')
}

export function postAdminMaasCampaign(body: {
  code: string
  name: string
  description?: string
  benefit_type: 'credits' | 'discount' | 'gift'
  benefit_value: Record<string, number>
  starts_at?: string | null
  ends_at?: string | null
  max_redemptions_per_tenant?: number
  total_budget_credits?: number
}) {
  return req<{ id: number }>('POST', '/api/admin/maas/campaigns', body)
}

export function patchAdminMaasCampaignStatus(id: number, status: 'draft' | 'active' | 'paused' | 'ended') {
  return req<{ status: string }>('PATCH', `/api/admin/maas/campaigns/${id}/status`, { status })
}

export function putAdminMaasModelPriceMode(body: {
  canonical_id: number
  billing_mode: 'token_plan' | 'booster_pack'
  prices: MaasModelRateUpsert
  note?: string
}) {
  return req<{ status: string }>('PUT', '/api/admin/maas/model-price-mode', body)
}

export function getMaasModels() {
  return req<{ items: MaasModel[] }>('GET', '/api/maas/models')
}

export function getMaasPlans() {
  return req<{ items: MaasPlan[] }>('GET', '/api/maas/plans')
}

export function getMaasTopupPackages() {
  return req<{ items: MaasTopupPackage[] }>('GET', '/api/maas/topup-packages')
}

export function getMaasWallet() {
  return req<MaasWallet>('GET', '/api/maas/wallet')
}

export function getMaasAccount() {
  return req<MaasAccount>('GET', '/api/maas/account')
}

export function getMaasOrders(limit = 20) {
  return req<{ items: MaasBillingOrder[] }>('GET', `/api/maas/orders?limit=${limit}`)
}

export function getMaasOrder(id: number) {
  return req<MaasBillingOrder>('GET', `/api/maas/orders/${id}`)
}

export function createMaasOrder(body: {
  type: 'subscribe' | 'topup'
  plan_id?: number
  package_id?: number
  payment_channel?: 'alipay' | 'wechat'
}) {
  return req<MaasBillingOrder>('POST', '/api/maas/orders', body)
}

export function getMaasLedger(limit = 50) {
  return req<{ items: MaasLedgerEntry[] }>('GET', `/api/maas/ledger?limit=${limit}`)
}

export interface MaasUsageModelRow {
  model: string
  requests: number
  credits: number
  cost_usd?: number
}

export interface MaasUsageTrendRow {
  date: string
  requests: number
  credits: number
  cost_usd?: number
}

export interface MaasUsageSummary {
  days: number
  tenant_id: string
  total_requests: number
  total_credits: number
  total_cost_usd?: number
  by_model: MaasUsageModelRow[]
  trend: MaasUsageTrendRow[]
  // Optional degradation markers. When usage_ledger_with_current_month (or
  // other analytic views) is missing, the backend returns zeroed metrics
  // together with these flags. The UI uses them to show a non-blocking
  // hint instead of an empty-data page.
  degraded?: boolean
  missing_view?: string
  error_code?: string
  hint?: string
}

export interface MaasConsumptionDetailRow {
  tenant_id: string
  owner_user?: string
  provider_id?: number | null
  provider_name: string
  credential_id?: number | null
  credential_label: string
  canonical_id?: number | null
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits_charged: number
  upstream_cost_usd: number
  tenant_revenue_usd: number
  gross_margin_usd: number
  gross_margin_rate: number
  cancelled_billed_requests: number
}

export interface MaasConsumptionDetail {
  tenant_id: string
  owner_user?: string
  days: number
  cents_per_credit: number
  rows: MaasConsumptionDetailRow[]
}

export interface MaasConsumptionDetailRow {
  tenant_id: string
  owner_user?: string
  provider_id?: number | null
  provider_name: string
  credential_id?: number | null
  credential_label: string
  canonical_id?: number | null
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits_charged: number
  upstream_cost_usd: number
  tenant_revenue_usd: number
  gross_margin_usd: number
  gross_margin_rate: number
  cancelled_billed_requests: number
}

export interface MaasConsumptionDetail {
  tenant_id: string
  owner_user?: string
  days: number
  cents_per_credit: number
  rows: MaasConsumptionDetailRow[]
}

export function getMaasUsageSummary(days = 7, limit = 10) {
  const q = new URLSearchParams()
  q.set('days', String(days))
  q.set('limit', String(limit))
  return req<MaasUsageSummary>('GET', `/api/maas/usage/summary?${q.toString()}`)
}

export function getAdminMaasWallet(tenantCode: string) {
  return req<MaasWallet>('GET', `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/wallet`)
}

export function getAdminMaasAccount(tenantCode: string) {
  return req<MaasAccount>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/account`,
  )
}

export function getAdminMaasUsageSummary(tenantCode: string, days = 7, limit = 10) {
  const q = new URLSearchParams()
  q.set('days', String(days))
  q.set('limit', String(limit))
  return req<MaasUsageSummary>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/usage/summary?${q.toString()}`,
  )
}

export function getAdminMaasConsumptionDetail(tenantCode: string, days = 7, ownerUser = '') {
  const q = new URLSearchParams({ days: String(days) })
  if (ownerUser.trim()) q.set('owner_user', ownerUser.trim())
  return req<MaasConsumptionDetail>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/usage/detail?${q.toString()}`,
  )
}

export function getAdminMaasLedger(tenantCode: string, limit = 50) {
  return req<{ items: MaasLedgerEntry[] }>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/ledger?limit=${limit}`,
  )
}

export function adjustAdminMaasCredits(tenantCode: string, amount: number, note: string) {
  return req<{ status: string }>(
    'POST',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/adjust`,
    { amount, note },
  )
}

export function grantAdminMaasCredits(tenantCode: string, grantedCredits: number, note: string) {
  return req<{ status: string }>(
    'POST',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/grant`,
    { granted_credits: grantedCredits, note },
  )
}

export function getAdminMaasOrders(limit = 50) {
  return req<{ items: MaasBillingOrder[] }>('GET', `/api/admin/maas/orders?limit=${limit}`)
}

export function getAdminMaasTenantOrders(tenantCode: string, limit = 20) {
  return req<{ items: MaasBillingOrder[] }>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/orders?limit=${limit}`,
  )
}

export function confirmAdminMaasOrder(orderId: number, note = '') {
  return req<{ status: string }>('POST', `/api/admin/maas/orders/${orderId}/confirm`, { note })
}

export const MAAS_LEDGER_TYPE_LABELS: Record<string, string> = {
  consume: '消耗',
  topup: '充值',
  subscribe: '订阅',
  adjust: '调整',
  refund: '退款',
}

export const MAAS_POOL_LABELS: Record<string, string> = {
  subscription_quota: '订阅额度',
  granted: '信用积分',
  purchased: '充值积分',
}

export const MAAS_ORDER_STATUS_LABELS: Record<string, string> = {
  pending: '待支付',
  paid: '已支付',
  cancelled: '已取消',
  expired: '已过期',
}


export interface MaasMonthlyBill {
  id: number
  tenant_id: string
  period_ym: string
  period_start: string
  period_end: string
  total_credits: number
  total_requests: number
  amount_cents: number
  currency: string
  status: string
  issued_at?: string
  note: string
  created_at: string
  updated_at: string
}

export interface MaasCouponRedemption {
  id: number
  coupon_id: number
  tenant_id: string
  credits_granted: number
  created_at: string
  code?: string
}

export function getMaasBills(limit = 24) {
  return req<{ items: MaasMonthlyBill[] }>('GET', `/api/maas/bills?limit=${limit}`)
}

export function exportMaasBillsCSV() {
  return req<string>('GET', '/api/maas/bills/export')
}

export function activateMaasCoupon(code: string) {
  return req<MaasCouponRedemption>('POST', '/api/maas/coupons/activate', { code })
}

export function getMaasCouponRedemptions(limit = 20) {
  return req<{ items: MaasCouponRedemption[] }>('GET', `/api/maas/coupons/redemptions?limit=${limit}`)
}

export function createMaasInvoicePlaceholder(billId?: number) {
  return req<{ id: number; status: string; note: string }>(
    'POST',
    '/api/maas/invoices',
    billId ? { bill_id: billId } : {},
  )
}

export function createAdminMaasCoupon(body: {
  code?: string
  credits_amount: number
  max_redemptions?: number
  note?: string
}) {
  return req<Record<string, unknown>>('POST', '/api/admin/maas/coupons', body)
}
