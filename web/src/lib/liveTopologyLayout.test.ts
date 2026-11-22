// liveTopologyLayout.test.ts — 布局判据（2026-10-18 三带改版）
//
// 核心不变量：
//  1. **确定性**（同输入同输出）。布局每次刷新重算，一旦引入随机，
//     运维没法形成肌肉记忆，且所有「节点在第几行」的断言失效。
//  2. **纵向**（client.y < provider.y < model.y）—— 2026-10-17 维护者拍板
//     把流程从左→右改成上→下。
//  3. **网关核心是大方块**（三带：进站链/存储转发/出站链）：它不出现在
//     nodes 里，但 gateway:core **必须进边的端点表** —— 2026-10-18 修复的
//     真实缺陷就是它缺席导致 client→gateway / gateway→provider 边全部
//     被静默丢弃（图上只剩 provider→model 一段，owner 报告「连线没有」）。

import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  bezierPoint,
  computeGatewayBox,
  computeLayout,
  computeRowY,
  edgeWidth,
  fitRow,
  isEdgeHighlighted,
  MIN_LAYOUT_HEIGHT,
  MIN_LAYOUT_WIDTH,
  NODE_GAP,
  NODE_H,
  NODE_W,
  particleRadius,
  stableHash,
} from '../lib/liveTopologyLayout'
import {
  GATEWAY_IN_MODULES,
  GATEWAY_INFRA_MODULES,
  GATEWAY_OUT_MODULES,
  GATEWAY_STORE_MODULES,
  type TopoNode,
} from '../types/liveTopology'

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

/**
 * ★★ 画布容器的**实际最小高度**（★ 真源＝画布组件的 CSS `min-height`）。
 *
 * ★ 为什么测试要从 CSS 读，而不是写死一个数：★ `computeLayout` 拿到的高度是
 *   **容器实测高度**（`size.value.height`）⇒ ★ 而容器的 CSS `min-height` 决定了
 *   它**不可能比这更矮** ⇒ ⇒ **比它矮的高度是不可达场景**。
 * ★ 2026-11-23 上游 `618a61325` 把画布 min-height 480 → **620px**（适配新的
 *   从上往下布局：「最小所需高度 ≈ 620px」）⇒ ⇒ 本断言原先写死扫描的 480
 *   **已不可达** ⇒ ⇒ 布局在 480 下溢出 12px ⇒ ⇒ 断言红了 ⇒ ⇒ **但那不是
 *   生产会出现的画面**（容器至少 620px 高）。
 * ★★ ⇒ 扫描高度**从真源派生**，不写死 ⇒ 上游再调画布高度，这条自动跟随，
 *   ★★ 而**真正会发生的溢出**（≥ 画布最小高度时）仍然照红不误。
 */
const CANVAS_MIN_HEIGHT = (() => {
  const src = readFileSync(
    resolve(process.cwd(), 'src/components/live-topology/TopologyCanvas.vue'),
    'utf8',
  )
  const m = src.match(/min-height:\s*(\d+)px/)
  if (!m) throw new Error('解析不出画布的 CSS min-height（真源丢失 ⇒ 判据不可信）')
  return Number(m[1])
})()

/** 可达画布高度：★ 从画布 CSS 下限起（★ 低于它的都是不可达场景）。 */
const REACHABLE_HEIGHTS = [CANVAS_MIN_HEIGHT, 700, 900, 1200]

describe('computeLayout', () => {
  it('is deterministic across repeated calls and input ordering', () => {
    const a = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const b = computeLayout({ nodes: [...NODES].reverse(), edges: EDGES, ...SIZE })
    const posA = a.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    const posB = b.nodes.map((n) => `${n.id}@${n.x},${n.y}`).sort()
    expect(posA).toEqual(posB)
    expect(JSON.stringify(a.gateway)).toBe(JSON.stringify(b.gateway))
  })

  it('flows top to bottom: client above model above provider（2026-11-21 拓扑顺序优化：模型前置）', () => {
    // ★ 契约换向（62 增量审计轮 D-P1-2）：2026-11-21/22 四笔链（633185705 →
    //   23b9d32b7 → 两笔调距）把视觉顺序有意改为「先看用什么模型，再看通过
    //   哪个节点」（ROW_RATIO 注释记录 owner 理由：client 0.075 / model 0.82 /
    //   provider 0.94）—— 钉测随新契约改钉，防止再次「契约换了钉测没跟」。
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const y = new Map(layout.nodes.map((n) => [n.id, n.y + n.h / 2]))
    expect(y.get('client:a')!).toBeLessThan(y.get('model:x')!)
    expect(y.get('model:x')!).toBeLessThan(y.get('cred:1')!)
    // 每一层的中心线也必须单调递增（新契约：client → model → provider）
    expect(layout.rowY.client).toBeLessThan(layout.rowY.model)
    expect(layout.rowY.model).toBeLessThan(layout.rowY.provider)
  })

  it('carries the gateway core as one big box instead of a single small node', () => {
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    expect(layout.nodes.map((n) => n.id)).not.toContain('gateway:core')
    expect(layout.gateway.w).toBeGreaterThan(SIZE.width / 2)
    expect(layout.gateway.h).toBeGreaterThan(SIZE.height / 4)
    // 三段（缎带/组区/图例）全部落在大方块内部（2026-11-20 C 方案轮；
    // 越界 = 画到框外了）
    expect(layout.gateway.lanes.map((l) => l.lane)).toEqual(['ribbon', 'groups', 'legend'])
    for (const lane of layout.gateway.lanes) {
      expect(lane.x).toBeGreaterThanOrEqual(layout.gateway.x)
      expect(lane.y).toBeGreaterThanOrEqual(layout.gateway.y)
      expect(lane.x + lane.w).toBeLessThanOrEqual(layout.gateway.x + layout.gateway.w)
      expect(lane.y + lane.h).toBeLessThanOrEqual(layout.gateway.y + layout.gateway.h)
    }
  })

  it('★ keeps client→gateway and gateway→provider edges in the layout (2026-10-18 修复的缺陷)', () => {
    // gateway:core 不是布局节点卡片（由大方块渲染），但边必须有它的端点几何。
    // 修复前：byId 里没有 gateway:core ⇒ 这两类边在 `if (!a || !b) continue`
    // 处被静默丢弃 ⇒ 图上客户端与网关之间没有任何连线（owner 报告的缺陷）。
    const layout = computeLayout({ nodes: NODES, edges: EDGES, ...SIZE })
    const ids = layout.edges.map((e) => e.id)
    expect(ids).toContain('client:a->gateway:core')
    expect(ids).toContain('gateway:core->cred:1')
    expect(ids).toContain('cred:1->model:x')
    // 连线方向也必须对：客户端边锚到方块**顶边**，供应端边从**底边**出发。
    const gw = layout.gateway
    const clientEdge = layout.edges.find((e) => e.id === 'client:a->gateway:core')!
    const providerEdge = layout.edges.find((e) => e.id === 'gateway:core->cred:1')!
    expect(clientEdge.path[0].y).toBeLessThan(gw.y)
    expect(clientEdge.path[3].y).toBe(gw.y)
    expect(providerEdge.path[0].y).toBe(gw.y + gw.h)
    expect(providerEdge.path[3].y).toBeGreaterThan(gw.y + gw.h)
    // 锚点水平方向散开在方块范围内（不是全挤到中心一个点）
    for (const edge of [clientEdge, providerEdge]) {
      const anchor = edge.from === 'gateway:core' ? edge.path[0] : edge.path[3]
      expect(anchor.x).toBeGreaterThanOrEqual(gw.x)
      expect(anchor.x).toBeLessThanOrEqual(gw.x + gw.w)
    }
  })

  it('anchors client edges into the box at spread positions (not one central bundle)', () => {
    // 多客户端时锚点必须散开：全部挤到同一 x 会退化成一束麻线，
    // 分不清哪条线属于哪个客户端。
    const clients = [
      node('client:left', 'client', 30),
      node('client:mid', 'client', 25),
      node('client:right', 'client', 20),
      node('cred:1', 'provider', 25),
      node('model:x', 'model', 20),
    ]
    const edges = [
      { id: 'e1', from: 'client:left', to: 'gateway:core', requests: 30, failure: 0 },
      { id: 'e2', from: 'client:mid', to: 'gateway:core', requests: 25, failure: 0 },
      { id: 'e3', from: 'client:right', to: 'gateway:core', requests: 20, failure: 0 },
    ]
    const layout = computeLayout({ nodes: clients, edges, ...SIZE })
    const anchors = layout.edges
      .filter((e) => e.to === 'gateway:core')
      .map((e) => e.path[3].x)
      .sort((a, b) => a - b)
    expect(anchors.length).toBe(3)
    // 最左与最右锚点至少相隔 60px（三条线明显分开）
    expect(anchors[anchors.length - 1] - anchors[0]).toBeGreaterThanOrEqual(60)
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
    // ★★ 扫描高度**从画布 CSS min-height 派生**（★ 见 CANVAS_MIN_HEIGHT 的注释）：
    // ★★ 低于画布下限的高度**不可达**（容器实测高度 ≥ CSS min-height）⇒
    // ★★ 断它＝断一个不存在的画面 ⇒ ★★ 上游 618a61325 把画布提到 620px 后，
    // ★★ 原来写死的 480 已被剔除 ⇒ ★★ 而**真正会发生的溢出仍照红不误**。
    for (const height of REACHABLE_HEIGHTS) {
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
        expect(n.y + n.h, `h=${height} 节点卡越出画布底边`).toBeLessThanOrEqual(layout.height)
      }
    }
  })

  it('★★ 画布的 CSS min-height 必须与布局兜底下限**相等**（★ 两处真源不许各写一个数）', () => {
    // ★ 关系判据（★ 不写死数值）：★ `MIN_LAYOUT_HEIGHT` 是布局的**兜底**
    //   （把更小的输入抬到该高度再排布）⇒ ★ 而容器的 CSS `min-height` 决定
    //   它**不可能比这更矮** ⇒ ⇒ 两处是**同一个下限**的两个写法 ⇒ ★ ⇒
    //   ⇒ 必须**相等**：★ 小于它 ⇒ 容器可能矮于布局兜底 ⇒ 排布内容溢出被裁
    //   （★ 与 `MIN_LAYOUT_WIDTH` 注释里同一条原则：下限大于容器不是信息是丢信息）；
    //   ★ 大于它 ⇒ 布局会按一个容器永远达不到的高度排布 ⇒ 白白浪费垂直空间。
    expect(
      CANVAS_MIN_HEIGHT,
      '★ 画布 CSS min-height 必须与布局兜底下限 MIN_LAYOUT_HEIGHT 相等'
        + '⇒ 两处是同一个下限 ⇒ 小于它会溢出被裁、大于它浪费空间',
    ).toBe(MIN_LAYOUT_HEIGHT)
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

  it('★ the folded label reports request volume and conserves the total', () => {
    // 同一份数据在不同宽度下折叠掉的节点数不同（1024 折 3 个、480 折 7 个），
    // 所以**折叠量本来就该随宽度变** —— 那不是缺陷。
    // 缺陷是「标签写成节点数」：用户会把屏幕窄误读成流量变了。
    // 真正要守的是：① 标签 = 折叠节点的请求量（自洽）；
    //             ② 可见 + 折叠 = 总量（流量守恒，折叠不是丢弃）。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:m${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 100 - i, success: 100 - i, failure: 0, tokens: 0, inFlight: 0 },
    }))
    const grandTotal = many.reduce((s, n) => s + n.metric.requests, 0)
    let sawFold = false
    for (const width of [1440, 1024, 744, 480]) {
      const layout = computeLayout({ nodes: many, edges: [], width, height: 800 })
      const layer = layout.nodes.filter((n) => n.column === 'model')
      const agg = layer.find((n) => n.isAggregate)
      const shown = layer.filter((n) => !n.isAggregate)
      if (!agg) {
        // 没折叠 ⇒ 必须全部可见（1440 宽就该画全）
        expect(shown.length, `w=${width} 未折叠却有 ${shown.length} 张可见卡`).toBe(many.length)
        continue
      }
      sawFold = true
      const n = Number(agg.label.replace('+', ''))
      expect(Number.isFinite(n), `w=${width} 标签 ${agg.label} 不是数字`).toBe(true)
      // 标签里的量必须等于折叠节点自身的请求量（自洽，不是凭空来的数）
      expect(n, `w=${width} 标签与实际折叠量不符`).toBe(agg.metric.requests)
      // 标签绝不能是「节点个数」—— 流量数值远大于节点数（772 vs 7）
      expect(n, `w=${width} 标签看起来是节点数而非请求量`).toBeGreaterThan(many.length)
      // 流量守恒：可见 + 折叠 = 总量
      const layerTotal = layer.reduce((s, x) => s + x.metric.requests, 0)
      expect(layerTotal, `w=${width} 折叠后流量不守恒`).toBe(grandTotal)
    }
    expect(sawFold, '窄宽度下应当至少折叠一次').toBe(true)
  })

  it('★ keeps the original edge ids so highlighting still works after folding', () => {
    // 折叠后布局边的 id 变成 `client:x->model:folded:8`，而视图的 edgePathFor
    // 产出的是原始 `client:x->model:deepseek-chat`。高亮按 id 集合比对 ⇒
    // 对不上 ⇒「点面板条目高亮该链路」静默失效。sourceIds 就是给这个用的。
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: `model:m${i}`, column: 'model' as const, label: `m${i}`, sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 10, success: 10, failure: 0, tokens: 0, inFlight: 0 },
    }))
    const client = {
      id: 'client:c0', column: 'client' as const, label: 'c0', sub: '',
      health: 'healthy' as const, live: true,
      metric: { requests: 10, success: 10, failure: 0, tokens: 0, inFlight: 0 },
    }
    const layout = computeLayout({
      nodes: [...many, client],
      // 两条边都指向「会被折叠掉」的模型节点
      edges: [
        { id: 'orig-1', from: 'client:c0', to: 'model:m7', requests: 4, failure: 0 },
        { id: 'orig-2', from: 'client:c0', to: 'model:m6', requests: 6, failure: 0 },
      ],
      width: 480,
      height: 800,
    })
    const merged = layout.edges.find((e) => e.from === 'client:c0')
    expect(merged, '折叠后应保留 client 侧链路').toBeTruthy()
    // 两条原始边被合并，但两个原始 id 都必须留着
    expect(merged!.sourceIds).toEqual(['orig-1', 'orig-2'])
    // 请求数是合并后的和
    expect(merged!.requests).toBe(10)
    // 视图用原始 id 就能命中这条布局边
    expect(layout.edges.some((e) => e.sourceIds.includes('orig-1'))).toBe(true)
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

describe('isEdgeHighlighted', () => {
  it('★ matches by original id after the node got folded away', () => {
    // 视图产出的高亮集是**原始** id，布局边在折叠后 id 已变。
    // 只比布局 id ⇒ 用户点面板条目高亮链路，图上毫无反应。
    const highlight = new Set(['client:c0->model:deepseek-chat'])
    expect(isEdgeHighlighted(highlight, 'client:c0->model:folded:8', ['client:c0->model:deepseek-chat'])).toBe(true)
    // 原始 id 对不上时不误报
    expect(isEdgeHighlighted(highlight, 'client:c1->model:folded:8', ['client:c1->model:qwen'])).toBe(false)
  })

  it('still matches the layout id itself (unfolded path unchanged)', () => {
    const highlight = new Set(['client:a->model:x'])
    expect(isEdgeHighlighted(highlight, 'client:a->model:x', ['orig-1'])).toBe(true)
    expect(isEdgeHighlighted(new Set<string>(), 'client:a->model:x', ['orig-1'])).toBe(false)
  })

  it('tolerates edges without sourceIds', () => {
    expect(isEdgeHighlighted(new Set(['e1']), 'e1')).toBe(true)
    expect(isEdgeHighlighted(new Set(['e2']), 'e1')).toBe(false)
  })
})

describe('computeGatewayBox', () => {
  it('stacks the three C-plan bands in order: ribbon → groups → legend', () => {
    // 2026-11-20 C 方案：大方块纵向三段 —— 流程缎带（全程节点）→
    // 四组瓦片区（主体）→ 外框图例行。三段 y 严格递增且段间不重叠。
    const box = computeGatewayBox(1200, 700)
    expect(box.lanes.map((l) => l.lane)).toEqual(['ribbon', 'groups', 'legend'])
    const [ribbon, groups, legend] = box.lanes
    expect(ribbon.y).toBeLessThan(groups.y)
    expect(groups.y).toBeLessThan(legend.y)
    expect(groups.y).toBeGreaterThanOrEqual(ribbon.y + ribbon.h)
    expect(legend.y).toBeGreaterThanOrEqual(groups.y + groups.h)
    // 组区（瓦片+扇出竖排）最高，图例行最矮
    expect(groups.h).toBeGreaterThan(ribbon.h)
    expect(groups.h).toBeGreaterThan(legend.h)
  })

  it('★ module→lane membership matches the flow position (请求侧/扇出/存储/响应侧 + 基础服务)', () => {
    // 模块落在哪条带 = 它在真实处理流程中的位置。任何模块挪带都会被这里抓住。
    // 2026-10-28 选路层轮：route/nodeState 移出主链进基础服务侧条；
    // ★ 批判式审计修正：压缩排在模型选择**之后**（候选解析先定目标模型，
    // SessionCompressor 才按其上下文窗口做滑窗压缩）。
    // 2026-11-20 C 方案：in 带四模块拆渲染为 ①入口治理+②选路压缩两组，
    // 但模块归属（lane='in'）不变 —— 布局/统计口径与视觉分组解耦。
    expect(GATEWAY_IN_MODULES.map((m) => m.key)).toEqual(['cache', 'sanitize', 'modelSelect', 'compress'])
    expect(GATEWAY_INFRA_MODULES.map((m) => m.key)).toEqual(['route', 'nodeState'])
    expect(GATEWAY_STORE_MODULES.map((m) => m.key)).toEqual(['sessionWrite', 'sessionFile'])
    expect(GATEWAY_OUT_MODULES.map((m) => m.key)).toEqual(['taskDetect', 'errorRecord', 'probeTask'])
    // 基础服务侧条不属于任何流程带（lane 标记为 infra）
    for (const m of GATEWAY_INFRA_MODULES) expect(m.lane).toBe('infra')
  })

  it('is deterministic', () => {
    expect(JSON.stringify(computeGatewayBox(1200, 700))).toBe(JSON.stringify(computeGatewayBox(1200, 700)))
  })

  it('★ reserves a header band so the title cannot sit on the first band', () => {
    // 2026-10-17 轮的真实肉眼缺陷（标题压住第一行模块）同样要防：
    // 第一段（缎带）的 y 必须在标题带（26px）之下。
    const box = computeGatewayBox(1200, 700)
    const firstLane = box.lanes[0]
    expect(firstLane.y - box.y).toBeGreaterThanOrEqual(24)
    // 末段（图例）不能溢出大方块底部
    const lastLane = box.lanes[box.lanes.length - 1]
    expect(lastLane.y + lastLane.h).toBeLessThanOrEqual(box.y + box.h)
  })

  it('keeps the box clear of client/provider cards across canvas heights', () => {
    // 比例量（box 高）vs 固定像素量（卡片高）随画布高度变化 —— 必须扫高度。
    for (const height of [480, 620, 700, 900, 1200]) {
      const box = computeGatewayBox(1200, height)
      const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height })
      const clientBottom = Math.max(...layout.nodes.filter((n) => n.column === 'client').map((n) => n.y + n.h))
      const providerTop = Math.min(...layout.nodes.filter((n) => n.column === 'provider').map((n) => n.y))
      expect(box.y, `h=${height} 顶边压住客户端卡片`).toBeGreaterThanOrEqual(clientBottom)
      expect(box.y + box.h, `h=${height} 底边压住供应端卡片`).toBeLessThanOrEqual(providerTop)
    }
  })

  it('★ caps the box height on very tall canvases (no giant empty frame)', () => {
    // 预览页实测画布高 1723px：纯比例会把大方块拉到 1000px+，
    // 而 C 方案内容只有 ~200px —— 其余全空白，读图的人只会觉得图坏了。
    // 高度必须夹在内容紧包裹上限内，且仍在客户端/供应端行之间居中
    // （2026-11-20：帽 320→204；provider 行 0.85→0.80，两行不再贴底）。
    const box = computeGatewayBox(1200, 1723)
    expect(box.h).toBeLessThanOrEqual(204)
    expect(box.y).toBeGreaterThan(1723 * 0.075 + 21)
    expect(box.y + box.h).toBeLessThan(1723 * 0.8 - 21)
  })

  it('★ allows a taller cap on narrow canvases where the four groups wrap 2×2', () => {
    // 窄画布（<900px）四组整行横排芯片 + 插件独立成行 —— 帽 288（内容 ≈276
    // 按生产真数据形态定），仍矮于四带版 320；
    // 且与组件 is-narrow 判据同值（GATEWAY_NARROW_W 单一真源）。
    const narrow = computeGatewayBox(600, 1723)
    expect(narrow.h).toBeLessThanOrEqual(288)
    expect(narrow.h).toBeGreaterThan(204)
    const wide = computeGatewayBox(1200, 1723)
    expect(wide.h).toBeLessThanOrEqual(204)
  })

  it('★ narrow cap and is-narrow use the SAME quantity (box width, not canvas width)', () => {
    // 211 生产实测踩过的错位区：画布 ∈ [900, 1000) 时 box.w ∈ [810, 900) ⇒
    // 组件已切窄版（内容 ~257px）而帽若按画布宽判宽版（204）⇒ 17px 溢出。
    // 帽判据必须与组件 is-narrow（box.w < 900）同量同值。
    for (const canvasW of [900, 950, 999]) {
      const box = computeGatewayBox(canvasW, 1723)
      if (box.w < 900) expect(box.h, `canvasW=${canvasW} box.w=${box.w} 窄版帽`).toBe(288)
    }
    // 画布 1000 ⇒ box.w=900 ⇒ 宽版帽 204（边界同侧）
    expect(computeGatewayBox(1000, 1723).w).toBe(900)
    expect(computeGatewayBox(1000, 1723).h).toBeLessThanOrEqual(204)
  })
})

describe('bezierPoint', () => {
  it('hits the endpoints at t=0/1 and the midpoint in between', () => {
    const path = [
      { x: 0, y: 0 },
      { x: 10, y: 20 },
      { x: 30, y: 20 },
      { x: 40, y: 40 },
    ]
    expect(bezierPoint(path, 0, 0)).toEqual({ x: 0, y: 0 })
    expect(bezierPoint(path, 1, 0)).toEqual({ x: 40, y: 40 })
    const mid = bezierPoint(path, 0.5, 0)
    expect(mid.x).toBeGreaterThan(0)
    expect(mid.x).toBeLessThan(40)
    // lateral 只产生横向散开，t=0/1 端点处无散开（sin(0)=sin(π)=0）
    expect(bezierPoint(path, 0, 5)).toEqual({ x: 0, y: 0 })
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


/**
 * ★★ **边界回归：布局高度下限**（2026-11-23 第十六轮）。
 *
 * ★ 缺陷形态（L23）：`MIN_LAYOUT_HEIGHT` 是**兜底**（把更小的输入抬到该高度
 *   再排布）⇒ ★ 若它**小于布局的真实需求高度**，⇒ ① 被抬上去的那些高度
 *   **抬完还是溢出**（兜底等于没兜）⇒ ② 夹在「真实需求」与「兜底」之间的
 *   那一段**直接溢出**（★ 实测 480~545 整段溢出，底边 546px）⇒ ⇒
 *   ★★ 而这类缺陷**不会让任何一条「抽几个高度采样」的判据变红** ⇒ ⇒
 *   ★★ ⇒ 必须**逐高度扫**才能看见（★ 这就是「边界回归」与「抽样回归」的差别）。
 */
describe('★ 边界回归：布局高度下限', () => {
  /**
   * 布局在给定宽度下**真正排得下**的最小高度。
   * ★ 直接用导出的 `computeRowY` / `computeGatewayBox` 算 ⇒ ★ **绕开**
   *   `computeLayout` 的兜底抬升 ⇒ ★ 否则会被抬高值掩盖真实需求。
   */
  function minFeasibleHeight(width = 1200): number {
    for (let h = 1; h <= 4000; h++) {
      const client = computeRowY(h).client
      const gw = computeGatewayBox(width, h, client)
      const rows = computeRowY(h, gw)
      // 供应端节点卡的**底边**（中心线 + 半卡高）
      if (rows.provider + NODE_H / 2 <= h) return h
    }
    return Number.POSITIVE_INFINITY
  }

  it('① 任何输入高度都不得溢出（★ 逐高度扫 1..1200 ⇒ 旧版 480~545 那段必须也在内）', () => {
    // ★ `computeRowY` 在超出时 console.warn ⇒ ★ 扫描会刷屏 ⇒ ★ 先静音。
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const overflow: string[] = []
      for (let h = 1; h <= 1200; h++) {
        const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: h })
        const bottom = Math.max(...layout.nodes.map((n) => n.y + n.h))
        if (bottom > layout.height) {
          overflow.push(`h=${h}(排布高度 ${layout.height}) 最下沿 ${bottom}px`)
        }
      }
      expect(
        overflow.slice(0, 5),
        `★ 有 ${overflow.length} 个输入高度会让内容溢出画布（★ 头几条：${overflow.slice(0, 3).join(' | ')}）`,
      ).toEqual([])
    } finally {
      warn.mockRestore()
    }
  })

  it('② 边界点本身安全：h = 下限 与 h = 下限-1 都不得溢出（★ 下限-1 应被抬到下限）', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      for (const h of [MIN_LAYOUT_HEIGHT, MIN_LAYOUT_HEIGHT - 1]) {
        const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: h })
        const bottom = Math.max(...layout.nodes.map((n) => n.y + n.h))
        expect(bottom, `h=${h} 时内容底边 ${bottom}px 超过排布高度 ${layout.height}px`).toBeLessThanOrEqual(
          layout.height,
        )
      }
      // h = 下限-1 会被抬到下限（★ 兜底语义）
      const raised = computeLayout({
        nodes: NODES, edges: EDGES, width: 1200, height: MIN_LAYOUT_HEIGHT - 1,
      })
      expect(raised.height, '★ 比下限矮 1px 的输入必须被抬到下限（★ 兜底语义）').toBe(
        MIN_LAYOUT_HEIGHT,
      )
    } finally {
      warn.mockRestore()
    }
  })

  it('③ 退化输入（高度 0 / 1）也被抬到安全下限且不溢出', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      for (const h of [0, 1]) {
        const layout = computeLayout({ nodes: NODES, edges: EDGES, width: 1200, height: h })
        expect(layout.height, `h=${h} 必须被抬到下限`).toBe(MIN_LAYOUT_HEIGHT)
        const bottom = Math.max(...layout.nodes.map((n) => n.y + n.h))
        expect(bottom, `h=${h} 内容底边 ${bottom}px 溢出`).toBeLessThanOrEqual(layout.height)
      }
    } finally {
      warn.mockRestore()
    }
  })

  it('④ 下限必须与布局的真实需求**双向锁死**（★ 不小于需求、不高于需求+余量）', () => {
    // ★ 这条把 `MIN_LAYOUT_HEIGHT` 与**算法**绑在一起（★ 而不是两个各写各的数字）：
    //   · 下限 ≥ 真实最小可行高度 ⇒ ★ 兜底才真的兜得住（★ 否则「抬完仍溢出」）；
    //   · 下限 ≤ 真实最小可行高度 + 余量 ⇒ ★ 下限没有高到离谱（★ 否则布局会按
    //     一个容器永远达不到的高度排布 ⇒ 白白浪费垂直空间）。
    // ★ ⇒ 若上游改了布局算法（间距/层数/大方块高度），本条会红 ⇒ ★ 提示
    // ★   「该把下限重新对一遍」⇒ ★ 而不会等到线上溢出才发现。
    const feasible = minFeasibleHeight()
    expect(
      MIN_LAYOUT_HEIGHT,
      `★ 布局兜底下限（${MIN_LAYOUT_HEIGHT}）不得小于真实最小可行高度（${feasible}）`
        + '⇒ 否则被抬高的那些高度抬完仍然溢出',
    ).toBeGreaterThanOrEqual(feasible)
    expect(
      MIN_LAYOUT_HEIGHT,
      `★ 布局兜底下限（${MIN_LAYOUT_HEIGHT}）不得高于真实最小可行高度（${feasible}）+ 40`
        + '⇒ 否则布局会按容器永远达不到的高度排布 ⇒ 浪费垂直空间',
    ).toBeLessThanOrEqual(feasible + 40)
  })
})
