// modules.ts — 功能模块面（**admin 档**，只读三端点）。
//   GET /api/admin/modules
//   GET /api/admin/modules/{key}
//   GET /api/admin/modules/{key}/config
//
// ⚠️ 注册**不在** `admin/handler.go`，而在 `admin/modules.go:1129-1134` 的
//   `registerModuleRoutes` 里：
//       mux.HandleFunc("/api/admin/modules",  h.admin(h.handleModulesList))
//       mux.HandleFunc("/api/admin/modules/", h.admin(h.handleModulesRouter))
//   ★ 我第一轮只 grep 了 `handler.go`，差点误判成「这族根本没注册」——
//     **grep 路由注册必须限定到全仓**，不能只搜那一个文件。
//
// 档位：两条都是 **`h.admin(...)`** ⇒ tenant_admin 可用
//   ⇒ 抽屉席**不设** `requiresRole`。
//   ★ handler 内部还有 `settings.Global == nil` 这道判（不是角色判）。
//
// ★★★★ `resolveModuleEnabled`（`admin/modules.go:628-648`）有**五条**失败路径
//   全部返回 `(true, "default")`：
//       1. m.SettingKey == ""            （模块压根没有开关）
//       2. settings.Global.Spec(key)==nil（spec 不存在）
//       3. EffectiveValue 返回 err
//       4. raw == nil                     （值为 NULL）
//       5. json.Unmarshal 成 bool 失败     （值不是布尔）
//   ⇒ ★★★ `source === "default"` 同时表示「静态默认」与「**读失败**」，
//     客户端**分辨不出** ⇒ 不许把 `enabled:true + source:"default"`
//     渲染成「已按配置启用」。
//
// ★★★★★★ 复核 `settings/spec.go:284-317` 又查出**第六条**同形来源：
//   `EffectiveValue` 的优先级链是 **DB > env > default**，第 3 步直接
//   `return b, "default", nil`（把 spec 自己的 `Default` marshal 出来）。
//   ⇒ `source==="default"` 实际有**三种**成因，客户端**都分辨不出**：
//       (a) 上面五条失败路径之一
//       (b) spec 的静态默认值恰好是 true（真·默认，不是故障）
//       (c) 值为 NULL 走了回落
//   ⇒ 正确措辞是「**没能读到配置**」，不是「故障」，也不是「按配置启用」。
//
// ★★★ `source` 的取值集合由 `EffectiveValue` 的 doc 注释写死：
//   `source ∈ {"db","env","default"}`。**没有别的值。**
//   ⇒ 只有 `db` / `env` 才代表「真读到了某处显式配置」。
//
// ★★★ `blocked_reason` 与 `can_toggle_enabled` 是**联动**的，且带 omitempty：
//   moduleStatusMap（`:731-738`）里 `CanToggleEnabled = (blocked == "")`，
//   而 `BlockedReason string \`json:"blocked_reason,omitempty"\``（`:60`）
//   ⇒ 「没有阻塞」= **键整个不存在**，不是空串。
//   ⇒ `blocked_reason` 非空时形如 `需先启用依赖模块: A、B`。
//
// ★★★★ `config` 里的键会**静默缺失**（`handleModulesGet` 的 `:816-833`）：
//   for _, ck := range found.ConfigKeys {
//       sp := settings.Global.Spec(ck); if sp == nil { continue }      ← 静默跳过
//       raw, src2, err := …EffectiveValue(…); if err != nil { continue } ← 静默跳过
//       config[ck] = map[string]any{"value": v, "source": src2, "spec": sp}
//   }
//   ⇒ ★★★ **`config` 里没有某个 `config_keys` 里声明的键**，有三种成因：
//     spec 不存在 / 取值出错 / 反序列化没产出值。
//     ⇒ 「config 里没这个键」**不等于**「这个配置没设」。
//     ⇒ 页面必须把 `config_keys`（声明）与 `config`（实际取到的）**并排显示**，
//     差集就是「读不出来的那部分」。
//
// ★★ `/config` 子端点**只有 `feishu_bot` 实现**，其它模块一律
//   **501 Not Implemented**（`config endpoint not implemented for module: <key>`）
//   ⇒ ★ 这是本仓**第一次**出现 501（此前见过 400/403/404/405/500/503/504）。
//
// ★★★★★★ 复核源码又查出一条**第三态**（我第一轮测绘漏了）：
//   `handleModulesGet` 的 config 循环（`:816-833`）是
//       raw, src2, err := …EffectiveValue(…); if err != nil { continue }
//       var v any; jsoncol.Decode(…, raw, &v)      ← **没有 `raw == nil` 判断**
//   对比 `resolveModuleEnabled` 是有 `if raw == nil` 的。
//   ⇒ ★★★ `raw` 为 NULL 时**不会**被 continue 跳过，而是 Decode 失败、
//     `v` 保持 nil，**键照样进 config**，于是 `value: null`。
//   ⇒ 每个声明键有**三**态，不是两态：
//       1. **键整个不在** config 里     ⇒ spec 不存在 **或** EffectiveValue 报错
//       2. **键在但 value 是 null**     ⇒ 值为 NULL（读到了，只是没值）
//       3. **键在且有值**
//   ⇒ 只报「缺了哪些键」会**把第 2 态误并进第 1 态**。
//
// ★★ `POST /api/admin/modules/{key}/test` **不是只读端点**：
//   handler 注释明说「对 feishu_bot：发送一条测试消息到 webhook_url」
//   ⇒ 它有**外部副作用**（真给飞书机器人发消息）⇒ 本页一律不碰，
//   尽管名字叫「test」。
//
// ★★ 503 的**两种文案**（都不是角色问题，是 settings 注册表没起来）：
//   list / get ⇒ `settings registry not initialised`
//   feishubot 配置摘要 ⇒ `settings not initialised`
//
// ★ 子树分派（`handleModulesRouter`）：
//   `GET  /{key}`        ⇒ 模块详情
//   `PUT  /{key}/toggle` ⇒ 开关（写）
//   `POST /{key}/test`   ⇒ 连通性测试（**有副作用**，不碰）
//   `GET  /{key}/config` ⇒ 配置摘要
//   其余（含空 key、`/{key}/unknown`）⇒ **404 `unknown modules endpoint`**
//
// ★ `items` 是 `make([]ModuleWithStatus, 0, len(defs))` ⇒ **永不为 null**。
// ★ `config` 是 `make(map[string]any)` ⇒ 无键时序列化成 **`{}`**（不是 null）。
// ★ `integration` / `dependencies` 在 `ModuleDefinition` 上带 omitempty ⇒ 可整个不存在。

import type { RequestOptions } from './client'
import { req } from './client'

/** ★ 只有 `feishu_bot` 实现了 `/config`，其余一律 501。 */
export const MODULES_WITH_CONFIG_ENDPOINT = ['feishu_bot'] as const

/** ★ `resolveModuleEnabled` 读失败时的兜底 source。 */
export const MODULES_FALLBACK_SOURCE = 'default'

/**
 * ★★★ `source` 的**全部**可能取值 —— 由 `settings/spec.go:284-283` 的 doc 注释写死：
 *   `source ∈ {"db","env","default"}`。**只有这三个。**
 *   ★ 我第一版测试夹具里编了 `'platform'` / `'tenant'` 两个不存在的值 ——
 *     夹具必须照抄后端，不能凭印象。已修正。
 */
export const MODULES_REAL_SOURCES = ['db', 'env'] as const

/** ★ `source` 落在 `MODULES_REAL_SOURCES` 之外（按实现只能是 `'default'`）。 */
export function modulesSourceIsKnown(source: string): boolean {
  return source === 'db' || source === 'env' || source === 'default'
}

export interface ModuleDependency {
  key: string
  name: string
  /** ★ 依赖是否「必需」——只有 required 的才会进 `blocked_reason`。 */
  required: boolean
  /** ★ 只在 `moduleStatusMap` 的第二轮里被填上；单模块详情里可能缺。 */
  enabled?: boolean
}

export interface ModuleIntegration {
  [k: string]: unknown
}

/** ★ 抄自 `ModuleDefinition`（`admin/modules.go:21-34`）。 */
export interface ModuleDefinition {
  key: string
  name: string
  description: string
  capabilities: string[]
  icon: string
  category: string
  /** ★ 为空 ⇒ `resolveModuleEnabled` 直接返回 `(true,"default")`。 */
  setting_key: string
  /** ★ 声明了哪些配置键；**实际取到的**可能比它少（见头注释）。 */
  config_keys: string[]
  docs_url: string
  danger_level: string
  integration?: ModuleIntegration
  dependencies?: ModuleDependency[]
}

/** ★ 抄自 `ModuleWithStatus`（`admin/modules.go:55-61`）。 */
export interface ModuleWithStatus extends ModuleDefinition {
  enabled: boolean
  /** ★ 读失败时也是 `"default"` —— 与「静态默认」**不可分辨**。 */
  source: string
  can_toggle_enabled: boolean
  /** ★ omitempty ⇒ 键不存在 = **没有阻塞**（不是空串）。 */
  blocked_reason?: string
}

/** ★ `GET /api/admin/modules/{key}` 里 `config[ck]` 的形状（`:829-833`）。 */
export interface ModuleConfigEntry {
  value: unknown
  source: string
  spec: unknown
}

export interface ModuleList {
  /** ★ `make(..., 0, len(defs))` ⇒ 永不为 null。 */
  items: ModuleWithStatus[]
}

export interface ModuleDetail {
  module: ModuleWithStatus
  /** ★ `make(map[string]any)` ⇒ 无键时是 `{}`，不是 null。 */
  config: Record<string, ModuleConfigEntry>
}

export function modulesPath(key?: string, sub?: string): string {
  // ★ key 也要 encode：后端 `strings.TrimPrefix(r.URL.Path, …)` + `strings.Split(…, "/")`
  const base = '/api/admin/modules'
  if (!key) return base
  return `${base}/${encodeURIComponent(key)}${sub ? '/' + sub : ''}`
}

// ── List ───────────────────────────────────────────────────────────

export function fetchModules(options?: RequestOptions): Promise<ModuleList> {
  return req<unknown>('GET', modulesPath(), undefined, options).then(unwrapModules)
}

/** ★★ `{items: [...]}` 信封。 */
export function unwrapModules(resp: unknown): ModuleList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (Array.isArray(m.items)) return m as unknown as ModuleList
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/modules 响应形状不符：期望 {items: [...]}，实得 ${actual}`)
}

// ── Detail ─────────────────────────────────────────────────────────

export function fetchModule(key: string, options?: RequestOptions): Promise<ModuleDetail> {
  return req<unknown>('GET', modulesPath(key), undefined, options).then(unwrapModule)
}

/** ★★ `{module, config}`。★ `config` 里**可能缺** `config_keys` 声明的键。 */
export function unwrapModule(resp: unknown): ModuleDetail {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const mod = m.module
    if (mod && typeof mod === 'object' && !Array.isArray(mod) && m.config && typeof m.config === 'object') {
      const mm = mod as Record<string, unknown>
      if (typeof mm.key === 'string' && typeof mm.enabled === 'boolean' && !Array.isArray(m.config)) {
        return m as unknown as ModuleDetail
      }
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/modules/{key} 响应形状不符：期望 {module: {key, enabled, …}, config: {…}}，实得 ${actual}`)
}

// ── Config 子端点（只有 feishu_bot 有）──────────────────────────────

/**
 * ★★ 只有 `feishu_bot` 实现了 `/config`，其余模块后端一律返 **501**。
 *   响应**没有信封**：本身就是那 20 个键（见 `MODULE_CONFIG_SUMMARY_KEYS`）。
 *   ⇒ 解包靠「20 键一个都不能少」，不靠 `items`。
 */
export function fetchModuleConfig(key: string, options?: RequestOptions): Promise<FeishuBotConfigSummary> {
  return req<unknown>('GET', modulesPath(key, 'config'), undefined, options).then(unwrapModuleConfigSummary)
}

/** ★★ 只有 `feishu_bot` 实现了 `/config`，其余模块后端一律返 **501**。 */
export function moduleHasConfigEndpoint(key: string): boolean {
  return (MODULES_WITH_CONFIG_ENDPOINT as readonly string[]).includes(key)
}

/**
 * ★★★★★★ **跨端点自相矛盾**：`GET /{key}` 与 `GET /{key}/config` 读的是**同一个**
 *   设置键 `feishu_bot.enabled`，但走的判定函数不同：
 *   · 模块面 `resolveModuleEnabled` 有**五条**失败路径，都 fallback 到 `(true,"default")`
 *   · 配置摘要 `readBool` 的失败路径 fallback 到 **`false`**（Go 零值）
 *   ⇒ ★★★ 读失败时，`/{key}` 报 `enabled: true`、`/config` 报 `enabled: false`。
 *   ⇒ 两个端点**能对不上**，这不是 bug 是设计后果 ⇒ 页面必须原样呈现两处，
 *     不许挑一个信、也不许悄悄 reconcile。
 */
export function moduleEnabledDisagrees(moduleEnabled: boolean, configEnabled: boolean): boolean {
  return moduleEnabled !== configEnabled
}

// ── 页面侧判读 ─────────────────────────────────────────────────────

/**
 * ★★★★★★ `enabled: true` **可能是读不出来的兜底**（`resolveModuleEnabled` 的五条失败路径
 *     全部返回 `(true, "default")`，而 `EffectiveValue` 第 3 步回落 spec 默认值时
 *     **也**返回 `("default")`）⇒ 返回 true 时页面**不许**说「已按配置启用」。
 */
export function moduleEnabledMayBeFallback(m: ModuleWithStatus): boolean {
  return m.enabled === true && m.source === MODULES_FALLBACK_SOURCE
}

/**
 * ★★★ 只有 `source ∈ {"db","env"}` 才代表**真读到了某处显式配置**
 *   （`EffectiveValue` 的 doc 注释写死的取值集合）。
 *   ⇒ `source === "default"` 时**不能**区分「没配 / 读失败 / spec 默认」三种成因。
 */
export function moduleEnabledWasReallyRead(m: ModuleWithStatus): boolean {
  return (MODULES_REAL_SOURCES as readonly string[]).includes(m.source)
}

/** ★★★★ 「没有阻塞」= `blocked_reason` **键不存在**（omitempty），不是空串。 */
export function moduleIsBlocked(m: ModuleWithStatus): boolean {
  return typeof m.blocked_reason === 'string' && m.blocked_reason.length > 0
}

/** ★ `can_toggle_enabled` 与「有没有阻塞」**联动**（后端 `= (blocked == "")`）。 */
export function moduleCanToggle(m: ModuleWithStatus): boolean {
  return m.can_toggle_enabled
}

/** ★★★ `config_keys` 里声明了、但 `config` 里**没取到**的那些键。 */
export function moduleMissingConfigKeys(d: ModuleDetail): string[] {
  return (d.module.config_keys ?? []).filter((k) => !(k in d.config))
}

/** ★ 页面必须把「声明」与「实际取到」并排显示，差集就是读不出来的那部分。 */
export function moduleConfigCoverage(d: ModuleDetail): { declared: number; resolved: number } {
  const declared = d.module.config_keys ?? []
  return { declared: declared.length, resolved: declared.length - moduleMissingConfigKeys(d).length }
}

/**
 * ★★★★★★ 第 2 态：键**在** `config` 里，但 `value` 是 `null`。
 *   `handleModulesGet` 的循环**没有** `raw == nil` 判断 ⇒ 值为 NULL 的键
 *   照样进 map（`jsoncol.Decode` 失败、`v` 保持 nil）。
 *   ⇒ 这一态**既不是**「没配置」**也不是**「读失败」，必须单列。
 */
export function moduleNullConfigKeys(d: ModuleDetail): string[] {
  return (d.module.config_keys ?? []).filter((k) => k in d.config && d.config[k]?.value === null)
}

/** ★★★ 逐键三态分类。页面按这三态分别措辞，不许合并。 */
export function moduleConfigKeyState(
  d: ModuleDetail,
  key: string,
): 'absent' | 'null-value' | 'resolved' {
  if (!(key in d.config)) return 'absent'
  return d.config[key]?.value === null ? 'null-value' : 'resolved'
}

export interface ModuleConfigStates {
  absent: string[]
  nullValue: string[]
  resolved: string[]
}

/** ★★★ 三态一次性分类：缺键 / 值为 null / 真取到。 */
export function moduleConfigStates(d: ModuleDetail): ModuleConfigStates {
  const declared = d.module.config_keys ?? []
  const out: ModuleConfigStates = { absent: [], nullValue: [], resolved: [] }
  for (const k of declared) {
    const s = moduleConfigKeyState(d, k)
    if (s === 'absent') out.absent.push(k)
    else if (s === 'null-value') out.nullValue.push(k)
    else out.resolved.push(k)
  }
  return out
}

/** ★ 三态计数。`resolved + nullValue` 才是「后端真的答了」的键数。 */
export function moduleConfigStateCounts(d: ModuleDetail): {
  declared: number
  absent: number
  nullValue: number
  resolved: number
} {
  const s = moduleConfigStates(d)
  return {
    declared: s.absent.length + s.nullValue.length + s.resolved.length,
    absent: s.absent.length,
    nullValue: s.nullValue.length,
    resolved: s.resolved.length,
  }
}

/** ★ 模块自己的 `config_keys` 为空（`setting_key` 也可能是空串）。 */
export function moduleHasNoSettingKey(m: ModuleWithStatus): boolean {
  return m.setting_key === ''
}

/** ★ `setting_key` 是空的 ⇒ 后端**根本没查**任何设置，直接 `(true,"default")`。 */
export function moduleIsAlwaysOn(m: ModuleWithStatus): boolean {
  return m.setting_key === '' && moduleEnabledMayBeFallback(m)
}

/** ★ 只列**必需依赖**（后端 `blocked_reason` 也只统计 `dep.Required` 的）。 */
export function moduleRequiredDepNames(m: ModuleWithStatus): string[] {
  return (m.dependencies ?? []).filter((d) => d.required).map((d) => d.name)
}

/** ★ `danger_level` 来自 `settings.DangerLevel`；枚举外的值按未知渲染。 */
export function moduleDangerTone(level: string): 'danger' | 'warning' | 'muted' {
  if (level === 'danger') return 'danger'
  if (level === 'warning') return 'warning'
  return 'muted'
}

/** ★ `capabilities` 可能是空数组（Go 里 `[]string{}` 序列化成 `[]`）。 */
export function moduleHasNoCapabilities(m: ModuleWithStatus): boolean {
  return (m.capabilities ?? []).length === 0
}

/** ★★ `POST /{key}/test` **有外部副作用**（真给飞书机器人发消息）⇒ 本页绝不调用。 */
export function moduleTestHasSideEffect(): boolean {
  return true
}

/** ★ 503 有**两种文案**（都是 settings 注册表没起，不是角色问题）。 */
export function modulesSettingsNotInitialised(msg: string): boolean {
  return /settings registry not initialised|settings not initialised/i.test(msg)
}

// ── feishu_bot 的 /config 摘要（`feishuBotConfigSummary`，`:1289-1357`）────────────

/**
 * ★★ 这 20 个键在 `summary := map[string]any{}` 上是**逐个无条件赋值**的
 *   ⇒ 响应里**这 20 个键永远都在**，一个都不会少。
 *   ⇒ 少任何一个 = 形状不符，抛错。
 */
export const MODULE_CONFIG_SUMMARY_BOOL_KEYS = [
  'enabled',
  'webhook_url_set',
  'verify_token_set',
  'encrypt_key_set',
  'notify_on_alert',
  'notify_on_approval',
  'quiet_hours_enabled',
  'approval_mention_crit',
  'commands_enabled',
  'commands_admin_only',
  'signature_required',
] as const

export const MODULE_CONFIG_SUMMARY_INT_KEYS = [
  'allowed_user_count',
  'alert_rate_limit_min',
  'alert_dedup_window_sec',
  'approval_expiry_min',
  'timestamp_window_sec',
] as const

export const MODULE_CONFIG_SUMMARY_STRING_KEYS = [
  'connection_mode',
  'alert_severity_min',
  'quiet_hours_window',
  'card_template',
] as const

export const MODULE_CONFIG_SUMMARY_KEYS: readonly string[] = [
  ...MODULE_CONFIG_SUMMARY_BOOL_KEYS,
  ...MODULE_CONFIG_SUMMARY_INT_KEYS,
  ...MODULE_CONFIG_SUMMARY_STRING_KEYS,
]

export type FeishuBotConfigSummary = Record<string, boolean | number | string>

/**
 * ★★ 解包 `GET /api/admin/modules/feishu_bot/config` 的响应。
 *   与别的端点不同：**没有信封**，响应本身就是那 20 个键 ⇒ 判据是
 *   「20 个键一个都不能少」，不是「有没有 items」。
 *   ★ 少一个键就抛错，不返 `{}`：空对象和「真没配」在页面上长得一样。
 */
export function unwrapModuleConfigSummary(resp: unknown): FeishuBotConfigSummary {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const missing = MODULE_CONFIG_SUMMARY_KEYS.filter((k) => !(k in m))
    if (missing.length === 0) return m as FeishuBotConfigSummary
    throw new Error(`admin/modules/feishu_bot/config 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/modules/feishu_bot/config 响应形状不符：期望 20 键扁平对象，实得 ${actual}`)
}

/**
 * ★★★★★★ `allowed_user_count` 的真身：`len(strings.Split(readString("feishu_bot.allowed_users"), ","))`。
 *   而 `readString` 在 spec 不存在 / EffectiveValue 报错 / raw 为 NULL / 解码失败
 *   四条路径上**全部返回 `""`**。
 *   ⇒ ★★★ `strings.Split("", ",")` 返回 `[""]`，**长度是 1**。
 *   ⇒ **`allowed_user_count: 1` 不可分辨**「一个都没配」与「恰好允许了 1 个人」。
 *   ⇒ 页面只能说「至少 1（含空值分割产物）」，不许说「允许 1 人」。
 */
export function allowedUserCountMayBeEmptySplit(n: number): boolean {
  return n === 1
}

/**
 * ★★★★ `quiet_hours_window` 是 `readString(start) + "–" + readString(end)` **字符串拼接**。
 *   两端都没配好时就是字面量 `"–"`（U+2013 EN DASH）—— 一个**看着像有值**的非空串。
 *   ⇒ 判「静默时段没配」不能只看「非空」。
 */
export function quietHoursWindowIsEmptyArtifact(s: string): boolean {
  return s === '–' || s === '-'
}
