import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, expect, it, vi } from 'vitest'
import ReportDayDetails from './ReportDayDetails.vue'
import { downloadReportExport, getReportDailyPage } from '../../api/reportrollup'
import reports from '../../locales/en-US/reports'

vi.mock('../../api/reportrollup', () => ({ downloadReportExport: vi.fn(), getReportDailyPage: vi.fn() }))
vi.mock('../ui/kxDatePresets', () => ({ systemTzDateStr: () => '2026-11-26' }))

const read = vi.mocked(getReportDailyPage)
const download = vi.mocked(downloadReportExport)
const projection = { from: '2026-11-24T16:00:00Z', through: '2026-11-25T16:00:00Z', timezone: 'Asia/Shanghai', updated_at: null, pending_since: null }

beforeEach(() => {
  vi.clearAllMocks()
  read.mockImplementation(async q => ({ day: q.day, group: q.group, projection, page: {
    number: q.page, page_size: 50, total: 51, rows: [],
  } }))
  download.mockResolvedValue(undefined)
})

it('preserves the report filters, paginates daily detail, and exports the full range', async () => {
  const query = { start: '2026-11-24', end: '2026-11-26', view: 'internal' as const, tenant_id: 'tenant-a', model: 'chat' }
  const wrapper = mount(ReportDayDetails, {
    props: { query, dates: ['2026-11-24', '2026-11-25', '2026-11-26'], dayThrough: projection.through, showCost: false, disabled: false },
    global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { reports, common: { loading: 'Loading', prevPage: 'Previous', nextPage: 'Next' } } } })] },
  })
  expect(read).not.toHaveBeenCalled()
  await wrapper.get('[data-testid="report-detail-toggle"]').trigger('click')
  await flushPromises()
  expect(read).toHaveBeenLastCalledWith({ ...query, group: 'model', day: '2026-11-25', page: 1 }, expect.anything())
  await wrapper.get('[data-testid="report-detail-next"]').trigger('click')
  await flushPromises()
  expect(read).toHaveBeenLastCalledWith({ ...query, group: 'model', day: '2026-11-25', page: 2 }, expect.anything())
  await wrapper.get('[data-testid="report-detail-group"]').setValue('person')
  await flushPromises()
  expect(read).toHaveBeenLastCalledWith({ ...query, group: 'person', day: '2026-11-25', page: 1 }, expect.anything())
  await wrapper.get('[data-testid="report-detail-export"]').trigger('click')
  expect(download).toHaveBeenCalledWith({ ...query, group: 'person', detail: true })
  wrapper.unmount()
})
