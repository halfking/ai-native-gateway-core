import { req } from './_core'

export interface DispatchJournalCounts {
  retries: number
  node_switches: number
  model_switches: number
  capacity_waits: number
  scheduled_waits: number
}

export interface DispatchJournalEntry {
  seq: number
  at: string
  model?: string
  credential_id?: number
  provider_id?: number
  vendor?: string
  action: string
  error_kind?: string
  http_status?: number
  attempt: number
  counts: DispatchJournalCounts
  from_model?: string
  to_model?: string
  from_credential_id?: number
  to_credential_id?: number
  from_provider_id?: number
  to_provider_id?: number
}

export interface DispatchJournalSnapshot {
  tenant_id: string
  request_id: string
  entries: DispatchJournalEntry[]
  truncated: boolean
  truncated_count: number
  snapshot_version: number
}

export function fetchDispatchJournal(
  tenantId: string,
  requestId: string,
  opts?: { signal?: AbortSignal },
): Promise<DispatchJournalSnapshot> {
  return req<DispatchJournalSnapshot>(
    'GET',
    `/api/admin/dispatch/journal/${encodeURIComponent(tenantId)}/${encodeURIComponent(requestId)}`,
    undefined,
    { signal: opts?.signal },
  )
}
