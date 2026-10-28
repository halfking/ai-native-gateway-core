// liveTopologyGraph.ts — 实时拓扑构图（纯函数，2026-10-16）
//
// 输入是 live-stream SSE 的两类帧（请求 / 节点健康），输出是四列节点与边。
// **纯函数**：不读时钟、不读全局状态、不产生随机数 —— 时间由参数 `now` 注入，
// 这样单测可以完全确定性地驱动构图逻辑（判据与被测逻辑同层）。

import {
  GATEWAY_CORE_ID,
  TOPO_COLUMN_LIMITS,
  type TopoEdge,
  type TopoHealth,
  type TopoNode,
  type TopoNodeMetric,
} from '../types/liveTopology'

/** 请求帧的最小结构（与 liveStreamStore.LiveRequest 结构兼容）。 */
export interface GraphRequestInput {
  ts?: string
  request_id?: string
  model?: string
  canonical_name?: string
  client_model?: string
  /** model=auto 的请求（2026-10-28 选路层轮：SSE additive 字段）。 */
  is_auto_request?: boolean
  /** auto 分类的任务类型（chat/reasoning/code/…，后端原始词）。 */
  task_type?: string
  /** 会话滑窗压缩的档位观测（below/preliminary/forced）——不是「压缩发生」。 */
  token_band?: string
  /** 压缩层实际改写出站体（环回口径：strategy 非空且非 delta_append）。 */
  compressed?: boolean
  provider_code?: string
  credential_id?: number
  credential_label?: string
  status?: string
  latency_ms?: number | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  total_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  gw_session_id?: string
  /** 任务类型。**线上就是驼峰 `requestType`**（见 liveStreamStore.LiveRequest
   *  2026-08-15 lifecycle 字段注释）；早先误写成 request_type 会让「任务识别」
   *  模块恒为 0 —— 那正是本仓禁止的「用 0 冒充没有」。 */
  requestType?: string
  error_kind?: string | null
  agent_name?: string
  agent_type?: string
  client_profile?: string | null
  client_protocol?: string
  identity_hash?: string | null
  is_probe?: boolean
}

/** node_update 帧的最小结构（与 liveStreamStore.LiveNodeStatus 兼容）。 */
export interface GraphNodeInput {
  credential_id: number
  credential_label?: string
  provider_code?: string
  health_status?: string
  circuit_state?: string
  availability_state?: string
  quota_state?: string
  in_flight?: number
  last_latency_ms?: number
  fp_disabled?: boolean
  /** 运维手动停用 —— 与熔断打开同级判「无效」（不可服务）。 */
  manual_disabled?: boolean
}

export interface BuildTopologyInput {
  requests: readonly GraphRequestInput[]
  nodes: readonly GraphNodeInput[]
  /** 注入的当前时刻（ms）。窗口过滤以此为基准，不用 Date.now()。 */
  now: number
  windowMs: number
  /**
   * 隐藏的供应端节点键集合（`cred:<id>` / `pcode:<code>`）。
   * 2026-10-18「默认去除无效供应商及节点」：这些 node_update **不建图节点**。
   * 经它们的请求由 applyFilters 在上游过滤（过滤与建图必须用同一个集合，
   * 否则会出现「节点没了、流量还在」的残边）。
   */
  hiddenNodes?: ReadonlySet<string>
}

export interface TopoGraph {
  nodes: TopoNode[]
  edges: TopoEdge[]
  /** 窗口内被丢弃（窗口外）的请求数 —— UI 要能说清「图上为什么这么空」。 */
  droppedByWindow: number
  totalTokens: number
}

const OTHER_SUFFIX = 'others'

function emptyMetric(): TopoNodeMetric {
  return { requests: 0, success: 0, failure: 0, tokens: 0, inFlight: 0 }
}

/** 客户端身份键：agent 名优先，其次协议画像、身份哈希；都没有才算未知。 */
export function clientKeyOf(req: GraphRequestInput): string {
  const agent = (req.agent_name ?? '').trim()
  if (agent) return agent
  const profile = (req.client_profile ?? '').trim()
  if (profile) return profile
  const proto = (req.client_protocol ?? '').trim()
  if (proto) return proto
  const identity = (req.identity_hash ?? '').trim()
  if (identity) return identity.slice(0, 8)
  return 'unknown'
}

/**
 * 客户端**易读显示名**（owner 2026-10-18：客户端信息要更易懂）。
 * 身份键是稳定 id（client:claude-code），显示时转成品牌写法：
 *  - 已知品牌走固定映射（Claude Code / ZCode / Codex…）；
 *  - 其它 kebab/snake 键按分段 Title Case（vs-code-insiders → Vs Code Insiders）；
 *  - 十六进制身份前缀原样保留 —— 大写化哈希反而失真；
 *  - 兜底键 'unknown' 由调用方用 i18n「未知客户端」处理（这里不掺文案）。
 * 品牌名与语言无关（不进 locale），但要变只能改这里的映射单点。
 */
const KNOWN_CLIENT_DISPLAY_NAMES: Record<string, string> = {
  'claude-code': 'Claude Code',
  'claude': 'Claude',
  'zcode': 'ZCode',
  'codex': 'Codex',
  'cursor': 'Cursor',
  'copilot': 'Copilot',
  'gemini-cli': 'Gemini CLI',
  'aider': 'Aider',
  'cline': 'Cline',
  'curl': 'curl',
}

export function clientDisplayName(key: string): string {
  const known = KNOWN_CLIENT_DISPLAY_NAMES[key]
  if (known) return known
  // 十六进制身份前缀（或任何带数字开头的短哈希）不动
  if (/^[0-9a-f]{4,}$/i.test(key)) return key
  if (!/^[a-z0-9][a-z0-9_.-]*$/i.test(key)) return key
  return key
    .split(/[-_.]/)
    .map((seg) => (seg ? seg[0].toUpperCase() + seg.slice(1) : seg))
    .join(' ')
}

/** 供应端节点键：凭据级优先；没有凭据时退回「仅供应商」节点。 */
export function providerNodeKeyOf(req: GraphRequestInput): string {
  if (req.credential_id) return `cred:${req.credential_id}`
  const code = (req.provider_code ?? '').trim()
  return code ? `pcode:${code}` : ''
}

/** 模型键：canonical 优先，回落到原始模型名。 */
export function modelKeyOf(req: GraphRequestInput): string {
  const canonical = (req.canonical_name ?? '').trim()
  if (canonical) return canonical
  return (req.model ?? '').trim() || 'unknown'
}

function metricOfStatus(status: string | undefined): 'success' | 'failure' | 'other' {
  if (status === 'success') return 'success'
  if (status === 'failure' || status === 'rate_limited') return 'failure'
  return 'other'
}

/** 节点健康态判定：熔断打开/不可达/手动停用 → down；配额耗尽/降级 → warning；
 *  没上报 → unknown（**不是** healthy：把「没数据」画成绿灯是假绿）。 */
export function healthOfNode(node: GraphNodeInput | undefined): TopoHealth {
  if (!node) return 'unknown'
  if (node.manual_disabled) return 'down'
  const circuit = (node.circuit_state ?? '').toLowerCase()
  const availability = (node.availability_state ?? '').toLowerCase()
  const quota = (node.quota_state ?? '').toLowerCase()
  const health = (node.health_status ?? '').toLowerCase()
  if (circuit.includes('open') || availability.includes('unavailable') || health.includes('unreachable')) return 'down'
  if (node.fp_disabled || quota.includes('exhaust') || circuit.includes('half') || health.includes('warning')) return 'warning'
  if (health === 'healthy') return 'healthy'
  if (health === 'warning') return 'warning'
  return 'unknown'
}

/**
 * 「无效节点」键集合（2026-10-18 默认隐藏口径）：熔断打开 / 不可达 /
 * 手动停用的凭据节点。**warning（配额耗尽/降级）不算无效** —— 它们仍在
 * 服务，只是变差；把 warning 也藏掉会让「半死」的供应商从视野里消失。
 * 过滤（applyFilters）与建图（buildTopology）必须消费同一个集合。
 */
export function invalidNodeKeys(nodes: readonly GraphNodeInput[]): Set<string> {
  const out = new Set<string>()
  for (const node of nodes) {
    if (typeof node?.credential_id !== 'number') continue
    if (healthOfNode(node) === 'down') out.add(`cred:${node.credential_id}`)
  }
  return out
}

/**
 * 构建四列拓扑。
 *
 * 口径说明：
 *  - 窗口外的请求直接丢弃并计数（droppedByWindow），不参与任何节点/边；
 *  - `in_progress` 的请求计入 requests 与 inFlight，但既不算成功也不算失败；
 *  - 每列超过 TOPO_COLUMN_LIMITS 时，多余的合并成一个「其他 N」聚合节点，
 *    边也一并指向它 —— 宁可信息粗一点，也不能让 615 个模型把画布撑爆。
 */
export function buildTopology(input: BuildTopologyInput): TopoGraph {
  const { now, windowMs } = input
  const cutoff = now - windowMs

  const nodesById = new Map<string, TopoNode>()
  const edgesById = new Map<string, TopoEdge>()
  const nodeInputs = new Map<number, GraphNodeInput>()
  let droppedByWindow = 0
  let totalTokens = 0

  const core: TopoNode = {
    id: GATEWAY_CORE_ID,
    column: 'gateway',
    label: 'gateway',
    sub: '',
    health: 'healthy',
    live: true,
    metric: emptyMetric(),
  }
  nodesById.set(GATEWAY_CORE_ID, core)

  const upsertNode = (id: string, column: TopoNode['column'], label: string, sub?: string): TopoNode => {
    let n = nodesById.get(id)
    if (!n) {
      n = { id, column, label, sub, health: 'unknown', live: false, metric: emptyMetric() }
      nodesById.set(id, n)
    } else if (sub && !n.sub) {
      n.sub = sub
    }
    return n
  }

  const bumpEdge = (from: string, to: string, req: GraphRequestInput) => {
    const id = `${from}->${to}`
    let e = edgesById.get(id)
    if (!e) {
      e = { id, from, to, requests: 0, failure: 0 }
      edgesById.set(id, e)
    }
    e.requests += 1
    if (req.status) e.lastStatus = req.status
    if (typeof req.latency_ms === 'number') e.lastLatencyMs = req.latency_ms
    if (metricOfStatus(req.status) === 'failure') e.failure += 1
    // 客户端→网关边：按协议累计（chip 渲染的数据源）。其他边没有
    // client_protocol 语义，不聚合（宁缺毋假）。
    if (to === GATEWAY_CORE_ID && from.startsWith('client:')) {
      const proto = (req.client_protocol ?? '').trim()
      if (proto) {
        if (!e.protocolCounts) e.protocolCounts = {}
        e.protocolCounts[proto] = (e.protocolCounts[proto] ?? 0) + 1
      }
    }
  }

  for (const node of input.nodes) {
    if (typeof node?.credential_id !== 'number') continue
    nodeInputs.set(node.credential_id, node)
  }

  for (const req of input.requests) {
    const ts = req.ts ? Date.parse(req.ts) : NaN
    if (Number.isFinite(ts) && ts < cutoff) {
      droppedByWindow += 1
      continue
    }

    const clientId = `client:${clientKeyOf(req)}`
    const providerId = providerNodeKeyOf(req)
    const modelId = `model:${modelKeyOf(req)}`

    const clientLabel = clientKeyOf(req)
    const client = upsertNode(clientId, 'client', clientLabel, req.agent_type ?? undefined)
    const model = upsertNode(modelId, 'model', modelKeyOf(req), req.provider_code ?? undefined)
    const status = metricOfStatus(req.status)
    const tokens = typeof req.total_tokens === 'number' ? req.total_tokens : 0
    if (tokens > 0) totalTokens += tokens

    for (const target of [client, core, model]) {
      target.metric.requests += 1
      if (status === 'success') target.metric.success += 1
      if (status === 'failure') target.metric.failure += 1
      if (req.status === 'in_progress') target.metric.inFlight += 1
      target.metric.tokens += tokens
      if (typeof req.latency_ms === 'number') target.metric.lastLatencyMs = req.latency_ms
    }

    let provider: TopoNode | undefined
    if (providerId) {
      const label = req.credential_label || (req.credential_id ? `#${req.credential_id}` : (req.provider_code ?? ''))
      provider = upsertNode(providerId, 'provider', label, req.provider_code ?? undefined)
      provider.providerCode = req.provider_code
      provider.credentialId = req.credential_id
      provider.metric.requests += 1
      if (status === 'success') provider.metric.success += 1
      if (status === 'failure') provider.metric.failure += 1
      if (req.status === 'in_progress') provider.metric.inFlight += 1
      provider.metric.tokens += tokens
      if (typeof req.latency_ms === 'number') provider.metric.lastLatencyMs = req.latency_ms
      if (req.is_probe) provider.isProbeOnly = true
    }

    bumpEdge(clientId, GATEWAY_CORE_ID, req)
    if (providerId) bumpEdge(GATEWAY_CORE_ID, providerId, req)
    if (providerId) bumpEdge(providerId, modelId, req)
    else bumpEdge(GATEWAY_CORE_ID, modelId, req)
  }

  // node_update 的实时健康与在途数覆盖到 provider 列（node_update 可能先于/晚于请求到达）。
  for (const [credentialId, inputNode] of nodeInputs) {
    const id = `cred:${credentialId}`
    // 无效节点默认隐藏（2026-10-18）：不建图节点。若已有请求流经它建了节点，
    // 说明过滤与建图用了不同集合（上游漏过滤）——这里按隐藏口径一并移除，
    // 并把它的边留到 keepIds 过滤阶段自然断链（from/to 不在 keep 集即丢）。
    if (input.hiddenNodes?.has(id)) {
      nodesById.delete(id)
      continue
    }
    const target = nodesById.get(id)
    if (!target) {
      // 还没有任何请求流经它：仍然建节点（这是「供应端当前有哪些」的真值来源），
      // 但 metric 为空 + live=false，UI 画成中性灰而不是绿色。
      const created = upsertNode(
        id,
        'provider',
        inputNode.credential_label || `#${credentialId}`,
        inputNode.provider_code ?? undefined,
      )
      created.providerCode = inputNode.provider_code
      created.credentialId = credentialId
      created.health = healthOfNode(inputNode)
      created.live = true
      if (typeof inputNode.in_flight === 'number') created.metric.inFlight = inputNode.in_flight
      if (typeof inputNode.last_latency_ms === 'number') created.metric.lastLatencyMs = inputNode.last_latency_ms
      continue
    }
    target.health = healthOfNode(inputNode)
    target.live = true
    if (typeof inputNode.in_flight === 'number') target.metric.inFlight = inputNode.in_flight
    if (typeof inputNode.last_latency_ms === 'number') target.metric.lastLatencyMs = inputNode.last_latency_ms
    if (!target.sub) target.sub = inputNode.provider_code
  }

  const collapsed = collapseColumns([...nodesById.values()])
  const keepIds = new Set(collapsed.nodes.map((n) => n.id))
  const edges = [...edgesById.values()].filter((e) => keepIds.has(e.from) && keepIds.has(e.to))

  return {
    nodes: collapsed.nodes,
    edges,
    droppedByWindow,
    totalTokens,
  }
}

/**
 * 每列按请求数降序截断到 TOPO_COLUMN_LIMITS，多余合并成「其他 N」。
 * gateway 列固定 1（核心节点），不做截断。
 * 返回的节点顺序即列内排序，供布局直接使用。
 */
export function collapseColumns(nodes: TopoNode[]): { nodes: TopoNode[] } {
  const out: TopoNode[] = []
  for (const column of ['client', 'gateway', 'provider', 'model'] as const) {
    const limit = TOPO_COLUMN_LIMITS[column]
    const inColumn = nodes
      .filter((n) => n.column === column)
      .sort((a, b) => b.metric.requests - a.metric.requests || a.id.localeCompare(b.id))
    if (inColumn.length <= limit) {
      out.push(...inColumn)
      continue
    }
    const keep = inColumn.slice(0, limit)
    const rest = inColumn.slice(limit)
    const aggregate: TopoNode = {
      id: `${column}${OTHER_SUFFIX}`,
      column,
      label: `+${rest.length}`,
      sub: column,
      health: 'unknown',
      live: false,
      isAggregate: true,
      metric: emptyMetric(),
    }
    for (const n of rest) {
      aggregate.metric.requests += n.metric.requests
      aggregate.metric.success += n.metric.success
      aggregate.metric.failure += n.metric.failure
      aggregate.metric.tokens += n.metric.tokens
      aggregate.metric.inFlight += n.metric.inFlight
    }
    out.push(...keep, aggregate)
  }
  return { nodes: out }
}
