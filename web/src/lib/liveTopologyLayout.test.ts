// liveTopologyLayout.test.ts — 布局判据（2026-10-17 改版：上→下 + 网关核心整体）
//
// 核心不变量：
//  1. **确定性**（同输入同输出）。布局每次刷新重算，一旦引入随机，
//     运维没法形成肌肉记忆，且所有「节点在第几行」的断言失效。
//  2. **纵向**（client.y < provider.y < model.y）—— 2026-10-17 维护者拍板
//     把流程从左→右改成上→下。
//  3. **网关核心是大方块**：它不再出现在 nodes 里，而是整块的 gateway 几何，
//     且大方块必须横在客户端层与供应端层之间（否则边会从大方块上面绕过去）。

import { describe, expect, it } from 'vitest'
import { computeGatewayBox, computeLayout, edgeWidth, particleRadius, stableHash } from '../lib/liveTopologyLayout'
import { GATEWAY_MODULES, type TopoNode } from '../types/liveTopology'

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

const SIZE = { width: 1200, height: 700 }

describe('computeLayout', () => {
  it('is deterministic across repeated calls and input ordering', () => {
    const a = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const b = computeLayout({ nodes: [...NODES].reverse(), edges: EDGES, ...SIZE })
    const posA = a.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    const posB = b.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    expect(posA).toEqual(posB)
    expect(JSON.stringify(a.gateway)).toBe(JSON.stringify(b.gateway))
  })

  it('flows top to bottom: client above provider above model', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const y = new Map(layout.nodes.map((n) => [n.id, n.y + n.h / 2]))
    expect(y.get('client:a')!).toBeLessThan(y.get('cred:1')!)
    expect(y.get('cred:1')!).toBeLessThan(y.get('model:x')!)
    // 每一层的中心线也必须单调递增
    expect(layout.rowY.client).toBeLessThan(layout.rowY.provider)
    expect(layout.rowY.provider).toBeLessThan(layout.rowY.model)
  })

  it('carries the gateway core as one big box instead of a single small node', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    expect(layout.nodes.map((n) => n.id)).not.toContain('gateway:core')
    expect(layout.gateway.w).toBeGreaterThan(SIZE.width / 2)
    expect(layout.gateway.h).toBeGreaterThan(SIZE.height / 4)
    // 九个模块格全部落在大方块内部（越界 = 画到框外了）
    expect(layout.gateway.cells).toHaveLength(GATEWAY_MODULES.length)
    for (const cell of layout.gateway.cells) {
      expect(cell.x).toBeGreaterThanOrEqual(layout.gateway.x)
      expect(cell.y).toBeGreaterThanOrEqual(layout.gateway.y)
      expect(cell.x + cell.w).toBeLessThanOrEqual(layout.gateway.x + layout.gateway.w)
      expect(cell.y + cell.h).toBeLessThanOrEqual(layout.gateway.y + layout.gateway.h)
    }
  })

  it('sits the gateway box between the client layer and the provider layer', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    expect(layout.gateway.y).toBeGreaterThan(layout.rowY.client)
    expect(layout.gateway.y + layout.gateway.h).toBeLessThan(layout.rowY.provider)
  })

  it('★ clears the node cards above and below it, not just their centre lines', () => {
    // 大方块底边曾压到供应端节点卡上沿 2px：判据只比中心线时是绿的。
    // 这里按「节点卡实际占位」断言，并复用布局导出的 NODE_H/NODE_W。
    //
    // ★ 关键：**必须扫多个画布高度**。净空随高度变化（比例量 vs 固定像素量），
    // 旧值 hRatio=0.42 在 height=700 恰好「正好相接」（等号能过），
    // 只测这一个高度就是假绿 —— 它在 height=620 下压 2px、height=800 下留 2.6px。
    for (const height of [480, 620, 700, 900, 1200]) {
      const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height })
      const inLayer = (column: string) => layout.nodes.filter((n) => n.column === column)
      const clientCards = inLayer('client')
      const providerCards = inLayer('provider')
      expect(clientCards.length).toBeGreaterThan(0)
      expect(providerCards.length).toBeGreaterThan(0)

      const clientBottom = Math.max(...clientCards.map((n) => n.y + n.h))
      const providerTop = Math.min(...providerCards.map((n) => n.y))
      expect(layout.gateway.y, `h=${height} 顶部压住客户端卡片`).toBeGreaterThanOrEqual(clientBottom)
      expect(layout.gateway.y + layout.gateway.h, `h=${height} 底部压住供应端卡片`).toBeLessThanOrEqual(providerTop)
      // 节点卡本身不越出画布左右边界
      for (const n of layout.nodes) {
        expect(n.x).toBeGreaterThanOrEqual(0)
        expect(n.x + n.w).toBeLessThanOrEqual(layout.width)
        expect(n.y + n.h).toBeLessThanOrEqual(layout.height)
      }
    }
  })

  it('orders each column by traffic descending', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const clients = layout.nodes.filter((n) => n.column === 'client')
    expect(clients[0].id).toBe('client:a')
  })

  it('drops edges whose endpoints are not in the layout (no dangling links)', () => {
    const layout = computeLayout({
      nodes: NODES,
      edges: [...EDGES, { id: 'ghost', from: 'client:a', to: 'cred:404', requests: 3, failure: 0 }],
      ...SIZE,
    })
    expect(layout.edges.map((e) => e.id)).not.toContain('ghost')
  })

  it('clamps to a minimum usable canvas size', () => {
    const tiny = computeLayout({ nodes: NODES, edges: [], width: 10, height: 10 })
    expect(tiny.width).toBeGreaterThanOrEqual(640)
    expect(tiny.height).toBeGreaterThanOrEqual(320)
  })
})

describe('computeGatewayBox', () => {
  it('lays the nine modules out in their declared 3x3 grid, in flow order', () => {
    const box = computeGatewayBox(1200, 700)
    const byKey = new Map(box.cells.map((c) => [c.key, c]))
    expect(byKey.get('cache')!).toMatchObject({ x: expect.any(Number), y: expect.any(Number) })
    // 流程顺序 = 先左后右、自上而下（row 升序，同 row 时 col 升序）
    const sorted = [...GATEWAY_MODULES].sort((a, b) => a.row - b.row || a.col - b.col).map((m) => m.key)
    expect(sorted).toEqual([
      'cache', 'sanitize', 'route',
      'nodeState', 'sessionFile', 'sessionWrite',
      'errorRecord', 'probeTask', 'taskDetect',
    ])
    // 第一行第一格必须在最左上角
    const topLeft = box.cells.reduce((a, b) => (a.x + a.y <= b.x + b.y ? a : b))
    expect(topLeft.key).toBe('cache')
    // 同一行：x 递增、y 相同；同一列：y 递增、x 相同
    // （写反了会变成「判据自己描述了一个不可能的网格」）
    const byKeyIndex = (r: number, c: number) => byKey.get(GATEWAY_MODULES.find((m) => m.row === r && m.col === c)!.key)!
    for (let row = 0; row < 3; row += 1) {
      const a = byKeyIndex(row, 0)
      const b = byKeyIndex(row, 1)
      expect(b.x).toBeGreaterThan(a.x)
      expect(b.y).toBe(a.y)
    }
    for (let col = 0; col < 3; col += 1) {
      const a = byKeyIndex(0, col)
      const b = byKeyIndex(1, col)
      expect(b.y).toBeGreaterThan(a.y)
      expect(b.x).toBe(a.x)
    }
  })

  it('is deterministic', () => {
    expect(JSON.stringify(computeGatewayBox(1200, 700))).toBe(JSON.stringify(computeGatewayBox(1200, 700)))
  })

  it('★ reserves a header band so the title cannot sit on the first row', () => {
    // 这条判据来自一次真实的肉眼缺陷：大方块顶部有「网关核心 · 缓存·脱敏…」
    // 标题（组件里是正常流的 .gw__head），而 3×3 网格原本从 pad=10px 就开始，
    // 于是标题正好压在第一行模块上。布局必须为标题让出空间。
    const box = computeGatewayBox(1200, 700)
    const firstRow = box.cells
      .filter((c) => GATEWAY_MODULES.find((m) => m.key === c.key)!.row === 0)
      .map((c) => c.y - box.y)
    // 第一行至少要下移到 24px 之下（标题带 + 内边距），且三列一致
    expect(Math.min(...firstRow)).toBeGreaterThanOrEqual(24)
    expect(new Set(firstRow).size).toBe(1)
    // 预留标题带后，第三行仍然不能溢出大方块底部
    const bottom = Math.max(...box.cells.map((c) => c.y + c.h - box.y))
    expect(bottom).toBeLessThanOrEqual(box.h)
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
