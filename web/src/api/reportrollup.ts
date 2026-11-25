// reportrollup.ts — 对账报表 API（2026-09-25 落地轮；2026-09-29 多维筛选轮重写）。
// 供应商/内部双视角 × 六维筛选（供应商/凭据/模型/租户/用户/apikey）× 汇总/按天明细。
// V2 reads the asynchronous request projection for summaries, daily details, and exports.
import { req, BASE, headers } from './_core'
import { exportFile } from '../utils/exportFile'
import { readExportBlob } from '../utils/exportResponse'

export type ReportView = 'provider' | 'internal'
/** 分组维度：主表按此维度汇总（取代旧版四张固定表）。 */
export type GroupDim = 'provider' | 'credential' | 'model' | 'tenant' | 'person' | 'apikey'

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
  estimated_cost_cents: number | null
  currency: string
  credits_charged: number
  internal_cost_cents?: number | null
  internal_currency?: string
  usd_measured_requests?: number
  usd_unmeasured_requests?: number
  cache_measured_requests?: number
  latency_measured_requests?: number
  cache_hit_ratio: number | null
  latency_p50_ms: number | null
  latency_p95_ms: number | null
}

export type Breakdown = Record<string, number>

/** 未落定维度的哨兵 id（未路由到 provider 的失败行没有这些维度）。 */
export const UNASSIGNED_ID = -1

export interface ReportProviderRow {
  provider_id: number
  provider_name: string
  quality_score: number
  totals: ReportTotals
  error_breakdown: Breakdown
}

export interface ReportModelRow {
  provider_id?: number
  provider_name?: string
  raw_model_name: string
  totals: ReportTotals
  error_breakdown?: Breakdown
  /** 质量评分 = 成功率 × 时效因子；公式与维度无关，每个分组都有（0 请求时不返回）。 */
  quality_score?: number
}

export interface ReportTenantRow {
  tenant_id: string
  totals: ReportTotals
  error_breakdown?: Breakdown
  quality_score?: number
}

export interface ReportPersonRow {
  tenant_id: string
  person: string
  totals: ReportTotals
  error_breakdown?: Breakdown
  quality_score?: number
}

export interface ReportCredentialRow {
  credential_id: number
  credential_name: string
  provider_id?: number
  provider_name?: string
  totals: ReportTotals
  error_breakdown: Breakdown
  quality_score?: number
}

export interface ReportApiKeyRow {
  api_key_id: number
  api_key_name: string
  tenant_id?: string
  totals: ReportTotals
  error_breakdown: Breakdown
  quality_score?: number
}

export interface ReportDayRow {
  date: string
  totals: ReportTotals
  error_breakdown?: Breakdown
}

export interface DailyModelRow {
  date: string
  raw_model_name: string
  provider_id?: number
  provider_name?: string
  totals: ReportTotals
  error_breakdown?: Breakdown
  /** 见 DailyGroupRow.quality_score。 */
  quality_score?: number
}

export interface DailyGroupRow {
  date: string
  key: string
  name?: string
  totals: ReportTotals
  error_breakdown?: Breakdown
  /** 明细模式主表的「质量评分」列；缺它那一列在按天明细里整列为空。 */
  quality_score?: number
}

export interface ReportCoverage {
  grain_dates: string[]
  legacy_dates: string[]
}

export interface RangeReport {
  start: string
  end: string
  view: ReportView
  totals: ReportTotals
  error_breakdown: Breakdown
  top_error_kind?: string
  top_error_count?: number
  days: ReportDayRow[]
  daily_models?: DailyModelRow[]
  providers?: ReportProviderRow[]
  credentials?: ReportCredentialRow[]
  api_keys?: ReportApiKeyRow[]
  models?: ReportModelRow[]
  model_totals?: ReportModelRow[]
  tenants?: ReportTenantRow[]
  persons?: ReportPersonRow[]
  person_page?: { number: number; page_size: number; total: number }
  daily_providers?: DailyGroupRow[]
  daily_credentials?: DailyGroupRow[]
  daily_api_keys?: DailyGroupRow[]
  daily_tenants?: DailyGroupRow[]
  daily_persons?: DailyGroupRow[]
  snapshot_dates: string[]
  coverage: ReportCoverage
  source: 'grain' | 'mixed' | 'legacy' | 'request_metrics'
  projection?: {
    from: string
    through: string
    timezone: string
    updated_at: string | null
    pending_since: string | null
  }
}

export interface DimensionOption {
  key: string
  name?: string
  requests: number
}

export interface DimensionOptions {
  providers: DimensionOption[]
  credentials: DimensionOption[]
  api_keys: DimensionOption[]
  models: DimensionOption[]
  tenants: DimensionOption[]
  persons: DimensionOption[]
  /**
   * 被硬上限砍掉的候选项数（维度名 → 砍掉几项）。
   *
   * 旧实现按 500 项静默截断（真库 30 天就有 527 个模型名），被砍掉的正是请求数
   * 靠后的长尾——「数据不见了」却没有任何提示。现在后端显式回报，前端据此
   * 披露，避免「列表变短」被误读成「数据变少了」。
   */
  truncated?: Record<string, number>
}

/** 六维筛选值（零值 / undefined = 不过滤）。 */
export interface ReportFilter {
  traffic_scope?: 'business'
  provider_id?: number
  credential_id?: number
  api_key_id?: number
  tenant_id?: string
  person?: string
  model?: string
}

export interface ReportQuery extends ReportFilter {
  start: string // YYYY-MM-DD
  end: string // YYYY-MM-DD
  view: ReportView
  /** true = 追加「按天 × 维度」明细行；false = 只要汇总（默认）。 */
  detail?: boolean
  summary_scope?: 'distribution' | 'totals'
  person_page?: number
  /**
   * 导出时「按天×X」那张 sheet 按哪个维度拆。缺省 model。
   * 与页面上主表的 groupDim 是同一个维度：导出的口径必须和屏幕上看的那张表
   * 一致，否则对帐人会拿到一份和界面不符的交接表。
   */
  group?: GroupDim
}

function rangeQs(q: ReportQuery): string {
  const p = new URLSearchParams()
  p.set('start', q.start)
  p.set('end', q.end)
  p.set('view', q.view)
  if (q.traffic_scope) p.set('traffic_scope', q.traffic_scope)
  // detail 必须**总是**显式发。早先只在打开时发 detail=daily，关掉就不发这个
  // 参数——后端于是分不清「用户要汇总」和「调用方没指定」，导出的「汇总 ⇄ 按天
  // 明细」开关成了摆设（实测 detail=false / true 导出字节数完全相同）。
  p.set('detail', q.detail ? 'daily' : 'summary')
  if (q.summary_scope) p.set('summary_scope', q.summary_scope)
  if (q.person_page != null) p.set('person_page', String(q.person_page))
  if (q.group) p.set('group', q.group)
  if (q.provider_id != null) p.set('provider_id', String(q.provider_id))
  if (q.credential_id != null) p.set('credential_id', String(q.credential_id))
  if (q.api_key_id != null) p.set('api_key_id', String(q.api_key_id))
  if (q.tenant_id) p.set('tenant_id', q.tenant_id)
  if (q.person) p.set('person', q.person)
  if (q.model) p.set('model', q.model)
  return p.toString()
}

export async function getReportSummary(q: ReportQuery): Promise<RangeReport> {
  const res = await req<{ report: RangeReport }>('GET', `/api/admin/report-rollup/v2/summary?${rangeQs(q)}`)
  return res.report
}

export interface ReportDailyPage {
  day: string
  group: GroupDim
  page: { number: number; page_size: number; total: number; rows: DailyGroupRow[] }
  projection: NonNullable<RangeReport['projection']>
}

export function getReportDailyPage(q: ReportQuery & { day: string; group: GroupDim; page: number }, signal?: AbortSignal): Promise<ReportDailyPage> {
  const params = new URLSearchParams(rangeQs(q))
  params.set('day', q.day)
  params.set('page', String(q.page))
  return req<ReportDailyPage>('GET', `/api/admin/report-rollup/v2/daily?${params}`, undefined, { signal })
}

export async function getReportDimensions(
  q: Pick<ReportQuery, 'start' | 'end' | 'view' | 'traffic_scope'>,
): Promise<DimensionOptions> {
  const res = await req<{ dimensions: DimensionOptions }>(
    'GET',
    `/api/admin/report-rollup/v2/dimensions?${rangeQs(q)}`,
  )
  return res.dimensions
}

export async function downloadReportExport(q: ReportQuery): Promise<void> {
  const res = await fetch(`${BASE}/api/admin/report-rollup/v2/export?${rangeQs(q)}`, {
    headers: headers('GET'),
  })
  if (!res.ok) throw new Error(`export failed: ${res.status}`)
  // 2026-10-06（UI规范 10 §4.6.18）：经 readExportBlob 取体。该端点显式设了
  // Content-Length（report_rollup.go:441），故命中「读之前就能判」那条路径。
  const blob = await readExportBlob(res)
  // 2026-10-05（UI规范 19 §3.1）：走 exportFile 降级链（分享面→壳桥→blob）。
  const prefix = q.view === 'internal' ? 'reconciliation_internal' : 'reconciliation_provider'
  await exportFile({ filename: `${prefix}_${q.start}_${q.end}.xlsx`, blob })
}

export async function runReportRollup(date?: string): Promise<{ date: string; rows_written: number; requests_seen: number }> {
  return req('POST', '/api/admin/report-rollup/run', { date: date ?? '' })
}
