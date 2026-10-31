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
  /** 后端也会下发这两个字段（admin/routing.go 的 availableVersionEntry）。 */
  raw_names?: string[]
  tags?: string[]
}

export interface ModelFamily {
  id: string
  display_name: string
  vendor: string
  versions: AvailableVersion[]
}

/** popular 条目：Go 端 popularModelEntry（admin/routing.go）—— 对象数组,
 * 与桌面端 web/src/api/models.ts 的 PopularModel 同一契约。 */
export interface PopularModel {
  canonical_name: string
  display_name: string
  source: 'policy' | 'usage' | string
  count?: number | null
}

export interface AvailableModelsResponse {
  families: ModelFamily[]
  popular?: PopularModel[]
  /**
   * ★ 后端返回的是**未映射模型名数组**（`[]string`，admin/routing.go 的
   * `unmapped` 收集的是原始模型名字符串），不是数量。此前这里声明成
   * `number` 是一处类型谎言 —— 契约与实现对不上，谁按数量用就会踩空。
   */
  unmapped?: string[]
  total_raw?: number
}

export type ModelStatus = 'active' | 'disabled' | 'deprecated' | 'hidden'

export interface CanonicalModel {
  id: number
  canonical_name: string
  display_name: string | null
  family: string | null
  vendor?: string | null
  modality: string
  context_window: number | null
  parameters_b: number | string | null
  notes: string | null
  status: ModelStatus
  disabled_reason: string | null
  reasoning_caps?: Record<string, unknown> | null
  multimodal_caps?: string[]
  tags: string[]
  alias_count?: number
  offer_count?: number
}

export interface CanonicalModelList { total: number; items: CanonicalModel[] }
export interface CanonicalModelDetail extends CanonicalModel {
  aliases: Array<{ id: number; raw_name: string; quantization: string | null; surface: string | null; status: ModelStatus; notes: string | null }>
  offers: Array<{ provider_id: number; provider_name: string; credential_id: number; credential_label: string; raw_model_name: string; standardized_name: string | null; available: boolean; p95_latency_ms: number | null; success_rate: number | null; input_price: number | null; output_price: number | null }>
}

export interface FeaturedModelSuggestion { name: string; count: number; source?: string }

/** Admin model catalog. Keep this distinct from the routing availability view above. */
export function fetchCanonicalModels(options?: RequestOptions): Promise<CanonicalModelList> {
  return req<CanonicalModelList>('GET', '/api/models', undefined, options)
}

export function fetchCanonicalModel(id: number, options?: RequestOptions): Promise<CanonicalModelDetail> {
  return req<CanonicalModelDetail>('GET', `/api/models/${id}`, undefined, options)
}

export function updateCanonicalModel(
  id: number,
  data: Partial<Pick<CanonicalModel, 'display_name' | 'family' | 'modality' | 'context_window' | 'parameters_b' | 'notes' | 'status' | 'disabled_reason' | 'reasoning_caps' | 'multimodal_caps'>>,
  options?: RequestOptions,
): Promise<CanonicalModel> {
  return req<CanonicalModel>('PATCH', `/api/models/${id}`, data, options)
}

export function fetchFeaturedModels(options?: RequestOptions): Promise<{ featured_models: string[] }> {
  return req<{ featured_models: string[] }>('GET', '/api/routing/featured', undefined, options)
}

export function saveFeaturedModels(featured_models: string[], options?: RequestOptions): Promise<{ status: string }> {
  return req<{ status: string }>('PATCH', '/api/routing/featured', { featured_models, actor: 'admin' }, options)
}

export function fetchFeaturedModelSuggestions(options?: RequestOptions): Promise<{ models: FeaturedModelSuggestion[] }> {
  return req<{ models: FeaturedModelSuggestion[] }>('GET', '/api/routing/featured-models', undefined, options)
}

export function updateCanonicalAlias(
  modelId: number,
  aliasId: number,
  data: Partial<{ raw_name: string; quantization: string | null; surface: string | null; status: ModelStatus; notes: string | null }>,
  options?: RequestOptions,
): Promise<CanonicalModelDetail['aliases'][number]> {
  return req('PATCH', `/api/models/${modelId}/aliases/${aliasId}`, data, options)
}

export function updateCanonicalTags(id: number, tags: string[], options?: RequestOptions): Promise<CanonicalModel> {
  return req<CanonicalModel>('PATCH', `/api/models/${id}/tags`, { tags }, options)
}

export function createCanonicalAlias(
  modelId: number,
  data: { raw_name: string; quantization?: string | null; surface?: string | null; notes?: string | null },
  options?: RequestOptions,
): Promise<CanonicalModelDetail['aliases'][number]> {
  return req('POST', `/api/models/${modelId}/aliases`, data, options)
}

export function fetchAvailableModels(options?: RequestOptions): Promise<AvailableModelsResponse> {
  return req<AvailableModelsResponse>('GET', '/api/routing/available-models', undefined, options)
}
