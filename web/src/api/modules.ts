import { req } from './_core'

// modules.ts — Module management API (enterprise feature module system).
// Each ModuleDefinition represents a capability module with enable/disable
// toggle, rich metadata, and integration configuration.

export interface ModuleIntegration {
  type: string
  label: string
  description: string
  doc_url: string
}

export interface ModuleDefinition {
  key: string
  name: string
  description: string
  capabilities: string[]
  icon: string
  category: string
  setting_key: string
  config_keys: string[]
  docs_url: string
  danger_level: number
  integration?: ModuleIntegration
  /** Soft-dependency list: other module keys that should be enabled. UI hint only. */
  requires?: string[]
}

export interface ModuleWithStatus extends ModuleDefinition {
  enabled: boolean
  source: string
  /** True when all required modules are enabled. */
  requirements_met: boolean
  /** Required module keys that are NOT enabled (empty when requirements_met=true). */
  missing_requirements?: string[]
}

export interface ModuleDetail {
  module: ModuleWithStatus
  config: Record<string, {
    value: any
    source: string
    spec: any
  }>
}

/** List all feature modules with their current enabled/disabled status. */
export function listModules() {
  return req<{ items: ModuleWithStatus[] }>('GET', '/api/admin/modules')
}

/** Get a single module's full detail including config values. */
export function getModule(key: string) {
  return req<ModuleDetail>('GET', `/api/admin/modules/${key}`)
}

/** Toggle a module's enabled/disabled state. */
export function toggleModule(key: string, enabled: boolean) {
  return req<{ status: string; enabled: boolean; module: string; message: string }>(
    'PUT', `/api/admin/modules/${key}/toggle`, { enabled })
}

/** Test the integration (e.g., send a probe message to feishu webhook). */
export function testModule(key: string) {
  return req<{
    reachable: boolean
    status_code?: number
    lark_code?: number
    lark_msg?: string
    response_ms?: number
    message?: string
    error?: string
  }>('POST', `/api/admin/modules/${key}/test`)
}

/** Get the lightweight config summary (for module dashboard cards). */
export function getModuleConfig(key: string) {
  return req<Record<string, any>>('GET', `/api/admin/modules/${key}/config`)
}

// ── 飞书机器人运营面（routing rules + send log）─────────────

/** 飞书路由规则（按 OpenID 维度） */
export interface FeishuRouteRule {
  id: number
  tenant_id: string
  open_id: string
  display_name: string
  user_role: 'admin' | 'member' | 'auditor'
  risk_levels: string[]
  priority: number
  enabled: boolean
  note: string
  created_by?: string
  created_at: string
  updated_at: string
}

export interface FeishuRouteRuleCreate {
  open_id: string
  display_name?: string
  user_role?: 'admin' | 'member' | 'auditor'
  risk_levels?: string[]
  priority?: number
  enabled?: boolean
  note?: string
  tenant_id?: string
}

export function listFeishuRoutingRules(params?: {
  tenant_id?: string
  enabled_only?: boolean
  user_role?: string
  limit?: number
}) {
  const qs = new URLSearchParams()
  if (params?.tenant_id) qs.set('tenant_id', params.tenant_id)
  if (params?.enabled_only) qs.set('enabled_only', 'true')
  if (params?.user_role) qs.set('user_role', params.user_role)
  if (params?.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<{ items: FeishuRouteRule[]; count: number; tenant_id: string }>(
    'GET', `/api/admin/feishubot/routing-rules${s ? '?' + s : ''}`,
  )
}

export function createFeishuRoutingRule(body: FeishuRouteRuleCreate) {
  return req<FeishuRouteRule & { id: number; created_at: string; updated_at: string }>(
    'POST', '/api/admin/feishubot/routing-rules', body,
  )
}

export function updateFeishuRoutingRule(id: number, body: Partial<FeishuRouteRuleCreate> & {
  display_name?: string
  priority?: number
  enabled?: boolean
  note?: string
  risk_levels?: string[]
  user_role?: 'admin' | 'member' | 'auditor'
}) {
  return req<{ id: number; updated: boolean }>(
    'PUT', `/api/admin/feishubot/routing-rules/${id}`, body,
  )
}

export function deleteFeishuRoutingRule(id: number) {
  return req<{ id: number; deleted: boolean }>(
    'DELETE', `/api/admin/feishubot/routing-rules/${id}`,
  )
}

export interface FeishuSendLogEntry {
  id: number
  tenant_id: string
  event_type: 'alert' | 'approval' | 'command'
  event_id?: string
  recipients_count: number
  success: boolean
  error_code?: number
  error_message?: string
  latency_ms?: number
  deduped: boolean
  rate_limited: boolean
  created_at: string
}

export function listFeishuSendLog(params?: {
  tenant_id?: string
  event_type?: 'alert' | 'approval' | 'command'
  success?: boolean
  limit?: number
}) {
  const qs = new URLSearchParams()
  if (params?.tenant_id) qs.set('tenant_id', params.tenant_id)
  if (params?.event_type) qs.set('event_type', params.event_type)
  if (params?.success !== undefined) qs.set('success', String(params.success))
  if (params?.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<{ items: FeishuSendLogEntry[]; count: number; tenant_id: string }>(
    'GET', `/api/admin/feishubot/send-log${s ? '?' + s : ''}`,
  )
}