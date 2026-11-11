// routeTrace.ts — 动态路由追踪数据类型
//
// 与后端 admin/route_trace.go 的 wire 格式保持一致。用于实时展示请求的
// 路由决策过程，包括模型选择、凭据选择、重试与 fallback 等。
//
// 2026-11-11: 初始实现，对标 magpie 的路由事件系统。

/**
 * 路由阶段枚举
 * 
 * 路由过程按以下顺序推进：
 * started → selecting → attempting → responding → done/failed
 * 
 * fallback 阶段表示在失败后重试下一个凭据
 */
export type RoutePhase = 
  | 'started'    // 请求接收，路由开始
  | 'selecting'  // 正在选择凭据/供应商
  | 'attempting' // 正在向上游发送请求
  | 'responding' // 正在接收响应内容
  | 'fallback'   // 失败后重试下一个凭据
  | 'done'       // 请求成功完成
  | 'failed'     // 所有尝试均失败

/**
 * 路由尝试记录
 * 
 * 记录单次上游请求的完整信息。当发生 fallback 时，
 * 一个请求会包含多个尝试记录。
 */
export interface RouteAttempt {
  sequence: number              // 尝试序号，从 1 开始
  credential_id: number         // 使用的凭据 ID
  credential_label?: string     // 凭据的用户友好标签
  provider: string              // 供应商代码（如 "openai"）
  model: string                 // 模型名称
  
  start_time: string            // ISO 8601 格式开始时间
  end_time?: string             // ISO 8601 格式结束时间
  
  status?: number               // HTTP 状态码
  success: boolean              // 是否成功
  latency_ms?: number           // 延迟（毫秒）
  ttft_ms?: number              // 首 token 时间（毫秒）
  
  error_kind?: string           // 错误分类（如 "rate_limit"）
  error_message?: string        // 脱敏后的错误消息
  is_fallback: boolean          // 是否为重试尝试
  
  // Token 使用量
  prompt_tokens?: number
  completion_tokens?: number
}

/**
 * 完整的路由追踪记录
 * 
 * 包含一个请求从开始到完成的完整路由决策链。
 * 随着路由过程推进，通过 SSE 增量更新。
 */
export interface RouteTrace {
  request_id: string            // 请求 ID
  tenant_id: number             // 租户 ID（用于隔离）
  start_time: string            // ISO 8601 格式开始时间
  phase: RoutePhase             // 当前路由阶段
  
  // 模型选择
  client_model: string          // 客户端请求的模型
  canonical_model?: string      // 解析后的标准模型名
  canonical_id?: number         // 标准模型 ID
  
  // 当前路由目标
  provider?: string             // 供应商代码
  credential_id?: number        // 凭据 ID
  credential_label?: string     // 凭据标签
  
  // 尝试历史
  attempts: RouteAttempt[]      // 所有尝试记录
  current_attempt: number       // 当前尝试序号（1-indexed）
  
  // 最终结果
  status: string                // "success" | "failed" | "in_progress"
  final_status?: number         // 最终 HTTP 状态码
  latency_ms?: number           // 总延迟（毫秒）
  ttft_ms?: number              // 首 token 时间（毫秒）
  tokens_used?: number          // 总 token 使用量
  error?: string                // 脱敏后的错误消息
  error_kind?: string           // 错误分类
  
  // 元数据
  updated_at: string            // ISO 8601 格式更新时间
  version: number               // 版本号（每次更新递增）
}

/**
 * 路由追踪 SSE 事件
 * 
 * LiveStreamEnvelope 的 route_trace 变体
 */
export interface RouteTraceEnvelope {
  type: 'route_trace'
  ts: string                    // ISO 8601 时间戳
  route_trace: RouteTrace
}

/**
 * 路由统计摘要
 * 
 * 用于列表视图的简化展示
 */
export interface RouteTraceSummary {
  request_id: string
  phase: RoutePhase
  provider?: string
  attempt_count: number
  is_fallback: boolean          // 是否发生过 fallback
  status: string
  latency_ms?: number
}

/**
 * 从完整路由追踪提取摘要信息
 */
export function extractRouteTraceSummary(trace: RouteTrace): RouteTraceSummary {
  return {
    request_id: trace.request_id,
    phase: trace.phase,
    provider: trace.provider,
    attempt_count: trace.attempts.length,
    is_fallback: trace.attempts.some(a => a.is_fallback),
    status: trace.status,
    latency_ms: trace.latency_ms,
  }
}

/**
 * 路由阶段的用户友好描述
 */
export const ROUTE_PHASE_LABELS: Record<RoutePhase, string> = {
  started: '开始路由',
  selecting: '选择凭据',
  attempting: '请求中',
  responding: '接收响应',
  fallback: '切换凭据',
  done: '完成',
  failed: '失败',
}

/**
 * 路由阶段的颜色主题
 */
export const ROUTE_PHASE_COLORS: Record<RoutePhase, string> = {
  started: 'blue',
  selecting: 'cyan',
  attempting: 'orange',
  responding: 'green',
  fallback: 'yellow',
  done: 'success',
  failed: 'error',
}

/**
 * 判断路由是否已完成（成功或失败）
 */
export function isRouteTraceTerminal(trace: RouteTrace): boolean {
  return trace.phase === 'done' || trace.phase === 'failed'
}

/**
 * 判断路由是否正在进行中
 */
export function isRouteTraceActive(trace: RouteTrace): boolean {
  return !isRouteTraceTerminal(trace)
}

/**
 * 计算路由的成功率（基于尝试次数）
 */
export function calculateRouteSuccessRate(trace: RouteTrace): number | null {
  if (trace.attempts.length === 0) {
    return null
  }
  const successfulAttempts = trace.attempts.filter(a => a.success).length
  return (successfulAttempts / trace.attempts.length) * 100
}

/**
 * 格式化延迟时间
 */
export function formatLatency(ms: number | undefined): string {
  if (ms === undefined) {
    return '-'
  }
  if (ms < 1000) {
    return `${ms}ms`
  }
  return `${(ms / 1000).toFixed(2)}s`
}

/**
 * 格式化 Token 数量
 */
export function formatTokens(tokens: number | undefined): string {
  if (tokens === undefined) {
    return '-'
  }
  if (tokens < 1000) {
    return `${tokens}`
  }
  return `${(tokens / 1000).toFixed(1)}k`
}
