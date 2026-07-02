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
