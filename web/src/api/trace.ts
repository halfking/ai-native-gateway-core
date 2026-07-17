// trace.ts — 2026-07-17
// 请求链路追踪 API 客户端 (用于 RequestTracePanel)。
//
// 数据源:
//   1) Redis (request:trace:{id}, 进行中或刚完成 10min 内)
//   2) PostgreSQL (request_logs.trace_events JSONB, 已 flush 的历史)
//
// 后端见 admin/request_trace.go
import { req } from './_core'

// TraceEvent 与后端 internal/trace.TraceEvent 字段一一对应。
// 仅在这里定义前端需要的展示字段; 后端原始 JSON 字段较多,
// 用 unknown/undefined 表示前端暂不关心。
export interface TraceSnapshot {
  captured_at?: string
  candidates?: unknown[]
  routing_state?: string
  credential_mode?: string
  node_probe_state?: Record<string, unknown>
  concurrency_slot?: {
    credential_id: number
    in_use: number
    max_slots: number
    blocked: boolean
    reason?: string
  }
  circuit_state?: string
  failure_hint?: string
  extra?: Record<string, unknown>
}

export interface TraceEvent {
  seq: number
  stage: string
  stage_name?: string
  module: string
  timestamp: string
  duration_ms: number
  status: 'success' | 'failed' | 'timeout' | 'skipped'
  details?: Record<string, unknown>
  error?: string
  snapshot?: TraceSnapshot | null
}

export interface RequestTrace {
  request_id: string
  events: TraceEvent[]
  final_status: 'success' | 'failed' | 'timeout' | '' // empty string for in-progress
  failed_at_stage?: string
  total_duration_ms: number
  source: 'redis' | 'postgres'
}

// getRequestTrace 拉取完整链路。
//
// 抛出 Error 当:
//   - 网络层失败 (fetch reject)
//   - HTTP 401/403/5xx
//   - 404 表示 Redis miss + Postgres miss (前端可重试或显示 "trace 暂不可用")
export async function getRequestTrace(requestId: string): Promise<RequestTrace> {
  if (!requestId) throw new Error('requestId is required')
  return req<RequestTrace>('GET', `/api/admin/requests/${encodeURIComponent(requestId)}/trace`)
}

// buildAIPromptResponse 是 POST /ai-prompt 响应。
export interface AIPromptResponse {
  prompt: string
  tokens_estimate: number
  event_count: number
}

// buildAIPrompt 生成可粘贴给 LLM 的故障分析提示词。
//
// userQuestion 留空时后端会塞一段默认占位文本。
// 响应中的 prompt 已包含基本信息 / 链路 / 原始错误 / 用户问题占位。
export async function buildAIPrompt(
  requestId: string,
  userQuestion?: string,
  lang?: string,
): Promise<AIPromptResponse> {
  if (!requestId) throw new Error('requestId is required')
  const body: Record<string, string> = {}
  if (userQuestion) body.user_question = userQuestion
  if (lang) body.lang_override = lang
  return req<AIPromptResponse>(
    'POST',
    `/api/admin/requests/${encodeURIComponent(requestId)}/ai-prompt`,
    body,
  )
}
