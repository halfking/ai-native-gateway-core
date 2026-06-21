import { req } from './_core'

// key-applications.ts — v6.0 audit T12 (2026-06-22)
// "W5" key-application workflow: end users submit a request via
// /api/keys/apply (see keys.ts), which lands in the key_applications
// table. Admins then approve / reject here. Approve both creates the
// live ApiKey and writes the FK to the application row.

export interface KeyApplication {
  id: string
  client_ip: string
  contact: string
  purpose: string | null
  status: 'pending' | 'approved' | 'rejected' | 'expired'
  issued_key_id: number | null
  admin_notes: string | null
  reviewed_by: string | null
  reviewed_at: string | null
  created_at: string
  expires_at: string | null
}

export interface ApproveApplicationResponse {
  application_id: string
  status: string
  key_id: number
  key_prefix: string
  message: string
}

export function listKeyApplications(status?: string) {
  const qs = status ? `?status=${encodeURIComponent(status)}` : ''
  return req<KeyApplication[]>('GET', `/api/key-applications${qs}`)
}

export function approveKeyApplication(id: string, adminNotes?: string) {
  return req<ApproveApplicationResponse>('POST', `/api/key-applications/${id}/approve`, {
    admin_notes: adminNotes ?? null,
    reviewed_by: 'admin',
  })
}

export function rejectKeyApplication(id: string, adminNotes?: string) {
  return req<{ application_id: string; status: string; message: string }>(
    'POST',
    `/api/key-applications/${id}/reject`,
    { admin_notes: adminNotes ?? null, reviewed_by: 'admin' },
  )
}