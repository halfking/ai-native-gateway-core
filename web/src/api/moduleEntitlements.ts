/** Center module entitlement APIs (ai-native-maintain). */

import { MAINTAIN_API_BASE } from '../config/edition'

export type ModuleEntitlement = {
  id: string
  module_id: string
  display_name?: string
  instance_id?: string
  tenant_id?: string
  status: 'pending' | 'opened' | 'rejected' | string
  requested_by?: string
  opened_at?: string
  requested_at: string
  note?: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${MAINTAIN_API_BASE}${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers || {}),
    },
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const msg =
      (body && typeof body === 'object' && (body.message || body.error)) ||
      `请求失败（${response.status}）`
    throw new Error(String(msg))
  }
  return body as T
}

export const moduleEntitlementApi = {
  list: (status?: string) => {
    const q = status ? `?status=${encodeURIComponent(status)}` : ''
    return request<{ items: ModuleEntitlement[] }>(`/admin/module-entitlements${q}`)
  },
  apply: (payload: {
    module_id: string
    display_name?: string
    instance_id?: string
    note?: string
    auto_open?: boolean
  }) =>
    request<ModuleEntitlement>('/admin/module-entitlements/apply', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  open: (id: string) =>
    request<ModuleEntitlement>(`/admin/module-entitlements/${encodeURIComponent(id)}/open`, {
      method: 'POST',
    }),
  reject: (id: string) =>
    request<ModuleEntitlement>(`/admin/module-entitlements/${encodeURIComponent(id)}/reject`, {
      method: 'POST',
    }),
}
