import { BASE, headers, req, type RequestOptions } from './_core'
import { exportFile } from '../utils/exportFile'
import { readExportBlob } from '../utils/exportResponse'

export type DailySection = 'requests' | 'attempts' | 'sessions' | 'postings' | 'coverage' | 'internal'

export interface DailyReconciliationCalendar {
  day: string
  timezone: string
}

export async function readDailyReconciliationDefault(options?: RequestOptions): Promise<DailyReconciliationCalendar | null> {
  const caps = await req<{ daily_reconciliation_default?: DailyReconciliationCalendar }>('GET', '/api/admin/capabilities', undefined, options)
  const calendar = caps?.daily_reconciliation_default
  return calendar?.day && /^\d{4}-\d{2}-\d{2}$/.test(calendar.day) && typeof calendar.timezone === 'string' && calendar.timezone
    ? calendar : null
}

export interface DailyQuery {
  day: string
  timezone: string
  section: DailySection
  tenantId?: string
  limit: number
  offset: number
}

export type EvidenceFields = Record<string, unknown>

const dailyVersion = 'request-reconciliation-v2'

export interface DailyReport {
  version: string
  section: DailySection
  start: string
  end: string
  timezone: string
  day: string
  tenant_id?: string
  platform_scope: boolean
  read_at: string
  coverage: string
  request_count_basis?: 'client_receipt'
  limit: number
  offset: number
  summary: EvidenceFields
  rows: EvidenceFields[]
  projection?: { source: 'request_metrics'; updated_at: string | null; pending_since: string | null }
}

export type EvidenceExport = 'csv' | 'csv-summary'

export interface SupplierInvoice {
  invoice_id: string
  provider_id: number
  account_ref: string
  invoice_ref: string
  currency: string
  stated_total: string
  window_start: string
  window_end: string
}

export interface SupplierInvoiceReport {
  read_at: string
  version: string
  invoice: SupplierInvoice
  summary: EvidenceFields
  lines: EvidenceFields[]
  limit: number
  offset: number
}

const invoicesPath = '/api/admin/provider-cost-reconciliation/invoices'

export function listDailySupplierInvoices(day: string, timezone: string, limit: number, offset: number, options?: RequestOptions) {
  const params = new URLSearchParams({ day, timezone, limit: String(limit), offset: String(offset) })
  return req<{ day: string; timezone: string; items: SupplierInvoice[]; limit: number; offset: number }>(
    'GET', `${invoicesPath}?${params}`, undefined, options,
  )
}

export function readSupplierInvoice(invoiceId: string, limit: number, offset: number, options?: RequestOptions) {
  const params = new URLSearchParams({ invoice_id: invoiceId, limit: String(limit), offset: String(offset) })
  return req<SupplierInvoiceReport>('GET', `${invoicesPath}?${params}`, undefined, options)
}

export async function exportSupplierInvoice(report: SupplierInvoiceReport, format: EvidenceExport, options?: RequestOptions) {
  const id = report.invoice.invoice_id
  const params = new URLSearchParams({ invoice_id: id, limit: String(report.limit), offset: String(report.offset), format })
  const scope = format === 'csv-summary' ? 'summary' : 'page'
  const response = await fetch(`${BASE}${invoicesPath}?${params}`, {
    credentials: 'same-origin', headers: { ...headers('GET'), Accept: 'text/csv' }, signal: options?.signal,
  })
  if (!response.ok || response.headers.get('X-Invoice-ID') !== id || response.headers.get('X-Report-Export') !== scope
    || response.headers.get('X-Report-Limit') !== String(report.limit) || response.headers.get('X-Report-Offset') !== String(report.offset)) {
    throw new Error('supplier invoice CSV unavailable')
  }
  const blob = await readExportBlob(response)
  if (!options?.signal?.aborted) await exportFile({ filename: `supplier-invoice-${id}-${scope}.csv`, blob })
}

function dailyPath(q: DailyQuery, format?: EvidenceExport): string {
  const params = new URLSearchParams({ day: q.day, timezone: q.timezone, section: q.section,
    limit: String(q.limit), offset: String(q.offset) })
  if (q.tenantId) params.set('tenant_id', q.tenantId)
  if (format) params.set('format', format)
  return `/api/admin/usage/v2/request-reconciliation?${params}`
}

export async function readDailyReport(q: DailyQuery, options?: RequestOptions): Promise<DailyReport> {
  const report = await req<DailyReport>('GET', dailyPath(q), undefined, options)
  if (report?.version !== dailyVersion || (needsTerminalUsage(q.section) && report.projection?.source !== 'request_metrics')) {
    throw new Error('unexpected daily reconciliation source')
  }
  return report
}

function needsTerminalUsage(section: DailySection) {
  return section === 'requests' || section === 'sessions' || section === 'coverage'
}

export async function exportDailyReport(q: DailyQuery, format: EvidenceExport, options?: RequestOptions): Promise<void> {
  const scope = format === 'csv-summary' ? 'summary' : 'page'
  const response = await fetch(BASE + dailyPath(q, format), {
    credentials: 'same-origin',
    headers: { ...headers('GET'), Accept: 'text/csv' },
    signal: options?.signal,
  })
  if (!response.ok || response.headers.get('X-Report-Version') !== dailyVersion || response.headers.get('X-Report-Export') !== scope
    || response.headers.get('X-Report-Day') !== q.day || response.headers.get('X-Report-Timezone') !== q.timezone
    || response.headers.get('X-Report-Limit') !== String(q.limit) || response.headers.get('X-Report-Offset') !== String(q.offset)
    || (needsTerminalUsage(q.section) && response.headers.get('X-Report-Terminal-Usage-Source') !== 'request_metrics')) {
    throw new Error('reconciliation CSV unavailable')
  }
  const blob = await readExportBlob(response)
  if (options?.signal?.aborted) return
  await exportFile({ filename: `reconciliation-${q.section}-${q.day}-${scope}.csv`, blob })
}
