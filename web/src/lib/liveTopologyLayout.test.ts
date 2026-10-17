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
import { computeGatewayBox, computeLayout, edgeWidth, fitRow, MIN_LAYOUT_HEIGHT, MIN_LAYOUT_WIDTH, NODE_GAP, NODE_W, NODE_W_MIN, particleRadius, stableHash } from '../lib/liveTopologyLayout'
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

  it('★ never lays out wider than its container (a wide canvas gets clipped away)', () => {
    // 早先下限是 640，于是 480 视口（容器 456px）时布局仍按 640 排布，
    // 而 `.topo-canvas` 是 `overflow:hidden` ⇒ 右侧 113px 被裁掉，
    // 大方块右边和每行末尾的卡永远看不见。**下限大于容器不是信息，是丢信息。**
    for (const containerWidth of [320, 456, 480, 616, 744, 1000, 1416]) {
      const layout = computeLayout({ nodes: NODES, edges: EDGES, width: containerWidth, height: 700 })
      expect(layout.width).toBe(Math.max(MIN_LAYOUT_WIDTH, containerWidth))
      // 任何东西都不得越过右边界
      const overflowX = layout.nodes.filter((n) => n.x + n.w > containerWidth)
      expect(overflowX.map((n) => `${n.id}@${n.x}+${n.w}`), `${containerWidth}px 下有节点越界`).toEqual([])
      expect(
        layout.gateway.x + layout.gateway.w <= containerWidth,
        `${containerWidth}px 下大方块越界`,
      ).toBe(true)
    }
  })

  it('folds nodes that cannot fit instead of letting cards overlap', () => {
    // 卡片固定 150px。8 张摊 1440 宽时槽位 138 < 150，每张压 12px；
    // 1024 宽压 58px —— 重叠和裁切是同一类「看不见信息」的缺陷。
    //
    // **断言用实际最小间隙、且扫全部白名单断点**：抖动幅度曾因未按间隙封顶
    // 而吃掉间隙（560px 实测只剩 9px < 最小 12px）。只测 480/1024/1440
    // 三个点会漏掉 560 这类宽度。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:node-with-a-longish-id-${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 100 - i, success: 100 - i, failure: 0, tokens: 0, inFlight: 0 },
    }))
    for (const width of [320, 360, 400, 480, 560, 640, 744, 768, 1024, 1440]) {
      const layout = computeLayout({ nodes: many, edges: [], width, height: 700 })
      const cards = layout.nodes.filter((n) => n.column === 'model').sort((a, b) => a.x - b.x)
      for (let i = 1; i < cards.length; i += 1) {
        const gap = cards[i].x - (cards[i - 1].x + cards[i - 1].w)
        expect(gap, `${width}px 下第 ${i} 张卡与前一张重叠或贴死`).toBeGreaterThanOrEqual(0)
      }
    }
  })

  it('★ keeps a visible gap between cards even after jitter', () => {
    // 只断言「不重叠」不够：卡片贴死（间隙 0~11px）读起来同样是「挤成一坨」。
    // 抖动是按 id 哈希派生的，某个宽度上恰好吃掉间隙（560px 实测 9px），
    // 所以这里钉「最小间隙 ≥ NODE_GAP」本身。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:node-with-a-longish-id-${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 100 - i, success: 100 - i, failure: 0, tokens: 0, inFlight: 0 },
    }))
    let tightest = Infinity
    for (let width = 320; width <= 1440; width += 4) {
      const layout = computeLayout({ nodes: many, edges: [], width, height: 700 })
      const cards = layout.nodes.filter((n) => n.column === 'model').sort((a, b) => a.x - b.x)
      for (let i = 1; i < cards.length; i += 1) {
        tightest = Math.min(tightest, cards[i].x - (cards[i - 1].x + cards[i - 1].w))
      }
    }
    expect(tightest).toBeGreaterThanOrEqual(NODE_GAP)
  })

  it('★ folds the overflow into a visible aggregate instead of dropping it silently', () => {
    // 「放不下就折叠」必须看得见：折叠节点要在图上，且请求数是真实的和。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:m${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 10, success: 10, failure: 0, tokens: 0, inFlight: 0 },
    }))
    const narrow = computeLayout({ nodes: many, edges: [], width: 480, height: 700 })
    const folded = narrow.nodes.filter((n) => n.isAggregate)
    expect(folded.length).toBeGreaterThan(0)
    // 折叠节点的请求数 = 被折叠掉那些的和，流量不会凭空消失
    const totalShown = narrow.nodes
      .filter((n) => n.column === 'model')
      .reduce((sum, n) => sum + n.metric.requests, 0)
    expect(totalShown).toBe(8 * 10)

    const wide = computeLayout({ nodes: many, edges: [], width: 1440, height: 700 })
    // 放得下就不该折叠
    expect(wide.nodes.filter((n) => n.isAggregate)).toEqual([])
  })

  it('reroutes folded edges to the aggregate and never emits a self-loop', () => {
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `client:c${i}`, column: 'client' as const, label: `c${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 5, success: 5, failure: 0, tokens: 0, inFlight: 0 },
    }))
    // 链路终点必须真实存在，否则边会被当成悬空边丢掉（判据自己就写错过这一版：
    // 只给 client 节点、没有 model 节点，edges 全空，断言 `length === 1` 恒不成立）。
    const target = {
      id: 'model:m0', column: 'model' as const, label: 'm0', sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 5, success: 5, failure: 0, tokens: 0, inFlight: 0 },
    }
    const edges = [
      ...many.map((n) => ({ id: `${n.id}->model:m0`, from: n.id, to: 'model:m0', requests: 2, failure: 0 })),
      // 同层内部链路：两端折叠到同一个节点 ⇒ 自环，必须丢掉
      { id: 'client:c5->client:c6', from: 'client:c5', to: 'client:c6', requests: 1, failure: 0 },
    ]
    const layout = computeLayout({ nodes: [...many, target], edges, width: 480, height: 700 })

    expect(layout.nodes.some((n) => n.isAggregate)).toBe(true)
    expect(layout.edges.some((e) => e.from === e.to)).toBe(false)
    // 可见卡保留自己的链路；被折叠的 7 张合并到折叠节点、请求数求和，
    // **而不是让后一条盖掉前一条**（少算就是丢流量）。
    const visibleEdge = layout.edges.find((e) => e.from === 'client:c0')
    expect(visibleEdge?.requests).toBe(2)
    const aggEdge = layout.edges.find((e) => e.from.startsWith('client:folded'))
    expect(aggEdge?.requests).toBe(7 * 2)
    // 总数守恒：一张都没丢
    expect(layout.edges.reduce((s, e) => s + e.requests, 0)).toBe(8 * 2)
    // 自环那条确实没进来
    expect(layout.edges.some((e) => e.id.includes('c5') && e.id.includes('c6'))).toBe(false)
  })

  it('clamps a degenerate zero-size container to the floor', () => {
    const tiny = computeLayout({ nodes: NODES, edges: [], width: 0, height: 0 })
    expect(tiny.width).toBeGreaterThanOrEqual(MIN_LAYOUT_WIDTH)
    expect(tiny.height).toBeGreaterThanOrEqual(MIN_LAYOUT_HEIGHT)
  })
})

describe('fitRow', () => {
  it('★ grows with width and never collapses to one card on a wide canvas', () => {
    // 本轮最大的坑：fitRow 的枚举方向写反后**恒返回 visible:1**，
    // 而「不越界」「不重叠」两条判据照样全绿（一张卡当然既不重叠也不越界）。
    // ⇒ 必须有一条**正向**判据钉住「宽屏就该画多张」。
    expect(fitRow(1440, false).visible).toBeGreaterThanOrEqual(6)
    expect(fitRow(1920, false).visible).toBeGreaterThan(fitRow(480, false).visible)
    expect(fitRow(320, false).visible).toBeGreaterThanOrEqual(1)
  })

  it('★ never folds when the aggregate node itself would not fit', () => {
    // 320px 极窄容器：折叠节点也是一张卡，放不下就不该折叠
    // —— 早先只判「单卡 + 一个间隙」放得下（太松，算出 gap = -1.3）。
    // 断言的是**实际布局结果**（不越界），不是 fitRow 的中间返回值。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:m${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 5, success: 5, failure: 0, tokens: 0, inFlight: 0 },
    }))
    for (const width of [320, 360, 480]) {
      const layout = computeLayout({ nodes: many, edges: [], width, height: 700 })
      for (const n of layout.nodes) {
        expect(n.x + n.w, `w=${width} 节点 ${n.id} 越界`).toBeLessThanOrEqual(width)
        expect(n.x, `w=${width} 节点 ${n.id} 左边越界`).toBeGreaterThanOrEqual(0)
      }
    }
  })

  it('prefers full-width cards over squeezing more of them in', () => {
    // 1440 宽应当画满宽 150 的卡，而不是摊成 12 张 105px 的窄卡
    // —— 张数上去了但可读性全丢，正是「多画了却看不见」。
    expect(fitRow(1440, false).cardW).toBe(NODE_W)
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
