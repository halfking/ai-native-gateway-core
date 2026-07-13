import { req } from './_core'

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

export interface OfflineActivateRequest {
  signed_license: string
  activation_code?: string
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

export interface ActivationResult {
  success: boolean
  message?: string
  signed_license?: { data: string; signature: string }
  expires_at?: string
  active_devices?: Array<{ device_name: string; instance_id: string }>
  need_deactivate?: boolean
}

export function getLicenseStatus() {
  return req<CustomerLicenseStatus>('GET', '/api/system/license/status')
}

export function getLicenseInfo() {
  return req<CustomerLicenseInfo>('GET', '/api/system/license/info')
}

export function activateLicense(payload: ActivateRequest) {
  return req<ActivationResult>('POST', '/api/system/license/activate', payload)
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