// quality.ts — same-origin wrappers for /api/quality/* (Admin UI).
// Prefer this over quality-api-client.ts (axios + hardcoded baseURL).

import { headers } from './_core'
import type {
  ProviderQualityData,
  ProviderRequestStats,
  QualityGrade,
} from '../types/quality-api'

export interface QualitySummaryItem {
  provider_id: number
  provider_name: string
  best_model_name: string | null
  quality_score: number
  quality_grade: QualityGrade | string
  availability_score: number
  performance_score: number
  total_requests_24h: number
  calculated_at: string
}

interface QualityEnvelope<T> {
  code: number
  message: string
  data?: T
}

async function qualityFetch<T>(path: string): Promise<QualityEnvelope<T>> {
  const r = await fetch(path, {
    method: 'GET',
    headers: headers('GET'),
    credentials: 'same-origin',
  })
  let body: QualityEnvelope<T>
  try {
    body = await r.json()
  } catch {
    throw new Error(r.statusText || 'quality api parse failed')
  }
  return body
}

/** One row per provider for /providers list enrichment. */
export async function getQualitySummary(): Promise<QualitySummaryItem[]> {
  const body = await qualityFetch<{ total: number; summary: QualitySummaryItem[] }>(
    '/api/quality/summary',
  )
  if (body.code !== 0) {
    throw new Error(body.message || 'load quality summary failed')
  }
  return body.data?.summary ?? []
}

/**
 * Per-provider model quality profiles for the detail Quality tab.
 * Returns null when the provider has no quality data yet (40402).
 */
export async function getProviderQualityDetail(
  providerId: number,
): Promise<ProviderQualityData | null> {
  const body = await qualityFetch<ProviderQualityData>(
    `/api/quality/providers/${providerId}`,
  )
  if (body.code === 40402 || body.code === 40401) return null
  if (body.code !== 0) {
    throw new Error(body.message || 'load provider quality failed')
  }
  return body.data ?? null
}

/**
 * 取供应商近 30 天请求统计（轻量接口，供请求详情抽屉等复用）。
 * 供应商不存在时返回 null。
 * model 可选：非空时按模型粒度统计（raw_model_name 过滤，方案 C）。
 */
export async function getProviderRequestStats(
  providerId: number,
  model?: string,
): Promise<ProviderRequestStats | null> {
  const qs = model ? `?model=${encodeURIComponent(model)}` : ''
  const body = await qualityFetch<ProviderRequestStats>(
    `/api/quality/providers/${providerId}/stats${qs}`,
  )
  if (body.code === 40401 || body.code === 40402) return null
  if (body.code !== 0) {
    throw new Error(body.message || 'load provider request stats failed')
  }
  return body.data ?? null
}
