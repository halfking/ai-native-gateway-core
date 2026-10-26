import { req } from './_core'

// requestActions.ts — 批 203（2026-10-17）
// 🟡 P2 requestActions 孤儿接前端：request_id 维度的会话动作时间线
//
// 端点：GET /api/admin/requests/{id}/actions（admin 中间件，handler.go:1078）
// 后端：从 Redis LIST llmgw:live:actions 过滤 ev.RequestID == requestID 的 actions
// 17 跳 line 1064：「只查 Redis LIST（liveactions.RedisKey LRange）」by-design 平台共享
//
// 真 orphan：API helper 0 命中 + 视图 0 命中
// flattenActionEvent（admin/live_stream_lifecycle.go:96）把 detail 字段展平到顶层

export interface RequestAction {
  request_id: string
  seq: number
  action: string
  ts: string
  model?: string
  credential_id?: number
  error_kind?: string
  retry_seq?: number
  retry?: boolean
  /** flattenActionEvent 把 detail 子键展平到顶层，允许其它未知字段 */
  [key: string]: unknown
}

export interface RequestActionsResponse {
  request_id: string
  actions: RequestAction[]
  count: number
}

/**
 * 拉取指定 request_id 的会话动作时间线（按 ts + seq 升序）
 * 端点：GET /api/admin/requests/{id}/actions
 * 后端 Redis 缓存 2s scan timeout（admin/request_actions.go:30）
 */
export async function getRequestActions(requestId: string): Promise<RequestActionsResponse> {
  return req<RequestActionsResponse>('GET', `/api/admin/requests/${encodeURIComponent(requestId)}/actions`)
}
