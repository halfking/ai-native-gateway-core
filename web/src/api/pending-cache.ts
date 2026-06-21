// pending-cache.ts — v6.0 audit T12 (2026-06-22)
// Track C client-side resume: when the client disconnects mid-stream
// (IDE crash, browser close, mobile background-then-foreground), the
// gateway's pending-store capturer keeps reading upstream and caches
// the full SSE response keyed by (sessionID, requestID). On reconnect
// the client calls getPendingResponse to recover the cached body
// without re-sending the LLM request.
//
// See sessions/handler.go:381 (server endpoint) and relay/stream.go:74
// (Track C C2 capturer) for the server side. The cached entry has TTL
// 7 days (pending/pending.go DefaultTTL) and a 1 MiB body cap.

export type PendingResponseStatus = 'completed' | 'in_progress' | 'failed' | 'not_found'

export interface PendingResponse {
  status: PendingResponseStatus
  /** SSE text (when contentType is text/event-stream) or JSON body */
  body?: string
  contentType?: string
  errorMessage?: string
}

/**
 * GET /v1/sessions/{sessionID}/pending-response
 *
 * Returns the cached vendor response for the most recent request under
 * this session (or a specific request_id if supplied). The 200 OK path
 * carries the full SSE body in `body`; 202 means still in-flight;
 * 404 / 503 mean nothing to resume.
 *
 * Failures (network, 5xx, malformed JSON) collapse to `not_found` so
 * callers can simply `if (status === 'completed')` without worrying
 * about exception handling. The cache is best-effort.
 */
export async function getPendingResponse(
  sessionId: string,
  apiKey: string,
  requestId?: string,
): Promise<PendingResponse> {
  if (!sessionId) return { status: 'not_found' }
  try {
    const qs = requestId ? `?request_id=${encodeURIComponent(requestId)}` : ''
    const resp = await fetch(
      `/v1/sessions/${encodeURIComponent(sessionId)}/pending-response${qs}`,
      {
        method: 'GET',
        headers: { Authorization: `Bearer ${apiKey}` },
      },
    )
    if (resp.status === 404 || resp.status === 503) return { status: 'not_found' }
    if (resp.status === 202) return { status: 'in_progress' }
    if (!resp.ok) return { status: 'not_found' }

    // 200 — body is the replayed vendor response. The X-Gw-Pending-Replay
    // header is the canonical signal that this is a replay (vs some
    // accidental 200 with empty body). Without it we treat as not_found
    // to avoid swallowing unrelated 200s.
    if (resp.headers.get('X-Gw-Pending-Replay') !== 'true') {
      return { status: 'not_found' }
    }
    const ct = resp.headers.get('Content-Type') ?? ''
    const body = await resp.text()
    if (ct.includes('text/event-stream')) {
      return { status: 'completed', body, contentType: ct }
    }
    // Non-SSE 200 — could be a JSON status envelope (e.g. failed entry).
    try {
      const obj = JSON.parse(body) as { status?: string; error_message?: string; body?: string }
      if (obj.status === 'failed') {
        return { status: 'failed', errorMessage: obj.error_message, contentType: ct }
      }
      if (obj.status === 'completed' && typeof obj.body === 'string') {
        return { status: 'completed', body: obj.body, contentType: ct }
      }
    } catch {
      /* fall through */
    }
    return { status: 'not_found' }
  } catch {
    // Network errors / CORS / aborted — treat as cache miss so the
    // caller falls through to a normal request.
    return { status: 'not_found' }
  }
}