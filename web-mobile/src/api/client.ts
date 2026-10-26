import { acceptLanguageHeader } from '@/i18n'
import { getTransport, gatewayBaseUrl, transportUsesCookieJar, type TransportResponse } from './transport'

// client.ts — web-mobile 请求管线（复刻网关 web/src/api/_core.ts 语义，
// UI规范 17 §5）：cookie 优先（credentials same-origin），无 userInfo 才补
// Authorization: Bearer；401 → replace 到 /login?redirect=（06 §4）；
// AbortSignal 全链路透传；sessionEpoch 代次校验（17 §4-R2）。

export interface UserInfo {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  must_change_password?: boolean
  /**
   * ★★ 这两个字段后端**一直都在发**（`admin/users.go:20-32` 的 `userInfo`
   *   是 10 字段的完整结构体），先前这个 TS 接口漏了它们。
   * ★ `LastLoginAt *time.Time` **没有 omitempty** ⇒ 键**恒存在**，
   *   从没登录过时是 **`null`**（不是缺键、不是空串）。
   * ★ 声明成**可选**而不是必填：仓库里有 9 处 spec 只构造前 8 个字段，
   *   设成必填会把它们全打破（改共享类型的必填性，影响面远大于触发它的那一处）。
   */
  last_login_at?: string | null
  created_at?: string
}

export interface AuthContext {
  getUserInfo(): UserInfo | null
  getBearer(): string
  isAuthenticated(): boolean
}

let authContext: AuthContext = {
  getUserInfo: () => null,
  getBearer: () => '',
  isAuthenticated: () => false,
}

export function bindAuthContext(ctx: AuthContext): void {
  authContext = ctx
}

// ---- sessionEpoch（17 §4-R2：登录/登出/换号递增） ----

let sessionEpoch = 0

export function bumpSessionEpoch(): void {
  sessionEpoch++
}

export function currentSessionEpoch(): number {
  return sessionEpoch
}

/** 代次已变（登录态切换）——调用方按 abort 语义静默丢弃，不进 UI。 */
export class EpochError extends Error {
  constructor() {
    super('session epoch changed')
    this.name = 'EpochError'
  }
}

export function isEpochError(err: unknown): boolean {
  return !!err && typeof err === 'object' && (err as { name?: unknown }).name === 'EpochError'
}

export function isAbortError(err: unknown): boolean {
  return !!err && typeof err === 'object' && (err as { name?: unknown }).name === 'AbortError'
}

/**
 * 2026-10-22：维护态信封识别。部署版服务端在维护窗口把部分管理面请求
 * （实测 /api/auth/me 会话水合）打成 {"code":"maintenance_mode"}。这类失败
 * **不是会话问题也不是凭据问题**：会话探针不应据此踢登录，登录页应展示
 * 可重试提示。取证：llm-gateway-client 仓 .maestro/evidence/2026-10-22-6s-real/
 * SERVICE-RELIABILITY-REPORT.md §3。
 */
export function isMaintenanceMode(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false
  const code = err.code
  if (!code) return false
  // 2026-10-22 审计修正：维护态信封是**一族**，不是一个字面量。
  //   - 部署版边缘组件下发 {"code":"maintenance_mode"}（2026-10-22 实测）
  //   - 本仓 maintain_proxy 下发 {"code":"maintain.*"}
  //     （cmd/gateway/maintain_proxy.go 的 maintain.upstream_unavailable /
  //      maintain.proxy_unavailable / maintain.not_configured）
  // 只认一个字面量 ⇒ 服务端换个码就静默退回「踢登录」，
  // 正是本函数要治的「维护态误杀会话」原样复发。
  // ⇒ 判据放宽为**信封族**；宁可放过一次真维护态，也不误杀一个好会话。
  return code === 'maintenance_mode' || code.startsWith('maintain.')
}

// ---- ApiError ----

export class ApiError extends Error {
  status: number
  detail: string
  /**
   * 后端 `writeErrorWithCode` 下发的业务错误码（如登录验证码的
   * `captcha_required` / `captcha_invalid`，见 admin/auth_captcha.go）。
   *
   * 为什么必须单列一个字段：光看 `detail` 文案去分支，等于把 UI 行为
   * 绑在一句**可能被改写的**人类可读文本上——文案一改，登录页就会安静地
   * 退回「用户名或密码错误」，而验证码框还在原地。
   * 未携带 code 的后端错误一律为空串，旧调用点不受影响。
   */
  code: string
  /**
   * 401 的**会话裁决结果**（`sessionReallyDead` 的探针结论）。
   *
   * 三态而不是布尔：
   *   · `undefined` —— 本次没探（已在登录页 / 非 `/api/` / 单飞闩锁已占用）。
   *     「没探」既可能是会话真的死了，也可能是压根不适用，**不可当成 false**。
   *   · `true`  —— 探针 `/api/auth/me` 成功 ⇒ 会话是好的，这个 401 是**端点自己拒**
   *     （典型：feature flag 关闭 ⇒ `/api/admin/*` 未注册，后端仍回 401）。
   *   · `false` —— 探针也失败 ⇒ 会话确实死了。
   *
   * 为什么必须随错误传下去：`formatApiError` 原本把**任何** 401 说成
   * 「登录状态已过期，请重新登录」—— 与本文件修复前的踢登录是**同一个错误假设**。
   * 修完跳转后实测（2026-10-13，245）：用户确实不再被踢回登录页，但页面级
   * 错误态仍在说「登录状态已过期」，而同一时刻探针刚返回 200 ⇒ 文案与事实相反。
   */
  sessionAlive?: boolean
  constructor(status: number, detail: string, code = '', sessionAlive?: boolean) {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.code = code
    if (sessionAlive !== undefined) this.sessionAlive = sessionAlive
  }
}

/** 从错误响应体里取 `error.code`（取不到就是空串）。 */
function errorCode(text: string): string {
  if (!text) return ''
  try {
    const j = JSON.parse(text) as Record<string, unknown>
    const err = j.error
    if (err && typeof err === 'object') {
      const code = (err as Record<string, unknown>).code
      if (typeof code === 'string') return code
    }
    // 2026-10-22：边缘/维护组件下发的顶层 {code,message,request_id} 信封
    // （无 .error 包装，实测形态 {"code":"maintenance_mode",...}）也要提出
    // code —— 否则所有按 code 分支的 UI（登录页、会话探针）都认不出维护态。
    if (typeof j.code === 'string' && j.code) return j.code
  } catch {
    /* 非 JSON 错误体：没有 code */
  }
  return ''
}

// ---- 401 处理 ----

let unauthorizedHandler: ((currentPath: string) => void) | null = null
let redirectStarted = false
/** 会话探针单飞：并发 401 只探一次 /api/auth/me（见 sessionReallyDead）。 */
let sessionProbe: Promise<boolean> | null = null

export function setUnauthorizedHandler(fn: (currentPath: string) => void): void {
  unauthorizedHandler = fn
}

/** 测试复位：401 bounce 单飞闩锁 + 会话探针单飞。 */
export function resetUnauthorizedForTests(): void {
  redirectStarted = false
  sessionProbe = null
}

function isAuthProbePath(path: string): boolean {
  return path === '/api/auth/me' || path === '/api/auth/token' || path === '/api/auth/logout'
}

/**
 * ★ 401 **不等于**会话过期 —— 踢回登录页之前必须先确认（2026-10-12 实测）。
 *
 * 后端把「端点没注册」和「你没带 token」返回成**逐字节相同**的响应：
 *   未注册 `/api/admin/*`（feature flag 关 ⇒ 路由不注册，落到 `mux.Handle("/api/admin/", e)`，
 *   而 `e.Use(jwtMiddleware)` 是全局中间件、在路由之前跑）
 *     → 401 `{"error":"authentication required"}` 36 字节
 *   已注册端点无 token
 *     → 401 `{"error":{"detail":"authentication required"}}` 47 字节
 *   已注册端点无 token 打一个**明显不存在**的路径，得到的与第一条**完全一致**
 *     ⇒ 「端点不存在」与「未认证」在响应上不可区分。
 *
 * 代价是实测到的：用户进「费用对账」页（flag 默认关闭 ⇒ 端点未注册），
 * 那一个 401 就把**完全有效**的会话整页踢回登录页
 * （同会话 9 秒前 `/api/auth/me` 与 `/api/admin/dashboard/board` 都是 200）。
 *
 * 所以这里在动 `unauthorizedHandler` 之前先探一次 `/api/auth/me`：
 *   · 探针也 401/失败 ⇒ 会话确实死了 ⇒ 照旧踢登录（**保留吊销后的强制登出**）
 *   · 探针成功     ⇒ 会话是好的，是这个端点自己拒 ⇒ **不踢**，按 ApiError(401) 走页面级错误态
 * 探针走 `/api/auth/me`（isAuthProbePath 内）⇒ 不会递归回本函数。
 * 探针单飞：并发 401 只多打一次探针。
 *
 * ⚠️ EpochError 不等于「会话死了」——它表示**另一处正在换 epoch**（登录/登出），
 *   那种情况下主动导航本身就是错的，所以一律不踢。
 */
async function sessionReallyDead(): Promise<boolean> {
  if (!sessionProbe) {
    sessionProbe = (async () => {
      try {
        await req('GET', '/api/auth/me')
        return false
      } catch (err) {
        // 维护态下探针失败 ≠ 会话死亡（2026-10-22 实测部署版服务端会把
        // /api/auth/me 打成 maintenance_mode）：踢登录会把好会话误杀。
        if (isEpochError(err) || isMaintenanceMode(err)) return false
        // 2026-10-22 审计修正：「没探成」≠「探死了」。
        // 探针失败的形态分三类，只有第三类才携带「会话已失效」的证据：
        //   ① epoch 变了（换号）       ⇒ 不判死（已在上面对齐）
        //   ② 维护态信封               ⇒ 不判死（已在上面对齐）
        //   ③ AbortError（请求被取消）/ 非 ApiError（如响应体不是 JSON
        //      触发的 SyntaxError）   ⇒ **没有任何证据**说会话死了
        // 判死需要**明确的认证失败信号**；「探针抛了异常」不是证据。
        // 旧实现把 ③ 也判成死亡 ⇒ 维护页返回 2xx+HTML 时「登录页↔维护页」死循环。
        if (isAbortError(err)) return false
        if (!(err instanceof ApiError)) return false
        return true
      } finally {
        // 结论只对这一轮有效：下一次 401 必须重新探，不能永久粘住。
        sessionProbe = null
      }
    })()
  }
  return sessionProbe
}

function errorMessage(statusText: string, text: string): string {
  if (!text) return statusText
  try {
    const j = JSON.parse(text) as Record<string, unknown>
    const err = j.error
    if (typeof err === 'string') return err
    if (err && typeof err === 'object') {
      const msg = (err as Record<string, unknown>).message
      const detail = (err as Record<string, unknown>).detail
      if (typeof msg === 'string') return msg
      if (typeof detail === 'string') return detail
    }
    if (typeof j.detail === 'string') return j.detail
    // 顶层 {code,message} 信封（维护态等边缘组件形态）：message 是人类可读文案
    if (typeof j.message === 'string' && j.message) return j.message
    return text
  } catch {
    return text
  }
}

export interface RequestOptions {
  signal?: AbortSignal
}

export async function req<T>(method: string, path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  const epochAtStart = sessionEpoch
  const hasBody = body !== undefined
  const headers: Record<string, string> = {}
  if (hasBody) headers['Content-Type'] = 'application/json'
  headers['Accept-Language'] = acceptLanguageHeader()

  // cookie 优先：userInfo 存在 = JWT 登录成功后端已设 HttpOnly llmgw_session，
  // 内存 Bearer 不携带以免遮蔽其它标签页更新的 cookie（web/_core.ts 同源裁决）
  // Browser fetch carries the HttpOnly session cookie. CapacitorHttp does not
  // share that cookie jar, so an authenticated native request must also carry
  // the short-lived Bearer token held in memory or admin APIs return 401.
  if (!transportUsesCookieJar() || !authContext.isAuthenticated() || !authContext.getUserInfo()) {
    const bearer = authContext.getBearer()
    if (bearer) headers['Authorization'] = `Bearer ${bearer}`
  }

  // 网络层失败归一化（移植自 feat/web-mobile-hyper 的 _core.ts）。
  // fetch 只在真正的传输层失败（断网 / DNS / CORS）时 reject，此时不存在
  // HTTP 状态码。不归一的话上层拿到的是原生 TypeError，`status` 字段根本
  // 不存在 —— HyperList.vue:56 的 `status === 0` 分支永远命中不了，用户在
  // 断网时看到的是「加载失败，点击重试」而不是「请检查网络」，恰好与那段
  // 注释的立意相反（别让用户朝错误方向排查）。
  // ★ 传输层接缝（10 §4.6.31 / 04 §7.2 ④）：`getTransport()` 默认就是
  //   `fetch(path, { … })` —— 与引入接缝之前**逐字相同**，网页形态零回归。
  //   壳内原生传输（CapacitorHttp）由壳侧**显式** `setTransport()` 安装，
  //   不自动生效 ⇒ 探测失败/未安装时自动落回网页形态，不会把用户卡死。
  //
  //   ⚠️ 接缝只换**传输**，不换**归一化**：下面这段 try/catch 是 main 上
  //   另一轮加的（AbortError 原样抛出 + network_error）。cherry-pick 时两侧
  //   都必须保留 —— 只取接缝那一侧会把断网提示退回成裸 TypeError，
  //   HyperList.vue 的 `status === 0` 分支又永远命中不了。
  let r: TransportResponse
  try {
    r = await getTransport()({
      method,
      path,
      baseUrl: gatewayBaseUrl(),
      headers,
      credentials: 'same-origin',
      body: hasBody ? JSON.stringify(body) : undefined,
      signal: options?.signal,
    })
  } catch (err) {
    // AbortError 是调用方主动取消（连续加载换页、组件卸载），不是故障：
    // 原样抛出，否则会被上层计入错误态并弹出重试 UI。
    if (err instanceof Error && err.name === 'AbortError') throw err
    throw new ApiError(0, 'network_error')
  }

  if (sessionEpoch !== epochAtStart) {
    throw new EpochError()
  }

  if (r.status === 401) {
    const raw = await r.text()
    const msg = errorMessage(r.statusText, raw)
    // `undefined` = 本次没探（见 ApiError.sessionAlive 的三态说明）。
    // 必须保持 undefined 而不是 false —— 「没探」不等于「探了说是死的」。
    let sessionAlive: boolean | undefined
    if (
      !redirectStarted &&
      !isAuthProbePath(path) &&
      path.startsWith('/api/') &&
      typeof window !== 'undefined' &&
      !window.location.pathname.startsWith('/m/login') &&
      !window.location.pathname.startsWith('/login')
    ) {
      // ★ 先确认会话是否真的死了，再决定要不要把用户踢回登录页。
      //   详见 sessionReallyDead 的注释：后端把「端点未注册」也回成 401，
      //   与「未认证」逐字节相同；旧实现直接踢 ⇒ 有效会话被误杀。
      const dead = await sessionReallyDead()
      // 探针结论随错误传下去，供 formatApiError 区分「会话有效但端点拒绝」
      // 与「会话真的过期」—— 两者对用户是完全不同的下一步动作。
      sessionAlive = !dead
      if (dead) {
        redirectStarted = true
        unauthorizedHandler?.(window.location.pathname + window.location.search)
      }
    }
    throw new ApiError(401, msg || 'Unauthorized', errorCode(raw), sessionAlive)
  }

  if (!r.ok) {
    let msg = r.statusText
    let code = ''
    try {
      const raw = await r.text()
      msg = errorMessage(r.statusText, raw)
      code = errorCode(raw)
    } catch {
      /* 网络错误读 body 失败 — 保留 statusText */
    }
    if (sessionEpoch !== epochAtStart) throw new EpochError()
    throw new ApiError(r.status, msg, code)
  }

  if (r.status === 204 || r.status === 205) return undefined as T
  const text = await r.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}
