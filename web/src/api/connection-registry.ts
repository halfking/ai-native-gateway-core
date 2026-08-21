// connection-registry.ts — T9 mock stage 连接注册表 + 节点恢复时间线 API
//
// 三态字段：connected（已建连）/ connecting（建连中）/ disconnected（断开
// 或未上线）。节点恢复时间线（recover_at）由连接注册表的事件投影出来。
//
// Mock 阶段：直接 import 自带 mock 数据；将来切真后端只换实现，不动调用方。

export type ConnectionState = 'connected' | 'connecting' | 'disconnected'

export interface ConnectionRecord {
  connection_id: string
  /** 与 request-journeys 的 credential_id 对齐 */
  credential_id: number
  provider_id?: number
  provider_code?: string
  provider_name?: string
  /** 该连接主备模型（取 credential_model_bindings 投影） */
  raw_models: string[]
  state: ConnectionState
  /** 在途请求数 */
  in_flight: number
  /** 最近一次状态变更 unix ms */
  updated_at_ms: number
  /** 上次错误时间 + 错误码（断开/降级节点携带） */
  last_error_at_ms?: number
  last_error_kind?: string
  /** 节点最近一次恢复时间（用于恢复时间线） */
  recover_at?: string
  /** fp_disabled 投影（详见 liveStreamStore.LiveNodeStatus） */
  fp_disabled?: boolean
  manual_disabled?: boolean
}

export interface ConnectionRegistryResponse {
  capacity: number
  records: ConnectionRecord[]
  /** observation_status 与 request-journeys 对齐：complete | observation_degraded */
  observation_status: 'complete' | 'observation_degraded'
}

export interface NodeRecoveryEvent {
  credential_id: string
  /** event_type ∈ reconnected | recovered | failed | degraded | quarantined | probing */
  event_type: 'reconnected' | 'recovered' | 'failed' | 'degraded' | 'quarantined' | 'probing'
  occurred_at: string
  /** 与上一节点的耗时差（仅恢复事件有意义） */
  duration_ms?: number
  reason_code?: string
  /** 仅 quarantined / probing 携带 */
  note?: string
}

export interface NodeRecoveryTimelineResponse {
  credential_id: string
  observation_status: 'complete' | 'observation_degraded'
  events: NodeRecoveryEvent[]
}

/**
 * mock 数据：与后端 connection-registry 字段对齐但用前端快照。
 * 真实接入时此函数替换为 fetch 即可。
 */
export async function fetchConnectionRegistry(): Promise<ConnectionRegistryResponse> {
  const now = Date.now()
  return {
    capacity: 200,
    observation_status: 'complete',
    records: [
      {
        connection_id: 'conn-001',
        credential_id: 11,
        provider_id: 7,
        provider_code: 'openai',
        provider_name: 'OpenAI',
        raw_models: ['gpt-4o', 'gpt-4o-mini'],
        state: 'connected',
        in_flight: 3,
        updated_at_ms: now - 1_200,
      },
      {
        connection_id: 'conn-002',
        credential_id: 12,
        provider_id: 7,
        provider_code: 'openai',
        provider_name: 'OpenAI',
        raw_models: ['gpt-4o'],
        state: 'connecting',
        in_flight: 0,
        updated_at_ms: now - 4_500,
      },
      {
        connection_id: 'conn-003',
        credential_id: 21,
        provider_id: 9,
        provider_code: 'anthropic',
        provider_name: 'Anthropic',
        raw_models: ['claude-3-7-sonnet'],
        state: 'disconnected',
        in_flight: 0,
        updated_at_ms: now - 30_000,
        last_error_at_ms: now - 30_000,
        last_error_kind: 'upstream_timeout',
        recover_at: new Date(now + 60_000).toISOString(),
        fp_disabled: true,
      },
      {
        connection_id: 'conn-004',
        credential_id: 31,
        provider_id: 11,
        provider_code: 'qwen',
        provider_name: '阿里通义',
        raw_models: ['qwen-plus'],
        state: 'connected',
        in_flight: 1,
        updated_at_ms: now - 3_000,
        manual_disabled: false,
      },
    ],
  }
}

/**
 * mock 节点恢复时间线：单凭据在过去 24h 内的关键恢复事件。
 * 真实接入时按 credential_id 拉后端 /api/admin/connections/:id/recovery。
 */
export async function fetchNodeRecoveryTimeline(credentialId: string): Promise<NodeRecoveryTimelineResponse> {
  const now = Date.now()
  const events: NodeRecoveryEvent[] = [
    {
      credential_id: credentialId,
      event_type: 'failed',
      occurred_at: new Date(now - 24 * 3_600_000).toISOString(),
      reason_code: 'upstream_timeout',
    },
    {
      credential_id: credentialId,
      event_type: 'probing',
      occurred_at: new Date(now - 24 * 3_600_000 + 30_000).toISOString(),
      note: '健康度探针启动',
    },
    {
      credential_id: credentialId,
      event_type: 'recovered',
      occurred_at: new Date(now - 24 * 3_600_000 + 90_000).toISOString(),
      duration_ms: 90_000,
    },
    {
      credential_id: credentialId,
      event_type: 'degraded',
      occurred_at: new Date(now - 6 * 3_600_000).toISOString(),
      reason_code: 'rate_limit',
    },
    {
      credential_id: credentialId,
      event_type: 'reconnected',
      occurred_at: new Date(now - 6 * 3_600_000 + 15_000).toISOString(),
      duration_ms: 15_000,
    },
    {
      credential_id: credentialId,
      event_type: 'quarantined',
      occurred_at: new Date(now - 2 * 3_600_000).toISOString(),
      reason_code: 'circuit_open',
      note: '熔断器开启',
    },
  ]
  return {
    credential_id: credentialId,
    observation_status: 'complete',
    events,
  }
}