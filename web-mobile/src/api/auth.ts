import { req, type RequestOptions, type UserInfo } from './client'

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
  return req<LoginResponse>(
    'POST',
    '/api/auth/token',
    {
      username,
      password,
      remember_me: options?.rememberMe ?? false,
      captcha_id: options?.captchaId ?? '',
      captcha_code: options?.captchaCode ?? '',
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
  return resp as UserInfo
}
