// liveTopologyFilter.ts — 拓扑过滤与过滤项候选（纯函数，2026-10-17 多选版）
//
// 多选语义（2026-10-17 维护者要求「可单选或多选」）：
//  - **同维度内 OR**：客户端选了 A、B，只要命中其一就保留；
//  - **跨维度 AND**：客户端命中 且 供应商命中 且 模型命中 且 节点命中 才保留；
//  - 空选 = 该维度不过滤。
// 这条语义必须与判据钉死（测试里有「多选 OR」「跨维 AND」「空选不过滤」三条），
// 否则前后端/前后理解不一致时，症状是「图上少了一半、右栏又多一半」。
//
// 另外两条不变的设计约束（2026-10-16 轮定）：
//  - 过滤**只影响呈现**，不下推到 SSE（SSE 是全局广播）；
//  - 状态过滤只过滤请求与粒子，不改节点健康着色。

import type { GatewayModuleKey, TopoFilters } from '../types/liveTopology'
import { GATEWAY_MODULES } from '../types/liveTopology'
import type { GraphRequestInput } from './liveTopologyGraph'
import { clientKeyOf, modelKeyOf, providerNodeKeyOf } from './liveTopologyGraph'

export interface FilterOption {
  value: string
  label: string
  count: number
}

function withinWindow(req: GraphRequestInput, now: number, windowMs: number): boolean {
  const ts = req.ts ? Date.parse(req.ts) : NaN
  if (!Number.isFinite(ts)) return true // 没有时间戳的帧不因窗口被丢（宁可多画）
  return ts >= now - windowMs
}

/** 同维度 OR：空选放行全部。 */
function anyOf(candidates: readonly string[], actual: string): boolean {
  if (!candidates.length) return true
  return candidates.includes(actual)
}

export function matchesFilters(
  req: GraphRequestInput,
  filters: TopoFilters,
  now: number,
  hiddenNodes?: ReadonlySet<string>,
): boolean {
  if (!withinWindow(req, now, filters.windowMs)) return false
  // 2026-10-18 数据开关（默认不选 = 默认隐藏）：
  //  - 探测请求：showProbe=false 时整条排除（图/面板/统计一致收敛）；
  //  - 无效节点：经熔断打开/不可达/手动停用节点的请求排除。
  // 隐藏集合由调用方（useLiveTopology）从 nodesRef 算好传入 —— 过滤层
  // 不读全局 store，保持可单测。
  if (!filters.showProbe && req.is_probe) return false
  // 语义收敛在函数内：开关打开时即便调用方仍传了集合也不隐藏
  //（调用方组合错误不能变成「双保险变双重过滤」）。
  if (!filters.showInvalidNodes && hiddenNodes && hiddenNodes.size) {
    const nodeId = providerNodeKeyOf(req)
    if (nodeId && hiddenNodes.has(nodeId)) return false
  }
  if (filters.status && (req.status ?? '') !== filters.status) return false
  if (!anyOf(filters.clients, clientKeyOf(req))) return false
  if (!anyOf(filters.models, modelKeyOf(req))) return false
  const code = (req.provider_code ?? '').trim()
  if (!anyOf(filters.providers, code) && !anyOf(filters.providers, providerNodeKeyOf(req))) return false
  if (filters.nodes.length) {
    const nodeId = providerNodeKeyOf(req)
    if (!filters.nodes.includes(nodeId)) return false
  }
  return true
}

export function applyFilters(
  requests: readonly GraphRequestInput[],
  filters: TopoFilters,
  now: number,
  hiddenNodes?: ReadonlySet<string>,
): GraphRequestInput[] {
  return requests.filter((r) => matchesFilters(r, filters, now, hiddenNodes))
}

/**
 * 生成四维过滤项候选（客户端 / 供应商 / 模型 / 节点）。
 * 只统计**窗口内**的请求（与过滤语义一致），并附命中数，让用户看得见
 * 「选了会剩多少」，而不是选完发现图空了。
 *
 * 供应商与节点是两档口径：`deepseek` 是供应商整体，`deepseek #7` 是单把凭据。
 */
export function filterOptions(
  requests: readonly GraphRequestInput[],
  now: number,
  windowMs: number,
  limit = 12,
): {
  clients: FilterOption[]
  providers: FilterOption[]
  models: FilterOption[]
  nodes: FilterOption[]
} {
  const clients = new Map<string, number>()
  const providers = new Map<string, FilterOption>()
  const models = new Map<string, number>()
  const nodes = new Map<string, FilterOption>()
  const bump = (m: Map<string, number>, key: string) => m.set(key, (m.get(key) ?? 0) + 1)

  for (const req of requests) {
    if (!withinWindow(req, now, windowMs)) continue
    bump(clients, clientKeyOf(req))
    bump(models, modelKeyOf(req))

    const code = (req.provider_code ?? '').trim()
    if (code) {
      const prev = providers.get(code)?.count ?? 0
      providers.set(code, { value: code, label: code, count: prev + 1 })
    }
    const nodeId = providerNodeKeyOf(req)
    if (nodeId) {
      const label = code
        ? `${code} ${req.credential_label ? `#${req.credential_id} ${req.credential_label}` : `#${req.credential_id}`}`
        : nodeId
      const prev = nodes.get(nodeId)?.count ?? 0
      nodes.set(nodeId, { value: nodeId, label, count: prev + 1 })
    }
  }

  const topNumbers = (m: Map<string, number>): FilterOption[] =>
    [...m.entries()]
      .map(([value, count]) => ({ value, label: value, count }))
      .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
      .slice(0, limit)
  const topObjects = (m: Map<string, FilterOption>): FilterOption[] =>
    [...m.values()].sort((a, b) => b.count - a.count || a.value.localeCompare(b.value)).slice(0, limit)

  return {
    clients: topNumbers(clients),
    providers: topObjects(providers),
    models: topNumbers(models),
    nodes: topObjects(nodes),
  }
}

/**
 * 网关核心九个模块的窗口内命中统计。
 *
 * 口径说明（**能算的算，算不出的留空**）：
 *  - cache：**缓存命中**的请求数（带 cache_read/cache_write token 的那些）——
 *    只有命中才有可看的量，「全部请求都过了缓存」这种数字等于没说；
 *  - sanitize / route：主链路请求数（每条都经过）；
 *  - sessionFile / sessionWrite：带 gw_session_id 的请求数（会话真的被写了）；
 *  - nodeState：node_update 上报的节点数（在图上有供应端节点 = 有状态可管）；
 *  - errorRecord：失败请求数；
 *  - probeTask：is_probe 请求数；
 *  - taskDetect：带 requestType（非 main）的请求数。
 *
 * **errors 只装失败/异常数**（`GatewayModuleStat.errors` 的类型契约）。
 * 早先版本把缓存命中数塞进 `errors`，而 `GatewayCoreBox` 用 `errors > 0`
 * 决定画红 —— 结果是**缓存工作正常反而显示成故障红**。命中是成功信号，
 * 不是异常信号，必须走 `count`。
 *
 * 没有真实命中的模块返回 0，UI 画成「无流量」而不是编一个数字。
 */
export function gatewayModuleStats(
  requests: readonly GraphRequestInput[],
  now: number,
  windowMs: number,
  nodeCount: number,
): Record<GatewayModuleKey, { count: number; errors: number }> {
  const stats = {} as Record<GatewayModuleKey, { count: number; errors: number }>
  for (const mod of GATEWAY_MODULES) stats[mod.key] = { count: 0, errors: 0 }

  let failures = 0
  for (const req of requests) {
    if (!withinWindow(req, now, windowMs)) continue
    const isFail = req.status === 'failure' || req.status === 'rate_limited'
    if (isFail) failures += 1
    // 缓存：只数「命中」，命中不是异常 ⇒ 不进 errors（否则格子被画成故障红）
    if ((req.cache_read_tokens ?? 0) > 0 || (req.cache_write_tokens ?? 0) > 0) stats.cache.count += 1
    stats.sanitize.count += 1
    stats.route.count += 1
    stats.sessionWrite.count += req.gw_session_id ? 1 : 0
    stats.sessionFile.count += req.gw_session_id ? 1 : 0
    if (req.is_probe) stats.probeTask.count += 1
    if (req.requestType && req.requestType !== 'main') stats.taskDetect.count += 1
  }
  stats.nodeState.count = nodeCount
  stats.errorRecord.count = failures
  stats.errorRecord.errors = failures
  return stats
}
