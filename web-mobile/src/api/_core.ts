// _core.ts — web-mobile 低层 fetch 管线（复刻网关 web/src/api/_core.ts 语义，
// UI 规范 17 §5）：cookie 优先（credentials:'same-origin'），无 userInfo 才补
// Authorization: Bearer；401 → replace 到 /m/login?redirect=；AbortSignal 全链路
// 透传；ApiError 携带 status + detail。

export const MOBILE_BASE = '/m'

export interface UserInfo {
  id?: number | string
  username?: string
  role?: string
  display_name?: string
  [k: string]: unknown
}

/** 内存持有 access_token（不落 localStorage，17 §5）。 */
let accessToken: string | null = null
let currentUser: UserInfo | null = null

export function setToken(token: string | null): void {
  accessToken = token
}

export function getToken(): string | null {
  return accessToken
}

export function setUserInfo(u: UserInfo | null): void {
  currentUser = u
}

export function getUserInfo(): UserInfo | null {
  return currentUser
}

export function isAuthenticated(): boolean {
  return currentUser !== null || accessToken !== null
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public detail?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

const PUBLIC_PREFIXES = ['/healthz', '/api/auth/token', '/api/auth/me', '/api/system/version', '/version']

function isPublicPath(path: string): boolean {
  return PUBLIC_PREFIXES.some((p) => path === p || path.startsWith(`${p}/`) || path.startsWith(`${p}?`))
}

export function buildHeaders(_method: string, hasBody: boolean): Record<string, string> {
  const h: Record<string, string> = {}
  if (hasBody) h['Content-Type'] = 'application/json'
  const locale = currentLocaleTag()
  if (locale) h['Accept-Language'] = locale
  // cookie 优先：JWT 登录成功后 userInfo 已水合 → 不带内存 Bearer，
  // 避免影子 cookie（与桌面端 _core.ts 同一裁决）。
  const bearer = isAuthenticated() && !currentUser ? accessToken : null
  if (bearer) h['Authorization'] = `Bearer ${bearer}`
  return h
}

function redirectToLogin(path: string): void {
  if (window.location.pathname.startsWith(`${MOBILE_BASE}/login`)) return
  const redirect = encodeURIComponent(
    window.location.pathname.slice(MOBILE_BASE.length) + window.location.search,
  )
  window.location.replace(`${MOBILE_BASE}/login?redirect=${redirect}${path ? '' : ''}`)
}

export async function req<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const hasBody = body !== undefined
  const headers = buildHeaders(method, hasBody)
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: hasBody ? JSON.stringify(body) : undefined,
      signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError('network_error', 0, err)
  }
  if (res.status === 401 && !isPublicPath(path.split('?')[0] ?? path)) {
    setUserInfo(null)
    setToken(null)
    redirectToLogin(path)
    throw new ApiError('unauthorized', 401)
  }
  if (!res.ok) {
    let detail: unknown = null
    let message = `http_${res.status}`
    try {
      const data = (await res.json()) as { error?: string; detail?: unknown; message?: string }
      if (data && typeof data === 'object') {
        message = data.error ?? data.message ?? message
        detail = data.detail ?? data
      }
    } catch {
      /* 非 JSON 错误体：保留 http_XXX */
    }
    throw new ApiError(message, res.status, detail)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

// ── Accept-Language（i18n/index.ts 注入，避免循环依赖走注册表）─────────

let localeProvider: () => string = () => 'zh-CN'

export function setLocaleProvider(fn: () => string): void {
  localeProvider = fn
}

function currentLocaleTag(): string {
  try {
    return localeProvider()
  } catch {
    return 'zh-CN'
  }
}
