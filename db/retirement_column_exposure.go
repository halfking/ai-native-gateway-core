package db

// Retirement column exposure (§9.161 / §9.162).
//
// The fill-rate measurement in db/session_family_column_availability_test.go
// establishes, per canonical column, whether the session family can still serve
// it once `request_logs` is dropped. This file is the **single source of truth**
// for the two dispositions that measurement produces, so that the read-side
// exposure analysis (admin/request_logs_retirement_exposure_test.go) does not
// carry its own copy of the list.
//
// Duplicating the list would be the failure mode worth naming: a second copy
// drifts silently, and the drifted copy is the one a reviewer trusts because it
// is the one sitting next to the code being reviewed.
//
// The lists are data, so they are NOT trusted on their own —
// TestSessionFamilyColumnAvailability_FillRates re-measures and fails if they no
// longer match the database. A list that can be contradicted by a measurement is
// a fact; a list that cannot is a comment.

// RetirementUnservableColumns are columns that go to **0% fill** on the session
// side while v1 still holds values, i.e. after retirement they lose all their
// data. Measured 2026-10-04 on the local real database.
//
// A reader of any of these breaks outright at retirement, so they are the
// sharp end of the exposure list.
var RetirementUnservableColumns = []string{
	"is_final_success", // 0.00% session vs 100.00% v1
	"client_protocol",  // 0.00% session vs 37.02% v1
	"work_type",        // 0.00% session vs 1.93% v1 (v1 is nearly empty too, so the loss is small)
}

// RetirementDegradedColumns are columns whose session-side fill rate is more
// than 5 percentage points below v1. Measured 2026-10-04 on the local real
// database.
//
// Degradation is not breakage: a reader still returns rows, it just returns
// fewer of them, and a SUM over such a column silently under-counts. That is why
// this list is a "check the arithmetic" list rather than a "this is broken" list.
var RetirementDegradedColumns = []string{
	"application_id", "client_model", "credential_id", "provider_id",
	"canonical_id", "total_tokens", "search_text", "egress_protocol",
	"failure_stage", "usage_source", "auto_decision", "compression_reason",
	"outbound_msg_hashes", "quality_flags", "quality_fix_actions",
	"stream_chunks_sent", "client_request_id", "attachments", "request_type",
	"origin_actor", "request_class", "client_ip", "origin_stage",
	"client_forwarded_for",
}

// RetirementStructuralGapColumns are columns the session family has **no source
// for at all** — the projection is a literal NULL placeholder, so no amount of
// backfilling changes them. Mirrors the registry inside
// TestSessionFamilyColumnAvailability_StructuralGaps, which is what actually
// pins it; this copy exists so read-side code can enumerate them without
// reaching into a test file.
var RetirementStructuralGapColumns = []string{
	"id", "test_col", "test_tab_indent", "provider_model", "credits_rate_multiplier",
}

// retiredColumnSet is the helper every consumer uses, so membership is decided
// once and the buckets cannot overlap by accident.
func retiredColumnSet(cols []string) map[string]bool {
	out := make(map[string]bool, len(cols))
	for _, c := range cols {
		out[c] = true
	}
	return out
}

// RetirementExposureClassify returns the exposure class of a canonical column.
//
// The classes are mutually exclusive and total over the contract, which is what
// makes "every column was classified" a checkable statement rather than an
// assertion. Order matters: structural gap first (the column can never be
// served), then unservable (it has a source but currently carries nothing), then
// degraded, then baseline.
//
// There is deliberately **no hand-maintained "safe" list**. A 118-name list kept
// by hand is a second thing to drift, and a drifted "safe" entry is worse than no
// entry: it tells a reviewer a column was checked when nobody checked it. Instead
// the default is the baseline class and
// TestRetirementColumnExposureIsTotalAndDisjoint enforces that every contract
// column lands in exactly one class, so a brand-new column cannot slip through
// unclassified — it shows up as baseline, in a test that also asserts the three
// non-baseline lists still match a live measurement.
func RetirementExposureClassify(col string) string {
	switch {
	case retiredColumnSet(RetirementStructuralGapColumns)[col]:
		return "structural-gap"
	case retiredColumnSet(RetirementUnservableColumns)[col]:
		return "unservable"
	case retiredColumnSet(RetirementDegradedColumns)[col]:
		return "degraded"
	default:
		return "baseline"
	}
}
