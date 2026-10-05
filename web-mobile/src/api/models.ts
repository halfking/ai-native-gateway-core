import { req, type RequestOptions } from './client'

// models.ts — /api/routing/available-models（家族 → 版本分类视图，形状对齐
// web/src/api/models.ts）。

export interface AvailableVersion {
  canonical_name: string
  display_name: string
  modality: string
  context_window: number | null
  parameters_b: number | null
  provider_count: number
  featured?: boolean
  aliases?: string[]
}

export interface ModelFamily {
  id: string
  display_name: string
  vendor: string
  versions: AvailableVersion[]
}

export interface AvailableModelsResponse {
  families: ModelFamily[]
  popular?: string[]
  unmapped?: number
  total_raw?: number
}

export function fetchAvailableModels(options?: RequestOptions): Promise<AvailableModelsResponse> {
  return req<AvailableModelsResponse>('GET', '/api/routing/available-models', undefined, options)
}
