import { req } from './_core'

// agents.ts — Phase 4 frontend integration (2026-06-25)
// Agent Registry surfaces backed by /api/agents (admin/agents.go).
// Exposes the unified apihub.Asset registry as paged list + per-id
// detail + link creation (depends_on / calls / similar_to edges).
// All routes require tenant_admin+; super_admin can pass ?tenant=.

export type AssetKind = 'llm_endpoint' | 'mcp_server' | 'agent'
export type AssetHealthState = 'healthy' | 'degraded' | 'down' | 'unknown'
export type RelationType = 'depends_on' | 'calls' | 'similar_to'

export interface AgentAsset {
  kind: AssetKind
  ref_id: number
  tenant_id: string
  name: string
  owner?: string
  team?: string | null
  cost_center?: string | null
  tags?: Record<string, string>
  health_state: AssetHealthState
  version?: string
  registered_at: string
  last_seen_at?: string
  metadata?: Record<string, any>
}

export interface AgentListResponse {
  agents: AgentAsset[]
  total: number
  limit: number
  offset: number
}

export interface AgentDetailResponse {
  agent: AgentAsset
}

export interface AgentLinkResponse {
  link: {
    source_id: number
    target_id: number
    link_type: RelationType
    created_at?: string
  }
}

export function getAgents(params: {
  kind?: AssetKind | 'all'
  tenant?: string
  limit?: number
  offset?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.kind) q.set('kind', params.kind)
  if (params.tenant) q.set('tenant', params.tenant)
  if (params.limit != null) q.set('limit', String(params.limit))
  if (params.offset != null) q.set('offset', String(params.offset))
  const qs = q.toString()
  return req<AgentListResponse>('GET', '/api/agents' + (qs ? '?' + qs : ''))
}

export function getAgent(id: number) {
  return req<AgentDetailResponse>('GET', `/api/agents/${id}`)
}

export function linkAgent(sourceId: number, targetId: number, linkType: RelationType) {
  return req<AgentLinkResponse>('POST', `/api/agents/${sourceId}/link`, {
    target_id: targetId,
    link_type: linkType,
  })
}