// ops.ts — Operations Platform API (2026-07-11 refactor)
//
// All endpoints are mounted under /api/admin/* by the Echo bridge.
// List endpoints return { items, total, offset, limit } wrappers.
// All identifiers use snake_case for consistency with backend JSON tags.

import { req } from './_core'

// ────────────────────────────────────────────────────────────────────────────
// Common wrapper types
// ────────────────────────────────────────────────────────────────────────────

export interface ListResponse<T> {
  items: T[]
  total: number
  offset: number
  limit: number
}

// 后端某些模块 (fault/center) 用专属 key (rules/events/instances/...)
// 这个辅助函数从响应中抽取数组，统一接口
function extractList<T>(data: any): T[] {
  if (Array.isArray(data)) return data as T[]
  if (!data || typeof data !== 'object') return []
  const candidates = ['items', 'rules', 'events', 'instances', 'projects', 'sessions', 'reviews', 'licenses', 'releases', 'devices']
  for (const k of candidates) {
    if (Array.isArray(data[k])) return data[k] as T[]
  }
  return []
}

// ────────────────────────────────────────────────────────────────────────────
// License Management
// ────────────────────────────────────────────────────────────────────────────

export interface License {
  id: number
  license_key: string
  customer: string
  customer_email?: string
  max_devices: number
  active_devices: number
  expires_at: string
  status: 'active' | 'expired' | 'revoked'
  created_at: string
  updated_at: string
  subscription_tier?: string
}

export interface LicenseDevice {
  id: number
  license_id: number
  device_id: string
  hostname: string
  activated_at: string
  last_seen?: string
  status: string
}

export interface OfflineActivationRequest {
  id: number
  license_key: string
  device_id: string
  request_code: string
  status: 'pending' | 'approved' | 'rejected'
  created_at: string
  approved_at?: string
  activation_code?: string
}

export async function getLicenses(offset = 0, limit = 100): Promise<License[]> {
  const data = await req<any>('GET', `/api/admin/licenses?offset=${offset}&limit=${limit}`)
  return extractList<License>(data)
}

export async function createLicense(data: {
  customer: string
  customer_name?: string
  customer_email?: string
  max_devices: number
  expires_at: string
  subscription_tier?: string
}): Promise<License> {
  return req<License>('POST', '/api/admin/licenses', data)
}

export async function revokeLicense(licenseKey: string): Promise<void> {
  return req<void>('POST', `/api/admin/licenses/${encodeURIComponent(licenseKey)}/revoke`)
}

export async function getLicenseDevices(licenseKey: string): Promise<LicenseDevice[]> {
  return req<LicenseDevice[]>('GET', `/api/admin/licenses/${encodeURIComponent(licenseKey)}/devices`)
}

export async function getOfflineActivationRequests(): Promise<OfflineActivationRequest[]> {
  return req<OfflineActivationRequest[]>('GET', '/api/admin/licenses/offline-requests')
}

export async function approveOfflineActivation(id: string): Promise<{ activation_code: string }> {
  return req<{ activation_code: string }>('POST', `/api/admin/licenses/offline-requests/${encodeURIComponent(id)}/approve`)
}

export async function rejectOfflineActivation(id: string, reason: string): Promise<void> {
  return req<void>('POST', `/api/admin/licenses/offline-requests/${encodeURIComponent(id)}/reject`, { reason })
}

// ────────────────────────────────────────────────────────────────────────────
// Fault Management
// ────────────────────────────────────────────────────────────────────────────

export interface FaultEvent {
  id: number
  rule_id: number
  rule_name: string
  severity: 'critical' | 'warning' | 'info' | 'error'
  status: 'new' | 'acknowledged' | 'resolving' | 'resolved' | 'ignored'
  title: string
  description: string
  message?: string
  source?: string
  context?: Record<string, unknown>
  detected_at: string
  resolved_at?: string
  created_at: string
  updated_at?: string
}

export interface FaultRule {
  id: number
  name: string
  description: string
  severity: 'critical' | 'warning' | 'info' | 'error'
  enabled: boolean
  metric: string
  operator: 'gte' | 'lte' | 'eq' | 'ne'
  threshold: number
  duration: string
  action: 'restart' | 'scale_up' | 'notify' | 'failover' | 'auto_recover' | 'run_script'
  cooldown?: string
  condition?: string // legacy alias = "metric operator threshold"
  auto_fix?: boolean  // legacy alias = action === 'auto_recover'
  created_at: string
  updated_at?: string
}

export interface FaultStats {
  total_events: number
  open_events: number
  resolved_24h: number
  resolved_events: number // alias for resolved_24h
  avg_resolve_mins: number
  avg_resolution_time_minutes: number // alias for avg_resolve_mins
  by_severity?: Record<string, number>
  by_source?: Record<string, number>
}

export async function getFaultEvents(offset = 0, limit = 100): Promise<FaultEvent[]> {
  const data = await req<any>('GET', `/api/admin/faults/events?offset=${offset}&limit=${limit}`)
  return extractList<FaultEvent>(data)
}

export async function getFaultRules(offset = 0, limit = 100): Promise<FaultRule[]> {
  const data = await req<any>('GET', `/api/admin/faults/rules?offset=${offset}&limit=${limit}`)
  return extractList<FaultRule>(data)
}

export async function createFaultRule(data: Partial<FaultRule>): Promise<FaultRule> {
  return req<FaultRule>('POST', '/api/admin/faults/rules', data)
}

export async function updateFaultRule(id: number, data: Partial<FaultRule>): Promise<FaultRule> {
  return req<FaultRule>('PUT', `/api/admin/faults/rules/${id}`, data)
}

export async function deleteFaultRule(id: number): Promise<void> {
  return req<void>('DELETE', `/api/admin/faults/rules/${id}`)
}

export async function getFaultStats(): Promise<FaultStats> {
  const s = await req<FaultStats>('GET', '/api/admin/faults/stats')
  // populate aliases for backwards compatibility with old frontend fields
  return {
    ...s,
    resolved_events: s.resolved_24h ?? 0,
    avg_resolution_time_minutes: s.avg_resolve_mins ?? 0,
  }
}

export async function acknowledgeFaultEvent(id: number, actor: string): Promise<void> {
  return req<void>('POST', `/api/admin/faults/events/${id}/acknowledge`, { actor })
}

export async function resolveFaultEvent(id: number, actor: string): Promise<void> {
  return req<void>('POST', `/api/admin/faults/events/${id}/resolve`, { actor })
}

// ────────────────────────────────────────────────────────────────────────────
// Auto Update Management
// ────────────────────────────────────────────────────────────────────────────

export type ReleaseStatus = 'draft' | 'published' | 'archived'
export type ReleaseChannel = 'stable' | 'beta' | 'canary'

export interface Release {
  id: number
  version: string
  build_seq: number
  channel: ReleaseChannel
  status: ReleaseStatus
  title: string
  description: string
  changelog: string
  release_notes?: string // alias for changelog
  image_tag: string
  download_url?: string // alias for image_tag
  image_digest?: string
  checksum?: string // alias for image_digest
  min_version?: string
  mandatory: boolean
  rollout_percentage?: number
  created_by: string
  created_at: string
  published_at?: string
}

export interface UpgradeLog {
  id?: number
  release_id?: number
  instance_id: string
  from_version?: string
  to_version?: string
  version: string
  status: 'pending' | 'downloading' | 'ready_to_restart' | 'upgrading' | 'success' | 'failed' | 'rolled_back'
  started_at: string
  completed_at?: string
  error_message?: string
  error?: string // alias for error_message
  retry_count: number
}

export async function getReleases(offset = 0, limit = 100): Promise<Release[]> {
  const data = await req<any>('GET', `/api/admin/releases?offset=${offset}&limit=${limit}`)
  // Backend returns raw Release; we add derived `status` and aliases
  return extractList<Release>(data).map((r) => decorateRelease(r))
}

export async function createRelease(data: Partial<Release>): Promise<Release> {
  return req<Release>('POST', '/api/admin/releases', data)
}

export async function publishRelease(version: string, rolloutPercentage?: number): Promise<Release> {
  return req<Release>('POST', `/api/admin/releases/${encodeURIComponent(version)}/publish`, {
    rollout_percentage: rolloutPercentage ?? 100,
  })
}

export async function rollbackRelease(targetVersion: string): Promise<void> {
  return req<void>('POST', '/api/admin/releases/rollback', { target_version: targetVersion })
}

export async function getUpgradeLogs(releaseId?: number): Promise<UpgradeLog[]> {
  // 后端按 instance_id 过滤；releaseId 暂时忽略
  const data = await req<any>('GET', '/api/admin/releases/upgrade-logs')
  return extractList<UpgradeLog>(data)
}

function decorateRelease(r: Release): Release {
  const status: ReleaseStatus = r.published_at ? 'published' : 'draft'
  return {
    ...r,
    status,
    release_notes: r.changelog,
    download_url: r.image_tag,
    checksum: r.image_digest,
    rollout_percentage: 100,
  }
}

// ────────────────────────────────────────────────────────────────────────────
// Center Management (Instance Operations)
// ────────────────────────────────────────────────────────────────────────────

export interface CenterInstance {
  instance_id: string
  id?: string // alias for instance_id (legacy)
  hostname: string
  ip_address?: string
  region?: string
  version: string
  build_seq?: number
  status: 'online' | 'offline' | 'degraded'
  last_heartbeat: string
  started_at?: string
  created_at?: string // alias for started_at
  cpu_usage?: number
  memory_usage?: number
  disk_usage?: number
  uptime_seconds?: number
}

export interface HeartbeatHistory {
  timestamp: string
  cpu_usage?: number
  memory_usage?: number
  disk_usage?: number
  uptime_secs?: number
  num_goroutine?: number
  alloc_mb?: number
  status?: string
  instance_id?: string
}

export interface CenterStats {
  total_instances: number
  online_count: number
  offline_count: number
  degraded_count: number
}

export async function getCenterInstances(): Promise<CenterInstance[]> {
  const data = await req<any>('GET', '/api/admin/center/instances')
  return extractList<CenterInstance>(data).map(decorateInstance)
}

export async function getCenterStats(): Promise<CenterStats> {
  return req<CenterStats>('GET', '/api/admin/center/stats')
}

export async function getHeartbeatHistory(instanceId: string, hours = 24): Promise<HeartbeatHistory[]> {
  const since = new Date(Date.now() - hours * 3600 * 1000).toISOString()
  const data = await req<any>(
    'GET',
    `/api/admin/center/instances/${encodeURIComponent(instanceId)}/heartbeat?since=${since}&limit=200`,
  )
  return extractList<HeartbeatHistory>(data)
}

export async function sendCommand(
  instanceId: string,
  command: string,
  params: Record<string, string> = {},
): Promise<void> {
  return req<void>('POST', `/api/admin/center/instances/${encodeURIComponent(instanceId)}/command`, {
    command,
    params,
    issued_by: 'admin',
  })
}

function decorateInstance(i: CenterInstance): CenterInstance {
  return {
    ...i,
    id: i.instance_id,
    created_at: i.started_at,
    // cpu_usage/memory_usage/disk_usage/uptime_seconds are only available
    // via heartbeat history, not on InstanceInfo; leave undefined.
  }
}

// ────────────────────────────────────────────────────────────────────────────
// VibeCoding Management
// ────────────────────────────────────────────────────────────────────────────

export interface VibeCodingProject {
  id: number
  name: string
  language: string
  framework: string
  status: 'active' | 'archived' | 'deleted'
  description?: string
  created_at: string
  updated_at: string
}

export interface VibeCodingSession {
  id: number
  project_id?: number
  session_name: string // alias for session_id
  session_id: string
  task_type: string
  status: 'active' | 'completed' | 'failed' | 'cancelled'
  started_at: string // alias for created_at
  ended_at?: string // alias for completed_at
  created_at: string
  completed_at?: string
  duration_seconds: number
}

export interface CodeIssue {
  line: number
  severity: 'error' | 'warning' | 'info'
  message: string
  code: string // alias for category
  category: string
}

export interface CodeSuggestion {
  line: number
  message: string
  suggested_code: string
}

export interface CodeReview {
  id: number
  session_id?: number
  file_path: string
  language: string
  score: number
  issues: CodeIssue[]
  suggestions: CodeSuggestion[]
  reviewed_at: string // alias for created_at
  created_at: string
}

export async function getVibeCodingProjects(): Promise<VibeCodingProject[]> {
  const data = await req<any>('GET', '/api/admin/vibecoding/projects')
  return extractList<VibeCodingProject>(data)
}

export async function createVibeCodingProject(data: {
  name: string
  language: string
  framework: string
  description?: string
  tenant_id?: string
  created_by?: string
}): Promise<VibeCodingProject> {
  return req<VibeCodingProject>('POST', '/api/admin/vibecoding/projects', {
    ...data,
    tenant_id: data.tenant_id || 'default',
    created_by: data.created_by || 'admin',
  })
}

export async function getVibeCodingSessions(projectId?: number): Promise<VibeCodingSession[]> {
  const qs = projectId ? `?project_id=${projectId}` : ''
  const data = await req<any>('GET', `/api/admin/vibecoding/sessions${qs}`)
  return extractList<VibeCodingSession>(data).map(decorateSession)
}

export async function createVibeCodingSession(
  projectId: number,
  sessionName: string,
  tenantId = 'default',
  taskType = 'code',
): Promise<VibeCodingSession> {
  return req<VibeCodingSession>('POST', '/api/admin/vibecoding/sessions', {
    tenant_id: tenantId,
    task_type: taskType,
    project_id: projectId,
    session_name: sessionName,
  })
}

export async function getCodeReviews(sessionId?: number): Promise<CodeReview[]> {
  const qs = sessionId ? `?session_id=${sessionId}` : ''
  const data = await req<any>('GET', `/api/admin/vibecoding/reviews${qs}`)
  return extractList<CodeReview>(data).map(decorateReview)
}

function decorateSession(s: VibeCodingSession): VibeCodingSession {
  const started = new Date(s.created_at).getTime()
  const ended = s.completed_at ? new Date(s.completed_at).getTime() : Date.now()
  return {
    ...s,
    session_name: s.session_id,
    started_at: s.created_at,
    ended_at: s.completed_at,
    duration_seconds: Math.max(0, Math.floor((ended - started) / 1000)),
  }
}

function decorateReview(r: CodeReview): CodeReview {
  // Backend packs issues/suggestions inside review_result map; flatten if present
  const result = (r as any).review_result || {}
  const issues: CodeIssue[] = (result.issues || []).map((it: any) => ({
    line: it.line ?? 0,
    severity: it.severity || 'info',
    message: it.message || '',
    code: it.category || '',
    category: it.category || '',
  }))
  const suggestions: CodeSuggestion[] = (result.suggestions || []).map((s: any, idx: number) =>
    typeof s === 'string'
      ? { line: 0, message: s, suggested_code: '' }
      : {
          line: s.line ?? idx,
          message: s.message || '',
          suggested_code: s.suggested_code || s.code || '',
        },
  )
  return {
    ...r,
    issues,
    suggestions,
    reviewed_at: r.created_at,
  }
}