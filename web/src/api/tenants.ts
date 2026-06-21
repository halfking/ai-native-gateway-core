import { req } from './_core'

// tenants.ts — v6.0 audit T12 (2026-06-22)
// Tenant summary + per-tenant usage + global default-limits config.
// All routes live under /api/usage/... or /api/config/... and are
// admin-only (adminMiddleware). The 30-day default for getTenantUsage
// matches the dashboard's "Last 30 days" tab.

export interface TenantSummary {
  tenant_id: string
  key_count: number
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface TenantUsage {
  tenant_id: string
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_applications: number
}

export function getTenants() {
  return req<TenantSummary[]>('GET', '/api/usage/tenants')
}

export function getTenantUsage(tenant: string, days = 30) {
  return req<TenantUsage>('GET', `/api/usage/by-tenant?tenant=${encodeURIComponent(tenant)}&days=${days}`)
}

// ── Configuration ─────────────────────────────────────────────────────────

export interface DefaultLimits {
  rate_limit_rpm: number
  rate_limit_concurrent: number
  rate_limit_tpm: number | null
}

export function getDefaultLimits() {
  return req<DefaultLimits>('GET', '/api/config/default-limits')
}

export function setDefaultLimits(data: DefaultLimits) {
  return req<DefaultLimits & { status: string }>('PUT', '/api/config/default-limits', data)
}