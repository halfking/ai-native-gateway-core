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
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★★ `operational` 整段**不做租户隔离**（2026-10-08 第一百零四批复核）
 * ══════════════════════════════════════════════════════════════════════════
 *
 * ★★★ 这条**本模块原先没有记**，只记在孤儿模块 `boardOperational.ts` 的文件头里 ——
 * 那是「知识被遗弃在没人看的地方」的典型：两边讲同一个端点，严谨的那份偏偏是没人调的那份。
 * ⇒ 本批把契约搬到这里。**孤儿那份的处置另议（不合并），但知识必须跟着在用的代码走。**
 *
 * | 子查询 | 过滤条件 | 出处 |
 * |---|---|---|
 * | `model_discovery_runs` | **硬编码 `tenant_id = 'default'`** | `dashboard_board_aux.go:32` |
 * | `credential_health_checks` | **完全没有 WHERE**（全租户合计） | `:52-56` |
 * | `self_check_runs` | **只按时间，没有 tenant 条件** | `:83-89` |
 *
 * 而注册是 `admin(...)` 档（`admin/handler.go:1068`）⇒ **tenant_admin 够得着**。
 * ⇒ ⇒ 在多租户部署下，一个**非 default 租户**的 tenant_admin 从这一段看到的是：
 *   ① **default 租户**的 discovery 状态；② **所有租户**合计的凭据健康检查次数（10 分钟窗）；
 *   ③ **所有租户**合计的 self-check 次数与成功率。
 * ⇒ 这是本仓**第三次**「不按调用方隔离 + admin 档」的组合
 *   （第六十六批 data-lifecycle `metrics`、第七十三批 node-health、本条）。
 *
 * ★★★ **前端处置：这一段只对 `super_admin` 可见**（前端严于后端），
 *   见 `DashboardOpsView.vue` 的 `operationalVisible`。
 *   ★ **这不是把后端修好了** —— 后端该加 tenant 条件，本条只是不让移动端替它兜底。
 *   ⇒ 后端修复前，任何绕过前端直接调端点的调用方仍然会拿到全租户数据。
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

/** ★ 第一百零四批新增：层级可指认的对象守卫（消息里带 where，不是一句 "in operator"）。 */
function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
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
  // ★★★ 2026-10-08 第一百零四批：三级子对象**在用 `k in x` 之前必须先过对象守卫**。
  //   原先直接 `d.background_tasks as Record<...>` 然后 `k in bg` ——
  //   ★ 而 `'a' in null` 在 JS 里抛的是 **TypeError: Cannot use 'in' operator**
  //   （不是本模块的契约错误），调用方拿到的是一个无法归因的栈。
  //   形状不符与键缺失要给出**能指认层级的**消息，否则线上排查只能看到 "in operator"。
  const bg = requireObject(d['background_tasks'], '看板运维面 background_tasks')
  const bgMissing = OPERATIONAL_BG_KEYS.filter((k) => !(k in bg))
  if (bgMissing.length > 0) {
    throw new Error(`看板运维面 background_tasks 缺 ${bgMissing.length} 个键（${bgMissing.join(', ')}）`)
  }
  const disc = requireObject(bg['discovery'], '看板运维面 background_tasks.discovery')
  const discMissing = OPERATIONAL_DISCOVERY_KEYS.filter((k) => !(k in disc))
  if (discMissing.length > 0) {
    throw new Error(`看板运维面 discovery 缺 ${discMissing.length} 个键（${discMissing.join(', ')}）`)
  }
  const sc = requireObject(d['selfcheck'], '看板运维面 selfcheck')
  const scMissing = OPERATIONAL_SELFCHECK_KEYS.filter((k) => !(k in sc))
  if (scMissing.length > 0) {
    throw new Error(`看板运维面 selfcheck 缺 ${scMissing.length} 个键（${scMissing.join(', ')}）`)
  }

  // ★★★ 同批补上：原先**只校键、完全不校类型**。
  //   「恒在键」= 后端无条件写进 map 的那些（`degraded` 是显式 `out["degraded"] = …`，
  //   `discovery` 三键在 aux.go:42-46 恒赋值）⇒ 类型不符一定是出了问题，必须抛。
  //   ★ 而 `checks_last_10m` 恒是数字（Go 侧 `var checksLast10m int`，查询失败时
  //   Scan 不写、零值 0 原样下发）⇒ 校验它是类型校验，不是语义校验。
  if (typeof disc['running'] !== 'boolean') {
    throw new Error('看板运维面 discovery 的 running 不是布尔')
  }
  for (const k of ['status', 'trigger'] as const) {
    if (disc[k] !== null && typeof disc[k] !== 'string') {
      throw new Error(`看板运维面 discovery 的 ${k} 不是字符串也不是 null`)
    }
  }
  if (typeof bg['degraded'] !== 'boolean') throw new Error('看板运维面 background_tasks 的 degraded 不是布尔')
  const loop = requireObject(bg['probe_loop'], '看板运维面 background_tasks.probe_loop')
  if (typeof loop['checks_last_10m'] !== 'number') {
    throw new Error('看板运维面 probe_loop 的 checks_last_10m 不是数字')
  }
  if (typeof sc['total_runs_24h'] !== 'number') throw new Error('看板运维面 selfcheck 的 total_runs_24h 不是数字')
  if (typeof sc['success_rate'] !== 'number') throw new Error('看板运维面 selfcheck 的 success_rate 不是数字')
  if (sc['last_status'] !== null && typeof sc['last_status'] !== 'string') {
    throw new Error('看板运维面 selfcheck 的 last_status 不是字符串也不是 null')
  }
  if (typeof sc['degraded'] !== 'boolean') throw new Error('看板运维面 selfcheck 的 degraded 不是布尔')
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