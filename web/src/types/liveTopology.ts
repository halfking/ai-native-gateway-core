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
  /**
   * 客户端→网关边上的协议分布（2026-10-18 owner 要求：连线要标注协议）。
   * 键 = client_protocol（openai / anthropic / gemini / …），值 = 窗口内条数。
   * 只在 client→gateway 边上聚合；渲染成小字 chip + 着色。
   */
  protocolCounts?: Record<string, number>
}

/** 网关核心内部的处理模块（2026-10-18 三带布局：进站链 / 存储转发带 / 出站链；
 *  2026-10-28 选路层轮：进站链加入 压缩 + 模型选择，路由/节点状态移入
 *  基础服务侧条 —— 它们是支撑主线的服务，不是主链的数据处理步骤）。 */
export type GatewayModuleKey =
  | 'cache'
  | 'sanitize'
  | 'compress'
  | 'modelSelect'
  | 'route'
  | 'nodeState'
  | 'sessionFile'
  | 'sessionWrite'
  | 'errorRecord'
  | 'probeTask'
  | 'taskDetect'

/**
 * 网关大方块内的**流程带**：
 *
 *   进·请求链（in，主线，L→R）：缓存 → 脱敏 →（插件带）→ 模型选择 → 压缩 → ⬇派发
 *     ★ 顺序与真实管线一致（批判式审计修正 2026-10-28）：会话压缩在
 *     **候选解析之后**才跑（handler.go SessionCompressor.Prepare 注释
 *     "Runs AFTER candidate resolution so we know the target model's context
 *     window"）——早先版本把压缩排在模型选择之前，违反本组件「模块按
 *     处理流程的真实位置排布」的立身原则（owner 2026-10-18）。
 *   选路扇出带（select）：模型选择这一步的三个**出口分解**（auto 按任务 /
 *     内部直连 / 指定模型）——它是模型选择格的明细注解带，不是主链的
 *     串行步骤，所以不带派发箭头、卡间不画箭头。
 *   存储转发带（store）：转发队列（真实 pipeline depth）+ 数据会话写入 ⇄ 会话文件存储
 *   出·响应链（out，R→L）：任务识别 / 错误记录 / 探测任务（旁路族，虚线）
 *   基础服务侧条（不在 lane 里，box 右侧竖条）：路由管理 / 节点状态管理 ——
 *     2026-10-28 owner 口径「路由与节点状态属于基础服务，放一侧，突出核心逻辑」。
 */
export type GatewayLane = 'in' | 'select' | 'store' | 'out' | 'infra'

export interface GatewayModuleDef {
  key: GatewayModuleKey
  lane: GatewayLane
  order: number
}

export const GATEWAY_IN_MODULES: readonly GatewayModuleDef[] = [
  { key: 'cache', lane: 'in', order: 0 },
  { key: 'sanitize', lane: 'in', order: 1 },
  { key: 'modelSelect', lane: 'in', order: 2 },
  { key: 'compress', lane: 'in', order: 3 },
] as const

/** 基础服务侧条（box 右侧竖条，不在主链上）：选路与节点治理。 */
export const GATEWAY_INFRA_MODULES: readonly GatewayModuleDef[] = [
  { key: 'route', lane: 'infra', order: 0 },
  { key: 'nodeState', lane: 'infra', order: 1 },
] as const

export const GATEWAY_STORE_MODULES: readonly GatewayModuleDef[] = [
  { key: 'sessionWrite', lane: 'store', order: 0 },
  { key: 'sessionFile', lane: 'store', order: 1 },
] as const

export const GATEWAY_OUT_MODULES: readonly GatewayModuleDef[] = [
  { key: 'taskDetect', lane: 'out', order: 0 },
  { key: 'errorRecord', lane: 'out', order: 1 },
  { key: 'probeTask', lane: 'out', order: 2 },
] as const

/** 全部模块（判据/统计遍历用）：各带并集，顺序稳定。 */
export const GATEWAY_MODULES: readonly GatewayModuleDef[] = [
  ...GATEWAY_IN_MODULES,
  ...GATEWAY_INFRA_MODULES,
  ...GATEWAY_STORE_MODULES,
  ...GATEWAY_OUT_MODULES,
] as const

/** 模块 → 所属带（组件渲染按带分组，不在这里再写第二份映射）。 */
export function laneOfModule(key: GatewayModuleKey): GatewayLane {
  const def = GATEWAY_MODULES.find((m) => m.key === key)
  return def?.lane ?? 'in'
}

/** 旁路模块（不串在主链路上，渲染成虚线边框）。 */
export const GATEWAY_BRANCH_MODULES: ReadonlySet<GatewayModuleKey> = new Set<GatewayModuleKey>([
  'errorRecord',
  'probeTask',
  'taskDetect',
])

/**
 * 「在用插件」chip（/api/admin/topology/plugins）。direction 决定 chip 落在
 * 哪条带（input/both → 进站链，output → 出站链）；status 只对外部插件
 * 有意义（builtin 固定 'builtin'）。
 */
export type TopoPluginStatus = 'builtin' | 'ready' | 'degraded' | 'failed' | 'stopped' | 'starting'

export interface TopoPluginChip {
  id: string
  /** 显示名：builtin 用 name，external 用 plugin_id。 */
  name: string
  direction: 'input' | 'output' | 'both'
  status: TopoPluginStatus
  external: boolean
}

export interface GatewayModuleStat {
  key: GatewayModuleKey
  /** 窗口内经过该模块的请求数（旁路模块 = 命中数）。 */
  count: number
  /** 失败/异常数（仅旁路与终态模块有意义）。 */
  errors: number
}

/**
 * 选路扇出带（select lane）的统计 —— 模型选择这一步的三个出口
 * （2026-10-28 owner 口径：「内部直连模型」与「auto 按任务选最终模型」
 * 这一层此前的图上没有表达）。
 *
 * 口径（能算的算，算不出的如实缺省）：
 *  - auto：is_auto_request 的请求；autoTasks 是它们的任务类型分布（top N，
 *    任务类型是后端 auto 分类的原始词，如 chat/reasoning/code —— 拉丁词
 *    不进语言包，跨语种一致）；
 *  - direct（内部直连）：provider_code ∈ 本地供应商集合（providers.kind
 *    === 'local'：ollama/mlx/llamacpp/lmstudio/vllm/local-*，内网直连、
 *    无真实 API key）。供应商清单取不到（接口失败）时该桶如实为 0 并由
 *    调用方标注「未测量」，而不是把直连请求误记成「指定模型」；
 *  - explicit：其余（客户端点名模型 / 别名 → canonical）；
 *  - total：窗口内总请求数，恒等 auto + direct + explicit（判据钉死，
 *    三桶分拣漏一条都会被抓到）。
 */
export interface ModelSelectionStats {
  auto: number
  autoTasks: ReadonlyArray<{ type: string; count: number }>
  direct: number
  explicit: number
  total: number
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

/** 网关核心大方块的几何（三带：进站链 / 存储转发带 / 出站链）。 */
export interface GatewayCoreBox {
  x: number
  y: number
  w: number
  h: number
  /**
   * 三带的横向带区（判据钉「带不越框、带间不重叠」用；组件内部
   * 模块格按正常文档流排布，不再逐格绝对定位 —— 绝对定位换来的是
   * 每次改带内顺序都要同步两处几何，2026-10-18 三带改版起弃用）。
   */
  lanes: Array<{ lane: GatewayLane; x: number; y: number; w: number; h: number }>
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
 *
 * 两个数据开关（2026-10-18 owner 要求「默认去除」）：
 *  - showProbe：false（默认）= 探测请求从图/面板/统计中隐藏；
 *  - showInvalidNodes：false（默认）= 经无效节点（熔断打开/不可达/手动停用）
 *    的请求隐藏，无效节点本身也不上图。「无效」判据见 liveTopologyGraph.invalidNodeKeys。
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
  /** 显示探测请求（默认 false：探测流量默认不出现在数据里）。 */
  showProbe: boolean
  /** 显示无效供应商/节点及其流量（默认 false）。 */
  showInvalidNodes: boolean
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
  showProbe: false,
  showInvalidNodes: false,
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

/** 右侧请求面板（供应商响应）与左侧（客户端请求）的单页上限。
 *  2026-10-18 owner 要求「分页最多一页 50 条」——面板内部翻页，
 *  数据源不再预先截断到 60。 */
export const PANEL_PAGE_SIZE = 50
