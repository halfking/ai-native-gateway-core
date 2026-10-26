import { req, type RequestOptions } from './client'

/**
 * opsOverview.ts — 运维总览聚合包（2026-10-08，第八十三批）。
 *
 * GET /api/admin/ops/overview
 *
 * - **注册**：`admin/handler.go:1144` `mux.HandleFunc("/api/admin/ops/overview", h.superAdmin(h.handleOpsOverview))`
 *   ⇒ **superAdmin 档**（`SuperAdminMiddleware` 逐字比 role ⇒ tenant_admin 后端**读不到**，403）。
 * ★★★★★★ 2026-10-21 行号订正 + **档位订正**：这一行原先写 `admin/handler.go:1134`
 *   `mux.HandleFunc("/api/admin/ops/overview", admin(h.handleOpsOverview))`（**漂 7 行**），
 *   且断言「**admin 档** ⇒ tenant_admin 后端能读」——**该结论已不成立**：
 *   批 158e（eb0aee4ec，2026-10-17）把注册处**降级**为 `h.superAdmin(...)`
 *   （Go 侧 `admin/handler.go:1135-1140` 的注释自证：「Downgraded to superAdmin so
 *   tenant_admin cannot reach the endpoint」）。
 *   ⇒ 断言按 Go 现在的事实改写为 **superAdmin 档**（前端那侧本来就设
 *     `requiresRole: 'super_admin'` ⇒ **现在**服务端也有真门，两边一致）。
 *   ⇒ 同族 `views/OpsOverviewView.spec.ts` 已把这条钉成契约（逐字含 `h.superAdmin(...)`、
 *     且逐字**不含** `admin(`）。
 * ★★ 2026-10-14 第一百一十七批订正（历史）：这一行原先写的是 `handler.go:1070`（**漂移 21 行**），
 *   且结论「抽屉席**不设** `requiresRole`」与同文件末尾那句「不按租户隔离」**自相矛盾** ——
 *   不隔离还放行给 tenant_admin，等于把全平台机队拓扑与别人的
 *   `license_key` / `hardware_hash` / `activation_code` 递出去。
 *   ⇒ 抽屉席改设 **`requiresRole: 'super_admin'`**（第三类：服务端完全没有租户维度 ⇒ 过度限制，
 *     与批 115/116 同处置）。当时的**事实**是「注册处仍是 admin 档」——
 *     ★ 该前提**已被批 158e 推翻**（见上），但「前端要更严」的结论**不变**。
 * - ★ `ops` **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes`（`:37-54`）里
 *   ⇒ 尽管桌面把 `/ops` 页面 `externalMaintainRedirect` 到独立 maintain 服务，
 *   **这个 API 仍由本进程的 admin mux 提供**。别被前端的重定向误导。
 * - **实现**：`admin/ops_overview.go`（532 行，handler `:48-76` + payload builder `:78-130`
 *   + **11 个**查询函数 `:132-531`）。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十七件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **十一个子查询并发跑，合进一个 `map[string]any`；失败策略逐个不同。**
 *      `:85-100` 的 `queries` 列表 + `:104-111` 每项一个 goroutine，
 *      `:117-125` 收集时：
 *      ```go
 *      for res := range ch {
 *          if res.err != nil { if firstErr == nil { firstErr = res.err }; continue }
 *          payload[res.key] = res.val
 *      }
 *      if firstErr != nil && len(payload) <= 1 { return nil, firstErr }
 *      ```
 *      ⇒ ★★★ **单个子查询失败 ⇒ 那个键直接缺失**，响应仍是 200。
 *      ⇒ ★★ **全部子查询都失败才 500**（`len(payload) <= 1` 即只剩 `generated_at`）。
 *      ⇒ ⇒ 客户端**无法区分**「这个子查询失败」与「后端版本不含这个子查询」。
 *
 * (2) ★★★★★ **响应的键数在 2..12 之间浮动 ⇒ 不能 requireKeys 全部子查询键。**
 *      最少 2（`generated_at` + 恰好一个成功的子查询），最多 12（1 + 11）。
 *      只有 `generated_at` 恒在（`:115` 在循环之前建的）。
 *
 * (3) ★★★★ `generated_at` 是 `time.Now().UTC().Format(time.RFC3339)` —— **秒级**，没有小数。
 *      `:115`。⇒ ★★★ 可自验：必须以 `Z` 结尾**且小数点后为空**。
 *      （其它端点用 `time.Time` 直接序列化 ⇒ RFC3339**Nano**，有小数部分。）
 *
 * (4) ★★★★ **15 秒内存缓存 + `Cache-Control: private, max-age=15`。**
 *      `:11` `opsOverviewCacheTTL = 15 * time.Second`；`:59-62` 命中时也发这个响应头。
 *      ⇒ ★★ 客户端看到的 `generated_at` 可能比真实采集时刻**早最多 15 秒**。
 *      ⇒ ⇒ 「两次采样相同」**不能**证明数据没变 —— 可能只是命中了缓存。
 *
 * (5) ★★★★★ `region_stats` 里三个 region 是**硬编码**的，缺失时补零值占位并打 `missing: true`。
 *      `:203` `expected := []string{"local", "245", "154"}`。
 *      ⇒ ★★★ **这三行恒存在**，即使一个实例都没有。
 *      ⇒ ★★ 占位行的九个字段全 0 + `missing: true`；真实行**没有** `missing` 键
 *        ⇒ 「`missing` 键存在」就是「这个 region 在库里没有记录」的可验标记。
 *      ⇒ ★★★ **其余 region 追加在后面，而 `for region, item := range byRegion` 是 map 遍历
 *        ⇒ Go 的 map 迭代顺序随机** ⇒ 客户端**绝不能**依赖额外行的顺序。
 *
 * (6) ★★★ `data_plane_tables` 的每一项失败时写 **`-1` 而不是报错**。
 *      `:288-292`：
 *      ```go
 *      if err := h.db.QueryRow(ctx, spec.query).Scan(&count); err != nil { out[spec.key] = -1; continue }
 *      ```
 *      ⇒ ★★★ **`-1` 是「这一项查询失败」的哨兵**，不是计数。
 *      ⇒ ★ 与批 76 的 `row_count = -1`（「未知」哨兵）同款。
 *      ⇒ ★★ 而且这个函数**永远不返回错误**（错误全被吞进 `-1`）
 *        ⇒ **`data_plane_tables` 键恒存在**，是 11 个键里唯一这样的。
 *
 * (7) ★★ `offline_requests` 把**同一个时间戳写了两个键**：`timestamp` 与 `created_at`。
 *      `:328-329` 两者都是 `createdAt`。
 *      ⇒ ★★★ 可自验不变式：**`timestamp === created_at`**（逐行恒成立）。
 *
 * (8) ★★ `offline_requests` 的 `status` 走 `COALESCE(status, 'pending')` ⇒ 恒非 NULL；
 *      而 `approved_at` / `activation_code` 是**条件键**（`:332-337` 有 `!= nil` 守卫）。
 *
 * (9) ★★★ `recent_upgrades` 的 `version` 也走 COALESCE，兜底值是**空串**。
 *      `:400` `COALESCE(NULLIF(new_version,''), NULLIF(old_version,''), '')`
 *      ⇒ 键恒在，**值可能是 `''`**。
 *
 * (10) ★★★★ `recent_faults` 只取 `status = 'new'`（SQL 硬编码，`:363`），
 *        而 `fault_stats` 的 `open_events` 统计的是 **三个** 状态。
 *      `:352-354` `WHERE status IN ('new', 'acknowledged', 'resolving')`
 *      ⇒ ★★★ **跨子查询不变式：`recent_faults.total <= fault_stats.open_events`**，
 *        且 `recent_faults.events` 里每一行的 `status` **必然**是 `'new'` ⇒ **可校验取值**。
 *
 * (11) ★★ 两个「最近列表」的容器键名**不一样**：
 *      `recent_faults` 是 `{"events": …, "total": …}`（`:394`），
 *      `recent_upgrades` 是 `{"items": …, "total": …}`（`:437`）。
 *      ⇒ ★★ 客户端**不能**共用一个「取列表」函数。
 *
 * (12) ★★★★★ **同一个文件里两种 nil 编码并存，而且是「同一个概念的两个字段」：**
 *      | 字段 | 写法 | NULL 的编码 |
 *      |---|---|---|
 *      | `region_stats[].last_heartbeat` | `if lastHeartbeat != nil { item["last_heartbeat"] = *… }`（`:194-196`） | **键缺失** |
 *      | `runtime_metrics_summary[].last_update` | `"last_update": lastUpdate`（`:523`，直接放 `*time.Time`） | **裸 `null`** |
 *      ⇒ ★★★ 这是**第十一种 nil 编码：往 `map[string]any` 里塞 Go 指针且不判空 ⇒ JSON `null`**。
 *      ⇒ ⇒ 判据必须分开写：`'last_heartbeat' in stat` 与 `row.last_update === null`。
 *
 * (13) ★★★ `runtime_metrics_summary` 的 WHERE 是 `gi.status != 'offline' OR m.last_update IS NOT NULL`
 *      （`:488`）⇒ ★★ **离线实例只要 24 小时内有指标就会出现在榜上**。
 *      ⇒ ⇒ 客户端**不能**用「出现在榜上」推断「在线」。
 *
 * (14) ★★★ 四个 AVG 全被 `COALESCE(…, 0)` 包住（`:481-484`）。
 *      ⇒ ★★ `avg_cpu_pct === 0` 是**二义的**：真的 0%，或者「24 小时内没有任何指标」。
 *      ⇒ ⇒ 判据要能表达这个二义，别把 0 当「真的没用 CPU」。
 *
 * (15) ★★ 四个数组型子查询都有 `if items == nil { items = …{} }` ⇒ **恒数组**。
 *      `deployment_nodes` `:266-268`、`offline_requests` `:343-345`、
 *      `recent_faults` `:391-393`（`events`）、`recent_upgrades` `:434-436`、
 *      `runtime_metrics_summary` `:527-529`。
 *
 * (16) ★★ **405 检查排在 503 之前** ⇒ 「非 GET **且** db 未配置」时拿到 **405 而不是 503**。
 *      `:49-52`（方法）在 `:53-56`（db）之前。
 *      ⇒ ★★ 与第八十二批 `format-anomalies` **正好相反**（那里 503 在 405 之前）。
 *      ⇒ ⇒ 这就是「错误码表必须逐端点列」的直接证据。
 *
 * (17) ★ 500 是 `writeInternalErr(w, "internal error (see server logs)", err)`
 *      ⇒ 固定文案、**不含 `err.Error()`**（`internal_error.go` 头注释的约定）。
 *
 * ★ 注意本端点**不按租户隔离**：11 个查询里没有任何租户条件
 *   （`gateway_instances` / `licenses` / `fault_events` / `upgrade_logs` / `download_events`
 *   都是全局表）⇒ admin 档的 tenant_admin 看到的是**全平台运维数据**。
 *
 * ★★★★ 更具体地说，`offline_requests` 每一行带 `license_key` / `hardware_hash` /
 *   `activation_code`（`:299-337`）—— 那是**别的租户的**凭据与硬件指纹；
 *   `deployment_nodes` / `runtime_metrics_summary` 是**全平台机队**的 IP、版本与用量。
 *   ⇒ 第一百一十七批把抽屉席钉成 `super_admin`。⚠️ 前端严于后端是**兜底不是修复**：
 *   真门在服务端，而这一族服务端**没有**门（`AdminMiddleware` 不检 role，handler 也不检）。
 *
 * ★★ 第(6) 条还有个可证推论（第一百一十七批补判据）：`queryOpsDataPlaneTables` 把错误
 *   吞成 `-1` 而**从不返回 error** ⇒ 它**是 11 个键里唯一恒存在**的
 *   ⇒ `data_plane_tables` 一旦**缺键**，只有一种解释：这个子查询整体没跑。
 *   而那件事后端保证不了（键可能因并发收集竞态而不落）⇒ 客户端必须能查，见
 *   `opsDataPlaneKeysAreComplete`。
 */

// ── 常量 ─────────────────────────────────────────────────────────────────────

/** `:11` `opsOverviewCacheTTL = 15 * time.Second`；响应头也是 `max-age=15`。见 (4)。 */
export const OPS_OVERVIEW_CACHE_TTL_SECONDS = 15

/** `:115` 唯一恒在的键。 */
export const OPS_OVERVIEW_ALWAYS_KEY = 'generated_at'

/** `:85-100` 十一个子查询键，按源码顺序。 */
export const OPS_OVERVIEW_SECTION_KEYS = [
  'center_stats',
  'region_stats',
  'deployment_nodes',
  'data_plane_tables',
  'license_total',
  'offline_requests',
  'fault_stats',
  'recent_faults',
  'recent_upgrades',
  'download_stats',
  'runtime_metrics_summary',
] as const

export type OpsOverviewSectionKey = (typeof OPS_OVERVIEW_SECTION_KEYS)[number]

/** `:203` 三个硬编码 region ⇒ 恒有这三行。见 (5)。 */
export const OPS_EXPECTED_REGIONS = ['local', '245', '154'] as const

/** `:289` 计数失败时的哨兵值。见 (6)。 */
export const OPS_DATA_PLANE_UNAVAILABLE = -1

/** `:277-284` `data_plane_tables` 的六个计数项。 */
export const OPS_DATA_PLANE_KEYS = [
  'gateway_instances',
  'instance_heartbeats',
  'download_events',
  'offline_activation_requests',
  'license_devices',
  'licenses',
] as const

/** `:352-354` `fault_stats` 统计的三个状态（`recent_faults` 只取其中第一个）。见 (10)。 */
export const OPS_FAULT_OPEN_STATUSES = ['new', 'acknowledged', 'resolving'] as const

/** `:363` `recent_faults` 的 SQL 硬编码过滤 ⇒ 每行 `status` 必然是这个值。 */
export const OPS_RECENT_FAULT_STATUS = 'new'

/** `:300` `COALESCE(status, 'pending')` 的兜底值。 */
export const OPS_OFFLINE_STATUS_FALLBACK = 'pending'

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface OpsCenterStats {
  total_instances: number
  online_instances: number
  offline_instances: number
  degraded_instances: number
}

/**
 * `region_stats` 的一行。
 *
 * ★ 五个计数字段恒在；`last_heartbeat` 是**键缺失**（有 `!= nil` 守卫）；`missing` 是**键缺失**（真实行不打它）。
 */
export interface OpsRegionStat {
  region: string
  total_instances: number
  online_instances: number
  offline_instances: number
  degraded_instances: number
  /** ★ 键缺失 ⇔ 该 region 库里有记录。NULL 心跳也是键缺失（两者不可区分）。见 (12)。 */
  last_heartbeat?: string
  /** ★ **恒为 `true`**（`:217`），且**只在占位行上出现**。见 (5)。 */
  missing?: true
}

export interface OpsDeploymentNode {
  instance_id: string
  hostname: string
  ip_address: string
  region: string
  version: string
  build_seq: number
  status: string
  started_at: string
  last_heartbeat: string
}

/** `data_plane_tables` 的一行：值可能是 `-1`（查询失败哨兵）。见 (6)。 */
export type OpsDataPlaneCount = Record<(typeof OPS_DATA_PLANE_KEYS)[number], number>

export interface OpsOfflineRequest {
  license_key: string
  hardware_hash: string
  instance_id: string
  device_name: string
  request_id: string
  /** ★★ 与 `created_at` **必然相等**（`:328-329` 同一个值）。见 (7)。 */
  timestamp: string
  created_at: string
  /** ★ `COALESCE(status,'pending')` ⇒ 恒非 NULL。 */
  status: string
  /** ★ 条件键。 */
  approved_at?: string
  /** ★ 条件键。 */
  activation_code?: string
}

export interface OpsFaultStats {
  open_events: number
}

export interface OpsRecentFault {
  id: number
  rule_id: number
  rule_name: string
  severity: string
  /** ★ SQL 硬编码 ⇒ **必然**是 `'new'`。见 (10)。 */
  status: string
  title: string
  description: string
  source: string
  detected_at: string
}

/** ★★ 容器键是 **`events`**（不是 `items`）。见 (11)。 */
export interface OpsRecentFaultsSection {
  events: OpsRecentFault[]
  total: number
}

export interface OpsRecentUpgrade {
  instance_id: string
  status: string
  /** ★ COALESCE 兜底是**空串**，所以值可能是 `''`。见 (9)。 */
  version: string
  started_at: string
  /** ★ 条件键（进行中的升级没有完成时间）。 */
  completed_at?: string
}

/** ★ 容器键是 **`items`**（不是 `events`）。见 (11)。 */
export interface OpsRecentUpgradesSection {
  items: OpsRecentUpgrade[]
  total: number
}

export interface OpsDownloadStats {
  today_downloads: number
  week_downloads: number
  total_downloads: number
  supporter_count: number
}

/**
 * `runtime_metrics_summary` 的一行。
 *
 * ★★ `last_update` 可能是**裸 `null`**（直接塞 Go 指针，没有判空守卫）⇒ 与 `region_stats`
 *   的 `last_heartbeat`（键缺失）是两种编码。见 (12)。
 */
export interface OpsRuntimeMetric {
  instance_id: string
  hostname: string
  region: string
  version: string
  /** ★ 可能是 `'offline'`（离线实例只要有 24h 指标就在榜上）。见 (13)。 */
  status: string
  /** ★ `0` 是**二义**的：真 0 或「无指标」。见 (14)。 */
  avg_cpu_pct: number
  /** ★ 同上。 */
  avg_mem_pct: number
  /** ★ 同上。 */
  avg_tps: number
  max_p99_ms: number
  /** ★ 可能是 `null`。 */
  last_update: string | null
}

/**
 * 运维总览的响应：**开放形状**。
 *
 * ★★ 只有 `generated_at` 恒在；十一个子查询键各自可能出现或缺失。见 (2)。
 */
export interface OpsOverviewResponse {
  /** ★ `time.RFC3339`（**秒级、无小数**）的 UTC 字符串。见 (3)。 */
  generated_at: string
  center_stats?: OpsCenterStats
  region_stats?: OpsRegionStat[]
  deployment_nodes?: OpsDeploymentNode[]
  data_plane_tables?: OpsDataPlaneCount
  license_total?: number
  offline_requests?: OpsOfflineRequest[]
  fault_stats?: OpsFaultStats
  recent_faults?: OpsRecentFaultsSection
  recent_upgrades?: OpsRecentUpgradesSection
  download_stats?: OpsDownloadStats
  runtime_metrics_summary?: OpsRuntimeMetric[]
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/ops/overview`（`admin/handler.go:1144`，**superAdmin 档** ⇒ 后端 tenant_admin 读不到）。无参数。 */
export function fetchOpsOverview(options?: RequestOptions): Promise<OpsOverviewResponse> {
  return req<unknown>('GET', '/api/admin/ops/overview', undefined, options).then(unwrapOpsOverview)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

/**
 * 校验形状。
 *
 * ★ 与别处相反：这里**只 require 一个键**（`generated_at`）。
 *   十一个子查询键**故意不 require** —— 缺键是合法形状（该子查询失败）。见 (1)(2)。
 *   ⇒ 但**存在**的键仍要逐个校验类型，否则形状不符的数据会流进 UI。
 */
export function unwrapOpsOverview(resp: unknown): OpsOverviewResponse {
  const d = requireObject(resp, '运维总览')
  if (!(OPS_OVERVIEW_ALWAYS_KEY in d)) {
    throw new Error(`运维总览 缺 1 个键（${OPS_OVERVIEW_ALWAYS_KEY}）`)
  }
  if (typeof d[OPS_OVERVIEW_ALWAYS_KEY] !== 'string') {
    throw new Error(`运维总览 的 ${OPS_OVERVIEW_ALWAYS_KEY} 不是字符串`)
  }
  if ('center_stats' in d) requireCenterStats(d['center_stats'])
  if ('region_stats' in d) {
    const arr = requireArray(d['region_stats'], 'region_stats')
    arr.forEach((r, i) => requireRegionStat(r, `region_stats[${i}]`))
  }
  if ('deployment_nodes' in d) requireArray(d['deployment_nodes'], 'deployment_nodes')
  if ('data_plane_tables' in d) requireDataPlaneTables(d['data_plane_tables'])
  if ('license_total' in d && typeof d['license_total'] !== 'number') {
    throw new Error('运维总览 的 license_total 不是数字')
  }
  if ('offline_requests' in d) requireArray(d['offline_requests'], 'offline_requests')
  if ('fault_stats' in d) requireObject(d['fault_stats'], '运维总览 的 fault_stats')
  if ('recent_faults' in d) requireListSection(d['recent_faults'], 'events', 'recent_faults')
  if ('recent_upgrades' in d) requireListSection(d['recent_upgrades'], 'items', 'recent_upgrades')
  if ('download_stats' in d) requireObject(d['download_stats'], '运维总览 的 download_stats')
  if ('runtime_metrics_summary' in d) requireArray(d['runtime_metrics_summary'], 'runtime_metrics_summary')
  return d as unknown as OpsOverviewResponse
}

function requireCenterStats(v: unknown): void {
  const d = requireObject(v, '运维总览 的 center_stats')
  for (const k of [
    'total_instances',
    'online_instances',
    'offline_instances',
    'degraded_instances',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`运维总览 的 center_stats 的 ${k} 不是数字`)
  }
}

function requireRegionStat(v: unknown, where: string): void {
  const d = requireObject(v, where)
  for (const k of [
    'region',
    'total_instances',
    'online_instances',
    'offline_instances',
    'degraded_instances',
  ] as const) {
    if (typeof d[k] !== 'string' && typeof d[k] !== 'number') {
      throw new Error(`${where} 的 ${k} 不是字符串或数字`)
    }
  }
  if (typeof d['region'] !== 'string') throw new Error(`${where} 的 region 不是字符串`)
  for (const k of [
    'total_instances',
    'online_instances',
    'offline_instances',
    'degraded_instances',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  // ★ `last_heartbeat` 与 `missing` 都是条件键；存在时类型必须对。
  if ('last_heartbeat' in d && typeof d['last_heartbeat'] !== 'string') {
    throw new Error(`${where} 的 last_heartbeat 不是字符串`)
  }
  if ('missing' in d && d['missing'] !== true) throw new Error(`${where} 的 missing 不是 true`)
}

function requireDataPlaneTables(v: unknown): void {
  const d = requireObject(v, '运维总览 的 data_plane_tables')
  for (const [k, val] of Object.entries(d)) {
    if (typeof val !== 'number') throw new Error(`运维总览 的 data_plane_tables 的 ${k} 不是数字`)
  }
}

function requireListSection(v: unknown, listKey: string, where: string): void {
  const d = requireObject(v, `运维总览 的 ${where}`)
  if (listKey in d) requireArray(d[listKey], `${where} 的 ${listKey}`)
  if (typeof d['total'] !== 'number') throw new Error(`运维总览 的 ${where} 的 total 不是数字`)
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return resp
}

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`运维总览 的 ${where} 不是数组`)
  return v
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

/** 见 (2)：某子查询是否成功（键存在 ⇔ 成功）。 */
export function opsHasSection(r: OpsOverviewResponse, key: OpsOverviewSectionKey): boolean {
  return key in r
}

/** 见 (1)：缺失的子查询键清单（**无法区分**「失败」与「后端没这个子查询」）。 */
export function opsMissingSectionKeys(r: OpsOverviewResponse): OpsOverviewSectionKey[] {
  return OPS_OVERVIEW_SECTION_KEYS.filter((k) => !(k in r))
}

/** 见 (1)：所有子查询都失败（那种情况后端会 500，所以 200 里不该出现）。 */
export function opsIsFullyDegraded(r: OpsOverviewResponse): boolean {
  return OPS_OVERVIEW_SECTION_KEYS.every((k) => !(k in r))
}

/** 见 (2)：成功的子查询个数（0..11）。 */
export function opsSectionCount(r: OpsOverviewResponse): number {
  return OPS_OVERVIEW_SECTION_KEYS.filter((k) => k in r).length
}

/**
 * 见 (3)：`generated_at` 是 `time.RFC3339` ⇒ **秒级精度、没有小数部分**。
 *
 * ★★ 这是与其它端点最直观的差别（那里 `time.Time` 直序列化 ⇒ RFC3339**Nano**，有小数）。
 */
export function opsGeneratedAtIsSecondPrecision(iso: string): boolean {
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(iso)
}

/** 见 (5)：某个硬编码 region 是否在列表里（**恒为 true**）。 */
export function opsRegionIsAlwaysPresent(
  regions: OpsRegionStat[],
  region: (typeof OPS_EXPECTED_REGIONS)[number],
): boolean {
  return regions.some((r) => r.region === region)
}

/** 见 (5)：该行是不是「库里没有这个 region」的占位行。 */
export function opsRegionIsPlaceholder(row: OpsRegionStat): boolean {
  return row.missing === true
}

/**
 * 见 (5)：占位行恒为「四个计数全 0」。
 * ★ 判据写成 `total === 0` 就错了 —— 真实 region 也可能真的是 0 台。
 *   ⇒ 必须靠 `missing` 键来判，且这条同时验证「有 missing ⇒ 全 0」。
 */
export function opsRegionPlaceholderIsAllZero(row: OpsRegionStat): boolean {
  return (
    row.total_instances === 0 &&
    row.online_instances === 0 &&
    row.offline_instances === 0 &&
    row.degraded_instances === 0
  )
}

/** 见 (5)：**前 12 行里必然包含全部三个硬编码 region**（顺序也固定）。 */
export function opsRegionPreambleCoversExpected(regions: OpsRegionStat[]): boolean {
  const head = regions.slice(0, OPS_EXPECTED_REGIONS.length)
  return OPS_EXPECTED_REGIONS.every((r, i) => head[i]?.region === r)
}

/** 见 (5)：额外行排在硬编码三行之后（顺序由 map 迭代决定，**不确定**）。 */
export function opsRegionExtraRowsFollowPreamble(regions: OpsRegionStat[]): boolean {
  const expected = new Set<string>(OPS_EXPECTED_REGIONS)
  const tail = regions.slice(OPS_EXPECTED_REGIONS.length)
  return tail.every((r) => !expected.has(r.region))
}

/** 见 (6)：该计数是不是「查询失败」哨兵。 */
export function opsDataPlaneCountIsUnavailable(count: number): boolean {
  return count === OPS_DATA_PLANE_UNAVAILABLE
}

/** 见 (6)：该计数是真实计数（`>= 0`）。 */
export function opsDataPlaneCountIsAvailable(count: number): boolean {
  return count >= 0
}

/** 见 (7)：`timestamp` 与 `created_at` 必然相等（同源值写了两遍）。 */
export function opsOfflineTimestampMirrorsCreatedAt(row: OpsOfflineRequest): boolean {
  return row.timestamp === row.created_at
}

/** 见 (8)：该行是否已审批（`approved_at` 键在）。 */
export function opsOfflineIsApproved(row: OpsOfflineRequest): boolean {
  return 'approved_at' in row
}

/** 见 (10)：该故障事件是否处于 `new`（本子查询里**恒为真**）。 */
export function opsFaultStatusIsNew(row: OpsRecentFault): boolean {
  return row.status === OPS_RECENT_FAULT_STATUS
}

/**
 * 见 (10)：跨子查询不变式 —— `recent_faults` 是 `fault_stats` 的子集。
 *
 * ★ 它统计的是 `status IN ('new','acknowledged','resolving')` 三个状态，
 *   而 `recent_faults` 只取 `status = 'new'` ⇒ 取数**必然更小**。
 */
export function opsRecentFaultsWithinOpenEvents(r: OpsOverviewResponse): boolean {
  if (r.recent_faults === undefined || r.fault_stats === undefined) return false
  return r.recent_faults.total <= r.fault_stats.open_events
}

/** 见 (10)：`recent_faults` 的每一行都必须是 `new`。 */
export function opsRecentFaultsAreAllNew(r: OpsOverviewResponse): boolean {
  if (r.recent_faults === undefined) return false
  return r.recent_faults.events.every((e) => e.status === OPS_RECENT_FAULT_STATUS)
}

/** 见 (11)：两个列表容器的键名不同（`events` vs `items`）。 */
export function opsFaultSectionListKey(): string {
  return 'events'
}
export function opsUpgradeSectionListKey(): string {
  return 'items'
}

/**
 * 见 (12)：`runtime_metrics_summary[].last_update` 可能是**裸 `null`**。
 *
 * ★★ 与 `region_stats[].last_heartbeat` 的**键缺失**是两回事。
 */
export function opsRuntimeLastUpdateIsNull(row: OpsRuntimeMetric): boolean {
  return row.last_update === null
}

/** 见 (12)：`region_stats[].last_heartbeat` 缺失时的形态（NULL 心跳 ⇒ 键缺）。 */
export function opsRegionLastHeartbeatIsAbsent(row: OpsRegionStat): boolean {
  return !('last_heartbeat' in row)
}

/** 见 (13)：榜上出现**不等于**在线（离线实例有 24h 指标时也在榜）。 */
export function opsRuntimeRowMayBeOffline(row: OpsRuntimeMetric): boolean {
  return row.status === 'offline'
}

/**
 * 见 (14)：`0` 是**二义**的 —— 真的 0，或者「24 小时内没有任何指标」。
 *
 * ★★ 所以这个判据的语义只能是「**可能**是二义的」，
 *   UI 要区分「真的 0」与「没数据」必须另找 `last_update`。
 */
export function opsRuntimeZeroIsAmbiguous(row: OpsRuntimeMetric): boolean {
  return row.avg_cpu_pct === 0 && row.avg_mem_pct === 0 && row.avg_tps === 0
}

/** 见 (14)：有指标（`last_update` 非 null）时，`0` 才可能是真实值。 */
export function opsRuntimeHasMetrics(row: OpsRuntimeMetric): boolean {
  return row.last_update !== null
}
// ═══════════════════════════════════════════════════════════════════════════
// 第一百一十七批新增：权限判定 + 四条可证不变式
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★★★★ 权限档（第一百一十七批逐字核过四处）：
 *   ① 注册处 `admin/handler.go:1099` 传的是 `admin(...)` = `h.admin`（`handler.go:901`）
 *      → `AdminMiddleware`（`admin/auth.go:85-140`）—— **只认证，不检 role**；
 *   ② 该中间件全文**没有** `claims.Role` 比较（对比 `SuperAdminMiddleware` `auth.go:376-409`）；
 *   ③ `admin/ops_overview.go` 全文**没有** `AuthContext` / `IsTenantAdmin` / `Role`；
 *   ④ **取租户键的代码根本不存在** —— 11 条子查询全是全局表，`grep tenant` 打不到不是遗漏，
 *      是**设计上就没有**。
 * ⇒ 归第三类：服务端**完全没有租户维度** ⇒ 过度限制到 `super_admin`。
 * ⚠️ 与批 113/114 那两席（`requiresRole: 'admin'`）成因相反：那一族服务端**有**租户维度，
 *   只是返回键可能为空串。
 */
export function opsOverviewRoleIsAllowed(role: string): boolean {
  return role === 'super_admin'
}

/** ★★ 503：`h.db == nil` 时三个分支共用 `database not configured`（`ops_overview.go:54`）。 */
export function opsOverviewDbNotConfiguredMessage(msg: string): boolean {
  return msg.trim() === 'database not configured'
}

/**
 * ★★★★★ 见 (6) 的推论：`queryOpsDataPlaneTables`（`:272-295`）把每个查询的错误
 *   **吞成 `-1`** 而 `return out, nil` ⇒ 它**从不返回 error** ⇒ 该键**恒存在**且**六项齐全**。
 *   ⇒ 一旦缺键 ⇒ 响应不是这个端点发的（或是并发收集竞态漏掉了整个键）。
 */
export function opsDataPlaneKeysAreComplete(d: OpsDataPlaneCount): boolean {
  return OPS_DATA_PLANE_KEYS.every((k) => k in d)
}

/**
 * ★★★★ 见 (2)：键数恒在 **2..12**（1 个恒在键 + 0..11 个成功的子查询）。
 *   ⇒ 越界只有一种解释：响应不是这个端点发的。
 */
export function opsResponseKeyCountIsInRange(r: OpsOverviewResponse): boolean {
  const n = Object.keys(r).length
  return n >= 2 && n <= OPS_OVERVIEW_SECTION_KEYS.length + 1
}

/**
 * ★★★★ `center_stats`（`:132-151`）四个数是同一张表的四个 `COUNT`，
 *   其中三个是 `COUNT(*) FILTER (WHERE status = …)` ⇒ **三值互斥且互斥于其它状态**
 *   ⇒ 它们的和**必然 ≤ `total_instances`**。
 *   ⚠️ 注意 `center_stats` **没有** `missing`/`unknown` 键可对账，
 *   所以这是单向判据（和 ≤ total 成立，和 < total 也合法 ⇒ 有第四种状态）。
 */
export function opsCenterStatusCountsFitTotal(c: OpsCenterStats): boolean {
  return c.online_instances + c.offline_instances + c.degraded_instances <= c.total_instances
}

/**
 * ★★★★ 两个「最近列表」的 `total` 都是 `len(容器)`（`:394` / `:437` 同一次构造）
 *   ⇒ 与 `recent_faults.total <= fault_stats.open_events`（(10)）**方向相反**：
 *   那条是跨子查询的**上界**，这条是**容器内的自洽**。
 *   ⇒ 两个列表键名不同（(11)），所以一条判据要同时钉住**两个**容器名。
 *
 * ★★★★ **缺键的语义（本批第一版写错、被判据当场顶红的那条）**：
 *   本函数首版注释写「键缺失时返回 `false`（不是 `true`）：没取到不算自洽」，
 *   而实现是**逐个存在才校验** ⇒ 两个容器都缺时返回 `true` ⇒ **注释与代码直接矛盾**。
 *   ⇒ 结论是**代码对、注释错**：两种语义各有各的用处，取决于**谁调它**——
 *     · 给 UI 当红线开关用 ⇒ 缺键**不能**报红（那个子查询只是失败了，容器压根不存在），
 *       否则一份 `{generated_at, center_stats}` 的正常响应会被标成「不自洽」；
 *     · 给判据当不变式用 ⇒ 缺键**不构成违规**（空真），返回 `true` 才对。
 *   ⇒ 所以拆成两条：**本函数**（无违规 = true，UI 直接拿它当红线开关）+
 *     `opsListSectionMismatchKinds`（**具体是哪一段**对不上，UI 要报给用户看）。
 *   ⚠️ 反面纪律：别用「否定式布尔」同时表达「没取到」与「取到了但不对」两种状态。
 */
export function opsListSectionTotalsMatchLengths(r: OpsOverviewResponse): boolean {
  return opsListSectionMismatchKinds(r).length === 0
}

/**
 * ★★★ 返回**存在且 `total` 与容器长度不符**的段名（`:394` 的 `events` / `:437` 的 `items`）。
 *   段不存在则**不进清单**（那是「没取到」，不是「不对」）。
 *   ⚠️ 返回段名而不只是布尔：只报布尔的话，用户看不出是哪一段的计数对不上。
 */
export function opsListSectionMismatchKinds(r: OpsOverviewResponse): Array<'recent_faults' | 'recent_upgrades'> {
  const bad: Array<'recent_faults' | 'recent_upgrades'> = []
  const faults = r.recent_faults
  if (faults !== undefined && faults.total !== faults.events.length) bad.push('recent_faults')
  const ups = r.recent_upgrades
  if (ups !== undefined && ups.total !== ups.items.length) bad.push('recent_upgrades')
  return bad
}
