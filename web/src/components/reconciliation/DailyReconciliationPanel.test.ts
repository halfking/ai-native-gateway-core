import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { DailyQuery, DailyReport } from '../../api/requestReconciliation'
import en from '../../locales/en-US/reports'
import zh from '../../locales/zh-CN/reports'
import DailyReconciliationPanel from './DailyReconciliationPanel.vue'
import { dailyFields } from './dailyFields'

const { readDailyReport, exportDailyReport, listDailySupplierInvoices, readSupplierInvoice, exportSupplierInvoice } = vi.hoisted(() => ({
  readDailyReport: vi.fn(), exportDailyReport: vi.fn(), listDailySupplierInvoices: vi.fn(), readSupplierInvoice: vi.fn(), exportSupplierInvoice: vi.fn(),
}))
vi.mock('../../api/requestReconciliation', () => ({ readDailyReport, exportDailyReport, listDailySupplierInvoices, readSupplierInvoice, exportSupplierInvoice }))

function response(query: DailyQuery): DailyReport {
  return {
    version: 'request-reconciliation-v1', section: query.section, day: query.day, timezone: query.timezone,
    tenant_id: query.tenantId, platform_scope: query.section !== 'internal', start: '2026-11-22T16:00:00Z', end: '2026-11-23T16:00:00Z',
    read_at: '2026-11-24T01:00:00Z', coverage: 'retained-evidence', limit: query.limit, offset: query.offset, rows: [], summary: {},
  }
}

function render(tenantScope?: string) {
  return mount(DailyReconciliationPanel, {
    props: { tenantScope },
    global: {
      plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { reports: en } } })],
      stubs: {
        KxDatePicker: { props: ['modelValue'], emits: ['update:modelValue'],
          template: `<input data-testid="selected-day" :value="modelValue" @input="$emit('update:modelValue', $event.target.value)" />` },
      },
    },
  })
}

describe('daily reconciliation evidence', () => {
  beforeEach(() => {
    readDailyReport.mockReset()
    exportDailyReport.mockReset().mockResolvedValue(undefined)
    listDailySupplierInvoices.mockReset()
    readSupplierInvoice.mockReset()
    exportSupplierInvoice.mockReset().mockResolvedValue(undefined)
  })

  it('queries one date, pages the evidence, and exports the displayed query', async () => {
    readDailyReport.mockImplementation(async (query: DailyQuery) => ({
      ...response(query), summary: { recorded_attempts: 51, sent: 50 }, rows: Array.from({ length: query.offset === 0 ? 50 : 1 }, (_, index) => ({
        attempt_id: `at-${query.offset + index}`, local_day: query.day, estimated_amount: '0.000000000000000123', estimated_currency: 'USD',
      })),
    }))
    const wrapper = render()
    expect(readDailyReport).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="selected-day"]').setValue('2026-11-23')
    await wrapper.get('[data-testid="daily-section"]').setValue('attempts')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const query = { day: '2026-11-23', timezone: 'Asia/Shanghai', section: 'attempts', limit: 50, offset: 0 }
    expect(readDailyReport).toHaveBeenNthCalledWith(1, query, expect.anything())
    expect(wrapper.text()).toContain('0.000000000000000123')
    await wrapper.get('[data-testid="daily-next"]').trigger('click')
    await flushPromises()
    expect(readDailyReport).toHaveBeenNthCalledWith(2, { ...query, offset: 50 }, expect.anything())
    expect(wrapper.text()).toContain('at-50')
    await wrapper.get('[data-testid="daily-export-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="daily-export-summary"]').trigger('click')
    expect(exportDailyReport).toHaveBeenNthCalledWith(1, { ...query, offset: 50 }, 'csv', expect.anything())
    expect(exportDailyReport).toHaveBeenNthCalledWith(2, { ...query, offset: 50 }, 'csv-summary', expect.anything())
    wrapper.unmount()
  })

  it('locks a tenant portal to internal credits and aborts requests on tenant changes', async () => {
    let finish: ((data: DailyReport) => void) | undefined
    readDailyReport.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = render('tenant-a')
    expect(wrapper.find('[data-testid="daily-section"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="daily-tenant"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    const [query, options] = readDailyReport.mock.calls[0]
    expect(query).toMatchObject({ section: 'internal', tenantId: 'tenant-a' })
    await wrapper.setProps({ tenantScope: 'tenant-b' })
    expect(options.signal.aborted).toBe(true)
    finish?.({ ...response(query), rows: [{ tenant_id: 'tenant-a', recorded_gross_debit_credits: 1234 }] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('tenant-a')
    expect(wrapper.find('[data-testid="daily-export-summary"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('has localized labels for every displayed field', () => {
    for (const { columns, summary } of Object.values(dailyFields)) {
      for (const field of [...columns, ...summary]) {
        expect(en.daily.fields).toHaveProperty(field)
        expect(zh.daily.fields).toHaveProperty(field)
      }
    }
  })

  it('opens exact-day supplier evidence and exports only the displayed invoice', async () => {
    const invoice = { invoice_id: 'fixture-id', provider_id: 35, account_ref: 'daily-account', invoice_ref: 'day-bill',
      currency: 'USD', stated_total: '0.000000000000000123', window_start: '2026-11-22T16:00:00Z', window_end: '2026-11-23T16:00:00Z' }
    listDailySupplierInvoices.mockResolvedValue({ day: '2026-11-23', timezone: 'Asia/Shanghai', offset: 0, limit: 30, items: [invoice] })
    const report = { invoice, version: 'supplier-invoice-v1', read_at: '2026-11-24T00:00:00Z', summary: { invoice_lines: 1 }, limit: 30, offset: 0, lines: [
      { line_ref: 'bucket-line', kind: 'usage_bucket', match_status: 'bucket_matched', invoice_amount: invoice.stated_total,
        bucket_generated_images: '0', bucket_observed_generated_images: '0',
        bucket_video_output_duration_us: '9007199254740993', bucket_observed_video_output_duration_us: '9007199254740993' },
    ] }
    readSupplierInvoice.mockResolvedValue(report)
    const wrapper = render()
    await wrapper.get('[data-testid="selected-day"]').setValue('2026-11-23')
    await wrapper.get('[data-testid="daily-section"]').setValue('supplier_invoices')
    expect(wrapper.find('[data-testid="daily-tenant"]').exists()).toBe(false)
    await wrapper.get('[data-testid="invoice-load-day"]').trigger('click')
    await flushPromises()
    expect(listDailySupplierInvoices).toHaveBeenCalledWith('2026-11-23', 'Asia/Shanghai', 30, 0, expect.anything())
    const invoices = wrapper.get('[data-testid="daily-supplier-invoices"]')
    await invoices.get('li button').trigger('click')
    await flushPromises()
    expect(readSupplierInvoice).toHaveBeenCalledWith('fixture-id', 30, 0, expect.anything())
    expect(invoices.get('table').text()).toContain('Usage bucket quantities verified')
    expect(invoices.get('table').text()).toContain(invoice.stated_total)
    expect(invoices.get('table').text()).toContain('Invoiced images')
    expect(invoices.get('table').text()).toContain('Retained video output (µs)')
    expect(invoices.get('table').text()).toContain('9007199254740993')
    expect(invoices.get('table').text()).not.toContain('Invoiced billable audio (µs)')
    const exportPage = invoices.findAll('button').find(button => button.text() === en.daily.exportPage)
    expect(exportPage).toBeDefined()
    await exportPage?.trigger('click')
    expect(exportSupplierInvoice).toHaveBeenCalledWith(report, 'csv', expect.anything())
    await wrapper.get('[data-testid="selected-day"]').setValue('2026-11-24')
    expect(invoices.find('table').exists()).toBe(false)
    expect(invoices.text()).not.toContain('day-bill')
    wrapper.unmount()
  })
})
