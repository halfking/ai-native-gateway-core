import { req, type RequestOptions } from './client'

// probeModelHealth.ts — 探测面的两个「模型级」端点。
//   GET /api/admin/probe/dashboard     每个模型 × 凭据的健康分布
//   GET /api/admin/probe/node-tasks    节点探测队列（queue-tasks 的姊妹端点）
//
// 鉴权：两条都在 `RegisterProbeDashboardRoutes(mux, wrapAdmin)`
// （admin/probe_dashboard.go:1900-1909）⇒ **admin 档**，不设 requiresRole。
//
// 它与已上移的页面分工：
//   /probe            = 具体哪个**任务**在跑、供应商直连多快
//   /node-tasks（本轮）= 节点探测队列（哪些凭据在被探测、排到第几次）
//   /heatmap          = 凭据 × 模型的热力图
//   本页 dashboard    = **模型级**汇总：哪个模型整体在坏
//
// ─────────────────────────────────────────────────────────────────────────
// ⚠️ 四个后端语义：
//
// (1) ★★★ **`nullFloat64` / `nullInt` 把 SQL NULL 变成 0**，
//     而 JSON 字段是**普通 `float64` / `int`**（无指针、无 omitempty）
//     —— probe_dashboard.go:2336-2348。
//     ⇒ `healthy_percentage: 0` 有两种完全不同的含义：
//       「0% 的凭据健康」（真值）/「SQL 算出来是 NULL」（没数据）。
//     **客户端拿不到区分依据** —— 信息在后端就丢了。
//     ★ 唯一可推导的判据：`total_credentials === 0` 时，
//       所有百分比/均值**必然**是「没数据」而不是「0」——
//       「0 个里的 0%」在数学上不成立。
//       见 `derivedStatsAbsent`。
//
// (2) ★★ **`real_success_rate_24h` 是 `*float64` + omitempty**
//     （`ModelHealthSummary:64`）⇒ **字段缺失 = 近 24h 没有真实请求**，
//     不是 0% 成功率。与 §11.47 的 `success_rate_last_1h` 同款三态。
//
// (3) ★ `limit` 越界**静默回落**，且两个姊妹端点**默认值不同**：
//     · `queue-tasks`  : 默认 **100**（:1318）
//     · `node-tasks`   : 默认 **120**（:1445）
//     两者都是 `if n > 0 && n <= 200` 才采纳，否则**保持默认、不报错**。
//     ⇒ 这是本仓库的**第六种**越界语义（400 / 400 for enum / 静默 clamp /
//       静默回落 / 静默 clamp 到上限 / **静默回落且默认值按端点不同**）。
//     ⇒ 前端**必须自己夹**，且夹的上限是 200 而不是各自默认值。
//
// (4) ★ `model` 过滤是 `ILIKE '%q%'`（dashboard :675-679），
//     是**子串匹配** ⇒ 传 `gpt-4` 会同时命中 `gpt-4o` 和 `gpt-4o-mini`。
//     命中数与「我以为筛的是哪一个」可能不一致。

// ── dashboard ─────────────────────────────────────────────────────────────

export type OverallHealth = 'critical' | 'warning' | 'degraded' | 'healthy' | 'unknown' | (string & {})

export interface ModelHealthSummary {
  provider_model_id: number
  raw_model_name: string
  outbound_model_name: string
  protocol: string
  provider_name: string

  // 状态分布
  total_credentials: number
  healthy_count: number
  suspicious_count: number
  failing_count: number
  probing_count: number
  /** ★ plain float64：NULL 已被后端压成 0。见文件头 (1)。 */
  healthy_percentage: number
  /** ★ 同上 */
  failing_percentage: number

  // 优先级分布
  urgent_count: number
  suspicious_priority_count: number
  failing_priority_count: number
  watchdog_count: number

  // 健康指标（★ 同样是 plain float64，NULL→0）
  avg_success_rate_7d: number
  avg_verification_hours: number
  avg_consecutive_successes: number

  // 近 24h 真实请求（★ TotalRealSuccess/Failure24h 也是 plain int，NULL→0）
  total_real_success_24h: number
  total_real_failure_24h: number
  /** ★ omitempty 指针：字段缺失 = 近 24h 没有真实请求，不是 0% 成功率。 */
  real_success_rate_24h?: number | null

  last_verified_at?: string | null
  last_real_request_at?: string | null
  next_probe_at?: string | null

  critical_nodes: number
  pending_probes_5min: number
  /** critical / warning / degraded / healthy / unknown */
  overall_health: OverallHealth
}

export interface ProbeDashboardResponse {
  models: ModelHealthSummary[] | null
  total?: number | null
}

/**
 * ★★ 派生统计量（百分比 / 均值）是否**必然**是「没数据」。
 *
 * 唯一可推导的情形：`total_credentials === 0`。
 * 「0 个凭据里的 0% 健康率」在数学上不成立 ⇒ 该 0 是 NULL 被压平的产物。
 *
 * ⚠️ **不是**完备判据：`total_credentials > 0` 时若 SQL 仍返回 NULL，
 *   客户端**无法**识别（本文件头 (1)）——那种情况只能靠后端修。
 *   所以 UI 在 `total_credentials > 0` 时仍要给一个「可能为 0」的余地说明。
 */
export function derivedStatsAbsent(m: Pick<ModelHealthSummary, 'total_credentials'> | null | undefined): boolean {
  return !m || m.total_credentials === 0
}

/** 真实请求近 24h 三态。缺失 ≠ 0%。见文件头 (2)。 */
export type RealSuccessRate24h = { kind: 'no_requests' } | { kind: 'real'; value: number }

export function realSuccessRate24hOf(m: ModelHealthSummary | null | undefined): RealSuccessRate24h {
  const v = m?.real_success_rate_24h
  if (v === undefined || v === null || !Number.isFinite(v)) return { kind: 'no_requests' }
  return { kind: 'real', value: v }
}

/** 近 24h 真实请求总量。0 有歧义（NULL→0，见文件头 (1)）⇒ 返回 null 让调用方自己判。 */
export function realRequests24hOf(m: ModelHealthSummary | null | undefined): number | null {
  if (!m) return null
  const s = m.total_real_success_24h ?? 0
  const f = m.total_real_failure_24h ?? 0
  const t = s + f
  // 两者都是 0 时无法区分「没有请求」与「NULL 被压平」
  return t === 0 ? null : t
}

const HEALTH_TONE: Record<string, 'danger' | 'warning' | 'muted' | 'success'> = {
  critical: 'danger',
  warning: 'warning',
  degraded: 'warning',
  healthy: 'success',
  unknown: 'muted',
}

/**
 * `overall_health` → 配色。★ 词表外一律 `muted`：
 * 给一个看不懂的状态打 `success`，等于把「不知道」显示成「健康」。
 * 同 `proposalStatusTone` 的纪律。
 */
export function healthTone(h: OverallHealth | null | undefined): 'danger' | 'warning' | 'muted' | 'success' {
  return HEALTH_TONE[(h ?? '').toLowerCase()] ?? 'muted'
}

/** i18n 键。词表外落到 `unknown`。 */
export function healthKeyOf(h: OverallHealth | null | undefined): string {
  const v = (h ?? '').toLowerCase()
  return ['critical', 'warning', 'degraded', 'healthy', 'unknown'].includes(v)
    ? 'probeModel.health.' + v
    : 'probeModel.health.unknown'
}

export interface ProbeDashboardParams {
  /** 子串匹配（ILIKE），不是精确。传 'gpt-4' 会同时命中 gpt-4o / gpt-4o-mini。 */
  model?: string
}

export function fetchProbeDashboard(
  params: ProbeDashboardParams = {},
  options?: RequestOptions,
): Promise<ProbeDashboardResponse> {
  const qs = new URLSearchParams()
  if (params.model) qs.set('model', params.model)
  const s = qs.toString()
  return req<ProbeDashboardResponse>('GET', `/api/admin/probe/dashboard${s ? '?' + s : ''}`, undefined, options)
}

// ── node-tasks ────────────────────────────────────────────────────────────

/** 状态与 `bg/node_probe.go` 的状态机一致：running（被租约持有）/ paused（达上限）/ pending。 */
export type NodeTaskStatus = 'running' | 'paused' | 'pending' | (string & {})

export interface NodeProbeTaskRow {
  credential_id: number
  provider_id: number
  provider_name: string
  provider_code: string
  raw_model: string
  /** COALESCE(standardized, canonical, raw) —— 三级回落，可能等于 raw_model。 */
  standardized_name: string
  status: NodeTaskStatus
  attempt: number
  consecutive_failures: number
  next_retry_at?: string | null
  last_direct_ok?: boolean | null
  last_gateway_ok?: boolean | null
  last_err_code?: string | null
  last_latency_ms?: number | null
  paused: boolean
  updated_at?: string | null
  /** 后端恒为 "node_probe"；与 queue-tasks 的 "integrity" 对称。 */
  source: string
}

export interface NodeTasksResponse {
  tasks: NodeProbeTaskRow[] | null
  total?: number | null
}

/** `node-tasks` 的后端默认 limit（:1445 `limit := 120`）。 */
export const NODE_TASKS_DEFAULT_LIMIT = 120
/** 两个姊妹端点共用的上限；越界**静默回落**不是 400。 */
export const PROBE_TASKS_MAX_LIMIT = 200
/** `queue-tasks` 的后端默认 limit（:1318 `limit := 100`）。 */
export const QUEUE_TASKS_DEFAULT_LIMIT = 100

export interface NodeTasksParams {
  limit?: number
}

export function fetchProbeNodeTasks(params: NodeTasksParams = {}, options?: RequestOptions): Promise<NodeTasksResponse> {
  const qs = new URLSearchParams()
  if (params.limit != null) {
    const n = Math.trunc(params.limit)
    // 越界后端静默回落（不是 400）⇒ 前端自己夹到 1..200，别让它悄悄改我们的数
    if (Number.isFinite(n) && n > 0) qs.set('limit', String(Math.min(n, PROBE_TASKS_MAX_LIMIT)))
  }
  const s = qs.toString()
  return req<NodeTasksResponse>('GET', `/api/admin/probe/node-tasks${s ? '?' + s : ''}`, undefined, options)
}

const NODE_STATUS_TONE: Record<string, 'danger' | 'warning' | 'muted' | 'success'> = {
  paused: 'danger',
  running: 'warning',
  pending: 'muted',
}

export function nodeStatusTone(s: NodeTaskStatus | null | undefined): 'danger' | 'warning' | 'muted' | 'success' {
  return NODE_STATUS_TONE[(s ?? '').toLowerCase()] ?? 'muted'
}

export function nodeStatusKeyOf(s: NodeTaskStatus | null | undefined): string {
  const v = (s ?? '').toLowerCase()
  return ['running', 'paused', 'pending'].includes(v) ? 'probeModel.nodeStatus.' + v : 'probeModel.nodeStatus.pending'
}

/**
 * ★ `last_latency_ms` 是 `*int` + omitempty ⇒ 缺失 = 没有延时记录。
 * `0` 也是一个真实值（探到了但延时 0ms）—— 两者**必须**分开。
 * 同 autoRouteInsights 的 `latencyOf` 纪律（§11.31）。
 */
export function latencyOf(t: NodeProbeTaskRow | null | undefined): number | null {
  const v = t?.last_latency_ms
  return v === undefined || v === null || !Number.isFinite(v) ? null : v
}

/**
 * ★ 队列里**真正需要人处理**的行。
 *
 * 判据：`paused`（已达重试上限，不会自己好了）—— 这一类**不会自愈**，
 * 其余（running / pending）都会自己往前走。
 * ⇒ 「等一等就好」和「等再久也不会好」必须能被区分开。
 */
export function needsManualAction(t: NodeProbeTaskRow | null | undefined): boolean {
  return t?.paused === true
}
