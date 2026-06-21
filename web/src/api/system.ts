import { req } from './_core'

// system.ts — v6.0 audit T12 (2026-06-22)
// Cross-cutting system endpoints:
//
//  - /api/system/background-tasks — liveness for the discovery, probe,
//    cycler, recovery, and telemetry background loops
//  - /api/routing/score-details + /scoring-weights + /manual-priority +
//    /featured-models — the model-routing scoring subsystem
//  - /healthz — liveness/readiness for the proxy itself
//
// Free-pool endpoints (catalog, signup hub, temp email) are in free-pool.ts.

export interface BackgroundTaskDiscovery {
  alive: boolean
  running: boolean
  status: string | null
  trigger: string | null
  started_at: string | null
  finished_at: string | null
  heartbeat_at: string | null
  error: string | null
  summary: Record<string, unknown> | null
  elapsed_seconds: number | null
  since_last_seconds: number | null
}

export interface BackgroundTaskLoop {
  alive: boolean
  last_check_at: string | null
  checks_last_10m?: number
}

export interface BackgroundTasksStatus {
  discovery: BackgroundTaskDiscovery
  probe_loop: BackgroundTaskLoop
  cycler: BackgroundTaskLoop
  recovery: BackgroundTaskLoop
  telemetry: BackgroundTaskLoop
}

export function getBackgroundTasksStatus() {
  return req<BackgroundTasksStatus>('GET', '/api/system/background-tasks')
}

// ── Routing: Score Details & Manual Priority ──────────────────────────────

export interface ScoreDetail {
  credential_id: number
  provider_id: number
  provider_name: string
  raw_model: string
  manual_priority: number
  price_in: number
  price_out: number
  blended_cost: number
  active_sessions: number
  consecutive_failures: number
  concurrency_limit: number | null
  currency: string
  normalized_cost: number
  session_load: number
  composite_score: number
}

export interface ScoreDetailsResponse {
  model: string
  weights: ScoringWeights
  candidates: ScoreDetail[]
}

export interface ScoringWeights {
  price: number
  session_load: number
  failure_penalty: number
  default_price_cny: number
  default_price_usd: number
}

export function getScoreDetails(model: string) {
  return req<ScoreDetailsResponse>('GET', `/api/routing/score-details?model=${encodeURIComponent(model)}`)
}

export function updateManualPriority(credentialId: number, modelName: string, priority: number) {
  return req<{ status: string }>('PATCH', '/api/routing/manual-priority', {
    credential_id: credentialId,
    model_name: modelName,
    manual_priority: priority,
  })
}

export function getScoringWeights() {
  return req<ScoringWeights>('GET', '/api/routing/scoring-weights')
}

export function updateScoringWeights(weights: Partial<ScoringWeights>) {
  return req<{ status: string }>('PATCH', '/api/routing/scoring-weights', weights)
}

export interface FeaturedModel {
  name: string
  standardized_name: string
  count: number
}

export function getFeaturedModelsDynamic() {
  return req<{ models: FeaturedModel[] }>('GET', '/api/routing/featured-models')
}

// ── System health ─────────────────────────────────────────────────────────

export interface HealthResponse {
  status: string
  version: string
  proxy?: {
    proxy: string
    healthy: boolean
    health_done: boolean
    domestic: string[]
  }
}

export function getHealth(full = false) {
  return req<HealthResponse>('GET', `/healthz${full ? '?full=true' : ''}`)
}