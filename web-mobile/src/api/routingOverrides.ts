import { req, type RequestOptions } from './client'

// routingOverrides.ts — 路由**覆盖规则**的完整生命周期。
//   GET    /api/admin/routing/overrides          列出当前规则
//   POST   /api/admin/routing/overrides          新建
//   DELETE /api/admin/routing/overrides/{id}     停用（软删）
//   PATCH  /api/admin/routing/overrides/{id}/extend  延长有效期
//
// ⚠️★ 整条线是 **superAdmin**：`handler.go:1381` 把
//   `RegisterAutoRouteRoutes(mux, h.superAdmin)`，且内部所有路由又套一层
//   `adminWrap` ⇒ 非 super_admin 一律 403。
//   ⇒ 移动端导航必须 `requiresRole: 'super_admin'`（与 /integrity、/routing-audit 同）。
//   这是排障线之外的第二条超管线：它直接改生产路由面。
//
// 它与已上移的 `/routing-audit` 是一对：
//   overrides  = **现在生效的规则是什么**
//   audit      = **这些规则是谁在什么时候改的**
// 只看审计会不知道当前状态；只看规则会不知道是不是刚被人动过。
//
// ───────────────────────────────────────────────────────────────────────────
// ★★ 四个坑：
//
// (1) **`filter` 回显的三个值全是字符串**（routing_overrides.go 的
//     `map[string]string`），包括 `active` —— 后端是
//     `strconv.FormatBool(activeOnly)`。当成 boolean 用会得到 `undefined`
//     而不是 `false`。
//
// (2) **DELETE 是软删**：SQL 是 `SET expires_at = NOW() - INTERVAL '1 second'`
//     （deleteRoutingOverride）。行**仍留在表里**。
//     ⇒ 删完之后用不带 `active=true` 的列表查，它**还会出现**。
//     UI 必须在删除后用 `active=true` 刷新，否则用户会以为删除失败。
//
// (3) **409 重复**：(task_type, profile, model_chosen, mode) 四元组重复 ⇒
//     "an override with the same (...) already exists"。
//     这不是并发问题，是业务规则 —— 新建前应先查列表。
//
// (4) **只有 `task_type` 和 `reason` 必填**（control/routing/create.go:167-185），
//     `profile` / `mode` / `model_chosen` 是**自由文本、无枚举校验**。
//     后端不告诉调用方哪些值合法 ⇒ UI 不能凭空枚举，只能
//     **拿现有规则的值做候选**（见 `knownProfiles` / `knownModes`），
//     否则用户只能靠猜，而猜错的后果是规则不生效或与预期不符。

export type RoutingOverrideMode = string

export interface RoutingOverride {
  id: number
  task_type: string
  profile: string
  mode: RoutingOverrideMode
  /** 指针 + omitempty ⇒ 可能「键不存在」或 null */
  model_chosen?: string | null
  reason: string
  created_by?: string | null
  /** RFC3339。★ 已过期的行仍在返回里（软删不删行） */
  expires_at?: string | null
  created_at: string
  updated_at: string
  [k: string]: unknown
}

/** 回显的过滤条件。★ 三个值都是 **string**，`active` 也是（"true"/"false"）。 */
export interface RoutingOverridesFilterEcho {
  task_type: string
  profile: string
  active: string
}

export interface RoutingOverridesResponse {
  overrides: RoutingOverride[] | null
  count: number
  filter: RoutingOverridesFilterEcho
}

export interface RoutingOverridesParams {
  /** `active=true` 只返回未过期的 */
  active?: boolean
  task_type?: string
  profile?: string
}

export function fetchRoutingOverrides(
  params?: RoutingOverridesParams,
  options?: RequestOptions,
): Promise<RoutingOverridesResponse> {
  const qs = new URLSearchParams()
  // ★ 后端判的是 `== "true"`，所以必须发字符串 "true"；不发 / 发 "false" 都算不过滤
  if (params?.active === true) qs.set('active', 'true')
  if (params?.task_type) qs.set('task_type', params.task_type)
  if (params?.profile) qs.set('profile', params.profile)
  const s = qs.toString()
  return req<RoutingOverridesResponse>('GET', `/api/admin/routing/overrides${s ? '?' + s : ''}`, undefined, options)
}

export interface CreateRoutingOverrideReq {
  /** 必填（空 → 400） */
  task_type: string
  /** 自由文本，但建议从现有规则里取 */
  profile: string
  /** 自由文本，同上 */
  mode: RoutingOverrideMode
  model_chosen?: string | null
  /** 必填（空 → 400） */
  reason: string
  /** RFC3339。省略 = 永不过期。 */
  expires_at?: string | null
}

export interface CreateRoutingOverrideResult {
  id: number
  status: string
  /** 后端固定带一句「OverrideStore refreshes on the next 1-min reload」。 */
  message: string
}

/**
 * 新建规则 → **201**。
 *
 * ⚠️ 201 的 message 明说「下一个 1-min reload 内生效」——
 *   写完**立刻**去查路由解析结果多半看不到新规则。
 *   UI 成功文案必须带上这个延迟，否则用户会以为没生效而重复提交
 *   —— 而重复提交会撞 409（同一四元组已存在），越急越错。
 */
export function createRoutingOverride(
  body: CreateRoutingOverrideReq,
  options?: RequestOptions,
): Promise<CreateRoutingOverrideResult> {
  return req<CreateRoutingOverrideResult>('POST', '/api/admin/routing/overrides', body, options)
}

export interface WriteOk {
  status?: string
  message?: string
  [k: string]: unknown
}

/** 停用。★ 软删：行还在，只是 expires_at 被设成过去。 */
export function deleteRoutingOverride(id: number, options?: RequestOptions): Promise<WriteOk> {
  return req<WriteOk>('DELETE', `/api/admin/routing/overrides/${encodeURIComponent(String(id))}`, undefined, options)
}

/** 延长有效期。body 形如 `{expires_at: "2026-10-08T00:00:00Z"}`。 */
export function extendRoutingOverride(
  id: number,
  expiresAt: string,
  options?: RequestOptions,
): Promise<WriteOk> {
  return req<WriteOk>(
    'PATCH',
    `/api/admin/routing/overrides/${encodeURIComponent(String(id))}/extend`,
    { expires_at: expiresAt },
    options,
  )
}

// ── 纯函数辅助 ─────────────────────────────────────────────────────────────

/** 规则是否已过期。`expires_at` 缺失 = 永不过期。 */
export function isExpired(o: RoutingOverride, now = Date.now()): boolean {
  if (!o.expires_at) return false
  const t = Date.parse(o.expires_at)
  return Number.isFinite(t) && t <= now
}

/**
 * 从现有规则里取 `profile` / `mode` 的候选值。
 *
 * ★ 后端对这两个字段**没有枚举**（create.go 只校验 task_type 与 reason），
 *   也不提供「合法值列表」端点 ⇒ 唯一可靠的候选来源就是**已经在跑的规则**。
 *   自由输入仍然允许（后端接受），但 UI 应优先给候选。
 */
export function knownProfiles(rows: readonly RoutingOverride[]): string[] {
  return [...new Set(rows.map((r) => r.profile).filter((p) => p !== ''))].sort()
}

export function knownModes(rows: readonly RoutingOverride[]): string[] {
  return [...new Set(rows.map((r) => r.mode).filter((m) => m !== ''))].sort()
}

export function knownTaskTypes(rows: readonly RoutingOverride[]): string[] {
  return [...new Set(rows.map((r) => r.task_type).filter((t) => t !== ''))].sort()
}

/** 规则的一句话摘要：`task_type / profile / mode → model_chosen`。 */
export function overrideSummary(o: RoutingOverride): string {
  const head = [o.task_type, o.profile, o.mode].filter((x) => x !== '').join(' / ')
  return o.model_chosen ? `${head} → ${o.model_chosen}` : head
}
