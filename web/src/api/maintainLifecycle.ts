import { MAINTAIN_API_BASE } from '../config/edition'

export type LicenseStatus = {
  state: string
  license_key?: string
  customer_name?: string
  expires_at?: string
  subscription_tier?: string
  device_name?: string
  last_heartbeat?: string
}

export type ActivateRequest = {
  instance_id: string
  license_key: string
  hardware_hash: string
  device_name?: string
}

export type OfflineActivationRequest = {
  request_id: string
  license_key: string
  hardware_hash: string
  device_name?: string
  status: string
  reject_reason?: string
  submitted_at: string
  processed_at?: string
}

export type OfflineActivationResponse = {
  request_id: string
  activation_code?: string
  signed_license?: string
  status: string
  message?: string
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
    const hint = body?.request_id ? `（request_id: ${body.request_id}）` : ''
    const msg = body?.message || body?.error || `请求失败（${response.status}）`
    throw new Error(String(msg) + hint)
  }
  return body as T
}

export const maintainLifecycleApi = {
  /** Public — no JWT; used during local first-boot. */
  publicActivate: (payload: ActivateRequest) =>
    request<{ state: string; license_key: string; signed_license?: string; instance_id: string }>(
      '/public/license/activate',
      { method: 'POST', body: JSON.stringify(payload) },
    ),

  /** Customer scope — may require session cookie. */
  consent: (payload: {
    subject_id: string
    instance_id?: string
    agreement_type: string
    agreement_version: string
    granted: boolean
    source: string
  }) => request('/consents', { method: 'POST', body: JSON.stringify(payload) }),

  licenseStatus: (instanceId: string) =>
    request<LicenseStatus>(`/license/status?instance_id=${encodeURIComponent(instanceId)}`),

  activateLicense: (payload: ActivateRequest) =>
    request<LicenseStatus>('/license/activate', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  submitOfflineRequest: (payload: { license_key: string; hardware_hash: string; device_name?: string }) =>
    request<OfflineActivationRequest>('/offline-activation/requests', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  offlineRequestStatus: (requestId: string) =>
    request<OfflineActivationRequest>(`/offline-activation/requests/${encodeURIComponent(requestId)}`),

  offlineResponse: (requestId: string) =>
    request<OfflineActivationResponse>(
      `/offline-activation/requests/${encodeURIComponent(requestId)}/response`,
    ),
}

export function licenseStateLabel(state?: string | null): string {
  const map: Record<string, string> = {
    none: '未激活',
    active: '已激活',
    expired: '已过期',
    revoked: '已吊销',
    grace: '宽限期',
    pending: '待生效',
  }
  if (!state) return '未知'
  return map[state] || state
}
