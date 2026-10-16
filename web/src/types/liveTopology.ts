// liveTopology.ts — 实时拓扑首页的数据模型（2026-10-16）
//
// 四列语义链路：客户端 → 网关核心 → 供应端节点（凭据级）→ 模型。
// 本文件只放**类型与纯常量**，不含任何副作用；构图/布局/过滤的纯函数在
// `src/lib/liveTopology*.ts`，便于单测直接驱动（判据必须与被测逻辑同层）。

export type TopoColumn = 'client' | 'gateway' | 'provider' | 'model'

export const TOPO_COLUMNS: readonly TopoColumn[] = ['client', 'gateway', 'provider', 'model'] as const

/** 节点健康态。`unknown` 与 `down` 必须分开：没有数据 ≠ 故障。 */
export type TopoHealth = 'healthy' | 'warning' | 'down' | 'unknown'

/** 过滤窗内的聚合口径（由前端按 SSE 事件滚动聚合，不是全量真值）。 */
export interface TopoNodeMetric {
  requests: number
  success: number
  failure: number
  tokens: number
  inFlight: number
  lastLatencyMs?: number
}

export interface TopoNode {
  id: string
  column: TopoColumn
  label: string
  sub?: string
  /** provider 列：所属供应商（分组标题用） */
  providerCode?: string
  credentialId?: number
  health: TopoHealth
  /** 节点实时态来自 node_update；false = 该来源没上报过（渲染成中性） */
  live: boolean
  metric: TopoNodeMetric
  /** 「其他 N」聚合节点标记：不是真实节点，点击不展开详情 */
  isAggregate?: boolean
  isProbeOnly?: boolean
}

export interface TopoEdge {
  id: string
  from: string
  to: string
  requests: number
  failure: number
  lastStatus?: string
  lastLatencyMs?: number
}

export interface TopoLayoutNode extends TopoNode {
  x: number
  y: number
  w: number
  h: number
}

export interface TopoLayoutEdge extends TopoEdge {
  /** 贝塞尔控制点（画流动曲线用；两段控制点让线在列间形成轻微弓形） */
  path: Array<{ x: number; y: number }>
}

export interface TopoLayout {
  nodes: TopoLayoutNode[]
  edges: TopoLayoutEdge[]
  width: number
  height: number
  columnX: Record<TopoColumn, number>
}

/** 全局过滤态（进 URL query，可分享）。空串/空数组 = 不过滤。 */
export interface TopoFilters {
  provider: string
  client: string
  model: string
  status: '' | 'success' | 'in_progress' | 'failure' | 'rate_limited'
  /** 时间窗（毫秒）。只影响「近窗」聚合与边粗细，不改节点健康。 */
  windowMs: number
}

export const TOPO_WINDOW_PRESETS: ReadonlyArray<{ value: number; labelKey: string }> = [
  { value: 5 * 60_000, labelKey: 'liveTopology.window.5m' },
  { value: 30 * 60_000, labelKey: 'liveTopology.window.30m' },
  { value: 2 * 60 * 60_000, labelKey: 'liveTopology.window.2h' },
]

export const DEFAULT_TOPO_FILTERS: TopoFilters = {
  provider: '',
  client: '',
  model: '',
  status: '',
  windowMs: 5 * 60_000,
}

/** 各列默认 Top N + 「其他」聚合节点的上限（性能预算见方案 §5.3）。 */
export const TOPO_COLUMN_LIMITS: Record<TopoColumn, number> = {
  client: 8,
  gateway: 1,
  provider: 8,
  model: 6,
}

/** 粒子硬上限（方案 §5.2）。超出按最旧丢弃，并在页头提示已聚合。 */
export const TOPO_PARTICLE_CAP = 240

export const GATEWAY_CORE_ID = 'gateway:core'
