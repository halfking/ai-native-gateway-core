// reportrollup.ts — 对账报表 API（2026-09-25 对账报表落地轮）。
// 供应商/内部双视角区间汇总 + Excel 双 sheet 导出 + 单日手动重跑。
import { req, BASE, headers } from './_core'

export type ReportView = 'provider' | 'internal'

export interface ReportTotals {
  request_count: number
  success_count: number
  error_count: number
  error_rate: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  estimated_cost_cents: number
  currency: string
  credits_charged: number
  internal_cost_cents?: number
  internal_currency?: string
  cache_hit_ratio: number | null
  latency_p50_ms: number
  latency_p95_ms: number
}

export interface ReportProviderRow {
  provider_id: number
  provider_name: string
  // 2026-09-26 审计轮：供应商综合评分（0-100，成功率×时效因子，读面现算）。
  quality_score: number
  totals: ReportTotals
  error_breakdown: Record<string, number>
}

export interface ReportModelRow {
  provider_id?: number
  provider_name?: string
  raw_model_name: string
  totals: ReportTotals
  error_breakdown: Record<string, number>
}

export interface ReportTenantRow {
  tenant_id: string
  totals: ReportTotals
  error_breakdown: Record<string, number>
}

export interface ReportPersonRow {
  tenant_id: string
  person: string
  totals: ReportTotals
  error_breakdown: Record<string, number>
}

export interface ReportDayRow {
  date: string
  totals: ReportTotals
}

export interface RangeReport {
  start: string
  end: string
  view: ReportView
  totals: ReportTotals
  error_breakdown: Record<string, number>
  days: ReportDayRow[]
  providers?: ReportProviderRow[]
  models: ReportModelRow[]
  tenants?: ReportTenantRow[]
  persons?: ReportPersonRow[]
  snapshot_dates: string[]
}

export interface ReportRangeQuery {
  start: string // YYYY-MM-DD
  end: string // YYYY-MM-DD
  view: ReportView
  provider_id?: number
  tenant_id?: string
  model?: string
}

function rangeQs(q: ReportRangeQuery): string {
  const p = new URLSearchParams()
  p.set('start', q.start)
  p.set('end', q.end)
  p.set('view', q.view)
  if (q.provider_id != null) p.set('provider_id', String(q.provider_id))
  if (q.tenant_id) p.set('tenant_id', q.tenant_id)
  if (q.model) p.set('model', q.model)
  return p.toString()
}

export async function getReportSummary(q: ReportRangeQuery): Promise<RangeReport> {
  const res = await req<{ report: RangeReport }>('GET', `/api/admin/report-rollup/summary?${rangeQs(q)}`)
  return res.report
}

export async function downloadReportExport(q: ReportRangeQuery): Promise<void> {
  const res = await fetch(`${BASE}/api/admin/report-rollup/export?${rangeQs(q)}`, {
    headers: headers('GET'),
  })
  if (!res.ok) throw new Error(`export failed: ${res.status}`)
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  const prefix = q.view === 'internal' ? 'reconciliation_internal' : 'reconciliation_provider'
  a.download = `${prefix}_${q.start}_${q.end}.xlsx`
  a.click()
  URL.revokeObjectURL(url)
}

export async function runReportRollup(date?: string): Promise<{ date: string; rows_written: number; requests_seen: number }> {
  return req('POST', '/api/admin/report-rollup/run', { date: date ?? '' })
}
