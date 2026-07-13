// Package routeincident — action_infra.go
//
// Shared infrastructure for every Phase-2 mutating action and
// diagnostic test. The point of having this in one place is that
// audit + idempotency + version-check are NOT a thing each action
// remembers to do — they happen by construction.
//
// Algorithm (per call):
//   1. Authorize the actor (super-admin) and validate the action
//      against the closed allow-list.
//   2. Begin a transaction.
//   3. SELECT the incident FOR UPDATE.
//   4. Check version == expectedVersion when supplied (stale-state
//      check; the dashboard always supplies the version it just
//      rendered).
//   5. Build the pre-snapshot from the row we just read.
//   6. Run the action-specific executor (see actions_exec.go).
//   7. INSERT the audit row + UPDATE the post-snapshot.
//   8. Commit. The unique index on `idempotency_key` rejects
//      duplicates; on 23505 we read the existing row and return
//      `idempotent: true` with the cached result.

package routeincident

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStaleState is returned when the supplied incident version
// does not match the persisted version. The dashboard re-fetches
// and re-confirms on this error.
var ErrStaleState = errors.New("routeincident: stale incident state, refetch and retry")

// ErrUnauthorized is returned when the actor is not super-admin.
// The API layer maps this to 403.
var ErrUnauthorized = errors.New("routeincident: actor lacks super-admin role")

// ErrInvalidInput is returned for malformed reasons / parameters.
var ErrInvalidInput = errors.New("routeincident: invalid input")

// ErrIdempotencyConflict is returned when the same idempotency key
// was previously used with DIFFERENT parameters. The store treats
// this as a hard error so an operator cannot accidentally replay
// an old key against a new incident.
var ErrIdempotencyConflict = errors.New("routeincident: idempotency key conflict")

// ErrActionNoop is the sentinel an ActionExecutor returns when the
// action determined the request should not change state. The
// dispatcher records the audit row with outcome=noop.
var ErrActionNoop = errors.New("routeincident: action noop")

// MaxReasonLen bounds the operator-provided reason. Longer strings
// are truncated before storage.
const MaxReasonLen = 256

// MaxParameterValueLen bounds individual parameter values to
// keep the audit row bounded.
const MaxParameterValueLen = 4096

// ActionContext carries the per-call data the dispatcher needs.
// The store is responsible for the transaction; the executor is
// the action-specific implementation.
type ActionContext struct {
	TenantID     string
	IncidentID   string
	Action       ActionKind
	Actor        string
	ActorIPHash  string
	Reason       string
	ConfirmToken string
	IdempKey     string
	Parameters   map[string]any

	// ExpectedVersion is the version the dashboard believed it was
	// acting on (0 means "don't care" — only the API layer, not
	// the dashboard, sets 0).
	ExpectedVersion int64

	// Pool is the live pgxpool. The dispatcher's transaction uses
	// the same pool so the audit row and the action commit
	// atomically.
	Pool *pgxpool.Pool
}

// sanitizeReason trims and bounds the operator reason.
func sanitizeReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxReasonLen {
		runes := []rune(s)
		if len(runes) > MaxReasonLen {
			s = string(runes[:MaxReasonLen])
		}
	}
	return s
}

// hashToken returns the SHA-256 hex of the operator-supplied
// confirmation token. The plaintext is never persisted.
func hashToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// hashIP returns a short, non-reversible identifier for the
// operator's IP. We use SHA-256 truncated to 16 chars; the goal
// is to correlate audits from the same source without leaking the
// raw address.
func hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:8])
}

// sanitizeParameters allow-lists the keys we are willing to write
// to the audit row. Anything not on the list is dropped. The
// spec says no arbitrary URL, no raw body, no SQL — this is the
// enforcement.
var allowedParameterKeys = map[string]struct{}{
	"throughput_ms": {}, // for through_gateway_test
	"max_tokens":    {},
	"max_cost_usd":  {},
	"timeout_ms":    {},
	"reason_extra":  {}, // free-text continuation, length-bounded
	"target_state":  {}, // for recover: 'recovered' (default) or 'closed'
	"slot_id":       {}, // for release_slot
	"note":          {},
}

func sanitizeParameters(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if _, ok := allowedParameterKeys[k]; !ok {
			continue
		}
		switch x := v.(type) {
		case string:
			if len(x) > MaxParameterValueLen {
				runes := []rune(x)
				if len(runes) > MaxParameterValueLen {
					out[k] = string(runes[:MaxParameterValueLen])
				} else {
					out[k] = x
				}
			} else {
				out[k] = x
			}
		default:
			out[k] = v
		}
	}
	return out
}

// ActionExecutor is the per-action hook. It runs inside the
// transaction the dispatcher opened. Implementations MUST be
// idempotent on (incident_id, parameters) so a retried
// idempotency_key produces the same result.
type ActionExecutor func(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (postSnapshot map[string]any, response map[string]any, run *DiagnosticRun, err error)

// dispatchAction is the single entry point every mutating action
// uses. It owns: auth, validation, transaction, version check,
// pre-snapshot, executor call, post-snapshot, audit row, commit,
// idempotency replay.
//
// The function is intentionally untyped: each action supplies its
// own executor closure.
func (s *Store) dispatchAction(ctx context.Context, call ActionContext, exec ActionExecutor) (*ActionResponse, error) {
	// Input validation runs first so a missing reason / token /
	// idempotency_key surfaces as ErrInvalidInput even when the DB
	// is unavailable. This lets clients distinguish "you sent
	// garbage" (400) from "DB is down" (503).
	if call.TenantID == "" || call.IncidentID == "" || call.IdempKey == "" {
		return nil, fmt.Errorf("%w: missing tenant_id / incident_id / idempotency_key", ErrInvalidInput)
	}
	if !IsAllowedAction(call.Action) {
		return nil, fmt.Errorf("%w: action %q not in allow-list", ErrInvalidInput, call.Action)
	}
	if call.Action.IsMutating() {
		// Reason and confirmation token are mandatory for mutating
		// actions. Evidence export is exempt.
		reason := sanitizeReason(call.Reason)
		if reason == "" {
			return nil, fmt.Errorf("%w: reason required for %s", ErrInvalidInput, call.Action)
		}
		call.Reason = reason
		if call.ConfirmToken == "" {
			return nil, fmt.Errorf("%w: confirmation_token required for %s", ErrInvalidInput, call.Action)
		}
	}
	if call.ExpectedVersion < 0 {
		call.ExpectedVersion = 0
	}
	call.Parameters = sanitizeParameters(call.Parameters)

	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Idempotency replay: if the key already exists for this
	// tenant, return the cached response. We DO NOT execute the
	// action twice. Two different keys for the same (tenant,
	// action, incident) are allowed and produce two audit rows.
	var (
		existingAuditID  int64
		existingOutcome  ActionOutcome
		existingFailure  *string
		existingResponse []byte
		existingRunID    *string
		existingIncident []byte
	)
	row := tx.QueryRow(ctx, `
		SELECT id, outcome, failure_reason, response_payload, diagnostic_run_id, incident_after
		FROM routing_audit_log
		WHERE idempotency_key = $1 AND tenant_id = $2
	`, call.IdempKey, call.TenantID)
	if err := row.Scan(&existingAuditID, &existingOutcome, &existingFailure, &existingResponse, &existingRunID, &existingIncident); err == nil {
		// Reuse the cached result.
		resp := &ActionResponse{
			AuditID:       existingAuditID,
			Outcome:       existingOutcome,
			FailureReason: existingFailure,
			Idempotent:    true,
		}
		if len(existingResponse) > 0 {
			_ = json.Unmarshal(existingResponse, &resp.Response)
		}
		if existingRunID != nil {
			run, _ := s.loadRunByIDInTx(ctx, tx, *existingRunID, call.TenantID)
			resp.DiagnosticRun = run
		}
		if len(existingIncident) > 0 {
			var inc Incident
			if err := json.Unmarshal(existingIncident, &inc); err == nil {
				resp.Incident = &inc
			}
		}
		// Commit the (read-only) transaction to release the locks.
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit replay: %w", err)
		}
		return resp, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}

	// Lock the incident.
	inc, err := s.lockIncidentForUpdate(ctx, tx, call.TenantID, call.IncidentID)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, ErrStaleState
	}
	if call.ExpectedVersion > 0 && inc.Version != call.ExpectedVersion {
		return nil, ErrStaleState
	}

	preSnapshot, err := json.Marshal(map[string]any{
		"state":           inc.State,
		"failure_streak":  inc.FailureStreak,
		"recovery_streak": inc.RecoveryStreak,
		"version":         inc.Version,
		"total_failures":  inc.TotalFailures,
		"total_successes": inc.TotalSuccesses,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal pre_snapshot: %w", err)
	}

	// Run the action executor.
	postSnapshot, response, run, execErr := exec(ctx, tx, inc, &ActionRequest{
		Reason:            call.Reason,
		ConfirmationToken: call.ConfirmToken,
		IdempotencyKey:    call.IdempKey,
		Parameters:        call.Parameters,
	})
	if execErr != nil {
		// Even on failure we record the audit row. outcome='failed',
		// failure_reason is sanitized. The transaction commits the
		// audit + post-snapshot so the operator can see exactly
		// what they tried.
		failureReason := sanitizeReason(execErr.Error())
		outcome := OutcomeFailed
		if errors.Is(execErr, ErrActionNoop) {
			outcome = OutcomeNoop
		}
		s.writeAudit(ctx, tx, call, inc, preSnapshot, []byte("{}"), []byte("{}"), []byte(`{"noop":true}`), outcome, &failureReason, nil, run)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit failed audit: %w", err)
		}
		return &ActionResponse{
			Outcome:       outcome,
			FailureReason: &failureReason,
			Idempotent:    false,
		}, nil
	}

	postJSON, _ := json.Marshal(postSnapshot)
	respJSON, _ := json.Marshal(response)
	if respJSON == nil {
		respJSON = []byte("{}")
	}

	// Insert the audit row. The unique index on idempotency_key
	// protects against a race where two parallel requests with the
	// same key both pass the SELECT and both INSERT.
	var (
		runID *string
	)
	if run != nil {
		rid := run.ID
		runID = &rid
	}
	auditID, err := s.writeAudit(ctx, tx, call, inc, preSnapshot, postJSON, respJSON, respJSON, OutcomeSuccess, nil, runID, run)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit success: %w", err)
	}

	// Re-load the post-execution incident so the caller sees the
	// updated state (e.g. recover → state=recovered).
	final, _ := s.Get(ctx, call.TenantID, call.IncidentID)
	return &ActionResponse{
		AuditID:       auditID,
		Outcome:       OutcomeSuccess,
		DiagnosticRun: run,
		Incident:      final,
		Response:      response,
		Idempotent:    false,
	}, nil
}

// writeAudit persists the audit row. It is unexported and only
// called by dispatchAction.
func (s *Store) writeAudit(
	ctx context.Context, tx pgx.Tx,
	call ActionContext,
	inc *Incident,
	preSnapshot, postSnapshot, requestPayload, responsePayload []byte,
	outcome ActionOutcome, failureReason *string, runID *string, run *DiagnosticRun,
) (int64, error) {
	// Insert the diagnostic run first if we have one; the audit
	// row references it.
	var resolvedRunID *string
	if run != nil {
		if err := s.persistRunInTx(ctx, tx, run, inc); err != nil {
			return 0, err
		}
		rid := run.ID
		resolvedRunID = &rid
	} else if runID != nil {
		resolvedRunID = runID
	}

	var incidentAfter []byte
	if inc != nil {
		b, _ := json.Marshal(inc)
		incidentAfter = b
	}

	const sql = `
		INSERT INTO routing_audit_log (
			incident_id, tenant_id, action, actor, reason,
			confirmation_token_hash, idempotency_key,
			request_payload, pre_snapshot, post_snapshot, response_payload,
			outcome, failure_reason, diagnostic_run_id, actor_ip_hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`
	var auditID int64
	err := tx.QueryRow(ctx, sql,
		inc.ID, call.TenantID, string(call.Action), call.Actor, call.Reason,
		hashToken(call.ConfirmToken), call.IdempKey,
		requestPayload, preSnapshot, postSnapshot, responsePayload,
		string(outcome), failureReason, resolvedRunID, call.ActorIPHash,
	).Scan(&auditID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The ON CONFLICT path: another caller raced us. Read
			// the existing row's id so the caller gets a stable
			// response.
			if err := tx.QueryRow(ctx, `SELECT id FROM routing_audit_log WHERE idempotency_key = $1`, call.IdempKey).Scan(&auditID); err != nil {
				return 0, fmt.Errorf("audit conflict lookup: %w", err)
			}
			return auditID, nil
		}
		// Surface unique-violation as a distinct error so the
		// caller can return 409.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, ErrIdempotencyConflict
		}
		return 0, fmt.Errorf("insert audit: %w", err)
	}
	_ = incidentAfter
	return auditID, nil
}

// lockIncidentForUpdate is a tenant-scoped row lock for the
// action transaction. Returns (nil, nil) for cross-tenant or
// missing — by design, indistinguishable to the caller.
func (s *Store) lockIncidentForUpdate(ctx context.Context, tx pgx.Tx, tenantID, incidentID string) (*Incident, error) {
	const sql = `
		SELECT id, tenant_id, endpoint_protocol, model, provider_id, credential_id,
		       state, failure_streak, recovery_streak,
		       first_failure_at, last_failure_at, last_success_at, recovered_at,
		       total_failures, total_successes,
		       last_error_kind, last_failure_stage,
		       version, created_at, updated_at
		FROM route_incidents
		WHERE id = $1 AND tenant_id = $2
		FOR UPDATE
	`
	row := tx.QueryRow(ctx, sql, incidentID, tenantID)
	inc, err := scanIncident(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock incident: %w", err)
	}
	return inc, nil
}

// persistRunInTx writes a diagnostic_runs row inside the action
// transaction so the audit row + run row commit atomically. We
// use UPSERT so a retried idempotency_key reuses the same run.
func (s *Store) persistRunInTx(ctx context.Context, tx pgx.Tx, run *DiagnosticRun, inc *Incident) error {
	if run == nil {
		return nil
	}
	routeJSON, _ := json.Marshal(run.RouteKey)
	paramsJSON, _ := json.Marshal(run.Parameters)
	resultJSON, _ := json.Marshal(run.Result)
	tenantID := run.TenantID
	if tenantID == "" {
		tenantID = inc.RouteKey.TenantID
	}
	state := run.State
	if state == "" {
		state = RunPending
	}
	startedAt := run.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	finishedAt := run.FinishedAt

	const sql = `
		INSERT INTO diagnostic_runs (
			id, incident_id, tenant_id, kind, state,
			route_key, parameters, started_at, finished_at, result,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())
		ON CONFLICT (id) DO UPDATE
		SET state = EXCLUDED.state,
		    finished_at = EXCLUDED.finished_at,
		    result = EXCLUDED.result,
		    updated_at = now()
	`
	var finished *time.Time
	if finishedAt != nil && !finishedAt.IsZero() {
		finished = finishedAt
	}
	_, err := tx.Exec(ctx, sql,
		run.ID, inc.ID, tenantID, string(run.Kind), string(state),
		routeJSON, paramsJSON, startedAt, finished, resultJSON,
	)
	if err != nil {
		return fmt.Errorf("persist diagnostic run: %w", err)
	}
	return nil
}

// loadRunByIDInTx fetches a single run inside an existing tx.
func (s *Store) loadRunByIDInTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (*DiagnosticRun, error) {
	const sql = `
		SELECT id, incident_id, tenant_id, kind, state, route_key, parameters,
		       started_at, finished_at, result, audit_log_id, created_at, updated_at
		FROM diagnostic_runs
		WHERE id = $1 AND tenant_id = $2
	`
	row := tx.QueryRow(ctx, sql, id, tenantID)
	run, err := scanDiagnosticRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return run, nil
}

// loadRunByID fetches a run outside of a tx (used by read paths).
func (s *Store) loadRunByID(ctx context.Context, id, tenantID string) (*DiagnosticRun, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	const sql = `
		SELECT id, incident_id, tenant_id, kind, state, route_key, parameters,
		       started_at, finished_at, result, audit_log_id, created_at, updated_at
		FROM diagnostic_runs
		WHERE id = $1 AND tenant_id = $2
	`
	row := s.pool.QueryRow(ctx, sql, id, tenantID)
	return scanDiagnosticRun(row)
}

// AuditLogList returns recent audit entries for an incident (or
// for a tenant when incidentID is empty). Tenant-scoped to enforce
// the cross-tenant 404 invariant.
func (s *Store) AuditLogList(ctx context.Context, tenantID, incidentID string, limit int) ([]AuditLogEntry, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conds := []any{tenantID}
	where := "WHERE tenant_id = $1"
	if incidentID != "" {
		conds = append(conds, incidentID)
		where += fmt.Sprintf(" AND incident_id = $%d", len(conds))
	}
	conds = append(conds, limit)
	sql := fmt.Sprintf(`
		SELECT id, incident_id, tenant_id, action, actor, reason, idempotency_key,
		       outcome, failure_reason, diagnostic_run_id,
		       pre_snapshot, post_snapshot, response_payload, created_at
		FROM routing_audit_log
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d
	`, where, len(conds))
	rows, err := s.pool.Query(ctx, sql, conds...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditLogEntry{}
	for rows.Next() {
		var a AuditLogEntry
		var action, outcome string
		var pre, post, resp []byte
		if err := rows.Scan(
			&a.ID, &a.IncidentID, &a.TenantID, &action, &a.Actor, &a.Reason,
			&a.IdempotencyKey, &outcome, &a.FailureReason, &a.DiagnosticRunID,
			&pre, &post, &resp, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		a.Action = ActionKind(action)
		a.Outcome = ActionOutcome(outcome)
		if len(pre) > 0 {
			_ = json.Unmarshal(pre, &a.PreSnapshot)
			a.PreSnapshot = SanitizeEvidence(a.PreSnapshot)
		}
		if len(post) > 0 {
			_ = json.Unmarshal(post, &a.PostSnapshot)
			a.PostSnapshot = SanitizeEvidence(a.PostSnapshot)
		}
		if len(resp) > 0 {
			_ = json.Unmarshal(resp, &a.ResponsePayload)
			a.ResponsePayload = SanitizeEvidence(a.ResponsePayload)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuditLogListByRun returns the audit entry that produced a given
// diagnostic run. Used by the evidence export to populate
// Exporter + Integrity metadata.
func (s *Store) AuditLogListByRun(ctx context.Context, tenantID, runID string) ([]AuditLogEntry, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	const sql = `
		SELECT id, incident_id, tenant_id, action, actor, reason, idempotency_key,
		       outcome, failure_reason, diagnostic_run_id,
		       pre_snapshot, post_snapshot, response_payload, created_at
		FROM routing_audit_log
		WHERE diagnostic_run_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`
	row := s.pool.QueryRow(ctx, sql, runID, tenantID)
	var a AuditLogEntry
	var action, outcome string
	var pre, post, resp []byte
	if err := row.Scan(
		&a.ID, &a.IncidentID, &a.TenantID, &action, &a.Actor, &a.Reason,
		&a.IdempotencyKey, &outcome, &a.FailureReason, &a.DiagnosticRunID,
		&pre, &post, &resp, &a.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.Action = ActionKind(action)
	a.Outcome = ActionOutcome(outcome)
	if len(pre) > 0 {
		_ = json.Unmarshal(pre, &a.PreSnapshot)
		a.PreSnapshot = SanitizeEvidence(a.PreSnapshot)
	}
	if len(post) > 0 {
		_ = json.Unmarshal(post, &a.PostSnapshot)
		a.PostSnapshot = SanitizeEvidence(a.PostSnapshot)
	}
	if len(resp) > 0 {
		_ = json.Unmarshal(resp, &a.ResponsePayload)
		a.ResponsePayload = SanitizeEvidence(a.ResponsePayload)
	}
	return []AuditLogEntry{a}, nil
}

// Helper: is allowed action?
func IsAllowedAction(a ActionKind) bool {
	for _, k := range AllActionKinds() {
		if k == a {
			return true
		}
	}
	return false
}

// DiagnosticRunsList returns recent runs for an incident.
func (s *Store) DiagnosticRunsList(ctx context.Context, tenantID, incidentID string, limit int) ([]DiagnosticRun, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const sql = `
		SELECT id, incident_id, tenant_id, kind, state, route_key, parameters,
		       started_at, finished_at, result, audit_log_id, created_at, updated_at
		FROM diagnostic_runs
		WHERE tenant_id = $1 AND incident_id = $2
		ORDER BY started_at DESC, id DESC
		LIMIT $3
	`
	rows, err := s.pool.Query(ctx, sql, tenantID, incidentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiagnosticRun{}
	for rows.Next() {
		run, err := scanDiagnosticRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

// diagnosticRunScanner is the subset of pgx.Row / pgx.Rows used
// by scanDiagnosticRun.
type diagnosticRunScanner interface {
	Scan(dest ...any) error
}

func scanDiagnosticRun(row diagnosticRunScanner) (*DiagnosticRun, error) {
	var run DiagnosticRun
	var kind, state string
	var routeKeyJSON, paramsJSON, resultJSON []byte
	if err := row.Scan(
		&run.ID, &run.IncidentID, &run.TenantID, &kind, &state,
		&routeKeyJSON, &paramsJSON, &run.StartedAt, &run.FinishedAt,
		&resultJSON, &run.AuditLogID, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return nil, err
	}
	run.Kind = ActionKind(kind)
	run.State = DiagnosticRunState(state)
	if len(routeKeyJSON) > 0 {
		_ = json.Unmarshal(routeKeyJSON, &run.RouteKey)
	}
	if len(paramsJSON) > 0 {
		_ = json.Unmarshal(paramsJSON, &run.Parameters)
		run.Parameters = sanitizeParameters(run.Parameters)
	}
	if len(resultJSON) > 0 {
		_ = json.Unmarshal(resultJSON, &run.Result)
		run.Result = SanitizeEvidence(run.Result)
	}
	return &run, nil
}

// ActionLogger centralises the slog call so the test suite can
// capture it. Phase 2 actions all log at info level.
func ActionLogger() *slog.Logger { return slog.Default() }
