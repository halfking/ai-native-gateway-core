import { req, type RequestOptions } from './client'

/**
 * approvals.ts — 审批查询面的两条只读端点（2026-10-08，第六十四批）。
 *
 * GET /api/admin/approvals         （cmd/gateway/main.go:7384，wrapAdmin 档）
 * GET /api/admin/approvals/stats   （cmd/gateway/main.go:7385，wrapAdmin 档）
 *
 * ★ 两者都是 `wrapAdmin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席不设 `requiresRole`。
 *
 * ## 不碰写操作
 * `/api/v1/approvals/*` 下有 approve / reject / resume 三条
 * （main.go:7375-7381），它们**真的改变审批状态**（会导致超时、影响会话），
 * 本批不碰。
 *
 * ## ★★★★ 本族最刺眼的一处：`total` 不是真实总数
 *
 * ```go
 * records, err := h.manager.List(r.Context(), filter)
 * // Get total count (simplified - return length for now)
 * total := len(records)          // approval_handler.go:348-350
 * ```
 *
 * `filter` 里带着 `Limit`/`Offset`，所以 `records` 是**当前这一页**。
 * ⇒ **`total` = 本页返回了几条，不是库里一共几条**。
 *
 * 连带地 `totalPages` 也失真：
 *
 * ```go
 * totalPages := (total + req.PageSize - 1) / req.PageSize   // :357
 * if totalPages < 1 { totalPages = 1 }
 * ```
 *
 * `total ≤ pageSize` 时 `totalPages` 恒为 **1**。
 * ⇒ 客户端**绝不能**用 `total_pages` 算总页数，也**绝不能**用
 * `total > items.length` 判断「还有更多」—— 前者永远说只有一页，
 * 后者永远说没有下一页。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/approvals
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * `sessionaudit.DetectResult` 的子字段本层**刻意不逐字钉**
 * （它由检测器写入，形状随检测项走），
 * 但 `detect_result` 本身**可以是 null** —— 这一点必须钉。
 */
export type DetectResult = Record<string, unknown> | null

/** api/approval_handler.go:87-102，逐字照抄。 */
export interface ApprovalItem {
  id: string
  session_id: string
  tenant_id: string
  request_id: string
  status: string
  /** ★ 可为 `null`（无检测结果的审批）。 */
  detect_result: DetectResult
  /**
   * ★★ **可以是空字符串**：`buildListItem` 只在 `record.DetectResult != nil`
   * 时才填 `RiskLevel`/`TriggerType`（:546-549）
   * ⇒ 没有检测结果的行，这两列**不是「低风险」也不是「无风险」，是「不知道」**。
   */
  risk_level: string
  /** ★ 同上，空串 = 未检测。 */
  trigger_type: string
  /** ★ 三个带 omitempty ⇒ 未审批时**键不存在**。 */
  approved_by?: string
  approved_at?: string
  reason?: string
  created_at: string
  expires_at: string
  /** ★ 带 omitempty，且只在 `status === "pending"` 且未过期时才有值。 */
  time_left?: string
}

export const APPROVAL_ITEM_KEYS = [
  'id', 'session_id', 'tenant_id', 'request_id', 'status', 'detect_result',
  'risk_level', 'trigger_type', 'created_at', 'expires_at',
] as const

/** api/approval_handler.go:78-84。★ 五个键全部无 omitempty。 */
export interface ApprovalListResponse {
  items: ApprovalItem[]
  /** ★★ **本页条数**，不是真实总数（见文件头）。 */
  total: number
  page: number
  page_size: number
  /** ★★ 恒为 1（`total ≤ page_size` 时），**不可当作真实总页数**。 */
  total_pages: number
}

export const APPROVAL_LIST_KEYS = ['items', 'total', 'page', 'page_size', 'total_pages'] as const

export const APPROVAL_PAGE_DEFAULT = 1
export const APPROVAL_PAGE_SIZE_DEFAULT = 50
export const APPROVAL_PAGE_SIZE_MAX = 200

/**
 * ★★ `status` **缺省是 `pending`**（:515-517），不是「全部」。
 * 四个合法值：pending / approved / rejected / timeout。
 */
export const APPROVAL_STATUSES = ['pending', 'approved', 'rejected', 'timeout'] as const
export type ApprovalStatus = (typeof APPROVAL_STATUSES)[number]
export const APPROVAL_STATUS_DEFAULT: ApprovalStatus = 'pending'

export interface ApprovalListQuery {
  status?: ApprovalStatus
  tenantId?: string
  riskLevel?: string
  page?: number
  /** ★ 参数名是 `page_size`（带下划线），不是 `size`。 */
  pageSize?: number
  sortBy?: string
  sortOrder?: string
}

/**
 * ★★★ 三个参数**四种越界行为**（与 dashboard/annotations 都不相同）：
 *   page       非整数或 ≤0 ⇒ 静默回落 **1**
 *   page_size  非整数或 >200 ⇒ 静默回落 **50**（`err == nil && val > 0 && val <= 200`）
 *   status     **不校验** ⇒ 非法值直接下发给 SQL filter（结果空列表，不是 400）
 */
export function approvalsPageEffective(page: number | undefined): number {
  if (page === undefined || !Number.isInteger(page)) return APPROVAL_PAGE_DEFAULT
  return page > 0 ? page : APPROVAL_PAGE_DEFAULT
}

export function approvalsPageSizeEffective(size: number | undefined): number {
  if (size === undefined || !Number.isInteger(size)) return APPROVAL_PAGE_SIZE_DEFAULT
  if (size <= 0 || size > APPROVAL_PAGE_SIZE_MAX) return APPROVAL_PAGE_SIZE_DEFAULT
  return size
}

/** ★ 缺省是 pending，不是「全部」。 */
export function approvalsStatusDefault(s: string | null | undefined): ApprovalStatus {
  if (s === undefined || s === null) return APPROVAL_STATUS_DEFAULT
  return s as ApprovalStatus
}

function approvalsSuffix(q: ApprovalListQuery | undefined): string {
  const qs = new URLSearchParams()
  if (!q) return ''
  if (q.status !== undefined) qs.set('status', q.status)
  if (q.tenantId !== undefined) qs.set('tenant_id', q.tenantId)
  if (q.riskLevel !== undefined) qs.set('risk_level', q.riskLevel)
  if (q.page !== undefined) qs.set('page', String(q.page))
  if (q.pageSize !== undefined) qs.set('page_size', String(q.pageSize))
  if (q.sortBy !== undefined) qs.set('sort_by', q.sortBy)
  if (q.sortOrder !== undefined) qs.set('sort_order', q.sortOrder)
  return qs.toString() ? `?${qs}` : ''
}

export function fetchApprovals(
  q?: ApprovalListQuery,
  options?: RequestOptions,
): Promise<ApprovalListResponse> {
  return req<unknown>('GET', `/api/admin/approvals${approvalsSuffix(q)}`, undefined, options)
    .then(unwrapApprovals)
}

export function unwrapApprovals(resp: unknown): ApprovalListResponse {
  const d = requireObject(resp, '审批列表')
  requireKeys(d, APPROVAL_LIST_KEYS, '审批列表')
  if (!Array.isArray(d.items)) throw new Error('审批列表 items 不是数组')
  d.items.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`审批列表 items[${i}] 不是对象`)
    requireKeys(row, APPROVAL_ITEM_KEYS, `审批列表 items[${i}]`)
    // ★ detect_result 可以是 null，但**不是** true/false/字符串
    const dr = row.detect_result
    if (dr !== null && !isPlainObject(dr)) {
      throw new Error(`审批列表 items[${i}].detect_result 不是对象也不是 null`)
    }
  })
  return d as unknown as ApprovalListResponse
}

/**
 * ★★ `total` 是本页条数，**不是库里的总数**。
 * UI 上要显示「共 N 条」必须自己说明那是本页数，
 * 或者干脆显示 `items.length`（两者恒等，见下方判据）。
 */
export function approvalsTotalIsPageSize(r: ApprovalListResponse): boolean {
  return r.total === r.items.length
}

/** ★★ 翻页只能靠「本页取满」，**不能**信 `total_pages`（恒为 1）。 */
export function approvalsHasNextPage(r: ApprovalListResponse): boolean {
  return r.items.length >= approvalsPageSizeEffective(r.page_size)
}

/** ★ 没有检测结果 ⇒ risk_level / trigger_type 是空串，不是「低风险」。 */
export function approvalRiskUnknown(item: ApprovalItem): boolean {
  return item.detect_result === null || item.risk_level === ''
}

/** ★ 待审批且有倒计时（`time_left` 带 omitempty，只在 pending 且未过期时出现）。 */
export function approvalCountingDown(item: ApprovalItem): boolean {
  return item.status === 'pending' && item.time_left !== undefined && item.time_left !== ''
}

/** ★ 待审批但**没有**倒计时 ⇒ 已过期却仍标着 pending，UI 必须显示逾期。 */
export function approvalOverdue(item: ApprovalItem): boolean {
  return item.status === 'pending' && !approvalCountingDown(item)
}

/** ★ 已审批过的行才有这三个键（`approved_by`/`approved_at`/`reason` 带 omitempty）。 */
export function approvalDecided(item: ApprovalItem): boolean {
  return 'approved_at' in item || 'approved_by' in item
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/approvals/stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/** api/approval_handler.go:51-62，逐字照抄。 */
export interface ApprovalStats {
  total: number
  pending: number
  approved: number
  rejected: number
  timeout: number
  /**
   * ★ `float64` 零值：`avg` 的分母为 0 时后端留 0.0。
   * ⇒ 与「真的是 0 秒」不可分，靠 `approved + rejected` 判有没有样本。
   */
  avg_approval_time_seconds: number
  /** ★ 两个 map，**键数可为 0**（`map[string]int{}` 序列化是 `{}` 不是 null）。 */
  by_risk_level: Record<string, number>
  by_trigger_type: Record<string, number>
  /**
   * ★★ `TodayTotal`/`TodayPending` **无视下面那个时间范围** ——
   * 它们在 `calculateStats` 里按「今天」单独算，而 `start_time`/`end_time`
   * 控制的是其余七个字段。⇒ 同一份响应里混着两套口径。
   */
  today_total: number
  today_pending: number
}

export const APPROVAL_STATS_KEYS = [
  'total', 'pending', 'approved', 'rejected', 'timeout',
  'avg_approval_time_seconds', 'by_risk_level', 'by_trigger_type',
  'today_total', 'today_pending',
] as const

/** ★★ `start_time`/`end_time` 是 **RFC3339**（`time.Parse(time.RFC3339, …)`），
 * 格式错**静默回落**（`if err == nil` 才赋值，:398-408）⇒ 不报错。 */
export const APPROVAL_TIME_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/

export interface ApprovalStatsQuery {
  tenantId?: string
  /** RFC3339。格式错 ⇒ 后端静默忽略这个参数。 */
  startTime?: string
  /** RFC3339。格式错 ⇒ 后端静默忽略这个参数。 */
  endTime?: string
}

export function approvalTimeValid(s: string | null | undefined): boolean {
  if (s === undefined) return true
  if (s === null) return false
  return APPROVAL_TIME_PATTERN.test(s)
}

function approvalStatsSuffix(q: ApprovalStatsQuery | undefined): string {
  const qs = new URLSearchParams()
  if (!q) return ''
  if (q.tenantId !== undefined) qs.set('tenant_id', q.tenantId)
  if (q.startTime !== undefined) qs.set('start_time', q.startTime)
  if (q.endTime !== undefined) qs.set('end_time', q.endTime)
  return qs.toString() ? `?${qs}` : ''
}

export function fetchApprovalStats(
  q?: ApprovalStatsQuery,
  options?: RequestOptions,
): Promise<ApprovalStats> {
  if (!approvalTimeValid(q?.startTime) || !approvalTimeValid(q?.endTime)) {
    return Promise.reject(new Error('时间格式必须是 RFC3339（2026-10-01T00:00:00Z）'))
  }
  return req<unknown>('GET', `/api/admin/approvals/stats${approvalStatsSuffix(q)}`, undefined, options)
    .then(unwrapApprovalStats)
}

export function unwrapApprovalStats(resp: unknown): ApprovalStats {
  const d = requireObject(resp, '审批统计')
  requireKeys(d, APPROVAL_STATS_KEYS, '审批统计')
  if (!isPlainObject(d.by_risk_level)) throw new Error('审批统计 by_risk_level 不是对象')
  if (!isPlainObject(d.by_trigger_type)) throw new Error('审批统计 by_trigger_type 不是对象')
  return d as unknown as ApprovalStats
}

/** ★★ 四个状态计数加起来对不上 total ⇒ 数据口径出了问题。 */
export function approvalCountsDisagree(s: ApprovalStats): boolean {
  return s.pending + s.approved + s.rejected + s.timeout > s.total
}

/** ★ 分母为 0 时 `avg_approval_time_seconds` 无意义（不是「0 秒审批」）。 */
export function approvalAvgTimeMeaningless(s: ApprovalStats): boolean {
  return s.approved + s.rejected === 0
}

/* ── 内部工具 ──────────────────────────────────────────────────────────── */

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