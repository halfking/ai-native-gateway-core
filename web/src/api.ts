// api.ts — v6.0 audit T12 (2026-06-22)
// Barrel re-export. The actual code lives under api/<domain>.ts.
//
// History: this file was a single 4176-line monolith with 117 exported
// functions and ~50 type interfaces. Splitting it into 23 domain
// files makes each surface reviewable on its own, but every existing
// call site uses `import { foo } from '../api'` (or `from '@/api'` if
// the alias were configured), so we keep the public surface stable by
// re-exporting everything here. New code should import from the domain
// module directly: `import { login } from '../api/auth`.

import { req } from './api/_core'

export * from './api/_core'
export * from './api/auth'
export * from './api/catalog'
export * from './api/providers'
export * from './api/provider-probe'
export * from './api/provider-settings'
export * from './api/keys'
export * from './api/key-applications'
export * from './api/tenants'
export * from './api/usage'
export * from './api/routing'
export * from './api/logs'
export * from './api/models'
export * from './api/system'
export * from './api/free-pool'
export * from './api/compression'
export * from './api/admin'
export * from './api/tuning'
export * from './api/memora'
export * from './api/maas'
export * from './api/settings'
export * from './api/tenant-model-policy'
export * from './api/pending-cache'
export * from './api/session'
export * from './api/credential-monitor'
export * from './api/format-anomalies'

// Default export: axios-style HTTP client (api.get/put/patch/post/delete).
// Wraps the low-level `req<T>(method, path, body?)` from api/_core so views
// that use the `import api from '@/api'` pattern keep type-safe responses.
export interface ApiRequestConfig {
  params?: Record<string, unknown>
  responseType?: 'json' | 'blob' | 'text' | 'arraybuffer'
}

export interface ApiClient {
  get:    <T = any>(path: string, config?: ApiRequestConfig) => Promise<{ data: T }>
  post:   <T = any>(path: string, body?: unknown, config?: ApiRequestConfig) => Promise<{ data: T }>
  put:    <T = any>(path: string, body?: unknown, config?: ApiRequestConfig) => Promise<{ data: T }>
  patch:  <T = any>(path: string, body?: unknown, config?: ApiRequestConfig) => Promise<{ data: T }>
  delete: <T = any>(path: string, config?: ApiRequestConfig) => Promise<{ data: T }>
}

function buildQuery(params?: Record<string, unknown>): string {
  if (!params) return ''
  const usp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null) continue
    usp.set(k, String(v))
  }
  const qs = usp.toString()
  return qs ? `?${qs}` : ''
}

const api: ApiClient = {
  get:    <T = any>(path: string, config?: ApiRequestConfig) =>
    req<any>('GET', path + buildQuery(config?.params)).then((data) => ({ data: data as T })),
  post:   <T = any>(path: string, body?: unknown, _config?: ApiRequestConfig) =>
    req<any>('POST', path, body).then((data) => ({ data: data as T })),
  put:    <T = any>(path: string, body?: unknown, _config?: ApiRequestConfig) =>
    req<any>('PUT', path, body).then((data) => ({ data: data as T })),
  patch:  <T = any>(path: string, body?: unknown, _config?: ApiRequestConfig) =>
    req<any>('PATCH', path, body).then((data) => ({ data: data as T })),
  delete: <T = any>(path: string, _config?: ApiRequestConfig) =>
    req<any>('DELETE', path).then((data) => ({ data: data as T })),
}

export default api
