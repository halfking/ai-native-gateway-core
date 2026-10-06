// sessionContext.ts — 会话上下文读面（**admin 档**，两条只读端点）。
//   GET  /api/system/session-context/{taskId}/extraction-status
//   POST /api/system/session-context/titles/batch        ← ★ 只读语义，但**只能用 POST**
//
// ⚠️ 前缀是 **`/api/system/…`**，不是 `/api/admin/…`
//   （`admin/handler.go:1296`）⇒ **URL 形态容易照抄错**。
// 档位：`h.admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ★★★★★★★★ 头号陷阱一：`extraction-status` 是**异形端点**，两种响应形状。
//   （`admin/session_extract.go:231-277`）
//       A 未抽取 ⇒ {"task_id": X, "extracted": false}          ← **只有 2 个键**
//       B 已抽取 ⇒ {task_id, extracted:true, extracted_at, written,
//                   skipped_noise, skipped_duplicate, status, detail}   ← 8 个键
//   ⇒ A 形**没有** extracted_at / written / status / detail 这些键，
//     客户端**不许**把它们当「空值」渲染。
//
// ★★★★★★★★ 头号陷阱二：`extracted: false` 有**三种**成因，**完全分不开**：
//   1. 任务不属于你的租户（`assertTaskInTenant` 为假）—— :240-246
//   2. 表里压根没这行（从没抽取过）                       —— `sql.ErrNoRows` 走 :258
//   3. ★★ **数据库查询出错**（任何 err！）                 —— :258-263
//   ⇒ ★★★ 三者都返回 **200** + `extracted:false`。
//   ⇒ **数据库故障被报告成「没抽取过」**，且**从不返回 404**：
//     任务不存在**不是** 404，而是 200 + `extracted:false`。
//
// ★★★★★★★ 头号陷阱三：`titles/batch` 的 map **键里含一个字面 NUL 字节**。
//   （`admin/session_title.go:381-383`）
//       func sessionTitleMapKey(taskID, scopedSessionID string) string {
//           return taskID + "\x00" + scopedSessionIDKey(scopedSessionID)
//       }
//   ⇒ ★★ 客户端按 `titles[taskId]` 查**永远查不到**（除非 scoped_session_id 为空
//     且你连尾随的 NUL 一起写了）。键形如 `"task-123\u0000scoped-456"`。
//   ⇒ `scopedSessionIDKey` 只是 `strings.TrimSpace`（:274-276），
//     所以键的构造是「两边各自 trim → 用 NUL 拼接」。
//
// ★★★★★ `titles/batch` 的四种「空」也**分不开**，全部是 `{"titles": {}}` + 200：
//   1. `keys: []`（空请求）            —— 早返回分支 :623-626
//   2. 每个键的 task_id 都是空串        —— 静默 `continue` 跳过，pairs 为空
//   3. ★★ **数据库查询出错**            —— `h.db.Query` 出错时直接返回空 map（:362-364）
//   4. 真的没有存过任何标题
//   ⇒ `len(titles) === 0` 什么都证明不了。
//
// ★★★★ `titles/batch` 是**只包成 POST 的只读查询**：GET ⇒ **405**。
//   （理由写在源码注释里：给 request-logs 列表一次往返批量富化。）
//
// ★★★★ 静默丢弃三处：
//   · `task_id` 为空 ⇒ `continue`，**不报错**（:640-642）
//   · 重复的 (task_id, scoped_session_id) ⇒ **静默去重**（:643-647）
//   · 没有存过标题的键 ⇒ **整个键从响应 map 里消失**
//   ⇒ ★★★ **键缺失 ≠ 标题是空串**。
//
// ★★ 限幅：`len(keys) > 500` ⇒ **400** `too many keys (max 500)`。
//   ★ 注意是 `>` 不是 `>=` ⇒ **正好 500 个是合法的**（本仓第 10 种限幅语义）。
//
// ★★ 分派器的两条「同名不同码」错误：
//   （`admin/session_extract.go:33-55`）
//     rest == ""            ⇒ **404** `task_id required`
//     TrimSpace(parts[0])=="" ⇒ **400** `task_id required`   ← **同一句话，两个码**
//   · `len(parts) == 1`（只有 taskId）⇒ 404 `unknown session-context route`
//   · `default`（未知子动作）⇒ 404 `unknown session-context route`
//   · 方法不匹配 ⇒ 405 `method not allowed`
//
// ★★ `titles/batch` 在 {taskId} 分派**之前**被特判
//   （:44-52，注释明说是为了不让字面量 "titles" 被当成 task_id）
//   ⇒ ★★ **task_id 恰好叫 `titles` 的会话永远走不到自己那条分支**。
//
// ★★ 租户隔离**只在** `IsTenantAdmin(r) && GetTenantID(r) != "" &&
//   GetTenantID(r) != "default"` 时生效（:239）
//   ⇒ super_admin、或租户 id 为 `default` 的请求**完全不做归属检查**。
// ★ `titles/batch` **根本不做**租户隔离（没有任何归属判断）。
// ★ 两个端点都是 5 秒超时（`context.WithTimeout`）。
// ★ `detail` 列是 `COALESCE(detail, '{}'::jsonb)` —— COALESCE 只挡 SQL NULL，
//   **挡不住 JSON 标量 null** ⇒ `detail` 可以是 `null`。

import type { RequestOptions } from './client'
import { req } from './client'

// ── extraction-status ────────────────────────────────────────────────

/** ★ A 形只有这 2 个键。 */
export const EXTRACTION_STATUS_MINIMAL_KEYS = ['task_id', 'extracted'] as const

/** ★ B 形是这 8 个键（`task_id` 与 `extracted` 与 A 形共有）。 */
export const EXTRACTION_STATUS_FULL_KEYS = [
  'task_id',
  'extracted',
  'extracted_at',
  'written',
  'skipped_noise',
  'skipped_duplicate',
  'status',
  'detail',
] as const

/** ★★ A 形：只有 2 个键，其余**不存在**（不是 null）。 */
export interface ExtractionStatusMinimal {
  task_id: string
  extracted: false
}

/** ★ B 形：8 个键齐全。 */
export interface ExtractionStatusFull {
  task_id: string
  extracted: true
  extracted_at: string
  written: number
  skipped_noise: number
  skipped_duplicate: number
  status: string
  /** ★ `COALESCE` 只挡 SQL NULL ⇒ 这里**可能是 JSON 标量 null**。 */
  detail: unknown
}

export type ExtractionStatus = ExtractionStatusMinimal | ExtractionStatusFull

export function sessionContextPath(taskId: string, action: string): string {
  // ★ 前缀是 `/api/system/…`（不是 `/api/admin/…`）⇒ taskId 也要 encode
  return `/api/system/session-context/${encodeURIComponent(taskId)}/${action}`
}

/** ★★ 只读语义，但后端**只收 POST**（GET ⇒ 405）。 */
export const TITLES_BATCH_PATH = '/api/system/session-context/titles/batch'

export interface TitlesBatchKey {
  task_id: string
  /** ★ `omitempty` ⇒ 空串不发这个键。 */
  scoped_session_id?: string
}

export interface TitlesBatchResult {
  /** ★ `make(map[string]string, …)` ⇒ **永不为 null**。 */
  titles: Record<string, string>
}

export function fetchExtractionStatus(taskId: string, options?: RequestOptions): Promise<ExtractionStatus> {
  return req<unknown>('GET', sessionContextPath(taskId, 'extraction-status'), undefined, options).then(
    unwrapExtractionStatus,
  )
}

/**
 * ★★★★★★ 两种形状都合法 ⇒ 解包判据只能钉「`task_id` 是字符串且 `extracted`
 *   是布尔」，**不能**要求 B 形的 8 个键（要求了就永远收不到 A 形）。
 */
export function unwrapExtractionStatus(resp: unknown): ExtractionStatus {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (typeof m.task_id === 'string' && typeof m.extracted === 'boolean') {
      return m as unknown as ExtractionStatus
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`session-context/extraction-status 响应形状不符：期望 {task_id, extracted, …}，实得 ${actual}`)
}

export function fetchTitlesBatch(
  keys: TitlesBatchKey[],
  options?: RequestOptions,
): Promise<TitlesBatchResult> {
  return req<unknown>('POST', TITLES_BATCH_PATH, { keys }, options).then(unwrapTitlesBatch)
}

/** ★ `{titles: {...}}` 一个信封键。 */
export function unwrapTitlesBatch(resp: unknown): TitlesBatchResult {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (m.titles && typeof m.titles === 'object' && !Array.isArray(m.titles)) return m as unknown as TitlesBatchResult
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`session-context/titles/batch 响应形状不符：期望 {titles: {…}}，实得 ${actual}`)
}

// ── map 键（NUL 分隔）────────────────────────────────────────────────

/**
 * ★★★★★★★ 复刻后端的键构造（`admin/session_title.go:381-383`）：
 *   `taskId + "\x00" + scopedSessionID.trim()`
 *   ⚠️ 少写这个 `\u0000` ⇒ 按 taskId 查**永远 miss**。
 * ★ `scopedSessionIDKey` 就是 `strings.TrimSpace`。
 */
export function sessionTitleMapKey(taskId: string, scopedSessionId: string): string {
  return `${taskId}\u0000${scopedSessionId.trim()}`
}

/** ★ 拆键（客户端只能这样反查，键不可猜）。 */
export function splitSessionTitleMapKey(key: string): { taskId: string; scopedSessionId: string } {
  const i = key.indexOf('\u0000')
  if (i < 0) return { taskId: key, scopedSessionId: '' }
  return { taskId: key.slice(0, i), scopedSessionId: key.slice(i + 1) }
}

/** ★★ 键里**确实**含 NUL ⇒ 用它判「这个 map 是不是后端产的」。 */
export function titleMapKeyHasNul(key: string): boolean {
  return key.includes('\u0000')
}

/** ★★★ 请求侧也要按同样规则去重，否则白跑一趟（后端会静默去重）。 */
export function normalizeTitlesBatchKeys(keys: TitlesBatchKey[]): TitlesBatchKey[] {
  const seen = new Set<string>()
  const out: TitlesBatchKey[] = []
  for (const k of keys) {
    const taskId = (k.task_id ?? '').trim()
    if (taskId === '') continue // ★ 后端也会静默跳过
    const scoped = (k.scoped_session_id ?? '').trim()
    const mk = sessionTitleMapKey(taskId, scoped)
    if (seen.has(mk)) continue
    seen.add(mk)
    out.push(scoped ? { task_id: taskId, scoped_session_id: scoped } : { task_id: taskId })
  }
  return out
}

// ── 页面侧判读 ───────────────────────────────────────────────────────

/** ★★ 后端硬门槛：`len(keys) > 500` ⇒ 400 ⇒ **正好 500 合法**。 */
export const TITLES_BATCH_MAX_KEYS = 500

export function titlesBatchExceedsLimit(n: number): boolean {
  return n > TITLES_BATCH_MAX_KEYS
}

/** ★★ 请求键数与响应键数不等，有三种成因，**分不开**。 */
export function titlesBatchPartiallyAnswered(requested: number, returned: number): boolean {
  return returned < requested
}

/** ★★★ 键缺失 = 「没存过标题」，**不是**「标题是空串」。 */
export function titlesBatchMissingKeys(r: TitlesBatchResult, requested: TitlesBatchKey[]): string[] {
  const want = new Set(normalizeTitlesBatchKeys(requested).map((k) => sessionTitleMapKey(k.task_id, k.scoped_session_id ?? '')))
  const got = new Set(Object.keys(r.titles))
  return [...want].filter((k) => !got.has(k))
}

/** ★★ `extracted:false` 分不开「不属于你的租户 / 没抽过 / DB 挂了」。 */
export function extractionStatusIsIndeterminate(s: ExtractionStatus): boolean {
  return s.extracted === false
}

/** ★★ A 形**没有**这些键 ⇒ 不许当空值渲染。 */
export function extractionStatusLacksDetailFields(s: ExtractionStatus): boolean {
  return !('status' in s) || !('written' in s) || !('extracted_at' in s)
}

/** ★★ 三种「空」分不开 ⇒ `titles` 为空什么都证明不了。 */
export function titlesBatchEmptyIsIndeterminate(r: TitlesBatchResult): boolean {
  return Object.keys(r.titles).length === 0
}

/** ★ B 形的 `detail` 可能是 JSON 标量 null（COALESCE 挡不住）。 */
export function extractionDetailIsNull(s: ExtractionStatus): boolean {
  return 'detail' in s && s.detail === null
}

/** ★ 两条都是 5 秒超时；超时会被 `client.ts` 折成错误。 */
export function sessionContextSettingsMissing(msg: string): boolean {
  return /database not configured/i.test(msg)
}

/** ★ 400 与 404 **共用同一句话** `task_id required` ⇒ 必须看状态码才能分。 */
export function taskIdRequiredMessage(msg: string): boolean {
  return /task_id required/i.test(msg)
}

/** ★★ task_id 恰好叫 `titles` 的会话走不到自己的分支（被特判截胡）。 */
export function taskIdIsShadowedByBatch(taskId: string): boolean {
  return taskId === 'titles'
}