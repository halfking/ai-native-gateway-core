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

export function credentialDisplayName(
  candidate: { credential_label?: string | null; provider_name?: string | null } | null | undefined,
  fallbackProvider: string,
  credentialId: number,
): string {
  const label = (candidate?.credential_label || '').trim()
  if (label) return label
  return `${fallbackProvider} · #${credentialId}`
}
