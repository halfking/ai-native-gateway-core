// Package routeincident — evidence.go
//
// Phase-2 evidence export. Produces a sanitized, integrity-checked
// bundle from a single completed DiagnosticRun + the incident +
// its events + the 24h timeline. The export is the answer to the
// spec's "Evidence Export" section:
//
//   - Generated only from a completed DiagnosticRun + allow-listed
//     DTO
//   - Includes timestamps, classifications, status codes, latency,
//     route decisions, aggregate resource counts, sanitized logs,
//     integrity checksum
//   - NEVER includes credentials, authorization headers, cookies,
//     full request/response bodies, client IP, user agent,
//     attachment paths, raw upstream errors, session titles,
//     raw slot holders
//   - Export size and retention are bounded (size below
//     MaxEvidenceExportBytes)
//   - Every export is audited (the caller writes a routing_audit_log
//     row with action='evidence_export' before returning)

package routeincident

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

var _ = uuid.Parse // keep the import even if a future refactor changes the parse path

// MaxEvidenceExportBytes bounds the canonicalized JSON payload.
// Anything larger is rejected; the export endpoint returns 413.
const MaxEvidenceExportBytes = 2 * 1024 * 1024 // 2 MiB

// MaxEvidenceEvents bounds the events slice. Larger windows must
// use the timeline instead.
const MaxEvidenceEvents = 200

// BuildEvidenceExport produces the sanitized bundle from a
// completed diagnostic run. The caller is responsible for
// authorizing the request and for writing the audit row.
//
// The function is read-only against the DB (it does NOT mutate
// state). The export itself is a derived view; the underlying
// rows stay where they are.
func (s *Store) BuildEvidenceExport(ctx context.Context, tenantID, runID, exporter string) (*EvidenceExport, error) {
	if s == nil || s.pool == nil {
		return nil, ErrNoDatabase
	}
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrInvalidInput)
	}

	run, err := s.loadRunByID(ctx, runID, tenantID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil // cross-tenant / not found
	}
	if !run.State.IsTerminal() {
		return nil, fmt.Errorf("%w: diagnostic run is not in a terminal state", ErrInvalidInput)
	}

	inc, err := s.Get(ctx, tenantID, run.IncidentID)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, nil
	}

	events, err := s.Events(ctx, tenantID, run.IncidentID, MaxEvidenceEvents)
	if err != nil {
		return nil, err
	}
	// Sanitize: drop response_preview / request_preview / any
	// field that might contain raw body fragments. The Events()
	// call already passes through SanitizeEvidence; this is a
	// belt-and-suspenders second pass.
	for i := range events {
		events[i].Evidence = SanitizeEvidence(events[i].Evidence)
	}

	timeline, err := s.Timeline24h(ctx, tenantID, run.IncidentID)
	if err != nil {
		return nil, err
	}
	if timeline == nil {
		timeline = []IncidentTimelinePoint{}
	}

	export := &EvidenceExport{
		Run:         *run,
		Incident:    *inc,
		Events:      events,
		Timeline:    timeline,
		GeneratedAt: time.Now().UTC(),
		Exporter:    exporter,
	}

	// Canonicalize before hashing. We use a sorted-keys JSON
	// encoding so the checksum is deterministic across runs.
	canonical, err := canonicalizeExport(export)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	if len(canonical) > MaxEvidenceExportBytes {
		return nil, fmt.Errorf("%w: export exceeds %d bytes", ErrInvalidInput, MaxEvidenceExportBytes)
	}
	sum := sha256.Sum256(canonical)
	export.Integrity = IntegrityChecksum{
		Algorithm: "sha256",
		Value:     hex.EncodeToString(sum[:]),
	}
	return export, nil
}

// RecordEvidenceExportAudit writes the audit row for an export
// call. The dispatcher calls this AFTER the export is generated
// (so the audit reflects a successful export).
func (s *Store) RecordEvidenceExportAudit(
	ctx context.Context,
	tenantID, incidentID, exporter, reason, confirmToken, idemKey, ipHash, runID string,
) error {
	if s == nil || s.pool == nil {
		return ErrNoDatabase
	}
	if tenantID == "" || runID == "" || idemKey == "" {
		return fmt.Errorf("%w: missing tenant_id / run_id / idempotency_key", ErrInvalidInput)
	}
	reason = sanitizeReason(reason)
	if reason == "" {
		return fmt.Errorf("%w: reason is required for evidence_export", ErrInvalidInput)
	}
	runUUID, err := uuid.Parse(runID)
	if err != nil {
		return fmt.Errorf("%w: run_id is not a uuid", ErrInvalidInput)
	}
	hash := hashToken(confirmToken)
	ipH := hashIP(ipHash)
	const sql = `
		INSERT INTO routing_audit_log (
			incident_id, tenant_id, action, actor, reason,
			confirmation_token_hash, idempotency_key,
			request_payload, pre_snapshot, post_snapshot, response_payload,
			outcome, failure_reason, diagnostic_run_id, actor_ip_hash
		) VALUES ($1, $2, 'evidence_export', $3, $4, $5, $6,
		          '{"run_id":"' || $7 || '"}'::jsonb,
		          '{}'::jsonb, '{}'::jsonb,
		          '{"run_id":"' || $7 || '"}'::jsonb,
		          'success', NULL, $7::uuid, $8)
		ON CONFLICT (idempotency_key) DO NOTHING
	`
	_, err = s.pool.Exec(ctx, sql,
		incidentID, tenantID, exporter, reason,
		hash, idemKey, runUUID.String(), ipH,
	)
	return err
}

// canonicalizeExport produces a deterministic JSON encoding. We
// sort map keys recursively so the SHA-256 is reproducible
// regardless of map iteration order.
//
// The encoding includes only fields the spec explicitly allow-
// lists. Anything outside the allow-list is dropped before
// canonicalization.
func canonicalizeExport(e *EvidenceExport) ([]byte, error) {
	type allowedExport struct {
		Schema      string            `json:"schema"`
		GeneratedAt string            `json:"generated_at"`
		Exporter    string            `json:"exporter"`
		Run         allowedRun        `json:"run"`
		Incident    allowedIncident   `json:"incident"`
		Events      []allowedEvent    `json:"events"`
		Timeline    []allowedTimeline `json:"timeline"`
	}
	out := allowedExport{
		Schema:      "route-incident-evidence/v1",
		GeneratedAt: e.GeneratedAt.UTC().Format(time.RFC3339Nano),
		Exporter:    e.Exporter,
		Run:         toAllowedRun(&e.Run),
		Incident:    toAllowedIncident(&e.Incident),
		Events:      make([]allowedEvent, 0, len(e.Events)),
		Timeline:    make([]allowedTimeline, 0, len(e.Timeline)),
	}
	for _, ev := range e.Events {
		out.Events = append(out.Events, toAllowedEvent(ev))
	}
	// Sort events deterministically by (created_at, id) — the API
	// already returns them sorted but we re-sort defensively.
	sort.SliceStable(out.Events, func(i, j int) bool {
		if out.Events[i].CreatedAt != out.Events[j].CreatedAt {
			return out.Events[i].CreatedAt < out.Events[j].CreatedAt
		}
		return out.Events[i].ID < out.Events[j].ID
	})
	for _, tp := range e.Timeline {
		out.Timeline = append(out.Timeline, toAllowedTimeline(tp))
	}
	return json.Marshal(out)
}

type allowedRun struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	State      string         `json:"state"`
	RouteKey   map[string]any `json:"route_key"`
	Parameters map[string]any `json:"parameters"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Result     map[string]any `json:"result"`
}

func toAllowedRun(r *DiagnosticRun) allowedRun {
	out := allowedRun{
		ID:         r.ID,
		Kind:       string(r.Kind),
		State:      string(r.State),
		RouteKey:   SanitizeEvidence(r.RouteKey),
		Parameters: sanitizeParameters(r.Parameters),
		StartedAt:  r.StartedAt.UTC().Format(time.RFC3339Nano),
		Result:     SanitizeEvidence(r.Result),
	}
	if r.FinishedAt != nil && !r.FinishedAt.IsZero() {
		out.FinishedAt = r.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

type allowedIncident struct {
	ID             string         `json:"id"`
	State          string         `json:"state"`
	FailureStreak  int            `json:"failure_streak"`
	RecoveryStreak int            `json:"recovery_streak"`
	FirstFailureAt string         `json:"first_failure_at"`
	LastFailureAt  string         `json:"last_failure_at,omitempty"`
	LastSuccessAt  string         `json:"last_success_at,omitempty"`
	RecoveredAt    string         `json:"recovered_at,omitempty"`
	TotalFailures  int64          `json:"total_failures"`
	TotalSuccesses int64          `json:"total_successes"`
	RouteKey       map[string]any `json:"route_key"`
}

func toAllowedIncident(inc *Incident) allowedIncident {
	out := allowedIncident{
		ID:             inc.ID,
		State:          string(inc.State),
		FailureStreak:  inc.FailureStreak,
		RecoveryStreak: inc.RecoveryStreak,
		FirstFailureAt: inc.FirstFailureAt.UTC().Format(time.RFC3339Nano),
		TotalFailures:  inc.TotalFailures,
		TotalSuccesses: inc.TotalSuccesses,
		RouteKey: map[string]any{
			"endpoint_protocol": inc.RouteKey.Protocol,
			"model":             inc.RouteKey.Model,
			"provider_id":       inc.RouteKey.ProviderID,
			"credential_id":     inc.RouteKey.CredentialID,
			// tenant_id intentionally omitted from the public
			// export payload; the exporter's tenant is captured
			// at the audit layer instead.
		},
	}
	if inc.LastFailureAt != nil && !inc.LastFailureAt.IsZero() {
		out.LastFailureAt = inc.LastFailureAt.UTC().Format(time.RFC3339Nano)
	}
	if inc.LastSuccessAt != nil && !inc.LastSuccessAt.IsZero() {
		out.LastSuccessAt = inc.LastSuccessAt.UTC().Format(time.RFC3339Nano)
	}
	if inc.RecoveredAt != nil && !inc.RecoveredAt.IsZero() {
		out.RecoveredAt = inc.RecoveredAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

type allowedEvent struct {
	ID             int64          `json:"id"`
	EventType      string         `json:"event_type"`
	RequestID      string         `json:"request_id,omitempty"`
	TerminalStatus string         `json:"terminal_status,omitempty"`
	FailureKind    string         `json:"failure_kind,omitempty"`
	FailureStage   string         `json:"failure_stage,omitempty"`
	FailureStreak  int            `json:"failure_streak,omitempty"`
	RecoveryStreak int            `json:"recovery_streak,omitempty"`
	Evidence       map[string]any `json:"evidence,omitempty"`
	CreatedAt      string         `json:"created_at"`
}

func toAllowedEvent(e IncidentEvent) allowedEvent {
	out := allowedEvent{
		ID:        e.ID,
		EventType: string(e.EventType),
		Evidence:  SanitizeEvidence(e.Evidence),
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if e.RequestID != nil {
		out.RequestID = *e.RequestID
	}
	if e.TerminalStatus != nil {
		out.TerminalStatus = *e.TerminalStatus
	}
	if e.FailureKind != nil {
		out.FailureKind = *e.FailureKind
	}
	if e.FailureStage != nil {
		out.FailureStage = *e.FailureStage
	}
	if e.FailureStreak != nil {
		out.FailureStreak = *e.FailureStreak
	}
	if e.RecoveryStreak != nil {
		out.RecoveryStreak = *e.RecoveryStreak
	}
	return out
}

type allowedTimeline struct {
	BucketStart  string   `json:"bucket_start"`
	Requests     int64    `json:"requests"`
	Errors       int64    `json:"errors"`
	AvgLatencyMs *float64 `json:"avg_latency_ms,omitempty"`
	P99LatencyMs *float64 `json:"p99_latency_ms,omitempty"`
	Recoveries   int64    `json:"recoveries"`
}

func toAllowedTimeline(t IncidentTimelinePoint) allowedTimeline {
	return allowedTimeline{
		BucketStart:  t.BucketStart.UTC().Format(time.RFC3339Nano),
		Requests:     t.Requests,
		Errors:       t.Errors,
		AvgLatencyMs: t.AvgLatencyMs,
		P99LatencyMs: t.P99LatencyMs,
		Recoveries:   t.Recoveries,
	}
}

// uuidParseString is a small wrapper kept for symmetry with the
// package's other "no-arbitrary-input" helpers. Internally it
// just calls uuid.Parse.
func uuidParseString(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// suppress unused warning if the wrapper above is removed in a
// future refactor.
var _ = uuidParseString
