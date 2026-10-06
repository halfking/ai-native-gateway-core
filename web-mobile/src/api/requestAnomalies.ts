import { req, type RequestOptions } from './client'

// requestAnomalies.ts — 请求侧异常（reqprobe）的只读面。
//   GET /api/admin/request-anomalies           列表（day/provider/model/trigger/unresolved_only）
//   GET /api/admin/request-anomalies/count     导航徽标计数
//
// 鉴权：两条都挂 `h.superAdmin(...)`（admin/handler.go:918-919）
// ⇒ **superAdmin 档**：tenant_admin 直接 403 ⇒ 抽屉席**必须**设
//   `requiresRole: 'super_admin'`，并同步 `AppDrawer.spec.ts` 的白名单。
//
// ★ 写操作 `POST /{id}/resolve` 与 `POST /batch-resolve` **不在本文件**。
//
// ⚠️⚠️⚠️ 这一族答的是「**上游在拒绝我们的什么请求**」，与
//   `response_format_anomalies`（PG 表，另一族）、`/node-audit`（节点健康）
//   互不重叠。存储是 Redis（Full）或内存（lite）。
//
// ⚠️⚠️⚠️⚠️ 最阴的一条：**同一个筛选面板里，四个字段的大小写敏感度不一致。**
//
// (1) ★★★★ `internal/reqprobe/types.go:117-134` 的 `Filter.matches`：
//       Day      : `r.Day != f.Day`                    ⇒ **大小写敏感**精确
//       Provider : `!strings.EqualFold(...)`           ⇒ **大小写不敏感**
//       Model    : `!EqualFold(ClientModel) && !EqualFold(OutboundModel)`
//                   ⇒ **大小写不敏感**，且是 **OR**：命中任一即可
//       Trigger  : `string(r.Trigger) != f.Trigger`    ⇒ **大小写敏感**精确
//     ⇒ `?trigger=PARAM_REJECTED` 会**静默返回空数组**且不报错；
//       `?provider=OpenAI` 却能命中 `openai`。
//     ⇒ 前端因此**不对 trigger 做大小写归一**（会把用户输入改成后端匹配不到的值），
//       但要在界面上说明 trigger 要按后端给的小写字面值填。
//
// (2) ★★★★ `model` 筛的是「**客户端模型 或 出站模型**」的 OR。
//     用户按「我请求的模型」筛，出来的行 `outbound_model` 可能**完全不同**
//     （网关做了模型重写）。⇒ 两个模型**必须都显示**，否则会以为筛错了。
//
// (3) ★★★★ **同一类错误每天一行。**
//     `Fingerprint`（`types.go:160-176`）把 `Day` 算进 sha1
//     ⇒ 同一个问题**每天**产生一条新记录。
//     所以 `occurrences` 只是**今天**的次数（注释自陈），
//     想看「这个错误总共出现过几次」必须自己跨天累加。
//
// (4) ★★★ `day` 是 `FirstSeen` 所在**本地日期**（`YYYY-MM-DD`），
//     而 `Today()` 用 `t.Local()` ⇒ **网关进程的时区**，不是浏览器时区。
//     ⇒ 移动端看到的「今天」可能与用户所在时区的今天**不是同一天**。
//
// (5) ★★★ `Counts` **只统计未解决的**（`redis.go:167-170`：
//     `if rec.Resolved { continue }`）⇒ `unresolved` 不含已解决，
//     `new_today` 是「今天首次出现**且未解决**」的条数。
//     ⇒ 徽标数字与列表条数**天然对不上**：列表默认返回全部（含已解决），
//       除非带 `unresolved_only=true`。这不是 bug，但必须说明，否则用户
//       会以为「计数错了」。
//
// (6) ★★★ `trigger` **只有三个**取值（`types.go:48-61`）：
//     `param_rejected` / `mode_mismatch` / `upstream_error`。
//     其中 `mode_mismatch` 不参与参数学习（协议切换是每请求的廉价回退）。
//
// (7) ★★ `limit` clamp [1,500]、非数字回落 50（`queryInt(r,"limit",50)` +
//     `if limit<=0 {limit=50}` + `if limit>500 {limit=500}`）；
//     `offset<0` → 0。**永不报错**。
//     ★ 数字与 `pending-responses` 的 `pageBounds` **完全一样**（50/500），
//       但那是**两个不同的实现**，不可当成同一份契约。
//     ★ `loadAll` 是 `HGETALL` 全量读回内存过滤（`redis.go:119`），
//       **没有条数上限**（retention 30 天）⇒ 列表不会被静默截断。
//
// (8) ★★ 响应里的可选字段**键可能整个不存在**（`omitempty`）：
//     `client_model` / `outbound_model` / `param` / `suggest_mode` /
//     `error_kind` / `error_sample` / `last_request_id` / `resolution_notes`，
//     以及 `resolved_at`（`*time.Time` + omitempty）。
//     ★ 另注意 `param` 是**逗号连接的多个参数名**（一个请求可能被拒多个参数），
//       不是单个值 —— 按逗号拆开显示。
//
// (9) ★ 错误信封走 `writeError`（`admin/handler.go:1494-1498`）⇒
//     `{"error":{"detail":…}}`，键是 **`detail`** 不是 `message`。
//     ★ 这是本仓库见到的**第三个信封族**：
//       pending-responses = `{"error":{"message":…,"code":…}}`
//       routing-opt       = **text/plain**（`http.Error`）
//       request-anomalies = `{"error":{"detail":…}}`
//     `api/client.ts` 的 `errorMessage` 三种都兜得住（`:100-117`）。
//     503 = store 未接线（Full/lite 都没起来）；500 = 查询失败。

/** ★ 后端 `Trigger` 常量的**全部**取值（`types.go:48-61`），照抄而非推断。 */
export const ANOMALY_TRIGGERS = ['param_rejected', 'mode_mismatch', 'upstream_error'] as const
export type AnomalyTrigger = (typeof ANOMALY_TRIGGERS)[number]

export const ANOMALY_LIMIT_DEFAULT = 50
export const ANOMALY_LIMIT_MAX = 500

export interface AnomalyRecord {
  id: number
  /** ★ 含 day 的 sha1 ⇒ 同一问题每天一行。见坑 3。 */
  fingerprint: string
  /** ★ `FirstSeen` 的本地日期（YYYY-MM-DD），**不是** UTC。见坑 4。 */
  day: string
  provider_id: number
  provider_code: string
  /** ★ omitempty ⇒ 可能缺键。筛 model 时**命中的是它或 outbound_model**。见坑 2。 */
  client_model?: string
  /** ★ omitempty ⇒ 可能缺键；可能与 `client_model` 完全不同（网关重写过）。 */
  outbound_model?: string
  protocol?: string
  trigger: string
  /** ★ omitempty；**逗号连接的多个参数名**，不是单值。见坑 8。 */
  param?: string
  /** ★ omitempty；`param_rejected` 时必为空。 */
  suggest_mode?: string
  http_status: number
  /** ★ errorsx 的低基数分类（`client_bug` / `unsupported_feature` …）。 */
  error_kind?: string
  /** ★ 上游错误体截断样例（≤512 字节），**不含请求正文**。 */
  error_sample?: string
  /** ★ **今天**的出现次数，不是累计。见坑 3。 */
  occurrences: number
  /** 「剔除参数/切换模式后重试成功」的次数。 */
  recovered_count: number
  first_seen: string
  last_seen: string
  last_request_id?: string
  resolved: boolean
  /** ★ `*time.Time` + omitempty ⇒ 可能缺键。 */
  resolved_at?: string
  resolution_notes?: string
}

/** `param` 是逗号连接的多个参数名 ⇒ 拆成数组供界面逐个显示。 */
export function anomalyParams(param: string | undefined): string[] {
  if (!param) return []
  return param
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

export interface AnomalyListResponse {
  anomalies: AnomalyRecord[]
  /** ★ 过滤后总数，在**切页之前**算 ⇒ 可以当分页总数用。 */
  count: number
  limit: number
  offset: number
}

export interface AnomalyListParams {
  day?: string
  provider?: string
  /** ★ 大小写不敏感，且匹配 client_model **或** outbound_model。 */
  model?: string
  /** ★ 大小写敏感精确匹配；只发 `ANOMALY_TRIGGERS` 里的值。见坑 1/6。 */
  trigger?: string
  unresolvedOnly?: boolean
  limit?: number
  offset?: number
}

export function fetchAnomalyList(params: AnomalyListParams = {}, options?: RequestOptions): Promise<AnomalyListResponse> {
  const qs = new URLSearchParams()
  const day = (params.day ?? '').trim()
  if (day !== '') qs.set('day', day)
  const pv = (params.provider ?? '').trim()
  if (pv !== '') qs.set('provider', pv)
  const md = (params.model ?? '').trim()
  if (md !== '') qs.set('model', md)
  // ★★ trigger **大小写敏感**：只发后端常量里那三个小写字面值。
  //   用户填 `PARAM_REJECTED` 会静默拿到空数组 ⇒ 这里**不归一**，
  //   改成小写反而会让人以为后端认了大写。
  if (params.trigger && (ANOMALY_TRIGGERS as readonly string[]).includes(params.trigger)) {
    qs.set('trigger', params.trigger)
  }
  // ★ `unresolved_only` 的真值判定是 `EqualFold("true") || == "1"`
  //   （`queryBool`，admin/handler.go:1546-1549）⇒ 只在这两种形态下发送。
  if (params.unresolvedOnly === true) qs.set('unresolved_only', 'true')
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n > 0 && n <= ANOMALY_LIMIT_MAX) qs.set('limit', String(n))
  }
  if (typeof params.offset === 'number' && Number.isFinite(params.offset)) {
    const n = Math.trunc(params.offset)
    if (n > 0) qs.set('offset', String(n))
  }
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/request-anomalies${s ? '?' + s : ''}`, undefined, options).then(unwrapAnomalyList)
}

/** ★ 形状不符抛错（理由同 `unwrapPendingList`）：静默返 `[]` 会让「解包失败」
 *  与「真的没有异常」在页面上长得一模一样。 */
export function unwrapAnomalyList(resp: unknown): AnomalyListResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as AnomalyListResponse).anomalies)) {
    return resp as AnomalyListResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`request-anomalies 响应形状不符：期望 {anomalies:[…], count, limit, offset}，实得 ${actual}`)
}

// ── count（导航徽标） ──────────────────────────────────────────────────────

export interface AnomalyCounts {
  /** ★ **只含未解决**；已解决的不计。见坑 5。 */
  unresolved: number
  /** ★ 「今天（网关本地日期）首次出现**且未解决**」的条数。见坑 4/5。 */
  new_today: number
}

export function fetchAnomalyCounts(options?: RequestOptions): Promise<AnomalyCounts> {
  return req<unknown>('GET', '/api/admin/request-anomalies/count', undefined, options).then(unwrapAnomalyCounts)
}

export function unwrapAnomalyCounts(resp: unknown): AnomalyCounts {
  if (
    resp &&
    typeof resp === 'object' &&
    typeof (resp as AnomalyCounts).unresolved === 'number' &&
    typeof (resp as AnomalyCounts).new_today === 'number'
  ) {
    return resp as AnomalyCounts
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`request-anomalies/count 响应形状不符：期望 {unresolved, new_today}，实得 ${actual}`)
}