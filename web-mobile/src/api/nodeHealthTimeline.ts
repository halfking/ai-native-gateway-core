import { req, type RequestOptions } from './client'

/**
 * nodeHealthTimeline.ts — 节点恢复时间线（2026-10-07，第七十三批）。
 *
 * GET /api/admin/node-health/{credential_id}/timeline?since=24h
 *
 * 注册在 `admin/handler.go:1028` 的 `admin(...)` ⇒ **admin 档**，tenant_admin 可用
 * ⇒ 抽屉席**不设** `requiresRole`。
 * ⚠️ 与第七十二批的 connection-registry 一样，桌面把这条路由标成了
 *   `requiresSuper: true`（`web/src/router.ts:285`）—— **前端比后端严**，
 *   以可执行的注册为准。
 *
 * 真源是 `node_probe_runs`（探测完成后落库的审计行），
 * 不返回 request_body / headers（`admin/node_health.go:14`）。
 *
 * ## ★★★★★ 本族最要紧的八件事
 *
 * (1) ★★★★★ **这条端点完全没有租户过滤。**
 *      `admin/node_health.go:183-184`：
 *      ```sql
 *      WHERE credential_id = $1 AND started_at >= $2
 *      ```
 *      **只有两个条件，没有 tenant_id** ⇒ 而注册是 `admin(...)` 档（tenant_admin 可用）
 *      ⇒ ★★ **tenant_admin 能查任意 credential_id 的完整探测时间线**，
 *      包括 `reason_code`（错误码）与 `note`（模型名 · 触发来源）。
 *      ★ 与第六十六批 data-lifecycle 的 `metrics` 端点**同型**：
 *        「不隔离 + admin 档注册」的组合在本仓已出现两次。
 *      ⇒ 移动端接入时**不要**把它放在 tenant_admin 可见的位置而不加说明。
 *
 * (2) ★★★★★ **`observation_status` 是硬编码的 `"complete"`，没有任何分支能产生别的值。**
 *      `admin/node_health.go:173`：
 *      ```go
 *      writeJSON(w, http.StatusOK, nodeRecoveryTimelineResponse{
 *          CredentialID:      strconv.FormatInt(credID, 10),
 *          ObservationStatus: "complete",
 *          Events:            events,
 *      })
 *      ```
 *      而查询失败走的是 **503**（`:165-167`），不是「status: partial + 空 events」
 *      ⇒ **「部分观测」这个状态在本端点上不可表达**。
 *      ★★ 解包器**只校验它是字符串，不校验取值** —— 校验 `"complete"` 是**恒真判据**。
 *
 * (3) ★★★★★ **`event_type` 是三态，且「恢复」有两个值。**
 *      `admin/node_health.go:82-90`：
 *      ```go
 *      eventType := "failed"
 *      if row.Success {
 *          eventType = "recovered"
 *          if row.TriggerKind == "credential_recovery" {
 *              eventType = "reconnected"      // ← 强制恢复触发的探测
 *          }
 *      }
 *      ```
 *      ⇒ `failed` / `recovered` / `reconnected`，**后两个都是「成功」**。
 *      ★★ 把「恢复」当单一状态处理会**漏掉 `reconnected`**
 *        —— 而 `credential_recovery` 正是**强制恢复**那条路径的触发来源。
 *
 * (4) ★★★★ **三个条件键全是「指针 + omitempty」，且填充条件各不相同。**
 *      `admin/node_health.go:26-32` + `:99-110`：
 *      | 键 | 什么时候**有**键 |
 *      |---|---|
 *      | `duration_ms` | **仅 `> 0`**（`:100-102`）⇒ 0 毫秒 ⇒ **键缺失**，不是 `0` |
 *      | `reason_code` | 非 nil **且** trim 后非空 **且 ≠ `"none"`**（`:105-107`） |
 *      | `note` | 模型名 / 触发来源至少一个非空（`:108-109` + `:126-140`） |
 *      ★★ `"none"` 是**哨兵值**：`firstNonEmptyPtr` 会选中它，紧接着的过滤又把它丢掉
 *        ⇒ **客户端永远看不到 `"none"`**，键缺失就是「无原因码」。
 *      ★ 这是本仓**第六种** nil/缺键编码：
 *        已见「恒数组 / 键缺失 / 裸 null / omitempty 条件键 / 恒发布尔 / **指针+omitempty**」。
 *
 * (5) ★★★★ **查询失败是 503，不是 500。**
 *      `admin/node_health.go:165-167`：
 *      ```go
 *      writeError(w, http.StatusServiceUnavailable, "node-health timeline unavailable")
 *      ```
 *      ⇒ 「暂时查不到」与「数据库没配」（`:158` 的 `database not configured`）**都是 503**，
 *        但 message 不同 ⇒ 客户端要按 message 区分，不要只看状态码。
 *
 * (6) ★★★★ **`since` 按后缀分派两种语法，且超上限静默 clamp 到 7 天。**
 *      `parseTimelineSince`（`admin/node_health.go:52-79`）：
 *      ```go
 *      if strings.HasSuffix(raw, "d") {
 *          days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
 *          if err != nil || days < 1 { return 0, fmt.Errorf("invalid since: %q", raw) }
 *          d := time.Duration(days) * 24 * time.Hour
 *          if d > nodeHealthMaxSince { d = nodeHealthMaxSince }   // ★ 静默 clamp
 *          return d, nil
 *      }
 *      d, err := time.ParseDuration(raw)                          // ns/us/ms/s/m/h
 *      if err != nil { return 0, ... }
 *      if d <= 0 { return 0, ... }
 *      if d > nodeHealthMaxSince { d = nodeHealthMaxSince }        // ★ 静默 clamp
 *      ```
 *      ★ Go 的 `ParseDuration` **不认 `d`** ⇒ `7d` 走上面那条分支，**两套语法不打架**。
 *      ★ 两种语法**都是 clamp 到 7 天**，都**不报错** ⇒ `since=30d` 静默变 7 天。
 *      ★ 缺省是 **24h**（`nodeHealthDefaultSince`），不是 7 天也不是 30 天。
 *
 * (7) ★★★ **排序键与展示键不是同一个字段。**
 *      SQL 是 `ORDER BY started_at DESC LIMIT 200`（`:186-187`），
 *      而展示用的 `occurred_at` 优先取 `CompletedAt`、回落 `StartedAt`（`:91-94`）
 *      ⇒ **跨完成的探测会与「开始时间」的排序不一致**。
 *      ★ 并且过滤也按 `started_at >= $2` ⇒ **`since` 过滤的是开始时间，不是展示时间**。
 *
 * (8) ★★★ **上限 200 不回显，被丢的是最早的。**
 *      `admin/node_health.go:21` `nodeHealthTimelineCap = 200`
 *      + SQL `ORDER BY started_at DESC LIMIT $3`（`:187`）
 *      ⇒ 取最近 200 条，Go 里再反转成升序（`:211-213`）
 *      ⇒ 响应里**没有任何字段**说明被截断了 ⇒ 只能说「可能还有更早的」。
 *
 * ## 另注
 *
 * · 响应里的 `credential_id` 是**字符串**（`strconv.FormatInt`，`:172`），
 *   不是数字 ⇒ 客户端不能按 number 解析。
 * · `credential_id` 非正整数 ⇒ **400 `invalid credential_id`**（`:146-151`），
 *   注意是 `<= 0` ⇒ `0` 和负数都拒。
 * · `events` **恒为数组**：`make([]nodeRecoveryEvent, 0, 32)`（`:200`）
 *   且 `:169-171` 又显式兜了一次 nil。
 * · `occurred_at` 是 **RFC3339Nano**（纳秒，`:99`）。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量（逐字来自后端源码）
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/node_health.go:17 `nodeHealthDefaultSince = 24 * time.Hour`。 */
export const NODE_TIMELINE_SINCE_DEFAULT = '24h'
/** admin/node_health.go:18 `nodeHealthMaxSince = 7 * 24 * time.Hour`。 */
export const NODE_TIMELINE_SINCE_MAX = '7d'
/** 同上，换算成毫秒供客户端比较。 */
export const NODE_TIMELINE_SINCE_MAX_MS = 7 * 24 * 60 * 60 * 1000
/** admin/node_health.go:21 `nodeHealthTimelineCap = 200`。★ 响应不回显。 */
export const NODE_TIMELINE_EVENTS_CAP = 200

/** :151 的 400 message。 */
export const INVALID_CREDENTIAL_ID_MESSAGE = 'invalid credential_id'
/** :159 的 503 message（数据库没配）。 */
export const DB_NOT_CONFIGURED_MESSAGE = 'database not configured'
/** :166 的 503 message（查询失败）。★ 与上面**同为 503**，靠 message 区分。 */
export const TIMELINE_UNAVAILABLE_MESSAGE = 'node-health timeline unavailable'

/** admin/node_health.go:82-90 的三个取值。 */
export const NODE_TIMELINE_EVENT_TYPES = ['failed', 'recovered', 'reconnected'] as const
export type NodeTimelineEventType = (typeof NODE_TIMELINE_EVENT_TYPES)[number]

/** ★ 强制恢复的触发来源（`admin/node_health.go:86` 的字面量比较）。 */
export const NODE_TIMELINE_FORCE_RECOVERY_TRIGGER = 'credential_recovery'

/** ★ 哨兵值：被 `firstNonEmptyPtr` 选中又被过滤掉，**客户端永远看不到**。 */
export const NODE_TIMELINE_REASON_NONE_SENTINEL = 'none'

/** ★ `observation_status` 唯一可能的取值（`:173` 写死）。 */
export const NODE_TIMELINE_OBSERVATION_STATUS = 'complete'

/** ★ 恒在的 3 个键。 */
export const NODE_EVENT_ALWAYS_KEYS = ['credential_id', 'event_type', 'occurred_at'] as const
/** ★ 三个「指针 + omitempty」条件键。 */
export const NODE_EVENT_OPTIONAL_KEYS = ['duration_ms', 'reason_code', 'note'] as const
export const NODE_TIMELINE_KEYS = ['credential_id', 'observation_status', 'events'] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/node_health.go:26-32 `nodeRecoveryEvent`。 */
export interface NodeRecoveryEvent {
  /** ★ 字符串不是数字（`strconv.FormatInt`，`:99`）。 */
  credential_id: string
  /** 三态之一；`recovered` 与 `reconnected` **都是成功**。 */
  event_type: NodeTimelineEventType
  /** ★ RFC3339**Nano**（`:99`），且优先取 `completed_at`。 */
  occurred_at: string
  /** ★ 条件键：**仅 > 0 时有键** ⇒ 0 毫秒表现为键缺失。 */
  duration_ms?: number
  /** ★ 条件键；哨兵 `"none"` 已被后端过滤，客户端看不到。 */
  reason_code?: string
  /** ★ 条件键；形态是 `"模型名 · 触发来源"`（`formatProbeNote`，`:126-140`）。 */
  note?: string
}

/** admin/node_health.go:34-38 `nodeRecoveryTimelineResponse`。 */
export interface NodeRecoveryTimelineResponse {
  /** ★ 字符串。 */
  credential_id: string
  /** ★ 硬编码 `"complete"`，取值**不可校验**（恒真）。 */
  observation_status: string
  /** ★ 恒为数组（含空数组）。 */
  events: NodeRecoveryEvent[]
}

export interface NodeTimelineQuery {
  /**
   * `24h` / `7d` / `30m` 两套语法；**超 7 天静默 clamp 到 7 天**。
   * 非法值原样发出让后端 400（客户端不静默改）。
   */
  since?: string
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 参数
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * 客户端侧的 `since` 处理，**只做 clamp，不做语法校验**。
 *
 * ★ 只对 `Nd` 形式（`^[+-]?\d+d$`）做 clamp，因为它对应后端
 *   `strings.HasSuffix(raw, "d")` 那条分支（`admin/node_health.go:60`）。
 *   其余形式（`30m` / `2h` / `1h30m` …）交给后端的 `time.ParseDuration`，
 *   客户端**不重新实现**它 —— 那套语法（可小数、可多段、可符号）复刻容易出错，
 *   而错了就会把合法值变成 400。
 * ⇒ 非法字符串**原样发出**，让后端返回它自己的 400 消息。
 */
export function nodeTimelineSinceEffective(since?: string): string {
  if (since === undefined || since === null || since === '') return NODE_TIMELINE_SINCE_DEFAULT
  const m = /^([+-]?\d+)d$/.exec(since)
  if (!m) return since
  const days = Number(m[1])
  // 后端对 days < 1 直接 400；这里不静默改，原样发出。
  if (!Number.isInteger(days) || days < 1) return since
  if (days > NODE_TIMELINE_SINCE_MAX_DAYS) return NODE_TIMELINE_SINCE_MAX
  return since
}

/** `parseTimelineSince` 的 clamp 上界（天）。 */
export const NODE_TIMELINE_SINCE_MAX_DAYS = 7

/**
 * ★ `credential_id` 必须是**正整数**，否则后端 400（`admin/node_health.go:146-151`
 * 判的是 `err != nil || credID <= 0`）。
 * ⇒ 客户端在发请求前就该拦，别把 400 当成「没数据」。
 */
export function nodeTimelineCredentialIdInvalid(id: unknown): string | null {
  if (typeof id !== 'number' || !Number.isInteger(id) || id <= 0) {
    return 'credential_id 必须是正整数，否则后端返回 400'
  }
  return null
}

/** `credential_id` 的十进制字符串形态（后端响应就是这个样子）。 */
export function nodeTimelineCredentialIdText(id: number): string {
  return String(id)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchNodeHealthTimeline(
  credentialId: number,
  q?: NodeTimelineQuery,
  options?: RequestOptions,
): Promise<NodeRecoveryTimelineResponse> {
  const since = nodeTimelineSinceEffective(q?.since)
  return req<unknown>(
    'GET',
    `/api/admin/node-health/${encodeURIComponent(nodeTimelineCredentialIdText(credentialId))}` +
      `/timeline?since=${encodeURIComponent(since)}`,
    undefined,
    options,
  ).then(unwrapNodeRecoveryTimeline)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapNodeRecoveryTimeline(resp: unknown): NodeRecoveryTimelineResponse {
  const d = requireObject(resp, '节点恢复时间线')
  requireKeys(d, NODE_TIMELINE_KEYS, '节点恢复时间线')

  // ★ 只校验类型不校验取值：`observation_status` 是硬编码常量（`:173`），
  //   断言它等于 'complete' 是**恒真判据**，写了没用。
  if (typeof d.observation_status !== 'string') {
    throw new Error('observation_status 不是字符串')
  }
  if (typeof d.credential_id !== 'string') {
    throw new Error('credential_id 不是字符串（后端用 FormatInt 转成了文本）')
  }

  const events = requireArray(d.events, '节点恢复时间线 events')
  events.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`events[${i}] 不是对象`)
    requireEventShape(row, `events[${i}]`)
  })
  return d as unknown as NodeRecoveryTimelineResponse
}

/**
 * 恒在的 3 个键必须都在；3 个条件键**不在**必检里
 * —— 它们是「指针 + omitempty」（`admin/node_health.go:29-31`），
 * 键缺失就是后端没这个值，不是契约破坏。
 */
function requireEventShape(obj: object, where: string): void {
  const d = obj as Record<string, unknown>
  const missing = NODE_EVENT_ALWAYS_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个恒在键（${missing.join(', ')}）`)
  }
  const t = String(d.event_type)
  if (!(NODE_TIMELINE_EVENT_TYPES as readonly string[]).includes(t)) {
    throw new Error(`${where} event_type 不是三个合法取值之一：${t}`)
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/** ★ 「恢复」有两个值：`recovered` 与 `reconnected`。 */
export function nodeTimelineIsRecovered(ev: NodeRecoveryEvent): boolean {
  return ev.event_type === 'recovered' || ev.event_type === 'reconnected'
}

/** ★ 哪些取值代表「成功」（= 非 `failed`）。 */
export function nodeTimelineIsSuccess(ev: NodeRecoveryEvent): boolean {
  return ev.event_type !== 'failed'
}

/**
 * ★★★ 这条直接对应「**强制恢复**」：`reconnected` 只在
 * `success && trigger_kind === 'credential_recovery'` 时产生（`:85-88`）。
 * ⇒ 统计「恢复次数」时**不能**只数 `recovered`。
 */
export function nodeTimelineIsForcedRecovery(ev: NodeRecoveryEvent): boolean {
  return ev.event_type === 'reconnected'
}

/** 失败的探测。 */
export function nodeTimelineIsFailure(ev: NodeRecoveryEvent): boolean {
  return ev.event_type === 'failed'
}

/**
 * ★ `note` 是 `formatProbeNote`（`:126-140`）拼的 `模型名 · 触发来源`，
 *   只有一半时只有一半，两半都空则**键缺失**。
 * ⇒ 想拿触发来源必须按 ` · ` 切，不能假定它一定有两个字段。
 */
export function nodeTimelineNoteModel(ev: NodeRecoveryEvent): string | null {
  if (!ev.note) return null
  const i = ev.note.indexOf(' · ')
  return i < 0 ? ev.note : ev.note.slice(0, i)
}

export function nodeTimelineNoteTrigger(ev: NodeRecoveryEvent): string | null {
  if (!ev.note) return null
  const i = ev.note.indexOf(' · ')
  return i < 0 ? ev.note : ev.note.slice(i + 3)
}

/** ★ 键缺失就是「无原因码」—— `"none"` 哨兵后端已过滤（`:105-107`）。 */
export function nodeTimelineReasonOrNull(ev: NodeRecoveryEvent): string | null {
  return ev.reason_code !== undefined && ev.reason_code !== '' ? ev.reason_code : null
}

/**
 * ★★ `duration_ms` 键缺失**不等于 0 毫秒** —— 后端只在 `> 0` 时才设指针（`:100-102`）。
 * ⇒ 缺失时只能读作「未记录」，不能读作「瞬间完成」。
 */
export function nodeTimelineDurationMs(ev: NodeRecoveryEvent): number | null {
  return typeof ev.duration_ms === 'number' ? ev.duration_ms : null
}

/** ★ `duration_ms` 缺失 ⇒ 分不清「没测到」与「0 毫秒完成」。 */
export function nodeTimelineDurationUnknown(ev: NodeRecoveryEvent): boolean {
  return ev.duration_ms === undefined
}

/**
 * ★★★ 事件可能被静默截断：上限 200（`nodeHealthTimelineCap`），
 *   SQL 取最近 200 条 ⇒ **丢的是最早的**，响应无字段可证。
 */
export function nodeTimelinePossiblyTruncated(r: NodeRecoveryTimelineResponse): boolean {
  return r.events.length >= NODE_TIMELINE_EVENTS_CAP
}

/**
 * ★★ 排序键（`started_at`）与展示键（`occurred_at`）**不是同一个字段**：
 *   `occurred_at` 优先取 `completed_at`（`:91-94`），排序按 `started_at`（`:186`）。
 * ⇒ 数组**不一定**按 `occurred_at` 升序（跨完成的探测会错位）
 *   ⇒ 要严格按时间渲染就得客户端自己排。
 */
export function nodeTimelineSortedByOccurredAt(
  r: NodeRecoveryTimelineResponse,
): NodeRecoveryEvent[] {
  return [...r.events].sort((a, b) => Date.parse(a.occurred_at) - Date.parse(b.occurred_at))
}

/** 升序是否成立（后端 `:211-213` 反转过一次，但不保证按 `occurred_at` 成立）。 */
export function nodeTimelineAscendingByOccurredAt(r: NodeRecoveryTimelineResponse): boolean {
  for (let i = 0; i < r.events.length - 1; i++) {
    const a = Date.parse(r.events[i]!.occurred_at)
    const b = Date.parse(r.events[i + 1]!.occurred_at)
    if (Number.isNaN(a) || Number.isNaN(b) || a > b) return false
  }
  return true
}

/** ★ 响应里的 `credential_id` 是否与请求的一致（能抓到后端路由错位）。 */
export function nodeTimelineMatchesCredential(
  r: NodeRecoveryTimelineResponse,
  credentialId: number,
): boolean {
  return r.credential_id === nodeTimelineCredentialIdText(credentialId)
}

/**
 * ★★ `observation_status` **恒为 `"complete"`**，而查询失败是 **503**
 *   ⇒ **「部分观测」不可表达** ⇒ 「完整」二字不能当作数据可信的证据。
 */
export function nodeTimelineIsClaimingComplete(r: NodeRecoveryTimelineResponse): boolean {
  return r.observation_status === NODE_TIMELINE_OBSERVATION_STATUS
}

/** ★ 空数组只表示「这段时间没有探测记录」，不表示「节点健康」。 */
export function nodeTimelineIsEmpty(r: NodeRecoveryTimelineResponse): boolean {
  return r.events.length === 0
}

/** 503 的两种 message（数据库没配 / 查询失败）——状态码相同，靠文案区分。 */
export function nodeTimelineUnavailableReason(message: string): 'db_not_configured' | 'query_failed' | null {
  if (message === DB_NOT_CONFIGURED_MESSAGE) return 'db_not_configured'
  if (message === TIMELINE_UNAVAILABLE_MESSAGE) return 'query_failed'
  return null
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

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}
