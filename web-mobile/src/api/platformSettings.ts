import { req, type RequestOptions } from './client'

/**
 * platformSettings.ts — 平台设置：清单 / 单键 / 变更历史（2026-10-08，第九十批）。
 *
 * GET /api/admin/settings
 * GET /api/admin/settings/{key}
 * GET /api/admin/settings/{key}/history
 *
 * - **注册**：**本仓第五种注册形态** —— `mux.HandleFunc` 不在 `admin/handler.go` 里，
 *   而在**另一个文件的方法**中，由 `admin/handler.go:1123` 调用：
 *   ```go
 *   // admin/settings.go:22-30
 *   func (h *Handler) registerSettingsRoutes(mux *http.ServeMux) {
 *       mux.HandleFunc("/api/admin/settings", h.admin(h.settingsList))
 *       mux.HandleFunc("/api/admin/settings/", h.admin(h.settingsRouter))
 *       …
 *   }
 *   // admin/handler.go:1123
 *   h.registerSettingsRoutes(mux)
 *   ```
 *   ⇒ ★★★ 只 grep `admin/handler.go` 里的 `mux.HandleFunc` 会判成「死端点」。
 * - **实现**：`admin/settings.go`（422 行）· `settings/spec.go`（Spec/枚举）·
 *   `settings/audit.go`（ListAudit/AuditEntry）。
 * - **桌面调用方**：`web/src/api/settings.ts:53` / `:57` / `:70` —— ★ 三条都是
 *   `req<{…}>` 直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十五件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **三个端点的权限档位是同一个：都是 `h.admin(...)` ⇒ admin 档**
 *     （tenant_admin 可用，抽屉席**不设** `requiresRole`）
 *     ⇒ ★★ 但 **`Dangerous` 档的设置在 PUT 上有 super_admin 闸**（`settings.go:236-239`）
 *     ⇒ ⇒ **读不设闸、写才设** ⇒ 移动端抽屉席不进 `requiresRole`，
 *       但**写按钮必须按 `danger_level` 隐藏**，否则用户点了就吃 403。
 *
 * (2) ★★★★★ **503 有两种文案、两种前置条件**：
 *     - `settings registry not initialised`（`:156` / `:200`）—— 查 `settings.Global == nil`
 *     - `db not wired`（`:315`，**只有 history 有**）—— 查 `h.db == nil`
 *     ⇒ ★★ **history 根本不看 `settings.Global`** ⇒ 即使注册表没初始化，
 *       历史照样能查；反之亦然 ⇒ 客户端**不能**用一个「设置系统可用」标志统管三者。
 *
 * (3) ★★★★ **404 也有两种**：未知设置是 `unknown setting <key>`
 *     （**key 被拼进文案**，`settings.go:205`）；路由不匹配是 `unknown settings endpoint`
 *     （`:114`）。
 *     ⇒ ★★ 而**方法不匹配返回的是 404 而不是 405**（`:113-115` 的 `default` 分支）
 *     ⇒ 与批 83（405 在前）、批 89（405 写在最前并带 `Allow` 头）**完全相反**
 *     ⇒ ⇒ 本族**没有 405**，用 POST 探 GET 会拿到 `unknown settings endpoint`。
 *
 * (4) ★★★★★ **列表里的 `value === null` ⟺ 该 spec 是 `tenant` 作用域**
 *     （`settings.go:166-175`）：`var v json.RawMessage` 初始为 nil，
 *     只有 `sp.Scope == settings.ScopePlatform` 才会被填；tenant 作用域**刻意不填**
 *     ⇒ `json.RawMessage(nil)` 序列化成 **`null`**，同时 `source` 是**空串** `""`
 *     ⇒ ⇒ 这是一条**可自验的强不变量**（见 `settingsValueIsNullForTenantScope`）。
 *
 * (5) ★★★★★ **`items` 的 nil 编码在同一族里是**相反**的**：
 *     - 列表：`items := []map[string]any{}`（`:160`）⇒ **恒为数组**，无设置时是 `[]`
 *     - 历史：`settings.ListAudit` 里 `var out []AuditEntry`（`audit.go:92`），
 *       无行时 `out` 保持 nil ⇒ `{"items": null}`
 *     ⇒ ★★★ **同一族两个端点，一个恒 `[]`、一个可能是 `null`**
 *     ⇒ ⇒ 解包器**不能共用解包器**。
 *
 * (6) ★★★★★ **历史只有 7 天窗口，且响应里没有任何字段说明这件事** ——
 *     `created_at > now() - INTERVAL '7 days'` 硬编码在 SQL 里（`audit.go:72` / `:83`），
 *     没有查询参数、没有分页、没有游标
 *     ⇒ ★★ **7 天前的变更在响应里「不存在」，与「从未变更过」不可区分**。
 *
 * (7) ★★★★★ **`old_value` / `new_value` 的「空」有**两种编码**：
 *     - SQL 里是 NULL ⇒ `COALESCE(…,'')` 得空串 ⇒ `len(oldV) == 0`
 *       ⇒ `e.OldValue` 保持 nil ⇒ `json:"…,omitempty"` 把**键整个删掉**
 *     - 列里存的是 JSON `null` ⇒ `old_value::text` 是 4 字符 `"null"` ⇒ `len > 0`
 *       ⇒ `OldValue = json.RawMessage("null")` ⇒ **键存在、值是 `null`**
 *     ⇒ ★★★ 「这个概念为空」在**同一个响应里**可以有两种形态
 *     ⇒ ⇒ 客户端**绝不能**用 `'old_value' in entry` 判「曾经有过旧值」。
 *
 * (8) ★★★★ **`tenant_id` / `client_ip` 也是 `omitempty`** ——
 *     SQL 用 `COALESCE(tenant_id,'')`、`COALESCE(client_ip,'')`
 *     ⇒ 空串 ⇒ **键消失** ⇒ `AuditEntry` 恒在的只有 5 个键。
 *
 * (9) ★★★★ **历史的 `Scan` 失败是裸 `continue`**（`audit.go:97-100`），
 *     **没有任何日志** ⇒ 又一处**静默跳行**（与批 88 的 `usage-summary` 同族）
 *     ⇒ ⇒ `items.length` 不能被当成「窗口内的变更条数」。
 *
 * (10) ★★★★ **`ORDER BY created_at DESC LIMIT 50`，且无 tiebreak** ⇒ 同秒行顺序未定义
 *     ⇒ ★★ 与批 88 的 items 排序同族：**只能断言「非升序」，不能断言「严格降序」**。
 *     ⇒ `limit` 是写死的 50（`settings.go:321`），`ListAudit` 另有 `limit<1||>500 ⇒ 50`
 *     的钳制（`audit.go:54-56`）—— 两者一致，所以永远取不到别的值。
 *
 * (11) ★★★★★ **单键端点的 `spec` 是 **PascalCase**，而列表项是 snake_case** ——
 *     `Spec` 结构体**一个 json tag 都没有**（`spec.go:92-116`）
 *     ⇒ Go 按字段名原样序列化：`Key`/`EnvName`/`Type`/`Scope`/`Category`/`Default`
 *     /`Min`/`Max`/`Options`/`Description`/`DescriptionLong`/`Unit`
 *     /`DangerLevel`/`HotReload`/`Observability`
 *     ⇒ ★★★ **同一族两个端点的键风格完全相反** ⇒ 客户端绝不能共用一套键名常量。
 *
 * (12) ★★★ **列表是「信息损失」的一端**：`settingsList` 的 15 个 map 键
 *     **漏掉了** `DescriptionLong` 与 `Unit`（Spec 里有、列表里没有）
 *     ⇒ 想看长描述与单位**必须**再打一次单键端点。
 *
 * (13) ★★★ `Spec` 的 `Min` / `Max` 是 `*float64`、`Options` 是 `[]string`，
 *     **三者都无 `omitempty`** ⇒ **恒在**，且都**可以是 `null`**。
 *
 * (14) ★★★ **`settingsRouter` 只取 `parts[0]` 与 `parts[1]`，多余段被静默忽略** ——
 *     `strings.Split(rest, "/")` 之后只读前两段（`:98-103`）
 *     ⇒ `/api/admin/settings/a/history/extra` 与 `/a/history` **完全等价**
 *     ⇒ ★ 而且 `key` **不做 TrimSpace、不做解码后校验** ⇒ 空 key（`/api/admin/settings/`）
 *     会走到 `unknown setting `（**文案尾部有一个空格**）。
 *
 * (15) ★★★ **`EffectiveValue` 出错时列表**静默跳过该条**（`:170-172` `continue`）
 *     ⇒ ★★ `items.length` **小于**注册表里的 spec 总数是**正常**的
 *     ⇒ ⇒ 客户端不能拿列表长度当「这个网关有多少个可调项」。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验三个响应的**全部恒在键与类型**，外加 `omitempty` 键**存在时**的类型；
 *   为 (4)(5)(6)(10)(14) 提供判据。
 *   ★ **不校验** `value` / `default` / `old_value` / `new_value` 的**取值** ——
 *     它们是 `json.RawMessage` / `any`，类型随 `type` 字段变化（7 种 `ValueType`），
 *     校验取值等于把 `Type` 枚举复制一遍（恒真判据），由 `type` 字段 + 注释承担。
 */

export const PLATFORM_SETTINGS_PATH = '/api/admin/settings'

/** `:156` / `:200` 的 503 文案（注册表未初始化）。见 (2)。 */
export const SETTINGS_REGISTRY_UNAVAILABLE_MESSAGE = 'settings registry not initialised'
/** `:315` 的 503 文案（**只有 history 有**）。见 (2)。 */
export const SETTINGS_DB_UNAVAILABLE_MESSAGE = 'db not wired'
/** `:324` 的 500 文案。 */
export const SETTINGS_HISTORY_FAILED_MESSAGE = 'query failed'
/** `:114` / `:149` 的 404 文案 —— ★ **方法不匹配也走这一句**，不是 405。见 (3)。 */
export const SETTINGS_UNKNOWN_ENDPOINT_MESSAGE = 'unknown settings endpoint'

/** `:205` 的 404 前缀 —— 后端把 key **拼进**文案。 */
export const SETTINGS_UNKNOWN_SETTING_PREFIX = 'unknown setting '

/** `settings.go:321` 的历史条数上限。 */
export const SETTINGS_HISTORY_LIMIT = 50
/** `audit.go:72` / `:83` 的硬编码窗口（天）。见 (6)。 */
export const SETTINGS_HISTORY_WINDOW_DAYS = 7
/** `audit.go:54-56` 的钳制下界/上界（外加默认 50）。 */
export const SETTINGS_AUDIT_LIMIT_MIN = 1
export const SETTINGS_AUDIT_LIMIT_MAX = 500

/** `spec.go:37-45` 的七种 `ValueType`。 */
export const SETTINGS_VALUE_TYPES = ['enum', 'int', 'float', 'bool', 'string', 'url', 'duration'] as const

/** `spec.go:50-53` 的两种 `Scope`。见 (4)。 */
export const SETTINGS_SCOPES = ['platform', 'tenant'] as const
export const SETTINGS_SCOPE_PLATFORM = 'platform'
export const SETTINGS_SCOPE_TENANT = 'tenant'

/** `spec.go:58-76` 的十一种 `Category`。 */
export const SETTINGS_CATEGORIES = [
  'compression',
  'rate_limit',
  'timeout',
  'routing',
  'session',
  'security',
  'circuit_breaker',
  'general',
  'integration',
  'attribution',
  'retry',
] as const

/** `spec.go:82-85` 的四档 `DangerLevel` —— ★ **只有 PUT 受它约束**。见 (1)。 */
export const SETTINGS_DANGER_LEVELS = ['safe', 'warning', 'dangerous', 'breaking'] as const
/** `Dangerous` 的数值下界 —— 达到即需 super_admin 才能 PUT。 */
export const SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD = 2

/** `audit.go:17` 注释里的三种 action（后端未强制）。 */
export const SETTINGS_AUDIT_ACTIONS = ['update', 'rollback', 'delete'] as const

/** `settingsList` 造的 15 个 snake_case 键。★ 与 `spec` 的 PascalCase **相反**，见 (11)。 */
export const SETTINGS_LIST_ITEM_KEYS = [
  'key',
  'env_name',
  'type',
  'scope',
  'category',
  'default',
  'value',
  'source',
  'options',
  'min',
  'max',
  'description',
  'danger_level',
  'hot_reload',
  'observability',
] as const

/** ★ `Spec` 无 json tag ⇒ Go 原样输出**字段名** ⇒ 15 个 PascalCase 键。见 (11)。 */
export const SETTINGS_SPEC_KEYS = [
  'Key',
  'EnvName',
  'Type',
  'Scope',
  'Category',
  'Default',
  'Min',
  'Max',
  'Options',
  'Description',
  'DescriptionLong',
  'Unit',
  'DangerLevel',
  'HotReload',
  'Observability',
] as const

/** 单键端点响应的三个键。 */
export const SETTINGS_DETAIL_KEYS = ['spec', 'value', 'source'] as const

/** `AuditEntry` 的 9 个 json tag。 */
export const SETTINGS_AUDIT_KEYS = [
  'setting_key',
  'tenant_id',
  'action',
  'old_value',
  'new_value',
  'operator_user',
  'operator_role',
  'client_ip',
  'created_at',
] as const

/** 其中**恒在**（无 omitempty）的五个。 */
export const SETTINGS_AUDIT_REQUIRED_KEYS = [
  'setting_key',
  'action',
  'operator_user',
  'operator_role',
  'created_at',
] as const

/** 其中 `omitempty` 的四个 —— ★ 值 0 / 空串会让**键整个消失**。见 (7)(8)。 */
export const SETTINGS_AUDIT_OPTIONAL_KEYS = ['tenant_id', 'old_value', 'new_value', 'client_ip'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface SettingsListItem {
  key: string
  env_name: string
  type: string
  scope: string
  category: string
  default: unknown
  /** ★★ `tenant` 作用域恒为 `null`，`platform` 才是真实取值。见 (4)。 */
  value: unknown
  /** ★ `tenant` 作用域恒为**空串**；否则是生效值来源。 */
  source: string
  /** ★ 恒在，可为 `null`。见 (13)。 */
  options: string[] | null
  /** ★ 恒在，可为 `null`（`*float64` 无 omitempty）。见 (13)。 */
  min: number | null
  /** ★ 同上。 */
  max: number | null
  description: string
  danger_level: number
  hot_reload: boolean
  observability: string
}

export interface SettingsListResponse {
  /** ★ 恒为数组（`[]map[string]any{}`）⇒ 空时是 `[]` 不是 `null`。见 (5)。 */
  items: SettingsListItem[]
}

/** ★★ `Spec` 结构体**没有 json tag** ⇒ Go 原样输出字段名（PascalCase）。见 (11)。 */
export interface SettingsSpec {
  Key: string
  EnvName: string
  Type: string
  Scope: string
  Category: string
  Default: unknown
  Min: number | null
  Max: number | null
  Options: string[] | null
  Description: string
  DescriptionLong: string
  Unit: string
  DangerLevel: number
  HotReload: boolean
  Observability: string
}

export interface SettingsDetailResponse {
  spec: SettingsSpec
  /** `json.RawMessage` ⇒ 值随 `type` 变化（7 种 `ValueType`）。 */
  value: unknown
  source: string
}

export interface SettingsAuditEntry {
  setting_key: string
  /** ★ `omitempty` + `COALESCE(tenant_id,'')` ⇒ 空串时**键消失**。见 (8)。 */
  tenant_id?: string
  action: string
  /** ★★ 两种「空」：SQL NULL ⇒ **键消失**；JSON `null` ⇒ **键在、值为 null**。见 (7)。 */
  old_value?: unknown
  new_value?: unknown
  operator_user: string
  operator_role: string
  client_ip?: string
  created_at: string
}

export interface SettingsHistoryResponse {
  /** ★★★ 无行时是 **`null`** 而不是 `[]`（`var out []AuditEntry`）。见 (5)。 */
  items: SettingsAuditEntry[] | null
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/settings`（**admin 档**，见 (1)）。 */
export function fetchSettingsList(
  params: { category?: string } = {},
  options?: RequestOptions,
): Promise<SettingsListResponse> {
  const qs = new URLSearchParams()
  if (params.category != null) qs.set('category', params.category)
  const s = qs.toString()
  return req<unknown>('GET', `${PLATFORM_SETTINGS_PATH}${s ? `?${s}` : ''}`, undefined, options).then(
    unwrapSettingsList,
  )
}

/** GET `/api/admin/settings/{key}`。 */
export function fetchSetting(
  params: { key: string },
  options?: RequestOptions,
): Promise<SettingsDetailResponse> {
  return req<unknown>('GET', `${PLATFORM_SETTINGS_PATH}/${encodeURIComponent(params.key)}`, undefined, options).then(
    unwrapSetting,
  )
}

/** GET `/api/admin/settings/{key}/history`。★ 窗口硬编码 7 天、最多 50 条、无分页。见 (6)(10)。 */
export function fetchSettingHistory(
  params: { key: string },
  options?: RequestOptions,
): Promise<SettingsHistoryResponse> {
  return req<unknown>(
    'GET',
    `${PLATFORM_SETTINGS_PATH}/${encodeURIComponent(params.key)}/history`,
    undefined,
    options,
  ).then(unwrapSettingHistory)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapSettingsList(resp: unknown): SettingsListResponse {
  const d = requireObject(resp, '设置清单')
  requireKeys(d, ['items'], '设置清单')
  const items = requireArray(d['items'], '设置清单 的 items')
  for (let i = 0; i < items.length; i++) {
    const o = requireObject(items[i], `设置清单 的 items[${i}]`)
    requireKeys(o, SETTINGS_LIST_ITEM_KEYS, `设置清单 的 items[${i}]`)
    for (const k of ['key', 'env_name', 'type', 'scope', 'category', 'source', 'description', 'observability'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`items[${i}] 的 ${k} 不是字符串`)
    }
    // ★ value / default 是 json.RawMessage / any：取值随 type 字段变化（7 种 ValueType），
    // 不校验取值。★★ 而它们的**键存在性**已由上面的 requireKeys 覆盖
    // （两者都在 SETTINGS_LIST_ITEM_KEYS 里）⇒ 这里**刻意不再重复断言** ——
    // 重复断言是可证冗余的（删掉它，没有任何用例会变红）。
    if (typeof o['danger_level'] !== 'number') throw new Error(`items[${i}] 的 danger_level 不是数字`)
    if (typeof o['hot_reload'] !== 'boolean') throw new Error(`items[${i}] 的 hot_reload 不是布尔`)
    // ★ options / min / max 恒在且可为 null（Spec 里都没有 omitempty）。
    if (o['options'] !== null && !Array.isArray(o['options'])) {
      throw new Error(`items[${i}] 的 options 不是数组也不是 null`)
    }
    if (o['min'] !== null && typeof o['min'] !== 'number') throw new Error(`items[${i}] 的 min 不是数字也不是 null`)
    if (o['max'] !== null && typeof o['max'] !== 'number') throw new Error(`items[${i}] 的 max 不是数字也不是 null`)
  }
  return d as unknown as SettingsListResponse
}

export function unwrapSetting(resp: unknown): SettingsDetailResponse {
  const d = requireObject(resp, '单个设置')
  requireKeys(d, SETTINGS_DETAIL_KEYS, '单个设置')
  const sp = requireObject(d['spec'], '单个设置 的 spec')
  // ★ PascalCase —— Spec 结构体没有 json tag。见 (11)。
  requireKeys(sp, SETTINGS_SPEC_KEYS, '单个设置 的 spec')
  for (const k of ['Key', 'EnvName', 'Type', 'Scope', 'Category', 'Description', 'DescriptionLong', 'Unit', 'Observability'] as const) {
    if (typeof sp[k] !== 'string') throw new Error(`spec 的 ${k} 不是字符串`)
  }
  // ★ DangerLevel 是 int ⇒ 恒为数字。
  if (typeof sp['DangerLevel'] !== 'number') throw new Error('spec 的 DangerLevel 不是数字')
  if (typeof sp['HotReload'] !== 'boolean') throw new Error('spec 的 HotReload 不是布尔')
  if (sp['Min'] !== null && typeof sp['Min'] !== 'number') throw new Error('spec 的 Min 不是数字也不是 null')
  if (sp['Max'] !== null && typeof sp['Max'] !== 'number') throw new Error('spec 的 Max 不是数字也不是 null')
  if (sp['Options'] !== null && !Array.isArray(sp['Options'])) throw new Error('spec 的 Options 不是数组也不是 null')
  if (typeof d['source'] !== 'string') throw new Error('单个设置 的 source 不是字符串')
  // ★ value 的**键存在性**已由上面的 requireKeys(d, SETTINGS_DETAIL_KEYS) 覆盖
  //   ⇒ **刻意不再重复断言**（重复断言可证冗余，删掉它没有用例会变红）。
  return d as unknown as SettingsDetailResponse
}

export function unwrapSettingHistory(resp: unknown): SettingsHistoryResponse {
  const d = requireObject(resp, '设置变更历史')
  requireKeys(d, ['items'], '设置变更历史')
  // ★★ 这里**不能**用 requireArray：无行时后端返回的是 **null**（见 (5)）。
  const items = d['items']
  if (items !== null) {
    if (!Array.isArray(items)) throw new Error('设置变更历史 的 items 不是数组也不是 null')
    const arr = items as unknown[]
    for (let i = 0; i < arr.length; i++) {
      const o = requireObject(arr[i], `设置变更历史 的 items[${i}]`)
      requireKeys(o, SETTINGS_AUDIT_REQUIRED_KEYS, `设置变更历史 的 items[${i}]`)
      for (const k of ['setting_key', 'action', 'operator_user', 'operator_role'] as const) {
        if (typeof o[k] !== 'string') throw new Error(`items[${i}] 的 ${k} 不是字符串`)
      }
      if (typeof o['created_at'] !== 'string') throw new Error(`items[${i}] 的 created_at 不是字符串`)
      // ★ omitempty 键：**存在才校验类型**（值可能是 JSON 任意类型，见 (7)）。
      if ('tenant_id' in o && typeof o['tenant_id'] !== 'string') throw new Error(`items[${i}] 的 tenant_id 不是字符串`)
      if ('client_ip' in o && typeof o['client_ip'] !== 'string') throw new Error(`items[${i}] 的 client_ip 不是字符串`)
      // ★ old_value / new_value 是 json.RawMessage：可能是**任意** JSON 类型
      // （对象、数组、字符串、数字、`null`），所以既没有类型可查、也不该查 ——
      // 唯一可判的是「键在不在」，见 settingsAuditKeyIsPresent。
    }
  }
  return d as unknown as SettingsHistoryResponse
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}
function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}
function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (4) tenant 作用域 ⇒ value 为 null、source 为空串 ──

/** ★★★★★ 见 (4)：`tenant` 作用域的条目 **`value` 恒为 `null`**。 */
export function settingsValueIsNullForTenantScope(item: SettingsListItem): boolean {
  return item.scope === SETTINGS_SCOPE_TENANT && item.value === null
}

/** ★★★★★ 见 (4) 的镜像面：`value === null` ⟺ `scope === 'tenant'`。 */
export function settingsValueIsNullIffTenantScope(item: SettingsListItem): boolean {
  return (item.value === null) === (item.scope === SETTINGS_SCOPE_TENANT)
}

/** ★★★★ 见 (4)：`tenant` 作用域的 `source` 恒为**空串**（不是 null、不是 undefined）。 */
export function settingsSourceIsEmptyForTenantScope(item: SettingsListItem): boolean {
  return item.scope === SETTINGS_SCOPE_TENANT && item.source === ''
}

// ── (5) 两端点的 items nil 编码相反 ──

/** ★★★★★ 见 (5)：清单的 `items` **恒为数组**（后端用 `[]map[string]any{}` 初始化）。 */
export function settingsListItemsAreAlwaysArray(r: SettingsListResponse): boolean {
  return Array.isArray(r.items)
}

/**
 * ★★★★★ 见 (5)：历史的 `items` **可能是 `null`** ⇒ 客户端**不能**直接 `.map`
 * ⇒ 这条判据回答的是「我拿到的是不是可以直接迭代的数组」。
 */
export function settingsHistoryItemsMayBeNull(r: SettingsHistoryResponse): boolean {
  return r.items === null
}

/** ★★★ 见 (5)：`items === null` **二义** —— 真的没有变更，**或**那行被静默跳过了（见 (9)）。 */
export function settingsHistoryNullItemsAreAmbiguous(r: SettingsHistoryResponse): boolean {
  return r.items === null
}

// ── (6)(10) 历史窗口与顺序 ──

/** ★★★★ 见 (10)：按 `created_at` 降序 ⇒ 可自验「非升序」，**不能**断言「严格降序」。 */
export function settingsHistoryIsNewestFirst(r: SettingsHistoryResponse): boolean {
  const arr = r.items
  if (!Array.isArray(arr)) return false
  for (let i = 1; i < arr.length; i++) {
    const prev = arr[i - 1] as SettingsAuditEntry
    const cur = arr[i] as SettingsAuditEntry
    if (prev.created_at < cur.created_at) return false
  }
  return true
}

/** ★★★★ 见 (6)(10)：条数**硬上限 50**，且**响应里没有任何字段说明这一点**。 */
export function settingsHistoryIsWithinLimit(r: SettingsHistoryResponse): boolean {
  return !Array.isArray(r.items) || r.items.length <= SETTINGS_HISTORY_LIMIT
}

// ── (7)(8) omitempty 键的存在性 ──

/**
 * ★★★★★ 见 (7)：`old_value` 的**两种「空」** ——
 * SQL NULL ⇒ `omitempty` 让**键整个消失**；
 * 而 JSON `null` ⇒ **键存在、值是 `null`**。
 * ⇒ ★★ 这条判据回答的是「键在不在」，**不是**「旧值是不是空」。
 */
export function settingsAuditKeyIsPresent(entry: SettingsAuditEntry, key: string): boolean {
  return (entry as unknown as Record<string, unknown>)[key] !== undefined
}

/** ★★★★ 见 (8)：`tenant_id` / `client_ip` 为空串时后端让**键消失** ⇒ 它们是可选键。 */
export function settingsAuditIsPlatformScoped(entry: SettingsAuditEntry): boolean {
  return !('tenant_id' in (entry as unknown as Record<string, unknown>))
}

// ── (1) 写权限闸（读端点没有） ──

/** ★★★★ 见 (1)：`danger_level >= 2` ⇒ PUT 需要 super_admin ⇒ **写按钮该隐藏**。 */
export function settingsWriteNeedsSuperAdmin(dangerLevel: number): boolean {
  return dangerLevel >= SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD
}

/** ★★★ 见 (1)：`danger_level` 的四个档位名（数值 0..3）。 */
export function settingsDangerLevelName(dangerLevel: number): string {
  return SETTINGS_DANGER_LEVELS[dangerLevel] ?? 'unknown'
}

// ── (3)(14) 404 与路由形状 ──

/** ★★★★ 见 (3)：未知设置的后端文案**把 key 拼进去** ⇒ 客户端要按前缀匹配。 */
export function settingsIsUnknownSettingMessage(detail: string): boolean {
  return detail.startsWith(SETTINGS_UNKNOWN_SETTING_PREFIX)
}

/**
 * ★★★★ 见 (14)：`settingsRouter` **只取前两段** ⇒
 * `/api/admin/settings/{key}/history/anything` 与 `/…/history` **完全等价**。
 * ⇒ ★★ 这条判据是给「用户手工拼错 URL」的提示用的：多出来的段会被静默忽略。
 */
export function settingsExtraPathSegmentsAreIgnored(pathSegments: string[]): boolean {
  return pathSegments.length > 2
}

/** ★★★ 见 (14)：空 key（路径以斜杠结尾）不会 404 在路由层，而是**进 handler** 报 `unknown setting `。 */
export function settingsEmptyKeyReachesHandler(key: string): boolean {
  return key === ''
}

// ── (15) 列表长度不能当 spec 总数 ──

/** ★★★★ 见 (15)：列表**可能**因 `EffectiveValue` 出错而少条目 ⇒ 长度不是注册表规模。 */
export function settingsListMayBeIncomplete(itemCount: number, registrySpecCount: number): boolean {
  return itemCount < registrySpecCount
}