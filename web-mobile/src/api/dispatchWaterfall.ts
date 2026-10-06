import { req, type RequestOptions } from './client'

// dispatchWaterfall.ts — 调度瀑布：时间到底花在哪一段。
//   GET /api/admin/dispatch/waterfall                      全量瀑布
//   GET /api/admin/dispatch/waterfall/request/{request_id} 单条瀑布
//   GET /api/admin/dispatch/queues                         队列深度
//
// 鉴权：三条都是 `wrapAdmin` = `AdminMiddleware`（cmd/gateway/main.go:7228-7230
// → main_admin_wrappers.go:26-30），只做认证 ⇒ tenant_admin 可用。
//
// 为什么这条线值得单独一页：journey 回答「发生了什么事件」，waterfall 回答
// 「每一段各花了多久」。排障时最常见的现象是「不报错但很慢」，只有瀑布能把它
// 拆成 排队等待 / 路由决策 / 取凭据 / 上游首字 / 流式传输 五段。
//
// ⚠️★ 降级语义与其他面**完全不同**，不要照抄 `degraded` 判据：
//   本端点**没有** degraded 布尔字段。降级由两个字段表达：
//     · wired: false  → 队列投影根本没接上（main_dispatch_projection.go:66-75），
//       此时 bottleneck_diagnosis.message = "dispatch queue projection not wired"
//     · source: 'none' → 没有可用数据源（waterfall_db.go:90-96, 245-247）
//   source 另有 'memory' | 'memory+db' | 'db'。
//   ⇒ 这两个信号必须**原样透出给用户**：不是「没数据」，是「这个观测面没接上」。
//
// ⚠️ 单条瀑布的 404 走 `http.Error` ⇒ **text/plain 纯文本**，不是 JSON
//（main_dispatch.go:172-176）。req() 读 body 失败时会退回 statusText，
//   所以调用方拿到的 ApiError.detail 可能是一句英文散文，UI 不要试图 JSON.parse 它。

/** 瀑布中的一条请求。各时间戳都是 **RFC3339 字符串**，不是 epoch 毫秒。 */
export interface WaterfallRequest {
  request_id: string
  tenant_id?: string
  session_id?: string
  model?: string
  credential_id?: number
  result: string
  vendor?: string
  attempts?: WaterfallAttempt[]
  arrived_at?: string
  total_enqueued_at?: string
  total_dequeued_at?: string
  model_enqueued_at?: string
  model_dequeued_at?: string
  // ⚠️ 缩写是 cred_ 不是 credential_（domains/dispatch/waterfall.go:14-46）
  cred_enqueued_at?: string
  cred_dequeued_at?: string
  forward_start_at?: string
  response_start_at?: string
  response_end_at?: string
  waiting_in_total_ms: number
  waiting_in_model_ms: number
  waiting_in_node_ms: number
  routing_ms: number
  acquire_ms: number
  upstream_latency_ms: number
  streaming_duration_ms: number
  queue_wait_ms: number
  total_ms: number
  [k: string]: unknown
}

export interface WaterfallAttempt {
  attempt_id: string
  attempt_no: number
  model?: string
  provider_id?: number
  credential_id: number
  vendor?: string
  started_at?: string
  first_byte_at?: string
  ended_at?: string
  outcome?: string
  error_kind?: string
  [k: string]: unknown
}

export interface BottleneckDiagnosis {
  bottleneck: string
  message: string
  suggestion?: string
  [k: string]: unknown
}

export type DispatchSource = 'memory' | 'memory+db' | 'db' | 'none' | (string & {})

export interface WaterfallResponse {
  requests: WaterfallRequest[]
  time_range?: { start: string; end: string }
  bottleneck_diagnosis: BottleneckDiagnosis
  enabled: boolean
  /** false = 投影未接上 ⇒ 降级。必须透出，不能显示成「没有请求」。 */
  wired: boolean
  source?: DispatchSource
  [k: string]: unknown
}

export interface WaterfallByRequestResponse {
  request: WaterfallRequest
  source?: DispatchSource
  [k: string]: unknown
}

export interface WaterfallParams {
  /**
   * 后端注释写「default 50, max 200」（main_dispatch.go:124-125），
   * ★ 但实现（:130-134）**完全没有 clamp** —— 注释是假的。
   *   所以移动端自己封顶，别依赖后端兜底。
   */
  limit?: number
  model?: string
  credential_id?: number
}

export const WATERFALL_DEFAULT_LIMIT = 50
export const WATERFALL_CLIENT_MAX_LIMIT = 200

export function fetchWaterfall(
  params?: WaterfallParams,
  options?: RequestOptions,
): Promise<WaterfallResponse> {
  const qs = new URLSearchParams()
  if (params?.limit != null) {
    const n = Math.trunc(params.limit)
    if (Number.isFinite(n) && n > 0) qs.set('limit', String(Math.min(n, WATERFALL_CLIENT_MAX_LIMIT)))
  }
  if (params?.model) qs.set('model', params.model)
  if (params?.credential_id != null) qs.set('credential_id', String(params.credential_id))
  const s = qs.toString()
  return req<WaterfallResponse>('GET', `/api/admin/dispatch/waterfall${s ? '?' + s : ''}`, undefined, options)
}

export function fetchWaterfallByRequest(
  requestId: string,
  options?: RequestOptions,
): Promise<WaterfallByRequestResponse> {
  const id = encodeURIComponent(requestId ?? '')
  return req<WaterfallByRequestResponse>(
    'GET',
    `/api/admin/dispatch/waterfall/request/${id}`,
    undefined,
    options,
  )
}

/** 队列车道视图。⚠️ 字段是 `credential`（不是 credential_id）—— queue_metrics_collector.go:63-70 */
export interface LaneView {
  model?: string
  credential?: string
  mode?: string
  depth: number
  limit?: number
  full?: boolean
  [k: string]: unknown
}

export interface DispatchQueuesResponse {
  enabled: boolean
  wired: boolean
  models: LaneView[]
  credentials: LaneView[]
  [k: string]: unknown
}

export function fetchDispatchQueues(options?: RequestOptions): Promise<DispatchQueuesResponse> {
  return req<DispatchQueuesResponse>('GET', '/api/admin/dispatch/queues', undefined, options)
}

/**
 * 瀑布是否处于「不可观测」态。
 *
 * ★ 本面**没有** degraded 字段，所以判据只能由 wired / source 推出：
 *   · wired === false → 投影没接上，什么都测不到
 *   · source === 'none' → 没有数据源
 * 这两种都**不是**「当前没有请求」，UI 必须显示成观测面不可用。
 */
export function isWaterfallUnavailable(
  r: Pick<WaterfallResponse, 'wired' | 'source'> | null | undefined,
): boolean {
  if (!r) return false
  return r.wired === false || r.source === 'none'
}

/**
 * 泳道名。后端字段叫 `credential` 不是 `credential_id`，两处都兜一下。
 * 返回 null 表示这行没有可用标识 —— UI 应显示占位符而不是 'undefined'。
 */
export function laneLabel(l: LaneView): string | null {
  if (typeof l.credential === 'string' && l.credential !== '') return l.credential
  const alt = l['credential_label']
  if (typeof alt === 'string' && alt !== '') return alt
  if (typeof l.model === 'string' && l.model !== '') return l.model
  return null
}

/**
 * 两点之间的毫秒差。
 *
 * ⚠️ 后端时间戳是 RFC3339 **字符串**，`b - a` 在 JS 里对字符串是 NaN。
 * 而且**缺一端时不能当 0** —— 「没记录到这一跳」和「这一跳耗时 0ms」是两件事，
 * 用 0 冒充会直接得出错误的瓶颈结论。
 */
export function spanMs(from?: string | null, to?: string | null): number | null {
  if (!from || !to) return null
  const a = Date.parse(from)
  const b = Date.parse(to)
  if (!Number.isFinite(a) || !Number.isFinite(b)) return null
  return b - a
}
