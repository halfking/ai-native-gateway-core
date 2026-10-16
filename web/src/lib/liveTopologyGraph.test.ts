// liveTopologyGraph.test.ts — 构图判据（2026-10-16）
//
// 覆盖三条真问题：身份键推导、健康态区分（unknown ≠ healthy）、列截断聚合。
// 每条都做过变异验证（改坏逻辑必须变红）。

import { describe, expect, it } from 'vitest'
import { buildTopology, clientKeyOf, healthOfNode, modelKeyOf, providerNodeKeyOf } from '../lib/liveTopologyGraph'
import { TOPO_COLUMN_LIMITS } from '../types/liveTopology'

const NOW = Date.parse('2026-10-16T12:00:00Z')
const MIN = 60_000

function req(over: Partial<Parameters<typeof buildTopology>[0]['requests'][number]> = {}) {
  return {
    ts: new Date(NOW - 5_000).toISOString(),
    request_id: 'r1',
    status: 'success',
    total_tokens: 100,
    agent_name: 'claude-code',
    provider_code: 'deepseek',
    canonical_name: 'glm-4.5',
    ...over,
  }
}

describe('liveTopologyGraph identity keys', () => {
  it('client key prefers agent_name, then profile, then protocol, then identity prefix', () => {
    expect(clientKeyOf(req({ agent_name: 'codex', client_profile: 'curl' }))).toBe('codex')
    expect(clientKeyOf({ client_profile: 'curl' })).toBe('curl')
    expect(clientKeyOf({ client_protocol: 'openai' })).toBe('openai')
    expect(clientKeyOf({ identity_hash: 'abcdef0123456789' })).toBe('abcdef01')
    expect(clientKeyOf({})).toBe('unknown')
  })

  it('provider node is credential-level, falling back to provider code', () => {
    expect(providerNodeKeyOf(req({ credential_id: 7 }))).toBe('cred:7')
    expect(providerNodeKeyOf({ provider_code: 'moonshot' })).toBe('pcode:moonshot')
    expect(providerNodeKeyOf({})).toBe('')
  })

  it('model key prefers canonical name over raw model name', () => {
    expect(modelKeyOf(req({ canonical_name: 'glm-4.5', model: 'deepseek-chat' }))).toBe('glm-4.5')
    expect(modelKeyOf({ model: 'deepseek-chat' })).toBe('deepseek-chat')
    expect(modelKeyOf({})).toBe('unknown')
  })
})

describe('liveTopologyGraph health', () => {
  it('never reports healthy for a node the backend did not report on', () => {
    // 「没上报」必须是 unknown：画成绿灯就是假绿（判据纪律第 3 条的同类）。
    expect(healthOfNode(undefined)).toBe('unknown')
    expect(healthOfNode({ credential_id: 1 })).toBe('unknown')
  })

  it('maps circuit/availability/quota/health to the right state', () => {
    expect(healthOfNode({ credential_id: 1, circuit_state: 'open' })).toBe('down')
    expect(healthOfNode({ credential_id: 1, availability_state: 'unavailable' })).toBe('down')
    expect(healthOfNode({ credential_id: 1, health_status: 'unreachable' })).toBe('down')
    expect(healthOfNode({ credential_id: 1, quota_state: 'quota_exhausted' })).toBe('warning')
    expect(healthOfNode({ credential_id: 1, fp_disabled: true })).toBe('warning')
    expect(healthOfNode({ credential_id: 1, health_status: 'healthy' })).toBe('healthy')
  })
})

describe('buildTopology', () => {
  it('builds the four-column chain and counts window metrics', () => {
    const graph = buildTopology({
      requests: [req({ credential_id: 7, latency_ms: 900 })],
      nodes: [{ credential_id: 7, provider_code: 'deepseek', health_status: 'healthy', in_flight: 2 }],
      now: NOW,
      windowMs: 5 * MIN,
    })
    const byId = new Map(graph.nodes.map((n) => [n.id, n]))
    expect([...byId.keys()].sort()).toEqual(
      ['client:claude-code', 'gateway:core', 'cred:7', 'model:glm-4.5'].sort(),
    )
    expect(byId.get('cred:7')?.health).toBe('healthy')
    expect(byId.get('cred:7')?.metric.inFlight).toBe(2)
    expect(byId.get('gateway:core')?.metric.success).toBe(1)
    expect(graph.edges.map((e) => e.id).sort()).toEqual(
      ['client:claude-code->gateway:core', 'cred:7->model:glm-4.5', 'gateway:core->cred:7'].sort(),
    )
  })

  it('drops window-out requests and reports the count (empty graph must be explainable)', () => {
    const graph = buildTopology({
      requests: [req({ ts: new Date(NOW - 30 * MIN).toISOString() })],
      nodes: [],
      now: NOW,
      windowMs: 5 * MIN,
    })
    expect(graph.droppedByWindow).toBe(1)
    expect(graph.nodes.every((n) => n.metric.requests === 0)).toBe(true)
  })

  it('counts in_progress as in-flight but neither success nor failure', () => {
    const graph = buildTopology({
      requests: [req({ status: 'in_progress' })],
      nodes: [],
      now: NOW,
      windowMs: 5 * MIN,
    })
    const core = graph.nodes.find((n) => n.column === 'gateway')!
    expect(core.metric.requests).toBe(1)
    expect(core.metric.inFlight).toBe(1)
    expect(core.metric.success).toBe(0)
    expect(core.metric.failure).toBe(0)
  })

  it('keeps a provider node that has health but no traffic yet (neutral, not green)', () => {
    const graph = buildTopology({
      requests: [],
      nodes: [{ credential_id: 99, credential_label: 'ds-key-2', provider_code: 'deepseek', health_status: 'healthy' }],
      now: NOW,
      windowMs: 5 * MIN,
    })
    const node = graph.nodes.find((n) => n.id === 'cred:99')
    expect(node?.live).toBe(true)
    expect(node?.metric.requests).toBe(0)
  })

  it('collapses overflow per column into one aggregate node', () => {
    const requests = Array.from({ length: TOPO_COLUMN_LIMITS.model + 4 }, (_, i) =>
      req({ request_id: `r${i}`, model: `m${i}`, canonical_name: '' }),
    )
    const graph = buildTopology({ requests, nodes: [], now: NOW, windowMs: 5 * MIN })
    const models = graph.nodes.filter((n) => n.column === 'model')
    expect(models.length).toBe(TOPO_COLUMN_LIMITS.model + 1)
    const aggregate = models[models.length - 1]
    expect(aggregate.isAggregate).toBe(true)
    expect(aggregate.label).toBe('+4')
    expect(aggregate.metric.requests).toBe(4)
  })
})
