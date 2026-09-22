import { describe, expect, it, vi, afterEach } from 'vitest'
import type { Router } from 'vue-router'
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

  it('uses the production named-route resolver for a waterfall deep link', () => {
    const resolve = vi.fn(() => ({ href: '/gateway/request-detail/req-42?tab=waterfall' }))
    const router = { resolve } as unknown as Pick<Router, 'resolve'>

    expect(requestDetailPath('req-42', { tab: 'waterfall' }, router))
      .toBe('/gateway/request-detail/req-42?tab=waterfall')
    expect(resolve).toHaveBeenCalledWith({
      name: 'request-detail',
      params: { requestId: 'req-42' },
      query: { tab: 'waterfall' },
    })
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

  it('does not open a tab for a blank request ID', () => {
    const open = vi.fn()
    vi.stubGlobal('window', { open })

    expect(openRequestDetailPage('   ', { tab: 'waterfall' })).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })
})
