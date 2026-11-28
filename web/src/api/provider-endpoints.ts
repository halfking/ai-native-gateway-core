import { req } from './_core'

// provider-endpoints.ts — L148 A4 frontend (2026-11-28)
// CRUD for provider_endpoint_protocols table. Manages the specific
// protocol endpoints a provider offers (base_url + weight + primary flag).

export interface ProviderEndpoint {
  id: number
  protocol: string
  base_url: string
  is_primary: boolean
  vendor_native: string | null
  enabled: boolean
  weight: number
  notes: string | null
  health_status: string
  health_checked_at: string | null
  health_latency_ms: number | null
  health_error: string | null
  consecutive_failures: number
  created_at: string
  updated_at: string
}

export interface ProviderEndpointsResponse {
  provider_id: number
  endpoints: ProviderEndpoint[]
}

export interface ProviderEndpointPutRequest {
  base_url?: string
  is_primary?: boolean
  enabled?: boolean
  weight?: number
  vendor_native?: string
  notes?: string
}

/** List all endpoints for a provider (includes disabled rows). */
export async function getProviderEndpoints(providerId: number): Promise<ProviderEndpointsResponse> {
  return req<ProviderEndpointsResponse>('GET', `/api/providers/${providerId}/endpoints`)
}

/** Create or update an endpoint (201=created, 200=updated). */
export async function putProviderEndpoint(
  providerId: number,
  protocol: string,
  body: ProviderEndpointPutRequest,
): Promise<ProviderEndpoint> {
  return req<ProviderEndpoint>('PUT', `/api/providers/${providerId}/endpoints/${protocol}`, body)
}

/** Delete an endpoint (primary endpoints cannot be deleted). */
export async function deleteProviderEndpoint(providerId: number, protocol: string): Promise<void> {
  await req('DELETE', `/api/providers/${providerId}/endpoints/${protocol}`)
}
