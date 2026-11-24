import { beforeEach, describe, expect, it, vi } from 'vitest'
import { req } from './client'
import { fetchDailyEvidence, fetchSupplierDayInvoice, listSupplierDayInvoices, type DailyEvidenceQuery } from './dailyReconciliation'

vi.mock('./client', () => ({ req: vi.fn() }))

const query: DailyEvidenceQuery = {
  day: '2026-11-23', timezone: 'Asia/Shanghai', tenantId: 'tenant-one', section: 'internal', limit: 20, offset: 40,
}
const result = {
  day: query.day, timezone: query.timezone, tenant_id: query.tenantId, section: query.section, platform_scope: true,
  limit: query.limit, offset: query.offset, summary: { recorded_gross_debit_credits: 12 }, rows: [],
}

describe('mobile reconciliation scope', () => {
  beforeEach(() => vi.mocked(req).mockReset())

  it('uses the exact day, tenant and requested window for internal credits', async () => {
    const options = { signal: new AbortController().signal }
    vi.mocked(req).mockResolvedValue(result)
    expect(await fetchDailyEvidence(query, options)).toEqual(result)
    const call = vi.mocked(req).mock.lastCall
    if (!call) throw new Error('evidence did not call the API')
    const [method, path, body, sentOptions] = call
    expect([method, body, sentOptions]).toEqual(['GET', undefined, options])
    const url = new URL(path, 'https://fixture.invalid')
    expect(url.pathname).toBe('/api/admin/usage/request-reconciliation')
    expect(Object.fromEntries(url.searchParams)).toEqual({
      day: '2026-11-23', timezone: 'Asia/Shanghai', tenant_id: 'tenant-one', section: 'internal', limit: '20', offset: '40',
    })
  })

  it.each([
    { tenant_id: 'tenant-other' },
    { platform_scope: false },
    { day: '2026-11-24' },
    { timezone: 'UTC' },
    { offset: 20 },
    { rows: [{ tenant_id: 'tenant-other' }] },
  ])('rejects a response from another scope (%o)', async (different) => {
    vi.mocked(req).mockResolvedValue({ ...result, ...different })
    await expect(fetchDailyEvidence(query)).rejects.toThrow('unexpected daily reconciliation scope')
  })

  it('requires the invoice detail to match the original listed window', async () => {
    const invoice = { invoice_id: 'selected', invoice_ref: '23', provider_id: 17, account_ref: 'acct',
      currency: 'USD', stated_total: '0.000000000000000123',
      window_start: '2026-11-22T16:00:00Z', window_end: '2026-11-23T16:00:00Z' }
    const day = { day: '2026-11-23', timezone: 'Asia/Shanghai', limit: 20, offset: 0 }
    vi.mocked(req).mockResolvedValueOnce({ ...day, items: [invoice] })
      .mockResolvedValueOnce({ limit: 20, offset: 0, invoice: { ...invoice, window_end: '2026-11-24T16:00:00Z' }, summary: {}, lines: [] })
      .mockResolvedValueOnce({ limit: 20, offset: 0, invoice, summary: {}, lines: [{ invoice_amount: invoice.stated_total }] })
    expect((await listSupplierDayInvoices(day)).items[0]).toEqual(invoice)
    await expect(fetchSupplierDayInvoice(invoice, { limit: 20, offset: 0 })).rejects.toThrow('unexpected supplier invoice scope')
    const report = await fetchSupplierDayInvoice(invoice, { limit: 20, offset: 0 })
    expect(report.lines[0]?.invoice_amount).toBe('0.000000000000000123')
    expect(vi.mocked(req).mock.calls[0]?.[1]).toBe('/api/admin/provider-cost-reconciliation/invoices?limit=20&offset=0&day=2026-11-23&timezone=Asia%2FShanghai')
    expect(vi.mocked(req).mock.calls[2]?.[1]).toBe('/api/admin/provider-cost-reconciliation/invoices?limit=20&offset=0&invoice_id=selected')
  })
})
