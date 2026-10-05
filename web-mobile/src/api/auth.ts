import { req, type RequestOptions, type UserInfo } from './client'

export interface LoginResponse {
  access_token: string
  token_type: string
  expires_at: string
  user: UserInfo
}

export function login(username: string, password: string, options?: RequestOptions): Promise<LoginResponse> {
  return req<LoginResponse>('POST', '/api/auth/token', { username, password }, options)
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
