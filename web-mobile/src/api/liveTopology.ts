import { req, type RequestOptions } from './client'

/**
 * liveTopology.ts — live 页的三组区间统计（2026-10-29 单一统计轮的移动端接面）。
 *
 * GET /api/admin/topology/providers-today   GET /api/admin/topology/models-today
 * GET /api/admin/topology/clients-today
 *
 * 三条都是 `admin(...)`（admin/handler.go:1145/1156/1160）⇒ **admin 档**，
 * tenant_admin 可用。与桌面 web/src/api/topology.ts 同契约，但桌面那份还带着
 * `?by=provider` 交叉展开与插件清单——本页不消费那些，不整模块照搬。
 *
 * ★ 本族最要紧的四件事（与桌面 topology.ts 文件头同口径）：
 * (1) ★★★★ `?window=5m|1h` 走 request_logs 直查（实时无滞后）；
 *     缺省 = 当天日桶（stats_usage_daily，rollup ≤1h 滞后）。
 *     ⚠️ 乱写的 window 被后端**静默落回 day**（topology_window_stats.go:32-36
 *     「其余一律走 day 日桶路径」）——但响应的 `window` 字段回显实际口径，
 *     调用方应以回显为准显示，而不是自己记发了什么。
 * (2) ★★★★ `degraded: true` = 聚合表未迁移/未装配 —— **必须渲染成「统计不可用」**，
 *     不是零行。零行会被人读成「今天没人用」。
 * (3) ★★★★ `source_high_watermark` 是数据水位：day 口径可能滞后真实时刻 ≤1h。
 *     UI 必须显式展示，否则「数字对不上」无从解释。
 * (4) ★★★ share 是 0..1 比例（客户端/供应商/模型三组同口径）；渲染百分比
 *     要 ×100。cost_usd 是美元浮点。
 */

/** 三组统计共用的行形状（后端三个结构体的字段并集，缺的键不出现）。 */
export interface LiveStatRow {
  /** 供应商组：catalog_code；模型组无此键（用 raw_model_name）；客户端组是 client_key */
  catalog_code?: string
  display_name?: string
  /** 客户端组的稳定身份键（agent → profile → protocol → identity 前 8 位 → unknown） */
  client_key?: string
  /** 客户端组的显示键（与 client_key 同值；类型收窄用） */
  canonical_name?: string
  raw_model_name?: string
  requests: number
  success: number
  failure: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens?: number
  credits_charged: number
  cost_usd: number
  /** 占比 0..1 */
  share: number
}

/** 三组统计共用的信封（window / watermark / degraded 契约同源）。 */
export interface LiveStatsEnvelope {
  day_utc?: string
  window_start_utc?: string
  tenant_scope?: string
  generated_at?: string
  rollup_cadence?: string
  /** 短窗口口径回显：?window= 请求时为 "5m"/"1h"；day 模式不输出该键 */
  window?: string
  /** day 模式的 rollup 水位封顶（可能滞后 ≤1h；短窗口口径恒空） */
  source_high_watermark?: string
  totals?: {
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
  items?: LiveStatRow[]
  /** 聚合表未迁移/未装配 —— 渲染「统计不可用」，不是零行 */
  degraded?: boolean
  error_code?: string
}

export type LiveStatsWindow = '5m' | '1h'

function windowQuery(window?: LiveStatsWindow): string {
  return window ? `?window=${window}` : ''
}

export function fetchProvidersToday(
  window?: LiveStatsWindow,
  options?: RequestOptions,
): Promise<LiveStatsEnvelope> {
  return req<LiveStatsEnvelope>('GET', `/api/admin/topology/providers-today${windowQuery(window)}`, undefined, options)
}

export function fetchModelsToday(
  window?: LiveStatsWindow,
  options?: RequestOptions,
): Promise<LiveStatsEnvelope> {
  return req<LiveStatsEnvelope>('GET', `/api/admin/topology/models-today${windowQuery(window)}`, undefined, options)
}

export function fetchClientsToday(
  window?: LiveStatsWindow,
  options?: RequestOptions,
): Promise<LiveStatsEnvelope> {
  return req<LiveStatsEnvelope>('GET', `/api/admin/topology/clients-today${windowQuery(window)}`, undefined, options)
}

/**
 * 行的显示名。三个后端结构体的名字键不同源（display_name / raw_model_name /
 * client_key），视图不该自己写三份取值链 —— 收在这里。
 * canonical_name（模型组真名）优先于 raw_model_name（原始请求名，可能是一串别名）。
 */
export function liveStatRowName(row: LiveStatRow): string {
  if (row.display_name) return row.display_name
  if (row.canonical_name) return row.canonical_name
  if (row.raw_model_name) return row.raw_model_name
  if (row.client_key) return row.client_key
  return ''
}

/** 行的成功率：分母 0 时 null（渲染层显「—」，不许 0% 冒充）。 */
export function liveStatRowSuccessRate(row: LiveStatRow): number | null {
  if (!row.requests) return null
  return (row.success / row.requests) * 100
}
