import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, bindAuthContext } from './client'
import { resetTransport, setTransport, type TransportRequest } from './transport'
import { reviewSanitizeRuleEvent, type SanitizeRuleEvent } from './sanitizeRules'

// 真实 review → req → transport → JSON.parse，不 mock API/client 导出。
const time = '2026-10-16T00:00:00Z'
const statuses = ['confirmed_sensitive', 'false_positive', 'ignored'] as const
const requestBody = () => ({ status: 'confirmed_sensitive' as const, comment: '复核备注' })
const event = (): SanitizeRuleEvent => ({
  id: 17, fingerprint: 'fingerprint', tenant_id: 'tenant-1', rule_id: 'phone-rule', version: 0,
  phase: 'input', action: 'mask', status: 'confirmed_sensitive', sample_masked: '[REDACTED:phone]',
  count: 1, first_seen: time, last_seen: time,
})
type ReviewBody = Parameters<typeof reviewSanitizeRuleEvent>[1]
let responseBody: unknown
let raw: string | undefined
const transport = vi.fn(async (_request: TransportRequest) => ({
  ok: true, status: 200, statusText: 'OK', text: async () => raw ?? JSON.stringify(responseBody),
}))
beforeEach(() => {
  responseBody = event()
  raw = undefined
  transport.mockClear()
  bindAuthContext({ getUserInfo: () => null, getBearer: () => '', isAuthenticated: () => false })
  setTransport(transport)
})
afterEach(() => resetTransport())

describe('复核 POST 真实请求契约', () => {
  for (const status of statuses) {
    for (const comment of ['', '中文复核备注']) {
      it(`${status} / ${comment === '' ? '空评论' : '中文评论'} 原样提交与返回`, async () => {
        responseBody = { ...event(), status, review_comment: comment }
        const signal = new AbortController().signal
        expect(await reviewSanitizeRuleEvent(17, { status, comment }, { signal })).toEqual(responseBody)
        expect(transport).toHaveBeenCalledTimes(1)
        const request = transport.mock.calls[0]![0]
        expect(request).toMatchObject({
          method: 'POST', path: '/api/admin/sanitize-rules/events/17/review', credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status, comment }),
        })
        expect(request.signal).toBe(signal)
      })
    }
  }
  it('额外租户、reviewer 与其它字段不透传，不修改调用方对象', async () => {
    const body = { ...requestBody(), tenant_id: 'other-tenant', tenant: 'other', reviewer: 'forged', id: 99, extra: true }
    const original = { ...body }
    await reviewSanitizeRuleEvent(17, body)
    expect(JSON.parse(transport.mock.calls[0]![0].body!)).toEqual(requestBody())
    expect(body).toEqual(original)
  })
  it('最大安全整数 ID 合法且路径不失真，options 可缺省', async () => {
    responseBody = { ...event(), id: Number.MAX_SAFE_INTEGER }
    expect(await reviewSanitizeRuleEvent(Number.MAX_SAFE_INTEGER, requestBody())).toEqual(responseBody)
    expect(transport.mock.calls[0]![0].path).toBe('/api/admin/sanitize-rules/events/9007199254740991/review')
    expect(transport.mock.calls[0]![0].signal).toBeUndefined()
  })
})

describe('UTF-8 评论字节边界（Go len）', () => {
  for (const [name, comment, allowed] of [
    ['ASCII 1999', 'a'.repeat(1999), true], ['ASCII 2000', 'a'.repeat(2000), true],
    ['ASCII 2001', 'a'.repeat(2001), false],
    ['中文 1998', '中'.repeat(666), true], ['中文混合 2000', `${'中'.repeat(666)}ab`, true],
    ['中文 2001', '中'.repeat(667), false], ['中文混合 2001', `${'中'.repeat(666)}abc`, false],
    ['emoji 2000', '😀'.repeat(500), true], ['emoji 2004', '😀'.repeat(501), false],
  ] as const) {
    it(name, async () => {
      if (allowed) {
        expect(await reviewSanitizeRuleEvent(17, { ...requestBody(), comment })).toEqual(responseBody)
        expect(JSON.parse(transport.mock.calls[0]![0].body!).comment).toBe(comment)
      } else {
        await expect(reviewSanitizeRuleEvent(17, { ...requestBody(), comment })).rejects.toThrow(/^sanitizeRules\.invalidReview$/)
        expect(transport).not.toHaveBeenCalled()
      }
    })
  }
})

describe('非法请求稳定错误键且不发送', () => {
  const invalidIDs: unknown[] = [0, -1, 1.5, NaN, Infinity, -Infinity, Number.MAX_SAFE_INTEGER + 1, '17', null, undefined, true, {}, []]
  invalidIDs.forEach((id, index) => {
    it(`非法 id ${index + 1}: ${String(id)}`, async () => {
      await expect(reviewSanitizeRuleEvent(id as number, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidReview$/)
      expect(transport).not.toHaveBeenCalled()
    })
  })
  const invalidStatuses: unknown[] = ['pending', '', 'CONFIRMED_SENSITIVE', 'false_positive ', ' ignored', 'done', 'ignored&tenant_id=other', null, undefined, 1, false, {}, []]
  invalidStatuses.forEach((status, index) => {
    it(`非法 status ${index + 1}`, async () => {
      await expect(reviewSanitizeRuleEvent(17, { ...requestBody(), status } as ReviewBody)).rejects.toThrow(/^sanitizeRules\.invalidReview$/)
      expect(transport).not.toHaveBeenCalled()
    })
  })
  const invalidComments: unknown[] = [null, undefined, 1, false, {}, [], new String('comment')]
  invalidComments.forEach((comment, index) => {
    it(`非法 comment ${index + 1}`, async () => {
      await expect(reviewSanitizeRuleEvent(17, { ...requestBody(), comment } as ReviewBody)).rejects.toThrow(/^sanitizeRules\.invalidReview$/)
      expect(transport).not.toHaveBeenCalled()
    })
  })
  for (const body of [null, undefined, [], '', 1, {}, { status: 'ignored' }, { comment: '' }]) {
    it(`非法 body ${JSON.stringify(body)}`, async () => {
      await expect(reviewSanitizeRuleEvent(17, body as unknown as ReviewBody)).rejects.toThrow(/^sanitizeRules\.invalidReview$/)
      expect(transport).not.toHaveBeenCalled()
    })
  }
})

describe('复用事件完整验证并核对 ID/status', () => {
  for (const bad of [null, [], 'bad', 5, {}, { events: [event()] }]) {
    it(`拒绝错误响应信封 ${JSON.stringify(bad)}`, async () => {
      responseBody = bad
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  for (const [key, value] of Object.entries(event())) {
    it(`必需字段 ${key} 缺失或错误类型整笔拒绝`, async () => {
      const row = { ...event() } as Record<string, unknown>
      delete row[key]
      responseBody = row
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      row[key] = typeof value === 'string' ? 3 : '3'
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  const invalidRows = [
    { id: 18 }, { id: 0 }, { id: Number.MAX_SAFE_INTEGER + 1 },
    ...['pending', 'false_positive', 'ignored', 'done'].map((status) => ({ status })),
    { phase: 'unknown' }, { action: 'MASK' }, { count: 0 }, { count: 0.5 }, { version: -1 },
  ]
  invalidRows.forEach((row, index) => {
    it(`非法事件或复核不一致 ${index + 1}`, async () => {
      responseBody = { ...event(), ...row }
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  })
  for (const text of ['<html>bad gateway</html>', '{', '', 'null']) {
    it(`非 JSON/空响应 ${JSON.stringify(text)} 映射稳定键`, async () => {
      raw = text
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  it('成功响应缺省可选字段不补值，合法可选字段及扩展字段保留', async () => {
    expect(await reviewSanitizeRuleEvent(17, requestBody())).toEqual(event())
    responseBody = {
      ...event(), type: '自由展示标签', field: '', reviewer: 'admin_key', review_comment: '',
      reviewed_at: time, resolved_version: 0, extra: true,
    }
    expect(await reviewSanitizeRuleEvent(17, requestBody())).toEqual(responseBody)
  })
  for (const key of ['type', 'field', 'reviewer', 'review_comment', 'reviewed_at', 'resolved_version']) {
    it(`可选字段 ${key} 存在时拒绝 null 或错误类型`, async () => {
      for (const bad of [null, key === 'resolved_version' ? '0' : 3]) {
        responseBody = { ...event(), [key]: bad }
        await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      }
    })
  }
  it('resolved_version 拒绝负数、小数与不安全整数', async () => {
    for (const resolved_version of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1]) {
      responseBody = { ...event(), resolved_version }
      await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
  })
})

describe('HTTP 与取消保持共享 client 语义', () => {
  for (const status of [400, 403, 404, 500]) {
    it(`HTTP ${status} 保留 ApiError/status/detail/code`, async () => {
      setTransport(async () => ({
        ok: false, status, statusText: 'Failure',
        text: async () => '{"error":{"detail":"denied","code":"review_denied"}}',
      }))
      const error = await reviewSanitizeRuleEvent(17, requestBody()).catch((error: unknown) => error)
      expect(error).toBeInstanceOf(ApiError)
      expect(error).toMatchObject({ status, detail: 'denied', code: 'review_denied' })
    })
  }
  it('非 JSON HTTP 错误不误映射 invalidResponse', async () => {
    setTransport(async () => ({ ok: false, status: 502, statusText: 'Bad Gateway', text: async () => '<html>upstream</html>' }))
    await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toMatchObject({ status: 502, detail: '<html>upstream</html>' })
  })
  it('网络失败保留 ApiError network_error，不误映射 invalidResponse', async () => {
    setTransport(async () => { throw new TypeError('offline') })
    await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toMatchObject({ status: 0, detail: 'network_error' })
  })
  it('进行中和预先取消均保留原 signal 及 Error-shaped AbortError 实例', async () => {
    const controller = new AbortController()
    const aborted = Object.assign(new Error('取消'), { name: 'AbortError' })
    setTransport(async (request) => {
      expect(request.signal).toBe(controller.signal)
      if (request.signal?.aborted) throw aborted
      return new Promise((_resolve, reject) => request.signal?.addEventListener('abort', () => reject(aborted), { once: true }))
    })
    const pending = reviewSanitizeRuleEvent(17, requestBody(), { signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toBe(aborted)
    await expect(reviewSanitizeRuleEvent(17, requestBody(), { signal: controller.signal })).rejects.toBe(aborted)
  })
  it('成功响应读取阶段的其它错误原实例透传', async () => {
    const failure = new Error('body read failure')
    setTransport(async () => ({ ok: true, status: 200, statusText: 'OK', text: async () => { throw failure } }))
    await expect(reviewSanitizeRuleEvent(17, requestBody())).rejects.toBe(failure)
  })
})
