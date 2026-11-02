import { req } from './_core'

// probe-lane.ts —— 探测流量物理隔离车道的读侧 API（2026-11-02）。
//
// ★ 为什么需要这个文件：
//
// 物理隔离开启（LLM_GATEWAY_PROBE_LANE_PHYSICAL=1）后，探测流量不再进业务表，
// 而是写进探测族表 session_turns_probe_hot。而既有的
// `getRequestLogs({ lane: 'probe' })` 读的是**业务表**上的历史探测行
// （服务端 admin/analytics.go 的 probeRequestFilter 注释自陈「物理分表开启前
// 读业务表上的历史探测行」）⇒ 隔离开启后它读到的永远是**隔离开启前**的历史行。
//
// ⇒ 探测运维页要看到「当前」的探测流水，必须走本文件的独立端点。
//   两个来源**都要读**（不是一个替代另一个）：
//   · 车道开着 → 新端点有当前数据，历史端点只有旧数据；
//   · 车道没开 → 新端点为空（探测在业务表里），历史端点才有数据。

/** 探测车道流水的一行（后端 admin/probe_lane_read.go 的 ProbeLaneRequestRow）。 */
export interface ProbeLaneRequestRow {
  request_id: string
  ts: string
  tenant_id: string
  credential_id?: string | null
  raw_model_name?: string | null
  client_model?: string | null
  outbound_model?: string | null
  success: boolean
  status_code?: number | null
  error_kind?: string | null
  latency_ms?: number | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  total_tokens?: number | null
  origin_stage?: string | null
  origin_actor?: string | null
  task_type?: string | null
  api_key_id?: string | null
  provider_id?: number | null
  client_ip?: string | null
  request_preview?: string | null
  gw_session_id?: string | null
  agent_name?: string | null
  quality_flags?: string[] | null
  created_at: string
}

export interface ProbeLaneRequestsResponse {
  rows: ProbeLaneRequestRow[]
  total: number
  /**
   * 车道开关当前是否开启。
   *
   * ★ 必须用它区分「0 行」的两种含义：车道没开（探测在业务表里，这个端点
   *   必然为空）vs 车道开了但最近没有探测。前端据此决定是否回落去读历史行。
   */
  lane_enabled: boolean
  from?: string
  to?: string
}

export interface ProbeLaneMinuteStats {
  requests: number
  success_count: number
  failure_count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  latency_ms_sum: number
  avg_latency_ms: number
}

export interface ProbeLaneStatsResponse {
  stats: ProbeLaneMinuteStats
  lane_enabled: boolean
  from?: string
  to?: string
}

/**
 * getProbeLaneRequests 读探测车道的当前流水（session_turns_probe_hot）。
 *
 * @param limit 单次返回上限（后端上限 500）
 * @param tenantId 可选，精确匹配租户
 * @param stage 可选，按 origin_stage 精确匹配
 */
export async function getProbeLaneRequests(
  params: { limit?: number; tenant_id?: string; stage?: string } = {},
): Promise<ProbeLaneRequestsResponse> {
  const qs = new URLSearchParams()
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  if (params.stage) qs.set('stage', params.stage)
  const suffix = qs.toString()
  return req('GET', `/api/admin/probe/lane/requests${suffix ? `?${suffix}` : ''}`)
}

/** getProbeLaneStats 读探测分钟聚合（request_stats_probe_minute）。 */
export async function getProbeLaneStats(
  params: { from?: string; to?: string; tenant_id?: string } = {},
): Promise<ProbeLaneStatsResponse> {
  const qs = new URLSearchParams()
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  const suffix = qs.toString()
  return req('GET', `/api/admin/probe/lane/stats${suffix ? `?${suffix}` : ''}`)
}

/**
 * 探测流水在页面上的展示形态（与 RequestLogsView 的 RequestLog 对齐的子集）。
 */
export interface ProbeLaneDisplayRow {
  ts: string
  request_id: string
  success: boolean
  request_status: 'success' | 'failure'
  latency_ms: number | null
  error_kind: string | null
  credential_label: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
}

/**
 * 把探测车道的一行映射成页面展示形态。
 *
 * ★ 口径差异（都刻意）：
 *   · request_status：车道表没有该列（存的是 success / status_code）
 *     ⇒ 由 success 派生 success / failure；
 *   · credential_label：车道表的 credential_id 是 **text**（业务表也是 TEXT，
 *     见 migration 526/707）⇒ 直接当标签用（页面那个数字型 credential_id
 *     在这里没有等价物 ⇒ 置 null，由标签位承担展示）；
 *   · **不映射 cost_usd**：探测**不计费**（方案 §2 第 3 条）⇒ 展示层显示
 *     「—」。填 0 会被读成「花了 0 块」，那是假信息。
 */
export function probeLaneRowToDisplay(row: ProbeLaneRequestRow): ProbeLaneDisplayRow {
  return {
    ts: row.ts,
    request_id: row.request_id,
    success: row.success,
    request_status: row.success ? 'success' : 'failure',
    latency_ms: row.latency_ms ?? null,
    error_kind: row.error_kind ?? null,
    credential_label: row.credential_id ?? null,
    prompt_tokens: row.prompt_tokens ?? null,
    completion_tokens: row.completion_tokens ?? null,
  }
}

/**
 * 探测流水的来源判定。
 *
 * ★ 为什么要显式区分：
 *   探测流量有**两个来源** ——
 *   · 物理隔离车道（当前探测，车道开启时探测落 session_turns_probe_hot）；
 *   · 业务表上的历史探测行（车道**未**开启时探测仍在业务表里）。
 *   两者都是「探测流水」，但语义不同（当前 vs 历史）⇒ 页面上必须能分辨，
 *   否则运维会把「车道没开 ⇒ 只看得到历史行」读成「最近没有探测」。
 */
export type ProbeLaneSource = 'current' | 'historical' | 'none'

/**
 * resolveProbeLaneSource 判定当前展示的探测流水来自哪个来源。
 *
 * @param laneRows 车道端点返回的行数
 * @param laneEnabled 车道开关当前是否开启（端点回传的 lane_enabled）
 */
export function resolveProbeLaneSource(laneRows: number, laneEnabled: boolean): ProbeLaneSource {
  if (laneRows > 0) return 'current'
  // 车道开着但没有行 ⇒ 当前确实没有探测流量（不是「读不到」）。
  if (laneEnabled) return 'current'
  // 车道没开 ⇒ 车道端点必然为空，探测在业务表里 ⇒ 展示的是历史行。
  return 'historical'
}
