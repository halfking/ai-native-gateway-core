import { req, type RequestOptions } from './client'

// outputCompliance.ts — 输出合规模块的只读面（**admin 档**，AdminMiddleware）。
//   GET /api/admin/output-compliance/stats         聚合计数
//   GET /api/admin/output-compliance/records       命中记录（分页 + check_type/hit_type 过滤）
//   GET /api/admin/output-compliance/review-queue  人工复核队列（分页 + status 过滤）
//   GET /api/admin/output-compliance/feedback      反馈列表（分页 + type 过滤）
//   GET /api/admin/output-compliance/policy        当前策略（单条）
//   GET /api/admin/output-compliance/keywords      自定义违禁词清单
//
// 鉴权：`RegisterRoutes`（admin/output_compliance_handler.go:38-56）统一套
// `AdminMiddleware(fn, h.pool, h.secret)` ⇒ **admin 档**，不设 requiresRole。
// ★ 该文件注释记了一笔 2026-10-03 的安全根修：此前 8 个端点**裸挂 mux**，
//   而全局 auth 中间件对 `/api/` 前缀显式旁路 ⇒ 未认证即可读 default 租户的
//   合规记录、甚至未认证写 policy。现在全部套上了。
//
// ★ 写操作一条不碰：policy(PUT)、keywords(POST/PUT/DELETE/toggle)、
//   feedback(POST)、review-queue(approve/reject)。
//
// ⚠️⚠️⚠️⚠️⚠️ 本文件存在的头号理由：**`stats` 里有三个字段是「假数」，不是一个真值。**
//
// (1) ★★★★★ `jailbreak_hits` **恒为 0**，而且**永远会**是 0：
//     handler 写死 `"jailbreak_hits": 0`，注释自陈
//     「本仓无 jailbreak 检测器，诚实报 0，而不是编一个数出来」
//     ⇒ 0 的含义是「**没有这个检测器**」，**不是**「没有越狱问题」。
//
// (2) ★★★★★ `avg_latency_ms` **恒为 0**，同上，注释写「审计表无此列」。
//
// (3) ★★★★★ `total_checks` **不是检查次数**，它**镜像 `total_issues`**：
//     handler 注释写「暂无真实计数源（检查通过不落审计行，
//     `policies.total_checks` 列存在但全仓无递增点），先镜像 `total_issues`
//     保持字段可用」。
//     ⇒ 把 `total_checks` 说成「检查次数」会让人以为「查了 100 次只命中 3 次」，
//       实际它是「命中 3 次」被写了两遍。
//
// (4) ★★★★ `stats` 的**单路失败会静默回 0**（不是 500）：
//     `pendingReviews` 查询失败 ⇒ `slog.Warn` 后 `pendingReviews = 0`；
//     分类查询失败 ⇒ 四个变量保持零值。
//     注释自陈「stats 是聚合展示，单路失败不应 500 整个面板，
//     但也不能静默吞掉——记进日志，**面板上该字段回 0 可见异常**」。
//     ⇒ ★★ 页面上的 `0` **可能是查询失败**，不是真的 0。
//     ⇒ 所以这些 0 **不能**无条件渲染成「没有」，必须带上一句口径说明。
//
// (5) ★★★★ `stats` 只统计 `issue_type` 取值域里的**三个**：
//     `pii` / `secret` / `toxic`。
//     ★ 但写库处的取值域有**五个**（`domains/outputcompliance/checker.go` 的
//       Type 字面量：`pii / toxic / secret / internal_ip / bias`）
//     ⇒ `internal_ip` 与 `bias` 的命中**在 stats 里没有字段**。
//     ⇒ `total_issues ≥ pii_hits + secret_hits + toxicity_hits`，
//       两者之差就是这两类的命中数。页面照实说明，不假装五类齐全。
//
// ⚠️⚠️⚠️⚠️ 第二个重理由：**`records` 的 `content_preview` 是安全约束，不是「没数据」。**
//
// (6) ★★★★★ SQL 是
//     `CASE WHEN COALESCE(redacted,false) THEN left(COALESCE(redacted_output,''),120)
//      ELSE '' END`
//     ⇒ **只有引擎已经脱敏过的行才有预览**；未脱敏的行一律返回**空字符串**
//       （不是 NULL、不是原文）。
//     handler 注释写得很清楚：回显原文等于把这个列表变成
//     「把 PII/密钥原文摊平给人看」的通道，与合规模块存在的目的相反。
//     ⇒ 页面必须说清「空预览 = 该行未脱敏」，否则用户会以为引擎没记内容。
//
// ⚠️⚠️⚠️ 第三个重理由：**`keywords` 与 `review-queue` 可能整个端点 500。**
//
// (7) ★★★★★ 这两个列表把**可空列直接扫进裸 Go `string`**，且扫失败就
//     `writeInternalErrStr` + `return`（**放弃整份列表**）：
//       · `scanKeyword`（:448-452）：`description text` **可空**、
//         另外 `action` / `enabled` / `severity` 也都有 DEFAULT 但**列本身可空**
//       · `scanReviewQueueItem`（:604-611）：`session_key` / `issue_subtype` /
//         `reviewer` / `review_comment` **四列全部可空**
//     ⇒ pgx 扫 NULL 进 `*string` 会报 `cannot scan NULL into *string`
//       （本仓已三处记录同一失败模式：`domains/reportrollup/grainreport.go:305`
//       「真库 E2E 实测整个端点 500」、`domains/providerprofile/pg_profile_store.go:104/184/264`、
//       `cmd/gateway/turn_logs_aggregator_crossmonth_realdb_test.go:67`）。
//     ★★ **同族内不一致**：`records` 那一侧全部用 `COALESCE(...)` 规避了
//       （5 处），`policy` 也用 `sql.NullString` 兜了 `last_detection_at`（:223）。
//       只有 `keywords` 与 `review-queue` 没兜。
//     ⇒ 客户端的义务：**500 时不许渲染成「清单为空」**。必须显示「后端扫描失败」，
//       并说明最可能的成因（有行为空的可空列）。
//
// (8) ★★★ `review-queue` 的响应**没有 `total`**（只有 `items/status/limit/offset`），
//     而 `records` 有 `total` ⇒ 队列**无法**做「共 N 条」分页，
//     只能按 `items.length === limit` 推测还有下一页。
//
// (9) ★★★ **同一族里默认条数不同**：`review-queue` 与 `feedback` 默认 **20**，
//     `records` 默认 **50**。上限统一 200（`complianceMaxLimit`，:935）。
//     ★ `parsePagination`（:937-955）是本仓库**第四个**同族分页实现
//       （前三：pending `pageBounds` 50/500、request-anomalies `queryInt` 50/500、
//       以及更早的 sessions/list）——**数字可能一样，实现各不相同，不可互相引用**。
//     ⇒ 非数字 / ≤0 ⇒ 回落默认；>200 ⇒ **静默 clamp 到 200**；`offset<0` ⇒ 0。
//     ★ 永不报错。
//
// (10) ★★★ `review-queue` 的 `status` **查询参数没有 allowlist**：
//     空 ⇒ 默认 `pending`；传什么就按什么查 ⇒ 传 `status=xxx` **静默返回空**。
//     ★ 数据库侧 `status` 列有 `CHECK IN ('pending','approved','rejected')`
//       （migration 365）—— 但那只约束**写入**，不约束**查询**。
//     ⇒ 前端因此只发这三个字面值。
//
// (11) ★★ `records.created_at` 是**显式 `.UTC().Format(time.RFC3339)`**
//     （:917），时区确定。
//     ★ 而 `review-queue` / `keywords` 的 `CreatedAt` 是 `string`，
//       由 pgx 从 `TIMESTAMPTZ` **直接扫进字符串**，**没有** `.UTC()`。
//       具体字面格式取决于 pgx 走的编解码路径与库的 session timezone，
//       ★ **本轮没有真库可验，因此不下结论**。
//     ⇒ 客户端义务：一律走自己的格式化函数，**解析不了就原样回显**，
//       绝不假定这一族全是 UTC RFC3339。
//
// (12) ★ `policy.exception_rules` / `policy.notification_channels` 是
//     `json.RawMessage` ⇒ **形状不定**（可能是数组、对象、也可能 `null`）。
//     ⇒ 不解析成固定类型，原样透传给页面。
//
// (13) ★★★★★★ **没有策略记录时，返回的是一个「合成的默认策略」，不是 404。**
//     `fetchPolicy`（:210-219）：
//         row, err := QueryRow(...)
//         if err == pgx.ErrNoRows { return defaultOutputCompliancePolicy(tenantID), nil }
//     ⇒ 「数据库里没配」与「配了但看着像默认值」在响应里**长得一模一样**：
//       `defaultOutputCompliancePolicy` 的 `id` 是 **0**、`created_at`/`updated_at`
//       是**空字符串**、`policy_name` 是字面量 `"default"`。
//     ⇒ 判据：`id === 0` 或 `created_at === ''` ⇒ 这是**后端合成的默认策略**，
//       不是库里存的那条；页面上必须标注，否则用户会以为「这就是我们的配置」。
//
// (14) ★ `policy.llm_engine_id` / `last_detection_at` 是**指针但 JSON tag 没有
//     `omitempty`** ⇒ **键一定存在**，值可能为 `null`（不是「键缺失」）。
//
// (15) ★ 错误信封又是一种：`{"error":"Failed to …"}`（**`error` 是字符串**，
//     不是对象）—— 见 `writeInternalErrStr`（admin/internal_error.go:61-64）。
//     ⇒ 至此本族共见**四种**：`error` 是字符串 / `error.message`+`code` /
//       `error.detail` / text/plain。`api/client.ts` 的 `errorMessage` 全都兜得住。
//
// (16) ★★★★★★ **`feedback` 的响应键是 `feedback`，不是 `items`。**
//     `listFeedback`（admin/output_compliance_handler.go:711）：
//         writeJSON(w, 200, map[string]interface{}{
//             "feedback": items, "limit": limit, "offset": offset})
//     ⇒ 同一 handler 里 `review-queue`（:602）用的是 **`items`**，
//       **`feedback` 用的是 `feedback`** —— 同族两个列表端点**键不同名**。
//     ★★ 本模块第一版把这里写成了 `items`，**而当时的夹具也照着 `items` 写**
//       ⇒ 三条用例全绿，对真后端却 **100% 抛错**，还带着 bug 过一次提交。
//       这不是「判据无牙」，是**夹具验证的是「代码符合我对契约的理解」**。
//     ⇒ 由此定一条硬规矩：**每个端点至少有一条判据，其夹具里的响应键
//       必须是从后端 `writeJSON` 的 map 字面量**逐字抄**下来的，
//       并在 spec 里标出该字面量的行号。见 `outputCompliance.test.ts`
//       的「响应键必须逐字对得上 writeJSON」一组。

/** ★ `output_compliance_review_queue.status` 的**全部**合法值（migration 365 的 CHECK）。 */
export const COMPLIANCE_QUEUE_STATUSES = ['pending', 'approved', 'rejected'] as const
export type ComplianceQueueStatus = (typeof COMPLIANCE_QUEUE_STATUSES)[number]

/** ★ `output_compliance_feedback.feedback_type` 的**全部**合法值（migration 365 的 CHECK）。 */
export const COMPLIANCE_FEEDBACK_TYPES = ['false_positive', 'false_negative', 'correct'] as const
export type ComplianceFeedbackType = (typeof COMPLIANCE_FEEDBACK_TYPES)[number]

/**
 * ★ `issue_type` 的**全部**取值域，来自 `domains/outputcompliance/checker.go`
 * 的 Type 字面量（5 个）。★ 注意 `stats` **只**返回其中三个的计数（见坑 5）。
 */
export const COMPLIANCE_ISSUE_TYPES = ['pii', 'toxic', 'secret', 'internal_ip', 'bias'] as const
export type ComplianceIssueType = (typeof COMPLIANCE_ISSUE_TYPES)[number]

/** ★ `stats` **实际**提供计数的三个（handler 只查这三个）。见坑 5。 */
export const COMPLIANCE_STATS_COUNTS_BY = ['pii', 'secret', 'toxic'] as const

/** ★ 恒为 0 的两个字段（handler 写死的）。见坑 1/2。 */
export const COMPLIANCE_ALWAYS_ZERO_FIELDS = ['jailbreak_hits', 'avg_latency_ms'] as const

export function complianceFieldAlwaysZero(field: string): boolean {
  return (COMPLIANCE_ALWAYS_ZERO_FIELDS as readonly string[]).includes(field)
}

/** ★ `total_checks` 是 `total_issues` 的镜像，**不是**检查次数。见坑 3。 */
export const COMPLIANCE_TOTAL_CHECKS_MIRRORS_ISSUES = true

export const COMPLIANCE_LIMIT_MAX = 200
export const COMPLIANCE_RECORDS_DEFAULT_LIMIT = 50
export const COMPLIANCE_QUEUE_DEFAULT_LIMIT = 20
/** ★ `listFeedback` 也是 `parsePagination(r, 20, 0)` ⇒ 默认 20（与 queue 同，非 records 的 50）。 */
export const COMPLIANCE_FEEDBACK_DEFAULT_LIMIT = 20

// ── stats ──────────────────────────────────────────────────────────────────

export interface ComplianceStats {
  total_issues: number
  blocked: number
  /** ★ 查不到时**静默回 0**（不是 500）。见坑 4。 */
  pending_reviews: number
  /** ★★ **恒等于 `total_issues`**，不是「检查次数」。见坑 3。 */
  total_checks: number
  pii_hits: number
  secret_hits: number
  toxicity_hits: number
  /** ★★ 恒为 0（本仓无 jailbreak 检测器）。见坑 1。 */
  jailbreak_hits: number
  /** ★★ 恒为 0（审计表无此列）。见坑 2。 */
  avg_latency_ms: number
  /** 空表时是**空字符串**（不是 null、不是缺键）。 */
  last_updated: string
}

export function fetchComplianceStats(options?: RequestOptions): Promise<ComplianceStats> {
  return req<unknown>('GET', '/api/admin/output-compliance/stats', undefined, options).then(unwrapComplianceStats)
}

export function unwrapComplianceStats(resp: unknown): ComplianceStats {
  if (resp && typeof resp === 'object' && typeof (resp as ComplianceStats).total_issues === 'number') {
    return resp as ComplianceStats
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`output-compliance/stats 响应形状不符：期望 {total_issues, blocked, pending_reviews, …}，实得 ${actual}`)
}

// ── records（命中记录） ─────────────────────────────────────────────────────

export interface ComplianceRecord {
  id: number
  /** 可能为空串（DB 里 `session_key` 可空，SQL 用 COALESCE 兜成 ''）。 */
  session_id: string
  check_type: string
  hit_type: string
  severity: number
  redacted: boolean
  /**
   * ★★★★ **只有 `redacted === true` 才有内容**；否则是**空字符串**。
   * 见坑 6 —— 这是安全约束，不是「没数据」。
   */
  content_preview: string
  /** ★ 显式 `.UTC().Format(time.RFC3339)`。见坑 11。 */
  created_at: string
}

export interface ComplianceRecordsResponse {
  records: ComplianceRecord[]
  total: number
  limit: number
  offset: number
}

export interface ComplianceRecordsParams {
  checkType?: string
  hitType?: string
  limit?: number
  offset?: number
}

export function fetchComplianceRecords(
  params: ComplianceRecordsParams = {},
  options?: RequestOptions,
): Promise<ComplianceRecordsResponse> {
  const qs = new URLSearchParams()
  const ct = (params.checkType ?? '').trim()
  if (ct !== '') qs.set('check_type', ct)
  const ht = (params.hitType ?? '').trim()
  if (ht !== '') qs.set('hit_type', ht)
  appendPage(qs, params.limit, params.offset)
  return req<unknown>('GET', `/api/admin/output-compliance/records${q(qs)}`, undefined, options).then(unwrapComplianceRecords)
}

export function unwrapComplianceRecords(resp: unknown): ComplianceRecordsResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as ComplianceRecordsResponse).records)) {
    return resp as ComplianceRecordsResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`output-compliance/records 响应形状不符：期望 {records:[…], total, limit, offset}，实得 ${actual}`)
}

// ── review-queue（复核队列） ────────────────────────────────────────────────

export interface ComplianceQueueItem {
  id: number
  audit_id: number
  request_id: string
  /** ★ 可空列被裸扫 ⇒ 一行为空会让**整个端点 500**。见坑 7。 */
  session_key?: string
  issue_type: string
  /** ★ 同上。 */
  issue_subtype?: string
  severity: number
  status: string
  /** ★ 同上。 */
  reviewer?: string
  /** ★ 同上。 */
  review_comment?: string
  /** ★ pgx 直接扫进字符串，**未** `.UTC()`；格式未实测。见坑 11。 */
  created_at: string
  /** `*string` + omitempty ⇒ 可能缺键。 */
  reviewed_at?: string
}

export interface ComplianceQueueResponse {
  items: ComplianceQueueItem[]
  /** ★ 后端回显的过滤值。 */
  status: string
  limit: number
  offset: number
  /** ★★ 注意：响应里**没有** `total`。见坑 8。 */
}

export function fetchComplianceReviewQueue(
  params: { status?: ComplianceQueueStatus; limit?: number; offset?: number } = {},
  options?: RequestOptions,
): Promise<ComplianceQueueResponse> {
  const qs = new URLSearchParams()
  // ★ 只发 CHECK 允许的三个字面值；发别的会静默返回空（见坑 10）。
  if (params.status && (COMPLIANCE_QUEUE_STATUSES as readonly string[]).includes(params.status)) {
    qs.set('status', params.status)
  }
  appendPage(qs, params.limit, params.offset)
  return req<unknown>('GET', `/api/admin/output-compliance/review-queue${q(qs)}`, undefined, options).then(
    unwrapComplianceQueue,
  )
}

export function unwrapComplianceQueue(resp: unknown): ComplianceQueueResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as ComplianceQueueResponse).items)) {
    return resp as ComplianceQueueResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`output-compliance/review-queue 响应形状不符：期望 {items:[…], status, limit, offset}，实得 ${actual}`)
}

// ── feedback ───────────────────────────────────────────────────────────────

export interface ComplianceFeedback {
  id: number
  audit_id: number
  feedback_type: string
  /** ★ 可空（`reporter VARCHAR(255)`），被裸扫 ⇒ NULL 会 500。见坑 7 同理。 */
  reporter?: string
  comment?: string
  created_at: string
}

/**
 * ★★★ 响应键是 **`feedback`**，**不是** `items`。
 *
 * 后端 `listFeedback` 的 `writeJSON` 字面量
 * （`admin/output_compliance_handler.go:711`）：
 *
 *     writeJSON(w, http.StatusOK, map[string]interface{}{
 *         "feedback": items, "limit": limit, "offset": offset})
 *
 * ⇒ 同一 handler 里 `review-queue` 用的是 `items`（:602），
 *   **`feedback` 用的是 `feedback`** —— 同族两个列表端点的键**不同名**。
 * ★ 本模块第一版把这里写成了 `items`，而当时的夹具也照着 `items` 写
 *   ⇒ 三条用例全绿，**对着真后端却 100% 抛错**。见坑 16。
 */
export interface ComplianceFeedbackResponse {
  feedback: ComplianceFeedback[]
  limit: number
  offset: number
}

export function fetchComplianceFeedback(
  params: { type?: ComplianceFeedbackType; limit?: number; offset?: number } = {},
  options?: RequestOptions,
): Promise<ComplianceFeedbackResponse> {
  const qs = new URLSearchParams()
  if (params.type && (COMPLIANCE_FEEDBACK_TYPES as readonly string[]).includes(params.type)) {
    qs.set('type', params.type)
  }
  appendPage(qs, params.limit, params.offset)
  return req<unknown>('GET', `/api/admin/output-compliance/feedback${q(qs)}`, undefined, options).then(unwrapComplianceFeedback)
}

export function unwrapComplianceFeedback(resp: unknown): ComplianceFeedbackResponse {
  // ★ 认 `feedback` 键（后端 :711 的 map 字面量），**不**认 `items` ——
  //   `items` 是 review-queue（:602）的键，串了就会对真响应 100% 抛错。
  if (resp && typeof resp === 'object' && Array.isArray((resp as ComplianceFeedbackResponse).feedback)) {
    return resp as ComplianceFeedbackResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`output-compliance/feedback 响应形状不符：期望 {feedback:[…], limit, offset}，实得 ${actual}`)
}

// ── policy（当前策略，单条） ────────────────────────────────────────────────

/**
 * 策略共 60+ 字段，这里**只声明页面要用的那些**。
 *
 * ★ `exception_rules` / `notification_channels` 是 `json.RawMessage` ⇒
 *   **形状不定**（可能是数组、对象，也可能 `null`）⇒ 原样透传，不解析成定类型。
 * ★ `llm_engine_id` / `last_detection_at` 是指针 ⇒ 键可能整个不存在。
 */
export interface CompliancePolicy {
  id: number
  policy_name: string
  enabled: boolean
  enforcement_mode: string
  /** ★ 键**一定存在**，值可能为 `null`（`*int` 且 JSON tag 无 `omitempty`）。见坑 14。 */
  llm_engine_id: number | null
  check_pii: boolean
  check_toxicity: boolean
  check_bias: boolean
  check_hallucination: boolean
  check_secrets: boolean
  check_internal_ip: boolean
  check_jailbreak_response: boolean
  check_instruction_injection_response: boolean
  pii_threshold: number
  toxicity_threshold: number
  bias_threshold: number
  hallucination_threshold: number
  secrets_threshold: number
  internal_ip_threshold: number
  /** ★ 量纲 **0..1**（阈值）⇒ 展示要 ×100。与 stats 的整数计数完全不同。 */
  auto_redact: boolean
  redact_email: boolean
  redact_phone: boolean
  redact_id_card: boolean
  redact_credit_card: boolean
  redact_bank_card: boolean
  redact_jwt: boolean
  redact_password: boolean
  toxic_replacement: string
  block_message: string
  strict_mode: boolean
  /**
   * ★ 列是 `text[] DEFAULT '{}'`（baseline 01-schema.sql），SQL 里
   *   `COALESCE(whitelist_keywords, '{}')` 也被推断成 `text[]`
   *   ⇒ **永远是数组**（可能为空数组），不会是对象、不会是 null。
   *   （我第一版写过「COALESCE 兜成 '{}' 可能是对象」，追到列类型才发现是错的。）
   */
  whitelist_keywords: string[]
  /** ★ `json.RawMessage` ⇒ 形状不定。 */
  exception_rules?: unknown
  /** ★ 同上。 */
  notification_channels?: unknown
  realtime_alert_enabled: boolean
  alert_threshold_severity: number
  alert_aggregation_window_minutes: number
  /** ★ 量纲 **0..1**。 */
  sampling_rate: number
  auto_review_queue_enabled: boolean
  feedback_loop_enabled: boolean
  skill_generation_enabled: boolean
  auto_threshold_tuning_enabled: boolean
  retention_days: number
  total_detections: number
  total_blocks: number
  /** ★ 键**一定存在**，值可能为 `null`。见坑 14。 */
  last_detection_at: string | null
  created_at: string
  updated_at: string
}

/** 策略里的**五个**阈值字段（全都是 0..1 比率）。 */
export const COMPLIANCE_THRESHOLD_FIELDS = [
  'pii_threshold',
  'toxicity_threshold',
  'bias_threshold',
  'hallucination_threshold',
  'secrets_threshold',
  'internal_ip_threshold',
] as const

/** 0..1 比率 → 百分数。★ 刻意不同名于 `formatRoutingOptAccuracy`（那是准确率）。 */
export function formatComplianceThreshold(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  return (v * 100).toFixed(2) + '%'
}

export function fetchCompliancePolicy(options?: RequestOptions): Promise<CompliancePolicy> {
  return req<unknown>('GET', '/api/admin/output-compliance/policy', undefined, options).then(unwrapCompliancePolicy)
}

export function unwrapCompliancePolicy(resp: unknown): CompliancePolicy {
  // ★ 收紧到**只接受对象**：handler 的 `writeJSON(w, 200, policy)` 传的是
  //   `*OutputCompliancePolicy`，源码实测**永远不会返回数组**。
  //   「宽容地接受单元素数组」会掩盖真实的后端形状变更。
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) return resp as CompliancePolicy
  const actual = resp === null ? 'null' : Array.isArray(resp) ? `array(len=${resp.length})` : typeof resp
  throw new Error(`output-compliance/policy 响应形状不符：期望单条策略对象，实得 ${actual}`)
}

/**
 * ★ 这个响应是不是后端**合成的默认策略**（库里没有配置行）？见坑 13。
 *
 * 判据（两个独立信号，任一成立即判定）：
 *   · `id === 0` —— 真实行的 `id` 是 SERIAL，一定 > 0；
 *     `defaultOutputCompliancePolicy` 从不设置 ID ⇒ 0。
 *   · `created_at === ''` —— 默认策略的时间字段是零值字符串。
 */
export function compliancePolicyIsSyntheticDefault(p: CompliancePolicy): boolean {
  return p.id === 0 || p.created_at === ''
}

// ── keywords（自定义违禁词） ────────────────────────────────────────────────

export interface ComplianceKeyword {
  id: number
  keyword: string
  category: string
  /** 1..10（DB CHECK）。 */
  severity: number
  /** `log` / `warn` / `redact` / `block`（DB CHECK）。 */
  action: string
  enabled: boolean
  /** ★ 可空列被裸扫 ⇒ NULL 会让**整个端点 500**。见坑 7。 */
  description?: string
  created_at: string
  updated_at: string
}

export interface ComplianceKeywordsResponse {
  keywords: ComplianceKeyword[]
}

export function fetchComplianceKeywords(
  params: { category?: string } = {},
  options?: RequestOptions,
): Promise<ComplianceKeywordsResponse> {
  const qs = new URLSearchParams()
  const c = (params.category ?? '').trim()
  if (c !== '') qs.set('category', c)
  return req<unknown>('GET', `/api/admin/output-compliance/keywords${q(qs)}`, undefined, options).then(
    unwrapComplianceKeywords,
  )
}

export function unwrapComplianceKeywords(resp: unknown): ComplianceKeywordsResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as ComplianceKeywordsResponse).keywords)) {
    return resp as ComplianceKeywordsResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`output-compliance/keywords 响应形状不符：期望 {keywords:[…]}，实得 ${actual}`)
}

// ── 内部小工具 ─────────────────────────────────────────────────────────────

function q(qs: URLSearchParams): string {
  const s = qs.toString()
  return s ? '?' + s : ''
}

/**
 * 分页参数只在合法区间内发：>200 会被后端**静默 clamp**（不是报错），
 * 非数字 / ≤0 会**回落默认**。见坑 9。
 */
function appendPage(qs: URLSearchParams, limit?: number, offset?: number): void {
  if (typeof limit === 'number' && Number.isFinite(limit)) {
    const n = Math.trunc(limit)
    if (n > 0 && n <= COMPLIANCE_LIMIT_MAX) qs.set('limit', String(n))
  }
  if (typeof offset === 'number' && Number.isFinite(offset)) {
    const n = Math.trunc(offset)
    if (n > 0) qs.set('offset', String(n))
  }
}