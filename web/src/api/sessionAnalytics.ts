import { req } from './_core'
import { ref } from 'vue'

// sessionAnalytics.ts — 会话全景分析 API client.
// 对应后端 /api/admin/session-analytics/* 与 /api/admin/session-clusters/*

export interface AnalyticsSessionSummary {
  gw_session_id: string
  tenant_id: string
  task_id?: string
  session_status?: string
  first_request_at: string
  last_request_at: string
  duration_seconds: number
  request_count: number
  success_count: number
  error_count: number
  total_cost_usd: number
  input_cost_usd: number
  output_cost_usd: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_tokens: number
  avg_latency_ms: number
  min_latency_ms?: number
  max_latency_ms?: number
  models_used: string[]
  primary_model?: string
  model_switch_count: number
  title?: string
  summary?: string
  key_topics: string[]
  user_intent?: string
  quality_score?: number
  compliance_status: string
  compliance_issues_count: number
  prompt_injection_detected: boolean
  pii_detected: boolean
  toxic_output_detected: boolean
  work_types: string[]
  providers: string[]
  client_models: string[]
  last_summarized_at?: string
  created_at: string
  updated_at: string
}

export interface RequestEvent {
  request_id: string
  created_at: string
  success: boolean
  client_model: string
  upstream_model: string
  prompt_tokens: number
  completion_tokens: number
  cost_usd: number
  latency_ms: number
  work_type?: string
  provider?: string
  compression_strategy?: string
  cache_read_tokens?: number
  error_message?: string
  request_preview?: string
  response_preview?: string
}

export interface SessionStepSummary {
  step_index: number
  request_id: string
  request_summary?: string
  response_summary?: string
  is_llm_generated: boolean
  tool_calls_summary?: string
}

export interface SessionTag {
  id: number
  tag_key: string
  tag_value: string
  tag_source: string
  confidence: number
  created_by?: string
  created_at: string
}

export interface SessionOptimizationSugg {
  id: number
  category: string
  severity: string
  title: string
  description?: string
  potential_savings_tokens: number
  potential_savings_cost: number
  applied: boolean
  dismissed: boolean
  created_at: string
}

export interface SessionClusterMembership {
  cluster_id: string
  label?: string
  score: number
}

export interface CacheSavings {
  cache_read_tokens: number
  cache_write_tokens: number
  estimated_saved_usd: number
}

export interface CompressionSavings {
  compressed_requests: number
  outbound_token_est: number
  estimated_tokens_saved: number
  estimated_saved_usd: number
}

export interface SessionAnalysis {
  cost_breakdown: { input_cost: number; output_cost: number; total_cost: number; by_model: Record<string, number>; by_provider: Record<string, number> }
  token_distribution: { prompt_tokens: number; completion_tokens: number; total_tokens: number; by_model: Record<string, number> }
  cache_savings?: CacheSavings
  compression_savings?: CompressionSavings
}

export interface SessionPanorama {
  summary: AnalyticsSessionSummary
  timeline: RequestEvent[]
  step_summaries: SessionStepSummary[]
  tags: SessionTag[]
  suggestions: SessionOptimizationSugg[]
  cluster?: SessionClusterMembership
  analysis: SessionAnalysis
  module_enabled: boolean
}

export interface SessionClusterItem {
  cluster_id: string
  tenant_id: string
  coarse_key?: string
  label?: string
  topic_path: string[]
  member_count: number
  avg_cost_usd: number
  avg_quality_score?: number
  updated_at: string
}

/** 列出会话（分页+筛选） */
export function listSessions(params: Record<string, any>) {
  const qs = buildQuery(params)
  return req<{ sessions: AnalyticsSessionSummary[]; page: number; page_size: number; total: number }>(
    'GET', '/api/admin/session-analytics' + qs,
  )
}

/** 会话统计（今日） */
export function getSessionStats() {
  return req<any>('GET', '/api/admin/session-analytics/stats')
}

/** 会话详情 */
export function getSessionDetail(gwSessionId: string) {
  return req<any>('GET', `/api/admin/session-analytics/${gwSessionId}`)
}

/** 会话全景图（一次返回全部聚合信息） */
export function getSessionPanorama(gwSessionId: string) {
  return req<SessionPanorama>('GET', `/api/admin/session-analytics/${gwSessionId}/panorama`)
}

/** 会话标签 */
export function getSessionTags(gwSessionId: string) {
  return req<{ tags: SessionTag[] }>('GET', `/api/admin/session-analytics/${gwSessionId}/tags`)
}

/** 手动打标签 */
export function addSessionTag(gwSessionId: string, tagKey: string, tagValue: string) {
  return req('POST', `/api/admin/session-analytics/${gwSessionId}/tags`, { tag_key: tagKey, tag_value: tagValue })
}

/** 删除标签 */
export function deleteSessionTag(gwSessionId: string, tagId: number) {
  return req('DELETE', `/api/admin/session-analytics/${gwSessionId}/tags/${tagId}`)
}

/** 优化建议列表 */
export function getSessionSuggestions(gwSessionId: string) {
  return req<{ suggestions: SessionOptimizationSugg[] }>('GET', `/api/admin/session-analytics/${gwSessionId}/suggestions`)
}

/** 采纳优化建议 */
export function applySuggestion(gwSessionId: string, suggestionId: number) {
  return req('POST', `/api/admin/session-analytics/${gwSessionId}/suggestions/${suggestionId}/apply`)
}

/** 聚类列表 */
export function listClusters(params: Record<string, any>) {
  const qs = buildQuery(params)
  return req<{ clusters: SessionClusterItem[]; page: number; page_size: number; total: number }>(
    'GET', '/api/admin/session-clusters' + qs,
  )
}

/** 聚类详情 */
export function getClusterDetail(clusterId: string) {
  return req<any>('GET', `/api/admin/session-clusters/${clusterId}`)
}

/** 手动触发聚类 */
export function runClustering(lookbackHours = 168) {
  return req<{ status: string; clusters_built: number }>(
    'POST', '/api/admin/session-clusters/run', { lookback_hours: lookbackHours },
  )
}

// ── 模块启用检测（前端据此决定是否显示全景页） ──────────────────────

const _moduleEnabled = ref<boolean | null>(null)

/**
 * 检测 session_analytics 模块是否启用。
 * 结果缓存（单次会话内）；返回 null 表示尚未加载。
 */
export async function detectSessionAnalyticsModule(): Promise<boolean> {
  if (_moduleEnabled.value !== null) return _moduleEnabled.value
  try {
    const r = await req<{ items: any[] }>('GET', '/api/admin/modules')
    const mod = r.items.find((m: any) => m.key === 'session_analytics')
    _moduleEnabled.value = !!(mod && mod.enabled)
  } catch {
    _moduleEnabled.value = false
  }
  return _moduleEnabled.value
}

/** 响应式的模块启用状态（供模板直接使用）。 */
export function useSessionAnalyticsEnabled() {
  return _moduleEnabled
}

// ── helpers ───────────────────────────────────────────────────────────

/** buildQuery 把对象转为 ?k=v&k2=v2（跳过空值）。 */
function buildQuery(params: Record<string, any>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    sp.append(k, String(v))
  }
  const s = sp.toString()
  return s ? '?' + s : ''
}
