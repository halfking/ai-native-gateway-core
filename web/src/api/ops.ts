// ops.ts — Operations Platform API (2026-07-10)
// Unified API module for License, Fault, AutoUpdate, Center, and VibeCoding management

import { req } from './_core'

// ────────────────────────────────────────────────────────────────────────────
// License Management
// ────────────────────────────────────────────────────────────────────────────

export interface License {
  id: number
  license_key: string
  customer_name: string
  customer_email: string
  max_devices: number
  expires_at: string
  created_at: string
  revoked_at?: string
}

export interface LicenseDevice {
  id: number
  license_id: number
  instance_id: string
  hardware_hash: string
  device_name: string
  activated_at: string
  last_heartbeat?: string
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

export async function getLicenses(): Promise<License[]> {
  const response = await req<{ licenses: License[] }>('GET', '/api/admin/licenses')
  return response.licenses
}

export async function createLicense(data: {
  license_key: string
  customer_name: string
  customer_email: string
  max_devices: number
  expires_at: string
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

export async function approveOfflineActivation(id: number): Promise<{ activation_code: string }> {
  return req<{ activation_code: string }>('POST', `/api/admin/licenses/offline-requests/${id}/approve`)
}

export async function rejectOfflineActivation(id: number, reason: string): Promise<void> {
  return req<void>('POST', `/api/admin/licenses/offline-requests/${id}/reject`, { reason })
}

// ────────────────────────────────────────────────────────────────────────────
// Fault Management
// ────────────────────────────────────────────────────────────────────────────

export interface FaultEvent {
  id: number
  rule_id: number
  rule_name: string
  severity: 'critical' | 'warning' | 'info'
  status: 'open' | 'resolving' | 'resolved'
  detected_at: string
  resolved_at?: string
  message: string
  context: Record<string, unknown>
}

export interface FaultRule {
  id: number
  name: string
  description: string
  severity: 'critical' | 'warning' | 'info'
  enabled: boolean
  condition: string
  auto_fix: boolean
  created_at: string
}

export interface FaultStats {
  total_events: number
  open_events: number
  resolved_events: number
  avg_resolution_time_minutes: number
}

export async function getFaultEvents(): Promise<FaultEvent[]> {
  const response = await req<{ events: FaultEvent[] }>('GET', '/api/admin/faults/events')
  return response.events
}

export async function getFaultRules(): Promise<FaultRule[]> {
  const response = await req<{ rules: FaultRule[] }>('GET', '/api/admin/faults/rules')
  return response.rules
}

export async function createFaultRule(data: Omit<FaultRule, 'id' | 'created_at'>): Promise<FaultRule> {
  return req<FaultRule>('POST', '/api/admin/faults/rules', data)
}

export async function updateFaultRule(id: number, data: Partial<FaultRule>): Promise<FaultRule> {
  return req<FaultRule>('PUT', `/api/admin/faults/rules/${id}`, data)
}

export async function deleteFaultRule(id: number): Promise<void> {
  return req<void>('DELETE', `/api/admin/faults/rules/${id}`)
}

export async function getFaultStats(): Promise<FaultStats> {
  return req<FaultStats>('GET', '/api/admin/faults/stats')
}

export async function triggerManualFix(eventId: number): Promise<void> {
  return req<void>('POST', `/api/admin/faults/events/${eventId}/resolve`, { actor: 'super_admin' })
}

// ────────────────────────────────────────────────────────────────────────────
// Auto Update Management
// ────────────────────────────────────────────────────────────────────────────

export interface Release {
  id: number
  version: string
  channel: 'stable' | 'beta' | 'canary'
  build_seq: number
  title: string
  description: string
  changelog: string
  image_tag: string
  image_digest?: string
  min_version?: string
  mandatory: boolean
  created_by: string
  published_at?: string
  created_at: string
}

export interface UpgradeLog {
  id: number
  instance_id: string
  from_version: string
  to_version: string
  status: 'pending' | 'downloading' | 'installing' | 'success' | 'failed'
  started_at: string
  completed_at?: string
  error_message?: string
}

export async function getReleases(): Promise<Release[]> {
  const response = await req<{ items: Release[] }>('GET', '/api/admin/releases')
  return response.items
}

export async function createRelease(data: Omit<Release, 'id' | 'created_at' | 'published_at'>): Promise<Release> {
  return req<Release>('POST', '/api/admin/releases', data)
}

export async function publishRelease(version: string): Promise<void> {
  return req<void>('POST', `/api/admin/releases/${encodeURIComponent(version)}/publish`)
}

export async function rollbackRelease(targetVersion: string): Promise<void> {
  return req<void>('POST', '/api/admin/rollback', { target_version: targetVersion })
}

export async function getUpgradeLogs(instanceId?: string): Promise<UpgradeLog[]> {
  const suffix = instanceId ? `?instance_id=${encodeURIComponent(instanceId)}` : ''
  return req<UpgradeLog[]>('GET', `/api/admin/upgrade-logs${suffix}`)
}

// ────────────────────────────────────────────────────────────────────────────
// Center Management (Instance Operations)
// ────────────────────────────────────────────────────────────────────────────

export interface CenterInstance {
  instance_id: string
  hostname: string
  ip_address: string
  region?: string
  version: string
  build_seq: number
  status: 'online' | 'offline' | 'degraded'
  last_heartbeat: string
  started_at: string
}

export interface HeartbeatHistory {
  timestamp: string
  uptime_secs: number
  num_goroutine: number
  alloc_mb: number
}

export interface CenterStats {
  total_instances: number
  online_instances: number
  offline_instances: number
  degraded_instances: number
}

export async function getCenterInstances(): Promise<CenterInstance[]> {
  const response = await req<{ items: CenterInstance[] }>('GET', '/api/admin/center/instances')
  return response.items
}

export async function getCenterStats(): Promise<CenterStats> {
  return req<CenterStats>('GET', '/api/admin/center/dashboard/stats')
}

export async function getHeartbeatHistory(instanceId: string): Promise<HeartbeatHistory[]> {
  return req<HeartbeatHistory[]>('GET', `/api/admin/center/instances/${encodeURIComponent(instanceId)}/heartbeats`)
}

export async function sendCommand(instanceId: string, command: string, args: Record<string, string>): Promise<void> {
  return req<void>('POST', `/api/admin/center/instances/${encodeURIComponent(instanceId)}/command`, { command, args })
}

// ────────────────────────────────────────────────────────────────────────────
// VibeCoding Management
// ────────────────────────────────────────────────────────────────────────────

export interface VibeCodingProject {
  id: number
  tenant_id: string
  name: string
  description: string
  language: string
  framework: string
  status: 'active' | 'archived' | 'deleted'
  created_at: string
  updated_at: string
}

export interface VibeCodingSession {
  id: number
  project_id?: number
  session_id: string
  task_type: string
  created_at: string
  completed_at?: string
  status: 'active' | 'completed' | 'failed' | 'cancelled'
}

export interface CodeReview {
  id: number
  session_id?: number
  tenant_id: string
  language: string
  file_path: string
  score: number
  review_result: { issues?: CodeIssue[]; suggestions?: string[] }
  created_at: string
}

export interface CodeIssue {
  line: number
  severity: 'error' | 'warning' | 'info'
  message: string
  code: string
}

export interface CodeSuggestion {
  line: number
  message: string
  suggested_code: string
}

export async function getVibeCodingProjects(): Promise<VibeCodingProject[]> {
  const response = await req<{ items: VibeCodingProject[] }>('GET', '/api/admin/vibecoding/projects')
  return response.items
}

export async function createVibeCodingProject(data: {
  name: string
  language: string
  framework: string
}): Promise<VibeCodingProject> {
  return req<VibeCodingProject>('POST', '/api/admin/vibecoding/projects', data)
}

export async function getVibeCodingSessions(projectId?: number): Promise<VibeCodingSession[]> {
  const suffix = projectId ? `?project_id=${projectId}` : ''
  const response = await req<{ items: VibeCodingSession[] }>('GET', `/api/admin/vibecoding/sessions${suffix}`)
  return response.items
}

export async function createVibeCodingSession(projectId: number, taskType: string): Promise<VibeCodingSession> {
  return req<VibeCodingSession>('POST', '/api/admin/vibecoding/sessions', { project_id: projectId, task_type: taskType })
}

export async function getCodeReviews(sessionId?: number): Promise<CodeReview[]> {
  const suffix = sessionId ? `?session_id=${sessionId}` : ''
  const response = await req<{ items: CodeReview[] }>('GET', `/api/admin/vibecoding/reviews${suffix}`)
  return response.items
}
