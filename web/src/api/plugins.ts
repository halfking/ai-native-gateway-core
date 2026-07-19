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
