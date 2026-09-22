import { describe, expect, it } from 'vitest'
import { inlineLoginPath, locationFromInternalPath, parseSafeInternalRedirect } from './safeRedirect'

describe('parseSafeInternalRedirect', () => {
  it('accepts a request-detail waterfall deep link', () => {
    expect(parseSafeInternalRedirect('/request-detail/abc?mode=request&tab=waterfall'))
      .toBe('/request-detail/abc?mode=request&tab=waterfall')
  })

  it('rejects protocol-relative and absolute URLs', () => {
    expect(parseSafeInternalRedirect('//evil.example/phish')).toBeNull()
    expect(parseSafeInternalRedirect('https://evil.example/phish')).toBeNull()
  })

  it('rejects /login so an already-authed bounce cannot loop', () => {
    expect(parseSafeInternalRedirect('/login')).toBeNull()
    expect(parseSafeInternalRedirect('/login?redirect=%2Frequest-detail%2Fa')).toBeNull()
  })

  it('normalizes encoded path segments through URL parsing', () => {
    expect(parseSafeInternalRedirect('/request-detail/abc%2F1?tab=waterfall'))
      .toBe('/request-detail/abc/1?tab=waterfall')
  })

  it('rejects a fully-encoded path that does not start with /', () => {
    expect(parseSafeInternalRedirect('%2Frequest-detail%2Fabc%3Ftab%3Dwaterfall')).toBeNull()
  })
})

describe('inlineLoginPath', () => {
  it('preserves the original request-detail URL in the redirect query', () => {
    expect(inlineLoginPath('/request-detail/abc?mode=request&tab=waterfall'))
      .toBe('/?login=1&redirect=%2Frequest-detail%2Fabc%3Fmode%3Drequest%26tab%3Dwaterfall')
  })

  it('does not wrap the home page in another redirect', () => {
    expect(inlineLoginPath('/')).toBe('/?login=1')
    expect(inlineLoginPath('/login')).toBe('/?login=1')
  })
})

describe('locationFromInternalPath', () => {
  it('splits a request-detail waterfall URL into named-route-friendly pieces', () => {
    expect(locationFromInternalPath('/request-detail/abc?mode=request&tab=waterfall')).toEqual({
      path: '/request-detail/abc',
      query: { mode: 'request', tab: 'waterfall' },
    })
  })

  it('rejects unsafe locations', () => {
    expect(locationFromInternalPath('https://evil.example')).toBeNull()
    expect(locationFromInternalPath('/login')).toBeNull()
  })
})
