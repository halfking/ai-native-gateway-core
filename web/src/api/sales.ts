import { req } from './_core'

// sales.ts — 报价系统 MVP 的前端 API 客户端（2026-11-05 W1-T2 前端轮）。
// 后端面见 admin/sales_handlers.go（W1，2026-11-04 实施规划 §5）。
// 公开分享面（/api/public/quote/{token}）不在本文件：客户版无登录，
// 由分享链接直开，不经管理端 fetch 通道。

// ── 供给池 ──────────────────────────────────────────────────────────────

export interface SellableAnnotation {
  id: number
  credential_id: number
  provider_model_id: number
  capacity_notes: string
  sla_tier: string
  valid_from: string
  valid_until: string
  vendor_contact: string
  status: string
  maintainer: string
  notes: string
}

export interface SellableRow {
  credential_id: number
  provider_model_id: number
  raw_model_name: string
  canonical_name: string
  provider_name: string
  supplier_currency: string
  billing_mode: string
  supplier_in_per_1m: string | null
  supplier_out_per_1m: string | null
  baseline_currency: string | null
  baseline_in_per_1m: string | null
  baseline_out_per_1m: string | null
  currency_comparable: boolean
  has_baseline: boolean
  market_ref_currency: string | null
  market_ref_in_per_1m: string | null
  market_ref_out_per_1m: string | null
  // 后端 service.go:86-87 为 *string/*int（可空），不是空串/0 占位。
  market_ref_platform_id: string | null
  market_ref_platform_count: number | null
  market_position: string | null
  price_confidence: string
  annotation?: SellableAnnotation
}

export interface WithheldGroup {
  canonical_name: string
  reason: string
  price_variants: Array<[string | null, string | null]>
}

export interface SellableList {
  items: SellableRow[]
  withheld: WithheldGroup[]
  total_trusted: number
  withheld_count: number
  // 三档一键报价系数（settings 热改即生效；仅前端快填用，服务端不校验）。
  tier_coefficients?: Record<string, number>
}

export interface SupplyAnnotationUpsert {
  credential_id: number
  provider_model_id: number
  capacity_notes?: string
  sla_tier?: string
  valid_from?: string
  valid_until?: string
  vendor_contact?: string
  status?: string
  maintainer?: string
  notes?: string
}

export function getSalesSellable() {
  return req<SellableList>('GET', '/api/admin/sales/sellable')
}

export function listSupplyAnnotations() {
  return req<{ items: Array<Record<string, unknown>> }>('GET', '/api/admin/sales/supply')
}

export function upsertSupplyAnnotation(body: SupplyAnnotationUpsert) {
  return req<Record<string, unknown>>('POST', '/api/admin/sales/supply', body)
}

// ── 需求池 ──────────────────────────────────────────────────────────────

export interface DemandCard {
  id: number
  customer_name: string
  scenario: string
  monthly_usage: string
  budget: string
  window_hint: string
  status: string
  claimed_by: string
  // 后端 omitempty（service.go:412-413）⇒ 缺席时是 undefined 而非 null。
  claimed_at?: string | null
  expires_at?: string | null
  expired: boolean
  lost_reason: string
  created_by: string
  notes: string
  created_at: string
  updated_at: string
}

export function listDemandCards(status?: string) {
  const q = status ? `?status=${encodeURIComponent(status)}` : ''
  return req<{ items: DemandCard[] }>('GET', `/api/admin/sales/demand${q}`)
}

export function createDemandCard(body: {
  customer_name: string
  scenario?: string
  monthly_usage?: string
  budget?: string
  window_hint?: string
  expires_at?: string
  notes?: string
}) {
  return req<DemandCard>('POST', '/api/admin/sales/demand', body)
}

export function claimDemandCard(id: number) {
  return req<DemandCard>('POST', `/api/admin/sales/demand/${id}/claim`)
}

export function closeDemandCard(id: number, status: string, reason: string) {
  return req<DemandCard>('POST', `/api/admin/sales/demand/${id}/close`, { status, reason })
}

// ── 报价工作台 ──────────────────────────────────────────────────────────

export interface QuoteItemInput {
  canonical_name: string
  display_name?: string
  est_tokens_in?: number
  est_tokens_out?: number
  volume_note?: string
  // 2026-11-08 审计 P1/P6：行成本/币种/置信档一律服务端从供给视图派生，
  // 客户端声明不再采信 —— 输入类型不再携带这三类字段（传了也会被覆盖）。
  rate_in?: number | null
  rate_out?: number | null
  discount_pct?: number
}

export interface QuoteItemOutput {
  id: number
  canonical_name: string
  display_name: string
  est_tokens_in: number
  est_tokens_out: number
  volume_note: string
  cost_in_per_1m: number | null
  cost_out_per_1m: number | null
  cost_currency: string
  rate_in: number | null
  rate_out: number | null
  discount_pct: number
  confidence: string
  fx_rate: number
  revenue_usd: number
  cost_usd: number
  margin_usd: number
  margin_pct: number | null
}

export interface QuoteVersion {
  id: number
  quote_id: number
  version_no: number
  price_confidence: string
  tier: string
  fx_rate: number
  cost_total_usd: number
  sale_total_usd: number
  margin_usd: number
  margin_pct: number | null
  valid_until: string | null
  expired: boolean
  superseded_by: number | null
  created_by: string
  created_at: string
  items?: QuoteItemOutput[]
}

export interface Quote {
  id: number
  demand_card_id?: number
  customer_name: string
  title: string
  status: string
  current_version_id?: number
  locked: boolean
  created_by: string
  created_at: string
  updated_at: string
  version?: QuoteVersion
  // W2-T2 成本漂移（提醒不自动撤）：活单行成本快照 vs 供给视图当前价。
  cost_drift_pct?: number | null
  cost_drifted?: boolean
}

export interface ShareLink {
  id: number
  quote_id: number
  version_id: number
  contact_name: string
  contact_email: string
  expires_at: string
  revoked_at: string | null
  sent_at: string | null
  viewed_at: string | null
  opened_at: string | null
  view_count: number
  created_by: string
  created_at: string
  token?: string
}

export function listQuotes(status?: string) {
  const q = status ? `?status=${encodeURIComponent(status)}` : ''
  return req<{ items: Quote[] }>('GET', `/api/admin/sales/quotes${q}`)
}

export function getQuote(id: number) {
  return req<Quote>('GET', `/api/admin/sales/quotes/${id}`)
}

export function createQuote(body: {
  demand_card_id?: number
  customer_name: string
  title?: string
  tier?: string
  valid_until?: string
  items: QuoteItemInput[]
}) {
  return req<Quote>('POST', '/api/admin/sales/quotes', body)
}

export function reviseQuote(id: number, body: {
  customer_name?: string
  title?: string
  tier?: string
  valid_until?: string
  items: QuoteItemInput[]
}) {
  return req<Quote>('POST', `/api/admin/sales/quotes/${id}`, body)
}

export function submitQuote(id: number) {
  return req<Quote>('POST', `/api/admin/sales/quotes/${id}/submit`)
}

export function decideQuote(id: number, decision: 'approved' | 'rejected', comment?: string) {
  return req<Quote>('POST', `/api/admin/sales/quotes/${id}/decide`, { decision, comment })
}

export function sendQuote(id: number) {
  return req<Quote>('POST', `/api/admin/sales/quotes/${id}/send`)
}

export function closeQuote(id: number, status: 'won' | 'lost', reason: string) {
  return req<Quote>('POST', `/api/admin/sales/quotes/${id}/close`, { status, reason })
}

export function listQuoteVersions(id: number) {
  return req<{ items: QuoteVersion[] }>('GET', `/api/admin/sales/quotes/${id}/versions`)
}

export function listQuoteApprovals(id: number) {
  return req<{ items: QuoteApproval[] }>('GET', `/api/admin/sales/quotes/${id}/approvals`)
}

// QuoteApproval ↔ sales/quote.go ListApprovals 的 map 投影（quote_id /
// version_id / trigger_reasons(jsonb::text) / requested_by / decision /
// approver / comment / created_at / decided_at / elapsed_seconds）。
// trigger_reasons 是三维红线判定（毛利下限/折扣深度/单笔金额）的
// 可复算快照，submit 内服务端裁决（决议 B），前端只呈现在审批记录里。
export interface QuoteApproval {
  id: number
  quote_id: number
  version_id: number
  trigger_reasons: string
  requested_by: string
  decision: string
  approver: string
  comment: string
  created_at: string
  decided_at: string | null
  elapsed_seconds: number | null
}

export function listQuoteShares(id: number) {
  return req<{ items: ShareLink[] }>('GET', `/api/admin/sales/quotes/${id}/shares`)
}

export function createQuoteShare(id: number, body: {
  contact_name: string
  contact_email?: string
  ttl_hours?: number
}) {
  return req<ShareLink>('POST', `/api/admin/sales/quotes/${id}/shares`, body)
}

// 撤销分享（2026-11-08 审计缺口收口：RevokeShare 此前没有任何 API 面）。
export function revokeQuoteShare(quoteId: number, shareId: number) {
  return req<{ ok: boolean }>('POST', `/api/admin/sales/quotes/${quoteId}/shares/${shareId}/revoke`)
}

// ── 管理层漏斗看板（W2-T2，super-only） ──────────────────────────────────

// SalesDashboard ↔ sales/dashboard.go FunnelDashboard 的读模型（全部现算，
// 无新表）。实收毛利不在此面：default 租户按设计跳过计费（credit_ledger 恒
// 0），「实收毛利」一栏由视图层显式标「待租户计费激活」（风暴档 §3.6）。
export interface SalesDashboard {
  funnel: Record<string, number>
  approvals: Record<string, number>
  submitted_quotes: number
  triggered_quotes: number
  trigger_rate_pct: number
  trigger_alert_pct: number
  trigger_rate_alert: boolean
  triggers_by_dimension: Record<string, number>
  lost_reasons: Array<{ reason: string; count: number }>
  margin_buckets: Record<string, number>
}

export function getSalesDashboard() {
  return req<SalesDashboard>('GET', '/api/admin/sales/dashboard')
}

// 红线判定在 submit 动作内由服务端执行（决议 B「只审例外」）：触发结果
// 通过 submit 的返回状态（approved=政策内自动放行 / pending_approval=触发）
// 与 /approvals 的 trigger_reasons 快照呈现，没有独立的 version 级 HTTP 端点。

