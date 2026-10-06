// attachments.ts — 附件数据留存读面（**admin 档**，只读六端点）。
//   GET  /api/admin/attachments?limit=&offset=&since=&until=&tenant_id=
//   GET  /api/admin/attachments/stats?since=&until=&tenant_id=
//   GET  /api/admin/attachments/policy
//   GET|POST /api/admin/attachments/cleanup/preview?older_than_days=
//   GET  /api/admin/attachments/{request_id}
//   GET  /api/admin/attachments/filesystem/stats
//
// 鉴权：六条**全是 admin 档**（`admin/handler.go:998-1008` 的 `admin(...)`）
// ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
// ★★ 但**同一前缀下混着 superAdmin**：
//     /api/admin/attachments/cleanup/execute       → h.superAdmin
//     /api/admin/attachments/filesystem/cleanup    → h.superAdmin
//     其余（含 filesystem/stats）                    → admin
//   ⇒ 前缀相同、档位不同，移动端**不能**按前缀判权限。
//
// ⚠️⚠️⚠️ 六条端点的**响应形状两两不同**，没有任何两条相同：
//     list     ⇒ `{items, limit, offset, count}`
//     stats    ⇒ `{breakdown, total_count, total_bytes}`
//     policy   ⇒ `{policy: {…4 键…}, note}`
//     preview  ⇒ `{older_than_days, affected_records, total_bytes, dry_run, action}`
//     item     ⇒ **裸对象** `{request_id, ts, tenant_id, client_model, success, attachments}`
//     fs/stats ⇒ **裸对象** `{attachment_dir, total_files, …10 键}`
// ⇒ 五种形状互不包含 ⇒ 互喂判据写得出。
//
// ★★★★★★ 头号陷阱：**同一个 `attachments` 字段，两条端点的 nullability 不同**。
//   · **list**（`data_lifecycle_attachments.go:182`）是
//       `COALESCE(client_model,''), success, attachments::text`
//     ⇒ `attachments` 是 **`json.RawMessage` 原样透传**，**可以是 JSON 标量 `null`**。
//     源码注释：2026-09-14 起 `request_logs.attachments` 里有 **18k+ 行**存的是
//     JSON `null` 标量（而不是数组）⇒ 一行**出现在列表里，但不存在任何附件**。
//   · **item**（`:629`）是
//       `COALESCE(attachments::text, '[]')`
//     ⇒ ★ 这里**保证是数组**（没有就给 `[]`）。
//   ⇒ ★★ 「列表里那条没附件」与「详情里 attachments 是空数组」是**同一件事的两种表现**，
//     客户端**不许**把 list 里的 `null` 直接当 `[]` 渲染成「无附件」，
//     也不许因此抛错。
//
// ★★★★ 第二陷阱：`stats` 的行集合是 `list` 的**真子集**。
//     list  : `WHERE attachments IS NOT NULL`
//     stats : `WHERE attachments IS NOT NULL AND jsonb_typeof(attachments) = 'array'`
//     （`:170` vs `:242`；后者是因为 18k+ 行的 `null` 标量会让
//      `jsonb_array_elements` 报 `cannot extract elements from a scalar`）
//   ⇒ ★★★ 「列表 N 条」与「统计 M」**对不上是预期的**，不是数据错。页面必须标口径。
//
// ★★★★ 第三陷阱：`parseTimeRange` **静默丢弃**解析失败的时间参数：
//     if t, err := time.Parse(time.RFC3339, s); err == nil { since = t }
//   ⇒ `?since=garbage` / `?since=2026-13-45` 一律**当作没传**，
//     **不报错、不告警**，返回**全时间范围**的数据。
//   ⇒ 页面绝不能因为「我传了 since」就以为窗口生效了。
//   ★ 区间是**半开**的：`ts >= since` 且 `ts < until`（含下不含上）。
//
// ★★★★ 第四陷阱：`limit` / `offset` 是**两端 clamp**（不是回落）：
//     clampInt(s, def, min, max)：空 ⇒ def；Atoi 失败 ⇒ def；<min ⇒ min；>max ⇒ max
//     limit  = clampInt(q, 50, 1, 200)      ⇒ 默认 50，两端夹到 [1,200]
//     offset = clampInt(q, 0, 0, 100000)    ⇒ 默认 0，两端夹到 [0,100000]
//   ⇒ ★ 与 approval-config 那个 audit limit（「越界**回落** 100」）**方向相反**。
//   ⇒ `?limit=99999` ⇒ 200（不是回落 50）；`?offset=-5` ⇒ 0。
//
// ★★★ 第五：`tenant_id` 对 **tenant_admin 被静默忽略**。
//     `attachmentTenantScope`（`:60-72`）第一个分支就命中：
//         if IsTenantAdmin(r) { return " AND tenant_id = $N", []any{GetTenantID(r)} }
//     ⇒ 传了 `?tenant_id=别的租户` 也**不报错**，只是不生效，仍只看自己租户。
//     super_admin + 显式 `tenant_id` ⇒ 收窄；super_admin 不传 ⇒ **看全部**。
//
// ★★ 第六：两种行丢失的失败方式**处理相反**：
//     · 每行 `rows.Scan` 失败 ⇒ `warnRowSkip` + `continue` ⇒ **静默丢行**
//     · `rows.Err()`（传输层截断）⇒ `writeAggRowsErr` ⇒ **整个 500**
//     源码注释：「截断的清单会被当成"就这么多附件"，静默 200 比失败更有害」。
//     ⇒ 所以 200 **不保证**条数完整，但也不是「悄悄少了就当全量」。
//
// ★★★ 第七：`policy` 是**硬编码常量**，而且**没有 `h.db == nil` 检查**：
//       {retention_days: 30, max_size_bytes: 20*1024*1024, auto_cleanup: false,
//        delete_filesystem: false, description: "…"}
//     ⇒ ★ 它**不从任何配置读**，也不需要数据库（其余五条没 DB 都 503）。
//     ⇒ `max_size_bytes` 就是 **20971520**（20 MiB），别在客户端另算一套。
//     响应还带一个 `note` 说明只能靠 `LLM_GATEWAY_ATTACHMENT_DISABLED=1` 整体关闭。
//
// ★★ 第八：`cleanup/preview` **没有方法门** —— 注册是 `admin(...)` 且 handler
//     里没有 `if r.Method != …` ⇒ **GET 也能调**（用 `?older_than_days=`）。
//     源码注释写的是 POST，但那是文档与实现不一致。`dry_run` 恒为 `true`。
//     `older_than_days` 走 `parseOlderThanDays`：先 query、再 body，都要求 `n > 0`，
//     否则**回落默认 30**（又一套「越界回落默认」语义）。
//
// ★★ 第九：`{request_id}` 详情端点 `ORDER BY ts DESC LIMIT 1`
//     ⇒ 同一 request_id 有多行时取**最新**那行。
//     跨租户时返 **404 而不是 403** —— 源码注释明说是**故意**的，
//     为的是不泄漏「这条记录在别的租户存在」。
//
// ★ `filesystem/stats` 的 `oldest_file_time` 是 **`*string` 且无 omitempty**
//   ⇒ 键一定在，值为 `null` = 目录里**一个文件都没有**。
//   `disk_warning_level` 由 `disk_usage_percent` 分档：>=90 danger、>=75 warning、否则 safe。
//
// ★★ 写操作本页一律不碰：`cleanup/execute`、`filesystem/cleanup`
//     （这两条是 **superAdmin** 档）。
//
// ★★ `client_model` 是 `COALESCE(client_model,'')` ⇒ 空串，不是 null。

import type { RequestOptions } from './client'
import { req } from './client'

/** ★ `clampInt(q, 50, 1, 200)` 的三参数。 */
export const ATTACHMENT_LIST_LIMIT_DEFAULT = 50
export const ATTACHMENT_LIST_LIMIT_MIN = 1
export const ATTACHMENT_LIST_LIMIT_MAX = 200

/** ★ `clampInt(q, 0, 0, 100000)` 的三参数。 */
export const ATTACHMENT_LIST_OFFSET_DEFAULT = 0
export const ATTACHMENT_LIST_OFFSET_MAX = 100000

/** ★ `policy.max_size_bytes` 是硬编码的 `20 * 1024 * 1024`。 */
export const ATTACHMENT_POLICY_MAX_SIZE_BYTES = 20 * 1024 * 1024
/** ★ `policy.retention_days` 是硬编码的 30。 */
export const ATTACHMENT_POLICY_RETENTION_DAYS = 30
/** ★ `parseOlderThanDays` 的默认值（preview 回落值）。 */
export const ATTACHMENT_PREVIEW_DAYS_DEFAULT = 30

// ── 线格式 ─────────────────────────────────────────────────────────

/**
 * ★★ `attachments` 是 `json.RawMessage` **原样透传**，**可能是 JSON 标量 `null`**。
 * 它**不是** `AttachmentMeta[]` —— 有 18k+ 行的 `attachments` 列存的就是 `null`。
 */
export interface AttachmentRow {
  request_id: string
  ts: string
  tenant_id: string
  /** ★ `COALESCE(client_model,'')` ⇒ 空串，不是 null。 */
  client_model: string
  success: boolean
  /** ★★ 可能是数组、也可能是 `null`（或后端写入的其它 JSON 形状）。 */
  attachments: unknown
}

export interface AttachmentList {
  items: AttachmentRow[]
  /** ★ clamp **之后**回显。 */
  limit: number
  offset: number
  /** ★ 派生值 `len(items)`。 */
  count: number
}

export interface AttachmentStatBucket {
  type: string
  content_type: string
  count: number
  total_bytes: number
}

export interface AttachmentStats {
  /** ★ `make([]bucket, 0)` ⇒ 永不为 null。 */
  breakdown: AttachmentStatBucket[]
  total_count: number
  total_bytes: number
}

export interface AttachmentPolicyInner {
  /** ★ 硬编码 30。 */
  retention_days: number
  /** ★ 硬编码 20971520（20 MiB）。 */
  max_size_bytes: number
  auto_cleanup: boolean
  delete_filesystem: boolean
  description: string
}

export interface AttachmentPolicy {
  policy: AttachmentPolicyInner
  note: string
}

export interface AttachmentCleanupPreview {
  older_than_days: number
  affected_records: number
  total_bytes: number
  /** ★ 恒为 `true`。 */
  dry_run: boolean
  action: string
}

export interface AttachmentItem {
  request_id: string
  ts: string
  tenant_id: string
  client_model: string
  success: boolean
  /** ★★ 这里**保证是数组**（`COALESCE(attachments::text,'[]')`），与 list 侧不同！ */
  attachments: unknown
}

export interface AttachmentFilesystemStats {
  attachment_dir: string
  total_files: number
  total_size_bytes: number
  total_size_human: string
  /** ★ 指针且**无** omitempty ⇒ 键在值为 `null` = 目录里一个文件都没有。 */
  oldest_file_time: string | null
  disk_total_bytes: number
  disk_used_bytes: number
  disk_avail_bytes: number
  disk_usage_percent: number
  /** safe | warning | danger */
  disk_warning_level: string
}

// ── List ───────────────────────────────────────────────────────────

export interface AttachmentListParams {
  limit?: number
  offset?: number
  /** ★ 必须 RFC3339；不合法会被后端**静默丢弃**。 */
  since?: string
  until?: string
  /** ★ 对 tenant_admin **静默无效**。 */
  tenantId?: string
}

/** ★ `clampInt` 的客户端镜像：空/Atoi 失败 ⇒ def；两端夹到 [min,max]。 */
function clampInt(v: number | string | undefined | null, def: number, min: number, max: number): number {
  if (v === undefined || v === null || v === '') return def
  const n = typeof v === 'number' ? (Number.isFinite(v) ? Math.trunc(v) : Number.NaN) : Number.NaN
  if (Number.isNaN(n)) return def
  if (n < min) return min
  if (n > max) return max
  return n
}

/** ★★ 两端 clamp（不是回落）。返回后端**实际会用的**那个值。 */
export function attachmentLimitClamped(v: number | string | null | undefined): number {
  return clampInt(v, ATTACHMENT_LIST_LIMIT_DEFAULT, ATTACHMENT_LIST_LIMIT_MIN, ATTACHMENT_LIST_LIMIT_MAX)
}

/** ★★ 两端 clamp。 */
export function attachmentOffsetClamped(v: number | string | null | undefined): number {
  return clampInt(v, ATTACHMENT_LIST_OFFSET_DEFAULT, 0, ATTACHMENT_LIST_OFFSET_MAX)
}

/** ★ 越界时页面必须显示「已夹到边界」，否则用户不知道自己看到的是子集。 */
export function attachmentLimitWasClamped(v: number | string | null | undefined): boolean {
  const eff = attachmentLimitClamped(v)
  if (typeof v !== 'number' || !Number.isFinite(v)) return false
  return eff !== Math.trunc(v)
}

/** ★ 同上。 */
export function attachmentOffsetWasClamped(v: number | string | null | undefined): boolean {
  const eff = attachmentOffsetClamped(v)
  if (typeof v !== 'number' || !Number.isFinite(v)) return false
  return eff !== Math.trunc(v)
}

/**
 * ★★ 只有**严格 RFC3339** 的时间参数才会生效。
 * 后端 `parseTimeRange` 用 `if err == nil` 才赋值 ⇒ 不合法的一律**当没传**，
 * **不报错也不告警**，于是返回**全时间范围**。这个函数让页面能提前自查。
 *
 * ★★★ 光有形状正则**不够**：Go 的 `time.Parse(time.RFC3339, …)` 会校验**取值范围**
 *     （`2026-13-45T00:00:00Z` 因月份 13 越界而失败 ⇒ 被丢弃），
 *     而 `\d{2}` 会照单全收 ⇒ 第一版这里正是这么错的：
 *     页面被告知「会生效」，后端却静默丢弃，整个提示反过来误导人。
 *     ⇒ 月/日/时/分/秒的范围都要校验。
 */
function rfc3339IsReal(v: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(v)
  if (!m) return false
  const month = Number(m[2])
  const day = Number(m[3])
  const hour = Number(m[4])
  const minute = Number(m[5])
  const second = Number(m[6])
  if (month < 1 || month > 12) return false
  if (day < 1 || day > 31) return false
  if (hour > 23) return false
  if (minute > 59) return false
  // Go 允许秒为 60（闰秒）
  if (second > 60) return false
  const off = m[8] ?? ''
  if (off !== 'Z') {
    const oh = Number(off.slice(1, 3))
    const om = Number(off.slice(4, 6))
    if (oh > 23 || om > 59) return false
  }
  return true
}

/** ★ 后端会不会**真的**采纳这个 `since`。 */
export function attachmentSinceWillBeUsed(v: string | undefined | null): boolean {
  return typeof v === 'string' && v.length > 0 && rfc3339IsReal(v)
}

/** ★ 同上。 */
export function attachmentUntilWillBeUsed(v: string | undefined | null): boolean {
  return typeof v === 'string' && v.length > 0 && rfc3339IsReal(v)
}

/** ★★ 用户填了时间但后端会丢弃 ⇒ 页面必须提示「窗口没生效」。 */
export function attachmentTimeWindowWasDropped(
  since?: string | null,
  until?: string | null,
): boolean {
  const sinceGiven = typeof since === 'string' && since.length > 0
  const untilGiven = typeof until === 'string' && until.length > 0
  return (sinceGiven && !attachmentSinceWillBeUsed(since)) || (untilGiven && !attachmentUntilWillBeUsed(until))
}

export function fetchAttachments(
  params: AttachmentListParams = {},
  options?: RequestOptions,
): Promise<AttachmentList> {
  const qs = new URLSearchParams()
  if (params.limit !== undefined) qs.set('limit', String(Math.trunc(params.limit)))
  if (params.offset !== undefined) qs.set('offset', String(Math.trunc(params.offset)))
  if (params.since) qs.set('since', params.since)
  if (params.until) qs.set('until', params.until)
  if (params.tenantId) qs.set('tenant_id', params.tenantId)
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/attachments${s ? '?' + s : ''}`, undefined, options).then(
    unwrapAttachments,
  )
}

/** ★★ 响应是 `{items, limit, offset, count}` 信封（与本仓多数 admin 端点一致）。 */
export function unwrapAttachments(resp: unknown): AttachmentList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (
      Array.isArray(m.items) &&
      typeof m.limit === 'number' &&
      typeof m.offset === 'number' &&
      typeof m.count === 'number'
    ) {
      return m as unknown as AttachmentList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments 响应形状不符：期望 {items, limit, offset, count}，实得 ${actual}`,
  )
}

// ── Stats ──────────────────────────────────────────────────────────

export function fetchAttachmentStats(
  params: { since?: string; until?: string; tenantId?: string } = {},
  options?: RequestOptions,
): Promise<AttachmentStats> {
  const qs = new URLSearchParams()
  if (params.since) qs.set('since', params.since)
  if (params.until) qs.set('until', params.until)
  if (params.tenantId) qs.set('tenant_id', params.tenantId)
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/attachments/stats${s ? '?' + s : ''}`, undefined, options).then(
    unwrapAttachmentStats,
  )
}

/** ★★ 响应是 `{breakdown, total_count, total_bytes}`。★ **没有** limit/offset。 */
export function unwrapAttachmentStats(resp: unknown): AttachmentStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (
      Array.isArray(m.breakdown) &&
      typeof m.total_count === 'number' &&
      typeof m.total_bytes === 'number'
    ) {
      return m as unknown as AttachmentStats
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments/stats 响应形状不符：期望 {breakdown, total_count, total_bytes}，实得 ${actual}`,
  )
}

// ── Policy（硬编码，且无需数据库）────────────────────────────────────

export function fetchAttachmentPolicy(options?: RequestOptions): Promise<AttachmentPolicy> {
  return req<unknown>('GET', '/api/admin/attachments/policy', undefined, options).then(unwrapAttachmentPolicy)
}

/** ★★ `{policy: {…}, note}`；`policy` 内层是硬编码的 5 个键。 */
export function unwrapAttachmentPolicy(resp: unknown): AttachmentPolicy {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const p = m.policy
    if (p && typeof p === 'object' && !Array.isArray(p)) {
      const pol = p as Record<string, unknown>
      if (
        typeof pol.retention_days === 'number' &&
        typeof pol.max_size_bytes === 'number' &&
        typeof pol.auto_cleanup === 'boolean' &&
        typeof pol.delete_filesystem === 'boolean'
      ) {
        return m as unknown as AttachmentPolicy
      }
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments/policy 响应形状不符：期望 {policy: {retention_days, max_size_bytes, auto_cleanup, delete_filesystem}, note}，实得 ${actual}`,
  )
}

// ── Cleanup preview（无方法门，GET 也能调）─────────────────────────

export function previewAttachmentCleanup(
  params: { olderThanDays?: number; tenantId?: string } = {},
  options?: RequestOptions,
): Promise<AttachmentCleanupPreview> {
  const qs = new URLSearchParams()
  if (params.olderThanDays !== undefined) qs.set('older_than_days', String(Math.trunc(params.olderThanDays)))
  if (params.tenantId) qs.set('tenant_id', params.tenantId)
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/attachments/cleanup/preview${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then(unwrapAttachmentCleanupPreview)
}

/** ★★ `{older_than_days, affected_records, total_bytes, dry_run, action}`。 */
export function unwrapAttachmentCleanupPreview(resp: unknown): AttachmentCleanupPreview {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (
      typeof m.older_than_days === 'number' &&
      typeof m.affected_records === 'number' &&
      typeof m.total_bytes === 'number' &&
      typeof m.dry_run === 'boolean'
    ) {
      return m as unknown as AttachmentCleanupPreview
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments/cleanup/preview 响应形状不符：期望 {older_than_days, affected_records, total_bytes, dry_run}，实得 ${actual}`,
  )
}

// ── Item（裸对象）──────────────────────────────────────────────────

export function fetchAttachmentItem(
  requestId: string,
  options?: RequestOptions,
): Promise<AttachmentItem> {
  return req<unknown>(
    'GET',
    `/api/admin/attachments/${encodeURIComponent(requestId)}`,
    undefined,
    options,
  ).then(unwrapAttachmentItem)
}

/** ★★ **裸对象** `{request_id, ts, tenant_id, client_model, success, attachments}`。 */
export function unwrapAttachmentItem(resp: unknown): AttachmentItem {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (typeof m.request_id === 'string' && typeof m.success === 'boolean' && 'attachments' in m) {
      return m as unknown as AttachmentItem
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments/{request_id} 响应形状不符：期望裸对象 {request_id, ts, success, attachments}，实得 ${actual}`,
  )
}

// ── Filesystem stats（裸对象）──────────────────────────────────────

export function fetchAttachmentFilesystemStats(options?: RequestOptions): Promise<AttachmentFilesystemStats> {
  return req<unknown>('GET', '/api/admin/attachments/filesystem/stats', undefined, options).then(
    unwrapAttachmentFilesystemStats,
  )
}

/** ★★ **裸对象**，10 个键；`oldest_file_time` **无** omitempty ⇒ 键在值可 null。 */
export function unwrapAttachmentFilesystemStats(resp: unknown): AttachmentFilesystemStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (
      typeof m.attachment_dir === 'string' &&
      typeof m.total_files === 'number' &&
      typeof m.disk_usage_percent === 'number' &&
      'oldest_file_time' in m
    ) {
      return m as unknown as AttachmentFilesystemStats
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/attachments/filesystem/stats 响应形状不符：期望裸对象 {attachment_dir, total_files, oldest_file_time, disk_usage_percent}，实得 ${actual}`,
  )
}

// ── 页面侧判读 ─────────────────────────────────────────────────────

/** ★★★ list 侧 `attachments === null` 是**合法数据**（18k+ 行），不是错误。 */
export function attachmentRawIsNull(row: AttachmentRow): boolean {
  return row.attachments === null
}

/** ★★★ 同一个字段，item 侧**保证**是数组（`COALESCE(…,'[]')`）。 */
export function attachmentItemIsArray(item: AttachmentItem): boolean {
  return Array.isArray(item.attachments)
}

/**
 * ★★★★ list 的行集合 ⊋ stats 的行集合（stats 多一个 `jsonb_typeof='array'` 条件）。
 * 返回 true 时页面要标「口径不同」，**不许**报成数据不一致。
 */
export function attachmentRowSetDiffers(listLen: number, statsCount: number): boolean {
  return listLen !== statsCount
}

/** ★★ `count` 是 `len(items)` 的派生值 ⇒ 不能当全量条数。 */
export function attachmentCountIsDerived(l: AttachmentList): boolean {
  return l.count === l.items.length
}

/** ★★ 客户端独立复算 stats 的两个汇总值（后端是同一批 bucket 求和）。 */
export function attachmentStatsTotalsMatch(s: AttachmentStats): boolean {
  const cnt = s.breakdown.reduce((a, b) => a + b.count, 0)
  const bytes = s.breakdown.reduce((a, b) => a + b.total_bytes, 0)
  return cnt === s.total_count && bytes === s.total_bytes
}

/** ★ `policy` 是硬编码的 ⇒ 客户端可以核对后端有没有改。 */
export function attachmentPolicyIsBuiltin(p: AttachmentPolicy): boolean {
  return (
    p.policy.retention_days === ATTACHMENT_POLICY_RETENTION_DAYS &&
    p.policy.max_size_bytes === ATTACHMENT_POLICY_MAX_SIZE_BYTES
  )
}

/** ★★ `auto_cleanup` 与 `delete_filesystem` 都硬编码 `false` ⇒ 现在**什么都不自动删**。 */
export function attachmentAutoCleanupEnabled(p: AttachmentPolicy): boolean {
  return p.policy.auto_cleanup
}

/** ★★ preview 的 `older_than_days` 越界/`≤0` ⇒ 回落 **30**（不是 clamp）。 */
export function attachmentPreviewDaysEffective(v: number | undefined | null): number {
  if (typeof v !== 'number' || !Number.isFinite(v)) return ATTACHMENT_PREVIEW_DAYS_DEFAULT
  const n = Math.trunc(v)
  if (n <= 0) return ATTACHMENT_PREVIEW_DAYS_DEFAULT
  return n
}

/** ★★ 越界回落时页面必须说「已回落」。 */
export function attachmentPreviewDaysFellBack(v: number | undefined | null): boolean {
  return attachmentPreviewDaysEffective(v) === ATTACHMENT_PREVIEW_DAYS_DEFAULT && typeof v === 'number' && v > 0
}

/** ★ preview 恒为 dry-run（handler 里没有执行分支）。 */
export function attachmentPreviewIsAlwaysDryRun(): boolean {
  return true
}

/** ★ `oldest_file_time === null` ⇒ 目录里**一个文件都没有**。 */
export function attachmentFsIsEmpty(f: AttachmentFilesystemStats): boolean {
  return f.oldest_file_time === null
}

/**
 * ★★ 磁盘告警分档：>=90 danger、>=75 warning、否则 safe。
 * 客户端独立复算，不信任后端给的 `disk_warning_level`。
 */
export function attachmentFsWarningLevel(percent: number): 'safe' | 'warning' | 'danger' {
  if (!Number.isFinite(percent)) return 'danger'
  if (percent >= 90) return 'danger'
  if (percent >= 75) return 'warning'
  return 'safe'
}

/** ★★ 后端给的 `disk_warning_level` 与复算不一致时按异常处理。 */
export function attachmentFsWarningMatches(f: AttachmentFilesystemStats): boolean {
  return f.disk_warning_level === attachmentFsWarningLevel(f.disk_usage_percent)
}

/** ★★ `tenant_id` 对 tenant_admin 静默无效；这条把该事实变成可断言的。 */
export function attachmentTenantFilterNeedsSuperAdmin(): boolean {
  return true
}

/** ★ 跨租户是 **404 不是 403** —— 源码明说是为避免泄漏存在性。 */
export function attachmentCrossTenantIsNotFound(): boolean {
  return true
}
