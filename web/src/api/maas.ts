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
  status: string
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
  custom_credits_per_1m_in: number | null
  custom_credits_per_1m_out: number | null
  custom_credits_per_1m_cache_in: number | null
  custom_credits_per_1m_cache_out: number | null
  is_custom: boolean
  updated_at: string | null
}

export interface MaasModelRateUpsert {
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
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

export function deleteAdminMaasModelRate(canonicalId: number) {
  return req<{ status: string }>('DELETE', `/api/admin/maas/model-rates/${canonicalId}`)
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