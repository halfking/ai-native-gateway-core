// sanitizeMatches.ts — session placeholder ↔ masked sensitive value matches.
import { req } from './_core'

export interface SanitizeMatchEntry {
  placeholder: string
  type: string
  index: number
  value_masked: string
  in_request?: boolean
}

export interface SanitizeMatchesResponse {
  session_id: string
  map_ref?: string
  source: string
  entries: SanitizeMatchEntry[]
  stats: Record<string, number>
  request_id?: string
  placeholder_count: number
}

export function getSanitizeMatches(
  sessionId: string,
  opts?: { requestId?: string; signal?: AbortSignal },
): Promise<SanitizeMatchesResponse> {
  const qs = new URLSearchParams()
  if (opts?.requestId) qs.set('request_id', opts.requestId)
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<SanitizeMatchesResponse>(
    'GET',
    `/api/admin/sessions/${encodeURIComponent(sessionId)}/sanitize-matches${suffix}`,
    undefined,
    opts?.signal ? { signal: opts.signal } : undefined,
  )
}
