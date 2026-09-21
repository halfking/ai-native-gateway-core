import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './_core'
import { getVendorCredentialErrorDetail } from './vendor-credential-error'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

const detail = {
  credential_id: 42,
  credential_label: 'credential-a',
  credential: { id: 42, label: 'credential-a' },
  error_summary: [],
  recent_failures: [],
  quality_scores_7d: [],
  hours: 24,
  since: '2026-08-28T00:00:00Z',
}

describe('getVendorCredentialErrorDetail', () => {
  it('freezes the GET path/query and returns the response contract', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(detail), { status: 200 }))

    await expect(getVendorCredentialErrorDetail(42)).resolves.toEqual(detail)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/vendors/credentials/42/error-detail?hours=24',
      expect.objectContaining({ method: 'GET' }),
    )
  })

  it.each([
    [400, 'invalid_hours', 'hours must be 1, 24, or 168'],
    [404, 'credential_not_found', 'credential not found'],
    [500, 'credential_query_failed', 'failed to load credential detail'],
  ])('preserves the standard error envelope for %i', async (status, code, message) => {
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code, detail: message },
    }), { status, statusText: 'Error' }))

    await expect(getVendorCredentialErrorDetail(42, '1')).rejects.toMatchObject({
      status,
      detail: message,
    } satisfies Partial<ApiError>)
  })
})
