import { req, type RequestOptions } from './client'

/**
 * slidingWindow.ts — 凭据模型的滑窗调用明细（2026-10-08，第九十四批）。
 *
 * GET /api/credentials/sliding-window?credential_id=X&model=Y&minutes=60&limit=50
 *
 * - **注册**：**第五种注册形态**，`mux.HandleFunc` 在**另一个文件的方法**里
 *   （`admin/credential_monitor.go:158` 的 `RegisterMonitorRoutes`），
 *   由 `admin/handler.go:1528` 现构造并挂载：
 *   ```go
 *   monitorH.RegisterMonitorRoutes(mux, h.admin)   // ★ admin 档
 *   ```
 *   ⇒ ★ 与批 92 的 model-history **同一注册处、同一档位**。
 * ★★ **行号订正（2026-10-14，第一百一十五批 → 2026-10-26 行号漂移轮）**：
 *   挂载点原先写 `:1453`，再写 `:1475`，**均已过期**（现 `:1528`）。
 *   ⇒ 行号只是**定位提示**，判据不得依赖它。
 * - **实现**：`admin/credential_monitor.go:753-863`（handler）·
 *   `:890`（回退查询 `slidingWindowQuery`）· `credentialhealth/recorder.go:16-22`
 *   （`CallEntry`）· `credentialhealth/recorder.go:155-177`（`ComputeStats`）。
 * - **桌面调用方**：`web/src/api/credential-monitor.ts:149-168` —— `req<{…}>`
 *   直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十四件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ⚠️⚠️⚠️⚠️ **权限档是 `requiresRole: 'super_admin'`，不是缺省、也不是 `'admin'`**
 *     —— 与批 92/113/114 **完全相反**。
 *
 *     注册处确实是 `h.admin` 档（`admin/handler.go:1528` 传 `h.admin`），
 *     而**「admin 档」在本族曾连「管理员隔离」都做不到**：
 *     ★★★★★ **本条记的后端缺口已于 2026-10-16/17 补上**（audit #153 租户隔离
 *     + 12h-audit 20261017 角色门）⇒ **下面先记补之前的形状，再记今天的真值**
 *     （★ 这一段是「结论已过期但没人改」的活样本：批 115 当初的判断
 *     今天已经不成立，可它仍以断言句写在模块头上）。
 *     ★★★★★ `handleSlidingWindow`（`admin/credential_monitor.go:753-863`）里
 *     **曾经**没有 `tenantID` 变量，也没有任何 `IsTenantAdmin` 判断 ⇒
 *     两条数据腿都只按 `credential_id` 过滤，而 `credential_id` 是
 *     credentials 表主键、**全局唯一** ⇒ 任何通过认证的主体报出别的租户的
 *     `credential_id` 就能读到它的逐次调用明细（含 request_id、成功与否、
 *     延时、错误类别）。
 *     ⇒ ⇒★ **今天的真值（2026-10-26 逐字核对过）**：
 *     - **角色门**（`:765-768`）：`!IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r)`
 *       ⇒ 403 ⇒ **`role=user` 进不来**（放行 tenant_admin / super_admin / admin_key）；
 *     - **租户作用域**（`:793-798`）：`tenantID` 由 `IsTenantAdmin` 决定；
 *     - **Redis 腿**（`:809`）：`if !isTenantAdmin && …` ⇒ **tenant_admin 直接跳过
 *       Redis 快腿**（Redis 键只有 `(credentialID, model)`，没有 tenant tag）；
 *     - **两条 SQL 腿**（`:905` / `:950`）：都带 `AND ($5 = '' OR tenant_id = $5)`
 *       ⇒ tenant_admin 只看得到自己的租户。
 *     ⇒ ⇒ 所以 `'admin'` 档**现在**是安全的；但**移动端仍保留
 *       `requiresRole: 'super_admin'`**（比后端严一档）——那是**过度限制**、
 *       不是漏洞：批 115 当初的判断依据已被后端补洞覆盖，但收紧一档不必
 *       跟着放开（放开前要先确认 super_admin 下的 Redis 快腿也无泄漏面）。
 *     ⚠️ ⚠️ **前端严于后端是兜底不是修复** —— 后端那条路本来就开着。
 *     ⚠️ 抽屉隐藏 ≠ 路由拦截（`meta.requiresRole` 全仓无人读）⇒ 视图内还要兜一层。
 *
 *     ★★ 本条取代了本模块头原先那句「抽屉席**不设** `requiresRole`」
 *     —— 那句话是照抄批 92 的结论，**没有逐字读 handler**。
 *
 * (2) ★★★★★ **响应是 `map[string]any` 手写的，恒有 **8 个键**
 *     （`:847-862`），★ **桌面类型只声明了 6 个** —— 漏掉 `limit` 与 `total_returned`
 *     （`web/src/api/credential-monitor.ts:151-165`）⇒ 照桌面类型写的客户端
 *     **看不到 `total_returned`**，也就无从发现下面 (3) 那组互锁关系。
 *
 * (3) ★★★★★ **四个数字完全互锁**（全部可自验）：
 *     ```go
 *     if len(entries) > limit { entries = entries[:limit] }   // :841-843 先截断
 *     stats := credentialhealth.ComputeStats(entries)          // :845 再统计
 *     … "total_returned": len(entries),                        // :853
 *     ```
 *     而 `ComputeStats` 第一行就是 `Total: len(entries)`（`recorder.go:157`）
 *     ⇒ ⇒ **`total_returned === entries.length === stats.total`**
 *     ⇒ 且 `ComputeStats` 用 `if e.Success { Success++ } else { Failed++ }`
 *       （`:162-165`，**完备二分**）⇒ ⇒ **`stats.success + stats.failed === stats.total`**
 *     ⇒ ⇒ 四个数字锁在一起，任何一个错都会被另外三个抓住。
 *
 * (4) ★★★★★ **`error_kinds` 的求和「小于等于」`failed`，而不是相等**：
 *     `if e.ErrorKind != "" { stats.ErrorKinds[e.ErrorKind]++ }`（`recorder.go:166-168`）
 *     ⇒ ★★★ **失败的条目若 `err` 是空串，就完全不进 `error_kinds`**
 *     ⇒ ⇒ `sum(error_kinds) <= failed`；等号成立**当且仅当**每个失败条目都有非空 `err`。
 *     ⇒ ★★ 「失败原因分布」与「失败数」对不上时，**不是 bug，是那些失败没有归类**。
 *
 * (5) ★★★★★ **`failure_rate` 是派生值，且 `total === 0` 时恒为 `0`**：
 *     `if stats.Total > 0 { stats.FailureRate = float64(stats.Failed)/float64(stats.Total) }`
 *     （`:172-174`）⇒ ⇒ `total === 0 ⟹ failure_rate === 0`（**不是 NaN、不是 null**）
 *     ⇒ ⇒ `total > 0 ⟹ failure_rate === failed / total`。
 *
 * (6) ★★★★★ **`source` 的语义：只有一条方向可断言**：
 *     ```go
 *     source := "redis"
 *     entries := make([]CallEntry, 0)
 *     if m.recorder != nil && m.recorder.Enabled() { entries, _ = m.recorder.GetRecent(…) }
 *     if len(entries) == 0 { source = "session_turns"; … }    // :820 ← 空必回退
 *     ⚠️⚠️ **2026-10-24 后端把值换成了 `"session_turns"`**（v1 → session_turns
 *       替换轮：回退腿早已改读会话族，而 `source` 还写着 `"_request_logs"`
 *       ⇒ 运营从响应里读到的是一个**不存在的表名**）⇒ 本模块同轮订正。
 *       ★ 本行先后抄错过两版：`"request_logs"`（漏前导下划线）与
 *       `"_request_logs"`（已被后端弃用）⇒ 两次都把常量、判据实现与
 *       3 条用例一起带歪 —— 见 `SLIDING_WINDOW_SOURCES` 处的说明。
 *     ```
 *     ⇒ ⇒ **`source === 'redis'` ⟹ `entries.length > 0`**（这是一条可断言的不变式）
 *     ⇒ ⇒ ★★ **`source === 'session_turns'` 是二义的**：Redis recorder 不可用
 *       **或** Redis 可用但窗口内无数据，**两种成因同形**。
 *
 * (7) ★★★★★ **Redis 的错误被整个丢弃**：`entries, _ = m.recorder.GetRecent(…)`（`:811`）
 *     ⇒ ★★★ Redis 挂掉 ⇒ 静默回退 `session_turns` ⇒ **响应里看不出 Redis 故障**。
 *     ⇒ ★ 与批 93 的 `enabled_source`（`default` 永不可达）、批 91 的 `count` 全 0
 *       同族：**「降级/兜底」的成因在响应里查不到。**
 *
 * (8) ★★★★ **`entries` 被强制非 nil**（`:805-808`，注释自陈「否则前端
 *     `windowEntries.length` 会抛 Cannot read properties of null」）
 *     ⇒ ⇒ **空时是 `[]` 不是 `null`**。
 *
 * (9) ★★★★ **`minutes` 完全没有校验**（`:777` `queryInt(r,"minutes",60)`），
 *     而 **`limit` 有 `1..500` 的 400**（`:788-791`）
 *     ⇒ ★★★ **同一族的两个查询参数，一个校验一个不校验**
 *     ⇒ ⇒ `minutes=0` / 负数 / 超大值**全部放行**，落到 SQL 的
 *       `ts > NOW() - ($3 || ' minutes')::interval`。
 *     ⇒ ★★ 且 `minutes=abc` 会被 `queryInt` **静默回落成 60**（`admin/handler.go:1613-1623`）
 *       —— 与批 92 的 `limit=abc` 同一形态。
 *
 * (10) ★★★★ **`credential_id == 0` 才判 400**（`:780-783`）—— **不是 `< 1`**
 *     ⇒ ★★★ **负数能过这一关并进 SQL**（与批 92 完全同形）。
 *
 * (11) ★★★★ **`model` 是大小写不敏感匹配**：
 *     `AND lower(COALESCE(outbound_model, client_model)) = lower($2)`（`:899`）
 *     ⇒ ⇒ 客户端**不需要**自己 lowercase
 *     ⇒ ★★ 但**回显的 `model` 是原样传入的那一个**（`:849` 直接回显，不做规范化）
 *     ⇒ ⇒ **`model` 的回显可能与实际命中的 `outbound_model` 大小写不同。**
 *
 * (12) ★★★★ **两源的「历史深度」完全不同**：
 *     Redis recorder 是 **2 小时窗口 / 100 条**（`:772`，
 *     `credentialhealth.NewRecorder(client, 2*time.Hour, 100)`），
 *     回退源 `request_logs_with_current_month` 是**当月视图**（`:894`，
 *     注释称「视图冻结不可改投影（R37 定案）」）
 *     ⇒ ⇒ 切到 `session_turns` 后能看到的范围**突然变大**，不是「补齐了缺失的数据」。
 *
 * (13) ★★★★ **`CallEntry` 的 json tag 是缩写，与 Go 字段名完全不同**：
 *     `RequestID→rid`、`Timestamp→ts`、`Success→ok`、`LatencyMs→lat`、`ErrorKind→err`
 *     （`recorder.go:16-22`）⇒ ★★★ **照 Go 字段名写客户端必然全错**。
 *     - `rid` 是**恒在键但可能为空串**（`COALESCE(request_id,'')`，`:892`）
 *     - `ts` 是 **unix 毫秒**（`EXTRACT(EPOCH FROM ts)::bigint * 1000`）
 *     - `err` 是**唯一带 `omitempty`** 的键 ⇒ 成功条目里**键消失**
 *
 * (14) ★★★★ **回退查询 `ORDER BY ts DESC` 无 tiebreak**（`:906`）
 *     ⇒ ★★★ **同毫秒时间戳的条目顺序未定义** ⇒ 只能断言**非升序**。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验 envelope 的 **8 个恒在键与类型**、每个 `CallEntry` 的 5 个键、
 *   `stats` 的 5 个键；为 (3)(4)(5)(6)(14) 各提供判据。
 *   ★ **不校验** `source` / 各 `err` 取值的**取值域** —— 它们由 Redis / 日志自由文本决定。
 *   ★ **不提供**「`rid` 非空」判据 —— `COALESCE(…,'')` 表明空串是合法值（见 13）。
 */
export const SLIDING_WINDOW_PATH = '/api/credentials/sliding-window'

/** ★★ (9) `queryInt` 的缺省与 (10) 的边界。 */
export const SLIDING_WINDOW_DEFAULT_MINUTES = 60
export const SLIDING_WINDOW_DEFAULT_LIMIT = 50
export const SLIDING_WINDOW_LIMIT_MIN = 1
export const SLIDING_WINDOW_LIMIT_MAX = 500

/**
 * ★★ (6) 两个取值；★ `'session_turns'` 那一侧是**二义的**。
 *
 * ⚠️⚠️⚠️ **本常量先后抄错过两版，每次都把整套件带歪**：
 *   ① **2026-10-14 第一百一十五批**：原写 `['redis', 'request_logs']` ——
 *      **漏了前导下划线**，而后端发的是 `'_request_logs'`
 *      （`admin/credential_monitor.go` 的 `source = "_request_logs"`）。
 *   ② **2026-10-26**：后端在 **v1 → session_turns 替换轮**（2026-10-24）
 *      把值换成了 **`"session_turns"`** —— 回退腿早已改读会话族
 *      （短窗 `session_turns_hot` / 长窗 canonical 视图），`source` 却还写着
 *      `"_request_logs"` ⇒ 运营从响应里读到的是一个**不存在的表名** ⇒
 *      后端已改成 `"session_turns"`，**本常量没跟着改**。
 * ⇒ ★★★ 两次的后果都是**行为坏掉**，不是「文案笔误」——
 *   `slidingWindowFallbackSourceStillHasEntries` 拿旧字面量去比，
 *   对**真实回退响应**永远不相等 ⇒ **恒返回 false**
 *   ⇒ 页面上「Redis 不可用、已回退且有数据」这个信号**永远不会亮**。
 * ⇒ ★★ 为什么一直没被发现：**模块头第 (6) 条把 Go 那行抄漏了**，
 *   常量、判据实现、3 条既有用例**全部照着这份错抄写**，
 *   三者互相印证、整套件全绿 ⇒ 与后端真值**三方一致地错**。
 * ⇒ ★★★ 判别动作（本次复用同一招）：**对着事实侧（可执行代码）核**，
 *   而不是「注释 / 常量 / 判据三方互证」—— 三方互证只证明它们同源，
 *   不证明它们正确。见文件末尾「(15) 常量必须对着 Go 源码钉」的判据。
 */
export const SLIDING_WINDOW_SOURCES = ['redis', 'session_turns'] as const

/** ★★ (2) `writeJSON` 手写的 8 个键 —— ★ 桌面类型只声明了 6 个。 */
export const SLIDING_WINDOW_KEYS = [
  'credential_id',
  'model',
  'window_minutes',
  'limit',
  'source',
  'total_returned',
  'entries',
  'stats',
] as const

/** ★★ (13) `CallEntry` 的 5 个 json tag（★ 全是缩写，与 Go 字段名不同）。 */
export const CALL_ENTRY_KEYS = ['rid', 'ts', 'ok', 'lat', 'err'] as const

/** 其中唯一带 `omitempty` 的那个（成功条目里键消失）。 */
export const CALL_ENTRY_OPTIONAL_KEYS = ['err'] as const

/** `stats` 的 5 个键（全部恒在）。 */
export const WINDOW_STATS_KEYS = ['total', 'success', 'failed', 'failure_rate', 'error_kinds'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface CallEntry {
  /** ★★ (13) 恒在键，★ 但**可能为空串**（`COALESCE(request_id,'')`）。 */
  rid: string
  /** ★★ (13) unix **毫秒**。 */
  ts: number
  ok: boolean
  /** 毫秒。 */
  lat: number
  /** ★★ (13) 唯一带 `omitempty` 的键 —— 成功条目里**键不存在**。 */
  err?: string
}

export interface WindowStats {
  /** ★★★ (3) 恒等于 `entries.length`。 */
  total: number
  success: number
  failed: number
  /** ★★★ (5) `total > 0` 时 === `failed / total`；`total === 0` 时恒为 0。 */
  failure_rate: number
  /** ★★★ (4) 求和 **≤ `failed`**（失败的条目若 `err` 空串则不进这里）。 */
  error_kinds: Record<string, number>
}

export interface SlidingWindowResponse {
  credential_id: number
  /** ★★ (11) 原样回显，★ **不是**规范化后的实际命中值。 */
  model: string
  window_minutes: number
  /** ★★ (2) 桌面类型漏了这个键。 */
  limit: number
  /** ★★ (6)(7) `'session_turns'` 那一侧是**二义的**。 */
  source: string
  /** ★★★ (3) 恒等于 `entries.length` 与 `stats.total`。★ 桌面类型漏了这个键。 */
  total_returned: number
  /** ★★ (8) 恒为数组，空时是 `[]` 不是 `null`。 */
  entries: CallEntry[]
  stats: WindowStats
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/credentials/sliding-window`。
 * ★ (9) `minutes` 后端**不校验**；`limit` 越界 400（1–500）。
 * ★ (9) 两者传非数字都会被 `queryInt` **静默回落**成缺省值。
 */
export function fetchSlidingWindow(
  params: { credentialId: number; model: string; minutes?: number; limit?: number },
  options?: RequestOptions,
): Promise<SlidingWindowResponse> {
  const q = new URLSearchParams()
  q.set('credential_id', String(params.credentialId))
  q.set('model', params.model)
  q.set('minutes', String(params.minutes ?? SLIDING_WINDOW_DEFAULT_MINUTES))
  q.set('limit', String(params.limit ?? SLIDING_WINDOW_DEFAULT_LIMIT))
  return req<unknown>('GET', `${SLIDING_WINDOW_PATH}?${q.toString()}`, undefined, options).then(unwrapSlidingWindow)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapSlidingWindow(resp: unknown): SlidingWindowResponse {
  const d = requireObject(resp, '滑窗')
  requireKeys(d, SLIDING_WINDOW_KEYS, '滑窗')
  for (const k of ['credential_id', 'window_minutes', 'limit', 'total_returned'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`滑窗 的 ${k} 不是数字`)
  }
  for (const k of ['model', 'source'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`滑窗 的 ${k} 不是字符串`)
  }
  const entries = d['entries']
  if (!Array.isArray(entries)) throw new Error('滑窗 的 entries 不是数组')
  for (let i = 0; i < entries.length; i++) {
    unwrapCallEntry(entries[i], `滑窗 的 entries[${i}]`)
  }

  const s = requireObject(d['stats'], '滑窗 的 stats')
  requireKeys(s, WINDOW_STATS_KEYS, '滑窗 的 stats')
  for (const k of ['total', 'success', 'failed', 'failure_rate'] as const) {
    if (typeof s[k] !== 'number') throw new Error(`滑窗 的 stats 的 ${k} 不是数字`)
  }
  requireRecord(s['error_kinds'], '滑窗 的 stats 的 error_kinds')
  return d as unknown as SlidingWindowResponse
}

/** ★★ (13) 五个键全用缩写；`err` 带 `omitempty` ⇒ **存在才校验**。 */
export function unwrapCallEntry(v: unknown, where: string): CallEntry {
  const o = requireObject(v, where)
  requireKeys(o, ['rid', 'ts', 'ok', 'lat'], where)
  if (typeof o['rid'] !== 'string') throw new Error(`${where} 的 rid 不是字符串`)
  if (typeof o['ts'] !== 'number') throw new Error(`${where} 的 ts 不是数字`)
  if (typeof o['lat'] !== 'number') throw new Error(`${where} 的 lat 不是数字`)
  if (typeof o['ok'] !== 'boolean') throw new Error(`${where} 的 ok 不是布尔`)
  if ('err' in o && typeof o['err'] !== 'string') throw new Error(`${where} 的 err 不是字符串`)
  return o as unknown as CallEntry
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}

function requireRecord(v: unknown, where: string): Record<string, number> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error(`${where} 不是对象`)
  const rec = v as Record<string, unknown>
  for (const k of Object.keys(rec)) {
    if (typeof rec[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  return rec as Record<string, number>
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (3) 四个数字互锁 ──

/** ★★★ (3) `total_returned === entries.length`。 */
export function slidingWindowTotalMatchesEntries(r: SlidingWindowResponse): boolean {
  return r.total_returned === r.entries.length
}

/** ★★★ (3) `stats.total === entries.length`（`ComputeStats` 的第一行）。 */
export function slidingWindowStatsTotalMatchesEntries(r: SlidingWindowResponse): boolean {
  return r.stats.total === r.entries.length
}

/** ★★★ (3) `success + failed === total`（`if/else` 是完备二分，不是有重叠的三项和）。 */
export function slidingWindowSuccessPlusFailedEqualsTotal(r: SlidingWindowResponse): boolean {
  return r.stats.success + r.stats.failed === r.stats.total
}

// ── (4) error_kinds 的和 ≤ failed ──

/** `error_kinds` 全部计数之和。 */
export function slidingWindowErrorKindSum(s: WindowStats): number {
  return Object.values(s.error_kinds).reduce((a, b) => a + b, 0)
}

/**
 * ★★★★★ (4) **`sum(error_kinds) ≤ failed`**，而不是相等 ——
 * 失败的条目若 `err` 是空串就**完全不进** `error_kinds`（`recorder.go:166-168`）。
 * ⇒ ⇒ 「失败原因分布」条数少于「失败数」是**正常的**，差值就是没归类的那些。
 */
export function slidingWindowErrorKindsAtMostFailed(s: WindowStats): boolean {
  return slidingWindowErrorKindSum(s) <= s.failed
}

/**
 * ★★★★ (4) 的**反面**：等号成立**当且仅当每个失败条目都有非空 `err`**。
 * ⇒ ⇒ 客户端用它判断「失败原因分布是否完整」。
 */
export function slidingWindowErrorKindsCoverEveryFailure(entries: CallEntry[]): boolean {
  let failed = 0
  let classified = 0
  for (const e of entries) {
    if (e.ok) continue
    failed++
    if (e.err !== undefined && e.err !== '') classified++
  }
  return classified === failed
}

// ── (5) failure_rate 是派生值 ──

/**
 * ★★★★★ (5) `total === 0` 时 `failure_rate` **恒为 0**（后端 `if Total > 0` 才计算），
 * `total > 0` 时恒等于 `failed / total`。
 * ⇒ ★★ 客户端**不能**直接写 `rate === failed / total` —— `total === 0` 时那是 `NaN`。
 */
export function slidingWindowFailureRateMatches(s: WindowStats): boolean {
  if (s.total === 0) return s.failure_rate === 0
  return s.failure_rate === s.failed / s.total
}

// ── (6) source 的语义 ──

/**
 * ★★★★ (6) **`source === 'redis'` ⟹ `entries.length > 0`** ——
 * 因为 `len(entries) == 0` 会触发回退并把 `source` 改成 `'session_turns'`（`:819-820`）。
 * ⇒ ★★ 这是 `source` 唯一**可以断言**的方向。
 */
export function slidingWindowRedisSourceImpliesEntries(r: SlidingWindowResponse): boolean {
  if (r.source !== 'redis') return true
  return r.entries.length > 0
}

/**
 * ★★★★ (6) 的反面：**`source === 'session_turns'` 不代表「entries 为空」** ——
 * Redis 窗口内无数据、但会话族有数据时，source 也是 `'session_turns'`。
 * ⇒ ★★★ 客户端**不能**用 `source` 推断「是不是降级失败了」。
 */
export function slidingWindowFallbackSourceStillHasEntries(r: SlidingWindowResponse): boolean {
  // ★★ 字面量必须与后端**逐字一致** —— 那是 `admin/credential_monitor.go:820`
  //   发出来的原样（`source = "session_turns"`）。
  //   ⇒ ★★★ 2026-10-26 订正：本行原先写 `'_request_logs'`（2026-10-14 那轮把它
  //   从漏下划线的 `'request_logs'` 改成带下划线的），而后端在 2026-10-24 的
  //   v1 → session_turns 替换轮里已把值换成 `"session_turns"` ⇒ **写旧字面量
  //   会让本函数对**真实回退响应**恒返回 false** ⇒ 页面上「Redis 不可用、
  //   已回退且有数据」这个信号永远不会亮。
  if (r.source !== 'session_turns') return false
  return r.entries.length > 0
}

// ── (14) 时间序无 tiebreak ──

/**
 * ★★★★ (14) 回退查询 `ORDER BY ts DESC` **无 tiebreak** ⇒ 同毫秒顺序未定义
 * ⇒ 只能断言**非升序**，**不能**断言严格降序。
 */
export function slidingWindowEntriesAreNonAscending(entries: CallEntry[]): boolean {
  for (let i = 1; i < entries.length; i++) {
    if ((entries[i - 1] as CallEntry).ts < (entries[i] as CallEntry).ts) return false
  }
  return true
}

// ── (9)(10) 查询参数边界 ──

/**
 * ★★★ (9) `limit` 是否落在后端接受的 1–500 内。
 * ★ **`minutes` 刻意没有对应判据** —— 后端**根本不校验它**，
 *   任何 `minutes`（0、负数、超大值）都会被放行并进入 SQL 的 interval 表达式。
 */
export function slidingWindowLimitIsInRange(limit: number): boolean {
  return limit >= SLIDING_WINDOW_LIMIT_MIN && limit <= SLIDING_WINDOW_LIMIT_MAX
}

/** ★★ (10) `credential_id == 0` 才 400（不是 `< 1`）⇒ 负数能过这一关。 */
export function slidingWindowCredentialIdIsRejected(credentialId: number): boolean {
  return credentialId === 0
}

// ═══════════════════════════════════════════════════════════════════════════
// 角色档（2026-10-14，第一百一十五批；口径与批 113/114 的两族**都不同**）
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★ (1) 本族**没有「仅本租户」这个口径** —— 两条数据腿都没有租户维度，
 * 而 `credential_id` 是全局唯一的主键 ⇒ 任何角色报出凭据 id 都能读到它。
 * ⇒ 因此这里只有「全租户」与「非预期」两值，`own` **不存在**。
 */
export const SLIDING_WINDOW_GLOBAL_ROLES = ['super_admin', 'admin_key'] as const

/**
 * ★★★★ 该角色能否访问本页。
 *
 * ⚠️⚠️ 与 `errorsTrend` / `modelHistory` 的同名 helper **语义相反**：
 * 那两族里 `tenant_admin` 够用（后端把它们收口到本租户）；
 * ★★ 本族**连租户维度都没有**，所以 `tenant_admin` **必须被挡** ——
 * 让它进来只会造成「以为自己被隔离了」的错觉。
 */
export function slidingWindowRoleIsPrivileged(role: string): boolean {
  return (SLIDING_WINDOW_GLOBAL_ROLES as readonly string[]).includes(role)
}

/**
 * ★★ 租户口径 —— ★★ **只有两值，没有 `own`**（见 `SLIDING_WINDOW_GLOBAL_ROLES` 的说明）。
 * `unexpected` 表示「本族根本没有为这个角色做过隔离」。
 */
export function slidingWindowTenantScopeLabel(role: string): 'all' | 'unexpected' {
  return slidingWindowRoleIsPrivileged(role) ? 'all' : 'unexpected'
}

/**
 * ★★★★ 三个参数是否值得发。
 *
 * - `credential_id`：**只拦 0**（后端 `:780-783` 判的是 `=== 0` 而不是 `< 1`
 *   ⇒ 负数能过后端那一关，**前端不替它加规则**）；
 *   非数字必须拦 —— `queryInt` 解析失败会**静默回落成 0** ⇒ 400 文案说
 *   `credential_id required`，与真实原因不符。
 * - `model`：后端只判非空。
 * - `limit`：必须落在 1–500。
 * - ★ **`minutes` 刻意不在这里** —— 后端**根本不校验它**（(9)），
 *   任何值都放行，所以客户端**没有「会被 400」这一格可挡**；
 *   页面只能把「我问了什么」与「后端认了什么」并排显示。
 */
export function slidingWindowParamsAreAcceptable(p: {
  credentialId: number
  model: string
  limit: number
}): boolean {
  if (!Number.isFinite(p.credentialId)) return false
  if (slidingWindowCredentialIdIsRejected(p.credentialId)) return false
  if (p.model === '') return false
  if (!Number.isFinite(p.limit)) return false
  return slidingWindowLimitIsInRange(p.limit)
}
