package admin

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// ─────────────────────────────────────────────────────────────────────────
// Outcome-source freshness (S4 audit 2026-09-30 §9.35)
//
// Why this exists. Every reward / avg_reward / ema_reward / success figure the
// auto-route endpoints return is **not measured** by the request being served —
// it is produced later by bg.AutoRouteSettleWorker, which fills in the outcome
// by joining that worker's selection row back to the turns table the worker
// is reading (2026-10-20 迁移 890 之后是会话族 `session_turns_hot`；
// `settleBatch` 把 selection 行接回同一张 turns 表，键是 request_id).
// The cohort baselines the reward normalises against come from the same table
// (`loadTaskBaselines`: percentiles over the same turns table).
//
// So that turns table is this family's **evidence source**, and while it is
// frozen (S4 停写 / v1 族退役) `auto_route_selections` keeps receiving new rows.
// That is the worst failure direction: it does not empty any result set, so
// there is no "0 findings" signal and no empty-result alert. The API keeps
// answering, with numbers.
//
// Concretely, once the newest v1 row is older than settleAbandonAfter, every
// new selection finds `rl.success IS NULL` and takes the abandon branch: the
// worker keeps stamping `settled_at` (a normal-looking terminal state) with a
// NULL reward, and the affinity rollup stops learning — invisibly.
//
// This block makes that visible **at read time**, with no migration and no
// change to any stored semantics. It deliberately does NOT gate the worker:
// gating would only make the number stop moving, and the correct fix for the
// worker is to read the session family, not to be silenced.
//
// ─────────────────────────────────────────────────────────────────────────

// outcomeSourceStaleAfter mirrors bg.settleAbandonAfter. It is the horizon
// inside which a selection can still reach a v1-derived terminal state: past
// it, every selection is abandoned. A freshness gap larger than this therefore
// means "the next sweeps cannot settle anything from v1", which is the exact
// moment the numbers below stop meaning what a reader assumes they mean.
//
// The mirroring is a drift risk, so it is enforced by a test rather than by a
// comment: TestOutcomeSourceStaleAfterMirrorsSettleAbandonAfter parses the
// literal out of bg/auto_route_settle_worker.go and fails if the two diverge.
//
// 4h, not the 24h baselineWindow: the outcome join only needs the request's own
// row, which is written within seconds of the request, so settleAbandonAfter —
// not the baseline lookback — is the binding constraint.
const outcomeSourceStaleAfter = 4 * time.Hour

// outcomeSourceFreshness is the JSON block added to the auto-route responses
// whose numbers are derived from the v1 hot table.
//
//	available  — MAX(ts) was actually read this request. False both when the
//	             v1 hot relation has been dropped (reason=absent) and when the
//	             read failed for any other reason (reason=query_failed); the
//	             reason field is what separates those two, and a consumer can
//	             retry on query_failed but must not on absent.
//	as_of      — MAX(ts) observed in the v1 hot table, i.e. the newest outcome
//	             evidence that exists anywhere.
//	age_seconds— now - as_of. Null when as_of is null (relation present, empty).
//	stale      — age > outcomeSourceStaleAfter, or the relation is gone.
//	reason     — machine-readable why, so a consumer can branch without
//	             re-deriving the arithmetic.
type outcomeSourceFreshness struct {
	Available         bool    `json:"available"`
	AsOf              *string `json:"as_of,omitempty"`
	AgeSeconds        *int64  `json:"age_seconds,omitempty"`
	Stale             bool    `json:"stale"`
	StaleAfterSeconds int64   `json:"stale_after_seconds"`
	Reason            string  `json:"reason"`
}

// Reason codes. Kept as bare strings because they cross into the API
// contract; the set is closed and TestOutcomeSourceReasonsAreClosed fails on
// an unlisted one.
const (
	outcomeReasonLive        = "live"         // fresh enough to settle from
	outcomeReasonNoRows      = "no_rows"      // relation present, no rows at all
	outcomeReasonStale       = "stale"        // rows exist but predate the horizon
	outcomeReasonAbsent      = "absent"       // v1 relation dropped: retirement done
	outcomeReasonQueryFailed = "query_failed" // transient; treated as stale
)

// queryOutcomeFreshness reads MAX(ts) from the relation the settle worker is
// actually reading this instant.
//
// 2026-10-16: this used to hardcode `_request_logs_hot`. That was correct
// while stop-write was only a plan, but bg.settleSourceFor switches the
// worker's source to `session_turns_hot` the moment the write gate closes, so a
// v1-only probe spent every post-stop-write run reporting `stale` against a
// table nobody reads any more — an alarm that asserts the opposite of the
// truth. Resolving the table through bg.ActiveSettleTurnsTable keeps the
// probe and the worker on one source (the same invariant settleSourceSpec's
// header demands for the outcome/baseline/LATERAL legs).
//
// Deliberately raw SQL: the only interpolated value is a constant chosen from a
// closed set inside this package, never caller input, so there is no injection
// surface.
//
// The 3s handler budget is not at risk: measured on the live database the v1
// form of this was an Index Only Scan on idx_request_logs_hot_ts, 0.275ms
// execution, 5 buffers; the session form uses idx_session_turns_hot_ts.
func queryOutcomeFreshness(ctx context.Context, db outcomeFreshnessDB, now time.Time) outcomeSourceFreshness {
	// Start pessimistic: everything that is not a positively-fresh reading
	// reports stale. The live branch below has to CLEAR these, which is why
	// Stale is not set by a bare early return — a reader must never have to
	// wonder whether an unvisited field was considered or merely forgotten.
	out := outcomeSourceFreshness{
		StaleAfterSeconds: int64(outcomeSourceStaleAfter / time.Second),
		Reason:            outcomeReasonQueryFailed,
		Stale:             true,
		Available:         false,
	}

	var asOf *time.Time
	if err := db.QueryRow(ctx, "SELECT MAX(ts) FROM "+bg.ActiveSettleTurnsTable()).Scan(&asOf); err != nil {
		// A dropped relation is the EXPECTED end state of the retirement, not a
		// fault: report it as such and let the endpoint answer normally.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			out.Available = false
			out.Reason = outcomeReasonAbsent
		}
		// anything else keeps Reason=query_failed
		return out
	}

	out.Available = true
	if asOf == nil {
		out.Reason = outcomeReasonNoRows
		return out
	}

	// ts is timestamptz; render in the server's own rendering so the value
	// round-trips through JSON the same way every other ts field in this
	// package does (they all use ::text casts).
	asOfText := asOf.UTC().Format(time.RFC3339)
	age := int64(now.Sub(*asOf) / time.Second)
	if age < 0 {
		// A clock skew or a future-dated row must not read as "very fresh";
		// clamp at 0 and let the age field show it.
		age = 0
	}
	out.AsOf = &asOfText
	out.AgeSeconds = &age
	if age > out.StaleAfterSeconds {
		out.Reason = outcomeReasonStale
		return out
	}
	// The only branch that reports a settled-from-v1 reading. It must clear
	// both flags explicitly: they were initialised pessimistic on purpose.
	out.Reason = outcomeReasonLive
	out.Stale = false
	return out
}

// outcomeFreshnessDB is the one method this needs, kept minimal so the
// freshness rule is unit-testable without a pool. *pgxpool.Pool satisfies it.
type outcomeFreshnessDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
