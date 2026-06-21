import { req } from './_core'

// tenant-model-policy.ts — v6.0 audit T12 (2026-06-22)
// Round 48 (2026-06-21) feature: per-tenant allow/deny of canonical
// models. A policy row is "tenant X is allowed to use canonical model Y,
// created by Z, reason W". Soft-delete (deleted_at/deleted_by) is used
// so undelete is a 1-call admin action; the audit log is preserved
// forever for compliance.

export interface TenantModelPolicy {
  id: number
  tenant_id: string
  canonical_name: string
  reason: string
  created_by: string
  deleted_at: string | null
  deleted_by: string | null
  created_at: string
  updated_at: string
}

export interface TenantModelPolicyListResp {
  policies: TenantModelPolicy[]
  count: number
  tenant: string
}

export interface TenantModelPolicyCheckResp {
  exists: boolean
  canonical_name: string
  family?: string
  vendor?: string
  modality?: string
}

export interface TenantModelPolicyAuditEntry {
  id: number
  ts: string
  action: 'insert' | 'update' | 'delete' | 'undelete'
  policy_id: number | null
  tenant_id: string
  canonical_name: string
  reason: string
  actor: string
}

export function listTenantModelPolicies(tenantCode: string, opts: { includeDeleted?: boolean } = {}) {
  const qs = opts.includeDeleted ? '?include_deleted=true' : ''
  return req<TenantModelPolicyListResp>(
    'GET', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies${qs}`)
}

export function createTenantModelPolicy(
  tenantCode: string,
  body: { canonical_name: string; reason: string },
) {
  return req<{ id: number; status: string; policy: TenantModelPolicy; message: string }>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies`, body)
}

export function patchTenantModelPolicy(
  tenantCode: string, id: number, body: { reason: string },
) {
  return req<TenantModelPolicy>(
    'PATCH', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}`, body)
}

export function deleteTenantModelPolicy(tenantCode: string, id: number) {
  return req<{ id: number; status: string; message: string }>(
    'DELETE', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}`)
}

export function undeleteTenantModelPolicy(tenantCode: string, id: number) {
  return req<{ id: number; status: string; message: string }>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}/undelete`)
}

export function checkTenantModelPolicy(
  tenantCode: string, body: { canonical_name: string },
) {
  return req<TenantModelPolicyCheckResp>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/check`, body)
}

export function listTenantModelPoliciesAudit(tenantCode: string, limit = 100) {
  return req<{ audit: TenantModelPolicyAuditEntry[]; count: number }>(
    'GET', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/audit?limit=${limit}`)
}