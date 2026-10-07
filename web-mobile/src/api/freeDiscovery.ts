// freeDiscovery.ts — 免费资源自动发现读面（第四十八批，6 条 **admin** 档 + 1 条 **superAdmin** 档）。
//   GET /api/free-discovery/templates                 (admin)
//   GET /api/free-discovery/templates/presets         (admin)
//   GET /api/free-discovery/templates/{id}            (admin)
//   GET /api/free-discovery/tasks                     (admin)
//   GET /api/free-discovery/tasks/{id}                (admin)
//   GET /api/free-discovery/tasks/{id}/results       (admin)
//   GET /api/free-discovery/scan-scheduler/status     (**superAdmin**)
//
// 后端逐条对应：
//   admin/handler.go:1302-1317                        7 条只读 GET 的注册（Go 1.22 方法限定路由）
//   admin/free_discovery.go:108-146                   templates GET/POST
//   admin/free_discovery.go:149-198                   templates/{id} GET/PUT/PATCH/DELETE
//   admin/free_discovery.go:201-235                   templates/presets（★405 在 503 之前）
//   admin/free_discovery.go:366-387                   tasks
//   admin/free_discovery.go:392-412                   tasks/{id}
//   admin/free_discovery.go:415-443                   tasks/{id}/results
//   admin/free_discovery.go:83-89                     scan-scheduler/status（★不查 fdDeps）
//   domains/freediscovery/types.go:85-107             ProviderTemplate（★2 个 omitempty 键）
//   domains/freediscovery/types.go:149-164            DiscoveryTask（★template_id 可空但无 omitempty）
//   domains/freediscovery/types.go:191-209            DiscoveryResult（★tenant_id 由 Go 回填）
//   domains/freediscovery/template_manager.go:127-153 Get：404 sentinel **不带 id**
//   domains/freediscovery/template_manager.go:401-438 scanTemplate：Get 与 List **同列**
//   domains/freediscovery/discovery_engine.go:396-443 GetTask：404 **带 (id N)**
//   domains/freediscovery/discovery_engine.go:445-497 ListTasks：★★SELECT **漏了 updated_at**
//   domains/freediscovery/discovery_engine.go:499-548 ListResults：**SELECT 没有 tenant_id 列**
//   bg/scan_scheduler.go:492-530                       ScanSchedulerStatus（★last_error omitempty）
//   admin/context.go:60-65                            EffectiveTenantID（super_admin ⇒ "default"）
//   admin/context.go:79-88                            RequireSuperAdminForWrite（★按方法分档）
//   sql/migrations/084-freediscovery-schema.sql        CHECK 取值集合（枚举的唯一真源）
//
// ★★★★★★★★★★ 头号缺陷一：`ListTasks` 的 SELECT **漏了 `updated_at`**。
//   `discovery_engine.go:460-464`：
//       SELECT id, tenant_id, template_id, provider_code, status, trigger_type,
//              COALESCE(triggered_by,''), started_at, completed_at,
//              COALESCE(error_message,''), models_found, models_imported, created_at
//       FROM discovery_tasks ORDER BY created_at DESC LIMIT $1
//   ⇒ **`GET /tasks` 里每个任务的 `updated_at` 恒为 Go 零值 `"0001-01-01T00:00:00Z"`**。
//   ★ 而 `GetTask`（`:408-412`，同一个结构体、同一个表）的 SELECT **有** `updated_at`
//     ⇒ `GET /tasks/{id}` 给的是真值。
//   ⇒ ★★★ **同字段、同名、同结构体、两个端点、两个值、都 200**。
//   ⇒ 客户端**不许**在列表里按 `updated_at` 排序或渲染「最后更新」。
//
// ★★★★★★★★★ 头号缺陷二：`presets` 没有 nil-guard，**同族另外三条都有**。
//   · `free_discovery.go:121-123`  templates ⇒ `if tpls == nil { tpls = []*…{} }`
//   · `free_discovery.go:383-385`  tasks     ⇒ `if tasks == nil { … }`
//   · `free_discovery.go:439-441`  results   ⇒ `if results == nil { … }`
//   · `free_discovery.go:218`      presets   ⇒ `var out []presetView`  ← **没有 guard**
//   ⇒ ★★★ 「空列表」在本族有**两种表示**：另外三条恒为 `[]`，`presets` 为 **`null`**。
//
// ★★★★★★★★ 头号缺陷三：**同一个 handler、同一条路径，按方法分档**。
//   `handler.go:1302-1309`：GET 注册为 `h.admin(...)`；POST/PUT/PATCH/DELETE 注册为
//   同一个 handler，但 handler 内每个写分支开头都有 `RequireSuperAdminForWrite(w, r)`
//   （`free_discovery.go:127 / 170 / 186`）。
//   ⇒ **读面 admin 档** ⇒ 抽屉席**不设** `requiresRole`；
//     但同一路径的写操作 tenant_admin 会拿到 **403**
//     `tenant_admin has read-only access; write operations require super_admin`。
//   ⇒ 这是本仓第 N 次「**不能按路径/前缀判权限**」的证据。
//
// ★★★★★★ 头号缺陷四：`enabled` 查询参数**只认字面 `"true"`**。
//   `free_discovery.go:115`：`r.URL.Query().Get("enabled") == "true"`
//   ⇒ `"1"` / `"TRUE"` / `"yes"` / 带空格 ⇒ **过滤关闭，返回全部**。
//
// ★★★★★★★ 头号缺陷五：`fdTenant` = `EffectiveTenantID`
//   （`free_discovery.go:100-103` → `admin/context.go:60-65`）：
//       tenant_admin                ⇒ 自己的租户
//       super_admin / legacy key    ⇒ **"default"**
//   ⇒ ★★★ **super_admin 看到的也只是 `default` 一个租户，不是全部租户。**
//     本仓存在 `EffectiveTenantIDAll`（返回 `""` = 查全部，`admin/context.go:69-74`），
//     但这条线**没有用**它。
//
// ★★★★★★ 头号缺陷六：两种「scheduler 不存在」有**两种表示**。
//   · `h.scanSchedulerStatus == nil` ⇒ **503** `scan-scheduler is not available`
//     （`free_discovery.go:84-87`）
//   · 接口里装着 typed-nil `*ScanScheduler` ⇒ `Status()` 走 `bg/scan_scheduler.go:509-511`
//     返回 `ScanSchedulerStatus{Enabled: false}` ⇒ **200** + `enabled:false`
//     + `interval:""` + 全 0 计数 + **无 `last_error`**
//   ⇒ ★★★ 而它是全族**唯一不查 `fdDeps`** 的端点
//     ⇒ 六条都在 503 的同时它可以 200。
//
// ★★★★ 方法检查与依赖检查的顺序**不一致**：
//   · `presets`：**405 在 503 之前**（`free_discovery.go:202-208`）
//   · 其余五条：**503 在 405 之前**（先 `fdDeps(w)` 再判 method）
//
// ★★★★ `presetView`（`free_discovery.go:209-217`）**丢了 `tos_url`**：
//   `ProviderPreset` 有 `TosURL` 字段，但响应结构体没有 ⇒ **客户端永远拿不到预设的 ToS 链接**，
//   只有 `tos_verdict` + `tos_notes`。
//
// ★★★★ `tasks` 的 limit 是**回落 50，不是 clamp 到 200**：
//   `discovery_engine.go:447-449`：`if limit <= 0 || limit > 200 { limit = 50 }`
//   · 注释写的是「limit capped at 200」——**注释与实现不符**
//   · `200` **合法**（就是 200）；**`201` ⇒ 50**（不是 200）
//   · `limit, _ := strconv.Atoi(...)`（`free_discovery.go:375`）**丢弃 error**
//     ⇒ `?limit=abc` ⇒ 0 ⇒ 50
//   ⇒ 本仓**第 11 种限幅语义**。
//
// ★★★★ `results` 的 `?status=` 未知取值**不被拒**：
//   `free_discovery.go:429-432` 空 ⇒ 默认 `"pending"`；
//   `discovery_engine.go:519` `status != "" && status != "all"` ⇒ 加 `AND import_status=$2`
//   ⇒ `?status=bogus` ⇒ **200 + 空数组**，与「没有 pending 结果」**分不开**。
//
// ★★★ `DiscoveryTask.template_id` 是 `*int64` 且**没有 `omitempty`**
//   ⇒ 键**恒存在**，但模板被删后（`ON DELETE SET NULL`）是 **JSON `null`**。
//   ⇒ 客户端按 `typeof === 'number'` 校验会**拒掉合法形状**。
//
// ★★★ `DiscoveryResult.tenant_id` **不来自 DB**：
//   `ListResults` 的 SELECT（`discovery_engine.go:512-517`）**没有 tenant_id 列**，
//   Go 侧 `r.TenantID = tenantID`（`:544`）用**请求者的有效租户**回填
//   ⇒ ★★ 结果行里的 `tenant_id` **恒等于请求者的租户**，不是行自己的租户。
//
// ★★★ `DiscoveryResult` 里**四个 0 全是二义**（`COALESCE(…,0)`）：
//   `context_window` / `max_tokens` / `monthly_tokens` / `daily_tokens`
//   ⇒ 「没量到」与「真的是 0」分不开。
//   ★★ **对照**：`free_type` 的 `''` **不是**二义 ——
//     `CHECK (free_type IN (…7 值…))` **不允许 `''`**（084 迁移 :107-110）
//     ⇒ `''` **唯一**对应 SQL NULL ⇒ 反而是确定的「推断不出」。
//
// ★★★ `ProviderTemplate.APIKeyEncrypted` 是 `json:"-"` ⇒ 密文**从不下发**；
//   而 `HasCredential()`（`types.go:111-113`）看的是
//   `APIKeyEnv != "" || len(APIKeyEncrypted) > 0`
//   ⇒ ★★★ **客户端算不出这个谓词**：`api_key_env === ''` 同时意味着
//     「无认证」和「有密文但没有 env 引用」。
//
// ★★★ 两条 404 的 detail **形状不同**：
//   · 模板 404 = sentinel 原文 `freediscovery: provider template not found`（**不带 id**）
//   · 任务 404 = `fmt.Errorf("%w (id %d)", …)` ⇒ `freediscovery: task not found (id 42)`
//     （**detail 里带 id**）
//
// ★★★ `created_at` / `updated_at` 走 `sql.NullTime`（`template_manager.go:407-408`），
//   列若为 NULL 则序列化 Go 零值 ⇒ **`created_at` 也可能是 `0001-01-01T00:00:00Z`**。
//
// ★★ `templates` GET 是全族**唯一直接 `writeInternalErr`** 的（`free_discovery.go:118`）
//   ⇒ 它的 500 detail 是 `internal error (see server logs)`，
//     而另外五条走 `writeFDErr` ⇒ `free discovery request failed`
//   ⇒ ★★ **同族两个 500 文案**。
//
// ★ 409 在只读面**不可达**：`ErrTemplateDisabled` / `ErrTaskStateConflict`
//   只在 Update / Import 路径产生。
//
// ★ 排序：templates `ORDER BY provider_code`；tasks `ORDER BY created_at DESC`；
//   results `ORDER BY model_id`；presets 按 code 字母序（`presets.go:110-117`）。
// ★ `presets` 里 `p == nil` 静默跳过（`:221-223`）——生产不可达
//   （`ListPresetCodes` 与 `GetPreset` 枚举同一个 map），但它是唯一会让 presets 少项的分支。

import type { RequestOptions } from './client'
import { req } from './client'

// ── 枚举（取值集合的唯一真源 = 084 迁移的 CHECK）────────────────────────

export const FD_API_TYPES = ['openai-completions', 'google-generative-ai', 'anthropic'] as const
export type FdApiType = (typeof FD_API_TYPES)[number]

export const FD_TOS_VERDICTS = ['ok', 'caution', 'ambiguous', 'avoid', 'unknown'] as const
export type FdTosVerdict = (typeof FD_TOS_VERDICTS)[number]

export const FD_TASK_STATUSES = ['pending', 'running', 'success', 'failed'] as const
export type FdTaskStatus = (typeof FD_TASK_STATUSES)[number]

export const FD_TRIGGER_TYPES = ['manual', 'scheduled', 'webhook'] as const
export type FdTriggerType = (typeof FD_TRIGGER_TYPES)[number]

export const FD_IMPORT_STATUSES = ['pending', 'imported', 'skipped', 'conflict'] as const
export type FdImportStatus = (typeof FD_IMPORT_STATUSES)[number]

/** ★ `free_type` 的线形态多了 `''`（= SQL NULL，见文件头「四个 0」对照段）。 */
export const FD_FREE_TYPES = [
  'recurring-daily',
  'recurring-monthly',
  'one-time-initial',
  'recurring-credit',
  'recurring-uncapped',
  'keyless',
  'discontinued',
] as const
export type FdFreeType = (typeof FD_FREE_TYPES)[number]

/** ★★ `DiscoveryResult.free_type` 的线形态：`''` **唯一**对应 SQL NULL。 */
export type FdFreeTypeWire = FdFreeType | ''

// ── 键集合（用于「缺任一必填键必抛错」）────────────────────────────────

/** ★ `ProviderTemplate` 的 17 个**恒存在**键（不含 `json:"-"` 的密文与 2 个 omitempty）。 */
export const FD_TEMPLATE_REQUIRED_KEYS = [
  'id',
  'tenant_id',
  'provider_code',
  'display_name',
  'base_url',
  'api_type',
  'api_key_env',
  'models_endpoint',
  'quota_endpoint',
  'tos_url',
  'tos_verdict',
  'tos_notes',
  'enabled',
  'created_by',
  'created_at',
  'updated_at',
  'consecutive_scan_failures',
] as const

/** ★ 两个 `omitempty` 键：**条件存在**，缺失 ≠ null。 */
export const FD_TEMPLATE_OPTIONAL_KEYS = ['last_scan_failure_at', 'auto_disabled_at'] as const

/** ★★ `json:"-"` ⇒ **永不下发**。收到它就说明这不是本端点的响应。 */
export const FD_TEMPLATE_NEVER_KEY = 'api_key_encrypted'

/** ★ `DiscoveryTask` 的 14 个**恒存在**键（含 `template_id`，它可空但**不** omitempty）。 */
export const FD_TASK_REQUIRED_KEYS = [
  'id',
  'tenant_id',
  'template_id',
  'provider_code',
  'status',
  'trigger_type',
  'triggered_by',
  'started_at',
  'completed_at',
  'error_message',
  'models_found',
  'models_imported',
  'created_at',
  'updated_at',
] as const

/** ★ `DiscoveryResult` 的 17 个**恒存在**键（含 `imported_at`，可空但**不** omitempty）。 */
export const FD_RESULT_REQUIRED_KEYS = [
  'id',
  'task_id',
  'tenant_id',
  'provider_code',
  'model_id',
  'display_name',
  'context_window',
  'max_tokens',
  'free_type',
  'monthly_tokens',
  'daily_tokens',
  'pool_key',
  'tos_verdict',
  'tos_notes',
  'import_status',
  'imported_at',
  'created_at',
] as const

/** ★ `presetView`（`free_discovery.go:209-217`）只有 7 个键，**没有** `tos_url`。 */
export const FD_PRESET_KEYS = [
  'provider_code',
  'display_name',
  'base_url',
  'api_type',
  'api_key_env',
  'tos_verdict',
  'tos_notes',
] as const

/** ★ `ScanSchedulerStatus` 的 9 个**恒存在**键。 */
export const FD_SCHEDULER_REQUIRED_KEYS = [
  'enabled',
  'started',
  'interval',
  'last_sweep_at',
  'sweeps_total',
  'scans_total',
  'scans_failed',
  'scans_skipped',
  'cycles_failed',
] as const

/** ★ `last_error` 是 `omitempty` ⇒ **条件存在**。 */
export const FD_SCHEDULER_OPTIONAL_KEYS = ['last_error'] as const

// ── 类型 ───────────────────────────────────────────────────────────────

/** `types.go:85-107` 逐字段照抄。 */
export interface FdTemplate {
  id: number
  tenant_id: string
  provider_code: string
  display_name: string
  base_url: string
  api_type: FdApiType
  /** ★ 空串**不是**「无认证」的证明——密文不下发，详见文件头。 */
  api_key_env: string
  models_endpoint: string
  quota_endpoint: string
  tos_url: string
  tos_verdict: FdTosVerdict
  tos_notes: string
  enabled: boolean
  created_by: string
  created_at: string
  /** ★★ **列表端点恒为 Go 零值**，只有详情端点是真值。 */
  updated_at: string
  consecutive_scan_failures: number
  /** ★ 条件存在（`omitempty`）。 */
  last_scan_failure_at?: string
  /** ★ 条件存在（`omitempty`）。 */
  auto_disabled_at?: string
}

/** `presetView` 的 7 个键。★ **没有** `tos_url`。 */
export interface FdPreset {
  provider_code: string
  display_name: string
  base_url: string
  api_type: FdApiType
  api_key_env: string
  tos_verdict: FdTosVerdict
  tos_notes: string
}

/** ★★ `presets` 端点的信封**可能是 `null`**（无 nil-guard）；另外三条端点恒为数组。 */
export interface FdPresetsEnvelope {
  presets: FdPreset[] | null
}

export interface FdTemplatesEnvelope {
  /** ★ 有 nil-guard ⇒ **永不为 null**。 */
  templates: FdTemplate[]
}

/** `types.go:149-164` 逐字段照抄。 */
export interface FdTask {
  id: number
  tenant_id: string
  /** ★ 键恒存在；模板被删后（ON DELETE SET NULL）是 **`null`**。 */
  template_id: number | null
  provider_code: string
  status: FdTaskStatus
  trigger_type: FdTriggerType
  triggered_by: string
  started_at: string | null
  completed_at: string | null
  error_message: string
  models_found: number
  models_imported: number
  created_at: string
  /** ★★ **列表端点恒为 Go 零值**（SELECT 漏列），只有详情端点是真值。 */
  updated_at: string
}

export interface FdTasksEnvelope {
  tasks: FdTask[]
}

/** `types.go:191-209` 逐字段照抄。 */
export interface FdResult {
  id: number
  task_id: number
  /** ★★ 由 Go 用**请求者的租户**回填，不是行自己的租户。 */
  tenant_id: string
  provider_code: string
  model_id: string
  display_name: string
  /** ★ 0 是二义（COALESCE）。 */
  context_window: number
  /** ★ 0 是二义（COALESCE）。 */
  max_tokens: number
  /** ★ `''` **唯一**对应 SQL NULL ⇒ 这里**不是**二义。 */
  free_type: FdFreeTypeWire
  /** ★ 0 是二义（COALESCE）。 */
  monthly_tokens: number
  /** ★ 0 是二义（COALESCE）。 */
  daily_tokens: number
  pool_key: string
  tos_verdict: FdTosVerdict
  tos_notes: string
  import_status: FdImportStatus
  /** 键恒存在；未导入时是 **`null`**。 */
  imported_at: string | null
  created_at: string
}

export interface FdResultsEnvelope {
  results: FdResult[]
}

/** `bg/scan_scheduler.go:493-504` 逐字段照抄。 */
export interface FdScanSchedulerStatus {
  /** ★ typed-nil provider 分支恒 false（与 503 分支要分开看）。 */
  enabled: boolean
  /** ★ worker 是否真的在跑（不是「env 开了」）。 */
  started: boolean
  /** ★ `Duration.String()`，如 `"6h0m0s"`；typed-nil 分支是 **`""`**。 */
  interval: string
  /** ★ `time.Time` 非指针 ⇒ 恒存在；从未扫过时是 Go 零值。 */
  last_sweep_at: string
  sweeps_total: number
  scans_total: number
  scans_failed: number
  scans_skipped: number
  cycles_failed: number
  /** ★ `omitempty` ⇒ 条件存在；空串与键缺失**同义**。 */
  last_error?: string
}

// ── 路径 ───────────────────────────────────────────────────────────────

export const FD_TEMPLATES_PATH = '/api/free-discovery/templates'
export const FD_PRESETS_PATH = '/api/free-discovery/templates/presets'
export const FD_TASKS_PATH = '/api/free-discovery/tasks'
/** ★★ **superAdmin** 档（`handler.go:1316`）：tenant_admin ⇒ 403。 */
export const FD_SCAN_SCHEDULER_PATH = '/api/free-discovery/scan-scheduler/status'

export function fdTemplateByIdPath(id: string | number): string {
  return `/api/free-discovery/templates/${encodeURIComponent(String(id))}`
}

export function fdTaskByIdPath(id: string | number): string {
  return `/api/free-discovery/tasks/${encodeURIComponent(String(id))}`
}

/**
 * ★★ 只有字面 `"true"` 会打开过滤（`free_discovery.go:115`）。
 * @param enabledOnly `true` ⇒ 发 `?enabled=true`；`false`/省略 ⇒ **不发这个参数**
 *   （发 `enabled=false` 也一样是「不过滤」，但不发更贴近「没传」）。
 */
export function fdTemplatesPath(opts?: { enabledOnly?: boolean }): string {
  return opts?.enabledOnly === true ? `${FD_TEMPLATES_PATH}?enabled=true` : FD_TEMPLATES_PATH
}

/** limit **原样下发**（后端的回落逻辑由 `fdTaskLimitEffective` 复刻）。 */
export function fdTasksPath(opts?: { limit?: number | string }): string {
  if (opts?.limit === undefined) return FD_TASKS_PATH
  return `${FD_TASKS_PATH}?limit=${encodeURIComponent(String(opts.limit))}`
}

export function fdTaskResultsPath(id: string | number, opts?: { status?: string }): string {
  const base = `/api/free-discovery/tasks/${encodeURIComponent(String(id))}/results`
  // ★ 空串会让后端回落成 "pending"，所以**不发**这个参数而不是发 `status=`。
  if (opts?.status === undefined || opts.status === '') return base
  return `${base}?status=${encodeURIComponent(opts.status)}`
}

// ── 形状判据（每端点独立解包，不共用解包器）───────────────────────────

function shapeFail(endpoint: string, expect: string, actual: string): Error {
  return new Error(`free-discovery/${endpoint} 响应形状不符：期望 ${expect}，实得 ${actual}`)
}

function actualKind(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  if (typeof v === 'object') return 'object'
  return typeof v
}

function missingKeys(m: Record<string, unknown>, keys: readonly string[]): string[] {
  return keys.filter((k) => !(k in m))
}

function isObj(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

/** ★ 每项都校验 17 个恒存在键 + 类型 ⇒ 「缺任一必填键必抛错」。 */
function validateTemplate(v: unknown, endpoint: string, i: number): FdTemplate {
  if (!isObj(v)) throw shapeFail(endpoint, '模板对象', actualKind(v))
  const missing = missingKeys(v, FD_TEMPLATE_REQUIRED_KEYS)
  if (missing.length > 0) {
    throw shapeFail(endpoint, `模板第 ${i} 项的 17 个必填键齐全`, `缺 ${missing.length} 个（${missing.join(', ')}）`)
  }
  if (typeof v.id !== 'number' || typeof v.provider_code !== 'string' || typeof v.enabled !== 'boolean') {
    throw shapeFail(endpoint, '模板 id:number / provider_code:string / enabled:boolean', actualKind(v.id))
  }
  if (typeof v.consecutive_scan_failures !== 'number') {
    throw shapeFail(endpoint, 'consecutive_scan_failures:number', actualKind(v.consecutive_scan_failures))
  }
  // ★★ `APIKeyEncrypted` 是 `json:"-"` ⇒ 密文**从不下发**；出现它就不是本端点的响应。
  if (FD_TEMPLATE_NEVER_KEY in v) {
    throw shapeFail(endpoint, `不含 \`${FD_TEMPLATE_NEVER_KEY}\`（json:"-" 从不下发）`, '出现了该键')
  }
  return v as unknown as FdTemplate
}

/**
 * ★ `{templates: […]}`：handler 有 nil-guard ⇒ **`templates` 永不为 null**，
 *   实得 `null` 就是形状不符，抛错（不返 `[]` 近似）。
 */
export function unwrapTemplatesList(resp: unknown): FdTemplatesEnvelope {
  if (isObj(resp)) {
    if (Array.isArray(resp.templates)) {
      return {
        templates: resp.templates.map((v, i) => validateTemplate(v, 'templates', i)),
      }
    }
    if ('templates' in resp) throw shapeFail('templates', '{templates: […]}（永不为 null）', `templates 是 ${actualKind(resp.templates)}`)
  }
  throw shapeFail('templates', '{templates: […]}', actualKind(resp))
}

/** ★ `presetView` 的 7 个键恒存在（结构体字段，无 omitempty）。 */
function validatePreset(v: unknown, i: number): FdPreset {
  if (!isObj(v)) throw shapeFail('templates/presets', '预设对象', actualKind(v))
  const missing = missingKeys(v, FD_PRESET_KEYS)
  if (missing.length > 0) {
    throw shapeFail('templates/presets', `第 ${i} 项的 7 个必填键齐全`, `缺 ${missing.length} 个（${missing.join(', ')}）`)
  }
  if (typeof v.provider_code !== 'string') {
    throw shapeFail('templates/presets', 'provider_code:string', actualKind(v.provider_code))
  }
  return v as unknown as FdPreset
}

/**
 * ★★ 与 `unwrapTemplatesList` **相反**：`presets` 无 nil-guard
 *   ⇒ `presets: null` 是**合法响应**，必须原样保留那个 null（降级成 `[]` 会丢掉这个区分）。
 */
export function unwrapPresets(resp: unknown): FdPresetsEnvelope {
  if (isObj(resp) && 'presets' in resp) {
    if (resp.presets === null) return { presets: null }
    if (Array.isArray(resp.presets)) return { presets: resp.presets.map(validatePreset) }
    throw shapeFail('templates/presets', '{presets: […] | null}', `presets 是 ${actualKind(resp.presets)}`)
  }
  throw shapeFail('templates/presets', '{presets: […] | null}', actualKind(resp))
}

/** ★★ `templates/{id}` 返回**裸对象**（`writeJSON(w, 200, tpl)`），**没有**信封。 */
export function unwrapTemplate(resp: unknown): FdTemplate {
  return validateTemplate(resp, 'templates/{id}', 0)
}

/**
 * ★ `{tasks: […]}`：nil-guard ⇒ 永不为 null。
 * ★ `template_id` 必须容忍 **`null`**（模板被删），`started_at`/`completed_at` 同理。
 */
function validateTask(v: unknown, endpoint: string, i: number): FdTask {
  if (!isObj(v)) throw shapeFail(endpoint, '任务对象', actualKind(v))
  const missing = missingKeys(v, FD_TASK_REQUIRED_KEYS)
  if (missing.length > 0) {
    throw shapeFail(endpoint, `任务第 ${i} 项的 14 个必填键齐全`, `缺 ${missing.length} 个（${missing.join(', ')}）`)
  }
  if (typeof v.id !== 'number' || typeof v.status !== 'string') {
    throw shapeFail(endpoint, 'id:number / status:string', actualKind(v.id))
  }
  // ★★★ 键恒存在，值可为 null —— 按 `typeof === 'number'` 校验会拒掉合法形状。
  if (!(v.template_id === null || typeof v.template_id === 'number')) {
    throw shapeFail(endpoint, 'template_id:number | null（键恒存在）', actualKind(v.template_id))
  }
  if (!(v.started_at === null || typeof v.started_at === 'string')) {
    throw shapeFail(endpoint, 'started_at:string | null（键恒存在）', actualKind(v.started_at))
  }
  if (!(v.completed_at === null || typeof v.completed_at === 'string')) {
    throw shapeFail(endpoint, 'completed_at:string | null（键恒存在）', actualKind(v.completed_at))
  }
  if (typeof v.models_found !== 'number' || typeof v.models_imported !== 'number') {
    throw shapeFail(endpoint, 'models_found:number / models_imported:number', actualKind(v.models_found))
  }
  return v as unknown as FdTask
}

export function unwrapTasksList(resp: unknown): FdTasksEnvelope {
  if (isObj(resp)) {
    if (Array.isArray(resp.tasks)) {
      return { tasks: resp.tasks.map((v, i) => validateTask(v, 'tasks', i)) }
    }
    if ('tasks' in resp) throw shapeFail('tasks', '{tasks: […]}（永不为 null）', `tasks 是 ${actualKind(resp.tasks)}`)
  }
  throw shapeFail('tasks', '{tasks: […]}', actualKind(resp))
}

/** ★★ `tasks/{id}` 返回**裸对象**，**没有**信封。 */
export function unwrapTask(resp: unknown): FdTask {
  return validateTask(resp, 'tasks/{id}', 0)
}

function validateResult(v: unknown, i: number): FdResult {
  if (!isObj(v)) throw shapeFail('tasks/{id}/results', '结果对象', actualKind(v))
  const missing = missingKeys(v, FD_RESULT_REQUIRED_KEYS)
  if (missing.length > 0) {
    throw shapeFail('tasks/{id}/results', `第 ${i} 项的 17 个必填键齐全`, `缺 ${missing.length} 个（${missing.join(', ')}）`)
  }
  if (typeof v.id !== 'number' || typeof v.model_id !== 'string' || typeof v.import_status !== 'string') {
    throw shapeFail('tasks/{id}/results', 'id:number / model_id:string / import_status:string', actualKind(v.id))
  }
  if (!(v.imported_at === null || typeof v.imported_at === 'string')) {
    throw shapeFail('tasks/{id}/results', 'imported_at:string | null（键恒存在）', actualKind(v.imported_at))
  }
  return v as unknown as FdResult
}

/** ★ `{results: […]}`：nil-guard ⇒ 永不为 null。 */
export function unwrapResultsList(resp: unknown): FdResultsEnvelope {
  if (isObj(resp)) {
    if (Array.isArray(resp.results)) {
      return { results: resp.results.map(validateResult) }
    }
    if ('results' in resp) throw shapeFail('tasks/{id}/results', '{results: […]}（永不为 null）', `results 是 ${actualKind(resp.results)}`)
  }
  throw shapeFail('tasks/{id}/results', '{results: […]}', actualKind(resp))
}

/** ★★ `scan-scheduler/status` 返回**裸对象**；`last_error` 条件存在。 */
export function unwrapScanSchedulerStatus(resp: unknown): FdScanSchedulerStatus {
  if (isObj(resp)) {
    const missing = missingKeys(resp, FD_SCHEDULER_REQUIRED_KEYS)
    if (missing.length > 0) {
      throw shapeFail('scan-scheduler/status', '9 个必填键齐全', `缺 ${missing.length} 个（${missing.join(', ')}）`)
    }
    if (typeof resp.enabled !== 'boolean' || typeof resp.started !== 'boolean') {
      throw shapeFail('scan-scheduler/status', 'enabled:boolean / started:boolean', actualKind(resp.enabled))
    }
    if (typeof resp.interval !== 'string' || typeof resp.last_sweep_at !== 'string') {
      throw shapeFail('scan-scheduler/status', 'interval:string / last_sweep_at:string', actualKind(resp.interval))
    }
    if (typeof resp.sweeps_total !== 'number' || typeof resp.cycles_failed !== 'number') {
      throw shapeFail('scan-scheduler/status', '5 个计数:number', actualKind(resp.sweeps_total))
    }
    if ('last_error' in resp && typeof resp.last_error !== 'string') {
      throw shapeFail('scan-scheduler/status', 'last_error:string（若存在）', actualKind(resp.last_error))
    }
    return resp as unknown as FdScanSchedulerStatus
  }
  throw shapeFail('scan-scheduler/status', '裸对象（无信封）', actualKind(resp))
}

// ── 取数 ───────────────────────────────────────────────────────────────

export function fetchTemplates(
  opts?: { enabledOnly?: boolean },
  options?: RequestOptions,
): Promise<FdTemplatesEnvelope> {
  return req<unknown>('GET', fdTemplatesPath(opts), undefined, options).then(unwrapTemplatesList)
}

export function fetchPresets(options?: RequestOptions): Promise<FdPresetsEnvelope> {
  return req<unknown>('GET', FD_PRESETS_PATH, undefined, options).then(unwrapPresets)
}

export function fetchTemplateById(id: string | number, options?: RequestOptions): Promise<FdTemplate> {
  return req<unknown>('GET', fdTemplateByIdPath(id), undefined, options).then(unwrapTemplate)
}

export function fetchTasks(
  opts?: { limit?: number | string },
  options?: RequestOptions,
): Promise<FdTasksEnvelope> {
  return req<unknown>('GET', fdTasksPath(opts), undefined, options).then(unwrapTasksList)
}

export function fetchTaskById(id: string | number, options?: RequestOptions): Promise<FdTask> {
  return req<unknown>('GET', fdTaskByIdPath(id), undefined, options).then(unwrapTask)
}

export function fetchTaskResults(
  id: string | number,
  opts?: { status?: string },
  options?: RequestOptions,
): Promise<FdResultsEnvelope> {
  return req<unknown>('GET', fdTaskResultsPath(id, opts), undefined, options).then(unwrapResultsList)
}

/** ★★ **superAdmin 档**（`handler.go:1316`）⇒ tenant_admin 403。 */
export function fetchScanSchedulerStatus(options?: RequestOptions): Promise<FdScanSchedulerStatus> {
  return req<unknown>('GET', FD_SCAN_SCHEDULER_PATH, undefined, options).then(unwrapScanSchedulerStatus)
}

// ── 请求侧判读（复刻后端语义，UI 用它解释「我发的和我收到的不是一回事」）──

/** ★ 后端只认字面 `"true"`（`free_discovery.go:115`）。 */
export function templatesEnabledFilterApplies(raw: string | undefined): boolean {
  return raw === 'true'
}

/** ★ 只有 `"true"` 才算「要过滤」——`"1"`/`"TRUE"` 一律返回全部。 */
export const TEMPLATES_ENABLED_PARAM = 'true'

/** ★★ 回落默认（**不是** clamp 到 200）：`discovery_engine.go:447-449`。 */
export const FD_TASK_LIMIT_DEFAULT = 50
/** ★ 200 **合法**（就是 200）；201 ⇒ 回落 50。 */
export const FD_TASK_LIMIT_MAX_LEGAL = 200

/**
 * ★★ 复刻 `limit, _ := strconv.Atoi(q)` + `if limit <= 0 || limit > 200 { limit = 50 }`。
 *   · 非数字 / 空串 ⇒ `Atoi` 的 error 被丢弃 ⇒ 0 ⇒ 50
 *   · `200` ⇒ 200；**`201` ⇒ 50**（注释说 capped at 200，实现是回落 50）
 */
export function fdTaskLimitEffective(raw?: string | number | null): number {
  let n: number
  if (typeof raw === 'number') {
    n = raw
  } else if (raw === undefined || raw === null || raw === '') {
    n = 0
  } else {
    const m = /^[+-]?\d+$/.exec(raw.trim())
    n = m ? Number.parseInt(raw.trim(), 10) : Number.NaN
  }
  if (!Number.isFinite(n) || n <= 0 || n > FD_TASK_LIMIT_MAX_LEGAL) return FD_TASK_LIMIT_DEFAULT
  return n
}

/** ★ 请求值与实际生效值不同 ⇒ 用户改了没反应（「201/9999 都被静默改成 50」）。 */
export function taskLimitWasRewritten(requested?: string | number | null): boolean {
  const raw = requested === undefined || requested === null ? '' : String(requested).trim()
  if (raw === '') return false
  return fdTaskLimitEffective(raw) !== Number.parseInt(raw, 10)
}

/** ★ 空 ⇒ 后端回落 `"pending"`（`free_discovery.go:429-432`）。 */
export function resultsStatusParam(raw?: string): string {
  return raw === undefined || raw === '' ? 'pending' : raw
}

/** ★ `"all"` = 不加过滤（`discovery_engine.go:519`）；`""` 不可达（handler 已回落）。 */
export function resultsStatusIsUnfiltered(status: string): boolean {
  return status === '' || status === 'all'
}

/** ★★ 未知取值**不被拒** ⇒ 空数组既可能是「真没有」也可能是「过滤写错了」。 */
export function resultsStatusIsKnown(status: string): boolean {
  return resultsStatusIsUnfiltered(status) || (FD_IMPORT_STATUSES as readonly string[]).includes(status)
}

/** ★ 空结果分不开三种成因：没有匹配的 / 过滤值写错了 / 任务 ID 根本不存在。 */
export function resultsEmptyIsIndeterminate(env: FdResultsEnvelope): boolean {
  return env.results.length === 0
}

// ── 模板侧判读 ─────────────────────────────────────────────────────────

/** ★ Go 零值 `time.Time` 的 JSON 形态：`scanTemplate` 的 `sql.NullTime` 为 NULL 时也是它。 */
export const GO_ZERO_TIME = '0001-01-01T00:00:00Z'

export function isGoZeroTime(v: string | null | undefined): boolean {
  return v === GO_ZERO_TIME
}

/** ★★ 列表端点每个任务的 `updated_at` 都是 Go 零值（SELECT 漏列）⇒ 此处恒 true。 */
export function taskUpdatedAtIsUnreadable(t: FdTask): boolean {
  return isGoZeroTime(t.updated_at)
}

/** ★ `created_at` 走 `sql.NullTime`，列若为 NULL 也是 Go 零值（templates/tasks 两处皆然）。 */
export function createdAtIsUnreadable(v: string): boolean {
  return isGoZeroTime(v)
}

/** ★★ 客户端**算不出**后端的 `HasCredential()`（密文 `json:"-"` 从不下发）。 */
export function templateCredentialIsIndeterminate(t: FdTemplate): boolean {
  return t.api_key_env === ''
}

/** ★ `auto_disabled_at` 是 `omitempty` ⇒ 键**存在**才谈得上自动停用。 */
export function templateAutoDisabled(t: FdTemplate): boolean {
  return typeof t.auto_disabled_at === 'string' && t.auto_disabled_at !== ''
}

/** ★ 手动停用（有 `auto_disabled_at` ⇒ 不是手动停的）。 */
export function templateManuallyDisabled(t: FdTemplate): boolean {
  return t.enabled === false && !templateAutoDisabled(t)
}

/** ★ 两个**互相独立**的信号：有失败计数 / 被自动停用。 */
export function templateHasScanFailures(t: FdTemplate): boolean {
  return t.consecutive_scan_failures > 0
}

/** ★ `last_scan_failure_at` 条件存在 ⇒ 缺失 ≠ null。 */
export function templateHasLastScanFailureAt(t: FdTemplate): boolean {
  return typeof t.last_scan_failure_at === 'string'
}

// ── 任务侧判读 ─────────────────────────────────────────────────────────

/** ★★ `template_id: null` ⇒ 模板被删（`ON DELETE SET NULL`），任务本身还在。 */
export function taskTemplateDeleted(t: FdTask): boolean {
  return t.template_id === null
}

/** ★★ 列表端点的 `updated_at` 恒为 Go 零值 ⇒ **不能**用它排「最后更新」。 */
export function taskListUpdatedAtIsAlwaysZero(tasks: FdTask[]): boolean {
  return tasks.every(taskUpdatedAtIsUnreadable)
}

export function taskIsFinished(t: FdTask): boolean {
  return t.status === 'success' || t.status === 'failed'
}

export function taskIsInFlight(t: FdTask): boolean {
  return t.status === 'pending' || t.status === 'running'
}

/** ★ `error_message` 是 `COALESCE(…,'')` ⇒ **空串不是 null**。 */
export function taskHasErrorMessage(t: FdTask): boolean {
  return t.error_message !== ''
}

/** ★ 计数为 0 是二义（可能是「一条都没扫」也可能是「扫到但没落库」）。 */
export function taskFoundNothing(t: FdTask): boolean {
  return t.models_found === 0
}

// ── 结果侧判读 ─────────────────────────────────────────────────────────

/** ★★ `free_type === ''` **唯一**对应 SQL NULL（CHECK 不允许 `''`）⇒ 这里**不是**二义。 */
export function resultFreeTypeUninferable(r: FdResult): boolean {
  return r.free_type === ''
}

/** ★★ 四个 0 **全是**二义（`COALESCE(…,0)`）：没量到 vs 真的是 0。 */
export const FD_RESULT_AMBIGUOUS_ZERO_FIELDS = [
  'context_window',
  'max_tokens',
  'monthly_tokens',
  'daily_tokens',
] as const

export function resultZeroFields(r: FdResult): string[] {
  return FD_RESULT_AMBIGUOUS_ZERO_FIELDS.filter((k) => r[k] === 0)
}

/** ★ `imported_at` 键恒存在；`null` ⇒ 还没导入。 */
export function resultNotImported(r: FdResult): boolean {
  return r.imported_at === null
}

/** ★ `pool_key` 是 `COALESCE(…,'')` ⇒ 空串**不是** null。 */
export function resultSharesNoPool(r: FdResult): boolean {
  return r.pool_key === ''
}

// ── scheduler 侧判读 ───────────────────────────────────────────────────

/** ★★ typed-nil provider 分支的**唯一**指纹：真 scheduler 至少有 `"0s"`。 */
export function schedulerIntervalUnset(s: FdScanSchedulerStatus): boolean {
  return s.interval === ''
}

/** ★ 从没扫过时 `last_sweep_at` 是 Go 零值。 */
export function schedulerNeverSwept(s: FdScanSchedulerStatus): boolean {
  return isGoZeroTime(s.last_sweep_at)
}

/** ★ `last_error` 是 `omitempty` ⇒ 键缺失与空串**同义**（「没有错误记录」）。 */
export function schedulerLastErrorMissing(s: FdScanSchedulerStatus): boolean {
  return !('last_error' in s) || s.last_error === ''
}

/** ★★ 两种「scheduler 不存在」：本函数覆盖 **200 + `interval:""`** 那一支。 */
export function schedulerProviderWasTypedNil(s: FdScanSchedulerStatus): boolean {
  return schedulerIntervalUnset(s) && s.enabled === false && s.started === false
}

/** ★ 计数为 0 是二义（从没跑过 vs 跑了但没产出）。 */
export function schedulerIdleCounters(s: FdScanSchedulerStatus): boolean {
  return (
    s.sweeps_total === 0 &&
    s.scans_total === 0 &&
    s.scans_failed === 0 &&
    s.scans_skipped === 0 &&
    s.cycles_failed === 0
  )
}

/** ★ `enabled`（env 开没开）与 `started`（worker 真的在跑）是**两件事**。 */
export function schedulerEnabledButNotStarted(s: FdScanSchedulerStatus): boolean {
  return s.enabled === true && s.started === false
}

// ── 错误文案判读 ───────────────────────────────────────────────────────
// 错误信封：`{"error": {"detail": msg}}`（`admin/handler.go:1494-1498`）
// ⇒ `client.ts` 抛 `ApiError(status, detail)`，`e.message === detail`。

/** ★★ 六条 deps 端点的 503（`free_discovery.go:92-98`）。 */
export function fdDepsMissingMessage(msg: string): boolean {
  return /free-discovery is not available \(database disabled\)/i.test(msg)
}

/** ★ scheduler 探针的 503（`free_discovery.go:84-87`）——与上面**另一句话**。 */
export function scanSchedulerMissingMessage(msg: string): boolean {
  return /scan-scheduler is not available/i.test(msg)
}

/** ★★ 只有 `templates` GET 的 500 是这句话（它直接 `writeInternalErr`）。 */
export function fdTemplatesInternalErrorMessage(msg: string): boolean {
  return /^internal error \(see server logs\)$/i.test(msg)
}

/** ★★ 另外五条的 500 是这一句（走 `writeFDErr`）。 */
export function fdRequestFailedMessage(msg: string): boolean {
  return /^free discovery request failed$/i.test(msg)
}

/** ★ `templates/{id}`：id 非数字 ⇒ **400 `invalid template id`**（`ParseInt`，不是 404）。 */
export function invalidTemplateIdMessage(msg: string): boolean {
  return /^invalid template id$/i.test(msg)
}

/** ★ `tasks/{id}` 与 `tasks/{id}/results` **共用**这一句 ⇒ 只看文案分不出是哪个端点。 */
export function invalidTaskIdMessage(msg: string): boolean {
  return /^invalid task id$/i.test(msg)
}

/** ★ 模板 404：sentinel 原文，**不带 id**。 */
export function templateNotFoundMessage(msg: string): boolean {
  return /^freediscovery: provider template not found$/i.test(msg)
}

/** ★★ 任务 404：sentinel **带 `(id N)`** ⇒ 与模板那条形状不同。 */
export function taskNotFoundMessage(msg: string): boolean {
  return /^freediscovery: task not found \(id -?\d+\)$/i.test(msg)
}

/** 从任务 404 文案里取回 id。 */
export function taskNotFoundId(msg: string): string | null {
  const m = /^freediscovery: task not found \(id (-?\d+)\)$/i.exec(msg)
  return m && m[1] !== undefined ? m[1] : null
}

/** ★ 同路径写操作的 403（`admin/context.go:84`）——解释「为什么这页只有读」。 */
export function tenantAdminWriteForbiddenMessage(msg: string): boolean {
  return /tenant_admin has read-only access; write operations require super_admin/i.test(msg)
}

/** ★★ 六条 503 与 scheduler 200 可以**同时**出现（scheduler 不查 `fdDeps`）。 */
export function schedulerAnswersWhileDepsAreDown(schedulerOk: boolean, depsError: string | null): boolean {
  return schedulerOk && depsError !== null && fdDepsMissingMessage(depsError)
}
