// useLiveTopology.ts — 实时拓扑页的编排层（2026-10-17 改版）
//
// 职责：把 live-stream SSE 的请求/节点帧 + 本地过滤态，编排成
// 「图（纵向四层）+ 左右两个请求面板 + 网关核心模块统计」共用的同一份数据。
// **关键约束**：图、左面板、右面板、左栏当天统计必须收敛到同一个过滤子集，
// 否则用户会看到「图里没有、右面板却有」的矛盾画面。
//
// 这里不碰 DOM、不碰 Canvas —— 渲染在组件里，纯函数在 lib/。

import { computed, ref } from 'vue'
import {
  actionsRef,
  nodesRef,
  queueRef,
  requestsRef,
  connectionRef,
  redisHealthyRef,
  redisErrorRef,
  initialDataReceivedRef,
  type ActionEvent,
  type LiveNodeStatus,
  type LiveQueueSnapshot,
  type LiveRequest,
} from './liveStreamStore'
import {
  buildTopology,
  clientKeyOf,
  invalidNodeKeys,
  modelKeyOf,
  providerNodeKeyOf,
  type GraphNodeInput,
  type TopoGraph,
} from '../lib/liveTopologyGraph'
import { applyFilters, filterOptions, gatewayModuleStats } from '../lib/liveTopologyFilter'
import {
  DEFAULT_TOPO_FILTERS,
  GATEWAY_CORE_ID,
  type GatewayModuleKey,
  type TopoFilters,
  type TopoPluginChip,
} from '../types/liveTopology'
import { getTopologyPlugins } from '../api/topology'

export interface PanelEntry {
  requestId: string
  ts: string
  /** 客户端侧：客户端名；供应商侧：供应商 / 凭据 */
  client: string
  provider: string
  nodeId: string
  model: string
  status: string
  latencyMs?: number | null
  tokens?: number | null
  promptTokens?: number | null
  completionTokens?: number | null
  costUsd?: number | null
  isProbe: boolean
  errorKind?: string | null
  failureStage?: string | null
  sessionId?: string
  requestType?: string
  /** 客户端协议（openai / anthropic / …）—— 详情面板与协议 chip 共用。 */
  clientProtocol?: string
}

export interface ClientSummary {
  client: string
  requests: number
  success: number
  failure: number
  inFlight: number
  tokens: number
  lastTs?: string
  topModel: string
}

export function useLiveTopology(opts?: { now?: () => number }) {
  const clock = opts?.now ?? (() => Date.now())
  const filters = ref<TopoFilters>({ ...DEFAULT_TOPO_FILTERS })

  const now = () => clock()

  /** 无效节点键集合（熔断打开/不可达/手动停用）。过滤与建图消费同一份，
   *  否则会出现「节点没了、残边还在」。开关 showInvalidNodes 打开时不启用。 */
  const invalidNodes = computed<Set<string>>(() =>
    invalidNodeKeys(nodesRef.value as unknown as GraphNodeInput[]),
  )
  const hiddenNodes = computed<Set<string> | undefined>(() =>
    filters.value.showInvalidNodes ? undefined : invalidNodes.value,
  )

  /** 过滤后的请求子集：图、两个面板、粒子全部读它。 */
  const filteredRequests = computed<LiveRequest[]>(() =>
    applyFilters(requestsRef.value as never, filters.value, now(), hiddenNodes.value) as never[],
  )

  const graph = computed<TopoGraph>(() =>
    buildTopology({
      requests: filteredRequests.value as never,
      nodes: nodesRef.value as unknown as LiveNodeStatus[],
      now: now(),
      windowMs: filters.value.windowMs,
      hiddenNodes: hiddenNodes.value,
    }),
  )

  const options = computed(() =>
    filterOptions(filteredRequests.value as never, now(), filters.value.windowMs),
  )

  /** 左面板：客户端请求摘要（按客户端聚合，最新在前）。 */
  const clientSummaries = computed<ClientSummary[]>(() => {
    const map = new Map<string, ClientSummary & { last: number; modelCount: Map<string, number> }>()
    for (const req of filteredRequests.value) {
      const key = clientKeyOf(req as never)
      let entry = map.get(key)
      if (!entry) {
        entry = { client: key, requests: 0, success: 0, failure: 0, inFlight: 0, tokens: 0, topModel: '', last: 0, modelCount: new Map() }
        map.set(key, entry)
      }
      entry.requests += 1
      if (req.status === 'success') entry.success += 1
      if (req.status === 'failure' || req.status === 'rate_limited') entry.failure += 1
      if (req.status === 'in_progress') entry.inFlight += 1
      entry.tokens += req.total_tokens ?? 0
      const ts = req.ts ? Date.parse(req.ts) : 0
      if (Number.isFinite(ts) && ts > entry.last) {
        entry.last = ts
        entry.lastTs = req.ts
      }
      const model = modelKeyOf(req as never)
      entry.modelCount.set(model, (entry.modelCount.get(model) ?? 0) + 1)
    }
    return [...map.values()]
      .map(({ last, modelCount, ...rest }) => ({
        ...rest,
        topModel: [...modelCount.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0]?.[0] ?? '',
      }))
      .sort((a, b) => b.requests - a.requests || a.client.localeCompare(b.client))
      .slice(0, 8)
  })

  /** 左面板的逐条请求（最新在前；面板内部按 50/页翻页，这里不再预截断）。 */
  const clientRequests = computed<PanelEntry[]>(() =>
    filteredRequests.value
      .slice()
      .reverse()
      .map(toPanelEntry),
  )

  /** 右面板：供应商端响应（只含有上游响应的请求；未派发出去的 in_progress 也算它已选定节点）。 */
  const providerResponses = computed<PanelEntry[]>(() =>
    filteredRequests.value
      .filter((r) => providerNodeKeyOf(r as never) !== '')
      .slice()
      .reverse()
      .map(toPanelEntry),
  )

  /** 过滤子集里真正流经过的供应端节点数（与图上 provider 列同源）。 */
  const filteredProviderNodeCount = computed(() => {
    const ids = new Set<string>()
    for (const req of filteredRequests.value) {
      const id = providerNodeKeyOf(req as never)
      if (id) ids.add(id)
    }
    // 过滤后一条请求都没有时仍如实反映「当前没有任何供应端在管」，
    // 而不是回退到全局总数（那会把「空」说成「有 N 个节点」）。
    return ids.size
  })

  /** 网关核心九个模块的窗口内统计。 */
  const gatewayStats = computed<Record<GatewayModuleKey, { count: number; errors: number }>>(() =>
    // nodeState 必须跟着过滤走：其余八格都是过滤子集内的量，只有它显示
    // 全量节点数的话，筛窄之后这一格会和旁边八格对不上账。
    // 判据：useLiveTopology.test.ts「nodeState 随节点过滤收敛」。
    gatewayModuleStats(
      filteredRequests.value as never,
      now(),
      filters.value.windowMs,
      filteredProviderNodeCount.value,
    ),
  )

  const queue = computed<LiveQueueSnapshot | null>(() => queueRef.value)

  const coreMetric = computed(() => graph.value.nodes.find((n) => n.id === GATEWAY_CORE_ID)?.metric)

  /** 某条请求在图上的路径（用于「点面板条目 → 高亮链路」）。 */
  function edgePathFor(requestId: string): string[] {
    const req = filteredRequests.value.find((r) => r.request_id === requestId)
    if (!req) return []
    const clientId = `client:${clientKeyOf(req as never)}`
    const providerId = providerNodeKeyOf(req as never)
    const modelId = `model:${modelKeyOf(req as never)}`
    const ids = [clientId, GATEWAY_CORE_ID, ...(providerId ? [providerId] : []), modelId]
    const keys: string[] = []
    for (let i = 0; i < ids.length - 1; i += 1) keys.push(`${ids[i]}->${ids[i + 1]}`)
    return keys
  }

  function nodeIdsFor(requestId: string): string[] {
    const req = filteredRequests.value.find((r) => r.request_id === requestId)
    if (!req) return []
    const providerId = providerNodeKeyOf(req as never)
    return [
      `client:${clientKeyOf(req as never)}`,
      GATEWAY_CORE_ID,
      ...(providerId ? [providerId] : []),
      `model:${modelKeyOf(req as never)}`,
    ]
  }

  function decisionsFor(requestId: string): ActionEvent[] {
    if (!requestId) return []
    const list = actionsRef.value.get(requestId) ?? []
    return [...list].sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
  }

  /**
   * 按 id 取任意一条请求的详情（全页详情路由 /live/request/:id 用）。
   * **刻意读原始 store 而不是过滤子集**：从面板点开一条被开关隐藏的
   * 探测/无效节点请求时，详情页必须还能打开（「隐藏」是展示口径，
   * 不是数据删除）；窗口外/已被淘汰的请求返回 null，由页面如实说明。
   */
  function panelEntryFor(requestId: string): PanelEntry | null {
    if (!requestId) return null
    const req = (requestsRef.value as LiveRequest[]).find((r) => r.request_id === requestId)
    return req ? toPanelEntry(req) : null
  }

  const requestById = computed(() => new Map(filteredRequests.value.map((r) => [r.request_id ?? '', r])))

  /** 过滤是否处于「有值但零命中」状态（UI 要把它与「真的没流量」分开说）。 */
  const activeFilterCount = computed(
    () => filters.value.clients.length + filters.value.providers.length + filters.value.models.length + filters.value.nodes.length + (filters.value.status ? 1 : 0),
  )

  const filteredOut = computed(() => filteredRequests.value.length === 0 && requestsRef.value.length > 0)

  /**
   * 被时间窗口丢掉的请求数。
   *
   * 这个数之前只在 `buildTopology` 里累加、没有任何 UI 消费：窗口调小后图会
   * 变空，但用户分不清是「真的没流量」还是「有流量但落在窗口外」。
   * 两个原因要采取的动作完全不同（等 vs 放宽窗口），所以必须在页面上说清。
   *
   * **不能读 `graph.droppedByWindow`**：`applyFilters` 已经先按窗口筛过一遍，
   * `buildTopology` 拿到手时窗口外的请求早就没了，那个计数器结构性恒为 0。
   * 这里另跑一遍纯计数，判据见 LiveTopologyView.honesty.test.ts。
   */
  const droppedByWindow = computed(() => {
    const cutoff = now() - filters.value.windowMs
    let dropped = 0
    for (const req of requestsRef.value as LiveRequest[]) {
      const ts = req.ts ? Date.parse(req.ts) : NaN
      // 没有时间戳的帧不算「被窗口丢掉」—— 它压根没参与时间判定。
      if (Number.isFinite(ts) && ts < cutoff) dropped += 1
    }
    return dropped
  })

  /**
   * 被两个数据开关隐藏的请求数（窗口内口径，与 droppedByWindow 同层）。
   * 必须说出口：默认隐藏后「图上比预期少」需要能被解释，否则用户会把
   * 「被开关藏了」误读成「没流量/丢数据」。
   */
  const hiddenProbeCount = computed(() => {
    if (filters.value.showProbe) return 0
    const cutoff = now() - filters.value.windowMs
    let n = 0
    for (const req of requestsRef.value as LiveRequest[]) {
      if (!req.is_probe) continue
      const ts = req.ts ? Date.parse(req.ts) : NaN
      if (Number.isFinite(ts) && ts < cutoff) continue
      n += 1
    }
    return n
  })

  const hiddenInvalidCount = computed(() => {
    if (filters.value.showInvalidNodes || !invalidNodes.value.size) return 0
    const cutoff = now() - filters.value.windowMs
    let n = 0
    for (const req of requestsRef.value as LiveRequest[]) {
      const nodeId = providerNodeKeyOf(req as never)
      if (!nodeId || !invalidNodes.value.has(nodeId)) continue
      const ts = req.ts ? Date.parse(req.ts) : NaN
      if (Number.isFinite(ts) && ts < cutoff) continue
      n += 1
    }
    return n
  })

  /** 网关「在用插件」chip（/api/admin/topology/plugins）。拉取失败 = 空清单
   *  + degraded 标记 —— 前端渲染成「插件状态不可用」，不是「没有插件」。 */
  const plugins = ref<TopoPluginChip[]>([])
  const pluginsDegraded = ref(false)

  async function loadPlugins(): Promise<void> {
    try {
      const resp = await getTopologyPlugins()
      if (resp.degraded) {
        plugins.value = []
        pluginsDegraded.value = true
        return
      }
      pluginsDegraded.value = false
      const chips: TopoPluginChip[] = []
      for (const p of resp.builtin ?? []) {
        const direction = p.direction === 'output' ? 'output' : p.direction === 'both' ? 'both' : 'input'
        chips.push({ id: `builtin:${p.name}`, name: p.name, direction, status: 'builtin', external: false })
      }
      for (const p of resp.external ?? []) {
        const status =
          p.status === 'ready' || p.status === 'degraded' || p.status === 'failed' || p.status === 'stopped' || p.status === 'starting'
            ? p.status
            : 'stopped'
        chips.push({ id: `ext:${p.plugin_id}`, name: p.plugin_id, direction: 'input', status, external: true })
      }
      plugins.value = chips
    } catch {
      plugins.value = []
      pluginsDegraded.value = true
    }
  }

  return {
    filters,
    now,
    filteredRequests,
    graph,
    options,
    clientSummaries,
    clientRequests,
    providerResponses,
    gatewayStats,
    queue,
    coreMetric,
    requestById,
    edgePathFor,
    nodeIdsFor,
    decisionsFor,
    panelEntryFor,
    activeFilterCount,
    filteredOut,
    droppedByWindow,
    hiddenProbeCount,
    hiddenInvalidCount,
    invalidNodeIds: invalidNodes,
    plugins,
    pluginsDegraded,
    loadPlugins,
    connection: connectionRef,
    initialDataReceived: initialDataReceivedRef,
    redisHealthy: redisHealthyRef,
    redisError: redisErrorRef,
  }
}

function toPanelEntry(req: LiveRequest): PanelEntry {
  return {
    requestId: req.request_id ?? '',
    ts: req.ts,
    client: clientKeyOf(req as never),
    provider: (req.provider_code ?? '').trim() || '—',
    nodeId: providerNodeKeyOf(req as never),
    model: modelKeyOf(req as never),
    status: req.status ?? 'in_progress',
    latencyMs: req.latency_ms ?? null,
    tokens: req.total_tokens ?? null,
    promptTokens: req.prompt_tokens ?? null,
    completionTokens: req.completion_tokens ?? null,
    costUsd: req.cost_usd ?? null,
    isProbe: !!req.is_probe,
    errorKind: req.error_kind ?? null,
    failureStage: req.failure_stage ?? null,
    sessionId: req.gw_session_id,
    requestType: req.requestType,
    clientProtocol: (req.client_protocol ?? '').trim() || undefined,
  }
}
