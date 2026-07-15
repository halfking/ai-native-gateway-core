import { req } from './_core'

export interface IPBlocklistEntry {
  id: number
  ip_or_cidr: string
  reason: string
  scope: 'global' | 'collect' | 'ops'
  source: 'manual' | 'auto_attack'
  enabled: boolean
  expires_at?: string
  hit_count: number
  created_by?: string
  created_at: string
  updated_at: string
}

export async function listIPBlocklist(params?: {
  scope?: string
  limit?: number
  offset?: number
}): Promise<{ items: IPBlocklistEntry[]; total: number }> {
  const q = new URLSearchParams()
  if (params?.scope) q.set('scope', params.scope)
  if (params?.limit != null) q.set('limit', String(params.limit))
  if (params?.offset != null) q.set('offset', String(params.offset))
  const qs = q.toString()
  return req('GET', `/api/admin/security/ip-blocklist${qs ? `?${qs}` : ''}`)
}

export async function createIPBlocklist(data: {
  ip_or_cidr: string
  reason?: string
  scope?: string
  expires_at?: string
}): Promise<IPBlocklistEntry> {
  return req('POST', '/api/admin/security/ip-blocklist', data)
}

export async function updateIPBlocklist(
  id: number,
  data: { reason?: string; enabled?: boolean; expires_at?: string | null }
): Promise<IPBlocklistEntry> {
  return req('PATCH', `/api/admin/security/ip-blocklist/${id}`, data)
}

export async function deleteIPBlocklist(id: number): Promise<void> {
  return req('DELETE', `/api/admin/security/ip-blocklist/${id}`)
}

export async function reloadIPBlocklist(): Promise<{ status: string }> {
  return req('POST', '/api/admin/security/ip-blocklist/reload')
}
