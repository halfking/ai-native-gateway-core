import { req, type RequestOptions } from './client'

/**
 * workTypes.ts — 工作类型：清单 / 单项 / 统计 / L1 任务类型（2026-10-08，第九十一批）。
 *
 * GET /api/admin/work-types
 * GET /api/admin/work-types/{key}
 * GET /api/admin/work-types/stats
 * GET /api/admin/work-types/l1-task-types
 *
 * - **注册**：`admin/work_types.go:50-55`（`mux.HandleFunc` 在**另一个文件**里，
 *   与批 90 的 settings 同族），由 `admin/handler.go:1433-1434` 现构造并挂载：
 *   ```go
 *   wtH := NewWorkTypeHandlers(h.db)
 *   wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)
 *   ```
 * - **实现**：`admin/work_types.go`（1014 行）· `deploy/sql/schemas/baseline/01-schema.sql:19408-19452`。
 * - **桌面调用方**：`web/src/api-work-types.ts`（★ 注意它在 `web/src/` **根目录**，
 *   不在 `web/src/api/` 下 ⇒ grep `web/src/api/` 会漏掉）——
 *   四条 GET 都是 `req<T>` 直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十九件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **整族是 `h.superAdmin` 档**（`handler.go:1434` 传的是 `h.superAdmin`，
 *     **不是** `h.admin`）⇒ tenant_admin 直接 403
 *     ⇒ ⇒ 移动端抽屉席**必须**设 `requiresRole: 'super_admin'`
 *       并同步 `src/components/shell/AppDrawer.spec.ts` 白名单
 *     ⇒ ★ 与批 90（settings，`h.admin`）**同前缀邻域、不同档位**。
 *
 * (2) ★★★★★ **错误体是本仓第四种形状**：`writeJSONErrCtx`（`auto_route.go:1089-1099`）
 *     ```go
 *     "error": map[string]string{"message": msg, "code": messageKey, "type": "admin_error"}
 *     ```
 *     ⇒ ★★ **`message` 是 `i18n.T(ctx, key)` 的本地化文本，而 `code` 才是稳定机读键**
 *     （`admin_not_found` / `admin_method_not_allowed` / `admin_work_type_not_found`）
 *     ⇒ ⇒ ★★★ **客户端必须匹配 `code`，绝不能匹配 `message`** ——
 *       `message` 随请求语言变。
 *
 * (3) ★★★★ **清单端点是顶层裸数组**（`out := make([]workTypeConfig, 0)`，`:553`）
 *     ⇒ 空时是 `[]` 不是 `null` ⇒ 解包器必须直接吃数组。
 *
 * (4) ★★★★ **`ORDER BY sort_order, key`（`:544`）—— 本仓少见的「完全确定」排序**，
 *     有 key 做 tiebreak ⇒ 可断言严格升序（不像批 88/89/90 那些无 tiebreak 的）。
 *
 * (5) ★★★★★ **`model_routes` 后端只产生**两态**：键**消失** 或 **非空数组**。
 *     ★★★ **本条原先写的是「三态（键消失 / null / 数组）」，第一百零三批复核后判定为错**，
 *       因为 Go 的 `omitempty` 对 slice 的判定是 `len() == 0` ——
 *       **非 nil 的空切片同样被省略**。两条装配路径都验过：
 *       - `listWorkTypes`：`out[i].ModelRoutes = routeMap[key]`（`:573`），
 *         而 `fetchRoutesForKeys` 只在 `rows.Next()` 里 append（`:961`），
 *         **没建过条目的 key 取出来是 nil slice** ⇒ 省略；
 *       - `getWorkType`：`fetchRoutes` 返回 `make([]modelRoute, 0)`（`:912`），
 *         零行时是**非 nil 的空切片**，一样被省略。
 *     ⇒ ⇒ **`null` 与 `[]` 都不可达**（除非将来有人去掉 `omitempty`）。
 *     ⇒ 本模块仍按「存在且非 null 就必须是数组」宽容校验（`:405`），
 *       那是**防御**，不是对后端形状的断言；`WorkTypeConfig.model_routes` 的
 *       `| null` 同样只为宽容而留。
 *     ⇒ ★★ 与批 90 的 `old_value`（SQL NULL ⇒ 键消失）同型，
 *       但**成因不同**（那条是 NULL，这条是 omitempty 的 len 判定）。
 *
 * (6) ★★★★★ **`l1-task-types` 的 `items` 是「canonical 8 ∪ DB 里出现过的 L1 键」**
 *     （`mergeL1TaskTypes`，`:217-246`）
 *     ⇒ ★★ **长度 ≥ 8，且可以多于 8** ⇒ 客户端**不能**假设恰好 8 条
 *     ⇒ ★★★ 而新增项的 `Label = k`（**回落成 key 本身**）、`Icon = "◆"`
 *     （`:240-241`）⇒ ⇒ **可自验的不变量：`icon === '◆'` ⟺ `label === key`**
 *     ⇒ ⇒ canonical 八项的 icon 是 emoji、`label` 是中文 ⇒ **两类的 label 形态不同**。
 *
 * (7) ★★★★★ **`count` 字段「全为 0」是**二义**的**：`dbCounts, _ := h.fetchL1Counts(ctx)`
 *     （`:167`）**丢弃错误**，注释自陈「出错则回退 canonical-only」
 *     ⇒ 三种成因同形：① 真的没有配置用它 ② **那次查询失败** ③ `h.db == nil`（`:179-181`
 *     返回 `(nil, nil)`，**连错误都不是**）
 *     ⇒ ⇒ 客户端**不能**把「全是 0」读成「没有工作类型在用这些 L1」。
 *
 * (8) ★★★★★ **`count_24h` 是**派生字段**，不是独立计数**（`:415-419`）：
 *     ```go
 *     count := row.CountDirect
 *     if count == 0 { count = row.CountL1 }   // 拿 L1 总量当代理
 *     ```
 *     ⇒ ★★★ 可自验：`count_24h === count_direct || count_24h === count_l1_proxy`
 *     ⇒ ★★ 且 `count_l1_proxy` 是**该 L1 的全局量** ⇒ **同一 L1 下的多个工作类型
 *       会各自报同一个 `count_l1_proxy`** ⇒ 这张表**重复计数**，求和无意义。
 *
 * (9) ★★★★★ **三处查询失败被静默吞掉，响应仍是 200**：
 *     - `if err == nil { … }` 包着 `by_work_type`（`:298`）与 `by_l1_task`（`:337`）两条聚合
 *     - `_ = h.db.QueryRow(…).Scan(&totalAuto, &totalSpec)`（`:373`）—— **错误整个丢弃**
 *     ⇒ ★★ ⇒ `by_work_type` 空 / `total_auto === 0` **都可能只是查询挂了**，
 *       客户端与用户都看不出来
 *     ⇒ ★★★ ★ 注释 `:308-309` 自陈「This handler previously swallowed the rows error
 *       entirely, which shipped an empty/undercounted by_work_type to the UI with a 200」
 *       ⇒ **他们修了 `rows.Err()` 那一处，但 `h.db.Query` 的错误仍然被吞**
 *       ⇒ 这是一处**半修**的吞错，与已记的「互斥锁只能防重叠」同型：**修了机制没修全**。
 *
 * (10) ★★★★ **`by_work_type` 只含 `enabled = TRUE` 的配置**（`:399` `WHERE enabled = TRUE`）
 *      ⇒ 停用的工作类型在统计里**整行消失** ⇒ 这是一处**看得见的**过滤
 *      （与批 87 的「表有列但 SELECT 不取」那处不可见过滤正好相反）。
 *
 * (11) ★★★ `top_models` 是 `ORDER BY c DESC LIMIT 10`（`:452-453`），**无 tiebreak**
 *      ⇒ 同计数行顺序未定义 ⇒ 只能断言「非升序」。
 *
 * (12) ★★★★ **`model_routes` 的排序是三层确定性 tiebreak**（`:941-944`）：
 *      `work_type_key`、tier CASE（primary→0 / secondary→1 / fallback→2 / 其他→3）、
 *      `weight DESC`、`canonical_name` ⇒ ⇒ 可断言「先 tier 分组、组内 weight 降序、
 *      同权重按 canonical_name 升序」。
 *
 * (13) ★★★★ **`tier` 与 `task_quality_score` 恒非空且被 CHECK 锁死**：
 *      SQL 用 `COALESCE(tier,'secondary')`、`COALESCE(task_quality_score, 0)`，
 *      表上还有 `tier IN (primary,secondary,fallback)` 的 CHECK
 *      （`01-schema.sql:19450`）
 *      ⇒ ⇒ ★★ **桌面那个 `normalizeRouteTier` 兜底是多余的**（数据层已保证三值）
 *
 * (14) ★★★ **`task_quality_score` 的真实域是 0–100，不是 0–1** ——
 *      `numeric(5,2)` + CHECK `>= 0 AND <= 100`（`01-schema.sql:19449`），
 *      而**桌面注释写的是「任务质量评分 0-1」**
 *      ⇒ ★★★ **前端注释是错的** ⇒ 客户端绝不能按 0–1 校验或显示进度条。
 *
 * (15) ★★★ **`tags` / `prompt_keywords` 是 `text[] DEFAULT '{}' NOT NULL`**
 *      （`01-schema.sql:19419-19420`）⇒ **恒为数组、永不为 `null`**，但可以是空数组。
 *
 * (16) ★★ `include_disabled` 是**精确字符串比较** `== "true"`（`:534`）
 *      ⇒ `"1"` / `"yes"` / `"TRUE"` **都不生效**（静默当作 false）。
 *
 * (17) ★★★ **`l1-task-types` 是保留子路径，在通用 key 分发**之前**判断**
 *      （`handleSub`，`:111-114`）—— 否则会被当成 work type key 去查表然后 404。
 *      另有 `strings.Trim(rest, "/")`（`:102`）⇒ **尾斜杠与不带等价**，
 *      全空 ⇒ 404 `admin_not_found`。
 *
 * (18) ★★ **超时各不相同**：`stats` / `list` / `get` 是 **10s**，
 *      `l1-task-types` 是 **5s**（`:163`）⇒ 移动端的请求超时**不能统一**。
 *
 * (19) ★★ 四个端点都走 `writeJSONOk`，而它内部调 `applyV1FreezeNotice(w)`
 *      （`auto_route.go:1060`）⇒ ⇒ **这些端点也带 v1 数据冻结告示响应头**
 *      （与批 86 同源）⇒ 移动端若统一处理该头，这几个端点自动受益。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验四个响应的**全部恒在键与类型**，外加 `omitempty` 键**存在时**的类型；
 *   为 (4)(6)(7)(8)(11)(12)(13) 各提供判据。
 *   ★ **不校验** `tier` / `default_profile` 的**取值**——它们由表 CHECK 锁死三值，
 *     校验取值等于把 CHECK 复制一遍（恒真判据），由常量 + 注释承担。
 *   ★ **不校验** `count_24h` 的**正确性**（后端自己都可能取自错误路径），
 *     只校验它与另两个计数的**自洽关系**（见 (8)）。
 */

export const WORK_TYPES_PATH = '/api/admin/work-types'

/** ★★ (2) 稳定机读错误码 —— ★ `message` 是本地化文本，只能匹配 `code`。 */
export const WORK_TYPES_ERROR_CODES = {
  notFound: 'admin_not_found',
  methodNotAllowed: 'admin_method_not_allowed',
  workTypeNotFound: 'admin_work_type_not_found',
  internal: 'internal error (see server logs)',
} as const

/** `:258` 的固定统计窗口（小时）。★ 硬编码，**不是查询参数**。 */
export const WORK_TYPES_STATS_WINDOW_HOURS = 24
/** `:453` 的 top_models 上限。 */
export const WORK_TYPES_TOP_MODELS_LIMIT = 10
/** `01-schema.sql:19425` 的 `default_profile` CHECK 三值。 */
export const WORK_TYPES_DEFAULT_PROFILES = ['smart', 'speed_first', 'cost_first'] as const
/** `01-schema.sql:19450` 的 `tier` CHECK 三值。 */
export const WORK_TYPES_ROUTE_TIERS = ['primary', 'secondary', 'fallback'] as const
/** ★ `mergeL1TaskTypes` 给「DB 里新增的 L1」用的占位 icon（`:241`）。见 (6)。 */
export const WORK_TYPES_EXTRA_L1_ICON = '◆'
/** `work_type_config.default_profile` 的缺省值。 */
export const WORK_TYPES_DEFAULT_PROFILE = 'smart'

/** ★★ `task_quality_score` 的真实域是 **0–100**（CHECK 约束），**不是 0–1**。见 (14)。 */
export const WORK_TYPES_TASK_QUALITY_MIN = 0
export const WORK_TYPES_TASK_QUALITY_MAX = 100

/** `canonicalL1TaskTypes` 的**前八项顺序**（`:76-85`，`mergeL1TaskTypes` 原样保留顺序）。 */
export const WORK_TYPES_CANONICAL_L1_KEYS = [
  'chat',
  'reasoning',
  'code',
  'agent',
  'creative',
  'long_context',
  'vision',
  'function_call',
] as const

/** `workTypeConfig` 的 **14 个 json tag**（其中 4 个带 `omitempty`）。 */
export const WORK_TYPE_CONFIG_KEYS = [
  'key',
  'label',
  'category',
  'l1_task_type',
  'default_profile',
  'tags',
  'prompt_keywords',
  'system_prompt',
  'acc_task_type',
  'enabled',
  'sort_order',
  'synced_from_acc_at',
  'updated_at',
  'model_routes',
] as const

/** 其中**恒在**的十个。 */
export const WORK_TYPE_CONFIG_REQUIRED_KEYS = [
  'key',
  'label',
  'category',
  'l1_task_type',
  'default_profile',
  'tags',
  'prompt_keywords',
  'enabled',
  'sort_order',
  'updated_at',
] as const

/** 其中 `omitempty` 的四个 —— ★ 都是指针，nil 时**键消失**。见 (5)。 */
export const WORK_TYPE_CONFIG_OPTIONAL_KEYS = [
  'system_prompt',
  'acc_task_type',
  'synced_from_acc_at',
  'model_routes',
] as const

/** `modelRoute` 的 **7 个 json tag**（只有 `id` 带 `omitempty`）。 */
export const MODEL_ROUTE_KEYS = [
  'id',
  'canonical_name',
  'weight',
  'min_score',
  'enabled',
  'tier',
  'task_quality_score',
] as const

/** ★★ `by_work_type` 每项的 7 个键（`handleStats` 的 `map[string]interface{}`）。 */
export const WORK_TYPE_STAT_ENTRY_KEYS = [
  'key',
  'label',
  'category',
  'l1_task_type',
  'count_24h',
  'count_direct',
  'count_l1_proxy',
] as const

/** `WorkTypeStats` 的 **7 个顶层键**。★ 注意 `total_specified` 桌面类型里没声明。 */
export const WORK_TYPE_STATS_KEYS = [
  'window_hours',
  'by_work_type',
  'by_l1_task',
  'total_auto',
  'total_specified',
  'top_models',
  'sync_meta',
] as const

/** `L1TaskTypeMeta` 的 4 个键。 */
export const L1_TASK_TYPE_META_KEYS = ['key', 'label', 'icon', 'count'] as const

/** `top_models` 每项的 2 个键。 */
export const WORK_TYPE_TOP_MODEL_KEYS = ['model', 'count'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface ModelRoute {
  /** ★ `int` + `omitempty` ⇒ **id 为 0 时键消失**。 */
  id?: number
  canonical_name: string
  weight: number
  min_score: number
  enabled: boolean
  /** ★ COALESCE + CHECK ⇒ 恒为三值之一，见 (13)。 */
  tier: string
  /** ★★ 真实域 **0–100**，不是 0–1，见 (14)。 */
  task_quality_score: number
}

export interface WorkTypeConfig {
  key: string
  label: string
  category: string
  l1_task_type: string
  /** ★ CHECK 三值，见 (13)。 */
  default_profile: string
  /** ★ `text[] NOT NULL DEFAULT '{}'` ⇒ 恒为数组、永不为 null，见 (15)。 */
  tags: string[]
  prompt_keywords: string[]
  /** ★ `*string` + `omitempty` ⇒ **键可能整个不存在**。 */
  system_prompt?: string
  acc_task_type?: string
  enabled: boolean
  sort_order: number
  /** ★ `*time.Time` + `omitempty`。 */
  synced_from_acc_at?: string
  updated_at: string
  /** ★★★ **三态**：键消失（无路由）/ null / 数组。见 (5)。 */
  model_routes?: ModelRoute[] | null
}

export interface WorkTypeStatEntry {
  key: string
  label: string
  category: string
  l1_task_type: string
  /** ★★★ **派生**：`=== count_direct || === count_l1_proxy`。见 (8)。 */
  count_24h: number
  count_direct: number
  /** ★★ 是该 L1 的**全局量**，同一 L1 下多行会重复。见 (8)。 */
  count_l1_proxy: number
}

export interface WorkTypeSyncMeta {
  source: string
  last_synced_at?: string | null
  enabled_count: number
  route_count: number
  acc_configured: boolean
}

export interface WorkTypeStats {
  window_hours: number
  /** ★ 只含 `enabled = TRUE` 的配置。见 (10)。 */
  by_work_type: Record<string, WorkTypeStatEntry>
  /** ★ key 可能是 `__specified__` 或 `'unknown'`。 */
  by_l1_task: Record<string, number>
  total_auto: number
  /** ★ 桌面 `WorkTypeStats` 接口**没有声明**它，但后端一定会返回。 */
  total_specified: number
  /** ★ 无 tiebreak ⇒ 同计数顺序未定义，见 (11)。 */
  top_models: Array<{ model: string; count: number }>
  sync_meta: WorkTypeSyncMeta
}

export interface L1TaskTypeMeta {
  key: string
  /** ★ 新增项的 label **回落成 key 本身**，canonical 项是中文。见 (6)。 */
  label: string
  /** ★ 新增项恒为 `'◆'`，canonical 项是 emoji。见 (6)。 */
  icon: string
  /** ★★ 「全为 0」是二义的：没用到 / 查询失败 / db 为 nil。见 (7)。 */
  count: number
}

export interface L1TaskTypesResponse {
  /** ★ 恒为数组（`make(..., 0, ...)`）⇒ 空不可能发生，最少 8 项。见 (6)。 */
  items: L1TaskTypeMeta[]
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/work-types`（**顶层裸数组**）—— ★ `include_disabled` 是精确字符串匹配。 */
export function fetchWorkTypes(
  params: { includeDisabled?: boolean } = {},
  options?: RequestOptions,
): Promise<WorkTypeConfig[]> {
  const q = params.includeDisabled === true ? '?include_disabled=true' : ''
  return req<unknown>('GET', `${WORK_TYPES_PATH}${q}`, undefined, options).then(unwrapWorkTypes)
}

/** GET `/api/admin/work-types/{key}`。 */
export function fetchWorkType(params: { key: string }, options?: RequestOptions): Promise<WorkTypeConfig> {
  return req<unknown>('GET', `${WORK_TYPES_PATH}/${encodeURIComponent(params.key)}`, undefined, options).then(
    unwrapWorkType,
  )
}

/** GET `/api/admin/work-types/stats`。★ 窗口固定 24h、无查询参数。 */
export function fetchWorkTypeStats(options?: RequestOptions): Promise<WorkTypeStats> {
  return req<unknown>('GET', `${WORK_TYPES_PATH}/stats`, undefined, options).then(unwrapWorkTypeStats)
}

/** GET `/api/admin/work-types/l1-task-types`。★ 保留子路径，不是 work type key。 */
export function fetchL1TaskTypes(options?: RequestOptions): Promise<L1TaskTypesResponse> {
  return req<unknown>('GET', `${WORK_TYPES_PATH}/l1-task-types`, undefined, options).then(unwrapL1TaskTypes)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapWorkTypes(resp: unknown): WorkTypeConfig[] {
  // ★★★ 本族第三种形状：顶层**裸数组**（`make([]workTypeConfig, 0)`）⇒ 空时是 []。
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : typeof resp
    throw new Error(`工作类型清单 响应形状不符：期望顶层裸数组，实得 ${actual}`)
  }
  for (let i = 0; i < resp.length; i++) {
    unwrapWorkTypeConfig(resp[i], `工作类型清单[${i}]`)
  }
  return resp as WorkTypeConfig[]
}

export function unwrapWorkType(resp: unknown): WorkTypeConfig {
  return unwrapWorkTypeConfig(resp, '工作类型')
}

function unwrapWorkTypeConfig(v: unknown, where: string): WorkTypeConfig {
  const o = requireObject(v, where)
  requireKeys(o, WORK_TYPE_CONFIG_REQUIRED_KEYS, where)
  for (const k of ['key', 'label', 'category', 'l1_task_type', 'default_profile', 'updated_at'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if (typeof o['enabled'] !== 'boolean') throw new Error(`${where} 的 enabled 不是布尔`)
  if (typeof o['sort_order'] !== 'number') throw new Error(`${where} 的 sort_order 不是数字`)
  // ★ tags / prompt_keywords：NOT NULL DEFAULT '{}' ⇒ 恒为数组。
  if (!Array.isArray(o['tags'])) throw new Error(`${where} 的 tags 不是数组`)
  if (!Array.isArray(o['prompt_keywords'])) throw new Error(`${where} 的 prompt_keywords 不是数组`)
  // ★ omitempty 指针键：**存在才校验**。
  for (const k of ['system_prompt', 'acc_task_type'] as const) {
    if (k in o && typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if ('synced_from_acc_at' in o && typeof o['synced_from_acc_at'] !== 'string') {
    throw new Error(`${where} 的 synced_from_acc_at 不是字符串`)
  }
  if ('model_routes' in o && o['model_routes'] !== null) {
    const arr = o['model_routes']
    if (!Array.isArray(arr)) throw new Error(`${where} 的 model_routes 不是数组也不是 null`)
    for (let j = 0; j < arr.length; j++) {
      unwrapModelRoute(arr[j], `${where} 的 model_routes[${j}]`)
    }
  }
  return o as unknown as WorkTypeConfig
}

function unwrapModelRoute(v: unknown, where: string): ModelRoute {
  const o = requireObject(v, where)
  requireKeys(o, ['canonical_name', 'weight', 'min_score', 'enabled', 'tier', 'task_quality_score'], where)
  if (typeof o['canonical_name'] !== 'string') throw new Error(`${where} 的 canonical_name 不是字符串`)
  if (typeof o['tier'] !== 'string') throw new Error(`${where} 的 tier 不是字符串`)
  for (const k of ['weight', 'min_score', 'task_quality_score'] as const) {
    if (typeof o[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof o['enabled'] !== 'boolean') throw new Error(`${where} 的 enabled 不是布尔`)
  // ★ id 是 int + omitempty ⇒ **可能不存在**，存在才校验。
  if ('id' in o && typeof o['id'] !== 'number') throw new Error(`${where} 的 id 不是数字`)
  return o as unknown as ModelRoute
}

export function unwrapWorkTypeStats(resp: unknown): WorkTypeStats {
  const d = requireObject(resp, '工作类型统计')
  requireKeys(d, WORK_TYPE_STATS_KEYS, '工作类型统计')
  if (typeof d['window_hours'] !== 'number') throw new Error('工作类型统计 的 window_hours 不是数字')
  if (typeof d['total_auto'] !== 'number') throw new Error('工作类型统计 的 total_auto 不是数字')
  if (typeof d['total_specified'] !== 'number') throw new Error('工作类型统计 的 total_specified 不是数字')

  const byWT = requireRecord(d['by_work_type'], '工作类型统计 的 by_work_type')
  for (const k of Object.keys(byWT)) {
    const where = `工作类型统计 的 by_work_type[${k}]`
    const e = requireObject(byWT[k], where)
    requireKeys(e, WORK_TYPE_STAT_ENTRY_KEYS, where)
    for (const f of ['key', 'label', 'category', 'l1_task_type'] as const) {
      if (typeof e[f] !== 'string') throw new Error(`${where} 的 ${f} 不是字符串`)
    }
    for (const f of ['count_24h', 'count_direct', 'count_l1_proxy'] as const) {
      if (typeof e[f] !== 'number') throw new Error(`${where} 的 ${f} 不是数字`)
    }
  }

  const byL1 = requireRecord(d['by_l1_task'], '工作类型统计 的 by_l1_task')
  for (const k of Object.keys(byL1)) {
    if (typeof byL1[k] !== 'number') throw new Error(`by_l1_task 的 ${k} 不是数字`)
  }

  const tops = requireArray(d['top_models'], '工作类型统计 的 top_models')
  for (let i = 0; i < tops.length; i++) {
    const where = `工作类型统计 的 top_models[${i}]`
    const t = requireObject(tops[i], where)
    requireKeys(t, WORK_TYPE_TOP_MODEL_KEYS, where)
    if (typeof t['model'] !== 'string') throw new Error(`${where} 的 model 不是字符串`)
    if (typeof t['count'] !== 'number') throw new Error(`${where} 的 count 不是数字`)
  }

  const sm = requireObject(d['sync_meta'], '工作类型统计 的 sync_meta')
  if (typeof sm['source'] !== 'string') throw new Error('sync_meta 的 source 不是字符串')
  if (typeof sm['enabled_count'] !== 'number') throw new Error('sync_meta 的 enabled_count 不是数字')
  if (typeof sm['route_count'] !== 'number') throw new Error('sync_meta 的 route_count 不是数字')
  if (typeof sm['acc_configured'] !== 'boolean') throw new Error('sync_meta 的 acc_configured 不是布尔')
  return d as unknown as WorkTypeStats
}

export function unwrapL1TaskTypes(resp: unknown): L1TaskTypesResponse {
  const d = requireObject(resp, 'L1 任务类型')
  requireKeys(d, ['items'], 'L1 任务类型')
  const items = requireArray(d['items'], 'L1 任务类型 的 items')
  for (let i = 0; i < items.length; i++) {
    const where = `L1 任务类型 的 items[${i}]`
    const o = requireObject(items[i], where)
    requireKeys(o, L1_TASK_TYPE_META_KEYS, where)
    for (const k of ['key', 'label', 'icon'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
    }
    if (typeof o['count'] !== 'number') throw new Error(`${where} 的 count 不是数字`)
  }
  return d as unknown as L1TaskTypesResponse
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
function requireRecord(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    throw new Error(`${where} 不是对象`)
  }
  return v as Record<string, unknown>
}
function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (4) 清单排序完全确定 ──

/** ★★★★ 见 (4)：`ORDER BY sort_order, key` 有 tiebreak ⇒ 严格升序可断言。 */
export function workTypesAreSortedBySortOrderThenKey(rows: WorkTypeConfig[]): boolean {
  for (let i = 1; i < rows.length; i++) {
    const prev = rows[i - 1] as WorkTypeConfig
    const cur = rows[i] as WorkTypeConfig
    if (prev.sort_order > cur.sort_order) return false
    if (prev.sort_order === cur.sort_order && prev.key > cur.key) return false
  }
  return true
}

// ── (5) model_routes 的三态 ──

/**
 * ★★★ 见 (5)：`model_routes` 可能是**键消失**（无路由）、`null`、或数组。
 * ⇒ 这条判据回答的是「这个工作类型**到底有没有**绑定模型路由」——
 * 键消失与 `null` 都算「没有」，而**两者都不该当成空数组渲染**。
 */
export function workTypeHasModelRoutes(wt: WorkTypeConfig): boolean {
  return Array.isArray(wt.model_routes) && wt.model_routes.length > 0
}

/** ★★ 见 (5)：`model_routes` 键**不存在**（omitempty + 无路由），与 `null` 同义。 */
export function workTypeModelRoutesKeyIsAbsent(wt: WorkTypeConfig): boolean {
  return !('model_routes' in (wt as unknown as Record<string, unknown>))
}

// ── (6) L1 列表：canonical 8 + DB 新增项 ──

/** ★★★★ 见 (6)：长度**恒 ≥ 8**（canonical 八项无条件 seed，见 `:221-228`）。 */
export function l1TaskTypesHasAtLeastCanonicalEight(items: L1TaskTypeMeta[]): boolean {
  return items.length >= WORK_TYPES_CANONICAL_L1_KEYS.length
}

/** ★★★★ 见 (6)：**前八项就是 canonical 且顺序固定**（新增项一律追加在后面）。 */
export function l1TaskTypesStartWithCanonicalInOrder(items: L1TaskTypeMeta[]): boolean {
  if (items.length < WORK_TYPES_CANONICAL_L1_KEYS.length) return false
  for (let i = 0; i < WORK_TYPES_CANONICAL_L1_KEYS.length; i++) {
    if ((items[i] as L1TaskTypeMeta).key !== WORK_TYPES_CANONICAL_L1_KEYS[i]) return false
  }
  return true
}

/** ★★★★ 见 (6)：新增项 `icon === '◆'` **等价于** `label === key`（回落成 key 本身）。 */
export function l1TaskTypeIsExtra(row: L1TaskTypeMeta): boolean {
  return row.icon === WORK_TYPES_EXTRA_L1_ICON
}

/** ★★★ 见 (6)：canonical 项的 icon **不是** `'◆'`（反向）。 */
export function l1TaskTypeIsCanonical(row: L1TaskTypeMeta): boolean {
  return row.icon !== WORK_TYPES_EXTRA_L1_ICON && row.label !== row.key
}

// ── (7) count 全为 0 是二义的 ──

/** ★★★★★ 见 (7)：**全部 count 都是 0** —— 「没用到」/「查询失败」/「db 为 nil」同形。 */
export function l1TaskTypeCountsAreAllZero(items: L1TaskTypeMeta[]): boolean {
  return items.every((r) => r.count === 0)
}

/** ★★★ 见 (7)：至少有一项非 0 ⇒ 那次查询**成功过**（反向）。 */
export function l1TaskTypeCountsHaveSignal(items: L1TaskTypeMeta[]): boolean {
  return items.some((r) => r.count !== 0)
}

// ── (8) count_24h 是派生字段 ──

/**
 * ★★★★★ 见 (8)：`count_24h === count_direct || count_24h === count_l1_proxy`
 * （direct 为 0 时用 L1 总量兜底）⇒ 这条判据把三个数字**互锁**起来。
 */
export function workTypeStatCountIsDerivedConsistently(e: WorkTypeStatEntry): boolean {
  return e.count_24h === e.count_direct || e.count_24h === e.count_l1_proxy
}

/** ★★ 见 (8)：`count_direct > 0` ⇒ `count_24h` **必须**等于它（不走 L1 兜底）。 */
export function workTypeStatPrefersDirectWhenPresent(e: WorkTypeStatEntry): boolean {
  return e.count_direct > 0 ? e.count_24h === e.count_direct : true
}

/** ★★★ 见 (8)：**同一个 `count_l1_proxy` 出现在多行** ⇒ 这张表重复计数。 */
export function workTypeStatsShareL1Proxy(entries: WorkTypeStatEntry[], l1TaskType: string): boolean {
  const same = entries.filter((e) => e.l1_task_type === l1TaskType)
  if (same.length < 2) return false
  const first = (same[0] as WorkTypeStatEntry).count_l1_proxy
  return same.every((e) => e.count_l1_proxy === first)
}

// ── (11) top_models 无 tiebreak ──

/** ★★★ 见 (11)：`ORDER BY c DESC` 无 tiebreak ⇒ 只能断言**非升序**，不能断言严格降序。 */
export function workTypeTopModelsAreNonAscending(r: WorkTypeStats): boolean {
  for (let i = 1; i < r.top_models.length; i++) {
    if ((r.top_models[i - 1] as { count: number }).count < (r.top_models[i] as { count: number }).count) {
      return false
    }
  }
  return true
}

/** ★★ 见 (11)：条数**硬上限 10**，响应里没有任何字段说明。 */
export function workTypeTopModelsWithinLimit(r: WorkTypeStats): boolean {
  return r.top_models.length <= WORK_TYPES_TOP_MODELS_LIMIT
}

// ── (12) model_routes 排序是三层确定 tiebreak ──

const TIER_RANK: Record<string, number> = { primary: 0, secondary: 1, fallback: 2 }

/**
 * ★★★★ 见 (12)：`ORDER BY work_type_key, tier CASE, weight DESC, canonical_name`
 * ⇒ 可自验「先 tier 分组、组内 weight 降序、同权重按 canonical_name 升序」。
 * ★ 未知 tier 排在三者之后（SQL 的 `ELSE 3`），故排在最后。
 */
export function modelRoutesAreOrderedByTierThenWeight(routes: ModelRoute[]): boolean {
  const rank = (t: string): number => (t in TIER_RANK ? (TIER_RANK[t] as number) : 3)
  for (let i = 1; i < routes.length; i++) {
    const prev = routes[i - 1] as ModelRoute
    const cur = routes[i] as ModelRoute
    const pr = rank(prev.tier)
    const cr = rank(cur.tier)
    if (pr > cr) return false
    if (pr === cr) {
      if (prev.weight < cur.weight) return false
      if (prev.weight === cur.weight && prev.canonical_name > cur.canonical_name) return false
    }
  }
  return true
}

// ── (13) tier 的三个取值 ──

/** ★★ 见 (13)：COALESCE + CHECK ⇒ `tier` 恒为三值之一。 */
export function modelRouteTierIsKnown(tier: string): boolean {
  return (WORK_TYPES_ROUTE_TIERS as readonly string[]).includes(tier)
}

// ── (2) 错误码 ──

/** ★★★ 见 (2)：错误**必须按 `code` 匹配**；`message` 是本地化文本，不能用。 */
export function workTypeErrorCodeIs(code: string, expected: string): boolean {
  return code === expected
}

/** ★★★ 见 (2)：错误体是第四种形状（`message` + `code` + `type`）—— 见 §11.126 对照。 */
export function workTypeErrorBodyKeys(): readonly string[] {
  return ['message', 'code', 'type'] as const
}

// ── (16)(17) 路由形状 ──

/**
 * ★★ 见 (16)：`include_disabled == "true"` 是**精确字符串比较** ⇒
 * `"1"` / `"yes"` / `"TRUE"` 全部静默当作 false。
 */
export function workTypesIncludeDisabledIsEffective(raw: string | null | undefined): boolean {
  return raw === 'true'
}

/** ★★ 见 (17)：`handleSub` 做 `strings.Trim(rest, "/")` ⇒ **尾斜杠与不带等价**。 */
export function workTypePathWithTrailingSlashIsEquivalent(key: string, withSlash: boolean): boolean {
  const rest = withSlash ? `${key}/` : key
  return rest.replace(/^\/+|\/+$/g, '') === key.replace(/^\/+|\/+$/g, '')
}
