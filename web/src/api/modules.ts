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

export interface ModuleDependencyStatus {
  key: string
  name: string
  enabled: boolean
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
  dependencies: string[]
  integration?: ModuleIntegration
}

export interface ModuleWithStatus extends ModuleDefinition {
  enabled: boolean
  source: string
  dependency_statuses: ModuleDependencyStatus[]
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
