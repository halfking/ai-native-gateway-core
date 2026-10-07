import { req, type RequestOptions } from './client'

// requestJourney.ts — 运维排障线的**入口与纵深**：
//   GET /api/admin/request-journeys/queues        「现在有哪些请求在飞 / 卡在哪一站」
//   GET /api/admin/request-journeys/{request_id}  「这一条到底发生了什么」
//
// 为什么排在请求日志（requestLogs.ts）之后而不是替代它：两者互补。
//   requestLogs  = 已结束请求的**结果面**（谁、什么模型、多少 token、多少钱）
//   journey      = 单个请求的**过程面**（进来到离开，走了几跳、为什么改道）
// 排障时「这条失败了」是日志给的，「它为什么失败 / 中途换了哪个节点」只有 journey 给。
//
// 鉴权：两条都是 `requestJourneyWrapAdmin` = `AdminMiddleware`
//（cmd/gateway/main.go:7072-7073 → main_admin_wrappers.go:26-30）。
// ★ AdminMiddleware 只做认证（admin/auth.go:76），**不校验角色** ⇒ tenant_admin 可用。
//   唯一例外是 queues 的 `scope=all`，它自己判 super_admin（request_journey.go:41-44）。
//
// ⚠️★ 本模块最容易踩的三个坑，都不是「写错了会报错」而是「写错了会静默骗人」：
//
// (1) 降级字段叫 `observation_status`，字面值是 **`observation_degraded`**，
//     **不是** `degraded`（本仓库其它面用的是 `degraded`，见 usage / board）。
//     写 `if (r.degraded)` 永远为假 ⇒ 降级被显示成「真的没有请求」。
//
// (2) 详情端点**不是靠 404 表示"没有"**。request_journey.go:265-268 的条件是
//     `journey == nil && observation_status != observation_degraded` 才 404。
//     ⇒ 降级且无数据时返回的是 **200 + 没有 journey 字段**。
//     所以「找不到」和「看不到」是两种状态，UI 必须分开呈现。
//
// (3) 后端错误信封是 `{"error": {"message": "...", "type": "admin_error"}}`
//     （request_journey.go:287-291），**不是** `{"error": "..."}`。

/** 队列视图。后端对非法值直接 400（request_journey.go:226），所以类型收窄到字面量。 */
export type JourneyView = 'total' | 'models' | 'nodes'

/** 三态过滤。非法值 400（:152）；空 = 不过滤。 */
export type JourneyLifecycleState = 'pending' | 'in_flight' | 'completed'

/**
 * 观测面健康状态。
 *
 * ★ 关键：降级的字面值是 `observation_degraded`，**没有** `degraded` 这个词。
 * 另有 `observation_scope` 区分数据来源：shared_redis（跨实例共享）/
 * instance_local（仅本实例）—— 非 default 部署下 instance_local 意味着
 * **你看不到别的网关实例的请求**，这不是故障，但必须让用户知道。
 */
export type ObservationStatus = 'observation_degraded' | (string & {})
export type ObservationScope = 'shared_redis' | 'instance_local' | (string & {})

/** 全局视图（scope=all）下的元素类型：更瘦，没有 model / lifecycle 字段。 */
export interface IngressSnapshot {
  request_id: string
  gateway_instance_id: string
  protocol: string
  path_class: string
  arrived_at: string
  updated_at: string
  status: string
  error_kind?: string
  http_status?: number
  [k: string]: unknown
}

/** 自有租户视图下的元素类型。 */
export interface RequestSnapshot {
  request_id: string
  tenant_id?: string
  gateway_instance_id?: string
  requested_model?: string
  resolved_model?: string
  current_stage: string
  last_seq?: number
  last_event_type?: string
  attempt?: number
  outcome?: string
  error_kind?: string
  http_status?: number
  retry_reason?: string
  lifecycle_state?: string
  retry_at?: string
  switch_reason?: string
  node_health_status?: string
  observation_status?: string
  started_at?: string
  updated_at: string
  completed_at?: string
  [k: string]: unknown
}

/**
 * 队列响应（requestJourneyQueuesResponse, request_journey.go:115-123）。
 *
 * ⚠️ scope=all 时后端换成另一个结构（:130-138）：`scope` / `observation_scope` /
 *    `total_snapshot` **无 omitempty 恒存在**，且 `total_snapshot.requests[]`
 *    的元素变成 `IngressSnapshot`（**没有** model / lifecycle / outcome 字段）。
 *    ⇒ 两个形态合流到本类型，但 `requests` 的元素按联合类型处理（见 isIngress）。
 */
export interface JourneyQueuesResponse {
  view: JourneyView
  scope?: 'all'
  observation_status: ObservationStatus
  observation_scope?: ObservationScope
  total_snapshot?: {
    pending?: number
    in_flight?: number
    completed?: number
    /** ⚠️ 元素类型随 scope 变：见 isIngress() */
    requests?: Array<RequestSnapshot | IngressSnapshot>
    [k: string]: unknown
  }
  model_snapshots?: unknown[]
  node_snapshots?: unknown[]
  [k: string]: unknown
}

/**
 * 区分两种快照元素。`protocol`/`path_class` 只在 Ingress 上存在，
 * `current_stage` 只在 RequestSnapshot 上存在 —— 两者互斥且必居其一。
 */
export function isIngress(v: RequestSnapshot | IngressSnapshot): v is IngressSnapshot {
  return typeof (v as IngressSnapshot).protocol === 'string'
}

export interface JourneyQueuesParams {
  view?: JourneyView
  /** 只有 super_admin 能用；其他角色传了会 403（:41-44），所以移动端不传。 */
  scope?: 'all'
  lifecycle_state?: JourneyLifecycleState
  /** 仅 super_admin 生效；移动端不传。 */
  tenant?: string
}

export const JOURNEY_VIEWS: readonly JourneyView[] = ['total', 'models', 'nodes'] as const
export const JOURNEY_LIFECYCLE_STATES: readonly JourneyLifecycleState[] = [
  'pending',
  'in_flight',
  'completed',
] as const

export function fetchJourneyQueues(
  params?: JourneyQueuesParams,
  options?: RequestOptions,
): Promise<JourneyQueuesResponse> {
  const qs = new URLSearchParams()
  if (params?.view) qs.set('view', params.view)
  if (params?.scope === 'all') qs.set('scope', 'all')
  if (params?.lifecycle_state) qs.set('lifecycle_state', params.lifecycle_state)
  if (params?.tenant) qs.set('tenant', params.tenant)
  const s = qs.toString()
  return req<JourneyQueuesResponse>('GET', `/api/admin/request-journeys/queues${s ? '?' + s : ''}`, undefined, options)
}

/** 单跳的尝试信息。 */
export interface JourneyAttempt {
  attempt_id: string
  attempt_no: number
  model?: string
  provider_id?: number
  provider?: string
  credential_id?: number
  [k: string]: unknown
}

/** 链路事件。字段与 contract.go:375-403 对齐。 */
export interface JourneyEvent {
  tenant_id: string
  gateway_instance_id: string
  request_id: string
  seq: number
  event_type: string
  stage: string
  requested_model?: string
  resolved_model?: string
  model?: string
  provider_id?: number
  provider?: string
  credential_id?: number
  from_model?: string
  to_model?: string
  from_credential_id?: number
  to_credential_id?: number
  attempt?: JourneyAttempt
  outcome?: string
  error_kind?: string
  http_status?: number
  retry_reason?: string
  retry_at?: string
  switch_reason?: string
  node_health_status?: string
  observation_status: ObservationStatus
  occurred_at: string
  [k: string]: unknown
}

export interface Journey {
  tenant_id: string
  gateway_instance_id: string
  request_id: string
  observation_status: ObservationStatus
  started_at: string
  updated_at: string
  events: JourneyEvent[]
  [k: string]: unknown
}

/** 分歧类型（query.go:203-206, 211-221）。非空 = 两侧记录不一致，本身就是故障线索。 */
export type JourneyDivergence = 'content_conflict' | 'redis_divergence' | 'postgres_gap'

/**
 * 详情响应（DetailResult, domains/requestjourney/query.go:31-35）。
 *
 * ⚠️ `journey` 是 **omitempty**：降级且无数据时它是 undefined，而 HTTP 仍是 200。
 */
export interface JourneyDetailResponse {
  observation_status: ObservationStatus
  journey?: Journey
  divergence?: JourneyDivergence[]
  [k: string]: unknown
}

/** 观测面是否处于降级。这是本模块唯一正确的降级判据。 */
export function isDegraded(r: { observation_status?: string } | null | undefined): boolean {
  return r?.observation_status === 'observation_degraded'
}

/**
 * 详情端点的结果必须三态区分，不能二元化：
 *
 *   degraded=true,  journey=null  ⇒ 「观测面降级，看不到这一条」——不是「不存在」
 *   degraded=false, journey=null  ⇒ 后端已保证这种情况会 404，不会走到这里
 *   journey!=null                 ⇒ 正常；degraded 仍可能为 true（数据来自部分源）
 */
export type JourneyDetailState =
  | { kind: 'ok'; journey: Journey; degraded: boolean; divergence: JourneyDivergence[] }
  | { kind: 'observation_degraded' }
  | { kind: 'not_found' }

export function classifyJourneyDetail(r: JourneyDetailResponse | null | undefined): JourneyDetailState {
  if (!r || !r.journey) {
    // 走到这里且非降级 ⇒ 后端契约被打破（该 404 却给了 200）。不静默当「正常但空」，
    // 归入 not_found 让 UI 走「查不到」分支，同时 degraded 仍为 true 时优先报降级。
    return isDegraded(r) ? { kind: 'observation_degraded' } : { kind: 'not_found' }
  }
  return {
    kind: 'ok',
    journey: r.journey,
    degraded: isDegraded(r),
    divergence: Array.isArray(r.divergence) ? r.divergence : [],
  }
}

export function fetchJourneyDetail(
  requestId: string,
  params?: { tenant?: string },
  options?: RequestOptions,
): Promise<JourneyDetailResponse> {
  // 后端对空 / 含 / 的 request_id 返回 400（request_journey.go:257-260），必须转义。
  const id = encodeURIComponent(requestId ?? '')
  const qs = new URLSearchParams()
  if (params?.tenant) qs.set('tenant', params.tenant)
  const s = qs.toString()
  return req<JourneyDetailResponse>('GET', `/api/admin/request-journeys/${id}${s ? '?' + s : ''}`, undefined, options)
}
