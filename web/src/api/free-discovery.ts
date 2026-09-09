import { req } from './_core'

// free-discovery.ts — 免费资源自动发现 Admin API 封装 (2026-09-09)。
//
// 后端: admin/free_discovery.go + domains/freediscovery
// 文档: docs/freediscovery-configuration.md §4 (端点速查)。
// 请求/响应字段全部 snake_case, 由后端 struct json tag 决定。
//
// 流程: 模板(含内置预设一键创建) → 触发扫描(/scan, 失败也返回 200+任务体)
//       → 审查发现结果 → 批量导入 free_resource_catalog (冲突策略可选)。

export interface FreeDiscoveryTemplate {
  id: number
  tenant_id: string
  provider_code: string
  display_name: string
  base_url: string
  api_type: string // openai-completions | google-generative-ai | anthropic
  // api_key_env 为 "$VAR" 形式的环境变量引用; 空字符串 = keyless
  api_key_env: string
  models_endpoint: string
  quota_endpoint: string
  tos_url: string
  tos_verdict: string // ok | caution | ambiguous | avoid | unknown
  tos_notes: string
  enabled: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export interface FreeDiscoveryTemplateCreate {
  provider_code: string
  display_name: string
  base_url: string
  api_type?: string
  api_key_env?: string
  /** 明文 Key (可选, 需后端已配 keyring; 无 keyring 时 fail closed) */
  api_key?: string
  models_endpoint?: string
  quota_endpoint?: string
  tos_url?: string
  tos_verdict?: string
  tos_notes?: string
  enabled?: boolean
}

export interface FreeDiscoveryTemplateUpdate {
  display_name?: string
  base_url?: string
  api_type?: string
  api_key_env?: string
  api_key?: string
  models_endpoint?: string
  quota_endpoint?: string
  tos_url?: string
  tos_verdict?: string
  tos_notes?: string
  enabled?: boolean
}

export interface FreeDiscoveryPreset {
  provider_code: string
  display_name: string
  base_url: string
  api_key_env: string
  api_type: string
  tos_verdict: string
  tos_notes: string
}

export interface FreeDiscoveryTask {
  id: number
  tenant_id: string
  template_id: number | null
  provider_code: string
  status: 'pending' | 'running' | 'success' | 'failed'
  trigger_type: 'manual' | 'scheduled' | 'webhook'
  triggered_by: string
  started_at: string | null
  completed_at: string | null
  error_message: string
  models_found: number
  models_imported: number
  created_at: string
  updated_at: string
}

export interface FreeDiscoveryResult {
  id: number
  task_id: number
  tenant_id: string
  provider_code: string
  model_id: string
  display_name: string
  context_window: number
  max_tokens: number
  free_type: string
  monthly_tokens: number
  daily_tokens: number
  pool_key: string
  tos_verdict: string
  tos_notes: string
  import_status: 'pending' | 'imported' | 'skipped' | 'conflict'
  imported_at: string | null
  created_at: string
}

export interface FreeDiscoveryImportSummary {
  imported: number
  skipped: number
  conflicted: number
  failed: number
}

export type FreeDiscoveryConflictPolicy = 'skip' | 'overwrite' | 'merge'

// ── 模板 ────────────────────────────────────────────────────────────────

export function listFreeDiscoveryTemplates(enabledOnly?: boolean): Promise<{ templates: FreeDiscoveryTemplate[] }> {
  const q = enabledOnly ? '?enabled=true' : ''
  return req('GET', `/api/free-discovery/templates${q}`)
}

export function createFreeDiscoveryTemplate(body: FreeDiscoveryTemplateCreate): Promise<FreeDiscoveryTemplate> {
  return req('POST', '/api/free-discovery/templates', body)
}

export function updateFreeDiscoveryTemplate(id: number, body: FreeDiscoveryTemplateUpdate): Promise<FreeDiscoveryTemplate> {
  return req('PATCH', `/api/free-discovery/templates/${id}`, body)
}

export function deleteFreeDiscoveryTemplate(id: number): Promise<{ deleted: boolean; id: number }> {
  return req('DELETE', `/api/free-discovery/templates/${id}`)
}

export function getFreeDiscoveryPresets(): Promise<{ presets: FreeDiscoveryPreset[] }> {
  return req('GET', '/api/free-discovery/templates/presets')
}

/** 整体直传 Orbi pi-providers JSON: {"providers": {"groq": {...}}} */
export function importFreeDiscoveryOrbiTemplate(
  file: { providers: Record<string, { baseUrl: string; api: string; apiKey: string }> },
): Promise<{ created: number; failed: number; errors: string[] }> {
  return req('POST', '/api/free-discovery/templates/import-orbi', file)
}

// ── 任务与结果 ──────────────────────────────────────────────────────────

/** 触发扫描; 上游失败时任务 status=failed 且仍返回 200 (UI 展示 error_message) */
export function scanFreeDiscovery(templateId: number): Promise<FreeDiscoveryTask> {
  return req('POST', '/api/free-discovery/scan', { template_id: templateId })
}

export function listFreeDiscoveryTasks(limit = 50): Promise<{ tasks: FreeDiscoveryTask[] }> {
  return req('GET', `/api/free-discovery/tasks?limit=${limit}`)
}

export function getFreeDiscoveryTask(id: number): Promise<FreeDiscoveryTask> {
  return req('GET', `/api/free-discovery/tasks/${id}`)
}

export function listFreeDiscoveryTaskResults(
  taskId: number,
  status: 'pending' | 'all' = 'pending',
): Promise<{ results: FreeDiscoveryResult[] }> {
  return req('GET', `/api/free-discovery/tasks/${taskId}/results?status=${status}`)
}

// ── 批量导入 ────────────────────────────────────────────────────────────

export function importFreeDiscoveryResults(body: {
  task_id: number
  // 省略 result_ids = 导入该任务全部 pending
  result_ids?: number[]
  conflict_policy?: FreeDiscoveryConflictPolicy
}): Promise<FreeDiscoveryImportSummary> {
  return req('POST', '/api/free-discovery/import', body)
}
