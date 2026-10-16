// liveTopologyFilter.test.ts — 过滤判据（2026-10-16）
//
// 「过滤后为空」有两种成因必须分开判：
//   (a) 真的没有请求；(b) 过滤器把它筛没了。
// 只要有一条允许侧断言，就必须同时钉一条「前提仍在场」的断言
// （判据纪律第 3 条的实证：只测允许侧会漏掉「压根没匹配上」）。

import { describe, expect, it } from 'vitest'
import { applyFilters, filterOptions, matchesFilters } from '../lib/liveTopologyFilter'
import { DEFAULT_TOPO_FILTERS } from '../types/liveTopology'

const NOW = Date.parse('2026-10-16T12:00:00Z')

const base = {
  ts: new Date(NOW - 1_000).toISOString(),
  request_id: 'r1',
  status: 'success',
  provider_code: 'deepseek',
  credential_id: 7,
  canonical_name: 'glm-4.5',
  agent_name: 'claude-code',
}

const filters = (over: Partial<typeof DEFAULT_TOPO_FILTERS> = {}) => ({ ...DEFAULT_TOPO_FILTERS, ...over })

describe('matchesFilters', () => {
  it('passes everything when no filter is set', () => {
    expect(matchesFilters(base, filters(), NOW)).toBe(true)
  })

  it('filters by provider code and by credential id', () => {
    expect(matchesFilters(base, filters({ provider: 'deepseek' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ provider: 'cred:7' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ provider: 'moonshot' }), NOW)).toBe(false)
  })

  it('filters by client and model', () => {
    expect(matchesFilters(base, filters({ client: 'claude-code' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ client: 'codex' }), NOW)).toBe(false)
    expect(matchesFilters(base, filters({ model: 'glm-4.5' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ model: 'gpt-5' }), NOW)).toBe(false)
  })

  it('filters by status', () => {
    expect(matchesFilters({ ...base, status: 'failure' }, filters({ status: 'failure' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ status: 'failure' }), NOW)).toBe(false)
  })

  it('excludes requests outside the time window', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString() }
    expect(matchesFilters(old, filters(), NOW)).toBe(false)
    // ★ 前提仍在场：同一条数据放大窗口就该回来 —— 防止「永远 false」的假绿
    expect(matchesFilters(old, filters({ windowMs: 2 * 60 * 60_000 }), NOW)).toBe(true)
  })

  it('keeps frames without a timestamp rather than dropping them silently', () => {
    expect(matchesFilters({ ...base, ts: undefined }, filters(), NOW)).toBe(true)
  })
})

describe('applyFilters', () => {
  it('converges every axis at once (graph + rails share one subset)', () => {
    const requests = [
      base,
      { ...base, request_id: 'r2', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
      { ...base, request_id: 'r3', status: 'failure' },
    ]
    expect(applyFilters(requests, filters(), NOW)).toHaveLength(3)
    expect(applyFilters(requests, filters({ provider: 'deepseek' }), NOW)).toHaveLength(2)
    expect(applyFilters(requests, filters({ provider: 'deepseek', status: 'failure' }), NOW)).toHaveLength(1)
    expect(applyFilters(requests, filters({ provider: 'deepseek', client: 'codex' }), NOW)).toHaveLength(0)
  })
})

describe('filterOptions', () => {
  it('lists candidates with hit counts, most active first', () => {
    const requests = [
      base,
      { ...base, request_id: 'r2', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
      { ...base, request_id: 'r3', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
    ]
    const options = filterOptions(requests, NOW, 5 * 60_000)
    // 供应商下拉同时给「供应商整体」与「单把凭据」两种口径（供应端是凭据级节点）
    expect(options.providers).toContainEqual({ value: 'moonshot', label: 'moonshot', count: 2 })
    expect(options.providers).toContainEqual({ value: 'cred:8', label: 'moonshot #8', count: 2 })
    expect(options.providers).toContainEqual({ value: 'deepseek', label: 'deepseek', count: 1 })
    expect(options.clients[0]).toMatchObject({ value: 'codex', count: 2 })
    expect(options.models[0]).toMatchObject({ value: 'kimi', count: 2 })
  })

  it('ignores out-of-window traffic when listing candidates', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString() }
    const options = filterOptions([old], NOW, 5 * 60_000)
    expect(options.clients).toHaveLength(0)
    expect(options.providers).toHaveLength(0)
  })
})
