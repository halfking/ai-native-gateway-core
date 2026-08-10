// model-iq.ts — client for the model IQ admin endpoints.
//
// Backs the provider model list (standard IQ + node IQ columns), the per-node
// IQ history chart in the model drawer, and the "test now" button.
import { req } from './_core'

// One node's latest + aggregate IQ (row of GET /node-latest).
export interface NodeIQLatest {
  credential_id: number
  credential_label: string
  provider_id: number
  provider_name: string
  raw_model_name: string
  canonical_name: string
  overall_score: number
  grade: string
  avg_score: number
  min_score: number
  max_score: number
  sample_count: number
  tested_at: string | null
}

// One historical IQ test run (row of GET /history), newest first.
export interface IQHistoryPoint {
  tested_at: string
  overall_score: number
  grade: string
  accuracy: number
  stability: number
  latency_p95: number
  probe_kind: string
  trigger_kind: string
  status: string
}

// One row of GET /catalog (per canonical model).
export interface CatalogIQRow {
  canonical_id: number
  canonical_name: string
  display_name: string
  family: string
  standard_iq: number | null
  standard_iq_source: string
  node_avg_iq: number | null
  node_count: number
  max_node_iq: number | null
  min_node_iq: number | null
}

// Result of POST /trigger — the modelquality.QualityScore shape.
export interface IQTestResult {
  model_name: string
  provider: string
  credential_id: number
  canonical_model?: string
  probe_kind?: string
  accuracy: number
  latency_p95: number
  stability: number
  overall_score: number
  grade: string
  timestamp: string
  benchmark_id: string
}

export function getNodeIQLatest(providerId: number): Promise<NodeIQLatest[]> {
  return req<NodeIQLatest[]>('GET', `/api/admin/model-iq/node-latest?provider_id=${providerId}`)
}

export function getModelIQHistory(
  credentialId: number,
  rawModelName: string,
  limit = 50,
): Promise<IQHistoryPoint[]> {
  const q = new URLSearchParams({
    credential_id: String(credentialId),
    raw_model_name: rawModelName,
    limit: String(limit),
  })
  return req<IQHistoryPoint[]>('GET', `/api/admin/model-iq/history?${q.toString()}`)
}

export function getCatalogIQ(): Promise<CatalogIQRow[]> {
  return req<CatalogIQRow[]>('GET', '/api/admin/model-iq/catalog')
}

export function triggerModelIQTest(
  credentialId: number,
  rawModelName: string,
): Promise<IQTestResult> {
  return req<IQTestResult>('POST', '/api/admin/model-iq/trigger', {
    credential_id: credentialId,
    raw_model_name: rawModelName,
  })
}
