import { req } from './_core'

// approval.ts — Approval workflow configuration API
// Manages approval settings, approvers, notification channels, and rules

export interface Approver {
  id?: number
  name: string
  email: string
  role: string
  priority: number
  enabled: boolean
}

export interface NotificationChannel {
  type: 'feishu' | 'wecom' | 'dingtalk'
  enabled: boolean
  config: {
    app_id?: string
    app_secret?: string
    webhook_url?: string
    corp_id?: string
    corp_secret?: string
    agent_id?: string
    app_key?: string
  }
}

export interface ApprovalRule {
  id?: number
  name: string
  enabled: boolean
  priority: number
  conditions: {
    field: string
    operator: string
    value: any
  }[]
  action: 'require_approval' | 'auto_approve' | 'auto_reject'
  risk_level: 'low' | 'medium' | 'high' | 'critical'
  description?: string
}

export interface ApprovalConfig {
  enabled: boolean
  mode: 'disabled' | 'automatic' | 'manual'
  timeout_seconds: number
  timeout_action: 'approve' | 'reject'
  approvers: Approver[]
  notification_channels: {
    feishu?: NotificationChannel
    wecom?: NotificationChannel
    dingtalk?: NotificationChannel
  }
  rules: ApprovalRule[]
}

export function getApprovalConfig() {
  return req<ApprovalConfig>('GET', '/api/admin/approval-config')
}

export function updateApprovalConfig(config: ApprovalConfig) {
  return req<ApprovalConfig>('PUT', '/api/admin/approval-config', config)
}

export function testNotificationChannel(channel: NotificationChannel) {
  return req<{ status: string; message?: string }>('POST', '/api/admin/approval-config/test-notification', channel)
}

// Approval request management

export interface ApprovalItem {
  id: string
  session_id: string
  tenant_id: string
  request_id: string
  status: 'pending' | 'approved' | 'rejected' | 'timeout'
  detect_result?: {
    decision: 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL'
    reason: string
    sensitive_info?: string[]
    cost_estimation?: number
  }
  risk_level: string
  trigger_type: string
  approved_by?: string
  approved_at?: string
  reason?: string
  created_at: string
  expires_at: string
  time_left?: string
}

export interface ApprovalDetail extends ApprovalItem {
  snapshot?: {
    session_summary?: string
    messages?: Array<{
      role: string
      content: string
      redacted?: boolean
    }>
    context?: any
    metadata?: Record<string, any>
  }
}

export interface ApprovalListParams {
  status?: string
  tenant_id?: string
  risk_level?: string
  page?: number
  page_size?: number
  sort_by?: string
  sort_order?: string
  created_after?: string
  created_before?: string
  session_id?: string
  request_id?: string
}

export interface ApprovalListResponse {
  items: ApprovalItem[]
  total: number
  page: number
  page_size: number
  total_pages: number
}

export interface ApprovalStats {
  total: number
  pending: number
  approved: number
  rejected: number
  timeout: number
  avg_approval_time_seconds: number
  by_risk_level: Record<string, number>
  by_trigger_type: Record<string, number>
  today_total: number
  today_pending: number
}

export function getApprovalList(params?: ApprovalListParams) {
  const query = new URLSearchParams()
  if (params?.status) query.set('status', params.status)
  if (params?.tenant_id) query.set('tenant_id', params.tenant_id)
  if (params?.risk_level) query.set('risk_level', params.risk_level)
  if (params?.page) query.set('page', params.page.toString())
  if (params?.page_size) query.set('page_size', params.page_size.toString())
  if (params?.sort_by) query.set('sort_by', params.sort_by)
  if (params?.sort_order) query.set('sort_order', params.sort_order)
  if (params?.session_id) query.set('session_id', params.session_id)
  if (params?.request_id) query.set('request_id', params.request_id)
  
  const queryStr = query.toString()
  return req<ApprovalListResponse>('GET', `/api/admin/approvals${queryStr ? '?' + queryStr : ''}`)
}

export function getApprovalDetail(requestId: string) {
  return req<ApprovalDetail>('GET', `/api/v1/approvals/${requestId}`)
}

export function approveApproval(requestId: string, reason?: string) {
  return req<{ success: boolean; message: string; status: string }>(
    'POST',
    `/api/v1/approvals/${requestId}/approve`,
    { reason: reason || '' }
  )
}

export function rejectApproval(requestId: string, reason: string) {
  return req<{ success: boolean; message: string; status: string }>(
    'POST',
    `/api/v1/approvals/${requestId}/reject`,
    { reason }
  )
}

export function getApprovalStats(params?: { tenant_id?: string; start_time?: string; end_time?: string }) {
  const query = new URLSearchParams()
  if (params?.tenant_id) query.set('tenant_id', params.tenant_id)
  if (params?.start_time) query.set('start_time', params.start_time)
  if (params?.end_time) query.set('end_time', params.end_time)
  
  const queryStr = query.toString()
  return req<ApprovalStats>('GET', `/api/admin/approvals/stats${queryStr ? '?' + queryStr : ''}`)
}
