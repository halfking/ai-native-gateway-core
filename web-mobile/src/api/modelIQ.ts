import { req, type RequestOptions } from './client'

/**
 * modelIQ.ts — 节点智商（模型 IQ）三端点（2026-10-08，第八十五批）。
 *
 * GET /api/admin/model-iq/node-latest
 * GET /api/admin/model-iq/history
 * GET /api/admin/model-iq/catalog
 *
 * - **注册**：`admin/handler.go:1492-1495`，四条**全部**是 `h.superAdmin(...)`
 *   （:1492 catalog / :1493 node-latest / :1494 history / :1495 trigger）
 *   ⇒ ★★ **superAdmin 档** ⇒ 抽屉席须设 `requiresRole: 'super_admin'`
 *     并同步 `src/components/shell/AppDrawer.spec.ts` 白名单（按字母序插）。
 *   ⚠️ **2026-10-14 第一百一十六批订正**：本模块头原先写的是 `:1470-1472`，
 *     **漂了 22 行**，且把「四条」写成「三个」。漂移发生在**同一族**里 ——
 *     与批 113（`slidingWindow.ts` 模块头第 (6) 条抄漏 Go 那行、导致判据恒 false）
 *     同族，但那次是**抄漏一个字**，这次是**抄漏一整段 + 少算一条**。
 *     ⇒ ★★★ **行号只是定位提示，不是契约**；注释里引的每一处行号
 *       都得对着事实侧（`grep -n` 实文件）核，核出来的差值要**当场改掉**，
 *       不要写「行号仅供参考」了事。
 *   - **不在** `maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *   - `trigger` 是 **POST**（有外部副作用、要真花 token）⇒ **不碰**。
 *
 * - ★★★★★★ **superAdmin 档是真门（本仓第四档里唯一「档位自带 role 检查」的一档）**：
 *   `h.superAdmin` → `SuperAdminMiddleware`（`admin/auth.go:376-409`，**逐字读过**）：
 *   ```go
 *   if claims.Role != "super_admin" {
 *       writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
 *       return
 *   }
 *   ```
 *   ⇒ 与批 113/114 的 `admin(...)` **形态相反**：那两条走 `AdminMiddleware`
 *     （`auth.go:85-138`）**只认证不检 role**（`users.go:307-309` 注释自陈
 *     是显式设计）⇒ 本仓才被迫推出 `requiresRole: 'admin'` 这一档。
 *     本族走 `SuperAdminMiddleware` ⇒ **`claims.Role` 逐字比对**，
 *     所以抽屉席设 `requiresRole: 'super_admin'` 就是**完整处置**，
 *     **不需要**任何段落级门控（照批 109 `FormatAnomaliesView.vue` 的范式）。
 *   - ★★ **与 `AdminMiddleware` 的第二个差别**：`SuperAdminMiddleware`
 *     **只有 JWT 分支，没有 admin key 分支** ⇒ 持 admin key 的调用方
 *     连认证都过不了（落到末尾 `401 authentication required`），
 *     与 `AdminMiddleware` 的「JWT **或** admin key」不同。
 *   - ★★ 403 文案逐字是 `super_admin role required for this endpoint`
 *     （全仓 7 处同文案，如 `auth.go:384` / `tenants.go:137`）
 *     ⇒ **不能靠文案区分来源端点**；但本族全族同档 ⇒ 也不需要区分。
 *
 * - ★★★★ **桌面端两个读端点是死代码**（2026-10-14 第一百一十六批实测）：
 *   `web/src/api/model-iq.ts` 导出了 4 个函数，但全仓 grep 调用点只有
 *   `web/src/components/model/ModelOfferExtrasPanel.vue` 用了
 *   `getModelIQHistory` + `triggerModelIQTest`；
 *   **`getCatalogIQ` 与 `getNodeIQLatest` 零调用方**
 *   （连绕过封装的直接 URL 调用都没有 —— `model-iq/catalog` 与
 *   `model-iq/node-latest` 这两个 URL 串在全仓只出现在封装文件自己里）。
 *   ⇒ ★★ `web/src/api/model-iq.ts` 的文件头自称
 *     "Backs the provider model list (standard IQ + node IQ columns)" —— **那是注释，
 *     不是事实**（与批 115「模块头第 (6) 条抄漏 Go 那行」同族的另一个面：
 *     **注释可以描述一个不存在的调用方**）。
 *   ⇒ ★★★ 本移动端页面把 catalog / node-latest 也做进 UI
 *     ⇒ **移动端比桌面端多覆盖两个读端点**（这是本批的功能增量所在）。
 * - **实现**：`admin/model_iq.go`；写入侧 `domains/modelquality/dbstorage.go`。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十四件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **三个端点的响应都是顶层裸数组**，不是 `{data:…}` 信封。
 *      `:120` / `:186` / `:254` 三处都是 `writeJSON(w, 200, out)`，
 *      而 `out := []T{}` 初始化 ⇒ **无匹配时是 `[]` 而不是 null**。
 *      ⇒ ★★ 与本仓多数端点的「envelope」形状相反 ⇒ 解包器顶层就校验数组。
 *
 * (2) ★★★★★ **`catalog` 的 10 键里 4 个是 `*float64` 且无 omitempty**
 *      ⇒ `standard_iq` / `node_avg_iq` / `max_node_iq` / `min_node_iq`
 *        **都可能是裸 `null`**（`catalogIQRow:195-200`）。
 *      ⇒ ★★★ 这是「指针 + 无 omitempty ⇒ 裸 null」在**非 map** struct 上的形态。
 *
 * (3) ★★★★★ **`catalog` 有一条锐利的可自验不变式**：`node_count` 与三个聚合
 *      互为充要 —— `LEFT JOIN LATERAL` 聚合无输入时 `count(*)` 返 0 而
 *      `avg/max/min` 返 NULL ⇒
 *      - `node_count === 0` ⇒ `node_avg_iq`/`max_node_iq`/`min_node_iq` **三者全 null**
 *      - `node_count > 0`  ⇒ 三者**全非 null**
 *      ⇒ 且 WHERE `standard_iq IS NOT NULL OR node_cnt > 0` ⇒
 *        **`standard_iq` 与 `node_count>0` 至少有一个成立**（`standard_iq` 单独可 null）。
 *
 * (4) ★★★★★ **`status` 可从 `accuracy`/`stability` 反推**（`dbstorage.go:80-87`）：
 *      ```go
 *      status := "success"
 *      if score.Stability < 100 { status = "partial" }
 *      if score.Accuracy <= 0 && score.Stability <= 0 { status = "failed" }
 *      ```
 *      ⇒ ★★★ 这是**写入侧**算出来的三值域 `{success, partial, failed}`
 *        ⇒ `/history` 客户端可做**自洽校验**（比对 status 与 accuracy/stability）。
 *      ⇒ ★★ 但 `history` 的 SQL 把 `stability` COALESCE 成 0
 *        ⇒ DB 里 stability 为 NULL 时响应里是 0 ⇒ 与「真的 0」二义（见 (8)）。
 *
 * (5) ★★★★ **`node-latest` 的 13 键里 10 个被 COALESCE 兜底**（`:75-85`）
 *      ⇒ 空串 / 0 全部可达；只有 `credential_id` 与 `raw_model_name` 是裸值。
 *      ⇒ ★★ `provider_id` 会被 `COALESCE(c.provider_id,0)` 兜成 **0**
 *        （凭据没挂 provider 时）⇒ 客户端不能把 `provider_id===0` 当「非法」。
 *
 * (6) ★★★★ **同一族、不同端点的参数校验风格与文案都不同**：
 *      - `node-latest` 的 `provider_id`/`canonical_id`：`ParseInt` +
 *        **`<= 0` 拒绝**，文案 `invalid provider_id` / `invalid canonical_id`
 *        （**ParseInt 前无 TrimSpace**）。
 *      - `history` 的 `credential_id`：`ParseInt` + `<= 0` 拒绝，
 *        但文案是 **`credential_id required`**（不是 invalid！）。
 *      ⇒ ★★★ 同一族的「同一个参数名」在两个端点**错误文案不同**。
 *
 * (7) ★★★★ **`history` 的 `limit` 静默回落，不 400**：`Atoi`（**无 TrimSpace**），
 *      仅当 `n > 0 && n <= 500` 才采纳，否则**静默用缺省 50**（`:155-160`）。
 *      ⇒ ★★ 非法 limit **不报错**，客户端无法从响应看出 limit 是否被采纳。
 *
 * (8) ★★★ **`history` 把 `stability`/`latency_p95` COALESCE 成 0**
 *      ⇒ `0` 是二义的（真 0 或 NULL）；配合 (4) 的反推，NULL-stability 的行
 *        在响应里**看起来像 failed**。
 *
 * (9) ★★★★ **`history` 的 `tested_at` 是 `COALESCE(tested_at, created_at)`**
 *      且列类型 `time.Time` ⇒ 回显 **RFC3339Nano**；⇒ 客户端**无法区分**
 *        「真的测过时间」与「用创建时间兜底」。
 *
 * (10) ★★★★ **`catalog` 的 WHERE 排除了 `hidden` 状态**
 *      （`status IN ('active','disabled','deprecated')`），而
 *      `models_canonical.status` 的 CHECK 域是**四值**（含 `hidden`，见
 *      `deploy/sql/schemas/baseline/01-schema.sql:10325`）⇒
 *      **hidden 模型永远不出现在 `/catalog`**。端点过滤 + 表有 CHECK 的组合。
 *
 * (11) ★★★ **`node-latest` 排序是 `overall_score DESC NULLS LAST, credential_id`**，
 *      而 `catalog` 是 `COALESCE(standard_iq, node_avg) DESC NULLS LAST`
 *      ⇒ 后者排序键是 COALESCE 结果 ⇒ 两个端点降序口径不同。
 *
 * (12) ★★★ **500 错误的 detail 就是 `op` 字符串**（`writeInternalErr` 把 op
 *      原样写进 body）⇒ 本族 500 的 detail 只可能是
 *      `query` / `scan` / `modelIQ.latest` / `modelIQ.history` / `modelIQ.catalog`
 *      ⇒ ★★ **完全可区分**（与批 80 的「两条路径文案相同」相反）。
 *      ⇒ 另有 `writeAggRowsErr`：遇 missing relation（42P01）会把迭代错误
 *        改写成 **503 `analytics_view_missing`**（第四种错误路径）。
 *
 * (13) ★★ 503 文案是 **`database not configured`**（`h.db == nil`）——
 *      本仓常见措辞（对照批 84 的 `database not available`）。
 *
 * (14) ★★ `probe_kind` 三值 `{gateway, direct, mock}`（`benchmark.go:27-29`），
 *      写入缺省 `direct`；`trigger_kind` 写入缺省 `scheduled`（域开放）。
 *      `benchmark_type` 缺省 `mmlu_lite`，但 **`/history` 不返回该列** ⇒ 不可见。
 *
 * (15) ★★★★★★ **同族的两个端点，`tested_at` 是两种「长得一样但机制完全不同」的串**
 *      （2026-10-14 第一百一十六批逐字核出来；这是本族最容易被判据蒙过去的一条）：
 *
 *      | 端点 | SELECT 里的写法 | Go 字段 | 精度 | 可为 null？ |
 *      |---|---|---|---|---|
 *      | `node-latest` | `to_char(n.tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` | `*string` | **秒**（丢亚秒） | **能**（`to_char(NULL)` → NULL） |
 *      | `history` | `COALESCE(tested_at, created_at)` | `time.Time` | **纳秒**（RFC3339Nano） | **不能**（`created_at` NOT NULL） |
 *
 *      ⇒ ★★★ `node-latest` 那个 `"Z"` 是**格式串里的带引号字面量**，
 *        不是时区标记 —— 它无条件被追加，与 `AT TIME ZONE 'UTC'` 的计算**无关**。
 *        两条路都能产出 `2024-05-01T12:34:56Z` 这种**合法 RFC3339**，
 *        所以**从响应里无法区分是哪条路**。
 *      ⇒ ★★★★ **连现有的 `modelIQHistoryTestedAtIsRfc3339` 都不是判别式**：
 *        它只查 `includes('T') && length > 10`，
 *        而 `node-latest` 的 `to_char` 产物（恒 20 字符、含 `T`）**同样满足**
 *        ⇒ **拿它当「格式对不对」的判据 ⇒ 恒真**。
 *        真要判别只能查**纳秒小数点**（`to_char` 那一路恒无 `.`）。
 *      ⇒ ★★ 落到 UI：`node-latest` 的时间显示**不要**显示到秒以下，
 *        也**不要**因为 `history` 精确到纳秒就以为两段是同一种时间戳来源。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验三个端点的**顶层是数组** +
 *   每项的**全部恒在键与类型**（13 / 9 / 10 键）+ 上述可自验不变式的**判据函数**。
 *   ⇒ 不校验排序（排序依赖 DB 状态，客户端只作为提示）。
 *   ⇒ ★★ 也不校验 `tested_at` 的**秒/纳秒精度**（见 (15)：响应里不可判别）。
 */

/** `:48` 503 文案（三个端点共用）。见 (13)。 */
export const MODEL_IQ_DB_NOT_CONFIGURED = 'database not configured'

/** `dbstorage.go:80-87` 写入的 status 三值域。见 (4)。 */
export const MODEL_IQ_STATUSES = ['success', 'partial', 'failed'] as const

/** `benchmark.go:27-29` 的 probe_kind 三值。见 (14)。 */
export const MODEL_IQ_PROBE_KINDS = ['gateway', 'direct', 'mock'] as const

/** `dbstorage.go:121` 写入缺省 `direct`。 */
export const MODEL_IQ_PROBE_DEFAULT = 'direct'
/** `dbstorage.go:95` 写入缺省 `scheduled`。 */
export const MODEL_IQ_TRIGGER_DEFAULT = 'scheduled'

/** `catalog` WHERE 放行的三值（**hidden 被排除**）。见 (10)。 */
export const MODEL_IQ_CATALOG_STATUS_FILTER = ['active', 'disabled', 'deprecated'] as const
/** `models_canonical_status_check` 的四值域（含 hidden）。见 (10)。 */
export const MODEL_IQ_CANONICAL_STATUS_DOMAIN = ['active', 'disabled', 'deprecated', 'hidden'] as const

/** `history` 的 `limit` 缺省 50、上限 500。见 (7)。 */
export const MODEL_IQ_HISTORY_DEFAULT_LIMIT = 50
export const MODEL_IQ_HISTORY_MAX_LIMIT = 500

/** `nodeIQLatestRow` 的 13 个恒在键。 */
export const MODEL_IQ_NODE_LATEST_KEYS = [
  'credential_id',
  'credential_label',
  'provider_id',
  'provider_name',
  'raw_model_name',
  'canonical_name',
  'overall_score',
  'grade',
  'avg_score',
  'min_score',
  'max_score',
  'sample_count',
  'tested_at',
] as const

/** `iqHistoryPoint` 的 9 个恒在键。 */
export const MODEL_IQ_HISTORY_KEYS = [
  'tested_at',
  'overall_score',
  'grade',
  'accuracy',
  'stability',
  'latency_p95',
  'probe_kind',
  'trigger_kind',
  'status',
] as const

/** `catalogIQRow` 的 10 个恒在键。 */
export const MODEL_IQ_CATALOG_KEYS = [
  'canonical_id',
  'canonical_name',
  'display_name',
  'family',
  'standard_iq',
  'standard_iq_source',
  'node_avg_iq',
  'node_count',
  'max_node_iq',
  'min_node_iq',
] as const

/** `catalog` 里 4 个**可为裸 null** 的键。见 (2)。 */
export const MODEL_IQ_CATALOG_NULLABLE_KEYS = [
  'standard_iq',
  'node_avg_iq',
  'max_node_iq',
  'min_node_iq',
] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface ModelIQNodeLatest {
  credential_id: number
  /** ★ 空串可达（COALESCE 兜底）。见 (5)。 */
  credential_label: string
  /** ★ 0 可达（凭据没挂 provider）。见 (5)。 */
  provider_id: number
  /** ★ 空串可达。 */
  provider_name: string
  raw_model_name: string
  /** ★ 空串可达。 */
  canonical_name: string
  overall_score: number
  /** ★ 空串可达。 */
  grade: string
  avg_score: number
  min_score: number
  max_score: number
  sample_count: number
  /** `*string` 无 omitempty ⇒ string 或裸 null。见 (5)。 */
  tested_at: string | null
}

export interface ModelIQHistoryPoint {
  /** `time.Time` ⇒ RFC3339Nano。见 (9)。 */
  tested_at: string
  overall_score: number
  grade: string
  accuracy: number
  /** ★ COALESCE 成 0，与真 0 二义。见 (8)。 */
  stability: number
  /** ★ COALESCE 成 0。见 (8)。 */
  latency_p95: number
  probe_kind: string
  trigger_kind: string
  /** ★ 三值域，可从 accuracy/stability 反推。见 (4)。 */
  status: string
}

export interface ModelIQCatalogRow {
  canonical_id: number
  canonical_name: string
  /** ★ 空串可达（COALESCE 兜底）。 */
  display_name: string
  family: string
  /** ★ 可为裸 null。 */
  standard_iq: number | null
  standard_iq_source: string
  /** ★ 可为裸 null；与 node_count 互为充要。见 (3)。 */
  node_avg_iq: number | null
  node_count: number
  /** ★ 可为裸 null。见 (2)(3)。 */
  max_node_iq: number | null
  /** ★ 可为裸 null。见 (2)(3)。 */
  min_node_iq: number | null
}

export interface ModelIQNodeLatestParams {
  providerId?: number
  canonicalId?: number
}
export interface ModelIQHistoryParams {
  credentialId: number
  rawModelName: string
  /** 非法值静默回落 50（不 400）。见 (7)。 */
  limit?: number
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/model-iq/node-latest`（superAdmin 档）。 */
export function fetchModelIQNodeLatest(
  params: ModelIQNodeLatestParams = {},
  options?: RequestOptions,
): Promise<ModelIQNodeLatest[]> {
  const qs = new URLSearchParams()
  if (params.providerId != null) qs.set('provider_id', String(params.providerId))
  if (params.canonicalId != null) qs.set('canonical_id', String(params.canonicalId))
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/model-iq/node-latest${s ? `?${s}` : ''}`, undefined, options).then(
    unwrapModelIQNodeLatest,
  )
}

/** GET `/api/admin/model-iq/history`（superAdmin 档）。两个必填参数。 */
export function fetchModelIQHistory(
  params: ModelIQHistoryParams,
  options?: RequestOptions,
): Promise<ModelIQHistoryPoint[]> {
  const qs = new URLSearchParams()
  qs.set('credential_id', String(params.credentialId))
  qs.set('raw_model_name', params.rawModelName)
  if (params.limit != null) qs.set('limit', String(params.limit))
  return req<unknown>('GET', `/api/admin/model-iq/history?${qs.toString()}`, undefined, options).then(
    unwrapModelIQHistory,
  )
}

/** GET `/api/admin/model-iq/catalog`（superAdmin 档）。无参数。 */
export function fetchModelIQCatalog(options?: RequestOptions): Promise<ModelIQCatalogRow[]> {
  return req<unknown>('GET', '/api/admin/model-iq/catalog', undefined, options).then(unwrapModelIQCatalog)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包 —— 三端点都是顶层裸数组。见 (1)。
// ═══════════════════════════════════════════════════════════════════════════

/** 顶层必须是数组（**不是** envelope）。见 (1)。 */
export function unwrapModelIQNodeLatest(resp: unknown): ModelIQNodeLatest[] {
  const arr = requireTopArray(resp, '节点智商最新')
  for (let i = 0; i < arr.length; i++) {
    const o = requireObject(arr[i], `节点智商最新[${i}]`)
    requireKeys(o, MODEL_IQ_NODE_LATEST_KEYS, `节点智商最新[${i}]`)
    for (const k of [
      'credential_id',
      'provider_id',
      'overall_score',
      'avg_score',
      'min_score',
      'max_score',
      'sample_count',
    ] as const) {
      if (typeof o[k] !== 'number') throw new Error(`节点智商最新[${i}] 的 ${k} 不是数字`)
    }
    for (const k of ['credential_label', 'provider_name', 'raw_model_name', 'canonical_name', 'grade'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`节点智商最新[${i}] 的 ${k} 不是字符串`)
    }
    // ★ tested_at 是 *string 无 omitempty ⇒ string 或裸 null。见 (5)。
    if (o['tested_at'] !== null && typeof o['tested_at'] !== 'string') {
      throw new Error(`节点智商最新[${i}] 的 tested_at 不是字符串也不是 null`)
    }
  }
  return arr as ModelIQNodeLatest[]
}

export function unwrapModelIQHistory(resp: unknown): ModelIQHistoryPoint[] {
  const arr = requireTopArray(resp, '智商历史')
  for (let i = 0; i < arr.length; i++) {
    const o = requireObject(arr[i], `智商历史[${i}]`)
    requireKeys(o, MODEL_IQ_HISTORY_KEYS, `智商历史[${i}]`)
    for (const k of ['overall_score', 'accuracy', 'stability', 'latency_p95'] as const) {
      if (typeof o[k] !== 'number') throw new Error(`智商历史[${i}] 的 ${k} 不是数字`)
    }
    for (const k of ['tested_at', 'grade', 'probe_kind', 'trigger_kind', 'status'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`智商历史[${i}] 的 ${k} 不是字符串`)
    }
  }
  return arr as ModelIQHistoryPoint[]
}

export function unwrapModelIQCatalog(resp: unknown): ModelIQCatalogRow[] {
  const arr = requireTopArray(resp, '智商目录')
  for (let i = 0; i < arr.length; i++) {
    const o = requireObject(arr[i], `智商目录[${i}]`)
    requireKeys(o, MODEL_IQ_CATALOG_KEYS, `智商目录[${i}]`)
    for (const k of ['canonical_id', 'node_count'] as const) {
      if (typeof o[k] !== 'number') throw new Error(`智商目录[${i}] 的 ${k} 不是数字`)
    }
    for (const k of ['canonical_name', 'display_name', 'family', 'standard_iq_source'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`智商目录[${i}] 的 ${k} 不是字符串`)
    }
    // ★ 四个 *float64 无 omitempty ⇒ number 或裸 null。见 (2)。
    for (const k of MODEL_IQ_CATALOG_NULLABLE_KEYS) {
      if (o[k] !== null && typeof o[k] !== 'number') {
        throw new Error(`智商目录[${i}] 的 ${k} 不是数字也不是 null`)
      }
    }
  }
  return arr as ModelIQCatalogRow[]
}

// ── 守卫 ─────────────────────────────────────────────────────────────────────

function requireTopArray(resp: unknown, where: string): unknown[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望顶层裸数组，实得 ${actual}`)
  }
  return resp
}
function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
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

// ── (4) status 从 accuracy/stability 反推（★ 最锐利的自洽校验） ──

/**
 * ★★★★★ 见 (4)：**按后端 `dbstorage.go:80-87` 原样重算 `status`**。
 *
 * ```go
 * status := "success"
 * if stability < 100 { status = "partial" }
 * if accuracy <= 0 && stability <= 0 { status = "failed" }   // 最后判定，优先级最高
 * ```
 *
 * ★★ 注意第三条在**最后**且覆盖前两条 ⇒ `accuracy<=0 && stability<=0`
 *   时必定是 `failed`，哪怕 stability 本可以是 <100 的 partial。
 */
export function modelIQDeriveStatus(accuracy: number, stability: number): string {
  if (accuracy <= 0 && stability <= 0) return 'failed'
  if (stability < 100) return 'partial'
  return 'success'
}

/** 见 (4)：`status` 与 `accuracy`/`stability` 自洽（写入侧同源算出）。 */
export function modelIQHistoryStatusMatchesMetrics(p: ModelIQHistoryPoint): boolean {
  return modelIQDeriveStatus(p.accuracy, p.stability) === p.status
}

/** 见 (4)：三值 status 都是合法值。 */
export function modelIQStatusIsKnown(p: ModelIQHistoryPoint): boolean {
  return (MODEL_IQ_STATUSES as readonly string[]).includes(p.status)
}

// ── (3) catalog：node_count 与三个聚合互为充要 ──

/**
 * ★★★★★ 见 (3)：**`node_count` 与三个聚合互为充要**。
 *
 * `LEFT JOIN LATERAL` 聚合无输入时 `count(*)` 返 0、`avg/max/min` 返 NULL
 * ⇒ `node_count === 0` ⇒ 三个聚合**全 null**；`node_count > 0` ⇒ **全非 null**。
 */
export function modelIQCatalogAggregatesMatchCount(row: ModelIQCatalogRow): boolean {
  // ★ 保留 `=== 0` 而不是 `<= 0`：`node_count` 来自 SQL `count(*)`，
  //   扫进 Go `int` ⇒ **可达集合只有非负整数** ⇒ 两种写法可证等价。
  if (row.node_count === 0) {
    return row.node_avg_iq === null && row.max_node_iq === null && row.min_node_iq === null
  }
  return row.node_avg_iq !== null && row.max_node_iq !== null && row.min_node_iq !== null
}

/**
 * ★★★★ 见 (3)：WHERE `standard_iq IS NOT NULL OR node_cnt > 0`
 * ⇒ **`standard_iq` 与 `node_count>0` 至少一个成立**。
 */
export function modelIQCatalogHasStandardOrNodes(row: ModelIQCatalogRow): boolean {
  return row.standard_iq !== null || row.node_count > 0
}

/** ★★★ 见 (2)：四个可空键里，「有节点」时 `node_avg_iq` 必非 null。 */
export function modelIQCatalogNodeAvgIsNull(row: ModelIQCatalogRow): boolean {
  return row.node_avg_iq === null
}

/** ★★ `standard_iq` 可为 null（即使有节点）；此时 catalog 靠节点分排序。见 (10)。 */
export function modelIQCatalogStandardIqIsNull(row: ModelIQCatalogRow): boolean {
  return row.standard_iq === null
}

// ── (5) node-latest：COALESCE 兜底的可辨形态 ──

/** ★★★ 见 (5)：`provider_id === 0` 表示「凭据没挂 provider」（COALESCE 兜底）。 */
export function modelIQNodeHasNoProvider(row: ModelIQNodeLatest): boolean {
  return row.provider_id === 0
}

/** ★★ 见 (5)：`provider_id > 0` 表示正常挂了 provider。 */
export function modelIQNodeHasProvider(row: ModelIQNodeLatest): boolean {
  return row.provider_id > 0
}

/** ★★★ 见 (5)：`tested_at` 是裸 null（该节点从未测过 / NULL）。 */
export function modelIQNodeTestedAtIsNull(row: ModelIQNodeLatest): boolean {
  return row.tested_at === null
}

/** ★★ 反向：`tested_at` 是 RFC3339 串（`to_char` 秒级，见下）。 */
export function modelIQNodeTestedAtIsString(row: ModelIQNodeLatest): boolean {
  return typeof row.tested_at === 'string'
}

/**
 * ★★★★ 见 (15)：**`node-latest` 的 `tested_at` 恒为「秒精度 + 恒 20 字符」**。
 *
 * `to_char(…, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` 里没有任何小数位占位符
 * ⇒ 产物形如 `2024-05-01T12:34:56Z`，**恒 20 字符、恒无 `.`**。
 *
 * ★★★ **为什么要有这个函数**：`modelIQHistoryTestedAtIsRfc3339` 只查
 *   `includes('T') && length > 10`，而这一路的产物**同样满足**
 *   ⇒ 拿它当「格式对不对」的判据是**恒真**。
 *   真判别只能查**有没有亚秒小数点**：history（`time.Time` ⇒ RFC3339Nano）
 *   在有亚秒时会带 `.`，这一路**恒不带**。
 *
 * ⚠️ 边界：若 `tested_at` 恰好落在整秒上，`time.Time` 的 marshal
 *   **也会省略小数点**（Go 的 RFC3339Nano 是「有精度才带」）⇒
 *   所以这个函数判的是「**本端点产出的串没有亚秒位**」，
 *   **不是**「这个时间点本身没有亚秒」。
 */
export function modelIQNodeTestedAtIsSecondPrecision(row: ModelIQNodeLatest): boolean {
  if (row.tested_at === null) return false
  return row.tested_at.length === 20 && !row.tested_at.includes('.')
}

// ── (7)(8)(9) history 的 limit / COALESCE / 回显 ──

/**
 * ★★★★ 见 (7)：这个 `limit` 会不会被后端采纳（`n > 0 && n <= 500`）。
 * ⇒ 非法值**静默回落 50**，不报错。
 */
export function modelIQHistoryLimitIsAccepted(n: number): boolean {
  return n > 0 && n <= MODEL_IQ_HISTORY_MAX_LIMIT
}

/** ★★ 见 (7)：非法 limit 的实际生效值（恒为缺省 50）。 */
export function modelIQHistoryEffectiveLimit(n: number): number {
  return modelIQHistoryLimitIsAccepted(n) ? n : MODEL_IQ_HISTORY_DEFAULT_LIMIT
}

/**
 * ★★ 见 (8)：`stability === 0` 是二义的（COALESCE 的 NULL 还是真 0）——
 * 判据只说「是 0」，不断言它必是 failed。
 */
export function modelIQHistoryStabilityIsZero(p: ModelIQHistoryPoint): boolean {
  // ★ 保留 `=== 0` 而不是 `!p.stability`：JSON 数字里唯一的 falsy 是 `0`
  //   （`NaN` 序列化不成、`-0 === 0` 且同样 falsy）⇒ 两者可证等价。
  return p.stability === 0
}

/** ★★ 见 (9)：`tested_at` 是 `time.Time` ⇒ RFC3339Nano（含 T、长度 > 10）。 */
export function modelIQHistoryTestedAtIsRfc3339(p: ModelIQHistoryPoint): boolean {
  return p.tested_at.includes('T') && p.tested_at.length > 10
}

// ── (10) catalog：hidden 永远不出现在响应里 ──

/** ★★★★ 见 (10)：这个 canonical 的 status 若为 `hidden`，它不会出现在 `/catalog`。 */
export function modelIQCatalogWouldBeHidden(status: string): boolean {
  return status === 'hidden'
}

/** ★★ 见 (10)：catalog 只放行三个状态，`hidden` 不在其中。 */
export function modelIQCatalogIncludesStatus(status: string): boolean {
  return (MODEL_IQ_CATALOG_STATUS_FILTER as readonly string[]).includes(status)
}

// ═══════════════════════════════════════════════════════════════════════════
// 角色与报文 —— superAdmin 档
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★★ 该角色是否被允许**访问本页**。
 *
 * 本族三条读端点全走 `SuperAdminMiddleware`（`admin/auth.go:376-409`），
 * 它逐字比对 `claims.Role != "super_admin"` ⇒ 只有 `super_admin` 能过。
 * 抽屉席 `requiresRole: 'super_admin'` 之外，**视图内还要兜一层** ——
 * 因为 `meta.requiresRole` 全仓无人消费（`hyper-wiring.ts` 的 `beforeEach`
 * 只查 `requiresAuth` 与 `auth.isAuthenticated`）⇒ 深链照样能打开页面。
 *
 * ⚠️ **档位对比（本批最容易搞混的一点）**：对 superAdmin 档页面，
 *   这一层兜底是**体验层**的（不让 tenant_admin 看见一个必然 403 的壳）；
 *   真正的安全门在服务端中间件，**不是这一层**。
 *   批 113/114 的 `requiresRole: 'admin'` 档**相反** ——
 *   那两族走 `AdminMiddleware`，**只认证不检 role**，
 *   所以视图内兜底是**唯一**挡在 tenant_admin 与跨租户数据之间的东西。
 *   ⇒ 判权限时不要把这两档的兜底当成同一件事。
 */
export function modelIQRoleIsAllowed(role: string): boolean {
  return role === 'super_admin'
}

/** ★★★ 403 文案逐字：`super_admin role required for this endpoint`（`auth.go:384`）。 */
export function modelIQSuperAdminOnlyMessage(msg: string): boolean {
  return /super_admin/i.test(msg)
}

/**
 * ★★★ 本族的 403 **只可能**来自 `SuperAdminMiddleware`（三个 handler 自己不检 role，
 *   `model_iq.go` 里没有任何 `claims.Role` / `IsSuperAdmin` 判断）
 *   ⇒ 见到 403 就等于「这个角色进不来」，不必再猜是哪一层挡的。
 *   ⚠️ 文案不可用于**区分来源端点**（全仓 7 处同文案），但本族全族同档 ⇒ 也不需要区分。
 */
export function modelIQForbiddenMessage(msg: string): boolean {
  return /forbidden/i.test(msg) || modelIQSuperAdminOnlyMessage(msg)
}

/** ★★ 503：三个端点共用 `database not configured`（`h.db == nil`）。见 (13)。 */
export function modelIQDbNotConfiguredMessage(msg: string): boolean {
  return msg.trim() === MODEL_IQ_DB_NOT_CONFIGURED
}

/**
 * ★★ 本族 400 只有四条，文案各不相同且**完全可区分**（见 (6)）：
 * `invalid provider_id` / `invalid canonical_id` / `credential_id required` /
 * `raw_model_name required`。
 * ⇒ 报错时能直接说出是哪个参数。
 */
export function modelIQBadRequestKind(
  msg: string,
): 'invalid-provider' | 'invalid-canonical' | 'credential-required' | 'model-required' | 'unknown' {
  const s = msg.trim()
  if (/invalid provider_id/i.test(s)) return 'invalid-provider'
  if (/invalid canonical_id/i.test(s)) return 'invalid-canonical'
  if (/credential_id required/i.test(s)) return 'credential-required'
  if (/raw_model_name required/i.test(s)) return 'model-required'
  return 'unknown'
}

/**
 * ★★ 参数本地预检：能否构造出**后端不会 400** 的一组参数。
 *
 * ★★★ 注意 `/history` 的 `limit` **不在这里**（见 (7)）：非法 limit
 *   **静默回落 50、不报错** ⇒ 它不是「会不会 400」的问题，
 *   页面上要单独说「你填的值可能被回落」（用
 *   `modelIQHistoryEffectiveLimit` 显示实际生效值）。
 */
export function modelIQParamsAreAcceptable(p: {
  credentialId: number
  rawModelName: string
  providerId?: number
  canonicalId?: number
}): boolean {
  if (!(Number.isFinite(p.credentialId) && p.credentialId > 0)) return false
  if (p.rawModelName === '') return false
  if (p.providerId !== undefined && !(Number.isFinite(p.providerId) && p.providerId > 0)) return false
  if (p.canonicalId !== undefined && !(Number.isFinite(p.canonicalId) && p.canonicalId > 0)) return false
  return true
}

// ═══════════════════════════════════════════════════════════════════════════
// (11) 三个端点的排序 —— ★ 都是**可证**的不变式，不只是「提示」
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★★★ 见 (11)：`node-latest` 的 `ORDER BY n.overall_score DESC NULLS LAST, credential_id`。
 *
 * ★★★ 这里的 NULLS LAST 与响应侧的 COALESCE **恰好互相抵消**，所以
 *   **响应里的 `overall_score` 保证非增** —— 这是可证的，不是观察到的：
 *   - `c` 非 NULL：比较发生在原始列上，COALESCE 是恒等映射 ⇒ 顺序不变；
 *   - `c` 为 NULL：显示 0，而 `NULLS LAST` 保证它后面的行**也都**是 NULL
 *     ⇒ 显示 0, 0, …
 *   ⇒ 取值序列恒满足「前 ≥ 后」。有 tiebreak（`credential_id`），
 *     所以并列行的顺序也是确定的。
 */
export function modelIQNodeLatestIsNonIncreasing(rows: ModelIQNodeLatest[]): boolean {
  for (let i = 1; i < rows.length; i++) {
    if (rows[i - 1]!.overall_score < rows[i]!.overall_score) return false
  }
  return true
}

/**
 * ★★★★★ 见 (11)：`catalog` 的 `ORDER BY COALESCE(mc.standard_iq, agg.node_avg) DESC NULLS LAST`。
 *
 * ★★★★ 这里比 node-latest 更强一档：**不可能出现 NULL 键**。
 *   由 (3) 的 WHERE `(standard_iq IS NOT NULL OR node_cnt > 0)` 与
 *   「`node_cnt > 0` ⇒ `node_avg_iq` 非 null」可得：
 *   `standard_iq` 与 `node_avg_iq` **不可能同时为 null**
 *   ⇒ COALESCE 恒有值 ⇒ **整张表严格按这一列非增，没有 NULLS LAST 的余地**。
 *   ⚠️ 排序键是 **COALESCE 后的值**，不是 `standard_iq` 本身 ——
 *     `standard_iq` 为 null 的行是**按 `node_avg_iq` 排的**。
 */
export function modelIQCatalogIsNonIncreasing(rows: ModelIQCatalogRow[]): boolean {
  const key = (r: ModelIQCatalogRow): number => r.standard_iq ?? r.node_avg_iq ?? -Infinity
  for (let i = 1; i < rows.length; i++) {
    if (key(rows[i - 1]!) < key(rows[i]!)) return false
  }
  return true
}

/** ★★★ `history` 的 `ORDER BY tested_at DESC` —— **无 tiebreak** ⇒ 只判非增。 */
export function modelIQHistoryIsNonIncreasing(rows: ModelIQHistoryPoint[]): boolean {
  for (let i = 1; i < rows.length; i++) {
    if (rows[i - 1]!.tested_at < rows[i]!.tested_at) return false
  }
  return true
}
