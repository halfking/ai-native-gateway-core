// liveTopologyLayout.test.ts — 布局判据（2026-10-16）
//
// 核心不变量：**确定性**（同输入同输出）。布局每次刷新重算，
// 一旦引入随机，运维没法形成肌肉记忆，且所有「节点在第几行」的断言失效。

import { describe, expect, it } from 'vitest'
import { computeLayout, edgeWidth, particleRadius, stableHash } from '../lib/liveTopologyLayout'
import type { TopoNode } from '../types/liveTopology'

function node(id: string, column: TopoNode['column'], requests: number): TopoNode {
  return {
    id,
    column,
    label: id,
    health: 'unknown',
    live: false,
    metric: { requests, success: requests, failure: 0, tokens: 0, inFlight: 0 },
  }
}

const NODES = [
  node('client:a', 'client', 30),
  node('client:b', 'client', 20),
  node('gateway:core', 'gateway', 50),
  node('cred:1', 'provider', 25),
  node('cred:2', 'provider', 10),
  node('model:x', 'model', 20),
]

const EDGES = [
  { id: 'e1', from: 'client:a', to: 'gateway:core', requests: 30, failure: 1 },
  { id: 'e2', from: 'gateway:core', to: 'cred:1', requests: 25, failure: 0 },
  { id: 'e3', from: 'cred:1', to: 'model:x', requests: 25, failure: 0 },
]

describe('computeLayout', () => {
  it('is deterministic across repeated calls and input ordering', () => {
    const a = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: 700 })
    const b = computeLayout({ nodes: [...NODES].reverse(), edges: EDGES, width: 1200, height: 700 })
    const posA = a.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    const posB = b.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    expect(posA).toEqual(posB)
  })

  it('places columns left to right in the declared order', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: 700 })
    const xs = new Map(layout.nodes.map((n) => [n.id, n.x]))
    expect(xs.get('client:a')!).toBeLessThan(xs.get('gateway:core')!)
    expect(xs.get('gateway:core')!).toBeLessThan(xs.get('cred:1')!)
    expect(xs.get('cred:1')!).toBeLessThan(xs.get('model:x')!)
  })

  it('orders each column by traffic descending', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: 700 })
    const clients = layout.nodes.filter((n) => n.column === 'client')
    expect(clients[0].id).toBe('client:a')
  })

  it('drops edges whose endpoints are not in the layout (no dangling links)', () => {
    const layout = computeLayout({
      nodes: NODES,
      edges: [...EDGES, { id: 'ghost', from: 'client:a', to: 'cred:404', requests: 3, failure: 0 }],
      width: 1200,
      height: 700,
    })
    expect(layout.edges.map((e) => e.id)).not.toContain('ghost')
  })

  it('clamps to a minimum usable canvas size', () => {
    const tiny = computeLayout({ nodes: NODES, edges: [], width: 10, height: 10 })
    expect(tiny.width).toBeGreaterThanOrEqual(640)
    expect(tiny.height).toBeGreaterThanOrEqual(320)
  })
})

describe('edge + particle visual mapping', () => {
  it('idle edges stay thin instead of being drawn as failures', () => {
    expect(edgeWidth(0)).toBeLessThan(1)
  })

  it('edge width grows with traffic and saturates', () => {
    expect(edgeWidth(1000)).toBeGreaterThan(edgeWidth(10))
    expect(edgeWidth(1e9)).toBeLessThanOrEqual(5)
  })

  it('particle radius buckets by token volume', () => {
    expect(particleRadius(0)).toBe(3)
    expect(particleRadius(100)).toBeLessThan(particleRadius(1000))
    expect(particleRadius(1000)).toBeLessThan(particleRadius(10000))
  })
})

describe('stableHash', () => {
  it('is stable and bounded', () => {
    expect(stableHash('deepseek')).toBe(stableHash('deepseek'))
    expect(stableHash('a')).not.toBe(stableHash('b'))
    expect(stableHash('x')).toBeGreaterThanOrEqual(0)
    expect(stableHash('x')).toBeLessThan(2 ** 32)
  })
})
