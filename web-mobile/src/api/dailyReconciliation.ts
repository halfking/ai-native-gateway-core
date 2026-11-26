import { req, type RequestOptions } from './client'

export const dailySections = ['coverage', 'requests', 'attempts', 'sessions', 'postings', 'internal', 'invoices'] as const
export type DailySection = Exclude<(typeof dailySections)[number], 'invoices'>
export type EvidenceFields = Record<string, unknown>
export class EvidenceScopeError extends Error {}

interface EvidencePage {
  limit: number
  offset: number
}

interface DayPage extends EvidencePage {
  day: string
  timezone?: string
}

export interface DailyEvidenceQuery extends DayPage {
  section: DailySection
  tenantId?: string
}

export interface DailyEvidence extends DayPage {
  version: string
  section: DailySection
  timezone: string
  tenant_id?: string
  platform_scope: boolean
  start: string
  end: string
  read_at: string
  request_count_basis?: 'client_receipt'
  summary: EvidenceFields
  rows: EvidenceFields[]
  projection?: { source: 'request_metrics'; updated_at: string | null; pending_since: string | null }
}

export interface SupplierDayInvoice {
  invoice_id: string
  invoice_ref: string
  account_ref: string
  provider_id: number
  currency: string
  stated_total: string
  window_start: string
  window_end: string
}

export interface SupplierDayInvoices extends DayPage {
  timezone: string
  items: SupplierDayInvoice[]
}

export interface SupplierDayInvoiceEvidence extends EvidencePage {
  invoice: SupplierDayInvoice
  read_at: string
  summary: EvidenceFields
  lines: EvidenceFields[]
}

const invoicePath = '/api/admin/provider-cost-reconciliation/invoices'

function pageParams(page: EvidencePage): URLSearchParams {
  return new URLSearchParams({ limit: String(page.limit), offset: String(page.offset) })
}

function dayParams(query: DayPage): URLSearchParams {
  const params = pageParams(query)
  params.set('day', query.day)
  if (query.timezone) params.set('timezone', query.timezone)
  return params
}

function samePage(result: EvidencePage, query: EvidencePage) {
  return result.offset === query.offset && result.limit === query.limit
}

function sameDay(result: DayPage, query: DayPage) {
  return samePage(result, query) && result.day === query.day && typeof result.timezone === 'string' && !!result.timezone
    && (!query.timezone || result.timezone === query.timezone)
}

function evidenceObject(value: unknown): value is EvidenceFields {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

export async function fetchDailyEvidence(query: DailyEvidenceQuery, options?: RequestOptions): Promise<DailyEvidence> {
  const params = dayParams(query)
  params.set('section', query.section)
  if (query.tenantId) params.set('tenant_id', query.tenantId)
  const result = await req<DailyEvidence>('GET', `/api/admin/usage/v2/request-reconciliation?${params}`, undefined, options)
  const requiresTerminalUsage = query.section === 'requests' || query.section === 'sessions' || query.section === 'coverage'
  if (!result || !sameDay(result, query) || result.section !== query.section || result.platform_scope !== true
    || result.version !== 'request-reconciliation-v2' || (requiresTerminalUsage && result.projection?.source !== 'request_metrics')
    || (result.tenant_id || undefined) !== query.tenantId || !evidenceObject(result.summary)
    || !Array.isArray(result.rows) || !result.rows.every(evidenceObject)
    || (query.tenantId && query.section !== 'coverage' && result.rows.some(row => row.tenant_id !== query.tenantId))) {
    throw new EvidenceScopeError('unexpected daily reconciliation scope')
  }
  return result
}

export async function listSupplierDayInvoices(query: DayPage, options?: RequestOptions): Promise<SupplierDayInvoices> {
  const result = await req<SupplierDayInvoices>('GET', `${invoicePath}?${dayParams(query)}`, undefined, options)
  if (!result || !sameDay(result, query) || !Array.isArray(result.items)) {
    throw new EvidenceScopeError('unexpected supplier invoice day')
  }
  return result
}

export async function fetchSupplierDayInvoice(invoice: SupplierDayInvoice, page: EvidencePage, options?: RequestOptions): Promise<SupplierDayInvoiceEvidence> {
  const params = pageParams(page)
  params.set('invoice_id', invoice.invoice_id)
  const result = await req<SupplierDayInvoiceEvidence>('GET', `${invoicePath}?${params}`, undefined, options)
  if (!result || !samePage(result, page) || result.invoice?.invoice_id !== invoice.invoice_id
    || result.invoice.window_start !== invoice.window_start || result.invoice.window_end !== invoice.window_end
    || !evidenceObject(result.summary) || !Array.isArray(result.lines) || !result.lines.every(evidenceObject)) {
    throw new EvidenceScopeError('unexpected supplier invoice scope')
  }
  return result
}
