import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { publishSanitizeRules } from './sanitizeRules'
import { setTransport, resetTransport, type TransportRequest, type TransportResponse } from './transport'
import { locale, setLocale } from '@/i18n'
const originalLocale = locale.value
setLocale('zh-CN')
const config = () => ({ rules: [{ id: 'one', name: 'One', type: 'custom', kind: 'pattern', pattern: 'token', group: 0, scopes: ['input'], action: 'mask', enabled: true, priority: 0 }] })
const version = (overrides: Record<string, unknown> = {}) => ({ id: 8, parent_id: 7, config: config(), actor: 'owner', comment: 'publish', created_at: '2026-10-16T00:00:00Z', ...overrides })
const response = (data: unknown): TransportResponse => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
let calls: TransportRequest[]
let stub: (req: TransportRequest) => Promise<TransportResponse>
const transport = vi.fn<(req: TransportRequest) => Promise<TransportResponse>>()
beforeEach(() => { calls = []; transport.mockReset(); stub = async req => { calls.push(req); return transport(req) }; setTransport(stub) })
afterEach(() => { resetTransport(); setLocale(originalLocale) })
describe('发布：请求白名单与响应严格校验', () => {
  it('合法请求：base/config/comment 三键、snapshot 复制、response 完整', async () => {
    transport.mockResolvedValueOnce(response(version()))
    const body = { base_version: 7, config: config(), comment: '发布说明' }
    const original = JSON.stringify(body)
    const result = await publishSanitizeRules(body)
    expect(calls).toHaveLength(1); expect(calls[0]!.method).toBe('POST'); expect(calls[0]!.path).toBe('/api/admin/sanitize-rules/publish'); expect(JSON.parse(calls[0]!.body!)).toEqual({ base_version: 7, config: config(), comment: '发布说明' })
    expect(JSON.stringify(body)).toBe(original)
    expect(result.id).toBe(8)
  })
  it('合法请求允许 review_event_ids 透传：去重且 ≤100', async () => {
    transport.mockResolvedValueOnce(response(version()))
    await publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok', review_event_ids: [1, 2, 3] })
    expect(JSON.parse(calls[0]!.body!).review_event_ids).toEqual([1, 2, 3])
  })
  it.each([0, -1, 1.5, '7', null, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])('非法 base_version %s 拒绝且不发请求', async value => {
    await expect(publishSanitizeRules({ base_version: value as number, config: config(), comment: 'ok' } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['', '   ', null, undefined, 1, true, {}, []])('非法 comment %s 拒绝', async value => {
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: value as string } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['a'.repeat(2001), '中'.repeat(667), `${'中'.repeat(666)}abc`])('超长 comment（%d字符）拒绝', async comment => {
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment })).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each(['a'.repeat(2000), '中'.repeat(666), `${'中'.repeat(666)}ab`])('合法边界 comment（%d字符）通过', async comment => {
    transport.mockResolvedValueOnce(response(version()))
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment })).resolves.toBeDefined()
  })
  it.each([
    { base_version: 7, config: config(), comment: 'ok', extra: true },
    { base_version: 7, config: config() },
    { base_version: 7, config: config(), comment: 'ok', review_event_ids: 'a' },
    { base_version: 7, config: config(), comment: 'ok', review_event_ids: [0, 1] },
    { base_version: 7, config: config(), comment: 'ok', review_event_ids: [1, 1] },
    { base_version: 7, config: config(), comment: 'ok', review_event_ids: Array.from({ length: 101 }, (_, i) => i + 1) },
  ])('非法 review_event_ids/未知键拒绝：%o', async body => {
    await expect(publishSanitizeRules(body as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it('非法 config 拒绝', async () => {
    await expect(publishSanitizeRules({ base_version: 7, config: { rules: [] }, comment: 'ok' } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    await expect(publishSanitizeRules({ base_version: 7, config: { rules: [{ id: 'a' }] }, comment: 'ok' } as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([null, undefined, [], '', 1, {}, { base_version: 7 }, { config: config() }, { comment: 'ok' }])('非法 body 整体 %o 拒绝', async body => {
    await expect(publishSanitizeRules(body as never)).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it('config 序列化后亦须合法（自定义 toJSON 不可绕过）', async () => {
    const cfg = { ...config(), toJSON() { return { rules: [] } } } as unknown as ReturnType<typeof config>
    await expect(publishSanitizeRules({ base_version: 7, config: cfg, comment: 'ok' })).rejects.toThrow(/^sanitizeRules\.invalidPublish$/)
    expect(transport).not.toHaveBeenCalled()
  })
  it.each([null, [], 'bad', 5, {}, { id: 7 }, { id: 8, parent_id: -1, config: config(), actor: 'owner', comment: 'x', created_at: 'now' }, { id: 8, parent_id: 7, config: { rules: [] }, actor: 'owner', comment: 'x', created_at: 'now' }])('非法响应 %o 映射稳定键', async value => {
    transport.mockResolvedValueOnce(response(value))
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok' })).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
  })
  it.each(['{', '', 'null', '<html>bad</html>'])('非 JSON 响应 %s 映射稳定键', async text => {
    transport.mockResolvedValueOnce({ status: 200, statusText: 'OK', ok: true, text: async () => text })
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok' })).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
  })
  it.each([400, 403, 404, 409, 503])('HTTP %d 保留原 ApiError', async status => {
    transport.mockResolvedValueOnce({ status, statusText: 'Err', ok: false, text: async () => JSON.stringify({ error: 'x' }) })
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok' })).rejects.toMatchObject({ status })
  })
  it('网络错误保持 network_error', async () => {
    transport.mockRejectedValueOnce(new Error('network_error'))
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok' })).rejects.toThrow(/network_error/)
  })
  it('signal 透传，AbortError 实例原样', async () => {
    transport.mockImplementationOnce(() => new Promise((_r, j) => { setTimeout(() => j(Object.assign(new Error('aborted'), { name: 'AbortError' })), 0) }))
    const ctrl = new AbortController()
    ctrl.abort()
    await expect(publishSanitizeRules({ base_version: 7, config: config(), comment: 'ok' }, { signal: ctrl.signal })).rejects.toMatchObject({ name: 'AbortError' })
  })
  it('调用方修改嵌套字段不改变已发送 payload', async () => {
    transport.mockResolvedValueOnce(response(version()))
    const draft = { base_version: 7, config: config(), comment: 'ok' }
    const pending = publishSanitizeRules(draft)
    draft.config.rules[0]!.name = 'mut'
    draft.comment = 'mut2'
    await pending
    const sent = JSON.parse(calls[0]!.body!)
    expect(sent.config.rules[0].name).toBe('One'); expect(sent.comment).toBe('ok')
  })
})
