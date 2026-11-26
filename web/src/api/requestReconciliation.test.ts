import { afterEach, describe, expect, it, vi } from 'vitest'
import { exportDailyReport, exportSupplierInvoice, type DailyQuery, type SupplierInvoiceReport } from './requestReconciliation'

const { save } = vi.hoisted(() => ({ save: vi.fn() }))
vi.mock('../utils/exportFile', async importOriginal => ({
  ...await importOriginal<typeof import('../utils/exportFile')>(),
  exportFile: save,
}))

const query: DailyQuery = { section: 'attempts', day: '2026-11-23', timezone: 'Asia/Shanghai', tenantId: 'tenant-a', limit: 50, offset: 50 }

describe('daily reconciliation CSV', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    save.mockReset()
  })

  it('requests the displayed daily page and reserves the correct filename', async () => {
    const csv = 'estimated_amount\n0.000000000000000123\n'
    const network = vi.fn().mockResolvedValue(new Response(csv, { headers: {
      'Content-Type': 'text/csv', 'X-Report-Export': 'page', 'X-Report-Day': query.day, 'X-Report-Timezone': query.timezone,
      'X-Report-Version': 'request-reconciliation-v2', 'X-Report-Limit': '50', 'X-Report-Offset': '50',
    } }))
    vi.stubGlobal('fetch', network)
    await exportDailyReport(query, 'csv')
    const url = new URL(network.mock.calls[0][0], 'http://fixture.invalid')
    expect(url.pathname).toBe('/api/admin/usage/v2/request-reconciliation')
    expect(Object.fromEntries(url.searchParams)).toEqual({
      section: 'attempts', day: '2026-11-23', timezone: 'Asia/Shanghai', tenant_id: 'tenant-a', limit: '50', offset: '50', format: 'csv',
    })
    const { blob, filename } = save.mock.calls[0][0]
    expect(filename).toBe('reconciliation-attempts-2026-11-23-page.csv')
    expect(blob.size).toBe(csv.length)
  })

  it('rejects a CSV carrying a different reconciliation date', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('metric,value', { headers: {
      'X-Report-Export': 'summary', 'X-Report-Day': '2026-11-22', 'X-Report-Timezone': query.timezone,
      'X-Report-Version': 'request-reconciliation-v2', 'X-Report-Limit': '50', 'X-Report-Offset': '50',
    } })))
    await expect(exportDailyReport(query, 'csv-summary')).rejects.toThrow('reconciliation CSV unavailable')
    expect(save).not.toHaveBeenCalled()
  })

  it('rejects a requests export without terminal projection provenance', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('metric,value', { headers: {
      'X-Report-Export': 'summary', 'X-Report-Day': query.day, 'X-Report-Timezone': query.timezone,
      'X-Report-Version': 'request-reconciliation-v2', 'X-Report-Limit': '50', 'X-Report-Offset': '50',
    } })))
    await expect(exportDailyReport({ ...query, section: 'requests' }, 'csv-summary')).rejects.toThrow('reconciliation CSV unavailable')
    expect(save).not.toHaveBeenCalled()
  })

  it('rejects a supplier CSV with a different invoice or page', async () => {
    const report: SupplierInvoiceReport = { invoice: {
      invoice_id: 'current-id', invoice_ref: 'bill', provider_id: 1, account_ref: 'account', window_end: '', window_start: '',
      currency: 'USD', stated_total: '0',
    }, version: 'supplier-invoice-v1', read_at: '2026-11-24T00:00:00Z', summary: {}, lines: [], limit: 30, offset: 60 }
    const reply = (id: string, offset: string) => new Response('line,invoice_amount\nbucket,0.000000000000000123', { headers: {
      'Content-Type': 'text/csv', 'X-Invoice-ID': id, 'X-Report-Export': 'page', 'X-Report-Limit': '30', 'X-Report-Offset': offset,
    } })
    const network = vi.fn().mockResolvedValueOnce(reply('old-id', '60')).mockResolvedValueOnce(reply('current-id', '0'))
      .mockResolvedValueOnce(reply('current-id', '60'))
    vi.stubGlobal('fetch', network)
    await expect(exportSupplierInvoice(report, 'csv')).rejects.toThrow('supplier invoice CSV unavailable')
    await expect(exportSupplierInvoice(report, 'csv')).rejects.toThrow('supplier invoice CSV unavailable')
    await exportSupplierInvoice(report, 'csv')
    expect(save).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ filename: 'supplier-invoice-current-id-page.csv' }))
    const url = new URL(network.mock.calls[2][0], 'http://fixture.invalid')
    expect(Object.fromEntries(url.searchParams)).toEqual({ invoice_id: 'current-id', limit: '30', offset: '60', format: 'csv' })
  })
})
