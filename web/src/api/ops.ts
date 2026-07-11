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
  status: string
}

export interface OfflineActivationRequest {
  license_key: string
  hardware_hash: string
  instance_id: string
  device_name: string
  request_id: string
  timestamp: string
  approved_at?: string
  activation_code?: string
  status: string
}

export async function getLicenses(): Promise<License[]> {
  const res = await req<{ licenses: License[]; total: number }>('GET', '/api/admin/licenses')
  return res.licenses || []
}

export async function createLicense(data: {
  customer: string
  max_devices: number
  expires_at: string
}): Promise<License> {
  return req<License>('POST', '/api/admin/licenses', data)
}

export async function revokeLicense(id: number): Promise<void> {
  return req<void>('POST', `/api/admin/licenses/${id}/revoke`)
}

export async function getLicenseDevices(licenseId: number): Promise<LicenseDevice[]> {
  return req<LicenseDevice[]>('GET', `/api/admin/licenses/${licenseId}/devices`)
}

export async function getOfflineActivationRequests(): Promise<OfflineActivationRequest[]> {
  return req<OfflineActivationRequest[]>('GET', '/api/admin/licenses/offline-requests')
}

export async function approveOfflineActivation(id: string): Promise<{ activation_code: string }> {
  return req<{ activation_code: string }>('POST', `/api/admin/licenses/offline-requests/${id}/approve`)
}

export async function rejectOfflineActivation(id: string, reason: string): Promise<void> {
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
  return req<FaultEvent[]>('GET', '/api/admin/faults/events')
}

export async function getFaultRules(): Promise<FaultRule[]> {
  return req<FaultRule[]>('GET', '/api/admin/faults/rules')
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
  return req<void>('POST', `/api/admin/faults/events/${eventId}/fix`)
}

// ────────────────────────────────────────────────────────────────────────────
// Auto Update Management
// ────────────────────────────────────────────────────────────────────────────

export interface Release {
  id: number
  version: string
  channel: 'stable' | 'beta' | 'alpha'
  status: 'draft' | 'published' | 'archived'
  published_at?: string
  rollout_percentage: number
  download_url: string
  checksum: string
  release_notes: string
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
  return req<Release[]>('GET', '/api/admin/releases')
}

export async function createRelease(data: Omit<Release, 'id' | 'created_at' | 'published_at'>): Promise<Release> {
  return req<Release>('POST', '/api/admin/releases', data)
}

export async function publishRelease(id: number, rolloutPercentage: number): Promise<Release> {
  return req<Release>('POST', `/api/admin/releases/${id}/publish`, { rollout_percentage: rolloutPercentage })
}

export async function rollbackRelease(id: number): Promise<void> {
  return req<void>('POST', `/api/admin/releases/${id}/rollback`)
}

export async function getUpgradeLogs(releaseId?: number): Promise<UpgradeLog[]> {
  const path = releaseId ? `/api/admin/releases/${releaseId}/logs` : '/api/admin/releases/logs'
  return req<UpgradeLog[]>('GET', path)
}

// ────────────────────────────────────────────────────────────────────────────
// Center Management (Instance Operations)
// ────────────────────────────────────────────────────────────────────────────

export interface CenterInstance {
  id: number
  instance_id: string
  hostname: string
  version: string
  status: 'online' | 'offline' | 'degraded'
  last_heartbeat: string
  cpu_usage: number
  memory_usage: number
  disk_usage: number
  uptime_seconds: number
  created_at: string
}

export interface HeartbeatHistory {
  timestamp: string
  cpu_usage: number
  memory_usage: number
  disk_usage: number
}

export interface CenterStats {
  online_count: number
  offline_count: number
  degraded_count: number
}

export async function getCenterInstances(): Promise<CenterInstance[]> {
  return req<CenterInstance[]>('GET', '/api/admin/center/instances')
}

export async function getCenterStats(): Promise<CenterStats> {
  return req<CenterStats>('GET', '/api/admin/center/stats')
}

export async function getHeartbeatHistory(instanceId: string, hours: number = 24): Promise<HeartbeatHistory[]> {
  return req<HeartbeatHistory[]>('GET', `/api/admin/center/instances/${instanceId}/heartbeat?hours=${hours}`)
}

export async function sendCommand(instanceId: string, command: string, params: Record<string, unknown>): Promise<void> {
  return req<void>('POST', `/api/admin/center/instances/${instanceId}/command`, { command, params })
}

// ────────────────────────────────────────────────────────────────────────────
// VibeCoding Management
// ────────────────────────────────────────────────────────────────────────────

export interface VibeCodingProject {
  id: number
  name: string
  language: string
  framework: string
  status: 'active' | 'archived'
  created_at: string
  updated_at: string
}

export interface VibeCodingSession {
  id: number
  project_id: number
  session_name: string
  started_at: string
  ended_at?: string
  duration_seconds: number
  status: 'active' | 'completed'
}

export interface CodeReview {
  id: number
  session_id: number
  language: string
  file_path: string
  score: number
  issues: CodeIssue[]
  suggestions: CodeSuggestion[]
  reviewed_at: string
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
  return req<VibeCodingProject[]>('GET', '/api/admin/vibecoding/projects')
}

export async function createVibeCodingProject(data: {
  name: string
  language: string
  framework: string
}): Promise<VibeCodingProject> {
  return req<VibeCodingProject>('POST', '/api/admin/vibecoding/projects', data)
}

export async function getVibeCodingSessions(projectId?: number): Promise<VibeCodingSession[]> {
  const path = projectId 
    ? `/api/admin/vibecoding/projects/${projectId}/sessions`
    : '/api/admin/vibecoding/sessions'
  return req<VibeCodingSession[]>('GET', path)
}

export async function createVibeCodingSession(projectId: number, sessionName: string): Promise<VibeCodingSession> {
  return req<VibeCodingSession>('POST', `/api/admin/vibecoding/projects/${projectId}/sessions`, { session_name: sessionName })
}

export async function getCodeReviews(sessionId?: number): Promise<CodeReview[]> {
  const path = sessionId
    ? `/api/admin/vibecoding/sessions/${sessionId}/reviews`
    : '/api/admin/vibecoding/reviews'
  return req<CodeReview[]>('GET', path)
}
