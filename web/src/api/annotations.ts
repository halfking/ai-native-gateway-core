import { req } from './_core'

// annotations.ts — 2026-09-06
// P2.1+ Human annotation Web workflow API module.
// All routes live under /api/admin/annotations/* and require admin authentication.

export interface AnnotationSample {
  request_id: string
  model_name: string
  task_type: string
  prompt_tokens: number
  is_streaming: boolean
  has_vision: boolean
  region: string
  profile: string
  auto_provider: string
  confidence: number
  // Annotation fields (populated if annotated)
  human_provider?: string
  is_correct?: boolean
  reason?: string
  annotator?: string
  annotated_at?: string
}

export interface AnnotationStats {
  total_annotations: number
  correct_count: number
  incorrect_count: number
  accuracy_percent: number
  num_annotators: number
  first_annotation_at: string | null
  last_annotation_at: string | null
}

export interface ProviderAccuracy {
  provider: string
  total_predictions: number
  correct_predictions: number
  incorrect_predictions: number
  accuracy_percent: number
  avg_confidence: number
}

export interface AnnotatorStats {
  annotator: string
  total_annotations: number
  correct_count: number
  incorrect_count: number
  accuracy_percent: number
  first_annotation_at: string
  last_annotation_at: string
}

export interface ReasonDistribution {
  reason: string
  count: number
  percentage: number
}

export interface StatsResponse {
  // 后端 Overall 为指针（空表时 null，见 admin/annotation_handler.go）
  overall: AnnotationStats | null
  by_provider: ProviderAccuracy[]
  by_annotator: AnnotatorStats[]
  by_reason: ReasonDistribution[]
}

export interface SamplesResponse {
  samples: AnnotationSample[]
  total: number
}

export interface CreateAnnotationRequest {
  request_id: string
  human_provider: string
  is_correct: boolean
  reason: string
  annotator: string
}

export interface BatchAnnotateRequest {
  request_ids: string[]
  human_provider: string
  is_correct: boolean
  reason: string
  annotator: string
}

export interface BatchAnnotateResponse {
  success: number
  failed: number
  errors?: string[]
}

export interface SamplesParams {
  page: number
  size: number
  start_date?: string
  end_date?: string
  min_confidence?: number
  max_confidence?: number
  annotated?: boolean
  annotator?: string
}

/**
 * Get paginated annotation samples with optional filters
 */
export function getSamples(params: SamplesParams) {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('size', String(params.size))
  
  if (params.start_date) query.set('start_date', params.start_date)
  if (params.end_date) query.set('end_date', params.end_date)
  if (params.min_confidence != null) query.set('min_confidence', String(params.min_confidence))
  if (params.max_confidence != null) query.set('max_confidence', String(params.max_confidence))
  if (params.annotated != null) query.set('annotated', String(params.annotated))
  if (params.annotator) query.set('annotator', params.annotator)
  
  return req<SamplesResponse>('GET', `/api/admin/annotations/samples?${query}`)
}

/**
 * Create a single annotation
 */
export function createAnnotation(data: CreateAnnotationRequest) {
  return req<{ success: boolean; message: string }>('POST', '/api/admin/annotations', data)
}

/**
 * Batch annotate multiple samples
 */
export function batchAnnotate(data: BatchAnnotateRequest) {
  return req<BatchAnnotateResponse>('POST', '/api/admin/annotations/batch', data)
}

/**
 * Delete an annotation (for correction)
 */
export function deleteAnnotation(requestId: string) {
  return req<{ success: boolean }>('DELETE', `/api/admin/annotations/${requestId}`)
}

/**
 * Get annotation statistics
 */
export function getAnnotationStats() {
  return req<StatsResponse>('GET', '/api/admin/annotations/stats')
}

/**
 * Valid annotation reasons (must match backend validation)
 */
export const VALID_REASONS = [
  'correct',
  'performance',
  'cost',
  'availability',
  'quality',
  'other',
] as const

export type AnnotationReason = typeof VALID_REASONS[number]

/**
 * Common provider list for annotation
 */
export const PROVIDERS = [
  'openai',
  'anthropic',
  'aws_bedrock',
  'google',
  'azure',
  'deepseek',
  'zhipu',
  'minimax',
  'baichuan',
  'moonshot',
  'groq',
  'together',
  'fireworks',
  'replicate',
  'other',
] as const

export type Provider = typeof PROVIDERS[number]
