import { req, type RequestOptions } from './client'

// nodes.ts — /api/credentials/monitor-summary（凭据/节点健康，形状对齐
// web/src/api/credential-monitor.ts，字段裁剪到移动端渲染面）。

export interface CredentialModelStatus {
  raw_model_name: string
  canonical_name?: string | null
  offer_available: boolean
  binding_available: boolean
  /** 'broken_confirmed' | 'healthy_confirmed' | 'recovering' | 'unknown' */
  probe_state: string
  probe_last_status?: string | null
  recent_success_rate?: number | null
  p95_latency_ms?: number | null
  offer_unavailable_reason?: string | null
}

export interface CredentialMonitorSummary {
  id: number
  provider_id: number
  provider_name: string
  label: string
  status: string
  effective_state?: string | null
  effective_reason?: string | null
  availability_state: string
  health_status: string
  quota_state: string
  effective_concurrency: number
  concurrency_limit: number | null
  manual_disabled: boolean
  consecutive_failures: number
  state_reason_code: string | null
  state_reason_detail: string | null
  health_checked_at: string | null
  total_requests: number
  model_total: number
  model_available: number
  broken_model_count: number
  models?: CredentialModelStatus[]
  aggregated_success_rate?: number | null
}

/** `/api/credentials/monitor-summary` 的响应信封。 */
export interface MonitorSummaryResponse {
  credentials: CredentialMonitorSummary[]
  count: number
  meta?: {
    cache_hit?: boolean
    generated_at?: string
    expires_at?: string
    server_duration_ms?: number
    ttl_seconds?: number
  }
}

/**
 * 从信封里取出 credentials 数组。
 *
 * ⚠️ 2026-10-06 修正：本文件原先声明 `Promise<CredentialMonitorSummary[]>` 并把
 * **整包**当作数组返回。后端 handleMonitorSummary（admin/credential_monitor.go）
 * 实际返回的是 `{credentials, count, meta}` 这个对象 —— 实测：
 *   顶层 dict，键 ['count','credentials','meta']，credentials 为 65 项 list。
 * 于是 NodesView 的 `cache.filter(...)` 是在对象上调用 ⇒ TypeError ⇒
 * 页面进错误态。也就是说 nodes 页只要接口成功就必然渲染失败，
 * 只是被后端 15s 超时 500 挡在前面一直没暴露。
 * （与 /api/auth/me 的双形态解包是同一类缺陷：一个信封当成裸载荷。）
 */
export function unwrapMonitorSummary(resp: MonitorSummaryResponse): CredentialMonitorSummary[] {
  if (resp && typeof resp === 'object' && Array.isArray((resp as MonitorSummaryResponse).credentials)) {
    return resp.credentials
  }
  // 形状不符**抛错**而不是返 []：契约违约应当被看见（视图会走错误态），
  // 静默返回空数组会让「接口没数据」和「解包失败」长得一模一样。
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`monitor-summary 响应形状不符：期望 {credentials:[…]}，实得 ${actual}`)
}

export function fetchMonitorSummary(options?: RequestOptions): Promise<CredentialMonitorSummary[]> {
  // 列表用途固定走 mode=core —— 与桌面 web 的 useCredentialLabels.ts:150 同一选择。
  //
  // 2026-10-06 在 245 实测（每档间隔 32s 避开 30s 缓存）：
  //   不带 mode 的**全量列表**  15.18s / 15.19s，两次都正好撞满 15s 超时 → 500
  //   mode=core（同样的全量）   0.90s 冷查，server_duration_ms=417
  // 桌面的 mode:'detail' 只在**带 credential_id**（单凭据钻取）时用
  // （CredentialDetailDrawer / NodeDetailOtherModelsPanel），那种范围下才便宜。
  // 移动端这个页面是全量列表，套 detail 模式就是病态用法。
  return req<MonitorSummaryResponse>('GET', '/api/credentials/monitor-summary?mode=core', undefined, options).then(
    unwrapMonitorSummary,
  )
}
