import { afterEach, describe, expect, it, vi } from 'vitest'
import { downloadReportExport, type ReportQuery } from './reportrollup'

const { headers, downloadSameOriginFile, exportFile, readExportBlob } = vi.hoisted(() => ({
  headers: vi.fn(), downloadSameOriginFile: vi.fn(), exportFile: vi.fn(), readExportBlob: vi.fn(),
}))
vi.mock('./_core', () => ({ BASE: '', headers }))
vi.mock('../utils/exportFile', () => ({ downloadSameOriginFile, exportFile }))
vi.mock('../utils/exportResponse', () => ({ readExportBlob }))

const query: ReportQuery = { start: '2026-08-27', end: '2026-11-24', view: 'internal', detail: true, group: 'person', tenant_id: 'tenant-3' }

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetAllMocks()
})

describe('historical workbook download', () => {
  it('checks the cookie session and passes the complete query to browser download', async () => {
    headers.mockReturnValue({ 'Accept-Language': 'en-US' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    await downloadReportExport(query)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, options] = fetchMock.mock.calls[0]!
    expect(options).toMatchObject({ method: 'HEAD', credentials: 'same-origin' })
    const params = new URL(url, 'https://example.invalid').searchParams
    expect(Object.fromEntries(params)).toMatchObject({ start: query.start, end: query.end, view: 'internal', detail: 'daily', group: 'person', tenant_id: 'tenant-3' })
    expect(downloadSameOriginFile).toHaveBeenCalledWith(url, 'reconciliation_internal_2026-08-27_2026-11-24.xlsx')
    expect(readExportBlob).not.toHaveBeenCalled()
  })

  it('surfaces an invalid download session before navigating', async () => {
    headers.mockReturnValue({})
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 401 })))
    await expect(downloadReportExport(query)).rejects.toThrow('401')
    expect(downloadSameOriginFile).not.toHaveBeenCalled()
  })

  it.each(['bearer', 'older gateway'])('preserves the capped download for %s', async (mode) => {
    const bearer = mode === 'bearer'
    headers.mockReturnValue(bearer ? { Authorization: 'Bearer test' } : {})
    const result = new Response(new Blob(['file']))
    const fetchMock = vi.fn().mockResolvedValue(result)
    if (!bearer) fetchMock.mockResolvedValueOnce(new Response(null, { status: 405 }))
    vi.stubGlobal('fetch', fetchMock)
    const blob = new Blob(['bounded'])
    readExportBlob.mockResolvedValue(blob)
    await downloadReportExport(query)
    expect(fetchMock).toHaveBeenCalledTimes(bearer ? 1 : 2)
    expect(readExportBlob).toHaveBeenCalledWith(result)
    expect(downloadSameOriginFile).not.toHaveBeenCalled()
    expect(exportFile).toHaveBeenCalledWith({ filename: 'reconciliation_internal_2026-08-27_2026-11-24.xlsx', blob })
  })
})
