import { req, type RequestOptions } from './client'

// promptInjection.ts — 提示词注入（prompt-injection）模块的只读面。
//   GET /api/admin/prompt-injection/stats             统计（读**预聚合表**）
//   GET /api/admin/prompt-injection/detections        检测记录（page + page_size）
//   GET /api/admin/prompt-injection/attack-vectors    攻击向量（page + page_size，**无 total**）
//   GET /api/admin/prompt-injection/policy            策略（单条）
//   GET /api/admin/prompt-injection/rules             规则清单（**无分页**）
//   GET /api/admin/prompt-injection/engines           LLM 引擎清单（**无分页**）
//   GET /api/admin/prompt-injection/severity-matrix   严重度处置矩阵
//   GET /api/admin/prompt-injection/canary-tokens     蜜罐 token 清单（**无分页**）
//
// 鉴权：`RegisterRoutes`（admin/prompt_injection_handler.go:33-72）统一套
// `AdminMiddleware` ⇒ **admin 档**，不设 requiresRole。
//
// ★ 写操作一条不碰：policy(PUT)、rules(POST/PUT/DELETE/toggle)、
//   engines(POST/PUT/DELETE/test)、severity-matrix(PUT)、canary-tokens(POST/PUT/DELETE)。
//
// ⚠️⚠️⚠️⚠️ 这一族最阴的一条：**三个列表端点的分页形状互不相同，
// 而后两个根本不接受分页。**
//
// (1) ★★★★ **`rules` / `engines` / `canary-tokens` 完全没有分页参数。**
//     SQL 里没有 `LIMIT`/`OFFSET`，响应是 `{"rules":[…], "count": len(rules)}`。
//     ⇒ `count` 是**本次返回的条数**，不是「总共有多少」。
//     ⇒ 前端**不提供**翻页控件，也不把 `count` 说成总数。
//
// (2) ★★★★ `detections` 用的是 **`page` + `page_size`**，不是 `limit`/`offset`：
//         page, _ := Atoi(page); if page < 1 { page = 1 }
//         pageSize, _ := Atoi(page_size)
//         if pageSize < 1 || pageSize > 100 { pageSize = 20 }
//     ★ `page_size` 越界是**回落默认 20**，**不是** clamp 到 100
//       （与 output-compliance / pending / request-anomalies 的 clamp 语义**相反**）。
//     ⇒ 前端只在 1..100 内发送。
//     ⇒ 响应 `{detections, page, page_size, total}`，其中 `total` 是
//       `COUNT(*)` 的**真总数**（含全部过滤条件）。
//
// (3) ★★★★ `attack-vectors` 的 `page`/`page_size` 规则与 detections **相同**，
//     但响应是 `{vectors, page, page_size}` —— **没有 `total`**！
//     ⇒ 无法做「共 N 条」，只能按「这页排满」近似（见 `vectorPageLooksFull`）。
//
// ⚠️⚠️⚠️ 第二条：**`enabled` / `blocked` 的真值判定是 `== "true"`。**
//
// (4) ★★★★ `listRules`（:322-325）与 `handleDetections`（:551-554）都是：
//         if enabled != "" { query += " AND enabled = $n"; args = append(args, enabled == "true") }
//     ⇒ `?enabled=1` / `?enabled=yes` / `?enabled=TRUE `（带空格）都判为 **false**
//       ⇒ 会筛出**恰好相反**的结果，而且**不报错**。
//     ⇒ 前端因此**只发字面量 `true` / `false`**，绝不发 `1`/`yes`。
//
// ⚠️⚠️ 第三条：**`stats` 没有统计行时返回全 0，而不是 404。**
//
// (5) ★★★★ `handleStats`（:637-670）读的是**预聚合表**
//     `prompt_injection_stats_enhanced`，且：
//         if err == pgx.ErrNoRows { stats = &DetectionStats{} }
//     ⇒ 「这张表里没有本租户的行」与「统计值全是 0」在响应里**长得一模一样**。
//     ★ 与 output-compliance 的「合成默认策略」是**同一形态**的问题。
//     ★ 而且它是**后台刷新的预聚合表** ⇒ 与 `detections` 的实时读法**存在延迟**，
//       两个面板的数字天然可能对不上。
//     ⇒ 页面据此标注 stats 的来源是聚合表。
//
// ⚠️ 第四~九条
//
// (6) ★★★ `rules` 的 `category` 过滤是**两列 OR**：
//     `AND (category = $n OR category_new::text = $n)`
//     ⇒ 新旧两套分类字段任一命中即可 ⇒ 页面两个分类列都要显示。
//     ★ 而 `detections` 的 `category` 是 `$n = ANY(categories)`（**数组包含**）——
//       同一个参数名，两族端点语义完全不同。
// (7) ★★★ `rules` 的 `search` 是 `ILIKE '%q%'` ⇒ **子串 + 不分大小写**
//     （匹配 `rule_name` 或 `description`）；`detections` **没有** search。
//     ★ `rules` 的 `type` 是精确匹配（大小写敏感）。
// (8) ★★★ `severity-matrix` 的 `notify_channels` 是
//     `COALESCE(notify_channels,'[]')` 取出**字符串**后在 Go 里 `jsoncol.Decode`，
//     ★★ 而 **`jsoncol.Decode` 的返回值被丢弃**（:985）—— 那一列内容不是合法 JSON 时
//     静默失败，`NotifyChannels` 保持零值（空数组）⇒ 页面会显示「没有通知渠道」，
//     而真相是「这一列解析失败」。客户端无法区分，只能照实显示。
// (9) ★★ `severity-matrix` 的排序是
//     `CASE severity_level WHEN 'low' THEN 1 … WHEN 'critical' THEN 4`
//     ⇒ **只认这四个字面值**；其它值排序键为 NULL，会被排到**最后**。
// (10) ★★ `detections` 的 `total` 用
//      `strings.Replace(query, "<SELECT 段原文>", "SELECT COUNT(*)", 1)` 拼出来
//      —— 极脆（依赖 SELECT 子串逐字匹配），但对客户端不可见，只作为记录。
// (11) ★ `avg_score` / `avg_llm_confidence` 是 `COALESCE(…,0)` ⇒ 无数据是 **0**。
// (12) ★ 指针字段（键一定存在，值可能 `null`）：`detections.llm_confidence`、
//      `engines.model_canonical_id` / `credential_id` / `last_called_at`、
//      `attack-vectors.detected_at`、`canary-tokens.expires_at` / `last_leaked_at`。
//      ★ `engines.model_name` 来自 `LEFT JOIN models_canonical` ⇒
//        `model_canonical_id` 为 null 时是**空串**（不是 null）。
// (13) ★ 错误信封是 `{"error":"…"}`（**字符串**，与 output-compliance 同族），
//      或 `writeInternalErrStr` 的 `{"error": op}`。

// ⚠️★★★★★ 第十条，也是本族最隐蔽的**量纲**陷阱：
//
// (14) ★★★★★ `detections.risk_level` 在库里是 **`integer`（CHECK 1..10）**，
//     而 Go 结构体把它声明成 `string` ⇒ pgx 把 int 格式化成文本，
//     ⇒ **响应里 `risk_level` 是 `"7"` 这种数字字符串，不是等级名**。
//     ★ 页面绝不能把它当 low/medium/high 那种分档来渲染。
//     ★ 同一张表里 `detection_score` 也是整数（另一个刻度）——
//       两个数字含义不同：score 是检测分，risk_level 是 1..10 的风险级别。
//     ★ 而 `severity_action_matrix.severity_level` **才是**字面量分档
//       （VARCHAR + CHECK），取值只有 low/medium/high/critical 四个 ——
//       **同一个概念在两张表里一个是数字、一个是单词**。
//
// 以下两个常量**照抄** schema 的 enum/CHECK，不是猜的：
//   · public.injection_category（15 个值）——`rules.category_new` 与
//     `detections.categories` 都是它。
//   · public.injection_action（11 个值）——`severity_action_matrix` 的
//     observe_action / enforce_action 与 `canary_tokens.leak_action` 都是它。

/** ★ `severity_action_matrix.severity_level` 的全部合法值（DB CHECK 照抄）。 */
export const INJECTION_SEVERITY_LEVELS = ['low', 'medium', 'high', 'critical'] as const
export type InjectionSeverityLevel = (typeof INJECTION_SEVERITY_LEVELS)[number]

/** ★ `public.injection_category` enum 的**全部** 15 个值（schema 照抄）。 */
export const INJECTION_CATEGORIES = [
  'role_hijack',
  'instruction_override',
  'instruction_leak',
  'jailbreak',
  'encoding_bypass',
  'injection_marker',
  'multi_turn_attack',
  'resource_exhaustion',
  'data_exfiltration',
  'social_engineering',
  'prompt_leaking',
  'payload_smuggling',
  'unicode_obfuscation',
  'context_manipulation',
  'tool_abuse',
] as const
export type InjectionCategory = (typeof INJECTION_CATEGORIES)[number]

/** ★ `public.injection_action` enum 的**全部** 11 个值（schema 照抄）。 */
export const INJECTION_ACTIONS = [
  'pass',
  'log',
  'warn',
  'replace',
  'redact',
  'remove',
  'reject',
  'terminate',
  'approve',
  'quarantine',
  'block',
] as const
export type InjectionAction = (typeof INJECTION_ACTIONS)[number]

/**
 * ★ `risk_level` 是「1..10 的数字字符串」，**不是**等级名。见坑 14。
 *
 * 返回 `null` 表示响应形状不对（不是数字、越界、非字符串）——
 * 调用方据此显示「—」，**绝不**把它当等级名硬套一个分档。
 */
export function parseInjectionRiskLevel(v: string | null | undefined): number | null {
  if (typeof v !== 'string') return null
  const n = Number(v.trim())
  if (!Number.isFinite(n) || !Number.isInteger(n) || n < 1 || n > 10) return null
  return n
}

/** ★ 后端 `page_size` 越界是**回落默认 20**，不是 clamp。见坑 2。 */
export const INJECTION_PAGE_SIZE_DEFAULT = 20
export const INJECTION_PAGE_SIZE_MAX = 100

// ── stats ──────────────────────────────────────────────────────────────────

export interface InjectionStats {
  total_detections: number
  blocked_count: number
  critical_count: number
  high_count: number
  medium_count: number
  low_count: number
  approval_count: number
  replaced_count: number
  terminated_count: number
  canary_leak_count: number
  /** ★ `COALESCE(avg_score,0)` ⇒ 无数据是 0，不是 null。见坑 11。 */
  avg_score: number
  max_score: number
  /** ★ 同上。 */
  avg_llm_confidence: number
  affected_sessions: number
}

export function fetchInjectionStats(options?: RequestOptions): Promise<InjectionStats> {
  return req<unknown>('GET', '/api/admin/prompt-injection/stats', undefined, options).then(unwrapInjectionStats)
}

export function unwrapInjectionStats(resp: unknown): InjectionStats {
  // ★ `ErrNoRows` 时后端返回的是全 0 对象，所以「全是 0」是**合法响应**，
  //   绝不能当成「没数据」抛错。判据只能是 `total_detections` 是数字。
  if (resp && typeof resp === 'object' && typeof (resp as InjectionStats).total_detections === 'number') {
    return resp as InjectionStats
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/stats 响应形状不符：期望 {total_detections, blocked_count, …}，实得 ${actual}`)
}

// ── detections ─────────────────────────────────────────────────────────────

export interface InjectionDetection {
  id: number
  request_id: string
  session_key: string
  detected_at: string
  detection_score: number
  /**
   * ★★ 库列是 `integer`（CHECK 1..10），但 Go 侧是 `string`
   * ⇒ 响应里是 `"7"` 这种**数字字符串**，不是等级名。见坑 14。
   * ⇒ 一律用 `parseInjectionRiskLevel` 解析，别当分档名用。
   */
  risk_level: string
  /** 逗号连接的规则名（与 `matched_rules_count` 配套）。 */
  matched_rules: string
  matched_rules_count: number
  action_taken: string
  blocked: boolean
  /** ★ 上游/用户输入里的命中片段 —— 可能含敏感内容，展示前先想清楚。 */
  evidence_text: string
  categories: string[]
  /** ★ 键一定存在，值可能 `null`（`*float64`）。见坑 12。 */
  llm_confidence: number | null
  llm_reason: string
  canary_token_leaked: string
  approval_id: string
  replaced_content: string
  client_ip: string
  user_agent: string
}

export interface InjectionDetectionsResponse {
  detections: InjectionDetection[]
  page: number
  page_size: number
  /** ★ `COUNT(*)` 的真总数（含全部过滤条件）。见坑 2。 */
  total: number
}

export interface InjectionDetectionsParams {
  riskLevel?: string
  action?: string
  sessionKey?: string
  /** ★ 语义是 `$n = ANY(categories)`（数组包含）。见坑 6。 */
  category?: string
  /** ★ 只发 `true` / `false` 字面量。见坑 4。 */
  blocked?: boolean
  page?: number
  pageSize?: number
}

export function fetchInjectionDetections(
  params: InjectionDetectionsParams = {},
  options?: RequestOptions,
): Promise<InjectionDetectionsResponse> {
  const qs = new URLSearchParams()
  const add = (k: string, v: string) => {
    if (v !== '') qs.set(k, v)
  }
  add('risk_level', (params.riskLevel ?? '').trim())
  add('action', (params.action ?? '').trim())
  add('session_key', (params.sessionKey ?? '').trim())
  add('category', (params.category ?? '').trim())
  // ★★ 后端判定是 `enabled == "true"`，所以只发这两个字面量。
  if (params.blocked === true) qs.set('blocked', 'true')
  else if (params.blocked === false) qs.set('blocked', 'false')
  const p = normPage(params.page)
  const ps = normPageSize(params.pageSize)
  qs.set('page', String(p))
  qs.set('page_size', String(ps))
  return req<unknown>('GET', `/api/admin/prompt-injection/detections?${qs.toString()}`, undefined, options).then(
    unwrapInjectionDetections,
  )
}

export function unwrapInjectionDetections(resp: unknown): InjectionDetectionsResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as InjectionDetectionsResponse).detections)) {
    return resp as InjectionDetectionsResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/detections 响应形状不符：期望 {detections:[…], page, page_size, total}，实得 ${actual}`)
}

// ── attack-vectors ─────────────────────────────────────────────────────────

export interface AttackVector {
  id: number
  attack_text: string
  attack_hash: string
  categories: string[]
  severity: number
  source: string
  request_id: string
  /** ★ 键一定存在，值可能 `null`。见坑 12。 */
  detected_at: string | null
  created_at: string
}

export interface AttackVectorsResponse {
  vectors: AttackVector[]
  page: number
  page_size: number
  /** ★★ 响应里**没有** `total`。见坑 3。 */
}

export function fetchAttackVectors(
  params: { page?: number; pageSize?: number } = {},
  options?: RequestOptions,
): Promise<AttackVectorsResponse> {
  const qs = new URLSearchParams()
  qs.set('page', String(normPage(params.page)))
  qs.set('page_size', String(normPageSize(params.pageSize)))
  return req<unknown>('GET', `/api/admin/prompt-injection/attack-vectors?${qs.toString()}`, undefined, options).then(
    unwrapAttackVectors,
  )
}

export function unwrapAttackVectors(resp: unknown): AttackVectorsResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as AttackVectorsResponse).vectors)) {
    return resp as AttackVectorsResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/attack-vectors 响应形状不符：期望 {vectors:[…], page, page_size}，实得 ${actual}`)
}

/**
 * ★ 这一页是否「看起来满了」——**近似**，不是精确的「还有更多」。
 *
 * 因为响应没有 `total`（见坑 3），这是**唯一**可得的信号。
 * 页面必须照实说「可能还有」，不能说「共 N 条」。
 */
export function vectorPageLooksFull(pageSize: number, returned: number): boolean {
  return returned > 0 && returned >= normPageSize(pageSize)
}

// ── rules（无分页） ────────────────────────────────────────────────────────

export interface InjectionRule {
  id: number
  rule_name: string
  rule_type: string
  /** 旧分类字段。 */
  category: string
  /** 新分类字段（`COALESCE(category_new::text,'')`）。见坑 6。 */
  category_new: string
  pattern: string
  description: string
  severity: number
  enabled: boolean
  case_sensitive: boolean
  /** `COALESCE(is_system, true)` ⇒ 缺值时**按 true**。 */
  is_system: boolean
  /** `COALESCE(action_override::text,'')` ⇒ 空串表示「沿用矩阵处置」。 */
  action_override: string
  tags: string[]
  examples: string[]
  created_at: string
  updated_at: string
}

export interface InjectionRulesResponse {
  rules: InjectionRule[]
  /** ★ 是**本次返回的条数**（SQL 无 LIMIT），不是总数。见坑 1。 */
  count: number
}

export interface InjectionRulesParams {
  /** 精确匹配，大小写敏感。 */
  type?: string
  /** ★ 两列 OR：`category` 或 `category_new`。见坑 6。 */
  category?: string
  /** ★ 只发 `true` / `false`。见坑 4。 */
  enabled?: boolean
  /** ★ `ILIKE '%q%'` ⇒ 子串 + 不分大小写，匹配名称与描述。见坑 7。 */
  search?: string
}

export function fetchInjectionRules(
  params: InjectionRulesParams = {},
  options?: RequestOptions,
): Promise<InjectionRulesResponse> {
  const qs = new URLSearchParams()
  const ty = (params.type ?? '').trim()
  if (ty !== '') qs.set('type', ty)
  const cat = (params.category ?? '').trim()
  if (cat !== '') qs.set('category', cat)
  if (params.enabled === true) qs.set('enabled', 'true')
  else if (params.enabled === false) qs.set('enabled', 'false')
  const se = (params.search ?? '').trim()
  if (se !== '') qs.set('search', se)
  const s = qs.toString()
  // ★ 注意这里**没有**任何分页参数 —— 后端也不接受。
  return req<unknown>('GET', `/api/admin/prompt-injection/rules${s ? '?' + s : ''}`, undefined, options).then(
    unwrapInjectionRules,
  )
}

export function unwrapInjectionRules(resp: unknown): InjectionRulesResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as InjectionRulesResponse).rules)) {
    return resp as InjectionRulesResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/rules 响应形状不符：期望 {rules:[…], count}，实得 ${actual}`)
}

// ── engines（无分页） ──────────────────────────────────────────────────────

export interface InjectionEngine {
  id: number
  engine_name: string
  description: string
  /** ★ 键一定存在，值可能 `null`。 */
  model_canonical_id: number | null
  /** ★ 来自 `LEFT JOIN models_canonical`，未关联时是**空串**（不是 null）。见坑 12。 */
  model_name: string
  /** ★ 键一定存在，值可能 `null`。 */
  credential_id: number | null
  temperature: number
  max_tokens: number
  timeout_ms: number
  max_retries: number
  system_prompt: string
  detection_prompt: string
  priority: number
  enabled: boolean
  total_calls: number
  total_detections: number
  avg_latency_ms: number
  error_count: number
  /** ★ 键一定存在，值可能 `null`。 */
  last_called_at: string | null
  created_at: string
  updated_at: string
}

export interface InjectionEnginesResponse {
  engines: InjectionEngine[]
  /** ★ 本次返回的条数，不是总数。见坑 1。 */
  count: number
}

export function fetchInjectionEngines(options?: RequestOptions): Promise<InjectionEnginesResponse> {
  return req<unknown>('GET', '/api/admin/prompt-injection/engines', undefined, options).then(unwrapInjectionEngines)
}

export function unwrapInjectionEngines(resp: unknown): InjectionEnginesResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as InjectionEnginesResponse).engines)) {
    return resp as InjectionEnginesResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/engines 响应形状不符：期望 {engines:[…], count}，实得 ${actual}`)
}

// ── severity-matrix ────────────────────────────────────────────────────────

export interface SeverityAction {
  id: number
  severity_level: string
  observe_action: string
  enforce_action: string
  require_approval: boolean
  approval_timeout_minutes: number
  notify_on_detect: boolean
  /**
   * ★★ 由 `jsoncol.Decode` 解析，而**它的返回值被丢弃** ⇒ 内容不是合法 JSON 时
   * 静默变成空数组。见坑 8。客户端**无法**区分「没有渠道」与「解析失败」。
   */
  notify_channels: string[]
  affect_session_health: boolean
  session_health_penalty: number
  terminate_on_repeat: boolean
  repeat_threshold: number
}

export interface SeverityMatrixResponse {
  matrix: SeverityAction[]
}

export function fetchSeverityMatrix(options?: RequestOptions): Promise<SeverityMatrixResponse> {
  return req<unknown>('GET', '/api/admin/prompt-injection/severity-matrix', undefined, options).then(unwrapSeverityMatrix)
}

export function unwrapSeverityMatrix(resp: unknown): SeverityMatrixResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as SeverityMatrixResponse).matrix)) {
    return resp as SeverityMatrixResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/severity-matrix 响应形状不符：期望 {matrix:[…]}，实得 ${actual}`)
}

// ── canary-tokens（无分页） ────────────────────────────────────────────────

export interface CanaryToken {
  id: number
  /** ★ 蜜罐 token 的真实值 —— 是**诱饵凭据**，不是用户凭据。 */
  token_value: string
  token_type: string
  token_name: string
  description: string
  leak_action: string
  notify_on_leak: boolean
  active: boolean
  /** ★ 键一定存在，值可能 `null`。 */
  expires_at: string | null
  times_injected: number
  times_leaked: number
  /** ★ 键一定存在，值可能 `null`。 */
  last_leaked_at: string | null
  created_at: string
}

export interface CanaryTokensResponse {
  tokens: CanaryToken[]
  /** ★ 本次返回的条数，不是总数。见坑 1。 */
  count: number
}

export function fetchCanaryTokens(options?: RequestOptions): Promise<CanaryTokensResponse> {
  return req<unknown>('GET', '/api/admin/prompt-injection/canary-tokens', undefined, options).then(unwrapCanaryTokens)
}

export function unwrapCanaryTokens(resp: unknown): CanaryTokensResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as CanaryTokensResponse).tokens)) {
    return resp as CanaryTokensResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`prompt-injection/canary-tokens 响应形状不符：期望 {tokens:[…], count}，实得 ${actual}`)
}

// ── 内部小工具 ─────────────────────────────────────────────────────────────

/** `page < 1` ⇒ 1（非数字也回落 1）。 */
function normPage(p?: number): number {
  if (typeof p !== 'number' || !Number.isFinite(p)) return 1
  const n = Math.trunc(p)
  return n < 1 ? 1 : n
}

/**
 * ★ `page_size` 越界是**回落默认 20**，**不是** clamp 到 100（见坑 2）。
 * 这里在客户端侧就只发 1..100，避免被后端静默改写成 20。
 */
function normPageSize(ps?: number): number {
  if (typeof ps !== 'number' || !Number.isFinite(ps)) return INJECTION_PAGE_SIZE_DEFAULT
  const n = Math.trunc(ps)
  if (n < 1 || n > INJECTION_PAGE_SIZE_MAX) return INJECTION_PAGE_SIZE_DEFAULT
  return n
}
