import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, bindAuthContext } from './client'
import { resetTransport, setTransport, type TransportRequest } from './transport'
import {
  fetchSanitizeRulesCurrent, fetchSanitizeRuleVersions, fetchSanitizeRuleEvents, fetchSanitizeRuleInstances,
  sanitizeRulesCanManage, sanitizeRulesCanReadEvents,
  type SanitizeRule, type SanitizeRuleCurrent, type SanitizeRuleVersion, type SanitizeRuleEvent, type SanitizeRuleInstance,
} from './sanitizeRules'

// 经真实 req → transport → JSON.parse 接缝测读面，不 mock client，也不只测类型。
const time = '2026-10-16T00:00:00Z'
const rule = (): SanitizeRule => ({
  id: 'phone-rule', name: '电话号码', type: 'phone', kind: 'pattern', pattern: '\\d+',
  group: 0, scopes: ['input', 'output'], action: 'mask', enabled: true, priority: -1,
})
const instance = (): SanitizeRuleInstance => ({
  instance_id: 'gateway:1', version: 0, config_digest: 'digest', last_seen: time,
  // Go time.Time 零值是字符串，而不是数据库层的 NULL。
  last_refresh: '0001-01-01T00:00:00Z', error: '',
})
const current = (): SanitizeRuleCurrent => ({ version: 0, config: { rules: [rule()] }, config_digest: 'digest', status: instance() })
const version = (): SanitizeRuleVersion => ({ id: 1, parent_id: 0, config: { rules: [rule()] }, actor: 'admin_key', comment: '', created_at: time })
const event = (): SanitizeRuleEvent => ({
  id: 1, fingerprint: 'fingerprint', tenant_id: '', rule_id: 'phone-rule', version: 0,
  phase: 'input', action: 'mask', status: 'pending', sample_masked: '[REDACTED:phone]',
  count: 1, first_seen: time, last_seen: time,
})

let body: unknown
let raw: string | undefined
const transport = vi.fn(async (_request: TransportRequest) => ({
  ok: true, status: 200, statusText: 'OK', text: async () => raw ?? JSON.stringify(body),
}))
beforeEach(() => {
  body = current()
  raw = undefined
  transport.mockClear()
  bindAuthContext({ getUserInfo: () => null, getBearer: () => '', isAuthenticated: () => false })
  setTransport(transport)
})
afterEach(() => resetTransport())

const readers = [
  { name: 'current', fetch: () => fetchSanitizeRulesCurrent(), fixture: () => current(), path: '/api/admin/sanitize-rules' },
  { name: 'versions', fetch: () => fetchSanitizeRuleVersions(), fixture: () => ({ versions: [version()] }), path: '/api/admin/sanitize-rules/versions' },
  { name: 'events', fetch: () => fetchSanitizeRuleEvents(), fixture: () => ({ events: [event()] }), path: '/api/admin/sanitize-rules/events?limit=100' },
  { name: 'instances', fetch: () => fetchSanitizeRuleInstances(), fixture: () => ({ target_version: 0, instances: [instance()] }), path: '/api/admin/sanitize-rules/instances' },
]

describe('四 GET 的真实传输与响应信封', () => {
  for (const reader of readers) {
    it(`${reader.name} 请求准确路径，无写请求体，完整响应不丢字段`, async () => {
      body = reader.fixture()
      expect(await reader.fetch()).toEqual(body)
      expect(transport).toHaveBeenCalledTimes(1)
      expect(transport.mock.calls[0]![0]).toMatchObject({ method: 'GET', path: reader.path, credentials: 'same-origin' })
      expect(transport.mock.calls[0]![0].body).toBeUndefined()
    })
    for (const invalid of [null, [], 'bad', 5, {}, { unexpected: [] }]) {
      it(`${reader.name} 拒绝错误信封 ${JSON.stringify(invalid)}`, async () => {
        body = invalid
        await expect(reader.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      })
    }
    it(`${reader.name} 非 JSON 也抛稳定翻译键`, async () => {
      raw = '<html>bad gateway</html>'
      await expect(reader.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
    it(`${reader.name} 空 body 不冒充空集`, async () => {
      raw = ''
      await expect(reader.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  for (const [key, fetch] of [
    ['versions', () => fetchSanitizeRuleVersions()],
    ['events', () => fetchSanitizeRuleEvents()],
    ['instances', () => fetchSanitizeRuleInstances()],
  ] as const) {
    it(`${key} 接受真实 Store 的 []，拒绝非真实 nil/null`, async () => {
      body = { [key]: [], ...(key === 'instances' ? { target_version: 0 } : {}) }
      expect(await fetch()).toEqual(body)
      body = { [key]: null, ...(key === 'instances' ? { target_version: 0 } : {}) }
      await expect(fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
})

// 各行所有必需字段都经传输逐个删除、改错类型，保证不静默过滤坏行。
const rowCases = [
  { name: 'rule', row: rule, wrap: (r: unknown) => ({ ...current(), config: { rules: [rule(), r] } }), fetch: () => fetchSanitizeRulesCurrent() },
  { name: 'current', row: current, wrap: (r: unknown) => r, fetch: () => fetchSanitizeRulesCurrent() },
  { name: 'version', row: version, wrap: (r: unknown) => ({ versions: [version(), r] }), fetch: () => fetchSanitizeRuleVersions() },
  { name: 'event', row: event, wrap: (r: unknown) => ({ events: [event(), r] }), fetch: () => fetchSanitizeRuleEvents() },
  { name: 'instance', row: instance, wrap: (r: unknown) => ({ target_version: 0, instances: [instance(), r] }), fetch: () => fetchSanitizeRuleInstances() },
]
describe('每个必需字段与整行验证', () => {
  for (const rowCase of rowCases) {
    for (const [key, value] of Object.entries(rowCase.row())) {
      it(`${rowCase.name}.${key} 不可缺键或错误类型`, async () => {
        const row = { ...rowCase.row() } as Record<string, unknown>
        delete row[key]
        body = rowCase.wrap(row)
        await expect(rowCase.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
        row[key] = typeof value === 'string' ? 3 : typeof value === 'number' ? '3' : null
        body = rowCase.wrap(row)
        await expect(rowCase.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      })
    }
    it(`${rowCase.name} 行为 null 也整笔拒绝，不静默 drop`, async () => {
      body = rowCase.wrap(null)
      await expect(rowCase.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  for (const rules of [null, [], {}, [null]]) {
    it(`config.rules=${JSON.stringify(rules)} 非 Compile 可构造配置`, async () => {
      body = { ...current(), config: { rules } }
      await expect(fetchSanitizeRulesCurrent()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      body = { versions: [{ ...version(), config: { rules } }] }
      await expect(fetchSanitizeRuleVersions()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  it('嵌套 status 必需字段也完整验证', async () => {
    for (const key of Object.keys(instance())) {
      const status = { ...instance() } as Record<string, unknown>
      delete status[key]
      body = { ...current(), status }
      await expect(fetchSanitizeRulesCurrent()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
  })
  it('instances.target_version 必须存在且为非负安全整数', async () => {
    for (const target_version of [undefined, null, -1, 0.5, '1', Number.MAX_SAFE_INTEGER + 1]) {
      body = { target_version, instances: [] }
      await expect(fetchSanitizeRuleInstances()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
  })
})

describe('omitempty 字段：可缺省，存在才验证', () => {
  const optionals = [
    { rowCase: rowCases[0]!, fields: { tenant_id: '租户', tools: ['工具'], fields: ['字段'], samples: [{ text: '样本', match: true }], exemptions: ['identity'] } },
    { rowCase: rowCases[2]!, fields: { rollback_of: 0, review_links: [{ id: 1, tenant_id: '租户' }] } },
    { rowCase: rowCases[3]!, fields: { type: '展示标签', field: '自由结构标识', reviewer: '审阅人', review_comment: '', reviewed_at: time, resolved_version: 0 } },
  ]
  for (const { rowCase, fields } of optionals) {
    it(`${rowCase.name} 所有合法可选字段保留，缺省不补假值`, async () => {
      body = rowCase.wrap({ ...rowCase.row(), ...fields })
      expect(await rowCase.fetch()).toEqual(body)
      body = rowCase.wrap(rowCase.row())
      expect(await rowCase.fetch()).toEqual(body)
    })
    for (const [key, value] of Object.entries(fields)) {
      it(`${rowCase.name}.${key} 存在时 null/错误类型拒绝`, async () => {
        for (const bad of [null, typeof value === 'string' ? 3 : 'bad']) {
          body = rowCase.wrap({ ...rowCase.row(), [key]: bad })
          await expect(rowCase.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
        }
      })
    }
  }
  for (const bad of [
    { tools: ['ok', 1] }, { fields: [false] }, { exemptions: ['unknown'] },
    { samples: [null] }, { samples: [{ text: 'ok' }] }, { samples: [{ text: 1, match: true }] },
    { samples: [{ text: 'ok', match: 'true' }] },
  ]) {
    it(`rule 嵌套可选项拒绝 ${JSON.stringify(bad)}`, async () => {
      body = rowCases[0]!.wrap({ ...rule(), ...bad })
      await expect(fetchSanitizeRulesCurrent()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  it('可选空数组合法；review_links 每一行也验证', async () => {
    body = rowCases[0]!.wrap({ ...rule(), tools: [], fields: [], exemptions: [], samples: [] })
    expect(await fetchSanitizeRulesCurrent()).toEqual(body)
    body = { versions: [{ ...version(), review_links: [] }] }
    expect(await fetchSanitizeRuleVersions()).toEqual(body)
    for (const link of [null, {}, { id: 0, tenant_id: '' }, { id: 1, tenant_id: 1 }, { id: Number.MAX_SAFE_INTEGER + 1, tenant_id: '' }]) {
      body = { versions: [{ ...version(), review_links: [link] }] }
      await expect(fetchSanitizeRuleVersions()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
  })
  it('自由展示字符串无需非空、无需匹配枚举；未知扩展字段不丢失', async () => {
    body = { events: [{ ...event(), type: '', field: 'future_structural_label', reviewer: '', review_comment: '中文备注', extra: true }] }
    expect(await fetchSanitizeRuleEvents()).toEqual(body)
    body = { ...current(), status: { ...instance(), error: '任意诊断字符串' } }
    expect(await fetchSanitizeRulesCurrent()).toEqual(body)
  })
})

describe('数字精度、零版本与真实枚举', () => {
  const numericCases = [
    { fetch: () => fetchSanitizeRulesCurrent(), wrap: (n: number) => ({ ...current(), version: n }) },
    { fetch: () => fetchSanitizeRulesCurrent(), wrap: (n: number) => rowCases[0]!.wrap({ ...rule(), group: n }) },
    { fetch: () => fetchSanitizeRulesCurrent(), wrap: (n: number) => rowCases[0]!.wrap({ ...rule(), priority: n }) },
    ...['id', 'parent_id', 'rollback_of'].map((key) => ({ fetch: () => fetchSanitizeRuleVersions(), wrap: (n: number) => ({ versions: [{ ...version(), [key]: n }] }) })),
    ...['id', 'version', 'count', 'resolved_version'].map((key) => ({ fetch: () => fetchSanitizeRuleEvents(), wrap: (n: number) => ({ events: [{ ...event(), [key]: n }] }) })),
    { fetch: () => fetchSanitizeRuleInstances(), wrap: (n: number) => ({ target_version: 0, instances: [{ ...instance(), version: n }] }) },
  ]
  it('所有数字字段拒绝小数、非有限值和超出 JS 安全范围的整数', async () => {
    for (const test of numericCases) {
      for (const n of [0.5, NaN, Infinity, -Infinity, Number.MAX_SAFE_INTEGER + 1, Number.MIN_SAFE_INTEGER - 1]) {
        body = test.wrap(n)
        await expect(test.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
      }
    }
  })
  it('最大安全整数可精确展示，原始 int64 大整数必须拒绝', async () => {
    body = { events: [{ ...event(), count: Number.MAX_SAFE_INTEGER }] }
    expect((await fetchSanitizeRuleEvents()).events[0]!.count).toBe(Number.MAX_SAFE_INTEGER)
    raw = JSON.stringify({ events: [event()] }).replace('"count":1', '"count":9223372036854775807')
    await expect(fetchSanitizeRuleEvents()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
  })
  it('fallback 0 合法，但历史 id/事件 id/count 不可为 0', async () => {
    body = current()
    expect((await fetchSanitizeRulesCurrent()).version).toBe(0)
    body = { target_version: 0, instances: [instance()] }
    expect((await fetchSanitizeRuleInstances()).target_version).toBe(0)
    body = { events: [event()] }
    expect((await fetchSanitizeRuleEvents()).events[0]!.version).toBe(0)
    body = { versions: [{ ...version(), id: 0 }] }
    await expect(fetchSanitizeRuleVersions()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    for (const key of ['id', 'count']) {
      body = { events: [{ ...event(), [key]: 0 }] }
      await expect(fetchSanitizeRuleEvents()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
  })
  it('除 priority 可负，版本/id/group 均不可负', async () => {
    for (const test of numericCases.filter((_test, index) => index !== 2)) {
      body = test.wrap(-1)
      await expect(test.fetch()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    }
    body = current()
    expect((await fetchSanitizeRulesCurrent()).config.rules[0]!.priority).toBe(-1)
  })
  for (const bad of [{ type: 'unknown' }, { kind: 'unknown' }, { action: 'pending' }, { scopes: [] }, { scopes: ['INPUT'] }, { scopes: [1] }]) {
    it(`rule 封闭域拒绝 ${JSON.stringify(bad)}`, async () => {
      body = rowCases[0]!.wrap({ ...rule(), ...bad })
      await expect(fetchSanitizeRulesCurrent()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
  for (const bad of [{ phase: 'unknown' }, { action: 'MASK' }, { status: 'done' }]) {
    it(`event 封闭域拒绝 ${JSON.stringify(bad)}`, async () => {
      body = { events: [{ ...event(), ...bad }] }
      await expect(fetchSanitizeRuleEvents()).rejects.toThrow(/^sanitizeRules\.invalidResponse$/)
    })
  }
})

describe('事件 URL 过滤：真实枚举、整数 limit，不拼 tenant_id', () => {
  it('所有合法 action/status 原样传输且合法响应均通过', async () => {
    for (const action of ['mask', 'block', 'log', 'exempted', 'pending']) {
      for (const status of ['pending', 'confirmed_sensitive', 'false_positive', 'ignored']) {
        body = { events: [{ ...event(), action, status }] }
        expect(await fetchSanitizeRuleEvents({ action, status, limit: 1000 })).toEqual(body)
        expect(transport.mock.calls.at(-1)![0].path).toBe(`/api/admin/sanitize-rules/events?action=${action}&status=${status}&limit=1000`)
      }
    }
  })
  it('空值不发送，未知 tenant_id 属性也不发送，limit=1 合法', async () => {
    body = { events: [] }
    await fetchSanitizeRuleEvents({ action: '', status: '', limit: 1, ...{ tenant_id: 'other-tenant' } })
    expect(transport.mock.calls[0]![0].path).toBe('/api/admin/sanitize-rules/events?limit=1')
  })
  for (const filter of [
    { action: 'MASK' }, { action: ' mask' }, { action: 'mask&tenant_id=other' }, { status: 'done' },
    { status: 'Pending' }, { action: null }, { status: false },
    ...[0, -1, 1001, 1.5, NaN, Infinity, '10', null].map((limit) => ({ limit })),
  ]) {
    it(`非法过滤 ${JSON.stringify(filter)} 拒绝且不发送请求`, async () => {
      await expect(fetchSanitizeRuleEvents(filter as Parameters<typeof fetchSanitizeRuleEvents>[0])).rejects.toThrow(/^sanitizeRules\.invalidFilter$/)
      expect(transport).not.toHaveBeenCalled()
    })
  }
})

describe('权限函数精确角色与租户组合（视图调用同一函数）', () => {
  for (const role of [null, '', 'super_admin', 'admin_key', 'tenant_admin', 'user', 'admin', 'SUPER_ADMIN', ' super_admin', 'tenant_admin ']) {
    for (const tenantID of [null, '', 'tenant-1', ' ']) {
      it(`role=${JSON.stringify(role)} tenant=${JSON.stringify(tenantID)}`, () => {
        const superRole = role === 'super_admin' || role === 'admin_key'
        expect(sanitizeRulesCanManage(role)).toBe(superRole)
        // 服务端比较 != ""，不擅自 trim 改变判据。
        expect(sanitizeRulesCanReadEvents(role, tenantID)).toBe(superRole || (role === 'tenant_admin' && tenantID !== null && tenantID !== ''))
      })
    }
  }
})

describe('AbortSignal 四请求透传及错误不混淆', () => {
  const calls = [
    { fetch: (signal: AbortSignal) => fetchSanitizeRulesCurrent({ signal }), fixture: current },
    { fetch: (signal: AbortSignal) => fetchSanitizeRuleVersions({ signal }), fixture: () => ({ versions: [] }) },
    { fetch: (signal: AbortSignal) => fetchSanitizeRuleEvents({}, { signal }), fixture: () => ({ events: [] }) },
    { fetch: (signal: AbortSignal) => fetchSanitizeRuleInstances({ signal }), fixture: () => ({ target_version: 0, instances: [] }) },
  ]
  calls.forEach((call, index) => {
    it(`请求 ${index + 1} 成功与取消均保留原 signal/AbortError`, async () => {
      const controller = new AbortController()
      body = call.fixture()
      await call.fetch(controller.signal)
      expect(transport.mock.calls[0]![0].signal).toBe(controller.signal)
      // 既有 client 仅原样保留 Error-shaped AbortError；不在本任务扩改共享管线。
      const aborted = Object.assign(new Error('取消'), { name: 'AbortError' })
      setTransport(async (request) => {
        expect(request.signal).toBe(controller.signal)
        if (request.signal?.aborted) throw aborted
        return new Promise((_resolve, reject) => request.signal?.addEventListener('abort', () => reject(aborted), { once: true }))
      })
      const pending = call.fetch(controller.signal)
      controller.abort()
      await expect(pending).rejects.toBe(aborted)
      await expect(call.fetch(controller.signal)).rejects.toBe(aborted)
    })
  })
  it('HTTP 与网络错误保留 client 语义，不伪报坏响应', async () => {
    setTransport(async () => ({ ok: false, status: 403, statusText: 'Forbidden', text: async () => '{"error":{"detail":"denied"}}' }))
    await expect(fetchSanitizeRulesCurrent()).rejects.toMatchObject({ status: 403, detail: 'denied' })
    setTransport(async () => { throw new TypeError('offline') })
    await expect(fetchSanitizeRulesCurrent()).rejects.toBeInstanceOf(ApiError)
    await expect(fetchSanitizeRulesCurrent()).rejects.toMatchObject({ status: 0, detail: 'network_error' })
  })
})
