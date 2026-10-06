// modelPolicies.ts — 租户模型策略子树（**superAdmin 档**，只读三端点）。
//   GET  /api/admin/tenants/{code}/model-policies?include_deleted=
//   POST /api/admin/tenants/{code}/model-policies/check
//   GET  /api/admin/tenants/{code}/model-policies/audit?limit=
//
// ⚠️ 档位（**纠正后端文件头注释**）：`admin/model_policies.go:10-11` 把 check/audit
//     标成 `(admin)`，**这是过时的**。真相是 `admin/handler.go:926-927`：
//         mux.HandleFunc("/api/admin/tenants",  h.superAdmin(h.handleTenants))
//         mux.HandleFunc("/api/admin/tenants/", h.superAdmin(h.handleTenants))
//     `handleTenantModelPolicies` **只有**这一个调用点（`admin/tenants.go:188-190`）
//     ⇒ 整棵子树（含 check 与 audit）**都是 superAdmin 硬门槛**。
//     ★★ `SuperAdminMiddleware` 只认 JWT，且 `claims.Role != "super_admin"` 即 403；
//         `sk-...` 那类 legacy Bearer token 走不到 handler，直接 401。
//     ⇒ `canAdministerTenant`（model_policies.go:659-672）里的
//         `case "tenant_admin"` 与 `case "admin_key"` 两个分支
//         **在这棵子树上都是死代码** —— tenant_admin 在中间件就被 403 挡掉了。
//         ★★★ 也就是说：**租户自己的 model-policy，租户管理员也管不了**，必须 super_admin。
//         页面不要按「本租户管理员可自助」的文案写。
//
// ⚠️⚠️⚠️ 三条只读端点对「租户码拼错」的响应**三样都不一样**（本批头号发现）：
//     · list  ⇒ **404 `tenant not found`**
//              （model_policies.go:173-178 先 `SELECT EXISTS(...) FROM tenants`）
//              ★★ 但那里的条件是 `err != nil || !exists` —— **数据库查询出错也被报成
//                 404 `tenant not found`** ⇒ 客户端**分辨不出**「租户不存在」与
//                 「tenants 表这一行查失败」。不是 500，不是 503。
//     · audit ⇒ **200 + 空数组**（`withTenantTx` **不校验租户存在**，
//              admin/tenant_ctx.go 只 SET GUC 就查，查不到行而已）
//     · check ⇒ **200 + `exists:false`**（见下条）
//
// ★★★★★★ check 端点**完全不做租户隔离**：函数签名收了 `tenantCode string`，
//     但 SQL（model_policies.go:564-570）里只有 `WHERE lower(mc.canonical_name)=lower($1)`，
//     **tenantCode 一个字都没用**。查的是全局 `models_canonical`。
//     ⇒ 在**任意**租户名下 check 任何 canonical name，结果完全相同。
//     ⇒ 页面不能把 check 描述成「本租户可用的模型」——它是**全局模型名录**校验。
//
// ★★★★ check 的匹配条件（两条都不是「存在即真」）：
//     `lower(canonical_name) = lower($1)`            ← **大小写不敏感**
//     `AND COALESCE(mc.status,'active') = 'active'`  ← **非 active 的规范模型报不存在**
//     ⇒ `exists:false` 的成因至少有三种：真的没这个名字 / 名字对但被下线了 /
//       `models_canonical` 里没这一行。页面不许把它渲染成「模型不存在（拼错了）」。
//
// ★★★★ `vendor` 字段**后端从不赋值**：`TenantModelPolicyCheckResp.Vendor` 全文件
//     只有声明（model_policies.go:79），`omitempty` + 从不写 ⇒ **响应里永远没有这个键**。
//     源码注释自陈「Returning empty string is acceptable for the UI which falls back to
//     "Unknown vendor"」⇒ **本 TS 接口故意不声明 `vendor`**。
//     页面必须显式显示「后端不提供厂商」，不许渲染成一个空白格。
//
// ★★★★ `modality` **恒有值**：`resp := {..., Modality: "text"}` 是初值，
//     命中才用 `COALESCE(NULLIF(TRIM(mc.modality),''),'text')` 覆盖。
//     ⇒ `exists:false` 时 `modality` 是字面量 `"text"`，**不是「没有模态」**。
//     同理 `family` 带 omitempty ⇒ 只有 `exists:true` **且** `mc.family IS NOT NULL`
//     才有 `family` 键；`exists:false` ⇒ 键整个不存在。
//
// ★★★★★ `count` 是**派生值**、恒等于数组长度：model_policies.go:218-222 是
//     `{"policies": out, "count": len(out)}`，`out` 就是同一个切片
//     ⇒ ★★★ `count` **永远** `=== policies.length`，
//         **既不是**全库条数，**也证明不了**扫描没丢行（见下条）。
//
// ★★★★ scan 失败静默丢行：`for rows.Next() { if serr := rows.Scan(...); serr != nil
//     { continue } }`（model_policies.go:202-211，audit 侧 631-641 同）
//     ⇒ 返回条数**可能**少于库里真实行数。页面不能把条数当「全量」。
//
// ★★★ `policies` / `audit` 都是 `make([]T, 0)` ⇒ **永不为 null**，空就是 `[]`。
//     （与 tenants 那族的「裸数组」不同，这里是 `{policies, count, tenant}` 包信封。）
//
// ★★★ `include_deleted` 只认**字面** `"true"`（model_policies.go:180
//     `r.URL.Query().Get("include_deleted") == "true"`）
//     ⇒ `1` / `TRUE` / `yes` / 空 一律当 **false**，**静默少返回软删除行**，不报错。
//
// ★★★★ `limit` 越界是**回落默认值 100**，不是 clamp 到边界
//     （model_policies.go:596-601：`if n, err := Atoi(s); err == nil && n > 0 && n <= 500
//     { limit = n }`，否则保持 100）
//     ⇒ `limit=0` ⇒ 100、`limit=-1` ⇒ 100、`limit=501` ⇒ **100**（不是 500！）、
//       `limit=abc` ⇒ 100。⇒ **本仓第九种限幅语义**，且与 MaaS 的
//       `ClampUsageLimit`（>50⇒50）**方向相反**。
//     且 audit 是 `ORDER BY ts DESC LIMIT $2`，**没有 OFFSET、没有游标** ⇒
//     翻历史只能调**小** limit（拿最近的 N 条），拿不到更老的。
//
// ★★★ 三个 handler 的 ctx 预算各不相同：list 5s / audit 5s / **check 3s**。
//
// ★★★★ audit 表由**数据库触发器**写，不是 `h.writeAuditLog`（那是另一张通用审计表）。
//     函数体 `sql/objects/functions/tenant_model_policies_audit_fn.sql`，三条事实：
//     (a) ★★ `actor` 的兜底是 `COALESCE(NULLIF(current_setting('app.current_admin',true),''),'system')`
//         ⇒ **`actor === 'system'` 表示当时没设 `app.current_admin` GUC**，
//           不是「某个叫 system 的账号」。页面要把它单列成「无署名」。
//     (b) ★ `delete` 那行写的是 **`OLD.reason`**（删除**前**的理由），
//         而 `insert`/`update`/`undelete` 写 `NEW.reason`
//         ⇒ 同一条策略的「理由」在审计里会**前后不一致**，这是设计不是数据错。
//     (c) ★ `UPDATE` 只有在 `deleted_at` 变了、**或** `reason`/`canonical_name` 变了
//         时才落审计行 ⇒ 一次「改成同样的值」的 PATCH **不留任何审计痕迹**。
//     (d) 触发器写 `policy_id` 时**总**填 `NEW.id`/`OLD.id`
//         ⇒ 走触发器产生的行 `policy_id` 恒在；列本身可空，键缺失只可能来自
//           非触发器写入的行。
//
// ★★★★ audit 表对「租户不存在」**不报错**：读路径 `withTenantTx`
//     （admin/tenant_ctx.go）只 `SET LOCAL app.current_tenant` 就查，
//     **不校验租户存在** ⇒ 租户码拼错 ⇒ **200 + `{audit: [], count: 0}`**。
//     对比同子树的 list 会 **404 `tenant not found`**。
//     ⇒ ★★★ 空审计列表**不能**当成「这个租户没有策略变更过」。
//
// ★★ 租户码含 `/` 时**到不了**这些端点：`admin/tenants.go:145` 用
//     `strings.SplitN(r.URL.Path, "/", 2)`，而 `r.URL.Path` 是**已解码**路径
//     ⇒ `%2F` 会被解回真斜杠再切坏。前端按既有约定 encode，但那只防 URL 层面歧义，
//     **防不住 Go 的解码**；含 `/` 的租户码是后端限制，404 `unknown sub-resource`。
//
// ★ 软删除是**真删除**（`SET deleted_at = now()`）+ 审计行保留；`{id}/undelete` 恢复。
//     本页**不碰**任何写操作。
//
// ★★ 写操作本页一律不碰：POST /model-policies（创建）、PATCH /{id}（改 reason）、
//     DELETE /{id}（软删）、POST /{id}/undelete（恢复）。

import type { RequestOptions } from './client'
import { req } from './client'

/** ★★ audit 的 limit 默认值；越界时**回落**到它（不是 clamp）。 */
export const MODEL_POLICY_AUDIT_LIMIT_DEFAULT = 100
/** ★★ audit 的 limit 上界；`> 上界` 回落默认值而非 clamp 到上界。 */
export const MODEL_POLICY_AUDIT_LIMIT_MAX = 500

/** ★ check 的 ctx 预算是 **3s**（list/audit 都是 5s）——超时更容易踩到。 */
export const MODEL_POLICY_CHECK_CTX_SECONDS = 3

/**
 * ★★★★ 审计动作**集合是闭合的**：`tenant_model_policies_audit` 上有
 *   `CHECK (action = ANY (ARRAY['insert','update','delete','undelete']))`
 * （`sql/objects/tables/tenant_model_policies_audit.sql`）
 * ⇒ 库里不可能出现别的动作。★ 注意第一个是 **`insert`**，不是 `create`。
 */
export const MODEL_POLICY_AUDIT_ACTIONS = ['insert', 'update', 'delete', 'undelete'] as const

export type TenantPolicyEndpoint = 'list' | 'audit' | 'check'

/**
 * ★★★★★ 三条只读端点对「租户不存在」的响应**三样都不一样**：
 *     · list  ⇒ 404 `tenant not found`
 *     · audit ⇒ 200 + 空数组（`withTenantTx` 不校验租户存在）
 *     · check ⇒ 200 + `exists:false`（而且 check 连 tenantCode 都不参与 SQL）
 * ⇒ 只有 list 能报「租户不存在」；audit 的空列表**不能**当成「这个租户没有策略」。
 */
export const TENANT_POLICY_TENANT_MISSING: Readonly<
  Record<TenantPolicyEndpoint, { httpStatus: number; shape: 'error' | 'empty-200' | 'exists-false-200' }>
> = Object.freeze({
  list: { httpStatus: 404, shape: 'error' },
  audit: { httpStatus: 200, shape: 'empty-200' },
  check: { httpStatus: 200, shape: 'exists-false-200' },
})

/** ★ 只有 list 会用状态码报「租户不存在」；另两条静默。 */
export function tenantPolicyTenantMissingIsError(ep: TenantPolicyEndpoint): boolean {
  return TENANT_POLICY_TENANT_MISSING[ep].shape === 'error'
}

// ── 线格式 ─────────────────────────────────────────────────────────

export interface TenantModelPolicy {
  id: number
  tenant_id: string
  canonical_name: string
  reason: string
  created_by: string
  /** ★ 带 omitempty 的指针 ⇒ 键不存在 = **未软删**。 */
  deleted_at?: string
  /** ★ 带 omitempty 的指针。 */
  deleted_by?: string
  created_at: string
  updated_at: string
}

export interface TenantModelPolicyList {
  /** ★ `make([]T,0)` ⇒ 永不为 null。 */
  policies: TenantModelPolicy[]
  /** ★★ 派生自 `len(out)` ⇒ **恒等于** `policies.length`，不是全库条数。 */
  count: number
  tenant: string
}

export interface TenantModelPolicyCheck {
  exists: boolean
  /** ★ 后端回显 `strings.TrimSpace` 之后的名字，不是原样回显。 */
  canonical_name: string
  /** ★ omitempty ⇒ 仅 `exists:true` 且 `mc.family IS NOT NULL` 时存在。 */
  family?: string
  /** ★ 恒有值：初值 `"text"`，命中才被覆盖。`exists:false` 时也是 `"text"`。 */
  modality: string
  /**
   * ★★ 故意**不声明** `vendor`：后端 `TenantModelPolicyCheckResp.Vendor`
   * 全文件从不赋值 + `omitempty` ⇒ 响应里**永远没有**这个键。
   * 页面要显示厂商，只能显式说「后端不提供」，不许渲染成空白。
   */
}

export interface TenantModelPolicyAuditRow {
  id: number
  ts: string
  action: string
  /** ★ 带 omitempty 的指针 ⇒ 键不存在 = 这条审计**没挂到具体策略**。 */
  policy_id?: number
  tenant_id: string
  canonical_name: string
  reason: string
  actor: string
}

export interface TenantModelPolicyAudit {
  /** ★ `make([]T,0)` ⇒ 永不为 null。 */
  audit: TenantModelPolicyAuditRow[]
  /** ★★ 派生自 `len(out)` ⇒ 恒等于 `audit.length`。 */
  count: number
}

function tenantPath(code: string, sub?: string): string {
  // ★ 沿用既有约定；但见头注释：后端在**已解码** Path 上 SplitN ⇒ 含 `/` 的码到不了。
  return `/api/admin/tenants/${encodeURIComponent(code)}${sub ? '/' + sub : ''}`
}

// ── List ──────────────────────────────────────────────────────────

export interface ModelPolicyListParams {
  includeDeleted?: boolean
}

/** ★ 后端只认字面 `"true"`，这里照抄；见 `backendSeesIncludeDeleted`。 */
export function modelPolicyIncludeDeletedWire(includeDeleted: boolean): string {
  return includeDeleted ? 'true' : 'false'
}

/**
 * ★★ 后端 `r.URL.Query().Get("include_deleted") == "true"`，**只认字面 `"true"`**。
 * `1` / `TRUE` / `yes` / 空 一律当 false ⇒ **静默少返回软删除行**，不报错。
 */
export function backendSeesIncludeDeleted(raw: string | null | undefined): boolean {
  return raw === 'true'
}

export function fetchTenantModelPolicies(
  code: string,
  params: ModelPolicyListParams = {},
  options?: RequestOptions,
): Promise<TenantModelPolicyList> {
  const qs = new URLSearchParams()
  if (params.includeDeleted !== undefined) {
    qs.set('include_deleted', modelPolicyIncludeDeletedWire(params.includeDeleted))
  }
  const s = qs.toString()
  return req<unknown>('GET', `${tenantPath(code, 'model-policies')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapTenantModelPolicies,
  )
}

/** ★★ 响应是 `{policies, count, tenant}` 信封，**不是**裸数组，也不是 `{items}`。 */
export function unwrapTenantModelPolicies(resp: unknown): TenantModelPolicyList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (Array.isArray(m.policies) && typeof m.count === 'number' && typeof m.tenant === 'string') {
      return m as unknown as TenantModelPolicyList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/tenants/{code}/model-policies 响应形状不符：期望 {policies, count, tenant}，实得 ${actual}`,
  )
}

// ── Check ─────────────────────────────────────────────────────────

export function checkTenantModelPolicy(
  code: string,
  canonicalName: string,
  options?: RequestOptions,
): Promise<TenantModelPolicyCheck> {
  return req<unknown>(
    'POST',
    tenantPath(code, 'model-policies/check'),
    { canonical_name: canonicalName },
    options,
  ).then(unwrapTenantModelPolicyCheck)
}

/** ★★ 响应是**裸对象** `{exists, canonical_name, family?, modality}`，**没有**任何信封。 */
export function unwrapTenantModelPolicyCheck(resp: unknown): TenantModelPolicyCheck {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const c = resp as Record<string, unknown>
    if (typeof c.exists === 'boolean' && typeof c.canonical_name === 'string' && typeof c.modality === 'string') {
      return c as unknown as TenantModelPolicyCheck
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/tenants/{code}/model-policies/check 响应形状不符：期望裸对象 {exists, canonical_name, modality}，实得 ${actual}`,
  )
}

// ── Audit ─────────────────────────────────────────────────────────

export function fetchTenantModelPolicyAudit(
  code: string,
  params: { limit?: number } = {},
  options?: RequestOptions,
): Promise<TenantModelPolicyAudit> {
  const qs = new URLSearchParams()
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    qs.set('limit', String(Math.trunc(params.limit)))
  }
  const s = qs.toString()
  return req<unknown>('GET', `${tenantPath(code, 'model-policies/audit')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapTenantModelPolicyAudit,
  )
}

/** ★★ 响应是 `{audit, count}` 信封（**没有** `tenant` 键 —— 与 list 不同）。 */
export function unwrapTenantModelPolicyAudit(resp: unknown): TenantModelPolicyAudit {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (Array.isArray(m.audit) && typeof m.count === 'number') {
      return m as unknown as TenantModelPolicyAudit
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `admin/tenants/{code}/model-policies/audit 响应形状不符：期望 {audit, count}，实得 ${actual}`,
  )
}

// ── 页面侧判读 ─────────────────────────────────────────────────────

/**
 * ★★★★ audit 的 limit **回落语义**：`n > 0 && n <= 500` 才采纳，否则**回落 100**。
 * ⇒ `0`/`-1`/`501`/`abc` 都得 100；`501` **不是** 500。
 *
 * ★ 注意**两层**：`fetchTenantModelPolicyAudit` 先 `Math.trunc` 再进 URL
 *   ⇒ 后端 `strconv.Atoi` 拿到的一定是合法整数字符串。
 *   本函数按「已经截断过的值」判定，所以 `500.9 → 500`。
 *   若哪天把 URL 里的 `Math.trunc` 去掉，后端 `Atoi("500.9")` 会**失败**
 *   ⇒ 静默回落 100 ⇒ 页面显示的条数与实际不符。
 */
export function modelPolicyAuditLimitEffective(limit: unknown): number {
  const n = typeof limit === 'number' && Number.isFinite(limit) ? Math.trunc(limit) : Number.NaN
  if (Number.isNaN(n) || n <= 0 || n > MODEL_POLICY_AUDIT_LIMIT_MAX) {
    return MODEL_POLICY_AUDIT_LIMIT_DEFAULT
  }
  return n
}

/** ★ 越界时会**回落**到默认值而不是 clamp ⇒ 返回 true 时页面必须显示「已回落」。 */
export function modelPolicyAuditLimitFellBack(limit: unknown): boolean {
  const n = typeof limit === 'number' && Number.isFinite(limit) ? Math.trunc(limit) : Number.NaN
  if (Number.isNaN(n)) return true
  return n <= 0 || n > MODEL_POLICY_AUDIT_LIMIT_MAX
}

/** ★ 已软删（`deleted_at` 是非空字符串）。 */
export function modelPolicySoftDeleted(p: TenantModelPolicy): boolean {
  return typeof p.deleted_at === 'string' && p.deleted_at.length > 0
}

/** ★ 未软删：键**整个不存在**（`omitempty` 的 nil 指针），不是 `deleted_at === null`。 */
export function modelPolicyActive(p: TenantModelPolicy): boolean {
  return !('deleted_at' in p)
}

/** ★ 删除人；键不存在 ⇒ 无从得知（不是「系统删的」）。 */
export function modelPolicyDeletedBy(p: TenantModelPolicy): string | null {
  return typeof p.deleted_by === 'string' && p.deleted_by.length > 0 ? p.deleted_by : null
}

/** ★ 软删行数。 */
export function modelPolicyDeletedCount(list: TenantModelPolicyList): number {
  return list.policies.filter(modelPolicySoftDeleted).length
}

/**
 * ★★★ `count` 是 `len(policies)` 的**派生值**（后端 `count: len(out)`，同一个切片）
 * ⇒ 它**恒等于**数组长度，**不能**当「全库策略数」显示。
 * 这个函数存在的意义是把「冗余」这件事变成可断言的事实。
 */
export function modelPolicyCountIsDerived(list: TenantModelPolicyList): boolean {
  return list.count === list.policies.length
}

/** ★★★ `exists:false` 有多种成因（未登记 / 大小写外的写法 / **已下线**），不许断言「拼错了」。 */
export function modelPolicyCheckIsUnknown(c: TenantModelPolicyCheck): boolean {
  return c.exists === false
}

/** ★ `family` 键不存在 ≠ 「无族」——`exists:true` 但 `mc.family IS NULL` 也是键不存在。 */
export function modelPolicyCheckHasFamily(c: TenantModelPolicyCheck): boolean {
  return 'family' in c
}

/** ★★ 厂商后端**永不提供**（字段从不赋值 + omitempty）⇒ 恒为 true，页面据此显示说明。 */
export function modelPolicyCheckVendorUnavailable(): boolean {
  return true
}

/** ★ 审计行是否挂到了具体策略；键不存在 = 没挂上。 */
export function modelPolicyAuditHasPolicyRef(a: TenantModelPolicyAuditRow): boolean {
  return typeof a.policy_id === 'number'
}

/** ★ 审计只给**最近 N 条**（`ORDER BY ts DESC LIMIT n`，无 OFFSET/游标）⇒ 没有「更早」。 */
export function modelPolicyAuditHasNoOlderPage(): boolean {
  return true
}

/**
 * ★★★★ 审计动作**集合是闭合的**：`sql/objects/tables/tenant_model_policies_audit.sql`
 * 上有 `CHECK (action = ANY (ARRAY['insert','update','delete','undelete']))`
 * ⇒ 库里不可能出现别的动作，页面不必准备「未知动作」分支。
 * ★ 第一个是 **`insert`** 不是 `create` —— 别按 REST 动词去猜。
 */
export function modelPolicyAuditKnownActions(): readonly string[] {
  return MODEL_POLICY_AUDIT_ACTIONS
}