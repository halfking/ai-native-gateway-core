import { req, ApiError, type RequestOptions, type UserInfo } from './client'

export interface LoginResponse {
  access_token: string
  token_type: string
  expires_at: string
  user: UserInfo
}

/** GET /api/auth/captcha 的响应（后端 admin/auth_captcha.go）。 */
export interface CaptchaResponse {
  /** 服务端签发的凭证 ID，登录时原样回传。答案本身不在这份响应里。 */
  captcha_id: string
  /** `data:image/png;base64,…` 的位图验证码。 */
  image: string
  expires_at: string
  /**
   * 后端判定「这个来源已被强制要求验证码」。
   * true 时前端把输入框标成必填并在未填时拦下提交，
   * 免得用户提交完才收到 400 captcha_required。
   */
  required: boolean
  ttl_seconds: number
}

export interface LoginOptions {
  /** 「保持登录」勾选状态。true=持久 cookie（24h），false=会话级 cookie。 */
  rememberMe?: boolean
  captchaId?: string
  captchaCode?: string
}

/**
 * 拉一张登录验证码。未达失败阈值时后端 `required=false`，此时验证码**可选**
 * ——用户可以直接留空提交（见 admin/auth_captcha.go 的自适应闸门）。
 */
export function fetchCaptcha(options?: RequestOptions): Promise<CaptchaResponse> {
  return req<CaptchaResponse>('GET', '/api/auth/captcha', undefined, options)
}

/**
 * 登录。
 *
 * ⚠️ `options` 同时承载 RequestOptions（signal）与 LoginOptions，是因为两者
 * 都是「这次请求的可选修饰」；拆成两个并列的可选参数会让调用点出现
 * `login(u, p, undefined, { rememberMe })` 这种只为占位的调用。
 */
export function login(
  username: string,
  password: string,
  options?: RequestOptions & LoginOptions,
): Promise<LoginResponse> {
  // ⚠️ 2026-10-11 实测缺陷（iOS 模拟器端到端登录卡死）：自适应验证码是**成对**
  // 契约 —— 后端 verifyLoginCaptcha（admin/auth_captcha.go:471-481）只在
  // `captcha_id` 与 `captcha_code` **同时非空**时才认为「用户提交了验证码」；
  // 只带 id 不带答案会直接被判 captcha_invalid。
  // 而页面 onMounted 总会先取一张码 ⇒ `captchaId` **永远非空**，于是旧实现
  // 把「用户没填验证码」也发成了「提交了一个空验证码」——
  // 未达失败阈值（required=false）时本来该放行的自适应路径 100% 走不通，
  // 用户看到的是「验证码错误或已过期，请重新输入」，且**永远不会**成功。
  // 修法：答案为空时两个字段一起留空，让服务端走「没带验证码」的分支。
  const captchaCode = (options?.captchaCode ?? '').trim()
  return req<LoginResponse>(
    'POST',
    '/api/auth/token',
    {
      username,
      password,
      remember_me: options?.rememberMe ?? false,
      captcha_id: captchaCode ? (options?.captchaId ?? '') : '',
      captcha_code: captchaCode,
    },
    options,
  )
}

export function logout(options?: RequestOptions): Promise<void> {
  return req<void>('POST', '/api/auth/logout', undefined, options)
}

/** 冷启动水合探测：401 = 未登录（正常路径，非错误）。 */
export function fetchMe(options?: RequestOptions): Promise<UserInfo> {
  return req<MeResponse>('GET', '/api/auth/me', undefined, options).then((resp) => unwrapMe(resp))
}

/**
 * 2026-10-11：raw 版 hydrate 使用。`/api/auth/me` 的常规路径（包裹态）会顺带回一份
 * 新的 access_token；unwrap 之后那一份就丢了——而壳内 CapacitorHttp 不共享 WebView
 * cookie jar，旧实现导致冷启动后 Bearer 永远空，所有受保护请求 401 ⇒ 整页跳登录。
 * 这里暴露原响应供 store 一次取齐 user + token。
 */
export function fetchMeRaw(options?: RequestOptions): Promise<MeResponse> {
  return req<MeResponse>('GET', '/api/auth/me', undefined, options)
}

/**
 * `/api/auth/me` 的实际响应有**两种形态**（见后端 admin/users.go handleAuthMe）：
 *
 *   ① 包裹态 { access_token, expires_at, user: UserInfo }  ← 签发新 token 成功时（常规路径）
 *   ② 裸对象 UserInfo                                      ← 签发失败时的兜底（注释：
 *      "Don't fail the /me call if signing fails — just return user info"）
 *
 * ⚠️ 2026-10-06 实测缺陷：本函数原先直接声明 `Promise<UserInfo>` 并把整包
 * 当 UserInfo 返回，`hydrate()` 随即 `userInfo.value = me`。
 * 包裹态下 `userInfo.role` / `display_name` 全是 undefined，于是账户面板
 * 渲染成「—」+「普通用户」—— **super_admin 被显示成普通用户**。
 * （用户：admin / 系统管理员 / role=super_admin）
 */
export type MeResponse = UserInfo & { access_token?: string; expires_at?: string; user?: UserInfo }

/** 从 `/api/auth/me` 响应里取出真正的 UserInfo，两种形态都吃。 */
export function unwrapMe(resp: MeResponse): UserInfo {
  if (resp && typeof resp === 'object' && resp.user && typeof resp.user === 'object') {
    return resp.user
  }
  const bare = resp as UserInfo
  // 2026-10-22 审计修正：2xx 的**维护信封**（{"code":…,"message":…,"request_id":…}）
  // 会被这一行原样当成 UserInfo 存进 store ⇒ isAuthenticated 变 true、
  // role 是 undefined ⇒ 路由守卫放行 ⇒ 造出「假登录态」（用户看到已登录，
  // 但所有请求都带着空角色）。实测形态：维护组件回 2xx + HTML/信封体。
  //
  // 判据用 UserInfo 的两个**必填**身份字段（id: number、username: string）——
  // 两种真实响应形态（包裹态的内层 user、裸态）都带齐它们，维护信封不带。
  // 缺字段且带 code ⇒ 是维护/错误信封 ⇒ 抛 ApiError(503, code)，
  // 让上层按「维护态」处理（不踢登录、不清本地态），而不是造出假登录态。
  if (
    bare &&
    typeof bare === 'object' &&
    typeof (bare as { id?: unknown }).id === 'number' &&
    typeof (bare as { username?: unknown }).username === 'string'
  ) {
    return bare
  }
  const code = (bare as { code?: unknown })?.code
  if (typeof code === 'string' && code) {
    throw new ApiError(503, 'gateway returned an envelope instead of /api/auth/me', code)
  }
  throw new Error('unexpected /api/auth/me payload shape')
}
