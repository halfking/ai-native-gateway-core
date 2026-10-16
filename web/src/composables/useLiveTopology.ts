// useLiveTopology.ts — 实时拓扑页的编排层（2026-10-16）
//
// 职责：把 live-stream SSE 的请求/节点帧 + 本地过滤态，编排成
// 「图 + 左右侧栏 + 粒子」三者共用的同一份数据。**关键约束**：
// 图、供应商统计栏、请求窄条必须收敛到同一个过滤子集，否则用户会看到
// 「图里没有、右栏却有」的矛盾画面（判据见 LiveTopologyView.filters.test.ts）。
//
// 这里不碰 DOM、不碰 Canvas —— 渲染在 TopologyCanvas.vue，纯函数在 lib/。

import { computed, ref, watch } from 'vue'
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
import { applyFilters, filterOptions } from '../lib/liveTopologyFilter'
import {
  DEFAULT_TOPO_FILTERS,
  GATEWAY_CORE_ID,
  type TopoFilters,
  type TopoNode,
} from '../types/liveTopology'

/** 请求窄条保留条数（超出淘汰最旧）。 */
export const STRIP_LIMIT = 60

export interface StripEntry {
  requestId: string
  ts: string
  label: string
  provider: string
  model: string
  status: string
  latencyMs?: number | null
  tokens?: number | null
  isProbe: boolean
  errorKind?: string | null
}

export function useLiveTopology(opts?: { now?: () => number }) {
  const clock = opts?.now ?? (() => Date.now())
  const filters = ref<TopoFilters>({ ...DEFAULT_TOPO_FILTERS })

  const now = () => clock()

  /** 过滤后的请求子集：图、粒子、右栏全部读它。 */
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

  const options = computed(() => filterOptions(filteredRequests.value as never, now(), filters.value.windowMs))

  /** 右栏窄条：最新在前，超过 STRIP_LIMIT 淘汰。 */
  const strip = computed<StripEntry[]>(() =>
    filteredRequests.value
      .slice()
      .reverse()
      .slice(0, STRIP_LIMIT)
      .map((r) => ({
        requestId: r.request_id ?? '',
        ts: r.ts,
        label: clientKeyOf(r as never),
        provider: (r.provider_code ?? '').trim() || '-',
        model: modelKeyOf(r as never),
        status: r.status ?? 'in_progress',
        latencyMs: r.latency_ms ?? null,
        tokens: r.total_tokens ?? null,
        isProbe: !!r.is_probe,
        errorKind: r.error_kind ?? null,
      })),
  )

  const stripIndex = computed(() => {
    const map = new Map<string, number>()
    strip.value.forEach((entry, i) => map.set(entry.requestId, i))
    return map
  })

  /** 某条请求在图上的三段路径（用于「点右栏 → 高亮链路」）。 */
  function edgePathFor(requestId: string): { from: string; to: string }[] {
    const req = filteredRequests.value.find((r) => r.request_id === requestId)
    if (!req) return []
    const clientId = `client:${clientKeyOf(req as never)}`
    const providerId = providerNodeKeyOf(req as never)
    const modelId = `model:${modelKeyOf(req as never)}`
    const path = [{ from: clientId, to: GATEWAY_CORE_ID }]
    if (providerId) {
      path.push({ from: GATEWAY_CORE_ID, to: providerId })
      path.push({ from: providerId, to: modelId })
    } else {
      path.push({ from: GATEWAY_CORE_ID, to: modelId })
    }
    return path
  }

  /** 队列/调度投影（网关核心节点的副标题：深度 + 等待 p95）。 */
  const queue = computed<LiveQueueSnapshot | null>(() => queueRef.value)

  const coreMetric = computed(() => {
    const core = graph.value.nodes.find((n) => n.id === GATEWAY_CORE_ID)
    return core?.metric
  })

  const nodesById = computed(() => new Map(graph.value.nodes.map((n) => [n.id, n])))

  /** 选中对象的路由判定时间线（request_lifecycle 的 action 序列）。 */
  function decisionsFor(requestId: string): ActionEvent[] {
    if (!requestId) return []
    const list = actionsRef.value.get(requestId) ?? []
    return [...list].sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
  }

  const providerCodes = computed(() => {
    const set = new Set<string>()
    for (const n of graph.value.nodes) {
      if (n.column === 'provider' && n.providerCode) set.add(n.providerCode)
    }
    return [...set].sort()
  })

  const providerNodesByCode = computed(() => {
    const map = new Map<string, TopoNode[]>()
    for (const n of graph.value.nodes) {
      if (n.column !== 'provider' || !n.providerCode) continue
      const list = map.get(n.providerCode) ?? []
      list.push(n)
      map.set(n.providerCode, list)
    }
    return map
  })

  /** 过滤是否处于「有值但零命中」状态（UI 要把它与「真的没流量」分开说）。 */
  const activeFilterCount = computed(
    () =>
      ['provider', 'client', 'model', 'status'].filter((k) => {
        const v = filters.value[k as keyof TopoFilters]
        return typeof v === 'string' && v !== ''
      }).length,
  )

  const filteredOut = computed(() => filteredRequests.value.length === 0 && requestsRef.value.length > 0)

  watch(
    () => filters.value.windowMs,
    () => {
      // 窗口变化不重置过滤，但要让上层重新拉一次 provider 当天统计的水位提示
    },
  )

  return {
    filters,
    now,
    filteredRequests,
    graph,
    options,
    strip,
    stripIndex,
    queue,
    coreMetric,
    nodesById,
    providerCodes,
    providerNodesByCode,
    decisionsFor,
    edgePathFor,
    activeFilterCount,
    filteredOut,
    connection: connectionRef,
    initialDataReceived: initialDataReceivedRef,
    redisHealthy: redisHealthyRef,
    redisError: redisErrorRef,
  }
}
