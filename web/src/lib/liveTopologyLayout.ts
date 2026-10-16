// liveTopologyLayout.ts — 拓扑布局（纯函数，2026-10-17 改版：上→下 + 网关核心整体）
//
// 四层自上而下：客户端 → **网关核心大方块** → 供应端节点 → 模型。
// 网关核心不是一个圆点，而是横跨中段的一个大矩形，内部 3×3 排布九个处理模块
// （缓存/脱敏/路由/节点状态/会话文件/会话写入/错误记录/探测任务/任务识别），
// 主链路模块实线、旁路模块虚线（承 GATEWAY_BRANCH_MODULES 的语义）。
//
// **必须确定性**：同输入同输出 —— 每次刷新节点乱跳，运维没法形成肌肉记忆，
// 且所有「节点在第几行」的断言失效。抖动来自 id 的稳定哈希，不用 Math.random()。

import {
  GATEWAY_MODULES,
  TOPO_COLUMNS,
  type GatewayCoreBox,
  type TopoColumn,
  type TopoEdge,
  type TopoLayout,
  type TopoLayoutEdge,
  type TopoLayoutNode,
  type TopoNode,
} from '../types/liveTopology'

export interface LayoutInput {
  nodes: readonly TopoNode[]
  edges: readonly TopoEdge[]
  width: number
  height: number
}

/** 节点卡尺寸。导出给判据用：重叠这类断言必须与布局共享同一份尺寸，
 *  否则测试里写死一个数字，布局一改就变成「测的不是它」。 */
export const NODE_W = 150
export const NODE_H = 42
/** 每层中心线的高度占比（自上而下）。网关核心那一层由大方块自己占位。 */
const ROW_RATIO: Record<TopoColumn, number> = {
  client: 0.09,
  gateway: 0.5,
  provider: 0.74,
  model: 0.93,
}

/**
 * 网关核心大方块：横向占 78%。
 *
 * 高度占比 0.40 是**算出来的**，不是拍脑袋：大方块底边必须让开供应端
 * 节点卡的**上沿**（`rowY.provider - NODE_H/2`），而不能只让开中心线。
 * 取 0.42 时底边落在 440、节点卡上沿 438 —— 压了 2px，肉眼几乎看不出，
 * 但它是真重叠，所以判据按节点卡而不是按中心线来钉。
 */
const GATEWAY_BOX = { xRatio: 0.11, yRatio: 0.29, wRatio: 0.78, hRatio: 0.4 }
/**
 * 3×3 网格。`header` 是**必须预留的标题带**：
 * 大方块顶部有一行「网关核心 · 缓存·脱敏·…」的标题（组件里是正常流的
 * `.gw__head`，不在格子里）。不预留的话第一行模块会从 `pad=10px` 处
 * 开始，正好被标题压住 —— 这是真实组件里同样会发生的重叠，
 * 所以必须在**布局函数**里让开，而不是靠 CSS 碰运气。
 */
const GATEWAY_GRID = { cols: 3, rows: 3, pad: 10, gap: 8, header: 28 }

/** 稳定字符串哈希（FNV-1a 32bit）：只用于派生抖动，不用于安全。 */
export function stableHash(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash >>> 0
}

/** 把 [min,max] 确定性抖动。 */
function jitter(id: string, salt: number, min: number, max: number): number {
  const unit = (stableHash(`${id}#${salt}`) % 1000) / 1000
  return min + unit * (max - min)
}

/** 计算网关核心大方块的几何（内部 3×3 模块格）。 */
export function computeGatewayBox(width: number, height: number): GatewayCoreBox {
  const x = Math.round(width * GATEWAY_BOX.xRatio)
  const y = Math.round(height * GATEWAY_BOX.yRatio)
  const w = Math.round(width * GATEWAY_BOX.wRatio)
  const h = Math.round(height * GATEWAY_BOX.hRatio)
  const innerW = w - GATEWAY_GRID.pad * 2
  const innerH = h - GATEWAY_GRID.pad * 2 - GATEWAY_GRID.header
  const cellW = (innerW - GATEWAY_GRID.gap * (GATEWAY_GRID.cols - 1)) / GATEWAY_GRID.cols
  const cellH = (innerH - GATEWAY_GRID.gap * (GATEWAY_GRID.rows - 1)) / GATEWAY_GRID.rows
  const gridTop = y + GATEWAY_GRID.pad + GATEWAY_GRID.header
  const cells = GATEWAY_MODULES.map((mod) => ({
    key: mod.key,
    x: Math.round(x + GATEWAY_GRID.pad + mod.col * (cellW + GATEWAY_GRID.gap)),
    y: Math.round(gridTop + mod.row * (cellH + GATEWAY_GRID.gap)),
    w: Math.round(cellW),
    h: Math.round(cellH),
  }))
  return { x, y, w, h, cells }
}

export function computeLayout(input: LayoutInput): TopoLayout {
  const width = Math.max(640, Math.floor(input.width || 0))
  const height = Math.max(480, Math.floor(input.height || 0))
  const gateway = computeGatewayBox(width, height)

  const rowY = {} as Record<TopoColumn, number>
  for (const column of TOPO_COLUMNS) {
    rowY[column] = Math.round(height * ROW_RATIO[column])
  }

  const laid: TopoLayoutNode[] = []
  for (const column of TOPO_COLUMNS) {
    if (column === 'gateway') continue // 网关核心由大方块承载，不再是单个小节点
    const inLayer = input.nodes.filter((n) => n.column === column)
    if (!inLayer.length) continue
    const ordered = [...inLayer].sort((a, b) => b.metric.requests - a.metric.requests || a.id.localeCompare(b.id))
    const spanW = width - NODE_W - 24
    ordered.forEach((node, index) => {
      const slot = spanW / (ordered.length + 1)
      const jitterX = ordered.length > 1 ? jitter(node.id, 1, -Math.min(8, slot / 8), Math.min(8, slot / 8)) : 0
      const jitterY = ordered.length > 1 ? jitter(node.id, 2, -4, 4) : 0
      laid.push({
        ...node,
        w: NODE_W,
        h: NODE_H,
        x: Math.round(12 + slot * (index + 1) + jitterX),
        y: Math.round(rowY[column] - NODE_H / 2 + jitterY),
      })
    })
  }

  const byId = new Map(laid.map((n) => [n.id, n]))
  const edges: TopoLayoutEdge[] = []
  for (const edge of input.edges) {
    const a = byId.get(edge.from)
    const b = byId.get(edge.to)
    if (!a || !b) continue
    // 纵向流程：客户端/供应端 → 模型/网关 走下边；反之走上边。
    const downward = a.y <= b.y
    const y1 = downward ? a.y + a.h : a.y
    const y2 = downward ? b.y : b.y + b.h
    const x1 = a.x + a.w / 2
    const x2 = b.x + b.w / 2
    const midY = (y1 + y2) / 2
    const bow = jitter(edge.id, 3, -14, 14)
    edges.push({
      ...edge,
      path: [
        { x: x1, y: y1 },
        { x: x1 + bow, y: midY },
        { x: x2 + bow * 0.4, y: midY },
        { x: x2, y: y2 },
      ],
    })
  }

  return { nodes: laid, edges, width, height, rowY, gateway }
}

/** 边粗细（px）：按窗口内请求数对数映射，上限 5px。
 *  0 请求 → 0.8px 的中性细线（**不是**红色：没流量≠故障）。 */
export function edgeWidth(requests: number): number {
  if (requests <= 0) return 0.8
  return Math.min(5, 0.8 + Math.log10(requests + 1) * 1.6)
}

/** 粒子大小（半径 px）：按该请求 token 量分 3 档；无 token 走中档。 */
export function particleRadius(totalTokens: number | null | undefined): number {
  if (!totalTokens || totalTokens <= 0) return 3
  if (totalTokens < 500) return 2.4
  if (totalTokens < 4000) return 3.4
  return 4.6
}
