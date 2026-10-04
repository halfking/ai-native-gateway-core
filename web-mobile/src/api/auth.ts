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
  return req<UserInfo>('GET', '/api/auth/me', undefined, options)
}
