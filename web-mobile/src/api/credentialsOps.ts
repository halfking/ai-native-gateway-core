import { req, type RequestOptions } from './client'

// credentialsOps.ts — 凭据节点的**写操作**与**路由检查**（UI规范 17 §2 的
// desktopOnly 让位轮）。本文件是把桌面 web/ 的运维动作按移动端形态裁剪后的
// 契约层，**每个端点的鉴权档位都在注释里写明**，视图层据此决定是否渲染。
//
// ⚠️ 权限矩阵（后端中间件实测，注册处见各处行号）——移动端 UI 按此隐藏入口：
//   admin        = h.admin      → super_admin ✅ / tenant_admin ✅（限本 tenant）
//   superAdmin   = h.superAdmin → super_admin ✅ / tenant_admin ❌ 403
//   结论：**tenant_admin 只能拿到 set/clear-manual-disabled，探测与强制恢复会 403。**
//   所以视图里写操作必须按 role 分档渲染，不能一律显示再吃后端 403。

// ── A. 节点状态修改（manual_disabled）—— admin 档，tenant_admin 可用 ──────────

/**
 * `POST /api/credentials/set-manual-disabled`
 *
 * 后端 admin/credential_monitor.go:1785-1792 强制校验：`reason` 为空串直接 400
 * （"reason required"）。移动端若让用户提交空 reason，请求必定失败且报错信息
 * 对用户无意义 ⇒ 视图层必须在提交前挡住，不要指望后端兜底。
 *
 * 副作用警示：设为 true 会让该凭据**完全退出路由池**、立即停止承载流量。
 * 响应 `{success, message}` —— 注意 `success` 是**业务字段**不是信封。
 */
export interface SetManualDisabledResult {
  success: boolean
  message: string
}

export function setManualDisabled(
  credentialId: number,
  manualDisabled: boolean,
  reason: string,
  options?: RequestOptions,
): Promise<SetManualDisabledResult> {
  return req<SetManualDisabledResult>(
    'POST',
    '/api/credentials/set-manual-disabled',
    { credential_id: credentialId, manual_disabled: manualDisabled, reason },
    options,
  )
}

/**
 * `POST /api/credentials/clear-manual-disabled`（注册 admin/credential_monitor.go:166）
 *
 * reason 同样必填（:1689-1691）。与 set(false) 的差别是这是**专用解除停用**
 * 端点，桌面用它做「凭据已修好，放回路由池」这个语义动作。
 */
export function clearManualDisabled(
  credentialId: number,
  reason: string,
  options?: RequestOptions,
): Promise<SetManualDisabledResult> {
  return req<SetManualDisabledResult>(
    'POST',
    '/api/credentials/clear-manual-disabled',
    { credential_id: credentialId, reason },
    options,
  )
}

// ── B. 凭据检查（探测）—— superAdmin 档 ───────────────────────────────────

/**
 * `POST /api/credentials/{id}/test` —— 单凭据立即重探。
 * 后端 admin/credential_state_handlers.go:174 用 `h.superAdmin` 包装，
 * handler :30-51：提交到 fast queue 后**立刻 202 返回**，探测在后台异步跑。
 * ⇒ 响应 `{message, credential_id, status:'pending'}`，**不是探测结果**。
 * 移动端文案必须说「已提交，探测在后台进行」，不能显示成「探测通过」。
 */
export interface ProbeSubmitResult {
  message: string
  credential_id: number
  status: string
}

export function submitCredentialProbe(credentialId: number, options?: RequestOptions): Promise<ProbeSubmitResult> {
  return req<ProbeSubmitResult>('POST', `/api/credentials/${credentialId}/test`, undefined, options)
}

/**
 * `POST /api/credentials/test-batch` —— 批量快速重探（同样 superAdmin）。
 * 后端硬上限 100 条，超了直接 400 "too many credentials (max 100)"
 * （admin/credential_state_handlers.go:107-110）⇒ 前端先截断，别让请求白跑。
 */
export interface BatchProbeResult {
  message?: string
  submitted?: number
  credential_ids?: number[]
}

export const BATCH_PROBE_MAX = 100

export function submitBatchProbe(credentialIds: number[], options?: RequestOptions): Promise<BatchProbeResult> {
  const ids = credentialIds.slice(0, BATCH_PROBE_MAX)
  return req<BatchProbeResult>('POST', '/api/credentials/test-batch', { credential_ids: ids }, options)
}

// ── C. 强制恢复 —— superAdmin 档，最高危 ───────────────────────────────────

/**
 * `POST /api/admin/diagnostics/credential/force-recover?id={credID}`
 * 注册 admin/handler.go:1461（h.superAdmin），handler admin/diagnostics_credential.go:227。
 *
 * ⚠️ **无请求体，只有 query id** —— 误触即执行，且后端**没有** X-Confirm 二次
 * 确认兜底（对比 PATCH /api/admin/providers/{id}/enable 有该门禁，
 * admin/node_operations.go:398）。二次确认完全是前端责任。
 *
 * 实际执行 5 步状态重置（:243-326）：credentials 状态 → bindings 恢复 →
 * model_probe_state → node_probe_state 清零 → 路由/key 缓存失效。
 * 响应体（:328-335）见下。**必须走 req() 而非裸 fetch** —— 桌面
 * EmergencyDiagnosticModal.vue:122 绕开了 req() 且只发 Bearer 不发 cookie，
 * 移动端不要照抄：用 req() 才有 cookie 鉴权 + 401 bounce + sessionEpoch 代次保护。
 */
export interface ForceRecoverResult {
  triggered: boolean
  credential_id: number
  timestamp: string
  message: string
  key_cache_invalidated: boolean
  key_rotator_reset: boolean
}

export function forceRecoverCredential(credentialId: number, options?: RequestOptions): Promise<ForceRecoverResult> {
  // 走 path 里的 id 之前必须确保是整数：后端 strconv.Atoi 失败即 400。
  if (!Number.isInteger(credentialId) || credentialId <= 0) {
    return Promise.reject(new Error(`invalid credential id: ${credentialId}`))
  }
  return req<ForceRecoverResult>(
    'POST',
    `/api/admin/diagnostics/credential/force-recover?id=${credentialId}`,
    undefined,
    options,
  )
}

/**
 * `POST /api/routing/credentials/{id}/reset-state`（admin/handler.go:946，superAdmin）
 * 聚焦式状态复位，handler admin/routing_reset.go:52。`reason` 必填（:69-72）。
 *
 * 与 force-recover 的分工：那个是「凭据级 5 步全清」，这个是「带 reason 的记账式
 * 复位」，可顺带 trigger_probe。
 *
 * ★★★★★★ 2026-10-07 第四十九轮补：本接口此前**零引用、零测试**，而 TS 接口是
 *   **凭空造的**——对照 `routing_reset.go:146-153` 的真实响应：
 *       writeJSON(w, 200, map[string]any{
 *           "message":         "credential state reset",
 *           "credential_id":   credID,
 *           "raw_model":       req.RawModel,        // ← 空串 = 整凭据复位
 *           "actor":           actor,
 *           "probe_triggered": req.TriggerProbe && h.probeSubmitter != nil,
 *           "details":         beforeAfter,
 *       })
 *   原接口写的 `success` / `reset_fields` **后端一个都不发**；
 *   而 `raw_model` / `actor` / `probe_triggered` / `details` **四个全漏了**。
 *   ⇒ 本次按后端逐字段重写，并补上独立解包（原先是 `req<ResetStateResult>` **零校验**直传）。
 */

/** ★ `details` 里恒存在的 5 个键（`routing_reset.go:96-102` 构造的 beforeAfter）。 */
export const RESET_STATE_DETAIL_REQUIRED_KEYS = [
  'credential_id',
  'raw_model',
  'reason',
  'endpoint',
  'actor',
] as const

/** ★ `details` 里**条件存在**的键：按 `applyForceEnable` 的执行路径才出现（且只在 200 响应里）。 */
export const RESET_STATE_DETAIL_OPTIONAL_KEYS = ['db_committed', 'auto_heal_pairs_submitted', 'audit_outcome'] as const

/** ★ 按后端 `routing_reset.go:146-153` 逐字段重写（旧接口是凭空造的）。 */
export interface ResetStateResult {
  /** 后端写死是字面量 `"credential state reset"`。 */
  message: string
  credential_id: number
  /** ★ 空串 = **整凭据**复位（覆盖该凭据的所有绑定模型）。 */
  raw_model: string
  /**
   * ★★ 审计里的操作者。**回退值是 `r.RemoteAddr`（`routing_reset.go:91-94`）**
   * ⇒ 没有登录态时这里会是**裸 IP**。
   * ★ 对照 free-discovery 的 `fdActor`：那条回退成 `"legacy-admin-key"` ⇒ **同一个仓里两套回退**。
   */
  actor: string
  /**
   * ★★★ 三种含义**分不开**：
   *   ① 请求了 `trigger_probe` 且 submitter 已接线 ⇒ true
   *   ② 请求了但 submitter 为 nil ⇒ **false**（静默忽略，`:124` 的守卫同样判 nil）
   *   ③ 压根没请求 ⇒ false
   * ⇒ ★★ `false` 分不出「没请求」与「请求了但没生效」⇒ **UI 不可承诺「已触发探测」**。
   *   且提交是 fire-and-forget ⇒ **true 也只代表「已提交」，不代表探测成功**。
   */
  probe_triggered: boolean
  /** ★ 异形：`endpoint` 恒为 `"reset-state"`，消费方可按它过滤审计流。 */
  details: ResetStateDetails
}

export interface ResetStateDetails {
  credential_id: number
  raw_model: string
  reason: string
  /** ★ 恒为 `"reset-state"`（`:101`）。 */
  endpoint: string
  actor: string
  /** ★ 条件存在；它为 `true` 只说明 DB 效果已落地（**响应里只有成功那一支带它**）。 */
  db_committed?: boolean
  auto_heal_pairs_submitted?: number
  /**
   * ★★ 只在**部分失败**那一支记进**审计日志**：`"partial_failed"`。
   *   ⚠️ 但那一支以 5xx 结束（`writeInternalErr`），**响应体里没有 details**
   *   ⇒ HTTP 客户端**永远读不到**这个键，它只对审计消费方可见。
   *   客户端判「状态是否可能已改」请用 `resetStateOutcomeAmbiguous(status)`。
   */
  audit_outcome?: string
}

/** ★★ 只有这三个键恒存在（`routing_reset.go:146-153` 的 map 字面量）。 */
export const RESET_STATE_REQUIRED_KEYS = [
  'message',
  'credential_id',
  'raw_model',
  'actor',
  'probe_triggered',
  'details',
] as const

/**
 * ★★★★★★ 客户端复算不出这个恒等式：后端写的是 `req.TriggerProbe && submitter != nil`，
 *   而**客户端不知道 submitter 接没接**。所以 `probe_triggered` 为 false 时无法判断原因。
 */
export function resetStateProbeIndeterminate(r: ResetStateResult, requested: boolean): boolean {
  return requested && r.probe_triggered === false
}

/** ★★ `probe_triggered: true` 只代表「已提交」（fire-and-forget），不代表探测成功。 */
export function resetStateProbeOnlySubmitted(r: ResetStateResult): boolean {
  return r.probe_triggered === true
}

/**
 * ★★★★★★★ 出错时**能不能**说「状态没变」——不能一概而论，按 status 分档。
 *
 * 后端 `routing_reset.go:106-119`：
 * ```go
 * if err := h.applyForceEnable(...); err != nil {
 *     if committed { beforeAfter["audit_outcome"] = "partial_failed"; h.logAudit(...) }
 *     writeInternalErr(w, "internal error (see server logs)", err)   // ← 5xx
 * }
 * ```
 * ★★★ **决定性的一步**：那一支走 `writeInternalErr` → `writeError` →
 *   `{"error":{"detail":"internal error (see server logs)"}}`
 *   ⇒ **`details` / `db_committed` / `audit_outcome` 一个都不在响应里**。
 *   客户端拿到的 5xx 与「DB 完全没动」在报文上**逐字节相同**。
 *   ⇒ 视图在 5xx 时**不得**显示「操作失败，状态未改变」。
 *
 * 两条恰好对称的推论：
 *  · 4xx（400 reason/id、404 not found、405、403、401）**全部**在
 *    `applyForceEnable` 之前就 return 了（:56-89）⇒ 能证明「什么都没发生」。
 *  · ★ `status === 0`（client.ts:172 把传输层失败归一成 `network_error`）
 *    与 `status === undefined`（`EpochError` / 非 ApiError）**同样二义**：
 *    请求可能已经到达服务端并落库，只是回程断了。
 *    ⇒ 「失败 = 没生效」这个直觉在**两个方向**都错。
 */
export function resetStateOutcomeAmbiguous(status: number | undefined): boolean {
  if (status == null) return true
  return status >= 500 || status === 0
}

/** ★ `actor` 是裸 IP 而不是用户名 ⇒ 这条审计是「无登录态」记的。 */
export function resetStateActorLooksLikeIp(actor: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(actor) || actor.startsWith('[')
}

/** ★ `raw_model` 为空串 = 整凭据复位（`routing_reset.go:37-39` 注释）。 */
export function resetStateIsWholeCredential(r: ResetStateResult): boolean {
  return r.raw_model === ''
}

/** ★★ 没有请求体时 `readJSON` 返回 nil（`handler.go:1518-1520`）⇒ req 全零值 ⇒ reason 为空 ⇒ 400。 */
export function resetStateReasonMissing(reason: string): boolean {
  return reason === ''
}

export function resetCredentialState(
  credentialId: number,
  reason: string,
  rawModel = '',
  triggerProbe = false,
  options?: RequestOptions,
): Promise<ResetStateResult> {
  // ★ 后端 `strconv.Atoi` 失败或 <= 0 ⇒ 400 `id path param must be a positive integer`。
  // ★ `reason` 空 ⇒ 400 `reason is required for audit trail`（`:69-72`）⇒ 前端先挡一道。
  if (!Number.isInteger(credentialId) || credentialId <= 0) {
    return Promise.reject(new Error(`id path param must be a positive integer: ${credentialId}`))
  }
  if (resetStateReasonMissing(reason)) {
    return Promise.reject(new Error('reason is required for audit trail'))
  }
  return req<unknown>(
    'POST',
    `/api/routing/credentials/${credentialId}/reset-state`,
    { reason, raw_model: rawModel, trigger_probe: triggerProbe },
    options,
  ).then(unwrapResetState)
}

/** ★ 按后端逐字段校验；**不**接受 `success` / `reset_fields`（后端不发）。 */
export function unwrapResetState(resp: unknown): ResetStateResult {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const missing = RESET_STATE_REQUIRED_KEYS.filter((k) => !(k in m))
    if (missing.length > 0) {
      throw new Error(`凭据状态复位 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
    if (
      typeof m.message !== 'string' ||
      typeof m.credential_id !== 'number' ||
      typeof m.raw_model !== 'string' ||
      typeof m.actor !== 'string' ||
      typeof m.probe_triggered !== 'boolean' ||
      !m.details ||
      typeof m.details !== 'object' ||
      Array.isArray(m.details)
    ) {
      throw new Error('凭据状态复位 响应形状不符：键类型不对')
    }
    const d = m.details as Record<string, unknown>
    const dMissing = RESET_STATE_DETAIL_REQUIRED_KEYS.filter((k) => !(k in d))
    if (dMissing.length > 0) {
      throw new Error(`凭据状态复位 details 形状不符：缺 ${dMissing.length} 个键（${dMissing.join(', ')}）`)
    }
    return m as unknown as ResetStateResult
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`凭据状态复位 响应形状不符：期望 6 键对象，实得 ${actual}`)
}

// ── D. 路由检查（只读）—— admin 档，tenant_admin 也可用 ───────────────────

/**
 * `GET /api/routing/resolve?model=X` —— 路由决策 explain。
 * 注册 admin/handler.go:929（h.admin），handler admin/routing.go:266。
 *
 * 这是移动端**最有价值的新增只读面**：回答「这个模型现在会被路由到哪、为什么
 * 不是别的凭据」。后端特意**始终返回全部候选**并带上不可用原因
 * （routing.go:273-275 注释），所以移动端能显示被阻塞的候选而不只是可用者。
 *
 * ⚠️ model 缺失 → 400（routing.go:272-275）。model 无变体时返回一个
 * 空 candidates 的合法对象（:288-296），**不是错误** —— 视图要显示
 * 「无匹配候选」而不是错误态。
 */
export interface RoutingCandidate {
  rank: number
  provider_id: number
  provider_name: string
  catalog_code: string
  protocol: string
  credential_id: number
  credential_label: string
  credential_status: string
  lifecycle_status: string | null
  availability_state: string | null
  /** 后端 SQL admin/routing.go:317 取 `c.availability_recover_at::text` —— 熔断/不可用的恢复时刻。 */
  availability_recover_at: string | null
  quota_state: string | null
  quota_recover_at?: string | null
  concurrency_limit: number | null
  effective_concurrency: number | null
  circuit_state: 'closed' | 'open' | 'half_open' | null
  available: boolean
  tier: number
  weight: number
  success_rate: number
  p95_latency_ms: number
  model_name?: string
  block_reason?: string | null
}

export interface RoutingResolveResponse {
  client_model: string
  canonical_name: string | null
  canonical_id: number | null
  resolution_path: string
  raw_models: string[]
  plan_order: Array<{ credential_id: number; provider_id: number; raw_model: string; tier: number }>
  candidates: RoutingCandidate[]
}

export function resolveRouting(
  model: string,
  options?: RequestOptions,
): Promise<RoutingResolveResponse> {
  const qs = new URLSearchParams({ model })
  return req<RoutingResolveResponse>('GET', `/api/routing/resolve?${qs}`, undefined, options)
}

/** `GET /api/routing/score-details?model=X`（admin/handler.go:1209）—— 打分明细。 */
export interface ScoreDetailsResponse {
  model: string
  total_score?: number
  dimensions?: Array<{ name: string; score: number; weight?: number; detail?: string }>
  [k: string]: unknown
}

export function fetchScoreDetails(model: string, options?: RequestOptions): Promise<ScoreDetailsResponse> {
  const qs = new URLSearchParams({ model })
  return req<ScoreDetailsResponse>('GET', `/api/routing/score-details?${qs}`, undefined, options)
}

/**
 * `GET /api/credentials/decisions?credential_id=&limit=&model=`
 * 注册 admin/credential_monitor.go:165，handler :1605。
 *
 * 回答「这个凭据最近在承载什么流量」——节点详情的只读补充面。
 * limit 默认 50，>200 归 50（:1631-1634）；tenant_admin 被限定在本 tenant（:1641-1649）。
 * 返回 `{credential_id, decisions, total}` 信封（:1658-1662）。
 */
export interface CredentialRoutingDecision {
  ts: string
  request_id: string
  model: string
  tier: number | null
  success: boolean
  latency_ms: number | null
  error_class: string | null
  chosen_provider_id: number | null
  client_model: string | null
  outbound_model: string | null
  sticky_hit: boolean | null
}

export interface CredentialDecisionsResponse {
  credential_id: number
  decisions: CredentialRoutingDecision[]
  total: number
}

/**
 * 2026-10-06：这里**不做**形状猜测。后端 :1658 明确写
 * `writeJSON(w, 200, {credential_id, decisions, total})` —— 是信封不是裸数组。
 * 但保留 unwrapDecisions 的双形态容忍：{decisions:[]} 取值，两个都不符**抛错**，
 * 立场与 nodes.ts 的 unwrapMonitorSummary 一致 ——「没数据」和「解包失败」
 * 不能长得一模一样，否则一次契约漂移会被读成「这个节点最近没流量」。
 */
export function fetchCredentialDecisions(
  credentialId: number,
  params?: { limit?: number; model?: string },
  options?: RequestOptions,
): Promise<CredentialRoutingDecision[]> {
  const qs = new URLSearchParams({ credential_id: String(credentialId) })
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.model) qs.set('model', params.model)
  return req<CredentialDecisionsResponse | CredentialRoutingDecision[]>(
    'GET',
    `/api/credentials/decisions?${qs}`,
    undefined,
    options,
  ).then(unwrapDecisions)
}

export function unwrapDecisions(
  resp: CredentialDecisionsResponse | CredentialRoutingDecision[],
): CredentialRoutingDecision[] {
  if (Array.isArray(resp)) return resp
  if (resp && typeof resp === 'object' && Array.isArray(resp.decisions)) return resp.decisions
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`decisions 响应形状不符：期望 {decisions:[…]}，实得 ${actual}`)
}
