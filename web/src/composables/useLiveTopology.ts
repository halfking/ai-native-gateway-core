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
import { buildTopology, clientKeyOf, modelKeyOf, providerNodeKeyOf, type TopoGraph } from '../lib/liveTopologyGraph'
import { applyFilters, filterOptions, gatewayModuleStats } from '../lib/liveTopologyFilter'
import {
  DEFAULT_TOPO_FILTERS,
  GATEWAY_CORE_ID,
  PANEL_LIST_LIMIT,
  type GatewayModuleKey,
  type TopoFilters,
} from '../types/liveTopology'

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

  /** 过滤后的请求子集：图、两个面板、粒子全部读它。 */
  const filteredRequests = computed<LiveRequest[]>(() =>
    applyFilters(requestsRef.value as never, filters.value, now()) as never[],
  )

  const graph = computed<TopoGraph>(() =>
    buildTopology({
      requests: filteredRequests.value as never,
      nodes: nodesRef.value as unknown as LiveNodeStatus[],
      now: now(),
      windowMs: filters.value.windowMs,
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
      .slice(0, PANEL_LIST_LIMIT)
  })

  /** 左面板的逐条请求（最新在前；点条目定位到图上）。 */
  const clientRequests = computed<PanelEntry[]>(() =>
    filteredRequests.value
      .slice()
      .reverse()
      .slice(0, PANEL_LIST_LIMIT)
      .map(toPanelEntry),
  )

  /** 右面板：供应商端响应（只含有上游响应的请求；未派发出去的 in_progress 也算它已选定节点）。 */
  const providerResponses = computed<PanelEntry[]>(() =>
    filteredRequests.value
      .filter((r) => providerNodeKeyOf(r as never) !== '')
      .slice()
      .reverse()
      .slice(0, PANEL_LIST_LIMIT)
      .map(toPanelEntry),
  )

  /** 网关核心九个模块的窗口内统计。 */
  const gatewayStats = computed<Record<GatewayModuleKey, { count: number; errors: number }>>(() =>
    gatewayModuleStats(filteredRequests.value as never, now(), filters.value.windowMs, nodesRef.value.length),
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

  const requestById = computed(() => new Map(filteredRequests.value.map((r) => [r.request_id ?? '', r])))

  /** 过滤是否处于「有值但零命中」状态（UI 要把它与「真的没流量」分开说）。 */
  const activeFilterCount = computed(
    () => filters.value.clients.length + filters.value.providers.length + filters.value.models.length + filters.value.nodes.length + (filters.value.status ? 1 : 0),
  )

  const filteredOut = computed(() => filteredRequests.value.length === 0 && requestsRef.value.length > 0)

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
    activeFilterCount,
    filteredOut,
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
    provider: (req.provider_code ?? '').trim() || '-',
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
  }
}
