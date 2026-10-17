// liveTopology.ts — 实时拓扑首页的数据模型（2026-10-17 改版：上到下 + 网关核心整体）
//
// 流程方向：**上 → 下**（2026-10-17 维护者拍板，原为左→右）：
//   第 1 层 客户端 → 第 2 层 网关核心（一个大方块，内部按模块流程排布）
//   → 第 3 层 供应端节点 → 第 4 层 模型
// 两侧是请求面板：左侧客户端请求摘要、右侧供应商响应请求（可点击放大）。
// 顶部是四维多选过滤：客户端 / 供应商 / 模型 / 节点。
//
// 本文件只放**类型与纯常量**，不含副作用；构图/布局/过滤的纯函数在
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

/** 网关核心内部的处理模块（2026-10-17 维护者点名的九个环节，按处理顺序排）。 */
export type GatewayModuleKey =
  | 'cache'
  | 'sanitize'
  | 'route'
  | 'nodeState'
  | 'sessionFile'
  | 'sessionWrite'
  | 'errorRecord'
  | 'probeTask'
  | 'taskDetect'

export interface GatewayModuleDef {
  key: GatewayModuleKey
  /** 3×3 网格内的位置（行、列），确定性布局的一部分。 */
  row: number
  col: number
}

/**
 * 网关核心内部模块的**流程顺序**（2026-10-17 维护者给定）：
 * 缓存 → 脱敏 → 路由 → 节点状态管理 → 会话数据文件存储 → 数据会话写入
 * → 错误记录 → 探测任务 → 任务识别。
 *
 * 注意最后三个（错误记录 / 探测任务 / 任务识别）语义上是**旁路**（不是串行
 * 卡在主链路上），但维护者要求它们作为一个整体方块呈现，所以仍排在同一块内，
 * 用 `branch: true` 标出「旁路」视觉语义（虚线边框），避免读图的人以为
 * 每个请求都要依次走完九步。
 */
export const GATEWAY_MODULES: readonly GatewayModuleDef[] = [
  { key: 'cache', row: 0, col: 0 },
  { key: 'sanitize', row: 0, col: 1 },
  { key: 'route', row: 0, col: 2 },
  { key: 'nodeState', row: 1, col: 0 },
  { key: 'sessionFile', row: 1, col: 1 },
  { key: 'sessionWrite', row: 1, col: 2 },
  { key: 'errorRecord', row: 2, col: 0 },
  { key: 'probeTask', row: 2, col: 1 },
  { key: 'taskDetect', row: 2, col: 2 },
] as const

/** 旁路模块（不串在主链路上，渲染成虚线边框）。 */
export const GATEWAY_BRANCH_MODULES: ReadonlySet<GatewayModuleKey> = new Set<GatewayModuleKey>([
  'errorRecord',
  'probeTask',
  'taskDetect',
])

export interface GatewayModuleStat {
  key: GatewayModuleKey
  /** 窗口内经过该模块的请求数（旁路模块 = 命中数）。 */
  count: number
  /** 失败/异常数（仅旁路与终态模块有意义）。 */
  errors: number
}

export interface TopoLayoutNode extends TopoNode {
  x: number
  y: number
  w: number
  h: number
}

export interface TopoLayoutEdge extends TopoEdge {
  path: Array<{ x: number; y: number }>
  /**
   * 折叠前的**原始** edge id 集合（通常 1 条，折叠合并后可能多条）。
   *
   * 为什么需要：窄屏下节点会被折叠，图上的边 id 于是变成
   * `client:x->model:folded:8` 这种，而视图的 `edgePathFor` 产出的是原始
   * `client:x->model:deepseek-chat`。高亮按 id 集合比对 ⇒ 对不上 ⇒
   * 「点面板条目高亮该链路」静默失效。有了它，高亮可按原始 id 命中。
   */
  sourceIds: string[]
}

/** 网关核心大方块的几何（纵向流程下的「中间那一层」）。 */
export interface GatewayCoreBox {
  x: number
  y: number
  w: number
  h: number
  /** 3×3 模块格的左上角与尺寸（与 GATEWAY_MODULES 的 row/col 对应）。 */
  cells: Array<{ key: GatewayModuleKey; x: number; y: number; w: number; h: number }>
}

export interface TopoLayout {
  nodes: TopoLayoutNode[]
  edges: TopoLayoutEdge[]
  width: number
  height: number
  /** 纵向流程下每一层的 y（中心线）。 */
  rowY: Record<TopoColumn, number>
  gateway: GatewayCoreBox
}

/**
 * 全局过滤态（进 URL query，可分享）。**四个维度都是多选**：
 * 同维度内 OR（命中任一即保留），跨维度 AND。
 */
export interface TopoFilters {
  clients: string[]
  providers: string[]
  models: string[]
  /** 节点 = 供应端凭据（`cred:<id>` 或 `pcode:<code>`） */
  nodes: string[]
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
  clients: [],
  providers: [],
  models: [],
  nodes: [],
  status: '',
  windowMs: 5 * 60_000,
}

/** 各层默认 Top N + 「其他」聚合节点的上限（性能预算见方案 §5.3）。 */
export const TOPO_COLUMN_LIMITS: Record<TopoColumn, number> = {
  client: 8,
  gateway: 1,
  provider: 8,
  model: 6,
}

/** 粒子硬上限（方案 §5.2）。超出按最旧丢弃，并在页头提示已聚合。 */
export const TOPO_PARTICLE_CAP = 240

export const GATEWAY_CORE_ID = 'gateway:core'

/** 右侧请求面板（供应商响应）与左侧（客户端请求）的条数上限。 */
export const PANEL_LIST_LIMIT = 60
