import { req, type RequestOptions } from './client'

// probeOps.ts — 探测面：现在有什么在探测、谁探测得慢。
//   GET /api/admin/probe/queue-tasks       探测队列当前任务
//   GET /api/admin/probe/provider-latency  各供应商最近一次成功直连探测的延时
//
// 鉴权：两条都 `adminWrap` = AdminMiddleware（admin/probe_dashboard.go:1903/1905）
// 只做认证不判角色 ⇒ tenant_admin 可用。
//
// 它回答的是凭据线答不了的一问：「**凭据的当前状态是怎么得出来的**」。
// NodesView 展示的是 node_probe_state 的**结论**（健康/故障/可疑），
// 这一页展示的是**过程**：谁排在队列里、试到第几次、下次什么时候重试、
// 供应商直连一次要多久。
// 排障时「这一条为什么被判成可疑」经常只能从这里看出来 ——
// 结论页不告诉你它是第 3 次重试还是第 1 次就成功了。
//
// ⚠️ 两条端点的取数语义**各不相同**，不要互相照抄：
//
// (1) queue-tasks 的 `limit` 越界是**静默保持默认 100**
//     （probe_dashboard.go 的 `if n > 0 && n <= 200` 不成立时就不赋值），
//     不是 400、也不是 clamp 到边界。⇒ 前端自己夹到 1..200。
//     ★ 这是本仓库第**四种**越界语义了（400 / 静默 clamp / 静默回落 / 保持默认），
//       每加一个端点都要重新读，不能沿用上一个的记忆。
//
// (2) provider-latency **没有任何查询参数**，且写死两个口径：
//     · 只统计 `direct_ok = TRUE AND direct_latency_ms > 0`
//     · 只看 `now() - 1 hour` 窗口内，`LIMIT 500`
//     ⇒ 「某个供应商没出现在列表里」可能是「1 小时内没有成功的直连探测」，
//       而不是「这个供应商不存在」。UI 必须这么说，否则会被读成「该供应商已消失」。

/** queue-tasks 的有效范围（越界静默保持 100）。 */
export const PROBE_TASKS_MAX_LIMIT = 200
export const PROBE_TASKS_DEFAULT_LIMIT = 100

export interface ProbeQueueTask {
  id: number
  credential_id: number
  provider_id: number
  provider_name: string
  provider_code: string
  raw_model: string
  standardized_name: string
  /** 队列状态。后端未在此结构体上定义词表，按字面量透出。 */
  status: string
  attempt: number
  priority: number
  reason_code: string
  /** sql.NullTime + omitempty ⇒ 可能是「键不存在」或 null */
  next_run_at?: string
  result_latency_ms?: number
  result_http_status?: number
  updated_at?: string
  source?: string
  [k: string]: unknown
}

export interface ProbeQueueTasksResponse {
  tasks: ProbeQueueTask[] | null
  total: number
}

export interface ProviderLatencyEntry {
  provider_id: number
  provider_name: string
  /** ★ 这是 swimlane lane.id 的实际取值，不是展示名。 */
  provider_code: string
  latency_ms: number
  probed_at: string
}

export interface ProviderLatencyResponse {
  entries: ProviderLatencyEntry[] | null
  total: number
}

export function fetchProbeQueueTasks(
  params?: { limit?: number },
  options?: RequestOptions,
): Promise<ProbeQueueTasksResponse> {
  const qs = new URLSearchParams()
  if (params?.limit != null) {
    const n = Math.trunc(params.limit)
    if (Number.isFinite(n) && n > 0) {
      qs.set('limit', String(Math.min(n, PROBE_TASKS_MAX_LIMIT)))
    }
  }
  const s = qs.toString()
  return req<ProbeQueueTasksResponse>('GET', `/api/admin/probe/queue-tasks${s ? '?' + s : ''}`, undefined, options)
}

/**
 * ★ 本端点不接受任何参数（无 limit / 无时间窗 / 无过滤）。
 *   写成一个不接受参数的函数，是为了让「想传参」的冲动在类型层面就被挡掉，
 *   而不是发出一个被后端忽略、让用户以为过滤生效了的查询串。
 */
export function fetchProviderLatency(options?: RequestOptions): Promise<ProviderLatencyResponse> {
  return req<ProviderLatencyResponse>('GET', '/api/admin/probe/provider-latency', undefined, options)
}

/**
 * 任务状态 → 配色。
 *
 * ⚠️ 词表外的状态一律 `muted`，**不给 success**。
 * 后端这个结构体上没有定义状态词表（对比 heatmap 的 node_status 是有词表的），
 * 所以值可能是我们没见过的。默认给 success 会把「看不懂的状态」显示成正常。
 *
 * ★ 这里刻意**不用** `Record<string, tone>` 查表：查表天然是
 * `TONE[status] ?? 'success'`（缺省成功），而这里要的缺省是 muted。
 *   写成 switch 之后「缺省是什么」是显式的一行，不靠默认值隐含。
 */
export function taskStatusTone(status: string | null | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  const s = (status ?? '').toLowerCase()
  if (s === 'done' || s === 'success' || s === 'succeeded' || s === 'ok') return 'success'
  if (s === 'failed' || s === 'error' || s === 'failure') return 'danger'
  if (s === 'running' || s === 'retry' || s === 'retrying' || s === 'in_flight') return 'warning'
  if (s === 'pending' || s === 'queued' || s === 'scheduled' || s === 'waiting') return 'muted'
  return 'muted'
}

/** 队列任务的展示名：优先标准化名，其次原始名，最后凭据 id。 */
export function taskLabel(t: Pick<ProbeQueueTask, 'standardized_name' | 'raw_model' | 'credential_id'>): string {
  if (t.standardized_name && t.standardized_name.trim() !== '') return t.standardized_name
  if (t.raw_model && t.raw_model.trim() !== '') return t.raw_model
  return `#${t.credential_id}`
}
