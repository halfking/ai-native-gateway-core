import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  getAttachmentSignedUrl,
  getSessionSnapshot,
  getSessionTurn,
  listSessionTurns,
  triggerInstantSummary,
} from './sessions_v2'

const reqMock = vi.fn()

vi.mock('./_core', () => ({
  req: (...args: unknown[]) => reqMock(...args),
}))

describe('sessions_v2 API', () => {
  beforeEach(() => {
    reqMock.mockReset()
  })

  it('lists turns with encoded session id, cursor, limit, and AbortSignal', async () => {
    const response = { session_id: 'session/1', turns: [], has_more: false, next_cursor: '' }
    const controller = new AbortController()
    reqMock.mockResolvedValue(response)

    await expect(
      listSessionTurns('session/1?', { cursor: 'after 1', limit: 25 }, { signal: controller.signal }),
    ).resolves.toBe(response)

    expect(reqMock).toHaveBeenCalledWith(
      'GET',
      '/api/admin/sessions/session%2F1%3F/turns?cursor=after+1&limit=25',
      undefined,
      { signal: controller.signal },
    )
  })

  it('omits optional query parameters when they are absent', async () => {
    reqMock.mockResolvedValue({ session_id: 's', turns: [], has_more: false, next_cursor: '' })

    await listSessionTurns('s')

    expect(reqMock).toHaveBeenCalledWith(
      'GET',
      '/api/admin/sessions/s/turns',
      undefined,
      undefined,
    )
  })

  it('passes abort rejection through unchanged', async () => {
    const controller = new AbortController()
    const abortError = new DOMException('The operation was aborted.', 'AbortError')
    reqMock.mockRejectedValue(abortError)

    await expect(listSessionTurns('s', {}, { signal: controller.signal })).rejects.toBe(abortError)
    expect(reqMock).toHaveBeenCalledWith(
      'GET',
      '/api/admin/sessions/s/turns',
      undefined,
      { signal: controller.signal },
    )
  })

  it.each([
    ['getSessionTurn', () => getSessionTurn('s/1', 7, { signal: new AbortController().signal }), 'GET', '/api/admin/sessions/s%2F1/turns/7'],
    ['getSessionSnapshot', () => getSessionSnapshot('s/1', { signal: new AbortController().signal }), 'GET', '/api/admin/sessions/s%2F1/snapshot'],
  ])('%s uses the expected GET endpoint', async (_name, call, method, path) => {
    const value = { ok: true }
    reqMock.mockResolvedValue(value)

    await expect(call()).resolves.toBe(value)

    expect(reqMock).toHaveBeenCalledWith(method, path, undefined, expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  it('triggers instant summary with POST and an empty JSON body', async () => {
    const value = { summary: 'done' }
    reqMock.mockResolvedValue(value)

    await expect(triggerInstantSummary('s/1')).resolves.toBe(value)

    expect(reqMock).toHaveBeenCalledWith(
      'POST',
      '/api/admin/sessions/s%2F1/instant-summary',
      {},
    )
  })

  it('gets an attachment signed URL with both path segments encoded', async () => {
    const value = { url: 'https://signed.example/file', expires_at: 123 }
    reqMock.mockResolvedValue(value)

    await expect(getAttachmentSignedUrl('s/1', 3, 'att/7?')).resolves.toBe(value)

    expect(reqMock).toHaveBeenCalledWith(
      'GET',
      '/api/admin/sessions/s%2F1/turns/3/attachments/att%2F7%3F/url',
    )
  })
})
