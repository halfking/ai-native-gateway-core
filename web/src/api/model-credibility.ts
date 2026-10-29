// 2026-10-29: Web API client for /api/admin/model-credibility/*.
//
// 模型可信度（防掺水）判定面。四个子分数**分开暴露**是刻意的：
// 运维必须能看出是**哪一项测量**导致了结论，否则一个不透明的总分
// 与「猜的」没有区别。

import { req } from './_core'

export type CredibilityVerdict = 'verified' | 'suspected' | 'diluted' | 'unknown'

export interface ModelCredibilityRow {
  tenant_id: string
  credential_id: number
  credential_label: string
  provider_id: number
  provider_name: string
  raw_model_name: string
  canonical_id: number
  canonical_name: string
  credibility_score: number | null
  identity_score: number | null
  capability_score: number | null
  consistency_score: number | null
  reference_score: number | null
  verdict: CredibilityVerdict
  status: string
  reported_model: string
  system_fingerprint: string
  anomalies: string[]
  measured_dimensions: number
  measured_at?: string
}

export interface ModelCredibilityCatalogRow {
  canonical_id: number
  canonical_name: string
  standard_iq: number
  node_count: number
  verified_count: number
  suspected_count: number
  diluted_count: number
  unknown_count: number
  min_credibility: number | null
  avg_credibility: number | null
  provider_count: number
}

export interface ModelCredibilityRun {
  measured_at: string
  credibility_score: number
  identity_score: number
  capability_score: number
  consistency_score: number
  reference_score: number
  verdict: CredibilityVerdict
  status: string
  reported_model: string
  anomalies: string[]
  measured_dimensions: number
}

export interface ModelCredibilityDetail {
  credential_id: number
  raw_model_name: string
  credibility_score: number
  identity_score: number
  capability_score: number
  consistency_score: number
  reference_score: number
  verdict: CredibilityVerdict
  status: string
  reported_model: string
  system_fingerprint: string
  measured_dimensions: number
  evidence: Record<string, any> | null
  anomalies: string[]
  measured_at?: string
}

export interface GetModelCredibilityParams {
  provider_id?: number
  credential_id?: number
  canonical_id?: number
  verdict?: string
  limit?: number
}

export function getModelCredibilityList(params: GetModelCredibilityParams = {}) {
  const q = new URLSearchParams()
  if (params.provider_id) q.set('provider_id', String(params.provider_id))
  if (params.credential_id) q.set('credential_id', String(params.credential_id))
  if (params.canonical_id) q.set('canonical_id', String(params.canonical_id))
  if (params.verdict) q.set('verdict', params.verdict)
  if (params.limit) q.set('limit', String(params.limit))
  const qs = q.toString()
  return req<ModelCredibilityRow[]>(
    'GET',
    '/api/admin/model-credibility/list' + (qs ? '?' + qs : ''),
  )
}

export function getModelCredibilityCatalog() {
  return req<ModelCredibilityCatalogRow[]>('GET', '/api/admin/model-credibility/catalog')
}

export function getModelCredibilityDetail(credentialId: number, rawModelName: string) {
  const q = new URLSearchParams()
  q.set('credential_id', String(credentialId))
  q.set('raw_model_name', rawModelName)
  return req<ModelCredibilityDetail>(
    'GET',
    '/api/admin/model-credibility/detail?' + q.toString(),
  )
}

export function getModelCredibilityHistory(
  credentialId: number,
  rawModelName: string,
  limit = 50,
) {
  const q = new URLSearchParams()
  q.set('credential_id', String(credentialId))
  q.set('raw_model_name', rawModelName)
  q.set('limit', String(limit))
  return req<ModelCredibilityRun[]>(
    'GET',
    '/api/admin/model-credibility/history?' + q.toString(),
  )
}

/** 立即对单个节点跑一次可信度探测（消耗一次真实上游请求）。 */
export function triggerModelCredibilityScan(credentialId: number, rawModelName: string) {
  return req<Record<string, any>>('POST', '/api/admin/model-credibility/trigger', {
    credential_id: credentialId,
    raw_model_name: rawModelName,
  })
}

/** 立即对全部活跃节点跑一轮扫描（每节点一次上游请求，串行执行）。 */
export function triggerModelCredibilityScanAll() {
  return req<{ scanned: number }>('POST', '/api/admin/model-credibility/scan', {})
}
