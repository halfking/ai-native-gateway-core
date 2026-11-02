// liveStreamIdle.ts — shared idle-marker detection for live stream / topology.
//
// Backend idle heartbeats carry status=idle + error_kind=no_traffic_* and a
 // stable request_id like idle-global-vendor-…. Frontends must treat them as
// "lane quiet", never as request failures.

export type IdleLike = {
  status?: string | null
  type?: string | null
  request_id?: string | null
  requestId?: string | null
  error_kind?: string | null
  errorKind?: string | null
}

export function isNoTrafficErrorKind(kind: string | null | undefined): boolean {
  if (!kind) return false
  return /^no_traffic/i.test(kind.trim())
}

/** True for idle heartbeats from any of the three identity shapes. */
export function isIdleMarker(item: IdleLike | null | undefined): boolean {
  if (!item) return false
  if (item.status === 'idle') return true
  if (item.type === 'idle_marker') return true
  const id = (item.request_id ?? item.requestId ?? '').trim()
  if (id.startsWith('idle-')) return true
  if (isNoTrafficErrorKind(item.error_kind ?? item.errorKind)) return true
  return false
}
