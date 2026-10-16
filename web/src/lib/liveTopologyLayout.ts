// liveTopologyLayout.ts — 拓扑布局（纯函数，2026-10-16）
//
// 四列固定 x，列内纵向按请求数降序等分。**必须确定性**：同输入同输出，
// 否则每次刷新节点乱跳，运维没法形成肌肉记忆（也导致单测没法钉住）。
// 抖动来自 id 的字符串哈希，不用 Math.random()。

import {
  TOPO_COLUMNS,
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

const NODE_W = 148
const NODE_H = 44
/** 列 x 的宽度占比；四段留白对称，视觉上左右平衡。 */
const COLUMN_RATIO: Record<TopoColumn, number> = {
  client: 0.15,
  gateway: 0.35,
  provider: 0.64,
  model: 0.87,
}

/** 稳定字符串哈希（FNV-1a 32bit）：只用于派生抖动，不用于安全。 */
export function stableHash(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash >>> 0
}

/** 把 [-1,1] 确定性抖动映射到 [min,max]。 */
function jitter(id: string, salt: number, min: number, max: number): number {
  const unit = (stableHash(`${id}#${salt}`) % 1000) / 1000
  return min + unit * (max - min)
}

export function computeLayout(input: LayoutInput): TopoLayout {
  const width = Math.max(640, Math.floor(input.width || 0))
  const height = Math.max(320, Math.floor(input.height || 0))
  const columnX = {} as Record<TopoColumn, number>
  for (const column of TOPO_COLUMNS) {
    columnX[column] = Math.round(width * COLUMN_RATIO[column])
  }

  const laid: TopoLayoutNode[] = []
  for (const column of TOPO_COLUMNS) {
    const inColumn = input.nodes.filter((n) => n.column === column)
    if (!inColumn.length) continue
    // 排序键：请求数降序 → id 字典序（保证跨刷新稳定）
    const ordered = [...inColumn].sort((a, b) => b.metric.requests - a.metric.requests || a.id.localeCompare(b.id))
    const slot = height / (ordered.length + 1)
    ordered.forEach((node, index) => {
      const centerY = slot * (index + 1)
      // 抖动只在多节点列生效：单节点列抖动没有意义，只会引入无谓的偏移。
      const jitterY = ordered.length > 1 ? jitter(node.id, 1, -Math.min(6, slot / 6), Math.min(6, slot / 6)) : 0
      const jitterX = ordered.length > 1 ? jitter(node.id, 2, -4, 4) : 0
      laid.push({
        ...node,
        w: NODE_W,
        h: NODE_H,
        x: Math.round(columnX[column] + jitterX),
        y: Math.round(centerY - NODE_H / 2 + jitterY),
      })
    })
  }

  const byId = new Map(laid.map((n) => [n.id, n]))
  const edges: TopoLayoutEdge[] = []
  for (const edge of input.edges) {
    const a = byId.get(edge.from)
    const b = byId.get(edge.to)
    if (!a || !b) continue
    const x1 = a.x + a.w
    const y1 = a.y + a.h / 2
    const x2 = b.x
    const y2 = b.y + b.h / 2
    const midX = (x1 + x2) / 2
    // 轻微弓形，方向由 id 哈希决定：多条并行链路不会完全重叠成一条。
    const bow = jitter(edge.id, 3, -18, 18)
    edges.push({
      ...edge,
      path: [
        { x: x1, y: y1 },
        { x: midX, y: (y1 + y2) / 2 + bow },
        { x: midX, y: (y1 + y2) / 2 - bow * 0.4 },
        { x: x2, y: y2 },
      ],
    })
  }

  return { nodes: laid, edges, width, height, columnX }
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
