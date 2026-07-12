import { req } from './_core'

// promptInjection.ts — Prompt-injection detection API.
//
// Mirrors `admin/prompt_injection_handler.go`. Exposes:
//   - PromptInjectionPolicy (GET/PUT /api/admin/prompt-injection/policy)
//   - PromptInjectionRule  (GET/POST/PUT/DELETE /api/admin/prompt-injection/rules[/:id])
//   - Rule toggle helper    (POST /api/admin/prompt-injection/rules/:id/toggle)
//   - All 15 risk categories + label/el-tag-type metadata used by both the
//     full admin view (PromptInjectionSettingsView.vue) and the embedded
//     config panel (PromptInjectionConfigPanel.vue).
//
// Keep this file in sync with admin/prompt_injection_handler.go.
// Backend columns are declared in sql/migrations/startup/{315,364}_prompt_injection_*.sql.

// ---------------------------------------------------------------------------
// Constants — risk categories
// ---------------------------------------------------------------------------

/** Canonical 15-category enum (see migration 364_prompt_injection_enhanced.sql). */
export type DetectionCategory =
  | 'role_hijack'
  | 'instruction_override'
  | 'instruction_leak'
  | 'jailbreak'
  | 'encoding_bypass'
  | 'injection_marker'
  | 'multi_turn_attack'
  | 'resource_exhaustion'
  | 'data_exfiltration'
  | 'social_engineering'
  | 'prompt_leaking'
  | 'payload_smuggling'
  | 'unicode_obfuscation'
  | 'context_manipulation'
  | 'tool_abuse'

/** Action enum applied to a detection by the severity matrix. */
export type InjectionAction =
  | 'pass' | 'log' | 'warn' | 'replace' | 'redact' | 'remove'
  | 'reject' | 'terminate' | 'approve' | 'quarantine' | 'block'

/** Detection mode for the tenant policy. */
export type DetectionMode = 'observe' | 'enforce'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface PromptInjectionRule {
  id: number
  rule_name: string
  rule_type: 'basic' | 'advanced'
  category: string
  category_new: string
  pattern: string
  description: string
  severity: number
  enabled: boolean
  case_sensitive: boolean
  is_system: boolean
  action_override: string
  tags: string[]
  examples: string[]
  created_at: string
  updated_at: string
}

export interface PromptInjectionPolicy {
  id: number
  tenant_id: string
  enabled: boolean
  detection_mode: DetectionMode
  enable_basic_rules: boolean
  enable_advanced_rules: boolean
  enable_heuristics: boolean
  enable_ml_model: boolean
  enable_llm_detection: boolean
  enable_canary_detection: boolean
  enable_vector_similarity: boolean
  llm_engine_id: number | null
  content_replacement: string
  max_input_length: number
  auto_learn_enabled: boolean
  detection_timeout_ms: number
  score_threshold_log: number
  score_threshold_warn: number
  score_threshold_sanitize: number
  score_threshold_block: number
  action_on_low_risk: string
  action_on_medium_risk: string
  action_on_high_risk: string
  whitelist_patterns: string[]
  whitelist_users: string[]
  notify_on_detection: boolean
  notification_webhook: string
  notification_email: string
  total_detections: number
  total_blocks: number
  last_detection_at: string | null
  created_at: string
  updated_at: string
}

export interface PromptInjectionRuleListResponse {
  rules: PromptInjectionRule[]
  count: number
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

export function getPolicy() {
  return req<PromptInjectionPolicy>('GET', '/api/admin/prompt-injection/policy')
}

export function updatePolicy(policy: PromptInjectionPolicy) {
  return req<{ message: string; policy_id: number }>('PUT', '/api/admin/prompt-injection/policy', policy)
}

export function listRules(opts: { category?: string; type?: 'basic' | 'advanced'; search?: string } = {}) {
  const params: Record<string, string> = {}
  if (opts.category) params.category = opts.category
  if (opts.type) params.type = opts.type
  if (opts.search) params.search = opts.search
  const qs = new URLSearchParams(params).toString()
  return req<PromptInjectionRuleListResponse>(
    'GET',
    '/api/admin/prompt-injection/rules' + (qs ? '?' + qs : ''),
  )
}

export function toggleRule(id: number | string, enabled: boolean) {
  return req<{ message: string }>(
    'POST',
    `/api/admin/prompt-injection/rules/${id}/toggle`,
    { enabled },
  )
}

/**
 * Fields the backend actually accepts on PUT /api/admin/prompt-injection/rules/{id}.
 *
 * Source of truth: admin/prompt_injection_handler.go:updateRule (lines 392-421).
 * Fields NOT here (rule_name, category, category_new, is_system, rule_type) are
 * either immutable or system-only and would be silently ignored — keeping them
 * out of the request type prevents drift.
 */
export interface UpdateRulePatch {
  pattern?: string
  description?: string
  severity?: number
  enabled?: boolean
  case_sensitive?: boolean
  action_override?: string
  tags?: string[]
  examples?: string[]
}

export function updateRule(id: number | string, patch: UpdateRulePatch) {
  return req<{ message: string }>('PUT', `/api/admin/prompt-injection/rules/${id}`, patch)
}

// ---------------------------------------------------------------------------
// Engines, canary tokens, severity matrix, stats, detections
// (Full-page-only endpoints; not used by PromptInjectionConfigPanel.)
// ---------------------------------------------------------------------------

export interface LLMEngine {
  id: number
  tenant_id: string
  engine_name: string
  description?: string
  model_canonical_id?: number | null
  model_name?: string
  credential_id?: number | null
  temperature?: number
  max_tokens?: number
  timeout_ms?: number
  max_retries?: number
  system_prompt?: string
  detection_prompt?: string
  priority?: number
  enabled?: boolean
  total_calls?: number
  total_detections?: number
  avg_latency_ms?: number
  error_count?: number
  last_called_at?: string | null
  created_at?: string
  updated_at?: string
}
export interface CreateEngineInput {
  engine_name: string
  description?: string
  model_canonical_id?: number | null
  temperature?: number
  max_tokens?: number
  timeout_ms?: number
  max_retries?: number
  system_prompt?: string
  detection_prompt?: string
  priority?: number
  enabled?: boolean
}

export function listEngines() {
  return req<{ engines: LLMEngine[]; count: number }>('GET', '/api/admin/prompt-injection/engines')
}
export function getEngine(id: number | string) {
  return req<LLMEngine>('GET', `/api/admin/prompt-injection/engines/${id}`)
}
export function createEngine(payload: CreateEngineInput) {
  return req<{ message: string; engine_id: number }>('POST', '/api/admin/prompt-injection/engines', payload)
}
export function updateEngine(id: number | string, patch: Partial<LLMEngine>) {
  return req<{ message: string }>('PUT', `/api/admin/prompt-injection/engines/${id}`, patch)
}
export function removeEngine(id: number | string) {
  return req<{ message: string }>('DELETE', `/api/admin/prompt-injection/engines/${id}`)
}

export interface CanaryToken {
  id: number
  tenant_id: string
  token_value?: string
  token_type?: string
  token_name?: string
  description?: string
  leak_action?: string
  notify_on_leak?: boolean
  active?: boolean
  expires_at?: string | null
  times_injected?: number
  times_leaked?: number
  last_leaked_at?: string | null
  created_at?: string
}
export interface CreateCanaryInput {
  token_name?: string
  token_type?: string
  token_value?: string
  description?: string
  leak_action?: string
  notify_on_leak?: boolean
  active?: boolean
  expires_at?: string | null
}

export function listCanaryTokens() {
  return req<{ tokens: CanaryToken[]; count: number }>('GET', '/api/admin/prompt-injection/canary-tokens')
}
export function createCanaryToken(payload: CreateCanaryInput) {
  return req<{ message: string; token_id: number; token_value: string }>('POST', '/api/admin/prompt-injection/canary-tokens', payload)
}
export function updateCanaryToken(id: number | string, patch: Partial<CanaryToken>) {
  return req<{ message: string }>('PUT', `/api/admin/prompt-injection/canary-tokens/${id}`, patch)
}
export function removeCanaryToken(id: number | string) {
  return req<{ message: string }>('DELETE', `/api/admin/prompt-injection/canary-tokens/${id}`)
}

export interface SeverityAction {
  id: number
  tenant_id: string
  severity_level: string
  observe_action?: string
  enforce_action?: string
  require_approval?: boolean
  approval_timeout_minutes?: number
  notify_on_detect?: boolean
  notify_channels?: string[]
  affect_session_health?: boolean
  session_health_penalty?: number
  terminate_session_on_repeat?: boolean
  repeat_threshold?: number
}
export function getSeverityMatrix() {
  return req<{ matrix: SeverityAction[] }>('GET', '/api/admin/prompt-injection/severity-matrix')
}
export function updateSeverityMatrix(matrix: SeverityAction[]) {
  return req<{ message: string }>('PUT', '/api/admin/prompt-injection/severity-matrix', matrix)
}

export interface DetectionStats {
  total_detections?: number
  blocked_count?: number
  critical_count?: number
  high_count?: number
  medium_count?: number
  low_count?: number
  approval_count?: number
  replaced_count?: number
  terminated_count?: number
  canary_leak_count?: number
  avg_score?: number
  max_score?: number
  avg_llm_confidence?: number
  affected_sessions?: number
}
export function listStats() {
  return req<DetectionStats>('GET', '/api/admin/prompt-injection/stats')
}

export interface DetectionListParams {
  page?: number
  page_size?: number
  risk_level?: string
  action?: string
  session_key?: string
  blocked?: string
  category?: string
}
export interface DetectionListResponse {
  detections: any[]
  page: number
  page_size: number
  total: number
}
export function listDetections(params: DetectionListParams = {}) {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== '' && v !== null && v !== undefined) qs.set(k, String(v))
  }
  return req<DetectionListResponse>('GET', '/api/admin/prompt-injection/detections' + (qs.toString() ? `?${qs}` : ''))
}

// ---------------------------------------------------------------------------
// Display metadata — kept in sync with PromptInjectionSettingsView.vue.
// ---------------------------------------------------------------------------

export interface CategoryMeta {
  value: DetectionCategory
  /** Translation key in sessions.config namespace, e.g. promptInjectionCategoryRoleHijack. */
  i18nKey: string
  /** Element-plus tag type used in the rules table. */
  tagType: 'danger' | 'warning' | 'info' | ''
  /** Hard-coded Chinese fallback when translation key is missing. */
  zhFallback: string
}

/** Canonical 15-category metadata, ordered by severity / typical impact. */
export const CATEGORIES: readonly CategoryMeta[] = [
  { value: 'role_hijack',         i18nKey: 'promptInjectionCategoryRoleHijack',         tagType: 'danger',  zhFallback: '角色劫持' },
  { value: 'instruction_override', i18nKey: 'promptInjectionCategoryInstructionOverride', tagType: 'danger',  zhFallback: '指令覆盖' },
  { value: 'instruction_leak',    i18nKey: 'promptInjectionCategoryInstructionLeak',    tagType: 'warning', zhFallback: '指令泄漏' },
  { value: 'jailbreak',           i18nKey: 'promptInjectionCategoryJailbreak',          tagType: 'danger',  zhFallback: '越狱攻击' },
  { value: 'encoding_bypass',     i18nKey: 'promptInjectionCategoryEncodingBypass',     tagType: 'info',    zhFallback: '编码绕过' },
  { value: 'injection_marker',    i18nKey: 'promptInjectionCategoryInjectionMarker',    tagType: 'danger',  zhFallback: '注入标记' },
  { value: 'multi_turn_attack',   i18nKey: 'promptInjectionCategoryMultiTurnAttack',    tagType: 'warning', zhFallback: '多轮攻击' },
  { value: 'resource_exhaustion', i18nKey: 'promptInjectionCategoryResourceExhaustion', tagType: 'info',    zhFallback: '资源耗尽' },
  { value: 'data_exfiltration',   i18nKey: 'promptInjectionCategoryDataExfiltration',   tagType: 'danger',  zhFallback: '数据窃取' },
  { value: 'social_engineering',  i18nKey: 'promptInjectionCategorySocialEngineering',  tagType: 'warning', zhFallback: '社会工程' },
  { value: 'prompt_leaking',      i18nKey: 'promptInjectionCategoryPromptLeaking',      tagType: 'warning', zhFallback: '提示词泄漏' },
  { value: 'payload_smuggling',   i18nKey: 'promptInjectionCategoryPayloadSmuggling',   tagType: 'info',    zhFallback: 'Payload走私' },
  { value: 'unicode_obfuscation', i18nKey: 'promptInjectionCategoryUnicodeObfuscation', tagType: 'info',    zhFallback: 'Unicode混淆' },
  { value: 'context_manipulation', i18nKey: 'promptInjectionCategoryContextManipulation', tagType: 'warning', zhFallback: '上下文操纵' },
  { value: 'tool_abuse',          i18nKey: 'promptInjectionCategoryToolAbuse',          tagType: 'danger',  zhFallback: '工具滥用' },
] as const

/** Legacy pre-364 categories still used by some seeded rules. */
const LEGACY_CATEGORY_LABELS: Record<string, { tagType: CategoryMeta['tagType']; zhFallback: string; i18nKey?: string }> = {
  dan:    { tagType: 'danger',  zhFallback: 'DAN越狱' },
  bypass: { tagType: 'info',    zhFallback: '绕过技术' },
}

/** Resolve display info for a category_new or legacy category value. */
export function getCategoryMeta(raw: string): { tagType: CategoryMeta['tagType']; i18nKey: string; zhFallback: string } {
  const found = CATEGORIES.find((c) => c.value === raw)
  if (found) return { tagType: found.tagType, i18nKey: found.i18nKey, zhFallback: found.zhFallback }
  const legacy = LEGACY_CATEGORY_LABELS[raw]
  if (legacy) return { tagType: legacy.tagType, i18nKey: legacy.i18nKey ?? 'promptInjectionCategoryLegacy', zhFallback: legacy.zhFallback }
  return { tagType: '', i18nKey: 'promptInjectionCategoryUnknown', zhFallback: raw }
}

/** Element-plus tag type for a 1-10 severity score. */
export function getSeverityTagType(severity: number): 'danger' | 'warning' | 'info' | 'success' {
  if (severity >= 9) return 'danger'
  if (severity >= 7) return 'warning'
  if (severity >= 5) return 'info'
  return 'success'
}

/** Display label for an action enum. */
export const ACTION_LABELS: Record<string, string> = {
  block: '阻断',
  reject: '拒绝',
  terminate: '终止',
  approve: '审批',
  replace: '替换',
  redact: '脱敏',
  remove: '移除',
  sanitize: '清洗',
  quarantine: '隔离',
  warn: '警告',
  log: '记录',
  pass: '放行',
}