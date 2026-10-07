import { req, type RequestOptions } from './client'

/**
 * dashboardBoard.ts — 看板的两条**裸 JSON** 端点（2026-10-08，第五十九批）。
 *
 * GET /api/admin/dashboard/operational          （handler.go:1068，admin 档）
 * GET /api/admin/dashboard/board/error-drill     （handler.go:1069，admin 档）
 *
 * ⚠️ 这两条**不在** `admin/dashboardapi` 包里，走的是 `writeJSON` ⇒ **无信封**，
 * 与同族的另外七条（`api/dashboard.ts`，`{success,data,metadata}` 信封 +
 * `metadata.degraded` 降级三联）**不是同一种形状**。
 * ⇒ 同一个 `/api/admin/dashboard/*` 前缀下有两种响应契约，不要跨端点类推。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/dashboard/operational
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ `discovery.status` / `trigger` / `selfcheck.last_status` 是
 * **`strPtrVal` 出来的 `string | null`**（dashboard_board_aux.go:196-201：
 * nil 指针返 nil，不是空串）。
 *
 * ★★ 两块里的 `degraded` 都是**恒发**（无 omitempty）—— 后端 2026-10-03
 * 专门加的，为的就是让前端能区分「真的是 0 次」与「没查出来」。
 * 这与 dashboardapi 那七条不同：那边的 `metadata.degraded` 带 omitempty。
 */
export interface OperationalDiscovery {
  /** ★ `status == "running"` 才 true；查不出来时也是 false。 */
  running: boolean
  status: string | null
  trigger: string | null
  started_at?: string
  heartbeat_at?: string
}

export interface OperationalBackgroundTasks {
  discovery: OperationalDiscovery
  probe_loop: { checks_last_10m: number }
  /** 恒发。`discErr != nil || checksErr != nil`（aux.go:66）。 */
  degraded: boolean
  /** 仅 `discErr != nil` 时出现。 */
  degraded_reason?: string
  /** 仅 `checksErr != nil` 时出现。 */
  probe_degraded?: boolean
}

export interface OperationalSelfCheck {
  total_runs_24h: number
  success_rate: number
  last_status: string | null
  /** 恒发。 */
  degraded: boolean
  degraded_reason?: string
  last_run_at?: string
}

export interface OperationalResponse {
  background_tasks: OperationalBackgroundTasks
  selfcheck: OperationalSelfCheck
}

export const OPERATIONAL_KEYS = ['background_tasks', 'selfcheck'] as const
export const OPERATIONAL_DISCOVERY_KEYS = ['running', 'status', 'trigger'] as const
export const OPERATIONAL_BG_KEYS = ['discovery', 'probe_loop', 'degraded'] as const
export const OPERATIONAL_SELFCHECK_KEYS = ['total_runs_24h', 'success_rate', 'last_status', 'degraded'] as const

export function fetchDashboardOperational(options?: RequestOptions): Promise<OperationalResponse> {
  return req<unknown>('GET', '/api/admin/dashboard/operational', undefined, options).then(
    unwrapOperational,
  )
}

export function unwrapOperational(resp: unknown): OperationalResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`看板运维面 响应形状不符：期望裸对象（无信封），实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  // ★ 顺带拦一道：本族是裸 JSON。若拿到 dashboardapi 的信封形状，
  //   说明调错了端点或后端换实现了 —— 静默放行会把 success/timestamp 当业务数据。
  if ('success' in d && 'timestamp' in d) {
    throw new Error('看板运维面 拿到的是 dashboardapi 信封形状，本端点应为裸对象')
  }
  const missing = OPERATIONAL_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`看板运维面 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  const bg = d.background_tasks as Record<string, unknown>
  const bgMissing = OPERATIONAL_BG_KEYS.filter((k) => !(k in bg))
  if (bgMissing.length > 0) {
    throw new Error(`看板运维面 background_tasks 缺 ${bgMissing.length} 个键（${bgMissing.join(', ')}）`)
  }
  const disc = bg.discovery as Record<string, unknown>
  const discMissing = OPERATIONAL_DISCOVERY_KEYS.filter((k) => !(k in disc))
  if (discMissing.length > 0) {
    throw new Error(`看板运维面 discovery 缺 ${discMissing.length} 个键（${discMissing.join(', ')}）`)
  }
  const sc = d.selfcheck as Record<string, unknown>
  const scMissing = OPERATIONAL_SELFCHECK_KEYS.filter((k) => !(k in sc))
  if (scMissing.length > 0) {
    throw new Error(`看板运维面 selfcheck 缺 ${scMissing.length} 个键（${scMissing.join(', ')}）`)
  }
  return d as unknown as OperationalResponse
}

/**
 * ★★★★ **「从未运行过」被后端算成 `degraded`**。
 *
 * `queryBoardBackgroundTasks`（aux.go:29-37）只对 **非** `pgx.ErrNoRows`
 * 记 slog，但第 66 行算 degraded 用的是**原始 err**：
 *
 * ```go
 * out["degraded"] = discErr != nil || checksErr != nil
 * if discErr != nil { out["degraded_reason"] = "discovery status unavailable" }
 * ```
 *
 * 而那条查询是 `... ORDER BY started_at DESC LIMIT 1`
 * ⇒ **一张都没跑过时返回 ErrNoRows ⇒ degraded=true**。
 * 且 `strPtrVal(nil)` = `null`，所以真错误与「没记录」在 status 上也一样是 `null`
 * ⇒ 客户端**分不开**这两种情况。
 *
 * 后果：一套**从未跑过 discovery** 的新网关会一直挂着一个红的降级提示，
 * 直到第一次真正跑起来为止 —— 而它什么故障都没有。
 * UI 侧能做的只有把它说成「状态未知 / 从未运行过」，不能直接说「降级」。
 */
export function operationalDiscoveryNeverRan(b: OperationalBackgroundTasks): boolean {
  return b.degraded && b.discovery.status === null
}

/** ★ selfcheck 的 `total_runs_24h === 0` 与 `degraded` 必须分开看（两个独立来源）。 */
export function operationalSelfCheckMeaningless(s: OperationalSelfCheck): boolean {
  return s.total_runs_24h === 0
}

/** ★ 两块里任一 degraded ⇒ 这一屏有数字不可信。 */
export function operationalAnyDegraded(r: OperationalResponse): boolean {
  return r.background_tasks.degraded || r.selfcheck.degraded
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/dashboard/board/error-drill
 * ═════════════════════════════════════════════════════════════════════════ */

/** boardPieItem（dashboard_board.go:10-16），逐字照抄。 */
export interface ErrorDrillItem {
  key: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

/**
 * ★★ **`source` 是条件键**：
 *   - 命中看板缓存时写入 `"source": "redis"`（dashboard_board.go:187）
 *   - 未命中时那个 map 里**根本没有 source**（:198-202 只写三个键）
 *
 * ⇒ 「键缺失」= 现算，「source=redis」= 缓存命中。两者都不是「数据来源视图」。
 *
 * ★★ 真正的取数来源**没有下发**：`queryErrorDrill`（aux.go:115-136）在
 * 分钟视图失败/为空时会**回落到 hot-log 兜底**（fallbackErrorDrill），
 * 而 `boardSource()`（能说出 request_stats_minute 还是
 * request_logs_with_current_month）**只在 `/dashboard/board` 用**（:121），
 * 本端点根本调不到它。
 * ⇒ 「兜底来的」与「权威视图来的」在响应里**无法区分**。
 */
export interface ErrorDrillResponse {
  error_kind: string
  dimension: string
  items: ErrorDrillItem[]
  source?: string
}

export const ERROR_DRILL_KEYS = ['error_kind', 'dimension', 'items'] as const
export const ERROR_DRILL_ITEM_KEYS = ['key', 'requests', 'tokens', 'credits', 'cost_usd'] as const

export const ERROR_DRILL_DIMENSION_DEFAULT = 'model'
export const ERROR_DRILL_DAYS_DEFAULT = 1
export const ERROR_DRILL_DAYS_MAX = 90

export interface ErrorDrillQuery {
  /** ★ **必填**，缺了后端 400（handler:157-160）⇒ 前端不填就别发。 */
  errorKind: string
  /** 缺省 "model"（:165-167）。 */
  dimension?: string
  days?: number
  tenantId?: string
}

function errorDrillSuffix(q: ErrorDrillQuery): string {
  const qs = new URLSearchParams()
  qs.set('error_kind', q.errorKind)
  if (q.dimension !== undefined) qs.set('dimension', q.dimension)
  if (q.days !== undefined) qs.set('days', String(q.days))
  if (q.tenantId !== undefined) qs.set('tenant_id', q.tenantId)
  return `?${qs}`
}

export function fetchErrorDrill(
  q: ErrorDrillQuery,
  options?: RequestOptions,
): Promise<ErrorDrillResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/dashboard/board/error-drill${errorDrillSuffix(q)}`,
    undefined,
    options,
  ).then(unwrapErrorDrill)
}

export function unwrapErrorDrill(resp: unknown): ErrorDrillResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`错误下钻 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  if ('success' in d && 'timestamp' in d) {
    throw new Error('错误下钻 拿到的是 dashboardapi 信封形状，本端点应为裸对象')
  }
  const missing = ERROR_DRILL_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`错误下钻 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (!Array.isArray(d.items)) {
    throw new Error('错误下钻 items 不是数组')
  }
  d.items.forEach((it, i) => {
    if (!it || typeof it !== 'object' || Array.isArray(it)) {
      throw new Error(`错误下钻 items[${i}] 不是对象`)
    }
    const im = ERROR_DRILL_ITEM_KEYS.filter((k) => !(k in (it as object)))
    if (im.length > 0) {
      throw new Error(`错误下钻 items[${i}] 缺 ${im.length} 个键（${im.join(', ')}）`)
    }
  })
  return d as unknown as ErrorDrillResponse
}

/** ★ `source` 缺失 = 现算（不是「未知来源」）。 */
export function errorDrillServedFromCache(r: ErrorDrillResponse): boolean {
  return r.source === 'redis'
}

/**
 * ★★ `days` 是 **clamp**（boardDays，dashboard_board.go:204-213）：
 * <1 → 1，>90 → 90。**默认 1**（不是 7）。
 * ⚠️ 本仓同一个 `days` 参数已有四种口径：
 *   dashboardapi 静默回落 7 / tuning-accuracy 报 400 / **board clamp 到 [1,90]**。
 */
export function errorDrillDaysEffective(days: number | undefined): number {
  if (days === undefined || !Number.isInteger(days)) return ERROR_DRILL_DAYS_DEFAULT
  if (days < 1) return 1
  if (days > ERROR_DRILL_DAYS_MAX) return ERROR_DRILL_DAYS_MAX
  return days
}

/** ★ 后端不回显归一化后的 dimension ⇒ 客户端要按同一规则算，才能显示实际口径。 */
export function errorDrillDimensionEffective(dimension: string | undefined): string {
  const v = (dimension ?? '').trim()
  return v === '' ? ERROR_DRILL_DIMENSION_DEFAULT : v
}

/** ★ `error_kind` 缺省/空白 ⇒ 后端 400，别发。 */
export function errorDrillKindValid(kind: string | null | undefined): boolean {
  return typeof kind === 'string' && kind.trim() !== ''
}

/** ★ 零行 ≠ 兜底来的空数据 —— 两者都只能靠 items.length===0 表达。 */
export function errorDrillEmpty(r: ErrorDrillResponse): boolean {
  return r.items.length === 0
}