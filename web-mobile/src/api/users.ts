import { req, type RequestOptions } from './client'

export interface ManagedUser {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  must_change_password: boolean
  last_login_at: string | null
  created_at: string
}

export function fetchManagedUsers(options?: RequestOptions): Promise<ManagedUser[]> {
  return req<unknown>('GET', '/api/users', undefined, options).then((data) => {
    if (!Array.isArray(data)) throw new Error('users response must be an array')
    return data as ManagedUser[]
  })
}

export function setManagedUserEnabled(id: number, enabled: boolean, options?: RequestOptions): Promise<ManagedUser> {
  return req<ManagedUser>('PUT', `/api/users/${id}`, { enabled }, options)
}
