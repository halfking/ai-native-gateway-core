import { req, type UserInfo } from './_core'

export interface LoginResponse {
  access_token?: string
  token_type?: string
  expires_at?: string
  user?: UserInfo
}

export function login(username: string, password: string, signal?: AbortSignal) {
  return req<LoginResponse>('POST', '/api/auth/token', { username, password }, signal)
}

export function fetchMe(signal?: AbortSignal) {
  return req<{ user: UserInfo } | UserInfo>('GET', '/api/auth/me', undefined, signal)
}

export function logout() {
  return req<{ ok: boolean }>('POST', '/api/auth/logout')
}
