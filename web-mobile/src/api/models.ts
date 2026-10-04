import { req } from './_core'

// models.ts — 模型目录（家族分组连续加载），对齐 web/src/api/models.ts。

export interface AvailableVersion {
  canonical_name: string
  display_name: string
  modality: string
  context_window: number | null
  provider_count: number
  tags: string[]
}

export interface AvailableFamily {
  id: string
  display_name: string
  vendor: string
  versions: AvailableVersion[]
}

export interface AvailableModelsResponse {
  families: AvailableFamily[]
  unmapped: string[]
  total_raw: number
}

export function fetchAvailableModels(signal?: AbortSignal) {
  return req<AvailableModelsResponse>('GET', '/api/routing/available-models', undefined, signal)
}
