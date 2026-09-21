// api-autoroute.test.ts — P1-28 cross-tenant sk-* relay cache isolation.
//
// Original bug: web/src/api-autoroute.ts held a module-level
// `_relayApiKeyCache = ''` shared across every SPA mount. Logging out
// as tenant A and logging back in as tenant B (without a full reload)
// would serve tenant A's revealed sk-* key to tenant B's
// simulateAutoRoute() call, leaking tenant credentials across the
// tenant boundary.
//
// The fix keeps the cache as a Map keyed by (tenant_id, user_id,
// bearer). The new export `clearRelayApiKeyCache()` is wired into
// store.ts mutations so every auth-state change eagerly drops the
// cache. This test exercises the key isolation property + the wiring
// by routing through simulateAutoRoute (the only public caller of
// resolveRelayApiKey) and inspecting the Authorization header that
// actually goes over the wire.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  clearRelayApiKeyCache,
  simulateAutoRoute,
} from './api-autoroute'
import { store, setUserInfo, clearJwt, type UserInfo } from './store'
import { getKeys, revealKey } from './api'

vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return {
    ...actual,
    getKeys: vi.fn(),
    revealKey: vi.fn(),
  }
})

const mockedGetKeys = vi.mocked(getKeys)
const mockedRevealKey = vi.mocked(revealKey)

const tenantA: UserInfo = {
  id: 1,
  tenant_id: 'tenant-a',
  username: 'a-admin',
  display_name: 'A Admin',
  email: 'a@example.com',
  role: 'tenant_admin',
  enabled: true,
}
const tenantB: UserInfo = {
  id: 2,
  tenant_id: 'tenant-b',
  username: 'b-admin',
  display_name: 'B Admin',
  email: 'b@example.com',
  role: 'tenant_admin',
  enabled: true,
}

function fakeKeyList(tenantId: string): Awaited<ReturnType<typeof getKeys>> {
  return [
    {
      id: 100,
      key_prefix: `sk-tenant-${tenantId}-`,
      owner_user: 'system',
      enabled: true,
      status: 'active',
      expires_at: null,
      last_used_at: null,
      budget_usd: null,
      rate_limit_rpm: null,
      rate_limit_concurrent: null,
      rate_limit_tpm: null,
      key_tier: 'standard',
      application_code: 'relay',
      default_client_profile: null,
      is_system: true,
      remark: null,
      total_requests: 0,
      total_prompt_tokens: 0,
      total_completion_tokens: 0,
      total_cost_usd: 0,
      last_request_at: null,
      tenant_id: tenantId,
      key_alias: null,
    },
  ]
}

/** Read the Bearer header from the most recent /v1/chat/completions
 *  fetch so the test can assert which sk-* the cache surfaced. */
function lastBearerFromFetch(): string | null {
  const calls = (globalThis.fetch as unknown as { mock?: { calls: unknown[] } }).mock?.calls
  if (!calls || calls.length === 0) return null
  const [, init] = calls[calls.length - 1] as [string, RequestInit]
  const headers = (init?.headers ?? {}) as Record<string, string>
  const auth = headers['Authorization'] || headers['authorization']
  if (!auth) return null
  const m = /^Bearer\s+(.+)$/.exec(auth)
  return m ? m[1] : null
}

beforeEach(() => {
  clearRelayApiKeyCache()
  clearJwt()
  localStorage.clear()
  store.jwtToken = ''
  store.apiKey = ''
  setUserInfo(null)
  mockedGetKeys.mockReset()
  mockedRevealKey.mockReset()
  globalThis.fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'X-Gw-Auto-Decision': '' } }),
  )
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('P1-28 relayApiKeyCache tenant isolation', () => {
  it('caches the revealed key under (tenant, user, bearer) and serves it on repeat', async () => {
    setUserInfo(tenantA)
    store.jwtToken = 'jwt-tenant-a'

    mockedGetKeys.mockResolvedValue(fakeKeyList('tenant-a'))
    mockedRevealKey.mockResolvedValue({ key_id: 100, api_key: 'sk-tenant-a-secret' })

    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-tenant-a-secret')
    expect(mockedGetKeys).toHaveBeenCalledTimes(1)
    expect(mockedRevealKey).toHaveBeenCalledTimes(1)

    // Repeat call must hit cache, not re-hit /api/keys.
    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-tenant-a-secret')
    expect(mockedGetKeys).toHaveBeenCalledTimes(1)
    expect(mockedRevealKey).toHaveBeenCalledTimes(1)
  })

  it('does NOT leak tenant A key to tenant B after a user/tenant swap', async () => {
    // Tenant A logs in, reveals a key.
    setUserInfo(tenantA)
    store.jwtToken = 'jwt-tenant-a'
    mockedGetKeys.mockResolvedValueOnce(fakeKeyList('tenant-a'))
    mockedRevealKey.mockResolvedValueOnce({ key_id: 100, api_key: 'sk-tenant-a-secret' })
    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-tenant-a-secret')

    // Switch to tenant B in the SAME SPA mount — no reload.
    setUserInfo(tenantB)
    store.jwtToken = 'jwt-tenant-b'
    mockedGetKeys.mockResolvedValueOnce(fakeKeyList('tenant-b'))
    mockedRevealKey.mockResolvedValueOnce({ key_id: 200, api_key: 'sk-tenant-b-secret' })
    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-tenant-b-secret')

    // Switching back to tenant A again must NOT serve the stale cache
    // either — the cache key changed in both directions.
    setUserInfo(tenantA)
    store.jwtToken = 'jwt-tenant-a'
    mockedGetKeys.mockResolvedValueOnce(fakeKeyList('tenant-a'))
    mockedRevealKey.mockResolvedValueOnce({ key_id: 100, api_key: 'sk-tenant-a-secret' })
    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-tenant-a-secret')
  })

  it('clearJwt drops the cache so the next login starts cold', async () => {
    setUserInfo(tenantA)
    store.jwtToken = 'jwt-tenant-a'
    mockedGetKeys.mockResolvedValueOnce(fakeKeyList('tenant-a'))
    mockedRevealKey.mockResolvedValueOnce({ key_id: 100, api_key: 'sk-tenant-a-secret' })
    await simulateAutoRoute('hi', 'smart')
    expect(mockedGetKeys).toHaveBeenCalledTimes(1)
    expect(mockedRevealKey).toHaveBeenCalledTimes(1)

    clearJwt()
    setUserInfo(tenantA)
    store.jwtToken = 'jwt-tenant-a'
    mockedGetKeys.mockResolvedValueOnce(fakeKeyList('tenant-a'))
    mockedRevealKey.mockResolvedValueOnce({ key_id: 100, api_key: 'sk-tenant-a-secret' })
    await simulateAutoRoute('hi', 'smart')
    // Re-fetched → cache was cleared on logout.
    expect(mockedGetKeys).toHaveBeenCalledTimes(2)
    expect(mockedRevealKey).toHaveBeenCalledTimes(2)
  })

  it('sk-* apiKey bypasses the cache entirely', async () => {
    // Legacy apiKey mode: no JWT, an sk-* in store.apiKey.
    store.apiKey = 'sk-explicit'
    await simulateAutoRoute('hi', 'smart')
    expect(lastBearerFromFetch()).toBe('sk-explicit')
    // Crucially, NO backend call must be issued just because the caller
    // gave us a known sk-* — otherwise we leak that the admin is
    // probing /api/keys for an unrelated tenant.
    expect(mockedGetKeys).not.toHaveBeenCalled()
    expect(mockedRevealKey).not.toHaveBeenCalled()
  })
})
