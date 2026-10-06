import { req, type RequestOptions } from './client'

/**
 * usageTrendSeries.ts — 用量趋势线两条**只读**端点（2026-10-08，第七十一批）。
 *
 * GET /api/admin/usage/trend-series
 * GET /api/admin/usage/trend-models
 *
 * 注册在 `admin/handler.go:1267` 的 `h.admin(h.HandleUsageAdmin)` 前缀子路由上，
 * 分派见 `admin/usage.go:60-64` 两个 case ⇒ **admin 档**，tenant_admin 可用
 * ⇒ 抽屉席**不设** `requiresRole`。
 * （不在 `cmd/gateway/maintain_proxy.go:37` 的 `maintainCompatPrefixes` 里
 *   ⇒ 本进程提供，不经 maintain 反代。）
 *
 * ## ★★★★★ 本族最要紧的八件事
 *
 * (1) ★★★★★ **`degraded` 也是恒发字段（不带 omitempty）——与第七十批同款写法。**
 *      `usage_trend_series.go:75-80` / `:96-101`：
 *      ```go
 *      // 恒发（无 omitempty）：空序列与「真的没有用量」在图上同形。
 *      // 全新安装没迁移 rollup 时返回的是降级，不是零用量。
 *      Degraded       bool   `json:"degraded"`                 // 恒发
 *      DegradedReason string `json:"degraded_reason,omitempty"` // 仅降级
 *      ```
 *      `dashboard_degrade.go:105` 的注释写明「与 `PeriodCompareResponse.Degraded` 同理」
 *      ⇒ **这是本仓既定风格，不是偶然**（第二次遇到：第七十批 usageEnhanced）。
 *      ★ 压缩统计（第六十八批）的 `*int64` + omitempty 族恰好相反
 *        ⇒ **不能照抄别族的「键缺失即异常」判据**。
 *
 * (2) ★★★★★ **降级时 `top` 与 `bucket_minutes` 仍是解析后的值，不是零值**（`:204-205`）。
 *      降级分支照样填了 `BucketMinutes: tr.trendBucketMinutes()` 与 `Top: f.top`
 *      ⇒ **「降级 + 空序列」与「成功 + 零用量」的区分只能靠 `degraded` 那一个键**。
 *
 * (3) ★★★★★ **`days` 的 clamp 是 90，不是 366；且 days=7 不是完整 7 天。**
 *      本族时间窗走 `boardTimeRangeFromRequest`（`admin/board_time_range.go:17-38`），
 *      `start`/`end` 都缺省时用的是 `boardDays(r)`（`dashboard_board.go:204-213`）：
 *      默认 1，clamp **[1,90]**。⇒ 第七十批 usageEnhanced 的 366 在本族**不适用**。
 *      起点 `boardPresetTimeRange`（`board_time_range.go:120`）是
 *      `todayStart.Add(-(days-1)*24h)` ⇒ **减 days-1 天** ⇒ days=7 起点是**六天前**零点。
 *      ⚠️ 对照 `resolveUsageTimeRange` 的 days 分支减的是 `days` 天（`usage.go:1447`）
 *        —— **两套 days 语义差一天**。不过本族那条分支**不可达**
 *        （`resolveUsageTimeRange` 只在 `start`/`end` 至少给一个时被调用，
 *          而它内部 days 分支的前提正是两者都缺省）⇒ 死代码，仅作对照。
 *
 * (4) ★★★★★ **自定义区间是左闭右开，响应 `end` 是「次日零点」而不是用户选的那天。**
 *      `usage.go:1467-1469`：
 *      ```go
 *      // [start, end) — end is exclusive so the picker-selected end date is fully included.
 *      return startDay.UTC(), endDay.Add(24 * time.Hour).UTC(), nil
 *      ```
 *      ⇒ 用户选 2026-01-01 ~ 2026-01-07，响应 `end` 是 `2026-01-08T00:00:00Z`
 *      ⇒ **UI 不能把 `end` 直接显示成「用户选的结束日」**。
 *      对照 days 预设路径的 `End = now`（带时分秒）⇒ **两条路径 `end` 形态不同**。
 *
 * (5) ★★★★ **分桶两套规则，同一个实际跨度可能是两个档位。**
 *      `trendBucketMinutes`（`board_time_range.go:47-67`）：
 *      - 预设档（`Custom=false`，按 `Days`）：`<=1` → 5；`<=7` → 15；否则 60
 *      - 自定义档（`Custom=true`，按实际 `span`）：`<=48h` → 5；`<=14d` → 15；否则 60
 *      ⇒ `start`/`end` 恰好 24 小时跨度 ⇒ span=24h ≤ 48h ⇒ **5 分钟**；
 *        而 days=2 预设 ⇒ Days=2 ⇒ **15 分钟**。**同样跨度，不同 `bucket_minutes`。**
 *
 * (6) ★★★★ **非法 / 负数 ID 会静默换数据档，不报 400。**
 *      `queryInt`（`handler.go:1539-1542`）解析失败**回落默认值**；`usageTrendSource`
 *      （`:155-164`）判的是 `f.providerID > 0` / `f.apiKeyID > 0`
 *      ⇒ `provider_id=abc`、`provider_id=-5` 都变成 0 ⇒ 落到 **default 档（dim）**。
 *      ⇒ 用户以为加了 provider 过滤，实际拿到的是**另一张表**的数据，响应里看不出异常
 *      （只有 `source` 变了，而 `source` 只是提示不是错误）。
 *
 * (7) ★★★★ **`source` 字符串不能反推真实表名。**
 *      `usageTrendSource:158` 对外返回 `"request_logs_with_current_month"`，
 *      而真实读的是 `request_logs_with_current_month_without_customer_id`（`:407`/`:588`）
 *      ⇒ **对外少了 `_without_customer_id` 后缀**。
 *
 * (8) ★★★ **三个不同的上限，后两个都不回显。**
 *      - `top`：默认 8、clamp **[1,20]**（`:129`/`:144-149`）⇒ **响应回显 clamp 后的值**
 *      - `model` 多选：空值/重复剔除，超过 **20** 截断（`:133-143`，`usageTrendMaxModels`）
 *      - `trend-models` 的 SQL **硬编码 `LIMIT 100`**（`:524`/`:554`/`:592`）
 *        ⇒ 超 100 个模型**静默截断**，响应里**没有任何字段说明被截断了**
 *
 * ## 另注
 *
 * · `__others__` **固定排最后**（`:221-229`），其余按 `TotalRequests` 降序；
 *   用的是 `sort.SliceStable` ⇒ **TotalRequests 相同时保持原序**
 *   （原序 = SQL `ORDER BY 2, 1` ⇒ bucket 升序下的模型名字典序）
 *   ⇒ 同请求数模型之间的顺序是数据决定的。
 * · 折叠只在「没指定 model」时发生：`foldUsageTrendRows:439` 的
 *   `modelFiltered || len(rows)==0` 直接透传 ⇒ **指定了 model 就不折叠**；
 *   且 `:455-457` 的 `len(ranked) <= top` 也透传 ⇒ **恰好 top 条不折叠**
 *   ⇒ 判「是否折叠」必须用 `模型数 > top`，不是 `>= top`。
 * · detail 档（带 `api_key_id`）的 `request_status` 是**三态白名单**
 *   `IN ('success','failure','rate_limited')`（`:383`/`:565`）⇒ 不是只看成功。
 *   对照压缩统计（第六十八批）的 `($3 OR rl.success)` 又是另一种口径。
 * · 降级时 `DegradedReason` 与 `MissingView` 是**同一个值**（`:209-210`）⇒ 两键恒等。
 * · `hint` 可本地推导（`dashboard_degrade.go:83-88`），不必信任后端文案。
 * · 租户：`statsTenantScope`（`stats.go:87-97`）—— 角色不是 `super_admin`/`admin_key`
 *   就用 `auth.TenantID` 并**忽略 `tenant_id` 参数** ⇒ tenant_admin 天然隔离。
 *   ★ 与 data-lifecycle 的 `metrics` 端点（**不隔离但注册是 admin 档**）**恰好相反**
 *     ⇒ 本族是本仓**第四种**租户口径。
 * · 超时 45 秒（`:182`/`:260`），detail 档长窗可能超时
 *   （文件头注释：前端对超时给出「缩短时间范围」提示）。
 * · ⚠️ 桌面的两处写法**不要抄**：
 *   - `web/src/api/usage.ts:456`/`:474` 把 `degraded` 声明成 `degraded?: boolean`
 *     ⇒ 可选，暗示可能缺键，但后端恒发 ⇒ 桌面「缺键即降级」恒假
 *   - `BoardUsageTrendSection.vue:71` `resp.bucket_minutes || …` /
 *     `UsageTrendExplorer.vue:167` `resp.bucket_minutes || 60`
 *     ⇒ 后端恒发 int ⇒ **兜底恒不生效**，且 60 不是唯一合法档位（还有 5/15）
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 公共：常量（全部逐字来自后端源码，**不要**从别处 import）
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/dashboard_board.go:205 `queryInt(r, "days", 1)` 的缺省。 */
export const TREND_DAYS_DEFAULT = 1
/** admin/dashboard_board.go:206-208 `if days < 1 { days = 1 }`。 */
export const TREND_DAYS_MIN = 1
/**
 * ★ admin/dashboard_board.go:209-211 `if days > 90 { days = 90 }`。
 * ★ **不是** usageEnhanced 的 366 —— 那是 `resolveUsageTimeRange` 的 clamp，
 *   本族走的是 `boardDays`，两者不是同一个函数。
 */
export const TREND_DAYS_MAX = 90

/** admin/usage_trend_series.go:129 `queryInt(r, "top", 8)` 的缺省。 */
export const TREND_TOP_DEFAULT = 8
/** admin/usage_trend_series.go:144-146 `if f.top < 1 { f.top = 1 }`。 */
export const TREND_TOP_MIN = 1
/** admin/usage_trend_series.go:147-149 `if f.top > 20 { f.top = 20 }`。 */
export const TREND_TOP_MAX = 20

/** admin/usage_trend_series.go:46 `usageTrendMaxModels = 20`。 */
export const TREND_MAX_MODELS = 20

/**
 * ★ admin/usage_trend_series.go:524 / :554 / :592 三处 SQL 都写死 `LIMIT 100`。
 * ★ **响应里没有任何字段回显这个上限** ⇒ 达到上限时只能提示「可能还有更多」。
 */
export const TREND_MODELS_SQL_LIMIT = 100

/** admin/usage_trend_series.go:42 `usageTrendOthersKey = "__others__"`。 */
export const TREND_OTHERS_KEY = '__others__'

/** admin/usage_trend_series.go:22-24 `usageTrendModelExpr` 的兜底常量。 */
export const TREND_UNKNOWN_MODEL = '__unknown__'

/** admin/usage_enhanced.go:466 同一套 `YYYY-MM` 无关；本族用 `usage.go:1452` 的 `YYYY-MM-DD`。 */
export const TREND_DATE_FORMAT = 'YYYY-MM-DD'

/* ── 分桶档位 ────────────────────────────────────────────────────────────── */

/** admin/board_time_range.go:60-66 预设档（按 `Days`）的五分钟档。 */
export const TREND_BUCKET_5M = 5
/** admin/board_time_range.go:62 预设档的十五分钟档。 */
export const TREND_BUCKET_15M = 15
/** admin/board_time_range.go:64 预设档的六十分钟档。 */
export const TREND_BUCKET_60M = 60

/** `sqlTrendBucket`（`board_time_range.go:70`）的判据：`minutes >= 60` 走 `date_trunc('hour')`。 */
export const TREND_BUCKET_HOUR_THRESHOLD = 60
/** admin/board_time_range.go:51 自定义档 `span <= 48*time.Hour` 的边界（毫秒）。 */
export const TREND_SPAN_48H_MS = 48 * 60 * 60 * 1000
/** admin/board_time_range.go:53 自定义档 `span <= 14*24*time.Hour` 的边界（毫秒）。 */
export const TREND_SPAN_14D_MS = 14 * 24 * 60 * 60 * 1000

/** 一天的毫秒数（`24*time.Hour`）。 */
export const TREND_DAY_MS = 24 * 60 * 60 * 1000

/**
 * admin/board_time_range.go:59-66 —— **预设档**：按解析出来的 `Days` 选桶。
 * `days` 缺省 1 ⇒ 5 分钟桶。
 */
export function trendBucketMinutesForDays(days?: number): number {
  const d = trendDaysEffective(days)
  if (d <= 1) return TREND_BUCKET_5M
  if (d <= 7) return TREND_BUCKET_15M
  return TREND_BUCKET_60M
}

/**
 * admin/board_time_range.go:48-57 —— **自定义档**：按 `End - Start` 的**实际跨度**选桶。
 *
 * ★★ 与 `trendBucketMinutesForDays` 是**两套规则**（一个按 days、一个按 span）
 *   ⇒ 同一个实际跨度可能落在不同档位：`start`/`end` 恰好 24 小时 ⇒ 5 分钟；
 *   而 days=2 预设窗口跨度也可能只有几十小时，但走的是 `Days=2` 分支 ⇒ 15 分钟。
 */
export function trendBucketMinutesForSpan(spanMs: number): number {
  if (spanMs <= TREND_SPAN_48H_MS) return TREND_BUCKET_5M
  if (spanMs <= TREND_SPAN_14D_MS) return TREND_BUCKET_15M
  return TREND_BUCKET_60M
}

/**
 * `boardDays`（`dashboard_board.go:204-213`）的客户端镜像：缺省 1、clamp [1,90]。
 * ★ 解析失败回落缺省那条路径客户端复现不了（那是 `queryInt` 的 `strconv.Atoi` 失败分支）
 *   ⇒ 本函数只对**已经能给出数字**的入参生效；非法字符串直接原样发出，让后端回落。
 */
export function trendDaysEffective(days?: number): number {
  if (days === undefined || days === null || !Number.isFinite(days)) return TREND_DAYS_DEFAULT
  const n = Math.trunc(days)
  if (n < TREND_DAYS_MIN) return TREND_DAYS_MIN
  if (n > TREND_DAYS_MAX) return TREND_DAYS_MAX
  return n
}

/**
 * `boardPresetTimeRange`（`board_time_range.go:114-125`）的**起点**公式：
 * `todayStartUTC.Add(-(days-1) * 24h)`。
 *
 * ★★ 是 **`days-1` 天**，不是 `days` 天 ⇒ days=7 的起点是**六天前**的 UTC 零点，
 *   `End = now` ⇒ 窗口不足 7×24h（只有 6 天整 + 今天已过部分）。
 * @param todayStartUtc 今天 UTC 零点（`now.Truncate(24h)`，`board_time_range.go:118`）
 */
export function trendPresetStartUtc(days: number, todayStartUtc: Date): Date {
  const d = trendDaysEffective(days)
  return new Date(todayStartUtc.getTime() - (d - 1) * TREND_DAY_MS)
}

/**
 * `resolveUsageTimeRange:1468-1469` 的自定义区间镜像：
 * `Start = startDay.UTC()`，`End = endDay.Add(24h).UTC()`。
 *
 * ★★ **左闭右开** ⇒ 用户选的结束日整天被包含，`End` 是**次日零点**。
 * @param startDay 用户选的起始日（UTC 零点）
 * @param endDay 用户选的结束日（UTC 零点）
 */
export function trendCustomRange(
  startDay: Date,
  endDay: Date,
): { start: Date; end: Date } {
  return {
    start: new Date(startDay.getTime()),
    end: new Date(endDay.getTime() + TREND_DAY_MS),
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 公共：过滤参数
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/usage_trend_series.go:155-164 `usageTrendSource` 的三个返回值。
 * ★ 注意第三个对外叫 `request_logs_with_current_month`，
 *   而真实读的是 `request_logs_with_current_month_without_customer_id`（`:407`）
 *   ⇒ **少 `_without_customer_id` 后缀**（见 `trendSourceRealTable`）。
 */
export const TREND_SOURCES = [
  'request_logs_with_current_month',
  'request_stats_minute',
  'request_stats_dim_minute',
] as const
export type TrendSource = (typeof TREND_SOURCES)[number]

/** 走明细读面的那个档（带 `api_key_id` 时）。对外名 ≠ 真实表名。 */
export const TREND_SOURCE_DETAIL = 'request_logs_with_current_month'
/** 走 provider 汇总档的那个。 */
export const TREND_SOURCE_PROVIDER = 'request_stats_minute'
/** 无 provider/api_key 过滤时的默认档。 */
export const TREND_SOURCE_DIM = 'request_stats_dim_minute'

export function trendSourceValid(v: string): v is TrendSource {
  return (TREND_SOURCES as readonly string[]).includes(v)
}

/**
 * admin/usage_trend_series.go:407 / :588 —— 对外 `source` 名对应的**真实读面**。
 *
 * ★ 对外少 `_without_customer_id` 后缀（见文件头第 (7) 条）
 *   ⇒ 想拿真实表名必须走这个映射，**不能**直接拿 `source` 当表名用。
 */
export function trendSourceRealTable(source: string): string | null {
  if (source === TREND_SOURCE_DETAIL) {
    return 'request_logs_with_current_month_without_customer_id'
  }
  if (source === TREND_SOURCE_PROVIDER) return 'request_stats_minute'
  if (source === TREND_SOURCE_DIM) return 'request_stats_dim_minute'
  return null
}

/**
 * admin/usage_trend_series.go:155-164 `usageTrendSource` 的客户端镜像。
 *
 * ★★ 只在**正整数** ID 下才生效 —— 后端判的是 `> 0`，非法值会静默回落 0
 *   并换档（见文件头第 (6) 条）。所以本函数对非正数也返回 default 档，
 *   与后端一致；UI 应据此提示「过滤没生效」。
 */
export function trendSourceForFilters(
  f: { provider_id?: number; api_key_id?: number } | undefined,
): TrendSource {
  const p = f?.provider_id ?? 0
  const k = f?.api_key_id ?? 0
  if (Number.isFinite(k) && Math.trunc(k) > 0) return TREND_SOURCE_DETAIL
  if (Number.isFinite(p) && Math.trunc(p) > 0) return TREND_SOURCE_PROVIDER
  return TREND_SOURCE_DIM
}

/**
 * ★★ 过滤 ID 会静默失效的情形（`queryInt` 回落 + `> 0` 判据）。
 * 返回非 null 时给出失效原因，UI 应阻止这次请求或明确告知用户。
 *
 * · 非数字 / NaN ⇒ `strconv.Atoi` 失败回落 0
 * · `<= 0` ⇒ 后端 `f.providerID > 0` / `f.apiKeyID > 0` 不成立 ⇒ 当成没传
 */
export function trendFilterIdInvalid(field: 'provider_id' | 'api_key_id', v: unknown): string | null {
  if (v === undefined || v === null) return null
  if (typeof v !== 'number' || !Number.isFinite(v)) return `${field} 不是数字，会被后端静默当成 0`
  if (Math.trunc(v) <= 0) return `${field} <= 0 不构成过滤条件，会被后端静默忽略`
  return null
}

/**
 * admin/usage_trend_series.go:131-143 —— 模型多选的规范化：
 * 空串剔除、重复剔除、超过 **20** 截断。
 * ★ 与后端同口径，使客户端能如实告诉用户「选了 25 个，实际按前 20 个查」。
 */
export function trendModelsNormalize(models: readonly string[] | undefined): string[] {
  if (!models) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of models) {
    const m = raw
    if (m === '' || seen.has(m)) continue
    seen.add(m)
    out.push(m)
    if (out.length >= TREND_MAX_MODELS) break
  }
  return out
}

/** ★ 规范化时因超过 `TREND_MAX_MODELS` 被丢弃的数量（用于提示）。 */
export function trendModelsDroppedCount(models: readonly string[] | undefined): number {
  if (!models) return 0
  return Math.max(0, trendModelsDistinctCount(models) - TREND_MAX_MODELS)
}

function trendModelsDistinctCount(models: readonly string[]): number {
  const seen = new Set<string>()
  for (const m of models) if (m !== '') seen.add(m)
  return seen.size
}

/** ★ 客户端侧 clamp，与 `:144-149` 同口径（回显值以服务端为准）。 */
export function trendTopEffective(top?: number): number {
  if (top === undefined || top === null || !Number.isFinite(top)) return TREND_TOP_DEFAULT
  const n = Math.trunc(top)
  if (n < TREND_TOP_MIN) return TREND_TOP_MIN
  if (n > TREND_TOP_MAX) return TREND_TOP_MAX
  return n
}

/** ★ 后端把非法/负数 ID 当成 0 ⇒ 这类请求的过滤没生效（返回非 null 说明失效）。 */
export function trendFilterIgnored(
  q: { provider_id?: number; api_key_id?: number } | undefined,
): string | null {
  return (
    trendFilterIdInvalid('provider_id', q?.provider_id) ??
    trendFilterIdInvalid('api_key_id', q?.api_key_id)
  )
}

export interface TrendQuery {
  /** 与 `end` **必须同时给**（`usage.go:1451-1453` 只给一个 ⇒ 400）。 */
  start?: string
  /** `YYYY-MM-DD`。区间左闭右开，`end` 字段回显的是**次日零点**。 */
  end?: string
  /** 仅在 `start`/`end` 都缺省时生效；缺省 1、clamp [1,90]。 */
  days?: number
  /**
   * ★ tenant_admin **忽略**这个参数（`stats.go:91-93` 用 `auth.TenantID`），
   *   只有 `super_admin` / `admin_key` 能跨租户。
   */
  tenant_id?: string
  provider_id?: number
  api_key_id?: number
  /** ★ 多选要发**重复参数** `model=a&model=b`（`r.URL.Query()["model"]`），不是逗号串。 */
  model?: string[]
  /** 缺省 8、clamp [1,20]；响应 `top` 回显 clamp 后的值。 */
  top?: number
}

/**
 * 组查询串。
 *
 * ★ `start`/`end` 只给一个时**照样发出去**让后端报 400，不静默补齐
 *   （与 usageEnhanced 同口径：客户端补齐会让用户以为自定义区间生效了）。
 * ★ `model` 必须用重复参数（后端读 `Query()["model"]`），不是 `model=a,b`。
 * ★ 非正整数 ID **不发**（发了会被后端静默当成 0 并换数据档）。
 */
export function trendQuery(q?: TrendQuery): string {
  const parts: string[] = []
  const hasStart = !!q?.start
  const hasEnd = !!q?.end
  if (hasStart || hasEnd) {
    if (q?.start) parts.push(`start=${encodeURIComponent(q.start)}`)
    if (q?.end) parts.push(`end=${encodeURIComponent(q.end)}`)
  } else {
    parts.push(`days=${trendDaysEffective(q?.days)}`)
  }
  if (q?.tenant_id) parts.push(`tenant_id=${encodeURIComponent(q.tenant_id)}`)
  const p = q?.provider_id
  if (typeof p === 'number' && Number.isFinite(p) && Math.trunc(p) > 0) {
    parts.push(`provider_id=${Math.trunc(p)}`)
  }
  const k = q?.api_key_id
  if (typeof k === 'number' && Number.isFinite(k) && Math.trunc(k) > 0) {
    parts.push(`api_key_id=${Math.trunc(k)}`)
  }
  for (const m of trendModelsNormalize(q?.model)) {
    parts.push(`model=${encodeURIComponent(m)}`)
  }
  if (q?.top !== undefined && q?.top !== null && Number.isFinite(q.top)) {
    parts.push(`top=${Math.trunc(q.top)}`)
  }
  return `?${parts.join('&')}`
}

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/usage/trend-series
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/usage_trend_series.go:51-57。四指标并列，指标过滤由前端选列。 */
export interface TrendPoint {
  /** `row.Bucket.UTC().Format(time.RFC3339)`（`:486`）⇒ **UTC**，秒级。 */
  bucket: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

/** admin/usage_trend_series.go:59-66。四个 total 是各点累加（`:492-495`）。 */
export interface TrendModelSeries {
  model: string
  total_requests: number
  total_tokens: number
  total_credits: number
  total_cost_usd: number
  /** 空序列时后端仍会填 `[]`（`make(...,0,len(order))`，`:497`）。 */
  points: TrendPoint[]
}

/** admin/usage_trend_series.go:68-81。 */
export interface TrendSeriesResponse {
  start: string
  /**
   * ★ 预设路径是 `now`（带时分秒），自定义路径是**次日 UTC 零点**
   *   ⇒ **不能假设它总对齐零点**。
   */
  end: string
  /** 5 / 15 / 60（两套规则，见 `trendBucketMinutesForDays` / `…ForSpan`）。 */
  bucket_minutes: number
  /** ★ **clamp 后**的值（`:205`/`:238` 填的是 `f.top`），不是请求原值。 */
  top: number
  /** 三个合法值之一；**不能**当真实表名用（少 `_without_customer_id` 后缀）。 */
  source: string
  /**
   * ★ 空时恒为 `[]` 不是 `null`（`:230-232` 的 `if series == nil { series = []… }`）。
   * ★ 降级时也是 `[]`（`:207`）⇒ **空数组分不清「零用量」与「降级」，只能靠 `degraded`**。
   */
  series: TrendModelSeries[]
  /** ★ 恒发（不带 omitempty）——「服务端确认过它不是降级」。 */
  degraded: boolean
  /** ★ 仅降级时下发；与 `missing_view` **同值**（`:209-210`）。 */
  degraded_reason?: string
  missing_view?: string
  hint?: string
}

// ★ `degraded` **不在**必检键里：由下面的 `typeof !== 'boolean'` 单独把关，
//   那条已覆盖「缺键」与「类型错」两件事（第七十批变异 #11 实测列进去是冗余）。
export const TREND_SERIES_KEYS = [
  'start', 'end', 'bucket_minutes', 'top', 'source', 'series',
] as const
export const TREND_SERIES_MODEL_KEYS = [
  'model', 'total_requests', 'total_tokens', 'total_credits', 'total_cost_usd', 'points',
] as const
export const TREND_POINT_KEYS = [
  'bucket', 'requests', 'tokens', 'credits', 'cost_usd',
] as const

export function fetchTrendSeries(
  q?: TrendQuery,
  options?: RequestOptions,
): Promise<TrendSeriesResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/usage/trend-series${trendQuery(q)}`,
    undefined,
    options,
  ).then(unwrapTrendSeries)
}

export function unwrapTrendSeries(resp: unknown): TrendSeriesResponse {
  const d = requireObject(resp, '用量趋势线')
  // 反向检测**先于**缺键检查：拿到 trend-models 的形状是更根本的错误，
  // 若先跑 requireKeys 会只报「缺 3 个键」而掩盖真正的原因。
  if ('models' in d && !('series' in d)) {
    throw new Error('用量趋势线 拿到的是 trend-models 的载荷形状')
  }
  requireKeys(d, TREND_SERIES_KEYS, '用量趋势线')
  if (typeof d.degraded !== 'boolean') throw new Error('degraded 不是布尔值')
  if (!trendSourceValid(String(d.source))) {
    throw new Error(`source 不是已知的三档之一：${String(d.source)}`)
  }
  if (!isBucketMinutes(String(d.bucket_minutes))) {
    throw new Error(`bucket_minutes 不是 5/15/60 之一：${String(d.bucket_minutes)}`)
  }
  const series = requireArray(d.series, '用量趋势线 series')
  series.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`series[${i}] 不是对象`)
    requireKeys(row, TREND_SERIES_MODEL_KEYS, `series[${i}]`)
    const pts = requireArray(row.points, `series[${i}].points`)
    pts.forEach((p, j) => {
      if (!isPlainObject(p)) throw new Error(`series[${i}].points[${j}] 不是对象`)
      requireKeys(p, TREND_POINT_KEYS, `series[${i}].points[${j}]`)
    })
  })
  return d as unknown as TrendSeriesResponse
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/usage/trend-models
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/usage_trend_series.go:83-89。 */
export interface TrendModelEntry {
  model: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

/** admin/usage_trend_series.go:91-101。 */
export interface TrendModelsResponse {
  start: string
  /** 同 trend-series：预设是 `now`，自定义是次日 UTC 零点。 */
  end: string
  source: string
  /**
   * ★ 空时恒为 `[]`（`:292-294`），降级时也是 `[]`（`:281`）。
   * ★ 按 `requests` 降序（`ORDER BY 2 DESC`），**没有 `__others__`**。
   * ★ 硬编码 `LIMIT 100`，达到上限时**静默截断、无字段提示**。
   */
  models: TrendModelEntry[]
  /** ★ 恒发（不带 omitempty）。 */
  degraded: boolean
  /** ★ 仅降级时下发；与 `missing_view` **同值**（`:283-284`）。 */
  degraded_reason?: string
  missing_view?: string
  hint?: string
}

// ★ 同上：`degraded` 由类型校验把关，不列进必检键。
export const TREND_MODELS_KEYS = ['start', 'end', 'source', 'models'] as const
export const TREND_MODEL_ENTRY_KEYS = [
  'model', 'requests', 'tokens', 'credits', 'cost_usd',
] as const

export function fetchTrendModels(
  q?: TrendQuery,
  options?: RequestOptions,
): Promise<TrendModelsResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/usage/trend-models${trendQuery(q)}`,
    undefined,
    options,
  ).then(unwrapTrendModels)
}

export function unwrapTrendModels(resp: unknown): TrendModelsResponse {
  const d = requireObject(resp, '用量模型选项')
  // 反向检测**先于**缺键检查（同上：更根本的原因优先报）。
  if ('series' in d && !('models' in d)) {
    throw new Error('用量模型选项 拿到的是 trend-series 的载荷形状')
  }
  requireKeys(d, TREND_MODELS_KEYS, '用量模型选项')
  if (typeof d.degraded !== 'boolean') throw new Error('degraded 不是布尔值')
  if (!trendSourceValid(String(d.source))) {
    throw new Error(`source 不是已知的三档之一：${String(d.source)}`)
  }
  const models = requireArray(d.models, '用量模型选项 models')
  models.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`models[${i}] 不是对象`)
    requireKeys(row, TREND_MODEL_ENTRY_KEYS, `models[${i}]`)
  })
  return d as unknown as TrendModelsResponse
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 公共：降级语义
 * ═══════════════════════════════════════════════════════════════════════════ */

export function trendDegraded(r: { degraded?: boolean }): boolean {
  return r.degraded === true
}

export function trendDegradedReason(r: { degraded_reason?: string }): string | null {
  return r.degraded_reason !== undefined && r.degraded_reason !== '' ? r.degraded_reason : null
}

export function trendMissingView(r: { missing_view?: string }): string | null {
  return r.missing_view !== undefined && r.missing_view !== '' ? r.missing_view : null
}

/**
 * ★★ 「零用量」与「没算出来」的唯一区分就是这个布尔（文件头第 (1)(2) 条）。
 *
 * `usageTrendSeriesResponse.Series` 空 + `degraded === false` ⇒ **真的是零用量**；
 * 空 + `degraded === true` ⇒ 读面没迁移，返回的是降级。
 * ⇒ **把空数组直接渲染成「这段时间没有用量」就是撒谎。**
 */
export function trendSeriesIsGenuinelyEmpty(r: TrendSeriesResponse): boolean {
  return !trendDegraded(r) && r.series.length === 0
}

/** 降级时后端仍填了 `bucket_minutes`（`:204`）⇒ 降级态的桶粒度仍是可信的。 */
export function trendBucketMinutesKnown(r: TrendSeriesResponse): boolean {
  return isBucketMinutes(String(r.bucket_minutes))
}

/** ★ 降级时 `top` 仍是 clamp 后的值（`:205`）⇒ 回显可以照着显示。 */
export function trendTopEchoed(r: TrendSeriesResponse): number {
  return Number(r.top)
}

/* ── 折叠判定 ───────────────────────────────────────────────────────────── */

/**
 * ★ `foldUsageTrendRows:438-471` —— 是否发生过 top-N 折叠。
 *
 * 折叠**只**在「没指定 model」且「模型数 > top」时发生：
 * · `:439` `modelFiltered || len(rows)==0` ⇒ **指定了 model 一律不折叠**
 * · `:455-457` `len(ranked) <= top` ⇒ **恰好 top 条不折叠**
 * ⇒ 判据必须用 **`>`** 而不是 `>=`。
 */
export function trendWasFolded(
  r: TrendSeriesResponse,
  modelsSelected: readonly string[] | undefined,
): boolean {
  if (trendModelsNormalize(modelsSelected).length > 0) return false
  return r.series.some((s) => s.model === TREND_OTHERS_KEY)
}

/** ★ 指定了 model 时长尾不会被折叠（`:439`）⇒ 条数可超过 `top`。 */
export function trendFoldSuppressedByModelSelection(
  modelsSelected: readonly string[] | undefined,
): boolean {
  return trendModelsNormalize(modelsSelected).length > 0
}

/**
 * ★★ `__others__` 是聚合线，不是真实模型名（`:462-469` 把未入选**行**改写成它，
 *   pivot 后自然聚成一条序列）⇒ 下拉 / 图例里必须标「其余 N 个模型合计」。
 */
export function trendOthersLine(r: TrendSeriesResponse): TrendModelSeries | null {
  return r.series.find((s) => s.model === TREND_OTHERS_KEY) ?? null
}

/**
 * ★★ `__others__` 固定排最后（`:221-229`），其余按 `TotalRequests` 降序。
 *
 * 等价于后端那个比较器：`__others__` **只允许出现在末位**；没出现也算成立。
 */
export function trendOthersIsLast(r: TrendSeriesResponse): boolean {
  const idx = r.series.findIndex((s) => s.model === TREND_OTHERS_KEY)
  return idx < 0 || idx === r.series.length - 1
}

/**
 * ★★ `sort.SliceStable`（`:221`）有两个可观察后果，**客户端只能验证其中一个**：
 *
 * 可验证：`__others__` 强制排最后（`:222-227`）+ 其余按 `TotalRequests` **非递增**。
 * **不可验证**：「同 `TotalRequests` 保持原序」—— 后端的原序来自 SQL
 * `ORDER BY 2, 1`（`:334` 的 bucket 升序下的模型名字典序），
 * 响应里**没有可用来比对的参照物** ⇒ 任何声称能验证这一点的客户端函数都是恒真。
 *
 * 所以这里只提供**非递增**这一半，且把 `__others__` 排除在比较之外
 * （它被强制排在末尾，不参与降序）。
 */
export function trendSeriesNonIncreasing(r: TrendSeriesResponse): boolean {
  const ranked = r.series.filter((s) => s.model !== TREND_OTHERS_KEY)
  return ranked
    .slice(1)
    // i 是 `slice(1)` 后的下标 ⇒ 对应 `ranked[0..n-2]`，由 slice 长度保证存在
    .every((cur, i) => Number(ranked[i]!.total_requests) >= Number(cur.total_requests))
}

/**
 * ★ `trend-models` 达到硬编码 `LIMIT 100` 时**静默截断**（`:524`/`:554`/`:592`）
 *   且响应无字段可证 ⇒ 只能说「可能还有更多」。
 */
export function trendModelsPossiblyTruncated(r: TrendModelsResponse): boolean {
  return r.models.length >= TREND_MODELS_SQL_LIMIT
}

/**
 * ★★ 传了 `model` 多选 ⇒ `trend-models` 结果被 `= ANY` 收敛到选中集合
 *   （`:579-581`）⇒ **它是「已选模型的量」而不是「可选模型清单」**，
 *   用它填下拉会得到一个只剩已选项的列表。
 */
export function trendModelsListIsConstrained(
  r: TrendModelsResponse,
  modelsSelected: readonly string[] | undefined,
): boolean {
  return trendModelsNormalize(modelsSelected).length > 0 && !trendDegraded(r)
}

/* ── hint ───────────────────────────────────────────────────────────────── */

/**
 * admin/dashboard_degrade.go:83-88 `missingRelationHint` 的本地镜像。
 *
 * ★ 后端 hint 可被本地复算 ⇒ 客户端应自己拼（可本地化），
 *   后端那份只当作契约校验对象。
 */
export function trendHintLocal(view: string | null): string {
  if (!view) return '数据视图尚未初始化，请先执行数据聚合迁移'
  return `数据视图 ${view} 尚未初始化，请先执行数据聚合迁移`
}

/** ★ 后端 hint 与本地推导不一致（正常不该发生；不一致说明两端口径已漂）。 */
export function trendHintDisagrees(r: { hint?: string }, view: string | null): boolean {
  if (r.hint === undefined) return false
  return r.hint !== trendHintLocal(view)
}

/* ── 时间窗形态 ─────────────────────────────────────────────────────────── */

/**
 * ★★ `end` 是**次日零点**时说明走的是自定义区间（`usage.go:1469`）；
 *   预设路径的 `End = now`（`board_time_range.go:121`）带时分秒。
 * ⇒ 可用来判断后端到底按哪条路径解析的，**不能**当成结束日显示。
 */
export function trendEndIsNextMidnight(endIso: string): boolean {
  const t = Date.parse(endIso)
  if (Number.isNaN(t)) return false
  const d = new Date(t)
  return (
    d.getUTCHours() === 0 &&
    d.getUTCMinutes() === 0 &&
    d.getUTCSeconds() === 0 &&
    d.getUTCMilliseconds() === 0
  )
}

/**
 * ★ 预设路径下 `end` 必带时分秒（就是 `now`）。
 * 两条互斥：真后端响应必居其一（`now` 恰为 UTC 零点是理论可能，取弱断言）。
 */
export function trendEndLooksLikeNow(endIso: string, nowMs: number): boolean {
  const t = Date.parse(endIso)
  if (Number.isNaN(t)) return false
  // 允许 2 分钟漂移：响应里的 now 与客户端时钟不同源
  return Math.abs(t - nowMs) <= 2 * 60 * 1000
}

/**
 * ★★ 自定义区间**左闭右开** ⇒ `start` 是选中日的零点（`end - (days+1)×24h` 侧），
 *   而 `end` 比「用户选的结束日」晚整整一天。
 *   ⇒ UI 显示结束日时应减一天，否则会多显示一个空桶。
 */
export function trendSelectedEndDay(endIso: string): Date | null {
  const t = Date.parse(endIso)
  if (Number.isNaN(t)) return null
  return new Date(t - TREND_DAY_MS)
}

/* ── 内部工具 ────────────────────────────────────────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  if ('success' in resp && 'timestamp' in resp) {
    throw new Error(`${where} 拿到的是 dashboardapi 信封形状，本族应为裸 JSON`)
  }
  return resp
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}

/**
 * ★ 只认 `trendBucketMinutes` 真能返回的三个值（`board_time_range.go:47-67`）：
 *   5 / 15 / 60。别的值说明前后端口径漂了。
 *   ⇒ 桌面 `UsageTrendExplorer.vue:167` 的 `resp.bucket_minutes || 60` 兜底
 *     **在本客户端不抄**：后端恒发 int，兜底恒不生效，且 60 不是唯一合法值。
 */
function isBucketMinutes(v: string): boolean {
  return v === String(TREND_BUCKET_5M) || v === String(TREND_BUCKET_15M) || v === String(TREND_BUCKET_60M)
}
