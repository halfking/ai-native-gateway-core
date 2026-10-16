import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { rollbackSanitizeRules } from './sanitizeRules'
import { setTransport, resetTransport, type TransportRequest, type TransportResponse } from './transport'
import { locale, setLocale } from '@/i18n'
const originalLocale = locale.value
setLocale('zh-CN')
const config = () => ({ rules: [{ id: 'one', name: 'One', type: 'custom', kind: 'pattern', pattern: 'token', group: 0, scopes: ['input'], action: 'mask', enabled: true, priority: 0 }] })
const version = (overrides: Record<string, unknown> = {}) => ({ id: 9, parent_id: 7, config: config(), actor: 'owner', comment: 'rollback', created_at: '2026-10-16T00:00:00Z', rollback_of: 7, ...overrides })
const response = (data: unknown): TransportResponse => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
let calls: TransportRequest[]
const transport = vi.fn<(req: TransportRequest) => Promise<TransportResponse>>()
beforeEach(() => { calls = []; transport.mockReset(); setTransport(async req => { calls.push(req); return transport(req) }) })
afterEach(() => { resetTransport(); setLocale(originalLocale) })
describe('回滚：请求白名单与响应严格校验', () => {
  it('合法请求：base/target/comment 三键、target_version>0', async () => {
    transport.mockResolvedValueOnce(response(version()))
    const result = await rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: '退回' })
    expect(calls).toHaveLength(1); expect(calls[0]!.path).toBe('/api/admin/sanitize-rules/rollback'); expect(calls[0]!.method).toBe('POST')
    expect(JSON.parse(calls[0]!.body!)).toEqual({ base_version: 7, target_version: 5, comment: '退回' })
    expect(result.rollback_of).toBe(7)
  })
  it.each([0, -1, 1.5, '7', null, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])('非法 base_version %s 拒绝', async value => {
    await expect(rollbackSanitizeRules({ base_version: value as number, target_version: 5, comment: 'ok' } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([0, -1, 1.5, '5', null, NaN, Infinity])('非法 target_version %s 拒绝', async value => {
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: value as number, comment: 'ok' } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['', null, undefined, 1, true, {}, []])('非法 comment %s 拒绝', async value => {
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: value as string } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['a'.repeat(2001), '中'.repeat(667), `${'中'.repeat(666)}abc`])('超长 comment（%d）拒绝', async comment => {
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment })).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['a'.repeat(2000), '中'.repeat(666), `${'中'.repeat(666)}ab`])('合法边界 comment（%d）通过', async comment => {
    transport.mockResolvedValueOnce(response(version()))
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment })).resolves.toBeDefined()
  })
  it.each([
    { base_version: 7, target_version: 5, comment: 'ok', extra: true },
    { base_version: 7, target_version: 5, comment: 'ok', actor: 'x' },
    { base_version: 7, target_version: 5 },
    { base_version: 7, comment: 'ok' },
    { target_version: 5, comment: 'ok' },
  ])('非法 body/未知键 %o 拒绝', async body => {
    await expect(rollbackSanitizeRules(body as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([null, undefined, [], '', 1, {}])('非法 body 整体 %o 拒绝', async body => {
    await expect(rollbackSanitizeRules(body as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([null, [], 'bad', 5, {}, { id: 9, parent_id: 7, config: { rules: [] }, actor: 'owner', comment: 'x', created_at: 'now' }, { id: 9, parent_id: -1, config: config(), actor: 'owner', comment: 'x', created_at: 'now' }])('非法响应 %o 映射稳定键', async value => {
    transport.mockResolvedValueOnce(response(value))
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: 'ok' })).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
  })
  it.each([400, 403, 404, 409, 503])('HTTP %d 保留原 ApiError', async status => {
    transport.mockResolvedValueOnce({ status, statusText: 'Err', ok: false, text: async () => JSON.stringify({ error: 'x' }) })
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: 'ok' })).rejects.toMatchObject({ status })
  })
  it('网络错误保持 network_error', async () => {
    transport.mockRejectedValueOnce(new Error('network_error'))
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: 'ok' })).rejects.toThrow(/network_error/)
  })
  it('signal 透传', async () => {
    transport.mockImplementationOnce(() => new Promise((_r, j) => { setTimeout(() => j(Object.assign(new Error('aborted'), { name: 'AbortError' })), 0) }))
    const ctrl = new AbortController(); ctrl.abort()
    await expect(rollbackSanitizeRules({ base_version: 7, target_version: 5, comment: 'ok' }, { signal: ctrl.signal })).rejects.toMatchObject({ name: 'AbortError' })
  })
  it('调用方修改不影响已发送 payload', async () => {
    transport.mockResolvedValueOnce(response(version()))
    const draft = { base_version: 7, target_version: 5, comment: 'ok' }
    const pending = rollbackSanitizeRules(draft)
    draft.comment = 'mut'
    await pending
    expect(JSON.parse(calls[0]!.body!).comment).toBe('ok')
  })
})
