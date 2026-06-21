import { req } from './_core'

// provider-settings.ts — v6.0 audit T12 (2026-06-22)
// Per-provider setting overrides (provider_settings table). Allows a
// single provider to opt out of (or into) a platform-wide setting
// without touching the global default. Each row carries enabled + value
// so a disabled override is a no-op but still visible in the UI.

export interface ProviderSetting {
  key: string
  value: any
  enabled: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export interface ProviderSettingsResponse {
  provider_id: number
  settings: ProviderSetting[]
}

/** Get all provider-level setting overrides. */
export async function getProviderSettings(providerId: number): Promise<ProviderSettingsResponse> {
  return req<ProviderSettingsResponse>('GET', `/api/providers/${providerId}/settings`)
}

/** Get a specific provider-level setting. */
export async function getProviderSetting(providerId: number, key: string): Promise<{ key: string; value: any; enabled: boolean }> {
  return req<{ key: string; value: any; enabled: boolean }>('GET', `/api/providers/${providerId}/settings/${key}`)
}

/** Set or update a provider-level setting. */
export async function setProviderSetting(providerId: number, key: string, value: any, enabled: boolean = true): Promise<void> {
  await req('PUT', `/api/providers/${providerId}/settings/${key}`, { value, enabled })
}

/** Delete a provider-level setting override (revert to platform default). */
export async function deleteProviderSetting(providerId: number, key: string): Promise<void> {
  await req('DELETE', `/api/providers/${providerId}/settings/${key}`)
}