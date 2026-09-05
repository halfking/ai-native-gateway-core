// useTurnTitleSummary.ts — per-turn title/summary fallback for TurnDigestDrawer
// (2026-09-05 audit F2-#9).
//
// The turn-detail endpoint (admin/session_turns_v2.go turnDetailV2Response)
// never produces `title` / `summary`, so the drawer's fallback chain was dead:
// when a turn has no persisted digest the card could only render
// t('turnDigest.empty') even though the turns LIST endpoint
// (GET /api/admin/sessions/{id}/turns → TurnListItem) already carries
// per-turn title/summary. This composable lazily fetches that list once per
// session (best effort), caches it, and exposes a synchronous lookup so the
// three drawer call sites (SessionDetailPage, SessionDrilldownPanel,
// SessionTurnsSyncPane) can pass real title/summary props.
//
// Failure semantics: the lookup is display-only polish. A failed or empty
// list leaves the cache empty and the drawer simply falls back to
// t('turnDigest.empty'); a later drawer open retries because failed sessions
// are never marked cached.
import { listSessionTurns } from '../api/sessions_v2'

export interface TurnTitleSummary {
  title: string
  summary: string
}

const EMPTY: TurnTitleSummary = { title: '', summary: '' }

/** sessionId → (turnNo → {title, summary}) */
const cache = new Map<string, Map<number, TurnTitleSummary>>()
const pending = new Map<string, Promise<void>>()

const LIST_LIMIT = 200

export async function ensureTurnTitleSummary(sessionId: string): Promise<void> {
  if (!sessionId || cache.has(sessionId)) return
  const inflight = pending.get(sessionId)
  if (inflight) return inflight
  const task = listSessionTurns(sessionId, { limit: LIST_LIMIT })
    .then((response) => {
      const map = new Map<number, TurnTitleSummary>()
      for (const item of response.turns ?? []) {
        map.set(item.turn_no, {
          title: item.title?.trim() || '',
          summary: item.summary?.trim() || '',
        })
      }
      cache.set(sessionId, map)
    })
    .catch(() => {
      // Best-effort: leave the session uncached so the next open retries.
    })
    .finally(() => {
      pending.delete(sessionId)
    })
  pending.set(sessionId, task)
  return task
}

export function lookupTurnTitleSummary(
  sessionId: string,
  turnNo: number | null | undefined,
): TurnTitleSummary {
  if (!sessionId || turnNo == null) return EMPTY
  return cache.get(sessionId)?.get(turnNo) ?? EMPTY
}

/** Drop cached entries — all sessions when sessionId is omitted (logout). */
export function clearTurnTitleSummaryCache(sessionId?: string): void {
  if (sessionId === undefined) {
    cache.clear()
    pending.clear()
    return
  }
  cache.delete(sessionId)
}

export function useTurnTitleSummary() {
  return {
    ensureTurnTitleSummary,
    lookupTurnTitleSummary,
    clearTurnTitleSummaryCache,
  }
}
