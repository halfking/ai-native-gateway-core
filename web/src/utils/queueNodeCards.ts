/** Queue perspective node-card helpers (priority spacing, capacity width, titles). */

export const PRIORITY_STEP = 5
export const PRIORITY_MAX = 99
export const WINDOW_MINUTES = 5
export const STATS_REFRESH_MS = 30_000
export const CARD_MIN_W = 120
export const CARD_MAX_W = 280
/** Mini window cells on queue node cards (batch include_entries cap). */
export const CARD_ENTRY_LIMIT = 24

/** Replace one card's window cells. Missing entries (JSON omitempty) means empty, not "keep stale". */
export function mergeCardWindowEntries<T>(
  dest: Map<string, T[]>,
  key: string,
  entries: T[] | undefined,
  limit = CARD_ENTRY_LIMIT,
): void {
  dest.set(key, (entries ?? []).slice(0, limit))
}

/** Assign unique priorities with preferred step=5 (5,10,15…). Compresses when n*step > 99. */
export function assignSpacedPriorities(count: number, step = PRIORITY_STEP, max = PRIORITY_MAX): number[] {
  if (count <= 0) return []
  if (count > max) {
    throw new Error(`cannot assign unique priorities for ${count} items within [1, ${max}]`)
  }
  let useStep = Math.max(1, step)
  if (count * useStep > max) {
    useStep = Math.max(1, Math.floor(max / count))
  }
  const out: number[] = []
  for (let i = 0; i < count; i++) {
    out.push(Math.min(max, (i + 1) * useStep))
  }
  // Guarantee uniqueness if compression collapsed trailing values onto max.
  const seen = new Set<number>()
  for (let i = 0; i < out.length; i++) {
    let p = out[i]
    while (seen.has(p) && p > 1) p -= 1
    while (seen.has(p) && p < max) p += 1
    if (seen.has(p)) {
      throw new Error(`cannot assign unique priorities for ${count} items within [1, ${max}]`)
    }
    seen.add(p)
    out[i] = p
  }
  return out
}

export function cardWidthFromCapacity(cap: number, maxCapInGroup: number, minW = CARD_MIN_W, maxW = CARD_MAX_W): number {
  const safeCap = Math.max(1, cap || 1)
  const safeMax = Math.max(1, maxCapInGroup || 1)
  const ratio = Math.min(1, safeCap / safeMax)
  return Math.round(minW + ratio * (maxW - minW))
}

export function nodeCapacity(candidate: { effective_concurrency?: number | null; concurrency_limit?: number | null } | null | undefined): number {
  const effective = candidate?.effective_concurrency
  if (typeof effective === 'number' && effective > 0) return effective
  const limit = candidate?.concurrency_limit
  if (typeof limit === 'number' && limit > 0) return limit
  return 1
}

/** Matches provider resolve SQL: COALESCE(quota_state, 'ok') = 'ok'. */
export function quotaAllowsPriority(quota: string | null | undefined): boolean {
  const q = (quota ?? 'ok').trim().toLowerCase()
  return q === '' || q === 'ok'
}

/** Minimal resolve candidate fields for stable queue ordering. */
export interface RoutingCandidateOrderKey {
  credential_id: number
  manual_priority?: number | null
  priority?: boolean | null
  quota_state?: string | null
  tier?: number | null
  weight?: number | null
  composite_score?: number | null
}

/** Mirror admin sortResolveCandidatesStable: priority flag → manual_priority ASC → tier → weight DESC → score DESC → credential_id. */
export function compareRoutingCandidates(
  left: RoutingCandidateOrderKey,
  right: RoutingCandidateOrderKey,
): number {
  const leftPriority = Boolean(left.priority) && quotaAllowsPriority(left.quota_state)
  const rightPriority = Boolean(right.priority) && quotaAllowsPriority(right.quota_state)
  if (leftPriority !== rightPriority) return leftPriority ? -1 : 1

  const leftManual = left.manual_priority ?? 99
  const rightManual = right.manual_priority ?? 99
  if (leftManual !== rightManual) return leftManual - rightManual

  const leftTier = left.tier ?? 2
  const rightTier = right.tier ?? 2
  if (leftTier !== rightTier) return leftTier - rightTier

  const leftWeight = left.weight ?? 100
  const rightWeight = right.weight ?? 100
  if (leftWeight !== rightWeight) return rightWeight - leftWeight

  const leftScore = left.composite_score ?? 0
  const rightScore = right.composite_score ?? 0
  if (leftScore !== rightScore) return rightScore - leftScore

  return left.credential_id - right.credential_id
}

export function sortRoutingCandidates<T extends RoutingCandidateOrderKey>(candidates: T[]): T[] {
  return [...candidates].sort(compareRoutingCandidates)
}

/** Order live nodes by resolve candidate rank; unknown candidates trail, tie-break credential_id. */
export function orderNodesByRoutingCandidates<T extends { credential_id: number }>(
  nodes: T[],
  candidatesByCredential: Map<number, RoutingCandidateOrderKey>,
): T[] {
  const ranked = sortRoutingCandidates([...candidatesByCredential.values()])
  const rankByCredential = new Map(ranked.map((candidate, index) => [candidate.credential_id, index]))
  return [...nodes].sort((left, right) => {
    const leftRank = rankByCredential.get(left.credential_id)
    const rightRank = rankByCredential.get(right.credential_id)
    if (leftRank != null && rightRank != null) {
      if (leftRank !== rightRank) return leftRank - rightRank
      return left.credential_id - right.credential_id
    }
    if (leftRank != null) return -1
    if (rightRank != null) return 1
    return left.credential_id - right.credential_id
  })
}

export function credentialDisplayName(
  candidate: { credential_label?: string | null; provider_name?: string | null } | null | undefined,
  fallbackProvider: string,
  credentialId: number,
  nodeLabel?: string | null,
): string {
  const label = (candidate?.credential_label || nodeLabel || '').trim()
  if (label) return label
  return `${fallbackProvider} · #${credentialId}`
}
