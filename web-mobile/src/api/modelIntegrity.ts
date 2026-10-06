import { req, type RequestOptions } from './client'

// modelIntegrity.ts — /api/admin/model-integrity/*（模型完整性异常）。
//
// **鉴权：整段是 superAdmin 档**（admin/handler.go:924-925 两处注册都包 h.superAdmin），
// tenant_admin 一律 403。⇒ 移动端按 role 分档渲染，非 super_admin 不显示入口。
//
// 选它的理由：这是「模型名对不上 / 参数漂移 / 指纹变化」这类问题的集中面 ——
// 它们表现为「请求失败或结果诡异」，但**不落在节点健康上**（凭据探测是好的）。
// 移动端此前完全没有这个面，排查这类问题只能开电脑。

/** 与后端 admin/model_integrity.go:17-37 的 ModelIntegrityRecord 逐字段对齐。 */
export interface ModelIntegrityRecord {
  id: number
  /** ⚠️ JSON tag 是 detected_at（:19）；SQL 里那一列叫 ts（:149）——两者同一字段，勿混。 */
  detected_at: string
  request_id?: string
  provider_id?: number
  provider_code?: string
  credential_id?: number
  client_model?: string
  outbound_model?: string
  raw_model_name?: string
  anomaly_type: string
  severity: 'low' | 'medium' | 'high' | 'critical'
  expected_value?: string
  actual_value?: string
  sample?: string
  context?: unknown
  resolved: boolean
  resolved_at?: string
  resolution_notes?: string
  tenant_id?: string
}

export interface ModelIntegrityEventsResponse {
  events: ModelIntegrityRecord[]
  /** 是**命中总数**（COUNT(*)），不是本页条数 —— 连续加载要靠它算总页。 */
  count: number
  limit: number
  offset: number
}

export interface ModelIntegritySummary {
  hour: string
  provider_code?: string
  client_model?: string
  anomaly_type: string
  severity: string
  anomaly_count: number
  affected_requests: number
  resolved_count: number
}

export interface ModelIntegritySummaryResponse {
  summaries: ModelIntegritySummary[]
  count: number
  hours: number
}

/** 后端默认 100、硬上限 500（model_integrity.go:94-100，超了静默 clamp）。 */
export const MODEL_INTEGRITY_MAX_LIMIT = 500

export interface ModelIntegrityParams {
  limit?: number
  offset?: number
  provider?: string
  model?: string
  anomaly_type?: string
  severity?: string
  /** 只看未处置的 —— 移动端默认开启（运维默认关心「还没处理的」）。 */
  unresolved_only?: boolean
}

export function fetchModelIntegrityEvents(
  params?: ModelIntegrityParams,
  options?: RequestOptions,
): Promise<ModelIntegrityEventsResponse> {
  const qs = new URLSearchParams()
  if (params?.limit != null) {
    const n = Math.trunc(params.limit)
    // 与 nodeAudit 同款守卫：<1 后端回落默认 100（:95-97，**不** 400），
    // >500 静默 clamp（:98-100）。不发非法值即可，无需拒绝。
    if (Number.isFinite(n) && n >= 1) qs.set('limit', String(Math.min(n, MODEL_INTEGRITY_MAX_LIMIT)))
  }
  if (params?.offset != null) {
    const n = Math.trunc(params.offset)
    if (Number.isFinite(n) && n >= 0) qs.set('offset', String(n))
  }
  if (params?.provider) qs.set('provider', params.provider)
  if (params?.model) qs.set('model', params.model)
  if (params?.anomaly_type) qs.set('anomaly_type', params.anomaly_type)
  if (params?.severity) qs.set('severity', params.severity)
  if (params?.unresolved_only) qs.set('unresolved_only', 'true')
  const q = qs.toString()
  return req<ModelIntegrityEventsResponse>(
    'GET',
    `/api/admin/model-integrity/events${q ? '?' + q : ''}`,
    undefined,
    options,
  )
}

export function fetchModelIntegritySummary(
  hours = 24,
  options?: RequestOptions,
): Promise<ModelIntegritySummaryResponse> {
  const qs = new URLSearchParams({ hours: String(Math.max(1, Math.trunc(hours) || 24)) })
  return req<ModelIntegritySummaryResponse>('GET', `/api/admin/model-integrity/summary?${qs}`, undefined, options)
}

/**
 * `POST /api/admin/model-integrity/events/{id}/resolve`
 * handler admin/model_integrity.go:271-310（superAdmin 档）。
 *
 * ⚠️ body 解析用的是 `readJSON`（:284）**并显式忽略返回错误**（`_ = readJSON(...)`），
 * 所以 resolution_notes **不是**必填、解析失败也不会 400。
 * 这与同批的 set-manual-disabled（严格 400）不同 —— 移动端可以不强制填备注，
 * 但**仍**给输入框，因为「为什么处置」是复盘时唯一有用的信息。
 *
 * id 非法（解析失败或 <=0）→ 400 "invalid integrity id"（:276-279）。
 */
export interface ResolveResult {
  ok?: boolean
  id?: number
  message?: string
  resolved?: boolean
  [k: string]: unknown
}

export function resolveModelIntegrityEvent(
  id: number,
  resolutionNotes: string,
  options?: RequestOptions,
): Promise<ResolveResult> {
  if (!Number.isInteger(id) || id <= 0) {
    return Promise.reject(new Error(`invalid integrity id: ${id}`))
  }
  return req<ResolveResult>(
    'POST',
    `/api/admin/model-integrity/events/${id}/resolve`,
    { resolution_notes: resolutionNotes },
    options,
  )
}

/** 严重度 → 语义色。未知值回落 muted，不臆造优先级。 */
export function severityTone(s: string): 'danger' | 'warning' | 'muted' {
  if (s === 'critical' || s === 'high') return 'danger'
  if (s === 'medium') return 'warning'
  return 'muted'
}
