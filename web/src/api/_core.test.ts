import { afterEach, describe, expect, it, vi } from 'vitest'
import { req } from './_core'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

describe('req', () => {
  it('uses error.message from the standard admin error envelope', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: {
        message: 'a default with the same (task_type, profile, tier, tenant_id) already exists',
        type: 'admin_error',
      },
    }), { status: 409, statusText: 'Conflict' }))

    await expect(req('POST', '/api/admin/auto-route/defaults', {})).rejects.toThrow(
      'a default with the same (task_type, profile, tier, tenant_id) already exists',
    )
  })
})
