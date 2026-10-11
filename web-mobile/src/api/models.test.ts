import { beforeEach, describe, expect, it, vi } from 'vitest'
import { req } from './client'
import { saveFeaturedModels } from './models'

vi.mock('./client', () => ({ req: vi.fn() }))

const request = vi.mocked(req)

describe('saveFeaturedModels', () => {
  beforeEach(() => vi.clearAllMocks())

  it('accepts the backend PATCH acknowledgement and sends the configured model list', async () => {
    request.mockResolvedValueOnce({ status: 'ok' } as never)

    await expect(saveFeaturedModels(['gpt-4o', 'claude-3-5-sonnet'])).resolves.toEqual({ status: 'ok' })
    expect(request).toHaveBeenCalledWith('PATCH', '/api/routing/featured', {
      featured_models: ['gpt-4o', 'claude-3-5-sonnet'],
      actor: 'admin',
    }, undefined)
  })
})
