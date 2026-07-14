import { req, BASE, headers } from './_core'

// customer.ts — Customer-facing License & Upgrade APIs (2026-07-13)
//
// These endpoints are intentionally unauthenticated so the customer can
// query license status and complete activation BEFORE any admin login.
// They are read-mostly except for activation / heartbeat, which mutate
// device state.

export interface CustomerLicenseStatus {
  state: 'none' | 'active' | 'grace' | 'expired' | 'revoked'
  customer_name?: string
  customer_email?: string
  license_key?: string
  expires_at?: string
  days_remaining?: number
  subscription_tier?: string
  mode: 'licensed' | 'community' | 'restricted'
  grace_days_left?: number
}

export interface CustomerLicenseInfo extends CustomerLicenseStatus {
  features?: string[]
  max_devices?: number
  active_devices?: number
  activated_at?: string
  last_heartbeat?: string
}

export interface ActivateRequest {
  license_key: string
  device_name?: string
}

export interface TrialRequest {
  email: string
  agree: boolean
}

export interface TrialResult {
  success: boolean
  license_key?: string
  message?: string
  expires_at?: string
}

export interface OfflineActivateRequest {
  signed_license: string
  request_id: string
  activation_code: string
}

export interface OfflineRequestPayload {
  license_key: string
  device_name?: string
}

export interface OfflineRequestResponse {
  request_id: string
  signed_request: string
  message: string
}

export type ActivationErrorCode =
  | 'activation_success'
  | 'license_not_found'
  | 'license_expired'
  | 'license_revoked'
  | 'device_limit_exceeded'
  | 'device_already_activated'

export interface ActivationResult {
  success: boolean
  error_code?: ActivationErrorCode
  message?: string
  signed_license?: { data: string; signature: string }
  expires_at?: string
  max_devices?: number
  active_devices?: Array<{
    device_name: string
    instance_id: string
    last_heartbeat?: string
  }>
  need_deactivate?: boolean
}

export function getLicenseStatus() {
  return req<CustomerLicenseStatus>('GET', '/api/system/license/status')
}

export function getLicenseInfo() {
  return req<CustomerLicenseInfo>('GET', '/api/system/license/info')
}

export async function activateLicense(payload: ActivateRequest): Promise<ActivationResult> {
  const r = await fetch(BASE + '/api/system/license/activate', {
    method: 'POST',
    headers: headers('POST'),
    credentials: 'same-origin',
    body: JSON.stringify(payload),
  })

  const text = await r.text()
  let body: ActivationResult | null = null
  if (text) {
    try {
      body = JSON.parse(text) as ActivationResult
    } catch {
      body = null
    }
  }

  if (body && typeof body.success === 'boolean') {
    return body
  }

  let msg = r.statusText
  if (body?.message) {
    msg = body.message
  } else if (text) {
    try {
      const j = JSON.parse(text)
      msg = (j && typeof j.error === 'string') ? j.error : text
    } catch {
      msg = text
    }
  }
  throw new Error(msg)
}

export function requestTrial(payload: TrialRequest) {
  return req<TrialResult>('POST', '/api/system/license/trial', payload)
}

export function offlineActivate(payload: OfflineActivateRequest) {
  return req<{ success: boolean; message: string; expires_at?: string }>(
    'POST',
    '/api/system/license/offline-activate',
    payload,
  )
}

export function createOfflineRequest(payload: OfflineRequestPayload) {
  return req<OfflineRequestResponse>(
    'POST',
    '/api/system/license/offline-request',
    payload,
  )
}

export function sendHeartbeat() {
  return req<{ success: boolean; last_heartbeat: string }>(
    'POST',
    '/api/system/license/heartbeat',
  )
}

export interface RuntimeTelemetryPreference {
  license_id: number
  enabled: boolean
  agreement_version: string
  updated_at?: string
  disabled_at?: string
}

export function getRuntimeTelemetryPreference() {
  return req<RuntimeTelemetryPreference>('GET', '/api/tenant/telemetry-preference')
}

export function setRuntimeTelemetryPreference(enabled: boolean) {
  return req<RuntimeTelemetryPreference>('PUT', '/api/tenant/telemetry-preference', { enabled })
}

// ── Upgrade APIs ─────────────────────────────────────────────────────────

export interface UpgradeStatus {
  current_version: string
  current_build_seq: number
  channel: 'stable' | 'beta' | 'canary'
  latest_version?: string
  latest_build_seq?: number
  has_update: boolean
  update_mandatory?: boolean
  release_title?: string
  release_notes?: string
  min_version?: string
  is_compatible: boolean
  published_at?: string
}

export interface UpgradeCheckResponse extends UpgradeStatus {
  checked_at: string
}

export function getUpgradeStatus() {
  return req<UpgradeStatus>('GET', '/api/system/upgrade/status')
}

export function checkForUpgrade() {
  return req<UpgradeCheckResponse>('POST', '/api/system/upgrade/check')
}
