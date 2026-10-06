import { req, type RequestOptions } from './client'

// probeHealth.ts — 探测线的两个「双轨」端点。
//   GET /api/admin/probe/system-health    系统健康（unified + legacy 双轨）
//   GET /api/admin/probe/queue-snapshot   队列快照（unified + legacy 双轨）
//
// 鉴权：两条都在 `RegisterProbeDashboardRoutes(mux, wrapAdmin)`
// （admin/probe_dashboard.go:1900-1909，注册点 cmd/gateway/main.go:7298）
// ⇒ **admin 档**，tenant_admin 可用，导航不设 requiresRole。
// 与已上移的 /probe（queue-tasks + provider-latency）同一条线。
//
// ─────────────────────────────────────────────────────────────────────────
// ⚠️⚠️ 这两个端点是本仓库里**最容易读错**的形状。三条陷阱，每一条
//    都会让页面显示一个「看着合理但完全不是当前状况」的数字：
//
// (1) ★★★ `system-health` 的**顶层字段全是 legacy 的**。
//     handler 把 `ProbeSystemHealth` marshal 之后**摊平到顶层**
//     （probe_dashboard.go:958-972 的 `legacyPayload`），再挂上
//     `unified` / `legacy` / `legacy_mode_safe`。
//     ⇒ 顶层 `total_nodes` / `healthy_nodes` / `urgent_queue_size`
//       **不是**新数据，是 `model_probe_state` 的遗留视图。
//     而新数据在 `unified` 下，且**字段名完全不同**：
//       顶层 legacy      vs  unified 新
//       total_nodes      vs  total_credentials   ← 不是一个东西
//       ready_probes     vs  queue_ready
//       current_probing  vs  queue_in_flight
//     写 `resp.total_nodes` 读出来的是 legacy 数字。**本模块不导出顶层字段**。
//
// (2) ★★★ `queue-snapshot` 的顶层 `queues` / `total` **也是 legacy 的**。
//     handler 先查 `v_probe_queue_snapshot`（= `model_probe_state`）得到
//     `queues`，再把它同时放进顶层和 `legacy` 键下
//     （probe_dashboard.go:775-830）。新数据只在 `unified` 里。
//     ★ 而这个 legacy 视图里躺着一批**历史积压**——handler 自己的注释写着
//       「the 572-row historical backlog (AGENT C handoff 2026-08-18)
//       lives in the legacy view and is unaffected by the active queue」
//       （probe_dashboard.go:764-767）。
//     ⇒ 拿 `resp.total` 当「当前探测队列积压」会显示 572，
//       而实际活动队列可能只有个位数。**这是本文件存在的最大理由。**
//
// (3) ★★ `system-health` 的 legacy 查询是**软失败**，
//     而 `queue-snapshot` 的是**硬失败** —— 两条线不对称。
//     · queue-snapshot（:780-786）：legacy 视图查询出错 ⇒ 直接 500。
//     · system-health（:888-895）：legacy 视图查询出错 ⇒ 只 `slog.Warn`，
//       `legacyHealth` 保持**零值**，响应仍是 200。
//     ★ 而且 handleProbeSystemHealth 的注释写着
//       「Surface the error in the response so the dashboard can warn the operator」
//       —— **代码没有这么做**（grep `legacyErr` 只有 slog.Warn 一处使用）。
//     ⇒ 所以 `legacy.total_nodes === 0` 有两种含义：
//       「真的一个节点都没有」/「legacy 视图没加载上来」。
//       **客户端无法区分**，也不能替它编一个。
//       见 `legacySectionMayBeUnloaded`。
//
// (4) **`legacy_mode_safe: false` 是后端给的显式警告**（两处都有）。
//     它表示「legacy 视图不再权威，别拿它当当前状况」。
//     ⇒ UI 必须把它透出来并据此给 legacy 区块加「历史遗留」标注，
//       而不是把 legacy 数字和 unified 数字并排放在同一个统计条里。
//
// (5) **新数据里也有需要注意的语义**：
//     · `queue_finished` / `queue_completed` / `queue_failed` / `queue_expired`
//       都是「**近 2 小时**」窗口，不是全量（结构体注释写明）。
//     · `node_due`（next_retry_at <= now）与 `node_unclaimable`
//       （in_flight_until 已过期但没人持有租约）是两种不同的「积压」，
//       第二个是**租约泄漏**的信号，性质比第一个严重。
//     · `stale_leases` 同理：活跃租约超过心跳窗口未续约。
//     · `success_rate_last_1h` 是 `omitempty` 的 `*float64`
//       ⇒ **字段缺失 = 那一小时没有运行记录**，不是 0% 成功率。

// ── system-health ─────────────────────────────────────────────────────────

/** 新源：unified（credential_probe_queue + node_probe_state + node_probe_runs + URSM）。 */
export interface UnifiedProbeSystemHealth {
  total_credentials: number
  credentials_with_ursm: number
  credentials_no_ursm: number
  ursm_key_count: number

  // 近 2h 窗口，不是全量
  queue_pending: number
  queue_in_flight: number
  queue_completed: number
  queue_failed: number
  queue_expired: number
  queue_total: number

  node_total: number
  node_healthy: number
  node_failing: number
  node_paused: number
  node_running: number
  node_due_now: number
  node_leased: number

  runs_last_1h: number
  runs_success_1h: number
  runs_failed_1h: number
  runs_last_at?: string | null
  /** ★ omitempty 指针：字段**不存在** = 近 1h 没有运行记录，不是 0% 成功率。 */
  success_rate_last_1h?: number | null

  /** 「伪成功」凭据数：probe 记录 direct_ok 但 URSM tenant key 已消失。 */
  pseudo_success_count: number

  legacy: TotalLegacySystemHealth

  snapshot_at: string
}

/** 遗留视图（model_probe_state）的结构化副本。 */
export interface TotalLegacySystemHealth {
  total_nodes: number
  healthy_nodes: number
  failing_nodes: number
  suspicious_nodes: number
  probing_nodes: number
  urgent_queue_size: number
  ready_probes: number
  current_probing: number
  last_probe_at?: string | null
  /** 常量字符串 "model_probe_state" —— 后端刻意给前端用来标注区块。 */
  legacy_source: string
  /** ★ false = legacy 已不权威。UI 必须据此标注。 */
  legacy_mode_safe: boolean
}

/**
 * `system-health` 响应。
 *
 * ★ **刻意不声明任何顶层统计字段。**
 *   它们在响应里确实存在（后端为了兼容老客户端摊平的），但全是 legacy 数字
 *   —— 见文件头 (1)。本接口只暴露 `unified` / `legacy` / `legacy_mode_safe`
 *   三个显式键，让「读错源」这件事在类型层面就做不到。
 *   `[k: string]: unknown` 的索引签名只用于判别形状，不提供具名字段。
 */
export interface SystemHealthResponse {
  unified?: UnifiedProbeSystemHealth | null
  legacy?: TotalLegacySystemHealth | null
  legacy_mode_safe?: boolean | null
  snapshot_at?: string | null
  [k: string]: unknown
}

export function fetchProbeSystemHealth(options?: RequestOptions): Promise<SystemHealthResponse> {
  return req<SystemHealthResponse>('GET', '/api/admin/probe/system-health', undefined, options)
}

/**
 * ★ legacy 区块是否**可能没加载上来**。
 *
 * 见文件头 (3)：system-health 的 legacy 查询软失败，只 slog.Warn，
 * 响应里**没有任何字段**能区分「legacy 视图失败」与「legacy 视图真的是全 0」。
 * 后端注释声称会 surface，实际没有。
 *
 * ⇒ 返回 true 时 UI 必须说「可能未加载」，**不能**把这些 0 读成
 *   「一个节点都没有」。
 *
 * ⚠️ 这不是精确判据 —— 全 0 且加载成功也会命中。方向刻意偏向
 *   「宁可说可能没加载，也不要把未知说成零」。
 */
export function legacySectionMayBeUnloaded(legacy: Partial<TotalLegacySystemHealth> | null | undefined): boolean {
  if (!legacy) return true
  const numeric = [
    legacy.total_nodes,
    legacy.healthy_nodes,
    legacy.failing_nodes,
    legacy.suspicious_nodes,
    legacy.probing_nodes,
    legacy.urgent_queue_size,
    legacy.ready_probes,
    legacy.current_probing,
  ]
  return numeric.every((v) => typeof v === 'number' && v === 0)
}

// ── queue-snapshot ────────────────────────────────────────────────────────

/** 新源：unified 队列聚合（credential_probe_queue + node_probe_state）。 */
export interface UnifiedProbeQueueStats {
  queue_ready: number
  queue_running: number
  /** 近 2h */
  queue_finished: number
  /** 活跃租约持有的行数 */
  queue_claims: number

  node_pending: number
  node_running: number
  node_paused: number
  /** next_retry_at <= now */
  node_due: number
  /** ★ in_flight_until 已过期但没有 worker 持有租约 = **租约泄漏**信号 */
  node_unclaimable: number

  /** 活跃租约超过心跳窗口（5 分钟）未续约 */
  stale_leases: number

  last_run_at?: string | null
  /** 兼容字段：历史上是跨优先级的聚合，已改路由到新源。 */
  queue_size: number

  snapshot_at: string
}

/** 遗留视图的按优先级/状态明细行（来自 v_probe_queue_snapshot）。 */
export interface LegacyQueueRow {
  probe_priority: string
  state: string
  queue_size: number
  ready_now: number
  ready_1min: number
  ready_5min: number
  earliest_retry_at?: string | null
  latest_retry_at?: string | null
  avg_wait_seconds?: number | null
  max_wait_seconds?: number | null
}

export interface LegacyQueueBlock {
  queues?: LegacyQueueRow[] | null
  total?: number | null
  legacy: true
  legacy_mode_safe: boolean
  legacy_source: string
}

/**
 * `queue-snapshot` 响应。
 *
 * ★ **刻意不声明顶层的 `queues` / `total`。**
 *   它们是 legacy 的（见文件头 (2)），而且那个 legacy 视图里躺着
 *   572 行历史积压，与活动队列无关。本接口只暴露
 *   `unified`（当前活动队列）与 `legacy`（历史遗留明细）。
 */
export interface QueueSnapshotResponse {
  unified?: UnifiedProbeQueueStats | null
  legacy?: LegacyQueueBlock | null
  snapshot_at?: string | null
  [k: string]: unknown
}

export function fetchProbeQueueSnapshot(options?: RequestOptions): Promise<QueueSnapshotResponse> {
  return req<QueueSnapshotResponse>('GET', '/api/admin/probe/queue-snapshot', undefined, options)
}

/**
 * ★★ **当前活动队列**的积压总量 —— 只能由 unified 算。
 *
 * 绝不要用 legacy 的 `total`（那是历史积压，见文件头 (2)）。
 * 这里的口径与 `probe/queue-tasks` 那页不同（那边按任务列），
 * 但「在等」的口径一致：ready（可立即执行）+ running（执行中）
 * + node_pending（节点探测侧排队）+ node_due（已到期待重试）。
 */
export function activeBacklogOf(unified: UnifiedProbeQueueStats | null | undefined): number | null {
  if (!unified) return null
  return (
    (unified.queue_ready ?? 0) +
    (unified.queue_running ?? 0) +
    (unified.node_pending ?? 0) +
    (unified.node_due ?? 0)
  )
}

/**
 * ★ 需要立刻有人看的三个数。任何一个 > 0 都是**异常**，不是普通积压。
 *
 * · `node_unclaimable`：租约过期没 worker 认领 ⇒ 队列会卡住
 * · `stale_leases`：活跃租约心跳超时 ⇒ worker 可能已经死了
 * · `queue_expired`：近 2h 过期的任务
 *
 * 分开返回而不是求和 —— 三者的处置动作完全不同，合成一个数就丢掉了处置信息。
 *
 * ★★ 三项**分属两个不同的 unified 对象**，这是本文件第二个大坑：
 *   · `node_unclaimable` / `stale_leases` 只在 **queue-snapshot** 的 unified 上
 *     （`UnifiedProbeQueueStats`，probe_dashboard.go:202-231）
 *   · `queue_expired` 只在 **system-health** 的 unified 上
 *     （`UnifiedProbeSystemHealth`，:250-257）
 *   两个对象**都叫 `unified`**，字段集却不同 ——
 *   我第一次写 `unified.queue_expired` 时是被 `vue-tsc` 抓住的（TS2339），
 *   不是自己看出来的。
 *   ⇒ 所以这里收**两个**源，而不是一个。
 */
export interface LeaseAnomalySources {
  queue?: UnifiedProbeQueueStats | null
  health?: UnifiedProbeSystemHealth | null
}

export interface LeaseAnomalies {
  nodeUnclaimable: number
  staleLeases: number
  queueExpired: number
  /** ★ 有几个源真的读到了。全 0 时用它区分「确实没有」与「没读到」。 */
  sourcesAvailable: number
}

export function leaseAnomaliesOf(src: LeaseAnomalySources | null | undefined): LeaseAnomalies {
  return {
    nodeUnclaimable: src?.queue?.node_unclaimable ?? 0,
    staleLeases: src?.queue?.stale_leases ?? 0,
    queueExpired: src?.health?.queue_expired ?? 0,
    sourcesAvailable: (src?.queue ? 1 : 0) + (src?.health ? 1 : 0),
  }
}

export function hasLeaseAnomaly(a: LeaseAnomalies): boolean {
  return a.nodeUnclaimable > 0 || a.staleLeases > 0 || a.queueExpired > 0
}

// ── success_rate_last_1h 的三态 ───────────────────────────────────────────

/**
 * ★ `success_rate_last_1h` 是 `omitempty` 的 `*float64`。
 *
 * Go 侧 `SuccessRateLast1h *float64 \`json:"success_rate_last_1h,omitempty"\``：
 * nil ⇒ **JSON 里根本没有这个键**。
 *
 * ⇒ 三态不可二元化：
 *   · `undefined`/缺失 ⇒ 近 1h **没有运行记录**（不是 0% 成功率）
 *   · `0`            ⇒ 跑了，而且**全部失败**（真实值）
 *   · `>0`           ⇒ 真实成功率
 * 把「没有记录」显示成 `0%` 就是在说「这一小时全挂了」。
 */
export type SuccessRate1h =
  | { kind: 'no_runs' }
  | { kind: 'real'; value: number }

export function successRate1hOf(unified: UnifiedProbeSystemHealth | null | undefined): SuccessRate1h {
  const v = unified?.success_rate_last_1h
  if (v === undefined || v === null || !Number.isFinite(v)) return { kind: 'no_runs' }
  return { kind: 'real', value: v }
}
