import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, bindAuthContext } from './client'
import { resetTransport, setTransport, type TransportRequest } from './transport'
import { validateSanitizeRules, type SanitizeRule, type SanitizeRuleConfig } from './sanitizeRules'

// 真实 validate → req → transport → JSON.parse；不 mock API/client 导出。
const digest = '0123456789abcdef'.repeat(4)
const rule = (): SanitizeRule => ({
  id: 'custom-rule', name: '自定义规则', type: 'custom', kind: 'pattern', pattern: '(secret)',
  group: 1, scopes: ['input', 'output', 'egress'], action: 'log', enabled: true, priority: -1,
})
const config = (): SanitizeRuleConfig => ({ rules: [rule()] })
const fullRule = (): SanitizeRule => ({
  ...rule(), tenant_id: 'tenant-1', tools: ['shell'], fields: ['password'],
  samples: [{ text: 'secret', match: true }, { text: 'public', match: false }],
  exemptions: ['inert_tool', 'nonroutable_destination', 'identity'],
})
let responseBody: unknown
let raw: string | undefined
const transport = vi.fn(async (_request: TransportRequest) => ({
  ok: true, status: 200, statusText: 'OK', text: async () => raw ?? JSON.stringify(responseBody),
}))
beforeEach(() => {
  responseBody = { valid: true, config_digest: digest }
  raw = undefined
  transport.mockClear()
  bindAuthContext({ getUserInfo: () => null, getBearer: () => '', isAuthenticated: () => false })
  setTransport(transport)
})
afterEach(() => resetTransport())

async function rejectsDraft(value: unknown) {
  await expect(validateSanitizeRules(value as SanitizeRuleConfig)).rejects.toThrow(/^sanitizeRules\.invalidDraft$/)
  expect(transport).not.toHaveBeenCalled()
}

describe('验证 POST 真实请求契约与快照', () => {
  it('所有 JSON 字段原样提交，返回摘要并透传 signal；不修改 config', async () => {
    const draft = { rules: [fullRule()] }
    const original = JSON.stringify(draft)
    const signal = new AbortController().signal
    expect(await validateSanitizeRules(draft, { signal })).toEqual(responseBody)
    expect(transport).toHaveBeenCalledTimes(1)
    expect(transport.mock.calls[0]![0]).toMatchObject({
      method: 'POST', path: '/api/admin/sanitize-rules/validate', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' }, body: original,
    })
    expect(transport.mock.calls[0]![0].signal).toBe(signal)
    expect(JSON.stringify(draft)).toBe(original)
  })
  it('可选字段缺省不补值，options 可省略', async () => {
    const draft = config()
    await validateSanitizeRules(draft)
    expect(transport.mock.calls[0]![0].body).toBe(JSON.stringify(draft))
    expect(transport.mock.calls[0]![0].signal).toBeUndefined()
  })
  it('调用后更改嵌套规则、数组和样本不改变已提交 payload', async () => {
    const draft = { rules: [fullRule()] }
    const original = JSON.stringify(draft)
    const pending = validateSanitizeRules(draft)
    draft.rules[0]!.name = '修改'
    draft.rules[0]!.scopes.push('input')
    draft.rules[0]!.tools!.push('other')
    draft.rules[0]!.fields!.push('other')
    draft.rules[0]!.samples![0]!.text = '修改'
    draft.rules[0]!.exemptions!.pop()
    draft.rules.push(rule())
    await pending
    expect(transport.mock.calls[0]![0].body).toBe(original)
  })
  it('RE2 和语义不在客户端编译或规范化', async () => {
    const draft = { rules: [{ ...rule(), id: '', name: '', pattern: '(?=secret)', priority: 10001, scopes: ['input', 'input'] }] }
    await validateSanitizeRules(draft)
    expect(transport.mock.calls[0]![0].body).toBe(JSON.stringify(draft))
  })
  for (const type of ['phone', 'id_card', 'email', 'credit_card', 'secret', 'internal_ip', 'server_ip', 'account', 'name', 'custom']) {
    it(`合法 type ${type}`, async () => {
      await validateSanitizeRules({ rules: [{ ...rule(), type }] })
      expect(transport).toHaveBeenCalledTimes(1)
    })
  }
  for (const kind of ['pattern', 'review_combination', 'network_destination', 'vendor_key_shape', 'credential_declaration', 'credential_assignment', 'credential_pair', 'account_declaration', 'account_assignment']) {
    it(`合法 kind ${kind}`, async () => {
      await validateSanitizeRules({ rules: [{ ...rule(), kind }] })
      expect(transport).toHaveBeenCalledTimes(1)
    })
  }
  for (const action of ['mask', 'block', 'log']) {
    it(`合法 action ${action} 与 enabled false`, async () => {
      await validateSanitizeRules({ rules: [{ ...rule(), action, enabled: false }] })
      expect(transport).toHaveBeenCalledTimes(1)
    })
  }
  it('可选空数组与空字符串原样保留', async () => {
    const draft = { rules: [{ ...rule(), tenant_id: '', tools: [], fields: [], samples: [], exemptions: [] }] }
    await validateSanitizeRules(draft)
    expect(transport.mock.calls[0]![0].body).toBe(JSON.stringify(draft))
  })
})

describe('草稿结构严格校验与未知字段拒绝', () => {
  const badConfigs: unknown[] = [null, undefined, [], '', 1, false, {}, { rules: null }, { rules: {} }, { rules: [] }, { rules: [null] }, { rules: new Array(1) }]
  badConfigs.forEach((draft, index) => {
    it(`非法 config ${index + 1}`, async () => rejectsDraft(draft))
  })
  for (const [key, value] of Object.entries(rule())) {
    it(`必需字段 ${key} 缺失或类型错误`, async () => {
      const row: Record<string, unknown> = { ...rule() }
      delete row[key]
      await rejectsDraft({ rules: [row] })
      row[key] = typeof value === 'string' ? 3 : 'bad'
      await rejectsDraft({ rules: [row] })
    })
  }
  const badRules = [
    { type: 'unknown' }, { kind: 'unknown' }, { action: 'MASK' }, { enabled: null },
    ...[-1, 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1].map((group) => ({ group })),
    ...[0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1].map((priority) => ({ priority })),
    { scopes: [] }, { scopes: ['unknown'] }, { scopes: [null] },
    { tenant_id: 1 }, { tools: 'shell' }, { tools: [1] }, { fields: [false] },
    { samples: {} }, { samples: [null] }, { samples: [{ text: 'secret' }] },
    { samples: [{ match: true }] }, { samples: [{ text: 1, match: true }] },
    { samples: [{ text: 'secret', match: 'true' }] }, { exemptions: ['unknown'] },
  ]
  badRules.forEach((row, index) => {
    it(`非法 rule shape ${index + 1}`, async () => rejectsDraft({ rules: [{ ...rule(), ...row }] }))
  })
  for (const key of ['tenant_id', 'tools', 'fields', 'samples', 'exemptions']) {
    for (const value of [null, undefined]) {
      it(`可选字段 ${key} 显式 ${String(value)} 不因 JSON 省略被接受`, async () => {
        await rejectsDraft({ rules: [{ ...rule(), [key]: value }] })
      })
    }
  }
  for (const [level, draft] of [
    ['config', { ...config(), extra: true }],
    ['rule', { rules: [{ ...rule(), extra: true }] }],
    ['sample', { rules: [{ ...rule(), samples: [{ text: 'secret', match: true, extra: true }] }] }],
    ['undefined config', { ...config(), extra: undefined }],
    ['undefined rule', { rules: [{ ...rule(), extra: undefined }] }],
    ['undefined sample', { rules: [{ ...rule(), samples: [{ text: 'secret', match: true, extra: undefined }] }] }],
  ] as const) {
    it(`${level} 未知键拒绝而非剥除`, async () => rejectsDraft(draft))
  }
  it('序列化后结构也必须有效（自定义 toJSON 不可改变配置）', async () => {
    const draft = config()
    Object.defineProperty(draft, 'toJSON', { value: () => ({ rules: [] }) })
    await rejectsDraft(draft)
  })
})

// 用 name 扩大 JSON，仅验证传输边界；服务端名称/规则数量等语义限制不在这里复制。
describe('实际 JSON UTF-8 2 MiB 边界', () => {
  const maximum = 2 << 20
  function sizedConfig(bytes: number, character: string): SanitizeRuleConfig {
    const draft = { rules: [{ ...rule(), name: '' }] }
    const overhead = new TextEncoder().encode(JSON.stringify(draft)).length
    const width = new TextEncoder().encode(JSON.stringify(character)).length - 2
    const count = Math.floor((bytes - overhead) / width)
    draft.rules[0]!.name = character.repeat(count) + 'a'.repeat(bytes - overhead - count * width)
    expect(new TextEncoder().encode(JSON.stringify(draft)).length).toBe(bytes)
    return draft
  }
  for (const [name, character] of [['ASCII', 'a'], ['中文', '中'], ['emoji', '😀'], ['JSON 转义', '"']] as const) {
    for (const offset of [-1, 0, 1]) {
      it(`${name} ${maximum + offset} 字节`, async () => {
        const draft = sizedConfig(maximum + offset, character)
        if (offset > 0) await rejectsDraft(draft)
        else {
          await validateSanitizeRules(draft)
          expect(transport.mock.calls[0]![0].body).toBe(JSON.stringify(draft))
        }
      })
    }
  }
})

describe('成功响应必须 valid true 和真实摘要形状', () => {
  const badResponses: unknown[] = [null, [], 'bad', 5, false, {}, { valid: true },
    ...[false, null, undefined, 'true', 1].map((valid) => ({ valid, config_digest: digest })),
    ...[null, 1, '', 'a'.repeat(63), 'a'.repeat(65), 'A'.repeat(64), 'g'.repeat(64), ` ${digest}`, `${digest}\n`].map((config_digest) => ({ valid: true, config_digest })),
  ]
  badResponses.forEach((body, index) => {
    it(`非法响应 ${index + 1}`, async () => {
      responseBody = body
      await expect(validateSanitizeRules(config())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  })
  for (const text of ['<html>bad gateway</html>', '{', '', 'undefined']) {
    it(`非法 JSON ${JSON.stringify(text)} 映射稳定键`, async () => {
      raw = text
      await expect(validateSanitizeRules(config())).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
})

describe('HTTP 与取消保持共享 client 语义', () => {
  for (const status of [400, 403, 503]) {
    it(`HTTP ${status} 保留 ApiError/status/detail/code`, async () => {
      setTransport(async () => ({
        ok: false, status, statusText: 'Failure',
        text: async () => '{"error":{"detail":"denied","code":"validate_denied"}}',
      }))
      const error = await validateSanitizeRules(config()).catch((error: unknown) => error)
      expect(error).toBeInstanceOf(ApiError)
      expect(error).toMatchObject({ status, detail: 'denied', code: 'validate_denied' })
    })
  }
  it('非 JSON HTTP 错误保留原 HTTP 语义', async () => {
    setTransport(async () => ({ ok: false, status: 503, statusText: 'Unavailable', text: async () => '<html>upstream</html>' }))
    await expect(validateSanitizeRules(config())).rejects.toMatchObject({ status: 503, detail: '<html>upstream</html>' })
  })
  it('进行中与预先取消均保留 signal 和 AbortError 实例', async () => {
    const controller = new AbortController()
    const aborted = Object.assign(new Error('取消'), { name: 'AbortError' })
    setTransport(async (request) => {
      expect(request.signal).toBe(controller.signal)
      if (request.signal?.aborted) throw aborted
      return new Promise((_resolve, reject) => request.signal?.addEventListener('abort', () => reject(aborted), { once: true }))
    })
    const pending = validateSanitizeRules(config(), { signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toBe(aborted)
    await expect(validateSanitizeRules(config(), { signal: controller.signal })).rejects.toBe(aborted)
  })
  it('网络错误保持 client network_error，不误映射 invalidResponse', async () => {
    setTransport(async () => { throw new TypeError('offline') })
    await expect(validateSanitizeRules(config())).rejects.toMatchObject({ status: 0, detail: 'network_error' })
  })
  it('读取响应时其它错误原实例透传', async () => {
    const failure = new Error('body read failure')
    setTransport(async () => ({ ok: true, status: 200, statusText: 'OK', text: async () => { throw failure } }))
    await expect(validateSanitizeRules(config())).rejects.toBe(failure)
  })
})
