import { req, type RequestOptions } from './client'

/**
 * modelIQ.ts — 节点智商（模型 IQ）三端点（2026-10-08，第八十五批）。
 *
 * GET /api/admin/model-iq/node-latest
 * GET /api/admin/model-iq/history
 * GET /api/admin/model-iq/catalog
 *
 * - **注册**：`admin/handler.go:1470-1472`，三个都是 `h.superAdmin(...)`
 *   ⇒ ★★ **superAdmin 档** ⇒ 抽屉席须设 `requiresRole: 'super_admin'`
 *     并同步 `AppDrawer.spec.ts` 白名单。
 *   - **不在** `maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *   - `trigger` 是 **POST**（有外部副作用、要真花 token）⇒ **不碰**。
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
 * ★★ **本模块明确声明的校验边界**：解包器校验三个端点的**顶层是数组** +
 *   每项的**全部恒在键与类型**（13 / 9 / 10 键）+ 上述可自验不变式的**判据函数**。
 *   ⇒ 不校验排序（排序依赖 DB 状态，客户端只作为提示）。
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
