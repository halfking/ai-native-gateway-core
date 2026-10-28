// topology.ts — 实时拓扑首页的后端读接口（2026-10-16）
//
// 只有一条新端点：供应商当天统计。左栏的「当天」口径由后端 stats_usage_daily
// 决定（与看板/账单同源），前端不自己再算一套 —— 双口径迟早分叉。

import { req } from './_core'

export interface TopologyProviderTodayItem {
  provider_id: number
  catalog_code: string
  display_name: string
  enabled: boolean
  /** providers 表里查得到；provider_id=0 的未知桶为 false（不丢流量，只标未知） */
  known: boolean
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  /** 占当日总请求的比重（0..1） */
  share: number
}

export interface TopologyProviderTodayTotals {
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
}

export interface TopologyProvidersToday {
  day_utc?: string
  window_start_utc?: string
  tenant_scope?: string
  generated_at?: string
  /** 聚合表的数据水位：与真实发生时刻可能差 ≤1h，UI 必须显式展示 */
  source_high_watermark?: string
  rollup_cadence?: string
  /** 短窗口口径回显（2026-10-28 头部统计轮）：?window= 请求时为 "5m"/"1h" */
  window?: string
  totals?: TopologyProviderTodayTotals
  items?: TopologyProviderTodayItem[]
  /** 聚合表未迁移 / 未装配 —— 必须渲染成「统计不可用」，不是零行 */
  degraded?: boolean
  error_code?: string
}

export function getProvidersToday(options?: {
  signal?: AbortSignal
  /** 短窗口口径（5m / 1h）：request_logs 直查，实时无 rollup 滞后 */
  window?: '5m' | '1h'
}): Promise<TopologyProvidersToday> {
  const query = options?.window ? `?window=${options.window}` : ''
  return req<TopologyProvidersToday>('GET', `/api/admin/topology/providers-today${query}`, undefined, {
    signal: options?.signal,
  })
}

// ── 在用插件（2026-10-18）──────────────────────────────────────────────
// GET /api/admin/topology/plugins —— 网关大方块插件带的真值来源。
// builtin = 治理管线注册的内置插件（Name/Direction 来自插件自身）；
// external = 插件进程状态（ready/degraded/failed，health loop 实时改写）。
// degraded 只留给「装配没发生」；空清单 = 真的没有，两类语义不得混。

export interface TopologyBuiltinPlugin {
  name: string
  direction: 'input' | 'output' | 'both' | string
}

export interface TopologyExternalPlugin {
  plugin_id: string
  plugin_version: string
  status: string
}

export interface TopologyPlugins {
  generated_at?: string
  builtin?: TopologyBuiltinPlugin[]
  external?: TopologyExternalPlugin[]
  degraded?: boolean
  error_code?: string
}

export function getTopologyPlugins(options?: { signal?: AbortSignal }): Promise<TopologyPlugins> {
  return req<TopologyPlugins>('GET', '/api/admin/topology/plugins', undefined, {
    signal: options?.signal,
  })
}

// ── 供应商 kind（2026-10-28 选路层轮）──────────────────────────────
// GET /api/admin/topology/provider-kinds —— 「内部直连」判定（kind='local'）
// 的真值来源。★ 刻意不走 /api/providers：那个端点要求 providerConsole 权限
// （super_admin / default 租户 tenant_admin），而 /live 任意租户管理员可进，
// 辅助信息不该要求更高权限（批判式审计修正）。degraded = DB 不可用 ——
// 前端渲染成「未测量」，不是「没有直连供应商」。

export interface TopologyProviderKind {
  code: string
  kind: string
}

export interface TopologyProviderKinds {
  generated_at?: string
  items?: TopologyProviderKind[]
  degraded?: boolean
  error_code?: string
}

export function getTopologyProviderKinds(options?: { signal?: AbortSignal }): Promise<TopologyProviderKinds> {
  return req<TopologyProviderKinds>('GET', '/api/admin/topology/provider-kinds', undefined, {
    signal: options?.signal,
  })
}

// ── 模型当天统计（2026-10-28 live 页当天汇总轮）────────────────────
// GET /api/admin/topology/models-today —— 与 providers-today 同表同口径
// （stats_usage_daily 的 provider_model 维度在 canonical_id 上再聚合）。
// 同一标准模型跨供应商/凭据/别名合并成一行（与拓扑图模型列的 canonical
// 口径一致）。水位语义同 providers-today：rollup 每小时重建，≤1h 滞后。
//
// ?by=provider（2026-10-28 遗留①）：交叉展开——items 各带 providers 子行
// （该模型在哪些供应商上各跑了多少）。子行 share 是**模型内**分布
// （分母 = 该模型的 requests），父行 share 仍是当日总量占比。

export interface TopologyModelProviderItem {
  provider_id: number
  catalog_code: string
  display_name: string
  /** providers 表里查得到；provider_id=0 的未知桶为 false（不丢流量，只标未知） */
  known: boolean
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  /** 模型内分布（0..1，分母 = 该模型的 requests） */
  share: number
}

export interface TopologyModelTodayItem {
  canonical_id: number
  canonical_name: string
  raw_model_name: string
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  /** 占当日总请求的比重（0..1） */
  share: number
  /** ?by=provider 交叉展开的子行；默认调用不出现（形状兼容） */
  providers?: TopologyModelProviderItem[]
}

export interface TopologyModelsToday {
  day_utc?: string
  tenant_scope?: string
  generated_at?: string
  rollup_cadence?: string
  source_high_watermark?: string
  /** 短窗口口径回显（2026-10-28 头部统计轮）：?window= 请求时为 "5m"/"1h" */
  window?: string
  items?: TopologyModelTodayItem[]
  truncated?: boolean
  total_requests?: number
  /** 聚合表未迁移 / 未装配 —— 必须渲染成「统计不可用」，不是零行 */
  degraded?: boolean
  error_code?: string
}

export function getTopologyModelsToday(options?: {
  signal?: AbortSignal
  /** 交叉展开：每个模型附 providers 子行（模型内分布）。仅 day 日桶提供 */
  byProvider?: boolean
  /** 短窗口口径（5m / 1h）：request_logs 直查，实时无 rollup 滞后 */
  window?: '5m' | '1h'
}): Promise<TopologyModelsToday> {
  const params = new URLSearchParams()
  if (options?.byProvider) params.set('by', 'provider')
  if (options?.window) params.set('window', options.window)
  const query = params.size ? `?${params.toString()}` : ''
  return req<TopologyModelsToday>('GET', `/api/admin/topology/models-today${query}`, undefined, {
    signal: options?.signal,
  })
}

// ── 客户端区间统计（2026-10-29 单一统计轮）────────────────────────────
// GET /api/admin/topology/clients-today —— 客户端维度没有日桶（usage_facts 不
// 携带客户端身份列），恒直查 request_logs；day 模式后端自行以 rollup 水位封顶
// （对齐 providers/models 组的日桶滞后，同面板三组对得上账）。client_key 与
// 前端 clientKeyOf 同口径，显示名由 clientDisplayName 映射。

export interface TopologyClientTodayItem {
  /** 稳定身份键（agent → profile → protocol → identity 前 8 位 → unknown） */
  client_key: string
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  /** 占区间总请求的比重（0..1） */
  share: number
}

export interface TopologyClientsToday {
  day_utc?: string
  /** 窗口口径的区间起点（day 模式不输出） */
  window_start_utc?: string
  tenant_scope?: string
  generated_at?: string
  rollup_cadence?: string
  /** 短窗口口径回显：?window= 请求时为 "5m"/"1h" */
  window?: string
  /** day 模式实际套用的 rollup 水位封顶（对齐日桶滞后） */
  source_high_watermark?: string
  items?: TopologyClientTodayItem[]
  truncated?: boolean
  total_requests?: number
  degraded?: boolean
  error_code?: string
}

export function getClientsToday(options?: {
  signal?: AbortSignal
  /** 短窗口口径（5m / 1h）；缺省 = 当天（UTC 日起点 + 水位封顶） */
  window?: '5m' | '1h'
}): Promise<TopologyClientsToday> {
  const query = options?.window ? `?window=${options.window}` : ''
  return req<TopologyClientsToday>('GET', `/api/admin/topology/clients-today${query}`, undefined, {
    signal: options?.signal,
  })
}
