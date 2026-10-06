import { req, type RequestOptions } from './client'

// credentialWriteOps.ts — 凭据的「改配置」类写操作。
//   POST /api/credentials/promote                手动提升为 ready
//   POST /api/credentials/demote                  手动降级为 cooling + 定时自愈
//   POST /api/credentials/set-concurrency-auto     设自动并发上限
//   POST /api/credentials/model-toggle             单个 (凭据, 模型) 绑定上/下线
//
// 鉴权：四条都在 `monitorH.RegisterMonitorRoutes(mux, h.admin)` 下
//（credential_monitor.go:158-162）⇒ tenant_admin 可用。
//
// 与已有的 credentialsOps.ts 不重复：那边是 set/clear-manual-disabled
// （整体停用/恢复）、probe 提交、force-recover、routing resolve；
// 这里是**调参**类（提升/降级/并发/模型绑定开关）。
//
// ───────────────────────────────────────────────────────────────────────────
// ★★ 本组端点最反直觉的一处：**reason 的强制性在各端点之间不一致**
//
//   model-toggle   reason **必填**且 ≤500 字（validateModelToggleRequest,
//                 credential_monitor.go:1069-1080），空串 400
//   promote        reason **不校验**（:857-890）—— 空串会被写进
//                 state_reason_detail = "manual_promote: " + reason，
//                 得到一个悬空的 "manual_promote: "
//   demote         reason 不校验（:910-960）
//   set-concurrency-auto  reason 不校验（:971-1030）
//
//   四个都会写 auditLog。所以「后端不要求」并不等于「可以不填」——
//   空 reason 会让审计日志失去意义，且 promote 那条会落一个残串进状态详情。
//
// ⇒ **移动端一律强制要求 reason**，不管后端校验不校验。
//   理由不是"对齐后端"，而是：这几个操作会改变生产路由面，
//   事后没人能说清「谁在什么时候因为什么把它降了级」。
//   这与 17 §11.1 对凭据操作区定的规矩同源（写操作必须带可追溯的理由）。
//
// 另一处不对称：demote 的 `recover_after_hours` **传 0 会被改成 2**
//（:930-932），不是报错。所以前端显式发值，不靠后端默认——
// 界面显示「2 小时后恢复」就必须真的发 2。

/** reason 上限（model-toggle 的 400 阈值）。其余三个不校验，但统一按此限制。 */
export const WRITE_REASON_MAX = 500

export interface SimpleWriteResult {
  success: boolean
  message: string
  [k: string]: unknown
}

/**
 * 校验 reason。**返回错误文案而不是抛异常**，让 UI 能就地展示在输入框下。
 * 与 model-toggle 的后端规则逐条对齐（空 ⇒ 400，超长 ⇒ 400）。
 */
export function validateReason(reason: string): string | null {
  const r = (reason ?? '').trim()
  if (r === '') return 'reasonRequired'
  if (r.length > WRITE_REASON_MAX) return 'reasonTooLong'
  return null
}

/** 手动提升：availability_state='ready' 并清 recover_at（:878-886）。 */
export function promoteCredential(
  credentialId: number,
  reason: string,
  options?: RequestOptions,
): Promise<SimpleWriteResult> {
  return req<SimpleWriteResult>(
    'POST',
    '/api/credentials/promote',
    { credential_id: credentialId, reason: (reason ?? '').trim() },
    options,
  )
}

/**
 * 手动降级：进 cooling 并设定自愈时间。
 *
 * ⚠️ `recoverAfterHours` 不传或传 0 ⇒ 后端**默默改成 2 小时**（:930-932）。
 *   所以这里显式发值：界面上写「几小时后恢复」就必须真的发几小时。
 */
export function demoteCredential(
  credentialId: number,
  reason: string,
  recoverAfterHours: number,
  options?: RequestOptions,
): Promise<SimpleWriteResult> {
  const h = Number.isFinite(recoverAfterHours) && recoverAfterHours > 0 ? recoverAfterHours : 2
  return req<SimpleWriteResult>(
    'POST',
    '/api/credentials/demote',
    { credential_id: credentialId, reason: (reason ?? '').trim(), recover_after_hours: h },
    options,
  )
}

/**
 * 设自动并发上限。
 *
 * ⚠️ `concurrency_limit_auto < 1` ⇒ 400（:993-996）。这是本组**唯一**
 *   有真值域校验的数值参数，其余的越界都被后端悄悄改掉。
 */
export function setConcurrencyAuto(
  credentialId: number,
  concurrencyLimitAuto: number,
  reason: string,
  options?: RequestOptions,
): Promise<SimpleWriteResult> {
  return req<SimpleWriteResult>(
    'POST',
    '/api/credentials/set-concurrency-auto',
    {
      credential_id: credentialId,
      concurrency_limit_auto: Math.trunc(concurrencyLimitAuto),
      reason: (reason ?? '').trim(),
    },
    options,
  )
}

export type ModelToggleAction = 'online' | 'offline'

/**
 * ★ 只有 `manual_offline` 这个 reason 能被切回 online。
 *
 * 后端刻意拒绝让操作员覆盖自动判定（credential_monitor.go 的 409 分支：
 * "only manual_offline can be toggled back to online"）—— 像
 * `model_probe_broken` 这类由探测共识持有的状态，只能由探测翻转。
 *
 * ⇒ **UI 不能把「上线」做成随手可点**：对任何非 manual_offline 的绑定
 *   都必然 409。与其让用户点了吃一个 409，不如按 `unavailable_reason`
 *   决定是否提供该动作。
 */
export const MANUAL_OFFLINE_REASON = 'manual_offline'

/** 该绑定当前是否允许切回 online。 */
export function canToggleOnline(currentReason: string | null | undefined): boolean {
  return (currentReason ?? '') === MANUAL_OFFLINE_REASON
}

/**
 * model-toggle 的响应（ModelToggleResponse, credential_monitor.go:1056-1063）。
 *
 * 它比其它三个端点**多回传了变更前后的状态**，所以不能当 SimpleWriteResult 用：
 * 界面要靠 `prev_available` / `prev_reason` 告诉用户「刚才它还是可用的」，
 * 否则用户不知道自己刚改掉了什么。
 */
export interface ModelToggleResult {
  success: boolean
  available: boolean
  unavailable_reason?: string | null
  prev_available: boolean
  prev_reason?: string | null
  action: ModelToggleAction
}

/**
 * 单个 (凭据, 模型) 绑定上/下线。
 *
 * ⚠️ `raw_model_name` 必须与 `credential_model_bindings` 里的**原样**匹配
 *   （后端按 raw_model_name 查 `provider_models` + `credential_model_bindings`）。
 *   发规范化后的名字会命中不了 —— 那是 404 `binding not found`
 *   （credential_monitor.go 的 `pgx.ErrNoRows` 分支），不是 200。
 *   ⚠️ 但 404 与「这个凭据确实没绑这个模型」在移动端无法区分，
 *      所以调用方**必须**把后端返回的原始名原样传回，不要做 normalize ——
 *      否则用户会看到一句「切换失败」而真实原因是前端改了名字。
 */
export function toggleCredentialModel(
  credentialId: number,
  rawModelName: string,
  action: ModelToggleAction,
  reason: string,
  options?: RequestOptions,
): Promise<ModelToggleResult> {
  return req<ModelToggleResult>(
    'POST',
    '/api/credentials/model-toggle',
    {
      credential_id: credentialId,
      // ★ 原样透传，不规范化（见函数注释）
      raw_model_name: rawModelName,
      action,
      reason: (reason ?? '').trim(),
    },
    options,
  )
}

/** 从 ModelToggleResult 派生一句人话，用于成功反馈。 */
export function describeToggle(r: ModelToggleResult): string {
  const to = r.available ? 'online' : 'offline'
  if (r.prev_available === r.available) return to
  return `${r.prev_available ? 'online' : 'offline'} → ${to}`
}
