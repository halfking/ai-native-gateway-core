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
  totals?: TopologyProviderTodayTotals
  items?: TopologyProviderTodayItem[]
  /** 聚合表未迁移 / 未装配 —— 必须渲染成「统计不可用」，不是零行 */
  degraded?: boolean
  error_code?: string
}

export function getProvidersToday(options?: { signal?: AbortSignal }): Promise<TopologyProvidersToday> {
  return req<TopologyProvidersToday>('GET', '/api/admin/topology/providers-today', undefined, {
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
