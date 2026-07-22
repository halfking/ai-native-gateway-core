// api/provider-quality.ts — 供应商质量画像 API

import { req } from './_core'

// ============================================================================
// Types
// ============================================================================

export interface QualityProfile {
  provider_id: number
  provider_name?: string
  model_name: string | null

  // L1: 核心可用性
  success_rate_5m: number
  success_rate_1h: number
  success_rate_24h: number
  error_rate_5xx_5m: number
  error_rate_5xx_1h: number
  error_rate_5xx_24h: number
  error_rate_4xx_5m: number
  error_rate_timeout_5m: number
  availability_24h: number

  // L2: 性能
  latency_p50_5m: number
  latency_p50_1h: number
  latency_p50_24h: number
  latency_p95_5m: number
  latency_p95_1h: number
  latency_p95_24h: number
  latency_p99_5m: number
  latency_p99_24h: number
  ttft_p95_5m: number
  ttft_p95_1h: number
  throughput_tokens_per_sec_1h: number

  // L3: 可信度（反欺诈）
  trustworthiness_score: number
  trustworthiness_grade: string
  ttft_deviation_rate: number
  token_ratio_deviation_rate: number
  quality_score: number
  probe_success_rate: number
  anomaly_count_24h: number

  // L4: 稳定性
  volatility_24h: number
  mttr_seconds_24h: number
  error_diversity_score_24h: number
  consecutive_failures: number
  last_failure_at: string | null
  last_recovery_at: string | null

  // L5: 成本效益
  cost_per_1k_tokens: number
  quota_usage_percentage: number

  // 综合评分
  availability_score: number
  performance_score: number
  stability_score: number
  cost_efficiency_score: number
  quality_score_overall: number
  quality_grade: string

  // 统计
  total_requests_5m: number
  total_requests_1h: number
  total_requests_24h: number

  // 元数据
  updated_at: string
  created_at: string
}

export interface QualityTrend {
  timestamp: string
  quality_score: number
  success_rate: number
  latency_p95: number
  error_rate_5xx: number
  trustworthiness_score: number
}

export interface HealthEvent {
  id: number
  provider_id: number
  model_name: string | null
  event_type: string
  severity: string
  title: string
  description: string
  trigger_metric: string
  trigger_value: number
  threshold_value: number
  affected_requests_count: number
  auto_action: string
  manual_action: string | null
  acknowledged_by: string | null
  acknowledged_at: string | null
  resolved_at: string | null
  notified: boolean
  created_at: string
}

export interface ErrorDetail {
  id: number
  provider_id: number
  model_name: string | null
  error_type: string
  error_code: string
  error_message: string
  occurrences: number
  first_seen_at: string
  last_seen_at: string
  acknowledged: boolean
  resolved: boolean
  resolution_note: string | null
}

export interface QualityConfig {
  provider_id: number

  // 告警阈值
  alert_error_rate_5xx_p0: number
  alert_error_rate_5xx_p1: number
  alert_availability_p0: number
  alert_latency_p99_p0: number
  alert_latency_p95_p1: number

  // 质量目标
  target_success_rate: number
  target_latency_p95: number
  target_latency_p99: number

  // 评分权重
  weight_availability: number
  weight_performance: number
  weight_stability: number
  weight_cost_efficiency: number
  weight_trustworthiness: number

  // 熔断配置
  circuit_breaker_enabled: boolean
  circuit_breaker_threshold: number
  circuit_breaker_timeout_seconds: number

  // 降权配置
  downgrade_on_score_below: number
  downgrade_weight_multiplier: number
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * 获取所有供应商的质量画像列表
 */
export async function getProviderQualityProfiles(): Promise<QualityProfile[]> {
  const resp = await req<{ profiles: QualityProfile[] }>('GET', '/api/providers/quality')
  return resp.profiles || []
}

/**
 * 获取单个供应商的质量画像
 */
export async function getProviderQualityProfile(providerId: number): Promise<QualityProfile> {
  return req<QualityProfile>('GET', `/api/providers/${providerId}/quality`)
}

/**
 * 获取供应商质量趋势（最近N小时）
 */
export async function getProviderQualityTrend(
  providerId: number,
  hours: number = 24,
): Promise<QualityTrend[]> {
  const resp = await req<{ trend: QualityTrend[] }>(
    'GET',
    `/api/providers/${providerId}/quality/trend?hours=${hours}`,
  )
  return resp.trend || []
}

/**
 * 获取供应商错误详情
 */
export async function getProviderErrors(providerId: number): Promise<ErrorDetail[]> {
  const resp = await req<{ errors: ErrorDetail[] }>(
    'GET',
    `/api/providers/${providerId}/errors`,
  )
  return resp.errors || []
}

/**
 * 获取供应商健康事件
 */
export async function getProviderHealthEvents(providerId: number): Promise<HealthEvent[]> {
  const resp = await req<{ events: HealthEvent[] }>(
    'GET',
    `/api/providers/${providerId}/events`,
  )
  return resp.events || []
}

/**
 * 确认健康事件
 */
export async function acknowledgeHealthEvent(
  providerId: number,
  eventId: number,
): Promise<void> {
  await req('POST', `/api/providers/${providerId}/events/${eventId}/ack`)
}

/**
 * 获取供应商质量配置
 */
export async function getProviderQualityConfig(providerId: number): Promise<QualityConfig> {
  return req<QualityConfig>('GET', `/api/providers/${providerId}/quality/config`)
}

/**
 * 更新供应商质量配置
 */
export async function updateProviderQualityConfig(
  providerId: number,
  config: Partial<QualityConfig>,
): Promise<QualityConfig> {
  return req<QualityConfig>('PUT', `/api/providers/${providerId}/quality/config`, config)
}

/**
 * 手动触发质量画像重新计算
 */
export async function recalculateProviderQuality(providerId: number): Promise<void> {
  await req('POST', `/api/providers/${providerId}/quality/recalculate`)
}
