// errorSurface.ts — 用户可见错误文案的**唯一出口**（2026-10-27，第 16 增量）
//
// ★ 要治的病（实测，不是推演）：
//   全仓 196 处 `error.value = e instanceof Error ? e.message : <兜底>`（95 个文件）。
//   三元把 `.message` 放在**第一分支** ⇒ 兜底只在【抛出的值不是 Error 实例】时才触发
//   ⇒ 本地化兜底**结构性失效** ⇒ 用户实际看到的是 `e.message`，
//      也就是后端下发的**英文技术串**
//      （实测：admin 侧 1852 处 `writeError(...)` 硬编码错误文案，去重 631 条，
//        其中仅 33 条中文 ⇒ 98% 是英文）。
//   ⇒ 第 15 增量改写的 143 个语言包键，在这条主路径上**根本没上过屏**。
//
// ★ 三问（impeccable skill/reference/clarify.md「An actionable error answers」）：
//   1. what failed    → **主文案**：主体专属键（点名主体）+ 恢复动作
//   2. why            → **原因**：原始 detail（**次要**，永不作主文案）
//   3. how to recover → 主文案自带（「请重试」等，8 语种都写在语言包里）
//
// ★ 为什么原因要过滤掉一批：
//   · HTTP 的 statusText（"Internal Server Error" / "Bad Request" …）不是原因，
//     是**内部措辞** ⇒ impeccable 明令「Do not expose internal codes as the primary
//     message」⇒ 它连「次要原因」都不该占位。
//   · 纯状态码（"500"）与内部错误码（"invalid_request_error"）同理。
//   · 与主文案重复的（原因已写进主文案）不重复显示。
//
// ★ 组成顺序（**唯一一份实现**）：
//   `主文案` + `（原因：xxx）`
//   括号里是**次要**信息 ⇒ 主文案永远是那句人能直接行动的话。
//
// ★ 状态层（没有主体专属键时）：
//   `errors.byStatus.<status>` / `errors.network` / `errors.unknown`
//   —— 这三个族原先是**死键**（`useApiError.ts` 零调用 ⇒ `errors.ts` 的 96 个值
//   全语种 0 引用）⇒ 本模块把它们**复活**成「没有主体文案时的兜底层」。
//
// ★ 为什么不用 `useApiError.ts`（`resolveApiError`）：
//   那条链是死代码，且它的口径是「**状态码 → 一句状态文案**」，
//   **没有主体、也没有恢复动作** ⇒ 不满足三问。
//   本模块是它的**替代**，不是它的包装 ⇒ 旧链保持死链（本轮不删，避免动别的工程）。

import { i18n } from './index'

export interface SurfaceErrorOptions {
  /**
   * 主体专属错误文案键（含「什么失败 + 怎么恢复」）。
   * 省略 ⇒ 走**状态层**（`errors.byStatus.*` / `errors.network` / `errors.unknown`）。
   */
  key?: string
  /** 是否把原始技术细节作为原因附在主文案后面。默认 true。 */
  withReason?: boolean
}

/**
 * HTTP 标准 statusText —— **不是原因，是内部措辞**，不该上屏。
 *
 * ★★ 第一版把它当**一张全局黑名单**用 ⇒ 误杀了后端真实的理由：
 *   后端 body 里就写着 "not found" / "forbidden"（"这个供应商 not found"），
 *   那是**有用的原因** ⇒ 被丢掉后用户只看到「资源未找到」而不知道为什么。
 *   ⇒ 正确口径：**只有当这段文字恰好是该错误自己那个状态码的 statusText** 时，
 *     它才可能是「fetch 没读到 body、拿 statusText 顶包」的产物 ⇒ 才丢。
 *   ⇒ 其余情况（有别的状态码、或压根没有状态码）⇒ 保留。
 */
const STATUS_TEXT_FOR_STATUS: Record<number, string> = {
  400: 'bad request',
  401: 'unauthorized',
  403: 'forbidden',
  404: 'not found',
  405: 'method not allowed',
  408: 'request timeout',
  409: 'conflict',
  410: 'gone',
  429: 'too many requests',
  500: 'internal server error',
  502: 'bad gateway',
  503: 'service unavailable',
  504: 'gateway timeout',
}

/** 原因文本长度上限（后端可能回一整段 body）。 */
const MAX_REASON = 200

/** 从各种形状的错误里取出「原始 detail」。 */
function candidateOf(err: unknown): string {
  if (typeof err === 'string') return err
  if (!err || typeof err !== 'object') return ''
  const e = err as Record<string, unknown>
  if (typeof e.detail === 'string' && e.detail) return e.detail
  if (typeof e.message === 'string' && e.message) return e.message
  const resp = e.response
  if (resp && typeof resp === 'object') {
    const d = (resp as Record<string, unknown>).data
    if (typeof d === 'string' && d) return d
    if (d && typeof d === 'object') {
      const dd = d as Record<string, unknown>
      if (typeof dd.error === 'string' && dd.error) return dd.error
      const de = dd.error
      if (de && typeof de === 'object') {
        const dm = (de as Record<string, unknown>).message
        if (typeof dm === 'string' && dm) return dm
      }
      if (typeof dd.message === 'string' && dd.message) return dd.message
    }
  }
  if (typeof e.statusText === 'string' && e.statusText) return e.statusText
  return ''
}

/** HTTP 状态码（ApiError.status / err.status / err.response.status）。 */
export function statusOf(err: unknown): number {
  if (!err || typeof err !== 'object') return 0
  const e = err as Record<string, unknown>
  const direct = e.status
  if (typeof direct === 'number' && direct >= 100 && direct < 600) return direct
  const resp = e.response
  if (resp && typeof resp === 'object') {
    const s = (resp as Record<string, unknown>).status
    if (typeof s === 'number' && s >= 100 && s < 600) return s
  }
  return 0
}

/** 网络层失败（fetch 抛的 TypeError）。 */
function isNetworkError(err: unknown): boolean {
  if (!(err instanceof TypeError)) return false
  const m = typeof err.message === 'string' ? err.message : ''
  return /fetch|network/i.test(m)
}

/**
 * 取译文；**缺键时返回空串**（vue-i18n 缺键会返回键路径本身，
 * 而本仓 `missingWarn:false` ⇒ 静默返回 ⇒ 不做这层判定就会把
 * 「some.key」当成译文上屏）。
 *
 * ★⚠ **无参 `t()` 会把 `{name}` 占位符吃掉**（vue-i18n 把它当命名插值，
 *   没传参 ⇒ 渲染成空串）⇒ 「（原因：{reason}）」经 `t()` 会变成「（原因：）」。
 *   ⇒ 需要保留占位符原样的场合（原因后缀）必须走 `rawMessage()`，**不能**用本函数。
 */
function resolveMessage(key: string): string {
  if (!key) return ''
  const v = i18n.global.t(key)
  const s = typeof v === 'string' ? v : String(v ?? '')
  return s === key ? '' : s
}

/**
 * 取**未经插值**的译文（保留 `{name}` 占位符原样）。
 *
 * ★ 为什么需要它：vue-i18n 的 `t()` 会把 `{reason}` 当命名插值吃掉
 *   （无参调用 ⇒ 渲染成空串）⇒ 原因后缀「（原因：{reason}）」经 `t()` 会变成
 *   「（原因：）」⇒ 出口就分不清「后缀有没有占位符」⇒ 原因会被错拼到末尾。
 *   ⇒ 这里直接从当前语种的**消息树**里取原文。
 */
export function rawMessage(key: string): string {
  if (!key) return ''
  const code = (i18n.global.locale as unknown as { value: string }).value
  for (const c of [code, 'en']) {
    const root = i18n.global.getLocaleMessage(c) as Record<string, unknown> | undefined
    let cur: unknown = root
    for (const seg of key.split('.')) {
      if (!cur || typeof cur !== 'object') { cur = undefined; break }
      cur = (cur as Record<string, unknown>)[seg]
    }
    if (typeof cur === 'string') return cur
  }
  return ''
}

/**
 * 状态层键：有状态码按状态码，否则网络错误，再否则未知。
 * ★ 该状态码在**当前语种**没有译文 ⇒ 退回 `errors.unknown`（不显示键路径）。
 */
function statusTierKey(err: unknown): string {
  if (isNetworkError(err)) {
    if (resolveMessage('errors.network')) return 'errors.network'
  }
  const s = statusOf(err)
  if (s) {
    const k = `errors.byStatus.${s}`
    if (resolveMessage(k)) return k
  }
  return 'errors.unknown'
}

/**
 * 这段原始 detail 能不能作为「次要原因」上屏。
 *
 * @param raw    原始 detail
 * @param primary 主文案（避免重复）
 * @param status 该错误自己的 HTTP 状态码（0 = 没有）
 */
export function isInformativeReason(raw: string, primary = '', status = 0): boolean {
  const s = String(raw ?? '').trim()
  if (!s) return false
  // ★ 只有「恰好是该状态码自己的 statusText」才当噪声丢（见 STATUS_TEXT_FOR_STATUS 的注释）。
  if (status && STATUS_TEXT_FOR_STATUS[status] === s.toLowerCase()) return false
  if (/^\d{3}$/.test(s)) return false // 纯状态码
  if (/^[A-Z][A-Z0-9_]{2,}$/.test(s)) return false // SCREAMING_SNAKE 内部码
  if (primary && s === primary) return false
  return true
}

/** 截断过长的 detail（后端可能回一整段 body）。 */
function truncate(s: string): string {
  return s.length > MAX_REASON ? `${s.slice(0, MAX_REASON)}…` : s
}

/**
 * **用户可见错误文案的唯一出口。**
 *
 * @param err  捕获到的错误（任意形状）
 * @param opts 主文案键 / 是否附原因
 * @returns 该语种的错误文案
 */
export function surfaceError(err: unknown, opts: SurfaceErrorOptions = {}): string {
  const withReason = opts.withReason !== false

  // ① 主文案：主体专属键 → 状态层。
  let primary = ''
  if (opts.key) primary = resolveMessage(opts.key)
  if (!primary) primary = resolveMessage(statusTierKey(err))
  if (!primary) primary = resolveMessage('errors.unknown')
  if (!primary) return ''

  if (!withReason) return primary

  // ② 原因：原始 detail，作为次要信息附在主文案后面。
  const raw = truncate(candidateOf(err).trim())
  if (!isInformativeReason(raw, primary, statusOf(err))) return primary

  // 后缀必须取**原文**（保留 `{reason}` 占位符）⇒ 见 rawMessage 的注释。
  const suffix = rawMessage('common.feedback.reasonSuffix')
  if (!suffix) return primary
  const rendered = suffix.includes('{reason}')
    ? suffix.replace('{reason}', raw)
    : `${suffix}${raw}`
  return `${primary}${rendered}`
}
