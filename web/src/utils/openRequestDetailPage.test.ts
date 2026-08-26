import { describe, expect, it, vi, afterEach } from 'vitest'
import { openRequestDetailPage, requestDetailPath } from './openRequestDetailPage'

describe('requestDetailPath', () => {
  it('builds path with encoded id and optional query', () => {
    expect(requestDetailPath('abc/1')).toBe('/request-detail/abc%2F1')
    expect(requestDetailPath('r1', { mode: 'session-turns', tab: 'flow' }))
      .toBe('/request-detail/r1?mode=session-turns&tab=flow')
  })

  it('returns empty for blank id', () => {
    expect(requestDetailPath('  ')).toBe('')
  })
})

describe('openRequestDetailPage', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('opens a new tab with noopener', () => {
    const open = vi.fn(() => ({ closed: false }))
    vi.stubGlobal('window', { open })
    expect(openRequestDetailPage('req-9', { tab: 'waterfall' })).toBe(true)
    expect(open).toHaveBeenCalledWith(
      '/request-detail/req-9?tab=waterfall',
      '_blank',
      'noopener,noreferrer',
    )
  })
})
