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

// ColumnFill is the measured fill rate of one canonical column on each side.
//
// This is the number that decides D14-a. "Repoint this reader to the canonical
// view" is not a fix, it is a **substitution of one data source for another**,
// and for several readers the substitution is a silent partial-data loss that
// is worse than the loud failure of a missing table: a query that returns 18% of
// its rows looks exactly like a query that is working.
type ColumnFill struct {
	SessionPct float64 // non-null share on session_turns_hot ∪ session_turns
	V1Pct      float64 // non-null share on request_logs
}

// Measured 2026-10-04 on the local real database by
// TestSessionFamilyColumnAvailability_FillRates, which also re-derives and
// compares this table — so a drift in the data turns into a red test rather
// than a stale claim in a decision sheet.
//
// ⚠️ These are a **lower bound on quality**, not a final answer: the local
// database is being written by a pre-823 binary (audit §9.163.2), so the session
// side is missing at least everything 823-and-later added. A production
// read-only run (D15-c) will raise some of these.
var RetirementColumnFill = map[string]ColumnFill{
	"application_id":       {SessionPct: 3.51, V1Pct: 37.00},
	"is_final_success":     {SessionPct: 0.00, V1Pct: 100.00},
	"client_protocol":      {SessionPct: 0.00, V1Pct: 37.02},
	"work_type":            {SessionPct: 0.00, V1Pct: 1.93},
	"canonical_id":         {SessionPct: 0.07, V1Pct: 9.79},
	"egress_protocol":      {SessionPct: 0.07, V1Pct: 9.79},
	"search_text":          {SessionPct: 0.00, V1Pct: 100.00},
	"client_ip":            {SessionPct: 12.21, V1Pct: 18.77},
	"client_forwarded_for": {SessionPct: 12.21, V1Pct: 18.77},
	"client_request_id":    {SessionPct: 15.73, V1Pct: 65.65},
	"request_type":         {SessionPct: 18.15, V1Pct: 100.00},
	"quality_fix_actions":  {SessionPct: 18.15, V1Pct: 100.00},
	"attachments":          {SessionPct: 18.19, V1Pct: 100.00},
	"outbound_msg_hashes":  {SessionPct: 22.75, V1Pct: 100.00},
	"usage_source":         {SessionPct: 39.71, V1Pct: 100.00},
	"auto_decision":        {SessionPct: 44.69, V1Pct: 100.00},
	"compression_reason":   {SessionPct: 46.96, V1Pct: 64.80},
	"stream_chunks_sent":   {SessionPct: 53.25, V1Pct: 100.00},
	"quality_flags":        {SessionPct: 54.80, V1Pct: 100.00},
	"provider_id":          {SessionPct: 55.82, V1Pct: 72.79},
	"origin_actor":         {SessionPct: 56.59, V1Pct: 74.98},
	"origin_stage":         {SessionPct: 56.82, V1Pct: 81.75},
	"credential_id":        {SessionPct: 62.65, V1Pct: 72.79},
	"request_class":        {SessionPct: 60.43, V1Pct: 100.00},
	"total_tokens":         {SessionPct: 58.36, V1Pct: 100.00},
	"failure_stage":        {SessionPct: 73.92, V1Pct: 85.36},
	"client_model":         {SessionPct: 90.06, V1Pct: 100.00},
}

// RetirementRepointVerdict is the answer to "would repointing this reader to the
// canonical view actually work?".
type RetirementRepointVerdict string

const (
	// RepointSafe: every needed column is at v1 parity on the session side, so
	// switching sources costs nothing.
	RepointSafe RetirementRepointVerdict = "repoint-safe"
	// RepointDegraded: the reader would still work, but on a fraction of the
	// rows. **This is the dangerous one** — a query returning 18% of its rows
	// is indistinguishable from a query that is working.
	RepointDegraded RetirementRepointVerdict = "repoint-degraded"
	// RepointEmpty: a needed column is at 0% on the session side, so the
	// reader returns nothing. Loud rather than silent, but still not a fix.
	RepointEmpty RetirementRepointVerdict = "repoint-empty"
	// RepointGapOnly: every needed column is either servable or a structural
	// gap, and the gaps are the only blockers. `id` lands here: the view has the
	// column and it is always NULL, because v1's request-row id and the turn id
	// are different things.
	RepointGapOnly RetirementRepointVerdict = "repoint-gap-only"
	// UnknownColumn: a needed column has no measured fill rate. Reported rather
	// than assumed — a column nobody measured is not a column anybody may claim
	// is safe.
	UnknownColumn RetirementRepointVerdict = "unknown-column"
)

// RetirementRepointVerdictFor computes what repointing to the canonical view
// would do to a reader that needs the given columns.
//
// The thresholds are deliberately blunt, and the reason is in the name: the
// question is not "how much data is lost", it is "will the reader look like it
// works". 5 percentage points is where a reader stops being a faithful
// substitute and starts being a plausible-looking lie.
func RetirementRepointVerdictFor(cols []string) RetirementRepointVerdict {
	worst := RepointSafe
	rank := map[RetirementRepointVerdict]int{
		RepointSafe: 0, RepointDegraded: 1, RepointGapOnly: 2,
		UnknownColumn: 3, RepointEmpty: 4,
	}
	for _, c := range cols {
		v := RepointSafe
		switch class := RetirementExposureClassify(c); class {
		case "baseline":
			// Already at or above v1 parity by definition, so it needs no
			// recorded rate. Without this arm, every parity column (latency_ms,
			// prompt_tokens, cost_usd …) falls through to the rate lookup, is
			// absent from RetirementColumnFill, and comes out as
			// `unknown-column` — i.e. **the columns that are in the best shape
			// are reported as the least known**. That is backwards, and it is
			// how admin/logs.go first came out mislabelled.
			v = RepointSafe
		case "structural-gap":
			// The view carries the column; it is a NULL placeholder. Not data
			// loss, but the reader loses the value entirely.
			v = RepointGapOnly
		default:
			f, ok := RetirementColumnFill[c]
			if !ok {
				v = UnknownColumn
			} else if f.SessionPct == 0 && f.V1Pct > 0 {
				v = RepointEmpty
			} else if f.SessionPct+5 < f.V1Pct {
				v = RepointDegraded
			}
		}
		if rank[v] > rank[worst] {
			worst = v
		}
	}
	return worst
}
