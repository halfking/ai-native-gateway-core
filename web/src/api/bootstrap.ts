/** Local first-boot bootstrap APIs (Gateway `/api/system/bootstrap/*`). */

export type BootstrapStatus = {
  activated: boolean
  registered: boolean
  center_online: boolean
  center_url?: string
  instance_id?: string
  hardware_hash?: string
  license_key?: string
  device_name?: string
  primary_ip?: string
  service_version?: string
  activated_at?: string
  admin_bootstrap_required?: boolean
  message?: string
}

export type ActivateQuickRequest = {
  instance_id: string
  hardware_hash: string
  device_name?: string
}

export type FingerprintInfo = {
  hardware_hash: string
  instance_id?: string
  os?: string
  arch?: string
  network_summary?: string
  error?: string
}

export type BootstrapActivateRequest = {
  instance_id: string
  license_key: string
  hardware_hash: string
  device_name?: string
  online?: boolean
}

export type BootstrapActivateResult = {
  activated: boolean
  center_online?: boolean
  registered?: boolean
  mode?: string
  message?: string
  error?: string
  online_error?: string
  maintain?: unknown
  activation?: unknown
}

export type ImportOfflineRequest = {
  instance_id: string
  hardware_hash: string
  signed_license?: string
  activation_code?: string
}

export type ImportOfflineResult = {
  activated: boolean
  mode?: string
  center_online?: boolean
  registered?: boolean
  license_key?: string
  message?: string
  error?: string
}

export type RegisterCenterRequest = {
  instance_id: string
  license_key: string
  hardware_hash: string
  hostname?: string
  version?: string
  region?: string
}

export type RegisterCenterResult = {
  registered: boolean
  deferred?: boolean
  message?: string
  error?: string
  body?: unknown
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
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
      (body && typeof body === 'object' && (body.error || body.message)) ||
      `请求失败（${response.status}）`
    throw new Error(String(msg))
  }
  return body as T
}

export const bootstrapApi = {
  status: (instanceId?: string, licenseKey?: string) => {
    const q = new URLSearchParams()
    if (instanceId) q.set('instance_id', instanceId)
    if (licenseKey) q.set('license_key', licenseKey)
    const qs = q.toString()
    return request<BootstrapStatus>(`/api/system/bootstrap/status${qs ? `?${qs}` : ''}`)
  },

  fingerprint: () => request<FingerprintInfo>('/api/system/bootstrap/fingerprint'),

  activate: (payload: BootstrapActivateRequest) =>
    request<BootstrapActivateResult>('/api/system/bootstrap/activate', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  /** One-click: agree → center issue license → local import. No license_key needed. */
  activateQuick: (payload: ActivateQuickRequest) =>
    request<BootstrapActivateResult>('/api/system/bootstrap/activate-quick', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  importOffline: (payload: ImportOfflineRequest) =>
    request<ImportOfflineResult>('/api/system/bootstrap/import-offline', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  registerCenter: (payload: RegisterCenterRequest) =>
    request<RegisterCenterResult>('/api/system/bootstrap/register-center', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
}

export const BOOTSTRAP_ACTIVATED_KEY = 'llmgw_activated'
export const BOOTSTRAP_REQUIRE_KEY = 'llmgw_require_bootstrap'

export function markBootstrapActivated(): void {
  try {
    localStorage.setItem(BOOTSTRAP_ACTIVATED_KEY, '1')
  } catch {
    /* ignore */
  }
}

export function isBootstrapSkippedByFlag(): boolean {
  try {
    if (localStorage.getItem(BOOTSTRAP_REQUIRE_KEY) === '0') return true
    if (localStorage.getItem(BOOTSTRAP_ACTIVATED_KEY) === '1') return true
  } catch {
    /* ignore */
  }
  return false
}
