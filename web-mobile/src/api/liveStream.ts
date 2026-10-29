import { req, type RequestOptions } from './client'

/**
 * liveStream.ts — 实时流（live）移动端读面。
 *
 * GET /api/admin/live-stream         （SSE，本模块只声明契约与 REST 兜底）
 * GET /api/admin/live-stream/stats   （Hub 监控指标）
 *
 * 鉴权与作用域（admin/handler.go:1091-1094 注册是 `admin(...)` ⇒ **admin 档**，
 * tenant_admin 可用；`HandleLiveStream`（live_stream_sse.go:2524-2540）在 hub 内
 * 算租户作用域：IsTenantAdmin ⇒ 只收本租户帧，super_admin 收全局帧）。
 *
 * ## 为什么移动端是「REST 兜底 + 页面轮询」，不是桌面那套 SSE 单例
 *
 * 桌面 liveStreamStore.ts 用模块级 EventSource 单例（1923 行，slim tile 增量 +
 * 多维泳道 + 粒子池）。移动端 WebView 的 **EventSource 不能带 Authorization 头**，
 * 而壳内 native transport（CapacitorHttp）**不共享 cookie jar**
 * （nativeTransport.ts 头注 ⚠️：HttpRequestHandler.java 全文 0 处 CookieManager）
 * ⇒ 桌面「HttpOnly cookie 随 EventSource 同源携带」的前提在壳内不成立。
 *
 * 桌面 buildUrl（liveStreamStore.ts:503-507）刻意**不发** `?token=`（P1-7），
 * 靠的是网页 cookie；移动端同一端点要吃得靠 Bearer 头 ⇒ 只有 fetch 能带。
 * ⇒ 移动端形态：**`/live-stream/stats` 轮询 + `snapshot_refresh` 触发式重灌**
 *   （trigger-snapshot 是 POST-only TEMPORARY DEBUG 端点，页面不碰它）。
 *   观察延迟从「实时帧」退到「轮询间隔」——这是移动端观察页的刻意取舍，
 *   换来：零常驻连接（后台/锁屏不烧流量）、断线自愈是普通 fetch 重试、
 *   jsdom 可测（EventSource 在 Node/jsdom 不存在）。
 *
 * ## ★ 本族最要紧的契约（逐条对拍线上 initial_data，2026-10-29 实测）
 *
 * (1) ★★★★★ `stats` 是裸 map（`handleLiveStreamStats` 直接 Encode(map[string]interface{})），
 *     **没有** error 信封；hub 未装配时是 **503 + `{"error": "..."}`**（键是字符串 `error`）。
 * (2) ★★★★★ 计数键**全**无 omitempty（Go atomic/结构字段直填 map）⇒ 键恒在，0 是真 0。
 *     `last_activity` 是 RFC3339 字符串；`seconds_since_activity` 是 float 秒。
 * (3) ★★★★ 本页**只读**：`trigger-snapshot`（POST）与 SSE 长连接都不碰 ——
 *     前者自陈 TEMPORARY DEBUG，后者在壳内有 cookie 语义差异（见上）。
 */

/** GET /api/admin/live-stream/stats 的响应（Hub.Stats()，live_stream_sse.go:3229-3264）。 */
export interface LiveStreamStats {
  active_clients: number
  active_scope_subscriptions: number
  total_connections: number
  total_disconnections: number
  auth_failures: number
  broadcast_count: number
  broadcast_drops: number
  cached_snapshot_baseline_present: number
  cached_snapshot_baseline_absent: number
  cached_snapshot_empty_skips: number
  cached_snapshot_evictions: number
  cached_snapshot_entries: number
  last_activity: string
  seconds_since_activity: number
  lifecycle_actions_delivered: number
  lifecycle_action_scan_errors: number
  record_drops: number
}

/** 503（hub 未装配）的错误体：`{"error": "Live stream hub not initialized"}`。 */
export function liveStreamStatsNotWired(err: unknown): boolean {
  const status = (err as { status?: unknown } | null)?.status
  return status === 503
}

export async function fetchLiveStreamStats(options?: RequestOptions): Promise<LiveStreamStats> {
  return req<LiveStreamStats>('GET', '/api/admin/live-stream/stats', undefined, options)
}
