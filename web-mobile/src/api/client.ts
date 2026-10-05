import { acceptLanguageHeader } from '@/i18n'

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

  // 网络层失败归一化（移植自 feat/web-mobile-hyper 的 _core.ts）。
  // fetch 只在真正的传输层失败（断网 / DNS / CORS）时 reject，此时不存在
  // HTTP 状态码。不归一的话上层拿到的是原生 TypeError，`status` 字段根本
  // 不存在 —— HyperList.vue:56 的 `status === 0` 分支永远命中不了，用户在
  // 断网时看到的是「加载失败，点击重试」而不是「请检查网络」，恰好与那段
  // 注释的立意相反（别让用户朝错误方向排查）。
  let r: Response
  try {
    r = await fetch(path, {
      method,
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
