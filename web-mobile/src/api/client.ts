import { acceptLanguageHeader } from '@/i18n'
import { getTransport, gatewayBaseUrl } from './transport'

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
  // 后端 /api/auth/me 与 /api/auth/token 的 user 都带这两个字段
  // （admin/users.go:641 SELECT last_login_at, created_at），但移动端当前未消费，
  // 故标可选以免任何构造点被强制填值。
  last_login_at?: string
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

// ---- ApiError ----

export class ApiError extends Error {
  status: number
  detail: string
  constructor(status: number, detail: string) {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }
}

// ---- 401 处理 ----

let unauthorizedHandler: ((currentPath: string) => void) | null = null
let redirectStarted = false

export function setUnauthorizedHandler(fn: (currentPath: string) => void): void {
  unauthorizedHandler = fn
}

/** 测试复位：401 bounce 单飞闩锁。 */
export function resetUnauthorizedForTests(): void {
  redirectStarted = false
}

function isAuthProbePath(path: string): boolean {
  return path === '/api/auth/me' || path === '/api/auth/token' || path === '/api/auth/logout'
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
  if (!authContext.isAuthenticated() || !authContext.getUserInfo()) {
    const bearer = authContext.getBearer()
    if (bearer) headers['Authorization'] = `Bearer ${bearer}`
  }

  // ★ 传输层接缝（10 §4.6.31 / 04 §7.2 ④）：`getTransport()` 默认就是
  //   `fetch(path, { … })` —— 与引入接缝之前**逐字相同**，网页形态零回归。
  //   壳内原生传输（CapacitorHttp）由壳侧**显式** `setTransport()` 安装，
  //   不自动生效 ⇒ 探测失败/未安装时自动落回网页形态，不会把用户卡死。
  const r = await getTransport()({
    method,
    path,
    baseUrl: gatewayBaseUrl(),
    headers,
    credentials: 'same-origin',
    body: hasBody ? JSON.stringify(body) : undefined,
    signal: options?.signal,
  })

  if (sessionEpoch !== epochAtStart) {
    throw new EpochError()
  }

  if (r.status === 401) {
    const msg = errorMessage(r.statusText, await r.text())
    if (
      !redirectStarted &&
      !isAuthProbePath(path) &&
      path.startsWith('/api/') &&
      typeof window !== 'undefined' &&
      !window.location.pathname.startsWith('/m/login') &&
      !window.location.pathname.startsWith('/login')
    ) {
      redirectStarted = true
      unauthorizedHandler?.(window.location.pathname + window.location.search)
    }
    throw new ApiError(401, msg || 'Unauthorized')
  }

  if (!r.ok) {
    let msg = r.statusText
    try {
      msg = errorMessage(r.statusText, await r.text())
    } catch {
      /* 网络错误读 body 失败 — 保留 statusText */
    }
    if (sessionEpoch !== epochAtStart) throw new EpochError()
    throw new ApiError(r.status, msg)
  }

  if (r.status === 204 || r.status === 205) return undefined as T
  const text = await r.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}
