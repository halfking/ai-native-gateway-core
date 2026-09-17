package bg

import "time"

// balance_manual_guard.go — migration 721 (2026-09-18) manual-protection
// predicate, shared by every AUTOMATIC balance writer.
//
// R42 audit (2026-09-18): the predicate previously lived as two hand-copied
// SQL literals (floor-guard Pass A candidate SELECT and probe_v2's cycleAll
// UPDATE) tied together only by "keep in sync" comments — and the floor
// guard's write-time UPDATE carried no predicate at all, so a probe that had
// already passed the candidate SELECT would still clobber a manual value
// patched by the operator while the probe was in flight (TOCTOU). The
// predicate is now one Go constant concatenated into every automatic write
// site; TestManualBalanceGuardPredicateLockstep pins the consumption count
// so a future writer cannot silently skip the guard.
//
// Column names are unqualified so the same text resolves against both the
// credentials⋈providers join (providers carries no balance_* columns) and
// the single-table UPDATEs.
const manualBalanceGuardSQL = "NOT (COALESCE(balance_source, '') = 'manual' AND balance_last_checked_at > NOW() - INTERVAL '24 hours')"

// balanceProbeFailStamp renders the balance_error text for automatic probe
// failures. Vendor response content is deliberately excluded —
// providercap.FetchBalanceUSD discards the body, and balance_error is an
// operator-facing column, not a log sink.
func balanceProbeFailStamp() string {
	return "balance probe failed (network/HTTP/parse) at " + time.Now().UTC().Format(time.RFC3339)
}
