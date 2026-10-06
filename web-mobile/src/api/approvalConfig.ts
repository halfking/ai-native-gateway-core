// approvalConfig.ts — 租户审批配置面（**admin 档**，只读四端点）。
//   GET /api/admin/tenant-approval-config/{code}/approval-config
//   GET /api/admin/tenant-approval-config/{code}/approvers
//   GET /api/admin/tenant-approval-config/{code}/approval-rules
//   GET /api/admin/tenant-approval-config/{code}/approval-config/stats
//
// ⚠️⚠️⚠️ 路径是 **`tenant-approval-config`（单数 tenant）**，不是 `tenants`：
//     `cmd/gateway/main.go:7367` 起的注册用的是
//         mux.HandleFunc("/api/admin/tenant-approval-config/", …)
//     而 `/api/admin/tenants/` 归 `admin/handler.go:926-927` 的
//     `h.superAdmin(h.handleTenants)` 所有。
//     ★ 打**复数** `/api/admin/tenants/{code}/approval-config` 会落进
//       `handleTenants` 的 `unknown sub-resource: approval-config` **404** 分支。
//     （2026-07-03 改前缀就是为了避开 ServeMux 冲突，两条曾同时注册导致启动 panic。）
//
// ⚠️ 档位是 **admin**（不是 superAdmin）：这一族的 handler 走
//     `wrapAdmin(...)`，而 `cmd/gateway/main_admin_wrappers.go:26-30` 里
//         newWrapAdmin = func(fn) { return admin.AdminMiddleware(fn, pool, secret) }
//     ⇒ `h.admin` 语义：**tenant_admin 可用**。
//     ⇒ 抽屉席**不设** `requiresRole`（与 model-policies 那族正好相反）。
//     ★ 但 handler 内部**还有一道** `canAccessTenant` / `canModifyTenant`
//       （approval_config_handler.go:452-490），两者都额外放行 `admin_key`
//       角色，中间件那道 `requiresRole` 没有建模。
//
// ⚠️⚠️⚠️ **整族挂载是有条件的**（main.go:7358）：
//     `if dbConn != nil && dbConn.Enabled() && redisClientForCache != nil { … }`
//     ★ **Redis 没起 ⇒ 这整个前缀根本没有注册** ⇒ 请求走 Go mux 的默认行为。
//     页面必须能区分「这个租户没有审批配置」与「这族压根没开」——
//     前者 200 + 合成默认值，后者是**路由层**的 404。
//
// ★★ 派发是 `strings.Contains` **逐条 case**（main.go:7369-7396），不是路径解析：
//     `case strings.Contains(path, "/approval-config/stats")` 在最前，
//     然后 `/approval-config` + GET/PUT，再 `/approvers/` + PUT/DELETE，
//     `/approvers` + GET/POST，`/approval-rules/` + DELETE，
//     `/approval-rules` + GET/POST，最后 `default: http.NotFound(w, r)`。
//     ★★ `default` 分支给的是 **`http.NotFound`** ⇒ **裸文本** `404 page not found`，
//        **不是** `{"error":{"detail":…}}` 信封。
//     ★★★ 后端缺陷（只记录不修）：`Contains` 不看段边界 ⇒ 若租户码让路径里出现
//        `/approval-config` / `/approvers` / `/approval-rules` 子串（例如租户码就叫
//        `approval-config`），GET 会被**派发到错误的 handler**。
//        `extractTenantID`（:413-424）反而是对的，它按**段**匹配
//        `tenant-approval-config` 或 `tenants` 取后一段。
//
// ★★★★★★ 头号发现：`GetConfig` 对**没有配置行的租户**返回**合成默认值**
//     （domains/approval/store.go:401-414）：
//         if errors.Is(err, pgx.ErrNoRows) {
//             return &ApprovalConfig{
//                 TenantID: tenantID, Enabled: false, Mode: ModeDisabled,
//                 TimeoutSeconds: 3600, AutoRejectOnTimeout: true,
//                 Approvers: []Approver{}, Channels: []NotificationChannel{},
//                 Rules: []ApprovalRule{},
//             }, nil
//         }
//     ⇒ ★★★ **没有「租户不存在」的 404**：任何过了鉴权的租户码都会拿到 200。
//     ⇒ ★★★ 而且 `timeout_seconds: 3600` 与 `auto_reject_on_timeout: true`
//         **是凭空造的**，库里没有这行 ⇒ 页面把它们显示成真实配置就是**假读数**。
//     ⇒ ★ 唯一能分辨「从未配置过」的标志是 **`created_at` / `updated_at` 是零值时间**
//         （Go 把零值 time.Time 序列化成 `"0001-01-01T00:00:00Z"`，且这两个键**无** omitempty）。
//     ⇒ 本族**没有** `count` 这种「派生值」问题在 config 上，但 stats 那边有（见下）。
//
// ★★★★★ 第二个头号发现：`/approvers` 与 `/approval-rules` 的 SQL **都带
//     `AND enabled = true`**（store.go:389 / :431）：
//         WHERE tenant_id = $1 AND enabled = true
//     ⇒ ★★★ **被停用的审批人 / 规则根本不在这两个列表里。**
//     ⇒ ★★ 而它们读的是 **`approval_approvers` / `approval_rules` 表**；
//        `config.approvers` / `config.rules` 与 `stats.*_count` 读的是
//        **`approval_configs.config` 那个 JSONB 列**。
//        ⇒ **两个数据源**，条数可以**对不上**，而且这不是 bug 是设计分层。
//     ⇒ 页面把 `/approvers` 的条数和 `stats.approver_count` 并排显示时
//        **必须**标注口径不同，否则读起来像数据不一致。
//
// ★★★★ 第三个：空列表序列化成 **`null`**，不是 `[]`。
//     `store.go:390` 是 `var approvers []Approver`（**nil 切片**，不是 `make([]T,0)`），
//     `GetRules` 同理 ⇒ 无行时 `writeJSON` 把 nil 切片写成 **`null`**，
//     而 `count` 是 `len(nil)` = **0**。
//     ⇒ `{"approvers": null, "count": 0}` 是**合法成功响应**，
//        解包器**不许**因为 `approvers === null` 就抛错。
//     ★ 与 model-policies 那族**正好相反**：那边是 `make([]T,0)` ⇒ 永不为 null。
//
// ★★★ 第四个：`config` / `stats` 是**裸对象**（`writeJSON(w, 200, config)`），
//     `approvers` / `rules` 才是 `{…, count}` 信封。
//     ⇒ 四个端点**两两形状互不包含**，可以互喂判据。
//
// ★★★ 第五个：`stats` 是 `config` 的**纯函数**（config_manager.go:435-472）：
//     approver_count/rule_count/channel_count 来自 `len(config.X)`，
//     enabled_* 来自「数 config.X 里 enabled 的个数」，
//     `last_updated` 就是 `config.updated_at`，`mode`/`enabled`/`timeout_seconds` 直接搬。
//     ⇒ 11 个字段**全部可由 config 独立复算** ⇒ 页面并排显示两者时必须一致，
//        这也是本族最强的一条判据（见 `approvalStatsMatchesConfig`）。
//
// ★★ `count`（approvers/rules 侧）又是**派生值**（`len(...)`），不是全表条数。
//
// ★★ `COALESCE(email,'')` / `COALESCE(phone,'')` + `omitempty`
//     ⇒ 空邮箱 / 空手机**整个键不存在**（不是空串、不是 null）。
//     `Name` / `UserID` / `Role` / `Priority` / `Enabled` 无 omitempty ⇒ 键一定在。
//
// ★★ 排序方向**相反**，且各按自己的语义（结构体注释写得很清楚）：
//     approvers `ORDER BY priority ASC`  ← `// Lower number = higher priority`
//     rules     `ORDER BY priority DESC` ← `// Higher number = higher priority`
//
// ★ `NotificationChannel.Config` 是 `map[string]string` 且**无** omitempty
//     ⇒ nil map 序列化成 **`null`**（不是 `{}`）。
// `RuleCondition` 三字段 `field`/`operator`/`value` 全部无 omitempty。
// `RuleAction` 三字段 `type`/`risk_level`/`reason` 全部无 omitempty。
//
// ★ 枚举：`Mode` = disabled/automatic/manual；`ChannelType` = feishu/wechat/dingtalk/
//   email/webhook；`RiskLevel` = LOW/MEDIUM/HIGH/CRITICAL；
//   `RuleAction.Type` = require_approval/auto_approve/auto_reject；
//   `RuleCondition.Operator` = contains/gt/lt/eq/regex。
//   这些都是**注释里声明**的，不是数据库 CHECK ⇒ 库里出现别的值是可能的，页面按未知渲染。
//
// ★★ 写操作本页一律不碰：PUT /approval-config、POST/PUT/DELETE /approvers[/{user_id}]、
//     POST/DELETE /approval-rules[/{rule_name}]。
//     `AddRule` 成功是 **201**，其余写端点是 200。

import type { RequestOptions } from './client'
import { req } from './client'

/** ★ Go 零值 time.Time 的 JSON 形态；也是「这张租户**从未配置过**」的判别标志。 */
export const APPROVAL_ZERO_TIME = '0001-01-01T00:00:00Z'

/** ★★ 合成默认值里凭空造出来的两个数（库里没有这行）。 */
export const APPROVAL_SYNTHETIC_TIMEOUT_SECONDS = 3600
export const APPROVAL_SYNTHETIC_AUTO_REJECT = true

// ── 线格式 ─────────────────────────────────────────────────────────

export interface Approver {
  user_id: string
  name: string
  /** ★ 空串会被 omitempty 省掉 ⇒ 键不存在。 */
  email?: string
  /** ★ 同上。 */
  phone?: string
  /** admin / auditor / manager（注释声明，非 CHECK）。 */
  role: string
  /** ★ **数小优先级高**（`ORDER BY priority ASC`）。 */
  priority: number
  enabled: boolean
}

export interface NotificationChannel {
  type: string
  /** ★ map 无 omitempty ⇒ nil map 序列化成 `null`（不是 `{}`）。 */
  config: Record<string, string> | null
  enabled: boolean
}

export interface RuleCondition {
  /** message_content / token_count / cost / tool_name */
  field: string
  /** contains / gt / lt / eq / regex */
  operator: string
  value: string
}

export interface RuleAction {
  /** require_approval / auto_approve / auto_reject */
  type: string
  /** LOW / MEDIUM / HIGH / CRITICAL */
  risk_level: string
  reason: string
}

export interface ApprovalRule {
  name: string
  enabled: boolean
  /** ★ **数大优先级高**（`ORDER BY priority DESC`），与 approvers 相反。 */
  priority: number
  conditions: RuleCondition[] | null
  action: RuleAction
}

export interface ApprovalConfig {
  tenant_id: string
  enabled: boolean
  /** disabled / automatic / manual */
  mode: string
  approvers: Approver[] | null
  channels: NotificationChannel[] | null
  /** ★ `timeout_seconds` 可能是**合成**出来的 3600。 */
  timeout_seconds: number
  auto_reject_on_timeout: boolean
  rules: ApprovalRule[] | null
  /** ★ 零值时间 ⇒ 这张租户**从未配置过**。 */
  created_at: string
  updated_at: string
}

export interface ApprovalConfigStats {
  tenant_id: string
  enabled: boolean
  mode: string
  /** ★ 来自 **JSONB 列**的 `len(config.Approvers)`，含停用的。 */
  approver_count: number
  /** JSONB 里 enabled 的个数。 */
  enabled_approvers: number
  rule_count: number
  enabled_rules: number
  channel_count: number
  enabled_channels: number
  timeout_seconds: number
  /** ★ 就是 `config.updated_at`（从未配置过时是零值时间）。 */
  last_updated: string
}

export interface ApproverList {
  /** ★★ 空时是 **`null`**（nil 切片），不是 `[]`。 */
  approvers: Approver[] | null
  /** ★ 派生值 `len(approvers)`。 */
  count: number
}

export interface ApprovalRuleList {
  /** ★★ 空时是 **`null`**。 */
  rules: ApprovalRule[] | null
  /** ★ 派生值 `len(rules)`。 */
  count: number
}

function acPath(code: string, sub: string): string {
  // ★ 单数 tenant-approval-config。租户码按既有约定 encode。
  return `/api/admin/tenant-approval-config/${encodeURIComponent(code)}/${sub}`
}

// ── Config（裸对象）────────────────────────────────────────────────

export function fetchApprovalConfig(
  code: string,
  options?: RequestOptions,
): Promise<ApprovalConfig> {
  return req<unknown>('GET', acPath(code, 'approval-config'), undefined, options).then(
    unwrapApprovalConfig,
  )
}

/**
 * ★★ 响应是**裸对象** `{tenant_id, enabled, mode, …}`，**没有**任何信封。
 *
 * ★★★★★ 判别键必须**多于三个**：`ConfigStats` 的键是 `ApprovalConfig` 的
 *     **真子集**（tenant_id / enabled / mode 三个它都有，而且类型全对）
 *     ⇒ 只按那三个判，**stats 会被当 config 放行**，
 *     而 stats 缺 `approvers` / `channels` / `rules` / `timeout_seconds` /
 *     `auto_reject_on_timeout` ⇒ 页面会拿着 stats 渲染出三个 `undefined`。
 *     补上的两个键（`timeout_seconds` / `auto_reject_on_timeout`）在 Go 侧
 *     **都没有** `omitempty` ⇒ 真实响应里必然存在，不是为了排他而编的。
 */
export function unwrapApprovalConfig(resp: unknown): ApprovalConfig {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const c = resp as Record<string, unknown>
    if (
      typeof c.tenant_id === 'string' &&
      typeof c.enabled === 'boolean' &&
      typeof c.mode === 'string' &&
      typeof c.timeout_seconds === 'number' &&
      typeof c.auto_reject_on_timeout === 'boolean'
    ) {
      return c as unknown as ApprovalConfig
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `tenant-approval-config/{id}/approval-config 响应形状不符：期望裸对象 {tenant_id, enabled, mode, timeout_seconds, auto_reject_on_timeout}，实得 ${actual}`,
  )
}

// ── Stats（裸对象）────────────────────────────────────────────────

export function fetchApprovalConfigStats(
  code: string,
  options?: RequestOptions,
): Promise<ApprovalConfigStats> {
  return req<unknown>('GET', acPath(code, 'approval-config/stats'), undefined, options).then(
    unwrapApprovalConfigStats,
  )
}

/** ★★ 也是**裸对象**；`approver_count` 等计数全是 `number`（无 omitempty ⇒ 键一定在）。 */
export function unwrapApprovalConfigStats(resp: unknown): ApprovalConfigStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const s = resp as Record<string, unknown>
    if (
      typeof s.tenant_id === 'string' &&
      typeof s.approver_count === 'number' &&
      typeof s.rule_count === 'number' &&
      typeof s.enabled_approvers === 'number'
    ) {
      return s as unknown as ApprovalConfigStats
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `tenant-approval-config/{id}/approval-config/stats 响应形状不符：期望裸对象 {tenant_id, approver_count, rule_count}，实得 ${actual}`,
  )
}

// ── Approvers（信封，数组可为 null）────────────────────────────────

export function fetchApprovalApprovers(
  code: string,
  options?: RequestOptions,
): Promise<ApproverList> {
  return req<unknown>('GET', acPath(code, 'approvers'), undefined, options).then(unwrapApprovalApprovers)
}

/**
 * ★★★ `approvers` **可以是 `null`**（后端 `var approvers []Approver` 是 nil 切片，
 * 空时序列化成 `null`）⇒ 形状校验**不能**要求它是数组，
 * 否则对一个完全正常的「这个租户没有启用中的审批人」响应抛错。
 */
export function unwrapApprovalApprovers(resp: unknown): ApproverList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (
      'approvers' in m &&
      (m.approvers === null || Array.isArray(m.approvers)) &&
      typeof m.count === 'number'
    ) {
      return m as unknown as ApproverList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `tenant-approval-config/{id}/approvers 响应形状不符：期望 {approvers|null, count}，实得 ${actual}`,
  )
}

// ── Rules（信封，数组可为 null）────────────────────────────────────

export function fetchApprovalRules(code: string, options?: RequestOptions): Promise<ApprovalRuleList> {
  return req<unknown>('GET', acPath(code, 'approval-rules'), undefined, options).then(
    unwrapApprovalRules,
  )
}

/** ★★★ 同 approvers：`rules` 空时是 `null`，不是 `[]`。 */
export function unwrapApprovalRules(resp: unknown): ApprovalRuleList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if ('rules' in m && (m.rules === null || Array.isArray(m.rules)) && typeof m.count === 'number') {
      return m as unknown as ApprovalRuleList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `tenant-approval-config/{id}/approval-rules 响应形状不符：期望 {rules|null, count}，实得 ${actual}`,
  )
}

// ── 页面侧判读 ─────────────────────────────────────────────────────

/** ★★★ 这张租户**从未配置过**（后端合成了默认配置，不是查到了真的）。 */
export function approvalConfigIsSynthesized(c: ApprovalConfig): boolean {
  return c.created_at === APPROVAL_ZERO_TIME || c.updated_at === APPROVAL_ZERO_TIME
}

/** ★ 从未配置过时这两个数是**凭空造的**，页面必须标注、不能当真实配置显示。 */
export function approvalTimeoutMayBeSynthetic(c: ApprovalConfig): boolean {
  return approvalConfigIsSynthesized(c) && c.timeout_seconds === APPROVAL_SYNTHETIC_TIMEOUT_SECONDS
}

export function approvalAutoRejectMayBeSynthetic(c: ApprovalConfig): boolean {
  return approvalConfigIsSynthesized(c) && c.auto_reject_on_timeout === APPROVAL_SYNTHETIC_AUTO_REJECT
}

/** ★ `mode` 三值之外按未知渲染（枚举来自注释，不是数据库 CHECK）。 */
export function approvalModeTone(mode: string): 'success' | 'warning' | 'muted' {
  if (mode === 'manual') return 'warning'
  if (mode === 'automatic') return 'success'
  // disabled 与任何未知值都归 muted（未知不猜）
  return 'muted'
}

/** ★ 审批人里的「数小优先」⇒ 列表首个优先级最高。 */
export function approvalTopApprover(list: Approver[] | null): Approver | null {
  if (!list || list.length === 0) return null
  return list[0] ?? null
}

/** ★ 规则里的「数大优先」⇒ 列表首个优先级最高。 */
export function approvalTopRule(list: ApprovalRule[] | null): ApprovalRule | null {
  if (!list || list.length === 0) return null
  return list[0] ?? null
}

/** ★★ `/approvers` 只回 `enabled = true` 的行 ⇒ 这里的条数**不是**总审批人数。 */
export function approverListIsEnabledOnly(): boolean {
  return true
}

/** ★★ 同上，`/approval-rules` 也只回 `enabled = true` 的行。 */
export function ruleListIsEnabledOnly(): boolean {
  return true
}

/** ★★ `count` 是 `len(...)` 的派生值 ⇒ 不能当全表条数。 */
export function approverCountIsDerived(l: ApproverList): boolean {
  return l.count === (l.approvers === null ? 0 : l.approvers.length)
}

/** ★★ 同上。 */
export function ruleCountIsDerived(l: ApprovalRuleList): boolean {
  return l.count === (l.rules === null ? 0 : l.rules.length)
}

/** ★ 通知渠道配置是 `null`（nil map）时**没有**可显示的键值对。 */
export function channelHasNoConfig(c: NotificationChannel): boolean {
  return c.config === null
}

/** ★ 规则条件是 `null` 时 ⇒ 这条规则**一个条件都没有**（不是「条件未知」）。 */
export function ruleHasNoConditions(r: ApprovalRule): boolean {
  return r.conditions === null
}

/**
 * ★★★★★ stats 是 config 的**纯函数**，11 个字段全部可由 config 复算
 *     （config_manager.go:441-469）。返回 true 时页面应显示「两者一致」。
 * 客户端独立复算，不信任后端给的数。
 */
export function approvalStatsMatchesConfig(
  c: ApprovalConfig,
  s: ApprovalConfigStats,
): boolean {
  const app = c.approvers ?? []
  const rules = c.rules ?? []
  const chans = c.channels ?? []
  return (
    s.tenant_id === c.tenant_id &&
    s.enabled === c.enabled &&
    s.mode === c.mode &&
    s.approver_count === app.length &&
    s.enabled_approvers === app.filter((a) => a.enabled).length &&
    s.rule_count === rules.length &&
    s.enabled_rules === rules.filter((r) => r.enabled).length &&
    s.channel_count === chans.length &&
    s.enabled_channels === chans.filter((x) => x.enabled).length &&
    s.timeout_seconds === c.timeout_seconds &&
    s.last_updated === c.updated_at
  )
}

/**
 * ★★★★★★ `/approvers` 读的是 `approval_approvers` 表且**只回 enabled**，
 *     而 `stats.approver_count` 读的是 JSONB 列且**含停用的**
 *     ⇒ 两者条数**可以对不上**，而且**这不是数据错**。
 * 页面并排显示时必须标口径，返回 true 表示「差值是预期的，不要报警」。
 */
export function approverCountGapIsExpected(stats: ApprovalConfigStats, listLen: number): boolean {
  return stats.approver_count !== listLen
}

/** ★ 规则同理。 */
export function ruleCountGapIsExpected(stats: ApprovalConfigStats, listLen: number): boolean {
  return stats.rule_count !== listLen
}

/** ★ 空邮箱 / 空手机是**键不存在**（COALESCE 成空串再被 omitempty 省掉）。 */
export function approverHasEmail(a: Approver): boolean {
  return 'email' in a
}

export function approverHasPhone(a: Approver): boolean {
  return 'phone' in a
}
