import { req, type RequestOptions } from './client'

/**
 * dataHorizon.ts — 「流量数据地平线」的全局告示（2026-10-15，第一百二十九批）。
 *
 * GET `/api/admin/data-horizon`
 *
 * ## ★★★★★ 本模块是**重写**，不是改名（r1033 换了判据本身）
 *
 * 前一代叫 `v1DataHorizon.ts` / `v1-data-horizon`，盯着
 * `storage.request_logs_write_enabled`（v1 停写门）。那个前提在统计链全面
 * session 化之后**已不成立**：页面数字经视图 session 分支持续更新，
 * 停写两日后 `request_stats_minute` 的 bucket 仍在推进（后端文件头原文）。
 * ⇒ 按 2026-10-15 裁决「以 session_turns 为准、弃用 request_logs」，
 * 后端把 `admin/v1_freeze_notice.go` 整文件换成 `admin/data_horizon.go`。
 *
 * ⚠️⚠️⚠️ **本批做的不是「把 v1 换成 data」** —— 后端**删掉了一整批概念**，
 *   移动端那些对应的 helper 是**下线**而不是改名。逐条对照：
 *
 * | 前一代概念 | 现状 | 处置 |
 * |---|---|---|
 * | `affects: string[]`（三值） | **后端已无该字段** | ★ **删除** `V1_HORIZON_AFFECTS` 及两个判据 |
 * | `gate_key`（配置键名） | **换成 `hint`（排查方向文案）** | ★ **删除** `V1_HORIZON_GATE_KEY` |
 * | gate `source ∈ {db, env, default}` | **概念消失**（配置来源不再参与判定） | ★ **删除** `V1_GATE_SOURCES` / `v1GateSourceIs*` |
 * | `deriveV1GateState(bool, source)` | 判定已内化到后端 `dataHorizonNoticeFor` | ★ **删除**（前端不该重算后端的纯函数判定） |
 * | `frozen: bool` | **`stale: bool`** | 改名 |
 * | `live/frozen/unavailable` | **`live/stale/unknown`** | 改名（第三档从「不可用」变「读不到」） |
 * | 响应头 `X-LLM-Gateway-V1-Data-Frozen`，值 `1`/`unknown` | **`X-LLM-Gateway-Data-Horizon`，值 `stale`/`unknown`** | 改名 |
 * | 头挂在 `writeJSON` **中央出口** | ★★ 头**只在端点自身** | 见 (7) |
 * | `source` 恒为 `"_request_logs"` | `source` 恒为 **`"session_turns_hot"`** | 改名（歧义消失，见 (8)） |
 * | 七键 notice | **六键** notice | 减少 |
 *
 * ⇒ ★★ 按本仓纪律「可证等价 ⇒ 删变异而不是补用例」的同源一条：
 *   **后端不存在的概念，前端不留影子 helper** ——
 *   留着它们只会让下一个人以为 `affects` / `gate_key` 还存在。
 *
 * - **注册**：`admin/handler.go:1109`
 *   `mux.HandleFunc("/api/admin/data-horizon", admin(h.handleDataHorizon))`
 *   ⇒ ★ **admin 档**（`admin := h.admin` 是 `admin/handler.go:925` 的局部别名，
 *     `h.admin` = `AdminMiddleware`，**全文无 `claims.Role` 比对**）
 *     ⇒ 抽屉席**不设** `requiresRole`。
 * ★★★★★ 权限结论**沿用前一代且理由不变**（第一百一十九批已把它升级成明确规则）：
 *   过度限制（`'super_admin'`）的**正当性来自「跨租户数据」**，不是来自
 *   「没有租户维度」本身。本族载荷是**平台级全局事实**
 *   （「这台网关的 session_turns 有没有在跟上」），六键里**没有一丁点租户相关**。
 *   ⇒ ★★★ 而且**必须**让 tenant_admin 看到：`Silence` 自己写着
 *     「这类断流没有任何错误信号：统计接口照常返回、数字齐全。
 *     接口没报错不代表数据是新的。」把告示挡在 tenant_admin 之外，
 *     等于让他们把断流数据读成新鲜数据。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **主键恒在，值有「对象」与「裸 `null`」两种形态** —— 不是主键消失。
 *      `handleDataHorizon`（`:198-206` 与 `:207-211`）两条分支都写**两键**：
 *      ```go
 *      {"data_horizon": notice, "//": "stale=true 表示网关有流量而 session_turns 未记录；…"}
 *      {"data_horizon": nil,    "//": "同上"}
 *      ```
 *      ⇒ 与批 84 的降级信封（**主键整个消失**）**正好相反**。
 *      ⇒ ⇒ 判据必须是「**值为 null ⇒ 数据新鲜或网关空闲**」，
 *        **不能**是「键存在 ⇒ 有告示」。
 *
 * (2) ★★★★★ **后端刻意不产出「有对象但 stale=false 且 unknown=false」**。
 *      `DataHorizonNotice` 类型注释（`:74-76`）写明：
 *      「两者互斥，且都为 false 时不产生对象」。
 *      ⇒ ★★★ 于是「有对象 ⇒ `stale` 与 `unknown` **恰好一个为 true**」是
 *        **可自验的强不变量**，机制是两个分支各自只设其中一个：
 *        - stale 分支（`:106-114`）：`Stale: true`，`Unknown` 未设 ⇒ 序列化为 `false`
 *        - unknown 分支（`:123-129`）：`Unknown: true`，`Stale` 未设 ⇒ `false`
 *
 * (3) ★★★★★ **`stale: false` 现在是有合法实例的**（就是 unknown 那一档）。
 *      ⇒ ★★★ 判「已断流」**不能**只读 `stale`。
 *
 * (4) ★★★★ **本代判据换了「两个信号对齐」，而前一代是单配置键** ——
 *      这是与前代最大的行为差异：
 *      ```go
 *      func dataHorizonNoticeFor(sessionMaxTS, lastRequestSeen, now time.Time) (*DataHorizonNotice, bool)
 *      ```
 *      · `sessionMaxTS`：`session_turns_hot` 的 `MAX(ts)`（会话族系统记录）
 *      · `lastRequestSeen`：hub 的 `lastActivity`（进程内真值，**不依赖任何存储**）
 *      ⇒ ★★ 后者能区分「没数据因为**没流量**」与「没数据因为**断流**」；
 *        前一代刻意不做 MAX(ts) 推断（「表空/首写未发生时给反结论」）。
 *      ⇒ ★★ **两个信号对齐后才能判**。
 *
 * (5) ★★★★ **两个时间窗常量**（前端只展示、不参与判定）：
 *      · `dataHorizonRecentTrafficWindow = 10 * time.Minute`
 *        「网关近期有请求」的判定窗；窗外视为空闲 ⇒ 不告示。
 *      · `dataHorizonLagTolerance = 5 * time.Minute`
 *        session 侧落后最近请求的容忍（镜像写入约 5s 量级，故分钟级极宽）。
 *
 * (6) ★★★★ **★ 空表（`sessionMaxTS` 零值）判 `stale`，不是 `unknown`。**
 *      `:101` 的注释：「零值（无行）与落后同样判 stale：一个刚接过请求的网关，
 *      会话族里连一行近期数据都没有，就是断流本身。」
 *      ⇒ 与 (2) 配套：**表空 + 有流量 = stale**；**读不上来 = unknown**。
 *
 * (7) ★★★★ **★ 响应头不再是「中央出口」，只在本端点自身携带。**
 *      `applyDataHorizonHeader`（`:174-181`）由 `handleDataHorizon`（`:193`）单独调。
 *      后端文件头：「新判据要查 DB——挂中央出口等于每个 JSON 响应都打一次库，
 *      不可接受。」⇒ ★★ 前一代的 `X-LLM-Gateway-V1-Data-Frozen` 可以出现在
 *      **任意** JSON 响应上；这一代**不能**。⇒ 客户端**不要**在别的端点上找它。
 *
 * (8) ★★★★ **响应头是「三值 + 缺失」**：
 *      - live（数据新鲜或网关空闲）⇒ **头根本不设置**（`notice == nil` 直接 return）
 *      - stale ⇒ 头 `"stale"`
 *      - unknown ⇒ 头 `"unknown"`（**与 stale 用不同值**，抓包必须能区分）
 *      ⇒ ★★★ 这是**第十二种 nil 编码：头缺失**。
 *      ⇒ ★★ 头与 body 是**两套独立表达**，客户端可以用它交叉验证。
 *
 * (9) ★★★ **`source` 恒为字面量 `"session_turns_hot"`**（`:108`、`:125`）。
 *      ⇒ ★★ 前一代的命名陷阱（「`source` 在响应里指两件不同的事」）**已消失** ——
 *        现在只有一个含义：**被检查的数据源族**。
 *      ⇒ 因为是恒定常量，本模块导出但**不提供**「取值域校验」型判据（那会恒真）。
 *
 * (10) ★★★ `effect` / `silence` / `hint` 是**给人读的文案**，
 *       stale 与 unknown **各一套**（stale 那套讲「断流」，unknown 那套讲「读不到」）
 *       ⇒ ★★ 客户端**不要**用文案判状态，要用 `stale`/`unknown`；
 *         文案只用于**展示**。
 *
 * ★★ `DataHorizonNotice` 六键**无 omitempty** ⇒ **六键恒在**
 *     （`stale`/`unknown`/`source`/`effect`/`silence`/`hint`）。
 *
 * ★★ 错误面：**非 GET 未单独拦**（`:190` 起直接进逻辑）⇒ 端点自身不产 405；
 *   查询失败**不产 500** —— 它被折叠进 `unknown` 分支（`:158-161`、`:163-165`）
 *   ⇒ ★★★ **「读不到」是一条正常状态，不是错误**。
 *   超时 **3s**（`:191`，前一代没有这一档）。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验**两键信封恒在** + 主键是
 *   「六键对象」或「`null`」+ 各键类型；**不校验** `effect`/`silence`/`hint`
 *   的文案内容（那是展示文案，两套字面量都是后端硬编码）。
 */

/** `:65` 携带告示的响应头名。★ 只在本端点自身（见 (7)）。 */
export const DATA_HORIZON_HEADER = 'X-LLM-Gateway-Data-Horizon'

/** (9) 被检查的数据源族，**恒定**。 */
export const DATA_HORIZON_SOURCE = 'session_turns_hot ∪ session_turns'

/** (2) 主键恒在的两个信封键。 */
export const DATA_HORIZON_ENVELOPE_KEYS = ['data_horizon', '//'] as const

/** 主键名（值是对象或裸 `null`，见 (1)）。 */
export const DATA_HORIZON_KEY = 'data_horizon'

/** (5) 「网关近期有请求」的判定窗（分钟）。★ 前端只展示。 */
export const DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES = 10

/** (5) session 侧落后最近请求的容忍（分钟）。★ 前端只展示。 */
export const DATA_HORIZON_LAG_TOLERANCE_MINUTES = 5

/** 三态（(1)(2)(3)）。前一代第三档是 `unavailable`，这一代是 `unknown`。 */
export const DATA_HORIZON_STATES = ['live', 'stale', 'unknown'] as const

/** 六键 notice 的键序（★ 与 Go struct 字段序一致，`stale`/`unknown` 在最前）。 */
export const DATA_HORIZON_NOTICE_KEYS = [
  'stale',
  'unknown',
  'source',
  'effect',
  'silence',
  'hint',
] as const

export type DataHorizonState = (typeof DATA_HORIZON_STATES)[number]

export interface DataHorizonNotice {
  /** ★★★★★ (2)(3)(6) true = **确证断流**；false 时**必须**看 `unknown`。 */
  stale: boolean
  /** ★★★★★ (2)(6) true = **读不到 session 数据**（DB 故障/表缺失/lite 未接库）。 */
  unknown: boolean
  /** ★★★ (9) 恒为 `"session_turns_hot"`；**不是**配置来源。 */
  source: string
  /** ★★ (10) 展示文案；两个分支各一套，不要用它判状态。 */
  effect: string
  /** ★★ (10) 展示文案；两个分支各一套。 */
  silence: string
  /** ★★ (10) 给运维的排查方向；两个分支各一套。★ 前代无此键（那边是 `gate_key`）。 */
  hint: string
}

export interface DataHorizonResponse {
  /** ★★ 恒在；值是「六键对象」或**裸 `null`**。见 (1)。 */
  data_horizon: DataHorizonNotice | null
  '//': string
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/data-horizon`（**admin 档**；★★ 抽屉席不设 `requiresRole`）。 */
export function fetchDataHorizon(options?: RequestOptions): Promise<DataHorizonResponse> {
  return req<unknown>('GET', '/api/admin/data-horizon', undefined, options).then(unwrapDataHorizon)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapDataHorizon(resp: unknown): DataHorizonResponse {
  const d = requireObject(resp, '数据地平线')
  requireKeys(d, DATA_HORIZON_ENVELOPE_KEYS, '数据地平线')
  const n = d['data_horizon']
  // ★ (1) 裸 `null` 是**合法且常见**的形态（数据新鲜或网关空闲）。
  //   若这里把它判成错，网关空闲时页面直接报错 —— 而那是完全正常的状态。
  if (n !== null) {
    const rec = requireObject(n, '数据地平线 的 data_horizon')
    requireKeys(rec, DATA_HORIZON_NOTICE_KEYS, '数据地平线 的 data_horizon')
    // ★ requireObject 已经收窄成 Record<string, unknown>，
    //   直接 rec[k] 才不会报 TS7053（n 在此处只是 `unknown`）。
    for (const k of ['stale', 'unknown'] as const) {
      if (typeof rec[k] !== 'boolean') {
        throw new Error(`数据地平线 的 data_horizon.${k} 不是布尔（实得 ${typeof rec[k]}）`)
      }
    }
    for (const k of ['source', 'effect', 'silence', 'hint'] as const) {
      if (typeof rec[k] !== 'string') {
        throw new Error(`数据地平线 的 data_horizon.${k} 不是字符串（实得 ${typeof rec[k]}）`)
      }
    }
  }
  return d as unknown as DataHorizonResponse
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

/**
 * ★★★★ (1)(3) 数据新鲜**或**网关空闲 —— 两种情况都**不产生对象**。
 *
 * ⇒ ★★★ 判据是「**主键为 `null`**」，不是「键存在」（键恒在，见 (1)）。
 */
export function dataHorizonIsLive(body: DataHorizonResponse): boolean {
  return body.data_horizon === null
}

/** ★★★★ (2)(3)(6) **确证断流**：有对象且 `stale` 为 true。 */
export function dataHorizonIsStale(body: DataHorizonResponse): boolean {
  const n = body.data_horizon
  return n !== null && n.stale === true
}

/**
 * ★★★★ (2) **读不到 session 数据**（DB 故障 / 表缺失 / lite 未接库）。
 *
 * ⇒ ★★ 与 `isStale` **严格互斥**，因为后端不产出「两个都 true」的对象。
 */
export function dataHorizonIsUnknown(body: DataHorizonResponse): boolean {
  const n = body.data_horizon
  return n !== null && n.unknown === true
}

/**
 * ★★★★★ (2) **可自验的强不变量**：有对象 ⇒ `stale` 与 `unknown` **恰好一个**为 true。
 *
 * ⇒ 这是本族最有牙齿的一条判据：它对**每一个真实后端响应**都成立，
 *   而一旦后端改成产出「两个都 false」或「两个都 true」，它立刻红。
 *   ⇒ 「值为 `null`」那一档**不算违反**（压根没有对象可判）。
 */
export function dataHorizonExactlyOneFlag(body: DataHorizonResponse): boolean {
  const n = body.data_horizon
  if (n === null) return true
  return (n.stale === true) !== (n.unknown === true)
}

/**
 * ★★★ (9) `source` 恒为 `"session_turns_hot"`。
 *
 * ⚠️ 这是**恒定常量比对**，不是「取值域校验」—— 判据防的是
 * 「客户端把 `source` 误当成配置来源」这个**命名陷阱**，
 * 而那个陷阱在前一代是真实存在的（见模块头 (9) 的对照行）。
 */
export function dataHorizonSourceIsSessionTurns(notice: DataHorizonNotice): boolean {
  return notice.source === DATA_HORIZON_SOURCE
}

/** ★★★ 形状不合契约（有对象但两个 flag 都不是 true）时的兜底态。 */
export function dataHorizonIsUnexpectedShape(body: DataHorizonResponse): boolean {
  return body.data_horizon !== null && !dataHorizonExactlyOneFlag(body)
}

// ═══════════════════════════════════════════════════════════════════════════
// 响应头
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★★ (8) 解析告示头 —— **四值**：缺失 `null` / `'stale'` / `'unknown'` /
 * `'unexpected'`（**出现了第三种字面量**）。
 *
 * ⚠️ (7) 这个头**只在本端点自身携带**；不要在别的 JSON 响应上找它。
 * ⚠️ 返回 `null` 有两种可能：真的没设置，或调用方压根没给这个 `get`。
 *
 * ★★★ `'unexpected'` 必须**进返回类型**而不是塞个类型断言：
 *   初版写成 `return 'unexpected' as 'stale'` ⇒ 类型说谎（`'stale'` 是它绝不返回的值），
 *   于是「契约破了」这个分支在类型层面不可见 ⇒ 下一个人会以为它不存在。
 *   ⇒ **类型必须反映现实**，否则「诚实的前端」只是口号。
 */
export type DataHorizonHeaderValue = 'stale' | 'unknown' | 'unexpected' | null

export function parseDataHorizonHeader(get: (name: string) => string | null): DataHorizonHeaderValue {
  const v = get(DATA_HORIZON_HEADER)
  if (v === null) return null
  if (v === 'stale') return 'stale'
  if (v === 'unknown') return 'unknown'
  // ★ 出现第三种字面量就是契约破了 —— 宁可显式暴露也别默默当成 live。
  return 'unexpected'
}

/**
 * ★★★★ (8) 头与 body **交叉验证**：两边说的必须是同一档。
 *
 * ⇒ ★★ `true` 的含义是「**没有矛盾**」，而**不是**「两边都非空」——
 *   live 档时 body 有对象吗？没有（是 null）；头呢？也没有（未设置）。
 *   ⇒ 所以判据必须把 live 单独放行，否则它会恒红。
 */
export function dataHorizonHeaderMatchesBody(
  get: (name: string) => string | null,
  body: DataHorizonResponse,
): boolean {
  const h = parseDataHorizonHeader(get)
  if (dataHorizonIsLive(body)) return h === null
  if (dataHorizonIsStale(body)) return h === 'stale'
  if (dataHorizonIsUnknown(body)) return h === 'unknown'
  return false
}
