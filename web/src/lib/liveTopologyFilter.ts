// liveTopologyFilter.ts — 拓扑过滤与过滤项候选（纯函数，2026-10-16）
//
// 设计约束（方案 §7）：
//  - 过滤**只影响呈现**（图 + 左右侧栏），不下推到 SSE —— SSE 是全局广播，
//    按客户端下推会破坏「超管看全局 / 租户看自己」的既有租户模型；
//  - 状态过滤只过滤**请求与粒子**，不过滤节点健康着色 —— 否则「只看失败」
//    会把健康节点画成灰色，读起来像节点坏了；
//  - 时间窗只影响近窗聚合，不改健康判定。

import type { TopoFilters } from '../types/liveTopology'
import type { GraphRequestInput } from './liveTopologyGraph'

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

export function matchesFilters(req: GraphRequestInput, filters: TopoFilters, now: number): boolean {
  if (!withinWindow(req, now, filters.windowMs)) return false
  if (filters.status && (req.status ?? '') !== filters.status) return false
  if (filters.provider) {
    const code = (req.provider_code ?? '').trim()
    const id = req.credential_id ? `cred:${req.credential_id}` : `pcode:${code}`
    if (code !== filters.provider && id !== filters.provider) return false
  }
  if (filters.client) {
    const key = (req.agent_name ?? '').trim() || (req.client_profile ?? '').trim()
      || (req.client_protocol ?? '').trim() || (req.identity_hash ?? '').trim().slice(0, 8) || 'unknown'
    if (key !== filters.client) return false
  }
  if (filters.model) {
    const key = (req.canonical_name ?? '').trim() || (req.model ?? '').trim() || 'unknown'
    if (key !== filters.model) return false
  }
  return true
}

export function applyFilters(
  requests: readonly GraphRequestInput[],
  filters: TopoFilters,
  now: number,
): GraphRequestInput[] {
  return requests.filter((r) => matchesFilters(r, filters, now))
}

/**
 * 生成过滤项候选（供应商 / 客户端 / 模型）。
 * 只统计**窗口内**的请求（与过滤语义一致），并附命中数，让用户看得见
 * 「选了会剩多少」，而不是选完发现图空了。
 *
 * 供应商下拉同时给两种可选项：
 *  - `value = code`（该供应商整体）
 *  - `value = cred:<id>`（单个凭据节点，label 形如 `deepseek #7`）
 * 两者在 matchesFilters 里都可匹配；分开是因为供应端节点是**凭据级**的
 * （2026-10-16 拍板），用户有时只想看一把 key 的链路。
 */
export function filterOptions(
  requests: readonly GraphRequestInput[],
  now: number,
  windowMs: number,
  limit = 12,
): { providers: FilterOption[]; clients: FilterOption[]; models: FilterOption[] } {
  const providers = new Map<string, FilterOption>()
  const clients = new Map<string, number>()
  const models = new Map<string, number>()
  const bump = (m: Map<string, number>, key: string) => m.set(key, (m.get(key) ?? 0) + 1)
  for (const req of requests) {
    if (!withinWindow(req, now, windowMs)) continue
    const code = (req.provider_code ?? '').trim()
    if (code) {
      const prev = providers.get(code)?.count ?? 0
      providers.set(code, { value: code, label: code, count: prev + 1 })
    }
    if (req.credential_id) {
      const key = `cred:${req.credential_id}`
      const prev = providers.get(key)?.count ?? 0
      providers.set(key, { value: key, label: code ? `${code} #${req.credential_id}` : `#${req.credential_id}`, count: prev + 1 })
    }
    const clientKey = (req.agent_name ?? '').trim() || (req.client_profile ?? '').trim()
      || (req.client_protocol ?? '').trim() || (req.identity_hash ?? '').trim().slice(0, 8) || 'unknown'
    bump(clients, clientKey)
    const modelKey = (req.canonical_name ?? '').trim() || (req.model ?? '').trim() || 'unknown'
    bump(models, modelKey)
  }
  const top = (m: Map<string, number>): FilterOption[] =>
    [...m.entries()]
      .map(([value, count]) => ({ value, label: value, count }))
      .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
      .slice(0, limit)
  const topProviders = [...providers.values()]
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
    .slice(0, limit)
  return { providers: topProviders, clients: top(clients), models: top(models) }
}

/** 过滤后是否还有任何请求（空图 vs 被过滤空，是两回事，UI 要分开说）。 */
export function isFilteredEmpty(requests: readonly GraphRequestInput[]): boolean {
  return requests.length === 0
}
