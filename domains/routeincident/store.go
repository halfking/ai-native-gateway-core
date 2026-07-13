// Package routeincident — store.go
//
// Persistent state for route_incidents and route_incident_events. The
// store is the only writer; the observer is the only caller during
// normal operation. Read paths (List / Get / Events / Timeline) are
// used by the read-only API.
//
// All transitions are guarded by a SELECT ... FOR UPDATE on the
// active/recovering aggregate row. Each event insertion is
// idempotent on (incident_id, request_id, terminal_status) so a
// retried observer can never double-count a streak.
package routeincident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistent layer. All methods are safe for concurrent
// use because each transition holds its own row lock.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store. The pool must be non-nil.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers (admin handler) that
// need to run cross-table timeline queries against request_logs.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// TransitionInput carries everything the observer knows about the
// terminal request that just landed. The store maps it to the route
// key and runs the state machine.
type TransitionInput struct {
	TenantID       string
	Protocol       string
	Model          string
	ProviderID     *int64
	CredentialID   *int64
	RequestID      string
	TerminalStatus string // "success" | "failure"
	FailureKind    string // optional, sanitized
	FailureStage   string // optional, sanitized "gateway" | "upstream"
	OccurredAt     time.Time
}

// TransitionResult is what the observer hands to the SSE hub. Visible
// is false ONLY when the state is recovered.
type TransitionResult struct {
	Incident *Incident
	Visible  bool
	Update   IncidentUpdate
	NoOp     bool   // true when the request did not move state (e.g. success with no incident)
	Reason   string // diagnostic reason for logs
}

// ErrUnqualified is returned by Transition when the request is not
// eligible to count toward an incident (e.g. a non-terminal request
// slipped through). The observer uses this to short-circuit.
var ErrUnqualified = errors.New("routeincident: request not eligible for incident transition")

// ErrNoDatabase is returned by Transition when the store has no pool.
// Operators should treat this as fatal for the observer; the SSE
// hub degrades to a no-op.
var ErrNoDatabase = errors.New("routeincident: no database pool configured")

// Transition applies the state machine to the row implied by the
// route key, in a single transaction, and returns the resulting
// Incident (or nil if there is no incident and no transition
// happened).
//
// Algorithm:
//  1. Begin a transaction.
//  2. SELECT ... FOR UPDATE the active/recovering row for this route
//     key (or the first row of any state if you want to recover from
//     a previous bug — but for phase 1 we only consider
//     active/recovering).
//  3. Run the pure DecideState from state.go.
//  4. UPSERT the aggregate.
//  5. Insert the idempotent event row.
//  6. Commit.
//
// The transaction isolation is READ COMMITTED (Postgres default);
// the FOR UPDATE on the unique partial index is sufficient because
// only one active/recovering row can exist per route.
func (s *Store) Transition(ctx context.Context, in TransitionInput) (*TransitionResult, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if in.TenantID == "" || in.Model == "" {
		return nil, fmt.Errorf("%w: missing tenant_id or model", ErrUnqualified)
	}
	if in.TerminalStatus != TerminalSuccess && in.TerminalStatus != TerminalFailure {
		return nil, fmt.Errorf("%w: terminal_status=%q", ErrUnqualified, in.TerminalStatus)
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockActive(ctx, tx, in.TenantID, in.Protocol, in.Model, in.ProviderID, in.CredentialID)
	if err != nil {
		return nil, err
	}

	newState, fs, rs, applied := DecideState(cur, in.TerminalStatus, DefaultThresholds())
	if !applied {
		// Nothing to do. The pure state machine decided the request
		// does not move the state machine — e.g. a success with no
		// incident. Commit (release the lock) and return a NoOp.
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit noop: %w", err)
		}
		return &TransitionResult{NoOp: true, Reason: "state machine decided no transition"}, nil
	}

	var incident *Incident
	isOpen := false
	if cur == nil {
		// Open a brand-new incident.
		isOpen = true
		incident, err = insertNew(ctx, tx, in, newState, fs, rs)
		if err != nil {
			return nil, err
		}
	} else {
		incident, err = updateExisting(ctx, tx, cur, in, newState, fs, rs)
		if err != nil {
			return nil, err
		}
	}

	// Idempotent event insertion.
	if err := insertEvent(ctx, tx, incident.ID, in, newState, fs, rs, isOpen); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	update := BuildUpdate(incident)
	return &TransitionResult{
		Incident: incident,
		Visible:  update.Visible,
		Update:   update,
		Reason:   fmt.Sprintf("state -> %s (failure=%d, recovery=%d)", newState, fs, rs),
	}, nil
}

// lockActive fetches the active/recovering row for the given route
// key with a row lock. Returns nil if no such row exists. The
// provider/credential columns are nullable, so we use COALESCE in
// the WHERE clause to match the partial unique index.
func lockActive(
	ctx context.Context, tx pgx.Tx,
	tenantID, protocol, model string,
	providerID, credentialID *int64,
) (*Incident, error) {
	const sql = `
		SELECT id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		       state, failure_streak, recovery_streak,
		       first_failure_at, last_failure_at, last_success_at, recovered_at,
		       total_failures, total_successes,
		       last_error_kind, last_failure_stage,
		       version, created_at, updated_at
		FROM route_incidents
		WHERE tenant_id = $1
		  AND endpoint_protocol = $2
		  AND model = $3
		  AND COALESCE(provider_id, 0) = COALESCE($4, 0)
		  AND COALESCE(credential_id, 0) = COALESCE($5, 0)
		  AND state IN ('active', 'recovering')
		FOR UPDATE
	`
	row := tx.QueryRow(ctx, sql, tenantID, protocol, model, providerID, credentialID)
	inc, err := scanIncident(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock active: %w", err)
	}
	return inc, nil
}

// scanIncident is a tiny adapter so the FOR UPDATE row can share a
// scan with regular reads.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanIncident(row rowScanner) (*Incident, error) {
	var inc Incident
	var protocol, model, state string
	var providerID, credentialID *int64
	if err := row.Scan(
		&inc.ID, &inc.RouteKey.TenantID, &protocol, &model, &providerID, &credentialID,
		&state, &inc.FailureStreak, &inc.RecoveryStreak,
		&inc.FirstFailureAt, &inc.LastFailureAt, &inc.LastSuccessAt, &inc.RecoveredAt,
		&inc.TotalFailures, &inc.TotalSuccesses,
		&inc.LastErrorKind, &inc.LastFailureStage,
		&inc.Version, &inc.CreatedAt, &inc.UpdatedAt,
	); err != nil {
		return nil, err
	}
	inc.State = State(state)
	inc.RouteKey.Protocol = protocol
	inc.RouteKey.Model = model
	inc.RouteKey.ProviderID = providerID
	inc.RouteKey.CredentialID = credentialID
	return &inc, nil
}

// insertNew opens a new active incident for the first qualifying
// failure. The store relies on the partial unique index
// (uq_route_incidents_active_route) to fail loudly on a race so the
// observer can retry the same request against the now-existing row.
func insertNew(ctx context.Context, tx pgx.Tx, in TransitionInput, state State, fs, rs int) (*Incident, error) {
	const sql = `
		INSERT INTO route_incidents (
			tenant_id, endpoint_protocol, model, provider_id, credential_id,
			state, failure_streak, recovery_streak,
			first_failure_at, last_failure_at,
			total_failures, total_successes,
			last_error_kind, last_failure_stage
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, 1, 0, $10, $11)
		RETURNING id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		          state, failure_streak, recovery_streak,
		          first_failure_at, last_failure_at, last_success_at, recovered_at,
		          total_failures, total_successes,
		          last_error_kind, last_failure_stage,
		          version, created_at, updated_at
	`
	failureKind := nullableString(RedactErrorKind(in.FailureKind))
	failureStage := nullableString(SanitizeStage(in.FailureStage))
	row := tx.QueryRow(ctx, sql,
		in.TenantID, in.Protocol, in.Model, in.ProviderID, in.CredentialID,
		string(state), fs, rs,
		in.OccurredAt,
		failureKind, failureStage,
	)
	inc, err := scanIncident(row)
	if err != nil {
		return nil, fmt.Errorf("insert incident: %w", err)
	}
	return inc, nil
}

// updateExisting applies a non-null transition to an existing row.
// The state machine has already decided the new state, the streak
// counters, and the totals; this function just renders them into SQL.
func updateExisting(ctx context.Context, tx pgx.Tx, cur *Incident, in TransitionInput, state State, fs, rs int) (*Incident, error) {
	// Build the patch list dynamically so we don't overwrite
	// recovered_at when the previous state was recovering.
	var totalFailures, totalSuccesses int64 = cur.TotalFailures, cur.TotalSuccesses
	var lastFailureAt = cur.LastFailureAt
	var lastSuccessAt = cur.LastSuccessAt
	var recoveredAt = cur.RecoveredAt
	var lastErrorKind = cur.LastErrorKind
	var lastFailureStage = cur.LastFailureStage

	switch in.TerminalStatus {
	case TerminalFailure:
		totalFailures++
		t := in.OccurredAt
		lastFailureAt = &t
		if k := RedactErrorKind(in.FailureKind); k != "" {
			s := k
			lastErrorKind = &s
		}
		if s := SanitizeStage(in.FailureStage); s != "" {
			lastFailureStage = &s
		}
	case TerminalSuccess:
		totalSuccesses++
		t := in.OccurredAt
		lastSuccessAt = &t
		if state == StateRecovered {
			recoveredAt = &t
		}
	}

	const sql = `
		UPDATE route_incidents
		SET state = $2,
		    failure_streak = $3,
		    recovery_streak = $4,
		    last_failure_at = $5,
		    last_success_at = $6,
		    recovered_at = $7,
		    total_failures = $8,
		    total_successes = $9,
		    last_error_kind = $10,
		    last_failure_stage = $11,
		    version = version + 1
		WHERE id = $1
		  AND version = $12
		RETURNING id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		          state, failure_streak, recovery_streak,
		          first_failure_at, last_failure_at, last_success_at, recovered_at,
		          total_failures, total_successes,
		          last_error_kind, last_failure_stage,
		          version, created_at, updated_at
	`
	row := tx.QueryRow(ctx, sql,
		cur.ID,
		string(state), fs, rs,
		lastFailureAt, lastSuccessAt, recoveredAt,
		totalFailures, totalSuccesses,
		lastErrorKind, lastFailureStage,
		cur.Version,
	)
	inc, err := scanIncident(row)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}
	return inc, nil
}

// insertEvent writes the immutable evidence row. The unique index
// (incident_id, request_id, terminal_status) makes this idempotent —
// a retried observer simply hits a duplicate-key error which we
// swallow and treat as success.
//
// The "opened" event is emitted on the very first failure that
// created the incident; all subsequent failures are tagged
// failure_observed. The caller distinguishes the two via the
// `isOpen` flag so the writer does not need to read prior rows.
func insertEvent(
	ctx context.Context, tx pgx.Tx, incidentID string, in TransitionInput,
	newState State, fs, rs int, isOpen bool,
) error {
	eventType := EventFailureObserved
	switch {
	case isOpen:
		eventType = EventOpened
	case newState == StateRecovered:
		eventType = EventRecovered
	case newState == StateRecovering:
		eventType = EventRecoveryProgress
	}

	evidence := map[string]any{}
	if k := RedactErrorKind(in.FailureKind); k != "" {
		evidence["failure_kind"] = k
	}
	if s := SanitizeStage(in.FailureStage); s != "" {
		evidence["failure_stage"] = s
	}
	if in.RequestID != "" {
		evidence["sample_request_ids"] = []string{in.RequestID}
	}
	if !in.OccurredAt.IsZero() {
		evidence["ts"] = in.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	safe := SanitizeEvidence(evidence)
	evidenceJSON, err := json.Marshal(safe)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	var requestID, terminalStatus, failureKind, failureStage *string
	var fsi, rsi *int
	if in.RequestID != "" {
		requestID = &in.RequestID
	}
	if in.TerminalStatus != "" {
		terminalStatus = &in.TerminalStatus
	}
	if k := RedactErrorKind(in.FailureKind); k != "" {
		failureKind = &k
	}
	if s := SanitizeStage(in.FailureStage); s != "" {
		failureStage = &s
	}
	if fs > 0 {
		f := fs
		fsi = &f
	}
	if rs > 0 {
		r := rs
		rsi = &r
	}

	const sql = `
		INSERT INTO route_incident_events (
			incident_id, event_type,
			request_id, terminal_status,
			failure_kind, failure_stage,
			failure_streak, recovery_streak,
			evidence
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (incident_id, request_id, terminal_status)
		WHERE request_id IS NOT NULL
		DO NOTHING
	`
	_, err = tx.Exec(ctx, sql,
		incidentID, string(eventType),
		requestID, terminalStatus,
		failureKind, failureStage,
		fsi, rsi,
		evidenceJSON,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// nullableString converts an empty string to a nil pointer so the DB
// gets NULL rather than ”.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ─── Read paths ────────────────────────────────────────────────────────

// ListFilter narrows the result set.
type ListFilter struct {
	TenantID    string // "" means all tenants (super-admin only)
	State       string // "active" | "recovering" | "recovered" | "" (all)
	Limit       int    // 0 → default 100
	OnlyVisible bool   // when true, exclude recovered (used by SSE hint)
}

// List returns recent incidents ordered by updated_at DESC.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Incident, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conds := []any{}
	where := "WHERE 1=1"
	if f.TenantID != "" {
		conds = append(conds, f.TenantID)
		where += fmt.Sprintf(" AND tenant_id = $%d", len(conds))
	}
	if f.State != "" {
		conds = append(conds, f.State)
		where += fmt.Sprintf(" AND state = $%d", len(conds))
	}
	if f.OnlyVisible {
		where += " AND state IN ('active', 'recovering')"
	}
	conds = append(conds, limit)
	sql := fmt.Sprintf(`
		SELECT id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		       state, failure_streak, recovery_streak,
		       first_failure_at, last_failure_at, last_success_at, recovered_at,
		       total_failures, total_successes,
		       last_error_kind, last_failure_stage,
		       version, created_at, updated_at
		FROM route_incidents
		%s
		ORDER BY updated_at DESC
		LIMIT $%d
	`, where, len(conds))
	rows, err := s.pool.Query(ctx, sql, conds...)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inc)
	}
	return out, rows.Err()
}

// Get fetches a single incident by id, scoped to a tenant. Returns
// (nil, nil) when not found OR cross-tenant (the caller cannot tell
// the difference — by design).
func (s *Store) Get(ctx context.Context, tenantID, id string) (*Incident, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if id == "" {
		return nil, nil
	}
	const sql = `
		SELECT id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		       state, failure_streak, recovery_streak,
		       first_failure_at, last_failure_at, last_success_at, recovered_at,
		       total_failures, total_successes,
		       last_error_kind, last_failure_stage,
		       version, created_at, updated_at
		FROM route_incidents
		WHERE id = $1
		  AND tenant_id = $2
	`
	row := s.pool.QueryRow(ctx, sql, id, tenantID)
	inc, err := scanIncident(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	return inc, nil
}

// Events returns the immutable event trail for an incident, scoped
// to the tenant. Limit 0 → 200.
func (s *Store) Events(ctx context.Context, tenantID, incidentID string, limit int) ([]IncidentEvent, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if incidentID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	const sql = `
		SELECT e.id, e.incident_id, e.event_type, e.request_id, e.terminal_status,
		       e.failure_kind, e.failure_stage, e.failure_streak, e.recovery_streak,
		       e.evidence, e.actor, e.created_at
		FROM route_incident_events e
		JOIN route_incidents i ON i.id = e.incident_id
		WHERE e.incident_id = $1
		  AND i.tenant_id = $2
		ORDER BY e.created_at ASC, e.id ASC
		LIMIT $3
	`
	rows, err := s.pool.Query(ctx, sql, incidentID, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	out := []IncidentEvent{}
	for rows.Next() {
		var e IncidentEvent
		var evidenceJSON []byte
		if err := rows.Scan(
			&e.ID, &e.IncidentID, &e.EventType, &e.RequestID, &e.TerminalStatus,
			&e.FailureKind, &e.FailureStage, &e.FailureStreak, &e.RecoveryStreak,
			&evidenceJSON, &e.Actor, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		if len(evidenceJSON) > 0 {
			_ = json.Unmarshal(evidenceJSON, &e.Evidence)
			e.Evidence = SanitizeEvidence(e.Evidence)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Timeline24h returns 5-minute buckets for the given incident over
// the last 24 hours, scoped to the tenant. Aggregates are derived
// from request_logs (read-only) — never inserted into the
// route_incidents aggregate. Insufficient data is explicit: the
// store returns an empty slice and the API layer renders a banner.
func (s *Store) Timeline24h(ctx context.Context, tenantID, incidentID string) ([]IncidentTimelinePoint, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if incidentID == "" {
		return nil, nil
	}
	// Look up the route key first (tenant-scoped).
	inc, err := s.Get(ctx, tenantID, incidentID)
	if err != nil || inc == nil {
		return nil, err
	}
	const sql = `
		WITH buckets AS (
			SELECT date_trunc('minute', rl.ts) - (EXTRACT(MINUTE FROM rl.ts)::int % 5) * INTERVAL '1 minute' AS bucket,
			       rl.success, rl.latency_ms, rl.request_status
			FROM request_logs_with_current_month rl
			WHERE rl.tenant_id = $1
			  AND rl.ts >= NOW() - INTERVAL '24 hours'
			  AND (rl.canonical_id = $2 OR rl.outbound_model = $3 OR rl.client_model = $3)
			  AND COALESCE(rl.provider_id, 0) = COALESCE($4, 0)
			  AND COALESCE(rl.credential_id, 0) = COALESCE($5, 0)
		)
		SELECT bucket,
		       COUNT(*) AS requests,
		       COUNT(*) FILTER (WHERE success = FALSE OR lower(COALESCE(request_status, '')) = 'failure') AS errors,
		       AVG(latency_ms)::float8 AS avg_latency,
		       PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY latency_ms)::float8 AS p99_latency
		FROM buckets
		GROUP BY bucket
		ORDER BY bucket ASC
	`
	// We approximate the model match on canonical_id where the row
	// has one; otherwise fall back to outbound_model / client_model.
	// canonical_id is not stored on Incident, so we pass 0 and let
	// the OR clause handle the model-name match.
	rows, err := s.pool.Query(ctx, sql, tenantID, int64(0), inc.RouteKey.Model, inc.RouteKey.ProviderID, inc.RouteKey.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("timeline query: %w", err)
	}
	defer rows.Close()
	out := []IncidentTimelinePoint{}
	for rows.Next() {
		var p IncidentTimelinePoint
		if err := rows.Scan(&p.BucketStart, &p.Requests, &p.Errors, &p.AvgLatencyMs, &p.P99LatencyMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BuildUpdate constructs the SSE envelope body for a transition
// result. It strips tenant_id, credential_id, and other sensitive
// fields; only the dimension/value pairs needed by the dashboard
// for lane aggregation remain.
func BuildUpdate(inc *Incident) IncidentUpdate {
	upd := IncidentUpdate{
		Type:           "incident_update",
		IncidentID:     inc.ID,
		State:          inc.State,
		FailureStreak:  inc.FailureStreak,
		RecoveryStreak: inc.RecoveryStreak,
		Visible:        inc.State.IsVisible(),
		RouteKey: RouteKey{
			// TenantID intentionally excluded from the wire body.
			Protocol:     inc.RouteKey.Protocol,
			Model:        inc.RouteKey.Model,
			ProviderID:   inc.RouteKey.ProviderID,
			CredentialID: inc.RouteKey.CredentialID,
		},
		UpdatedAt: inc.UpdatedAt,
	}
	// affected_lanes is computed by the SSE hub (it knows the
	// provider_code lookup) and added in admin/live_stream_sse.go.
	if inc.LastErrorKind != nil {
		upd.LastError = &SanitizedError{
			Kind:  *inc.LastErrorKind,
			Stage: SanitizeStage(derefString(inc.LastFailureStage)),
		}
	}
	return upd
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// LogTransition writes an info-level slog entry. It is split out so
// tests can swap in a quieter logger.
func LogTransition(r *TransitionResult) {
	if r == nil || r.NoOp {
		return
	}
	slog.Info("routeincident.transition",
		"incident_id", r.Update.IncidentID,
		"state", string(r.Update.State),
		"visible", r.Update.Visible,
		"failure_streak", r.Update.FailureStreak,
		"recovery_streak", r.Update.RecoveryStreak,
		"reason", r.Reason,
	)
}
