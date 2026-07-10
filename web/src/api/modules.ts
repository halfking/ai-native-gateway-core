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

export interface ModuleDependency {
  key: string
  name: string
  icon?: string
  required: boolean
  description: string
  /** Runtime status filled by the backend (true when this dependency is currently enabled). */
  enabled?: boolean
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
  dependencies?: ModuleDependency[]
}

export interface ModuleWithStatus extends ModuleDefinition {
  enabled: boolean
  source: string
  /** True when all required dependencies are enabled (no blocked dependencies). */
  can_toggle_enabled: boolean
  /** Human-readable reason why this module cannot be enabled. */
  blocked_reason?: string
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

export interface ToggleModuleResponse {
  status: string
  enabled: boolean
  module: string
  message: string
  /** When cascade=true and required deps were auto-enabled, this lists their module keys. */
  cascaded?: string[]
}

/** Toggle a module's enabled/disabled state. */
export function toggleModule(key: string, enabled: boolean, opts: { cascade?: boolean } = {}) {
  const qs = opts.cascade ? '?cascade=true' : ''
  return req<ToggleModuleResponse>(
    'PUT', `/api/admin/modules/${key}/toggle${qs}`, { enabled })
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
