// plugins.ts — plugin-runtime 的 web client。
export interface NavEntry {
  plugin_id: string
  plugin_version: string
  page_path: string
  page_type: 'settings' | 'data'
  nav_group: string
  label_key: string
  icon?: string
  super: boolean
  platform_ops: boolean
  tenant_only: boolean
  order: number
  route_url: string
}

export async function fetchPluginNav(): Promise<NavEntry[]> {
  const resp = await fetch('/api/v1/plugin-nav', { credentials: 'include' })
  if (!resp.ok) return []
  const body = await resp.json().catch(() => ({ items: [] }))
  return (body.items ?? []) as NavEntry[]
}

export interface SettingsProperty {
  type: 'boolean' | 'integer' | 'number' | 'string'
  title?: string
  title_key?: string
  description?: string
  description_key?: string
  enum?: Array<string | number | boolean>
  minimum?: number
  maximum?: number
  default?: unknown
}

export interface SettingsSchema {
  type: 'object'
  properties: Record<string, SettingsProperty>
  'x-order'?: string[]
}

export interface PluginSettingsPayload {
  plugin_id: string
  settings_schema: SettingsSchema | null
  config?: Record<string, unknown>
}

/** Fetch schema-driven settings for one plugin (host-rendered form; no plugin-specific UI). */
export async function fetchPluginSettings(pluginId: string): Promise<PluginSettingsPayload | null> {
  const resp = await fetch(`/api/v1/plugins/${encodeURIComponent(pluginId)}/settings`, {
    credentials: 'include',
  })
  if (!resp.ok) return null
  return (await resp.json().catch(() => null)) as PluginSettingsPayload | null
}
