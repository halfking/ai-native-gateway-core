import { req, type RequestOptions } from './client'

/**
 * boardOperational.ts — 看板运维芯片（2026-10-07，第七十五批）。
 *
 * GET /api/admin/dashboard/operational
 *
 * 注册在 `admin/handler.go:1069` 的 `admin(...)` ⇒ **admin 档**，tenant_admin 可用
 * ⇒ 抽屉席**不设** `requiresRole`。
 *
 * 它是 `boardOperationalPayload()`（`admin/dashboard_operational.go:53-62`）的独立出口，
 * 载荷只有两个顶层键：`background_tasks` 与 `selfcheck`。
 *
 * ## ★★★★★ 本族最要紧的八件事
 *
 * (1) ★★★★★ **三个子查询有三种租户口径，而且没有一种是「按调用方租户过滤」的。**
 *      | 子查询 | 过滤条件 | 出处 |
 *      |---|---|---|
 *      | `model_discovery_runs` | **硬编码 `tenant_id = 'default'`** | `dashboard_board_aux.go:32` |
 *      | `credential_health_checks` | **完全不过滤**（全租户合计） | `:53-56` |
 *      | `self_check_runs` | **完全不过滤**（全租户合计） | `:84-89` |
 *      ⇒ ★★★★ 注册是 `admin(...)` 档（tenant_admin 可用）
 *        ⇒ **非 default 租户的 tenant_admin 看到的是别人的 discovery 状态**，
 *          而两个计数是**所有租户的合计**。
 *      ⇒ 这是本仓**第三次**出现「不按调用方隔离 + admin 档」的组合
 *        （第六十六批 data-lifecycle `metrics`、第七十三批 node-health、本条）。
 *
 * (2) ★★★★★ `degraded` 是**显式 map 赋值**，不是 struct tag ⇒ 恒发。
 *      `dashboard_board_aux.go:61-72`：
 *      ```go
 *      out := map[string]any{
 *          "discovery":  discovery,
 *          "probe_loop": map[string]any{"checks_last_10m": checksLast10m},
 *      }
 *      // 恒发：前端要区分「真的 0 次」与「没查出来」。字段缺失与 0 不可分。
 *      out["degraded"] = discErr != nil || checksErr != nil
 *      if discErr != nil { out["degraded_reason"] = "discovery status unavailable" }
 *      if checksErr != nil { out["probe_degraded"] = true }
 *      ```
 *      ★ 两个条件键**各自对应一个查询** ⇒ `degraded: true` 时必须看条件键才知道是谁挂了。
 *      ★ `selfcheck` 那边有**同名但语义不同**的 `degraded`（只由 `selfErr` 驱动，`:104`）。
 *
 * (3) ★★★★★ **`degraded: true` 可能是「表里从来没有记录」，不是「查询失败」。**
 *      `:35-37` 打日志时**排除了** `pgx.ErrNoRows`：
 *      ```go
 *      if discErr != nil && !errors.Is(discErr, pgx.ErrNoRows) {
 *          slog.Warn("board: discovery run status query failed", "error", discErr)
 *      }
 *      ```
 *      但 `:66` 的 `out["degraded"] = discErr != nil || checksErr != nil`
 *      **没有排除** ⇒ ★ 「没有 discovery 记录」⇒ `ErrNoRows` ⇒ `degraded = true`
 *      + `status: null` + `degraded_reason: "discovery status unavailable"`。
 *      ⇒ **`status: null` + `degraded: true` 要读成「没跑过 discovery」**，
 *        而不能直接说成「查询挂了」。
 *      ★★ 而 `selfcheck` 那边**不会**这样：它用的是 `COUNT(*)` 聚合，
 *        恒返回一行 ⇒ 空表时 `selfErr` 为 nil、`degraded` 为 false。
 *      ⇒ **两个 `degraded` 的触发原因不同构。**
 *
 * (4) ★★★★ `checks_last_10m === 0` 是二义的，必须配 `probe_degraded` 读。
 *      计数失败只 `slog.Warn`（`:57-59`），`checksLast10m` 保持 **0**（`:52`）
 *      ⇒ 与第六十八批的 `compressed_requests === 0`、第六十七批的
 *        `cache-economics` 估算链是**同一类二义**。
 *      ⇒ 「0 次」只有在 `probe_degraded` 缺失时才可信。
 *
 * (5) ★★★★ `success_rate` 是 **0-1 比例**（`:96-99`），且 `total == 0` 时**留 0.0**：
 *      ```go
 *      rate := 0.0
 *      if total > 0 { rate = float64(success) / float64(total) }
 *      ```
 *      ⇒ ★★ 「24 小时内没跑过自检」与「跑了但全失败」**都是 `0.0`**
 *        ⇒ 必须同时看 `total_runs_24h` 与 `degraded`。
 *      ★ 单位是 0-1（对照 `percentage` 是 0-100，第六十八批记录过这个坑）。
 *
 * (6) ★★★ `discovery` 里有**两个条件键**：`started_at` 与 `heartbeat_at`。
 *      `dashboard_board_aux.go:45-50` 都是 `if != nil` 才写；
 *      而 `running` / `status` / `trigger` 是**恒发**（`strPtrVal(nil)` ⇒ JSON `null`，
 *      见 `:196-201`）⇒ **同一对象里恒发与条件键混排**。
 *
 * (7) ★★★ **有 30 秒缓存，两层。**
 *      - 进程内 `boardOperationalCache`，TTL **30 秒**（`dashboard_operational.go:11`）
 *        ⇒ 连着两次请求**很可能拿到同一份**，缓存由 `boardOperationalPayload:54-61` 填充
 *        ⇒ ★ 两次采样看不到变化**可能只是缓存**，不是「数据没动」。
 *        ★ 缓存是**进程内**的 ⇒ 多副本部署时各副本的缓存**互不相同**。
 *      - 响应头 `Cache-Control: private, max-age=30`（`:77`）。
 *
 * (8) ★★ **`include_operational` 参数被这个端点完全忽略。**
 *      `includeBoardOperational`（`:44-52`）是**看板汇总**那条路用的；
 *      `handleDashboardOperational` 从头到尾**没有读**这个 query 参数
 *      ⇒ 传 `include_operational=0` **不会**让这个端点变轻。
 *
 * ## 另注
 *
 * · 非 GET ⇒ 405；`h.db == nil` ⇒ 503 `database not configured`。
 * · DB 查询超时 **5 秒**（`:74`）。
 * · `selfcheck.degraded_reason` 的原文是 `self-check summary unavailable`，
 *   与 background_tasks 的 `discovery status unavailable` **不同**。
 * · 客户端**不能**对两个 `degraded` 复用同一套渲染文案。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量（逐字来自后端源码）
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/dashboard_operational.go:11 `boardOperationalCacheTTL = 30 * time.Second`。 */
export const BOARD_OPERATIONAL_CACHE_TTL_MS = 30000
/** :77 的响应头。 */
export const BOARD_OPERATIONAL_CACHE_CONTROL = 'private, max-age=30'
/** :74 的查询超时。 */
export const BOARD_OPERATIONAL_QUERY_TIMEOUT_MS = 5000
/** :159 的 503 message。 */
export const BOARD_DB_NOT_CONFIGURED_MESSAGE = 'database not configured'

/** dashboard_board_aux.go:68 的 degraded_reason 原文。 */
export const BOARD_DISCOVERY_DEGRADED_REASON = 'discovery status unavailable'
/** :108 的 degraded_reason 原文。★ 与上面**不同**。 */
export const BOARD_SELFCHECK_DEGRADED_REASON = 'self-check summary unavailable'

/** ★ `include_operational` 的字面量 —— 本端点**忽略**它。 */
export const INCLUDE_OPERATIONAL_PARAM = 'include_operational'

/** ★ dashboard_board_aux.go:32 写死的租户。 */
export const BOARD_DISCOVERY_HARDCODED_TENANT = 'default'

/** background_tasks 的恒在键。 */
export const BOARD_BG_ALWAYS_KEYS = ['discovery', 'probe_loop', 'degraded'] as const
/** background_tasks 的条件键（各自对应一个查询）。 */
export const BOARD_BG_OPTIONAL_KEYS = ['degraded_reason', 'probe_degraded'] as const
/** discovery 的恒在键（`status` / `trigger` 可为 `null`）。 */
export const BOARD_DISCOVERY_ALWAYS_KEYS = ['running', 'status', 'trigger'] as const
/** discovery 的两个条件键。 */
export const BOARD_DISCOVERY_OPTIONAL_KEYS = ['started_at', 'heartbeat_at'] as const
/** probe_loop 只有一个键。 */
export const BOARD_PROBE_LOOP_KEYS = ['checks_last_10m'] as const
/** selfcheck 的恒在键。 */
export const BOARD_SELFCHECK_ALWAYS_KEYS = [
  'total_runs_24h', 'success_rate', 'last_status', 'degraded',
] as const
/** selfcheck 的条件键。 */
export const BOARD_SELFCHECK_OPTIONAL_KEYS = ['degraded_reason', 'last_run_at'] as const

export const BOARD_OPERATIONAL_KEYS = ['background_tasks', 'selfcheck'] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

/** dashboard_board_aux.go:40-50。★ 恒发与条件键混排。 */
export interface BoardDiscovery {
  /** 恒发。`discStatus == "running"`（`:39`）。 */
  running: boolean
  /** 恒发。★ `strPtrVal(nil)` ⇒ `null`。 */
  status: string | null
  /** 恒发。可为 `null`。 */
  trigger: string | null
  /** ★ 条件键：`started_at` 为 nil 时**键不存在**（`:45-47`）。 */
  started_at?: string
  /** ★ 条件键（`:48-50`）。 */
  heartbeat_at?: string
}

/** dashboard_board_aux.go:63。 */
export interface BoardProbeLoop {
  /** 恒发。★ **失败时也是 0**（只 `slog.Warn`，见文件头第 (4) 条）。 */
  checks_last_10m: number
}

/** dashboard_board_aux.go:61-72。 */
export interface BoardBackgroundTasks {
  discovery: BoardDiscovery
  probe_loop: BoardProbeLoop
  /** 恒发。★ `discErr != nil || checksErr != nil` —— 两个来源。 */
  degraded: boolean
  /** ★ 条件键，**只**对应 discovery 查询。 */
  degraded_reason?: string
  /** ★ 条件键，**只**对应 credential_health_checks 查询。 */
  probe_degraded?: boolean
}

/** dashboard_board_aux.go:101-109。 */
export interface BoardSelfCheck {
  /** 恒发。24 小时内自检总次数。 */
  total_runs_24h: number
  /** ★ **0-1 比例**；`total === 0` 时留 **0.0** ⇒ 与「全失败」同值。 */
  success_rate: number
  /** 恒发。可为 `null`（`strPtrVal(lastStatus)`）。 */
  last_status: string | null
  /** 恒发。★ 与 `background_tasks.degraded` **同名但语义不同**（只由 `selfErr` 驱动）。 */
  degraded: boolean
  /** ★ 条件键，**只**在 `selfErr != nil` 时存在。 */
  degraded_reason?: string
  /** ★ 条件键（`:109-111`）。 */
  last_run_at?: string
}

/** admin/dashboard_operational.go:59-61。 */
export interface BoardOperationalResponse {
  background_tasks: BoardBackgroundTasks
  selfcheck: BoardSelfCheck
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchBoardOperational(
  options?: RequestOptions,
): Promise<BoardOperationalResponse> {
  // ★ **不传** include_operational：本端点忽略它（文件头第 (8) 条），
  //   传了只会让人以为能关掉这块计算。
  return req<unknown>('GET', '/api/admin/dashboard/operational', undefined, options).then(
    unwrapBoardOperational,
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapBoardOperational(resp: unknown): BoardOperationalResponse {
  const d = requireObject(resp, '看板运维')
  requireKeys(d, BOARD_OPERATIONAL_KEYS, '看板运维')

  const bg = requireObject(d.background_tasks, 'background_tasks')
  requireKeys(bg, BOARD_BG_ALWAYS_KEYS, 'background_tasks')
  if (typeof bg.degraded !== 'boolean') {
    throw new Error('background_tasks.degraded 不是布尔值')
  }
  // `degraded_reason` / `probe_degraded` **不在**必检键里：
  // 它们是「查询失败时才出现」的条件键（`:67-72`）。

  const disc = requireObject(bg.discovery, 'background_tasks.discovery')
  requireKeys(disc, BOARD_DISCOVERY_ALWAYS_KEYS, 'background_tasks.discovery')
  if (typeof disc.running !== 'boolean') {
    throw new Error('background_tasks.discovery.running 不是布尔值')
  }
  // `status` / `trigger` 恒发但**可为 null**（strPtrVal(nil) ⇒ JSON null），
  // 所以这里只校键存在，不校非空。
  requireNullableString(disc.status, 'background_tasks.discovery.status')
  requireNullableString(disc.trigger, 'background_tasks.discovery.trigger')

  const probe = requireObject(bg.probe_loop, 'background_tasks.probe_loop')
  requireKeys(probe, BOARD_PROBE_LOOP_KEYS, 'background_tasks.probe_loop')
  if (typeof probe.checks_last_10m !== 'number') {
    throw new Error('background_tasks.probe_loop.checks_last_10m 不是数字')
  }

  const sc = requireObject(d.selfcheck, 'selfcheck')
  requireKeys(sc, BOARD_SELFCHECK_ALWAYS_KEYS, 'selfcheck')
  if (typeof sc.degraded !== 'boolean') {
    throw new Error('selfcheck.degraded 不是布尔值')
  }
  if (typeof sc.total_runs_24h !== 'number') {
    throw new Error('selfcheck.total_runs_24h 不是数字')
  }
  if (typeof sc.success_rate !== 'number') {
    throw new Error('selfcheck.success_rate 不是数字')
  }
  requireNullableString(sc.last_status, 'selfcheck.last_status')

  return d as unknown as BoardOperationalResponse
}

function requireNullableString(v: unknown, where: string): void {
  if (v !== null && typeof v !== 'string') {
    throw new Error(`${where} 不是字符串也不是 null`)
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/* ── ②③ degraded 的来源 ─────────────────────────────────────────────────── */

/** ★ background_tasks 的 `degraded` 由**两个**查询驱动。 */
export function boardBgDiscoveryDegraded(r: BoardOperationalResponse): boolean {
  return r.background_tasks.degraded_reason !== undefined
}

/** ★ 只有 credential_health_checks 挂时才出现的那个键。 */
export function boardBgProbeDegraded(r: BoardOperationalResponse): boolean {
  return r.background_tasks.probe_degraded === true
}

/**
 * ★★ `degraded: true` 时必须能说出「是谁挂了」；`false` 时两个条件键都不该在。
 * 依据 `:66-72`：`degraded` 为真必然至少有一个条件键。
 */
export function boardBgDegradationIsAttributable(r: BoardOperationalResponse): boolean {
  const bg = r.background_tasks
  if (bg.degraded) {
    return bg.degraded_reason !== undefined || bg.probe_degraded === true
  }
  return bg.degraded_reason === undefined && bg.probe_degraded === undefined
}

/** ★ discovery 与 probe **同时**降级（两个条件键都在）。 */
export function boardBgBothSourcesDegraded(r: BoardOperationalResponse): boolean {
  return boardBgDiscoveryDegraded(r) && boardBgProbeDegraded(r)
}

/**
 * ★ discovery 查询失败时的 reason 原文。
 *
 * ★★ `v !== ''` 这一半是**防御性归一化**：后端只可能写入非空字面量
 *   （`dashboard_board_aux.go:68`），所以对本族契约**不可达**。
 *   留着是因为 `degraded_reason` 是**条件键，解包器不校它的类型**
 *   ——这几个取值未校验的键上，这是唯一的兜底。
 */
export function boardBgDiscoveryReason(r: BoardOperationalResponse): string | null {
  const v = r.background_tasks.degraded_reason
  return v !== undefined && v !== '' ? v : null
}

/* ── ③ ErrNoRows 也会触发 degraded ──────────────────────────────────────── */

/**
 * ★★★ 「没有 discovery 记录」与「查询失败」在响应里**长得一样**：
 *   `discErr != nil` 未排除 `pgx.ErrNoRows`（`:35` 只在日志里排除了，`:66` 没有）
 *   ⇒ 两者都是 `status: null` + `degraded: true` + 同一个 `degraded_reason`。
 * ⇒ 这个判定**只能说「这三种解释里有几种还站得住」**，
 *   不能断定是哪一种 —— 需要 UI 把它讲成「没有数据或查不到」。
 */
export function boardDiscoveryAmbiguousReasons(r: BoardOperationalResponse): number {
  let n = 1 // 查询失败
  if (r.background_tasks.discovery.status === null) n++ // 从没跑过（ErrNoRows）
  if (!r.background_tasks.discovery.started_at) n++ // 没有 started_at
  return n
}

/** ★ `status === null` 表示后端没查到 status 值（**不代表查询失败**）。 */
export function boardDiscoveryHasNoStatus(r: BoardOperationalResponse): boolean {
  return r.background_tasks.discovery.status === null
}

/**
 * ★ `discovery.started_at` 缺失即「这条记录没有 started_at」。
 *
 * ★★ 同 `boardBgDiscoveryReason`：后端写的是 `discStarted.UTC().Format(time.RFC3339)`
 *   （`dashboard_board_aux.go:45-47`），**不可能**是空串 ⇒ `!== ''` 对契约不可达，
 *   保留同上（该键也是解包器不校类型的条件键）。
 */
export function boardDiscoveryStartedAtOrNull(r: BoardOperationalResponse): string | null {
  const v = r.background_tasks.discovery.started_at
  return v !== undefined && v !== '' ? v : null
}

/* ── ④ checks_last_10m 的二义 ───────────────────────────────────────────── */

/** ★ 计数为 0 **且** probe 查询降级 ⇒ 这个 0 是「查不出来」。 */
export function boardChecksCountMayBeFailed(r: BoardOperationalResponse): boolean {
  return Number(r.background_tasks.probe_loop.checks_last_10m) === 0 && boardBgProbeDegraded(r)
}

/** ★ 计数为 0 且 probe 未降级 ⇒ 「真的 0 次」可信。 */
export function boardChecksCountIsGenuineZero(r: BoardOperationalResponse): boolean {
  return Number(r.background_tasks.probe_loop.checks_last_10m) === 0 && !boardBgProbeDegraded(r)
}

/**
 * ★ 整个 `probe_loop` 块在计数查询失败时**不可信** —— 与第六十八批
 *   `compressed_requests === 0` 拖垮整条估算链是同一类。
 */
export function boardProbeLoopUnreliable(r: BoardOperationalResponse): boolean {
  return boardBgProbeDegraded(r)
}

/* ── ⑤ success_rate 的二义 ─────────────────────────────────────────────── */

/** ★ `total_runs_24h === 0` ⇒ `success_rate` 恒为 `0.0`，与「全失败」同值。 */
export function boardSuccessRateIsMeaningless(r: BoardOperationalResponse): boolean {
  return Number(r.selfcheck.total_runs_24h) === 0
}

/** ★ 真的 0 次运行 ⇒ 不是「全都不健康」。 */
export function boardSelfCheckNeverRan(r: BoardOperationalResponse): boolean {
  return Number(r.selfcheck.total_runs_24h) === 0 && !boardSelfCheckDegraded(r)
}

/** 自检查询本身降级（★ 与 background_tasks 的 `degraded` **不是一回事**）。 */
export function boardSelfCheckDegraded(r: BoardOperationalResponse): boolean {
  return r.selfcheck.degraded === true
}

/**
 * 自检查询失败时的 reason 原文（★ 与 discovery 的**不同**）。
 * ★ `!== ''` 同上：`:108` 只写非空字面量，对契约不可达，留作未校类型条件键的兜底。
 */
export function boardSelfCheckReason(r: BoardOperationalResponse): string | null {
  const v = r.selfcheck.degraded_reason
  return v !== undefined && v !== '' ? v : null
}

/**
 * ★★ 「健康」这个判断要同时满足三件事：查出来了、有运行记录、成功率不低。
 * 单看 `success_rate` 会把「没跑过」说成「0% 健康」。
 */
export function boardSelfCheckHealthy(r: BoardOperationalResponse, minRate: number): boolean {
  if (boardSelfCheckDegraded(r)) return false
  if (boardSuccessRateIsMeaningless(r)) return false
  return Number(r.selfcheck.success_rate) >= minRate
}

/* ── ⑦ 缓存 ─────────────────────────────────────────────────────────────── */

/**
 * ★★ 两次采样值完全相同**不能证明**「数据没变」——
 *    进程内有 30 秒 TTL 的缓存（`dashboard_operational.go:11`）。
 *
 * 本函数只回答「这次采样**可能**落在缓存窗口内吗」⇒ **只看间隔**：
 * 间隔不足 TTL 时进程内那份缓存一定还热着（同一 Handler 实例共享），
 * 与两次内容是否相同**无关** ⇒ 内容比较不在这里做。
 * 跨 TTL 之后的比较见 `boardOperationalSampleStableAcrossTtl`。
 */
export function boardOperationalSampleMayBeCached(
  a: BoardOperationalResponse,
  b: BoardOperationalResponse,
  elapsedMs: number,
): boolean {
  void a
  void b
  return elapsedMs < BOARD_OPERATIONAL_CACHE_TTL_MS
}

/** ★ 跨过 TTL 之后仍然完全相同，才说明采样真的没变。 */
export function boardOperationalSampleStableAcrossTtl(
  a: BoardOperationalResponse,
  b: BoardOperationalResponse,
  elapsedMs: number,
): boolean {
  if (elapsedMs < BOARD_OPERATIONAL_CACHE_TTL_MS) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

/* ── 503 ────────────────────────────────────────────────────────────────── */

export function boardNotConfigured(status: number, message: string): boolean {
  return status === 503 && message === BOARD_DB_NOT_CONFIGURED_MESSAGE
}

/* ── 内部工具 ────────────────────────────────────────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  if ('success' in resp && 'timestamp' in resp) {
    throw new Error(`${where} 拿到的是 dashboardapi 信封形状，本族应为裸 JSON`)
  }
  return resp
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}
