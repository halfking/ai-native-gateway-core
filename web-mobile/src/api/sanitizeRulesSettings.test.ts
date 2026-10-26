import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { updateSanitizeRuleSettings, updateSetting } from './platformSettings'
import { setTransport, resetTransport, type TransportRequest, type TransportResponse } from './transport'
const response = (data: unknown): TransportResponse => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
let calls: TransportRequest[]
const transport = vi.fn<(req: TransportRequest) => Promise<TransportResponse>>()
beforeEach(() => { calls = []; transport.mockReset(); setTransport(async req => { calls.push(req); return transport(req) }) })
afterEach(() => { resetTransport() })
describe('updateSetting：单条 PUT', () => {
  it('合法 PUT：只传 value、白名单构造 body、response status/new_value/applied_at', async () => {
    transport.mockResolvedValueOnce(response({ status: 'ok', old_value: 5, new_value: 10, applied_at: '2026-10-16T00:00:00Z' }))
    const original = { key: 'sanitize.rules.refresh_interval_seconds', value: 10 }
    const result = await updateSetting(original)
    expect(calls).toHaveLength(1); expect(calls[0]!.method).toBe('PUT'); expect(calls[0]!.path).toBe('/api/admin/settings/sanitize.rules.refresh_interval_seconds')
    expect(JSON.parse(calls[0]!.body!)).toEqual({ value: 10 })
    expect(result).toEqual({ status: 'ok', old_value: 5, new_value: 10, applied_at: '2026-10-16T00:00:00Z' })
  })
  it.each(['', null, undefined, 0, {}, true, false])('非法 key %s 拒绝', async value => {
    try { await updateSetting({ key: value as never, value: 1 }); throw new Error('should not resolve') }
    catch (e) { expect((e as Error).message).toBe('settings.invalidKey') }
    expect(transport).not.toHaveBeenCalled()
  })
  it('非法 value undefined 拒绝', async () => {
    try { await updateSetting({ key: 'sanitize.rules.refresh_interval_seconds' } as never); throw new Error('should not resolve') }
    catch (e) { expect((e as Error).message).toBe('settings.invalidValue') }
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([null, [], 'bad', {}, { status: 'ok' }, { status: 'ok', new_value: 5 }, { status: 'ok', new_value: 5, applied_at: 1 }])('非法响应 %o 映射稳定键', async value => {
    transport.mockResolvedValueOnce(response(value))
    try { await updateSetting({ key: 'a', value: 1 }); throw new Error('should not resolve') }
    catch (e) { expect((e as Error).message).toBe('settings.invalidResponse') }
  })
  it.each([400, 403, 404, 409, 500, 503])('HTTP %d 保留原 ApiError', async status => {
    transport.mockResolvedValueOnce({ status, statusText: 'Err', ok: false, text: async () => JSON.stringify({ error: 'x' }) })
    await expect(updateSetting({ key: 'a', value: 1 })).rejects.toMatchObject({ status })
  })
  it('signal 透传', async () => {
    transport.mockImplementationOnce(() => new Promise((_r, j) => { setTimeout(() => j(Object.assign(new Error('aborted'), { name: 'AbortError' })), 0) }))
    const ctrl = new AbortController(); ctrl.abort()
    await expect(updateSetting({ key: 'a', value: 1 }, { signal: ctrl.signal })).rejects.toMatchObject({ name: 'AbortError' })
  })
})
describe('updateSanitizeRuleSettings：并发 PUT', () => {
  it('三键各自独立请求，缺键不发', async () => {
    transport.mockResolvedValueOnce(response({ status: 'ok', new_value: 10, applied_at: '2026-10-16T00:00:00Z' }))
    const result = await updateSanitizeRuleSettings({ refresh_interval_seconds: 10 })
    expect(calls).toHaveLength(1)
    expect(calls[0]!.path).toBe('/api/admin/settings/sanitize.rules.refresh_interval_seconds')
    expect(JSON.parse(calls[0]!.body!)).toEqual({ value: 10 })
    expect(result).toEqual({ refresh_interval_seconds: { status: 'ok', new_value: 10, applied_at: '2026-10-16T00:00:00Z' } })
  })
  it('三键并发，URL 顺序独立', async () => {
    transport.mockImplementation(async () => response({ status: 'ok', new_value: 5, applied_at: '2026-10-16T00:00:00Z' }))
    const result = await updateSanitizeRuleSettings({ refresh_interval_seconds: 10, audit_enabled: false, event_limit: 20000 })
    expect(calls).toHaveLength(3)
    const paths = calls.map(r => r.path).sort()
    expect(paths).toEqual(['/api/admin/settings/sanitize.rules.audit_enabled', '/api/admin/settings/sanitize.rules.event_limit', '/api/admin/settings/sanitize.rules.refresh_interval_seconds'])
    expect(result.refresh_interval_seconds).toBeDefined(); expect(result.audit_enabled).toBeDefined(); expect(result.event_limit).toBeDefined()
  })
  it('空对象不发请求', async () => {
    await expect(updateSanitizeRuleSettings({})).resolves.toEqual({})
    expect(transport).not.toHaveBeenCalled()
  })
  it('首个失败抛错', async () => {
    transport.mockResolvedValueOnce(response({ status: 'ok', new_value: 10, applied_at: '2026-10-16T00:00:00Z' }))
    transport.mockResolvedValueOnce({ status: 400, statusText: 'Bad', ok: false, text: async () => JSON.stringify({ error: 'bad' }) })
    await expect(updateSanitizeRuleSettings({ refresh_interval_seconds: 10, audit_enabled: false })).rejects.toMatchObject({ status: 400 })
  })
  it('调用方修改更新对象不影响已发送 payload', async () => {
    transport.mockResolvedValue(response({ status: 'ok', new_value: 5, applied_at: '2026-10-16T00:00:00Z' }))
    const draft = { refresh_interval_seconds: 10, audit_enabled: false }
    const pending = updateSanitizeRuleSettings(draft)
    draft.refresh_interval_seconds = 99
    await pending
    const sent = calls.map(r => JSON.parse(r.body!).value)
    expect(sent).toEqual([10, false])
  })
})
