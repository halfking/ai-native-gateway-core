import { req } from './_core'

// tuning.ts — v6.0 audit T12 (2026-06-22)
// Auto-route analytics + admin tuning surfaces. The auto-router
// evaluates different routing strategies (Phase 5) and A/B-tests them
// against a baseline; the endpoints here surface the verdict, per-
// strategy correlation breakdowns, and admin overrides (pin/ban
// a model for a given task_type × profile combo).
//
// Data Lifecycle endpoints (hot/warm/cold/expired data + cleanup
// preview) live at the bottom of this file because they share the
// "admin observability" theme.

export interface StrategySummaryRow {
  strategy: string
  total: number
  avg_quality: number
  avg_success: number
  avg_latency: number
  avg_cost: number
  drift_rate: number
}

export interface StrategyBreakdownRow {
  strategy: string
  task_type: string
  total: number
  avg_quality: number
  avg_success: number
}

export interface StrategyResponse {
  window_days: number
  summary: StrategySummaryRow[]
  breakdown: StrategyBreakdownRow[]
  ab_verdict: 'pattern_layered_wins' | 'baseline_heuristic_wins' | 'no_significant_difference' | 'insufficient_samples' | 'ab_test_disabled'
  ab_enabled: boolean
  ab_baseline_pct: number
  generated_at: string
}

export function getTuningStrategies(days = 7) {
  return req<StrategyResponse>('GET',
    `/api/admin/auto-route/tuning/strategies?days=${days}`)
}

export interface CorrelationRow {
  label: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
  avg_quality?: number
}

export interface CorrelationRowMT {
  model: string
  task_type: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
}

export interface CorrelationVerdict {
  task_type: string
  model: string
  success_rate: number
  avg_latency_ms: number
  rank: number
}

export interface AutoRouteCorrelationsResponse {
  window_days: number
  by_model: CorrelationRow[]
  by_strategy: CorrelationRow[]
  by_task_type: CorrelationRow[]
  by_model_task: CorrelationRowMT[]
  verdict: CorrelationVerdict[]
  generated_at: string
}

export function getAutoRouteCorrelations(params: {
  days?: number
  min_samples?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.min_samples) q.set('min_samples', String(params.min_samples))
  const path = '/api/admin/auto-route/correlations' + (q.toString() ? '?' + q : '')
  return req<AutoRouteCorrelationsResponse>('GET', path)
}

export interface RoutingOverride {
  id: number
  task_type: string
  profile: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  created_by?: string
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface RoutingOverridesResponse {
  overrides: RoutingOverride[]
  count: number
  filter: { task_type: string; profile: string; active: string }
}

export interface RoutingOverrideCreate {
  task_type: string
  profile?: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  expires_at?: string
}

export function getRoutingOverrides(params: {
  active?: boolean
  task_type?: string
  profile?: string
} = {}) {
  const q = new URLSearchParams()
  if (params.active) q.set('active', 'true')
  if (params.task_type) q.set('task_type', params.task_type)
  if (params.profile) q.set('profile', params.profile)
  const path = '/api/admin/routing/overrides' + (q.toString() ? '?' + q : '')
  return req<RoutingOverridesResponse>('GET', path)
}

export function createRoutingOverride(body: RoutingOverrideCreate) {
  return req<{ id: number; status: string; message: string }>('POST',
    '/api/admin/routing/overrides', body)
}

export function deleteRoutingOverride(id: number) {
  return req<{ id: number; status: string; note: string }>('DELETE',
    `/api/admin/routing/overrides/${id}`)
}

export function extendRoutingOverride(id: number, expires_at: string | null) {
  return req<{ id: number; status: string }>('PATCH',
    `/api/admin/routing/overrides/${id}/extend`, { expires_at })
}

export interface QualityCorrelationRow {
  bucket: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_quality: number
  avg_cost_usd: number
}

export interface QualityCorrelationInsight {
  predictor: 'prompt_length' | 'tools' | 'images' | 'code_block'
  buckets: number
  samples: number
  correlation: number
  abs_r: number
  interpretation: string
}

export interface QualityCorrelationResponse {
  window_days: number
  by: string
  breakdown: QualityCorrelationRow[]
  insights: QualityCorrelationInsight[]
  generated_at: string
}

export function getQualityCorrelations(params: {
  days?: number
  by?: 'prompt_length' | 'tools' | 'images' | 'code_block'
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.by) q.set('by', params.by)
  const path = '/api/admin/auto-route/quality-correlations' + (q.toString() ? '?' + q : '')
  return req<QualityCorrelationResponse>('GET', path)
}

export interface RoutingAuditEntry {
  id: number
  ts: string
  action: 'insert' | 'update' | 'delete'
  override_id?: number
  task_type?: string
  profile?: string
  mode?: string
  model_chosen?: string
  reason?: string
  expires_at?: string
  old_expires_at?: string
  actor?: string
}

export interface RoutingAuditResponse {
  entries: RoutingAuditEntry[]
  count: number
  filter: { action: string; actor: string; override_id: string; days: string }
}

export function getRoutingAudit(params: {
  action?: 'insert' | 'update' | 'delete' | ''
  actor?: string
  override_id?: number
  days?: number
  limit?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.action) q.set('action', params.action)
  if (params.actor) q.set('actor', params.actor)
  if (params.override_id) q.set('override_id', String(params.override_id))
  if (params.days) q.set('days', String(params.days))
  if (params.limit) q.set('limit', String(params.limit))
  const path = '/api/admin/routing/overrides/audit' + (q.toString() ? '?' + q : '')
  return req<RoutingAuditResponse>('GET', path)
}

// ── Data Lifecycle Management API ─────────────────────────────────────────

export interface DataSegment {
  rows: number
  size_bytes: number
  size_human: string
  days: number
  percent_of_total: number
}

export interface TenantDataStats {
  tenant_id: string
  rows: number
  size_bytes: number
  size_human: string
}

export interface DailyGrowth {
  date: string
  requests: number
  compressed: number
  compression_rate: number
}

export interface DataLifecycleStatsResponse {
  total_rows: number
  total_size_bytes: number
  total_size_human: string
  hot_data: DataSegment | null
  warm_data: DataSegment | null
  cold_data: DataSegment | null
  expired_data: DataSegment | null
  by_tenant: TenantDataStats[]
  growth_trend: DailyGrowth[]
}

export function dataLifecycleStats() {
  return req<DataLifecycleStatsResponse>('GET', '/api/admin/data-lifecycle/stats')
}

export interface CleanupPreviewResponse {
  affected_rows: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  warning_message?: string
}

export function dataLifecycleCleanupPreview(
  action: string,
  from: string,
  to: string
) {
  return req<CleanupPreviewResponse>('POST', '/api/admin/data-lifecycle/cleanup/preview', {
    action,
    from,
    to
  })
}

export interface DataLifecycleMetricsResponse {
  total_rows: number
  total_size_bytes: number
  hot_data_rows: number
  hot_data_size_bytes: number
  warm_data_rows: number
  warm_data_size_bytes: number
  cold_data_rows: number
  cold_data_size_bytes: number
  expired_data_rows: number
  expired_data_size_bytes: number
  last_cleanup_at?: string
  last_archive_at?: string
}

export function dataLifecycleMetrics() {
  return req<DataLifecycleMetricsResponse>('GET', '/api/admin/data-lifecycle/metrics')
}

// ── Storage overview (2026-07-01) ──────────────────────────────────
//
// DB vs 本机磁盘对比 + 表级 Top-N 占用 + 本机日志目录大小。
// 用于 /admin/data-lifecycle 第一屏。

export interface DatabaseStorageInfo {
  database_bytes: number
  database_human: string
  total_bytes: number
  total_human: string
  tables_bytes: number
  indexes_bytes: number
  toast_bytes: number
  server_version?: string
}

export interface FilesystemInfo {
  path: string
  total_bytes: number
  total_human: string
  used_bytes: number
  used_human: string
  free_bytes: number
  free_human: string
  used_percent: number
}

export interface LocalDirInfo {
  path: string
  exists: boolean
  files: number
  size_bytes: number
  size_human: string
  oldest_mtime: number
  newest_mtime: number
}

export interface StorageOverview {
  database: DatabaseStorageInfo
  filesystem: FilesystemInfo
  local_logs?: LocalDirInfo
  warnings: string[]
  collected_at: string
}

export function dataLifecycleStorage() {
  return req<StorageOverview>('GET', '/api/admin/data-lifecycle/storage')
}

export interface TableSizeInfo {
  table: string
  schema: string
  rows: number
  total_bytes: number
  total_human: string
  index_bytes: number
  toast_bytes: number
  percent_of_db: number
  is_partitioned: boolean
}

export interface TableSizesResponse {
  tables: TableSizeInfo[]
  total_bytes: number
  total_human: string
  collected_at: string
}

export function dataLifecycleTableSizes(limit = 20) {
  return req<TableSizesResponse>('GET', `/api/admin/data-lifecycle/storage/tables?limit=${limit}`)
}

// ── Blob 管理 (2026-07-01) ─────────────────────────────────────────
//
// request_logs.request_body / outbound_body 当作"附件"管：按大小/年龄
// 列出 Top-N 大字段，预览/执行清理（置 NULL，保留元数据）。

export interface BlobRow {
  request_id: string
  session_key: string
  tenant_id: string
  occurred_at: string
  request_body_bytes: number
  outbound_body_bytes: number
  total_bytes: number
  total_human: string
  model?: string
}

export interface BlobTopResponse {
  rows: BlobRow[]
  total_bytes: number
  total_human: string
  collected_at: string
}

export function dataLifecycleBlobTop(limit = 20) {
  return req<BlobTopResponse>('GET', `/api/admin/data-lifecycle/blobs/top?limit=${limit}`)
}

export interface BlobCleanupRequest {
  older_than_days?: number
  larger_than_kb?: number
  scope?: 'all' | 'current'
}

export interface BlobCleanupResponse {
  affected_rows: number
  request_body_affected: number
  outbound_affected: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  executed: boolean
  warning_message?: string
  started_at: string
  finished_at?: string
}

export function dataLifecycleBlobCleanupPreview(body: BlobCleanupRequest) {
  return req<BlobCleanupResponse>('POST', '/api/admin/data-lifecycle/blobs/cleanup/preview', body)
}

export function dataLifecycleBlobCleanupExecute(body: BlobCleanupRequest) {
  return req<BlobCleanupResponse>('POST', '/api/admin/data-lifecycle/blobs/cleanup/execute', body)
}

// ── 附件管理 (2026-07-01) ──────────────────────────────────────────
//
// request_logs.attachments (JSONB 列) 的列表 / 统计 / 策略 / 清理预览 / 清理执行。
// 本端点只清理 JSONB 元数据（置 NULL），不删除文件系统实体文件。

export interface AttachmentListItem {
  request_id: string
  ts: string
  tenant_id: string
  client_model: string
  success: boolean
  attachments: any[] // JSONB 数组
}

export interface AttachmentListResponse {
  items: AttachmentListItem[]
  limit: number
  offset: number
  count: number
}

export function attachmentList(params: { since?: string; until?: string; limit?: number; offset?: number } = {}) {
  const q = new URLSearchParams()
  if (params.since) q.set('since', params.since)
  if (params.until) q.set('until', params.until)
  if (params.limit) q.set('limit', String(params.limit))
  if (params.offset) q.set('offset', String(params.offset))
  const qs = q.toString()
  return req<AttachmentListResponse>('GET', `/api/admin/attachments${qs ? '?' + qs : ''}`)
}

export interface AttachmentStatBucket {
  type: string
  content_type: string
  count: number
  total_bytes: number
}

export interface AttachmentStatsResponse {
  breakdown: AttachmentStatBucket[]
  total_count: number
  total_bytes: number
}

export function attachmentStats() {
  return req<AttachmentStatsResponse>('GET', '/api/admin/attachments/stats')
}

export interface AttachmentPolicyInfo {
  retention_days: number
  max_size_bytes: number
  auto_cleanup: boolean
  delete_filesystem: boolean
  description: string
}

export interface AttachmentPolicyResponse {
  policy: AttachmentPolicyInfo
  note: string
}

export function attachmentPolicyGet() {
  return req<AttachmentPolicyResponse>('GET', '/api/admin/attachments/policy')
}

export interface AttachmentCleanupRequest {
  older_than_days: number
}

export interface AttachmentCleanupPreviewResponse {
  older_than_days: number
  affected_records: number
  total_bytes: number
  dry_run: true
  action: string
}

export interface AttachmentCleanupExecuteResponse {
  older_than_days: number
  rows_affected: number
  action: string
  filesystem_files: string
}

export function attachmentCleanupPreview(body: AttachmentCleanupRequest) {
  return req<AttachmentCleanupPreviewResponse>('POST', '/api/admin/attachments/cleanup/preview', body)
}

export function attachmentCleanupExecute(body: AttachmentCleanupRequest) {
  return req<AttachmentCleanupExecuteResponse>('POST', '/api/admin/attachments/cleanup/execute', body)
}

export function attachmentItem(requestID: string) {
  return req<{
    request_id: string
    ts: string
    tenant_id: string
    client_model: string
    success: boolean
    attachments: any[]
  }>('GET', `/api/admin/attachments/${requestID}`)
}

// ── Attachment Filesystem Management (2026-07-01) ──────────────────────
// 管理附件文件系统：目录大小、磁盘空间、按时间清理文件

export interface AttachmentFilesystemStats {
  attachment_dir: string
  total_files: number
  total_size_bytes: number
  total_size_human: string
  oldest_file_time: string | null
  disk_total_bytes: number
  disk_used_bytes: number
  disk_avail_bytes: number
  disk_usage_percent: number
  disk_warning_level: 'safe' | 'warning' | 'danger'
}

export interface AttachmentFilesystemCleanupRequest {
  older_than_days: number
  dry_run: boolean
  reason: string
}

export interface AttachmentFilesystemCleanupResponse {
  dry_run: boolean
  files_deleted: number
  bytes_freed: number
  bytes_freed_human: string
  deleted_paths?: string[]
  error?: string
}

export function attachmentFilesystemStats() {
  return req<AttachmentFilesystemStats>('GET', '/api/admin/attachments/filesystem/stats')
}

export function attachmentFilesystemCleanup(body: AttachmentFilesystemCleanupRequest) {
  return req<AttachmentFilesystemCleanupResponse>('POST', '/api/admin/attachments/filesystem/cleanup', body)
}

// ── Tuning proposals + accuracy (Phase 5) ──────────────────────────────
//
// Three endpoints are mounted by admin/auto_route_tuning.go:
//
//   GET  /api/admin/auto-route/tuning/proposals?status=&category=&limit=
//   POST /api/admin/auto-route/tuning/proposals/:id/approve
//   POST /api/admin/auto-route/tuning/proposals/:id/reject  (body: {reason}?)
//   GET  /api/admin/auto-route/tuning/accuracy?days=
//
// `triggerTuningAnalyze` is currently a frontend-only call: there is no
// matching backend endpoint yet (auto_route_tuning.go mounts 4 routes,
// none of which trigger an ad-hoc analyzer run). The function below
// posts to /tuning/analyze; the existing try/catch in TuningView.vue
// surfaces the 404 as a user-facing alert. When the backend adds the
// trigger endpoint the call will start succeeding.

export type TuningProposalCategory = 'keyword_add' | 'weight_adjust' | 'threshold_change'
export type TuningProposalStatus = 'pending' | 'approved' | 'rejected' | 'applied'

export interface TuningProposal {
  id: number
  ts: string
  category: TuningProposalCategory
  task_type: string | null
  proposal: Record<string, unknown>
  evidence: ProposalEvidence
  status: TuningProposalStatus
  reviewed_by: string | null
  reviewed_at: string | null
  applied_at: string | null
  review_note: string | null
}

// The analyzer writes a different evidence shape per category
// (see bg/feedback_analyzer.go lines 244-249 and 319-324). The
// frontend only renders a few fields in evidenceSummary so we keep
// the optional+typed model: present fields per category, others
// undefined.
export interface ProposalEvidence {
  sample_count?: number
  window_days?: number
  quality_threshold?: number
  actual_success?: number
  predicted_match?: number
  avg_quality?: number
  rationale?: string
  confidence?: number
}

export interface TuningProposalsResponse {
  proposals: TuningProposal[]
  count: number
  filter: { status: string; category: string }
}

export function getTuningProposals(params: {
  status?: TuningProposalStatus | ''
  category?: TuningProposalCategory | ''
  limit?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.status) q.set('status', params.status)
  if (params.category) q.set('category', params.category)
  if (params.limit != null) q.set('limit', String(params.limit))
  const s = q.toString()
  return req<TuningProposalsResponse>('GET', `/api/admin/auto-route/tuning/proposals${s ? '?' + s : ''}`)
}

export function approveTuningProposal(id: number) {
  return req<{ id: number; status: string; message: string }>(
    'POST', `/api/admin/auto-route/tuning/proposals/${id}/approve`
  )
}

export function rejectTuningProposal(id: number, reason?: string) {
  return req<{ id: number; status: string; message: string }>(
    'POST', `/api/admin/auto-route/tuning/proposals/${id}/reject`,
    { reason: reason ?? null }
  )
}

export interface AccuracyBreakdownRow {
  task_type: string
  classifier: string
  total: number
  avg_quality: number
  avg_success: number
  avg_latency: number
  avg_cost: number
  drift_rate: number
}

export interface TuningAccuracyResponse {
  window_days: number
  breakdown: AccuracyBreakdownRow[]
  generated_at: string
}

export function getTuningAccuracy(days = 7) {
  return req<TuningAccuracyResponse>('GET', `/api/admin/auto-route/tuning/accuracy?days=${days}`)
}

export interface TriggerTuningAnalyzeResponse {
  completed_at: string
  triggered_by: string
}

export function triggerTuningAnalyze() {
  // TODO(backend): no matching endpoint in admin/auto_route_tuning.go
  // yet. Post path is a placeholder — when the trigger endpoint lands,
  // update this path to match. Until then the call will 404 and the
  // TuningView.vue catch handler will show the error to the user.
  return req<TriggerTuningAnalyzeResponse>('POST', '/api/admin/auto-route/tuning/analyze')
}