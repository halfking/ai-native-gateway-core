package bg

import "time"

// balance_manual_protection.go — migration 721 (2026-09-18) manual-balance
// protection window, shared by every AUTOMATIC balance writer.
//
// The predicate skips credentials whose balance_usd was hand-calibrated by an
// operator (admin updateCredential stamps balance_source='manual' on every
// balance_usd PATCH) within the last 24 hours, so automatic probes cannot
// silently overwrite a manual correction. The columns are unqualified on
// purpose: they exist only on credentials, so the same fragment is valid in
// both the floor guard's candidate SELECT (credentials c JOIN providers p)
// and probe_v2's bare UPDATE.
//
// Writers of balance_usd and their contract with this window:
//   - bg/balance_floor_guard.go Pass A candidate SELECT — must filter.
//   - bg/credential_probe_v2.go cycleAll balance UPDATE — must filter.
//   - admin/provider_credential.go updateCredential — writes 'manual' itself.
//   - bg/balance_floor_guard.go refreshBalance failure path — stamps
//     balance_error only (R42); guarded by this predicate.
//   - bg/credential_probe_v2.go cycleAll failure path — same (R42).
//   - admin/provider_credential_balance.go refresh-balance — operator-
//     triggered, intentionally OVERWRITES the manual stamp (the operator
//     asked for fresh data); NOT a consumer of this predicate.
//
// Any new automatic writer must append this predicate to its WHERE clause.
// TestBalanceManualProtectionIsWired (balance_manual_protection_test.go)
// locks the constant content and both call sites; extend it when adding a
// third writer.
const ManualBalanceProtectionPredicate = `NOT (
	COALESCE(balance_source, '') = 'manual'
	AND balance_last_checked_at > NOW() - INTERVAL '24 hours'
)`

// balanceProbeFailStamp renders the balance_error text for automatic probe
// failures (R42, 2026-09-18 audit: the background writers previously failed
// silently — no log, no stamp — leaving a broken vendor balance endpoint
// invisible unless an operator clicked ⟳). Vendor response content is
// deliberately excluded — providercap.FetchBalanceUSD discards the body,
// and balance_error is an operator-facing column, not a log sink. Callers
// must guard the stamp UPDATE with ManualBalanceProtectionPredicate and
// must NOT bump balance_last_checked_at (#12a fail-open retry cadence).
func balanceProbeFailStamp() string {
	return "balance probe failed (network/HTTP/parse) at " + time.Now().UTC().Format(time.RFC3339)
}
