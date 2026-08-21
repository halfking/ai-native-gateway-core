//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// TestApprovalFlow_E2E walks the full approval → resume pipeline against a
// real PostgreSQL database (TEST_DATABASE_URL), with the LLM caller, client
// responder, and pending-response writer replaced by in-memory stubs.
//
// Pipeline under test:
//  1. A pending row exists in approval_queue (with a serialised RequestSnapshot).
//  2. Admin calls ApprovalManager.Approve — DB row flips to approved (RLS-bypassed
//     because expectedTenantID matches the row's tenant_id).
//  3. ApprovalResumeHandler.ResumeAfterApproval fires:
//     a. record loaded with snapshot JSON rehydrated
//     b. stub LLMCaller invoked exactly once with the snapshot's payload
//     c. stub PendingWriter receives one "completed" entry
//  4. Re-running ResumeAfterApproval on the same row is a no-op (terminal status).
//  5. A separate rejected row exercises the rejection branch and the
//     ClientResponder.RespondRejection path.
//
// Run with:
//
//	export LLM_GATEWAY_PG_URL='postgres://postgres:postgres@127.0.0.1:5432/llm_gateway_test?sslmode=disable'
//	go test -tags=integration ./tests/integration -v -run TestApprovalFlow_E2E
//
// This E2E needs the postgres superuser because the test exercises both
// pending → approved/rejected transitions and the RLS policy on approval_queue.
// The low-privilege gateway_rls_test account intentionally has no INSERT/SELECT
// on approval_queue (the policy is fail-closed for unprivileged callers).
func TestApprovalFlow_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping approval E2E in short mode")
	}
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	const (
		tenantA = "test-tenant-approval-a"
		tenantB = "test-tenant-approval-b"
	)
	// Cleanup is scoped by tenant_id prefix so it catches every row we
	// inserted during this test run, regardless of which sub-test it landed
	// in. Using WHERE id = ANY($1) with []uuid.UUID hit a pgx/v5 array-type
	// encoding path that silently no-op'd on this image; the prefix path
	// keeps cleanup deterministic and survives the test running multiple
	// sub-tests in any order.
	cleanup := func(tenant string) {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_, _ = pool.Exec(cleanCtx,
			`DELETE FROM approval_queue WHERE tenant_id = $1`, tenant)
	}

	approvalMgr := sessionaudit.NewApprovalManager(pool, 5*time.Minute)

	t.Run("ApprovedHappyPath", func(t *testing.T) {
		approvedID := uuid.New()
		snapshot := sampleSnapshot(tenantA, "sess-approved", "req-approved")
		insertPendingRow(t, ctx, pool, approvedID, tenantA, snapshot)

		// 1. Approve via real RLS-aware path.
		if err := approvalMgr.Approve(ctx, approvedID.String(), tenantA, "admin@e2e", "looks safe"); err != nil {
			t.Fatalf("Approve failed: %v", err)
		}

		// DB must reflect approved status.
		gotStatus := readStatus(t, ctx, pool, approvedID)
		if gotStatus != string(sessionaudit.ApprovalApproved) {
			t.Fatalf("expected status=approved, got %s", gotStatus)
		}

		// 2. Resume with stub collaborators. The stub LLMCaller mirrors the
		// real createLLMCaller wiring: after a successful ChatHandler call it
		// writes a "completed" pending.Response so the client can fetch the
		// LLM output via the polling endpoint. ApprovalResumeHandler itself
		// only writes the "in_progress" heartbeat; the completed entry must
		// come from the LLMCaller adapter.
		pending := &capturingPendingWriter{}
		llm := &capturingLLMCaller{pending: pending}
		responder := &capturingResponder{}
		handler := session.NewApprovalResumeHandler(nil, approvalMgr, llm, responder, pending)

		if err := handler.ResumeAfterApproval(ctx, approvedID.String(), tenantA); err != nil {
			t.Fatalf("ResumeAfterApproval failed: %v", err)
		}

		if llm.calls != 1 {
			t.Fatalf("expected LLMCaller to fire once, got %d", llm.calls)
		}
		if got := string(llm.lastSnapshot.BodyBytes); got != string(snapshot.BodyBytes) {
			t.Fatalf("snapshot body mismatch: got %q, want %q", got, snapshot.BodyBytes)
		}
		if llm.lastSnapshot.SessionID != snapshot.SessionID || llm.lastSnapshot.TenantID != tenantA {
			t.Fatalf("snapshot identity mismatch: %+v", llm.lastSnapshot)
		}
		if pending.entries == 0 {
			t.Fatalf("expected PendingWriter to receive at least one entry, got 0")
		}
		var completed int
		for _, e := range pending.all {
			if e.Status == "completed" {
				completed++
			}
		}
		if completed < 1 {
			t.Fatalf("expected at least one completed pending entry, got %d (statuses=%v)",
				completed, pending.statuses())
		}
		if responder.rejections != 0 {
			t.Fatalf("rejection path should not fire on approved row, got %d calls", responder.rejections)
		}

		// 3. Repeat-resume is an idempotent no-op after the successful claim
		// has been completed. A crashed process may still be retried after a
		// lease expires (at-least-once recovery), but a live completed row
		// must never invoke the external LLM again.
		if err := handler.ResumeAfterApproval(ctx, approvedID.String(), tenantA); err != nil {
			t.Fatalf("second ResumeAfterApproval failed: %v", err)
		}
		if llm.calls != 1 {
			t.Fatalf("expected completed resume to remain idempotent, got %d calls", llm.calls)
		}

		cleanup(tenantA)
	})

	t.Run("RejectedBranch", func(t *testing.T) {
		rejectedID := uuid.New()
		snapshot := sampleSnapshot(tenantB, "sess-rejected", "req-rejected")
		insertPendingRow(t, ctx, pool, rejectedID, tenantB, snapshot)

		if err := approvalMgr.Reject(ctx, rejectedID.String(), tenantB, "admin@e2e", "contains PII"); err != nil {
			t.Fatalf("Reject failed: %v", err)
		}

		gotStatus := readStatus(t, ctx, pool, rejectedID)
		if gotStatus != string(sessionaudit.ApprovalRejected) {
			t.Fatalf("expected status=rejected, got %s", gotStatus)
		}

		llm := &capturingLLMCaller{}
		responder := &capturingResponder{}
		pending := &capturingPendingWriter{}
		handler := session.NewApprovalResumeHandler(nil, approvalMgr, llm, responder, pending)

		if err := handler.ResumeAfterApproval(ctx, rejectedID.String(), tenantB); err != nil {
			t.Fatalf("ResumeAfterApproval on rejected row failed: %v", err)
		}

		if llm.calls != 0 {
			t.Fatalf("LLMCaller must NOT fire on rejected row, got %d calls", llm.calls)
		}
		if responder.rejections != 1 {
			t.Fatalf("expected ClientResponder.RespondRejection to fire once, got %d", responder.rejections)
		}
		if responder.lastReason == "" {
			t.Fatalf("rejection reason should be propagated to responder")
		}

		cleanup(tenantB)
	})

	t.Run("CrossTenantRejectedByManager", func(t *testing.T) {
		// A row owned by tenantB must NOT be reachable by an admin pretending
		// to be tenantA — this guards against horizontal privilege escalation.
		foreignID := uuid.New()
		insertPendingRow(t, ctx, pool, foreignID, tenantB, sampleSnapshot(tenantB, "sess-foreign", "req-foreign"))

		err := approvalMgr.Approve(ctx, foreignID.String(), tenantA, "admin@e2e", "")
		if !errors.Is(err, sessionaudit.ErrTenantMismatch) {
			t.Fatalf("expected ErrTenantMismatch, got %v", err)
		}

		// DB row must remain pending.
		if got := readStatus(t, ctx, pool, foreignID); got != string(sessionaudit.ApprovalPending) {
			t.Fatalf("expected status to remain pending, got %s", got)
		}

		cleanup(tenantB)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func sampleSnapshot(tenantID, sessionID, requestID string) *sessionaudit.RequestSnapshot {
	return &sessionaudit.RequestSnapshot{
		SessionID:   sessionID,
		TenantID:    tenantID,
		RequestID:   requestID,
		ClientModel: "gpt-4o-mini",
		ClientInfo: sessionaudit.ClientInfo{
			IP:        "127.0.0.1",
			UserAgent: "approval-e2e/1.0",
			Model:     "gpt-4o-mini",
		},
		DetectResult: &sessionaudit.DetectResult{
			Score:          8,
			SensitiveWords: []string{"ssn"},
			Decision:       sessionaudit.DecisionNeedApproval,
			Reason:         "Contains PII — needs human review",
			LatencyMs:      12,
		},
		BodyBytes: []byte(fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello-%s"}]}`, requestID)),
		CreatedAt: time.Now(),
	}
}

func insertPendingRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, tenantID string, snap *sessionaudit.RequestSnapshot) {
	t.Helper()
	detectJSON, err := json.Marshal(snap.DetectResult)
	if err != nil {
		t.Fatalf("marshal detect_result: %v", err)
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	// pgx treats []byte as bytea; jsonb columns reject bytea payloads with
	// SQLSTATE 22P02. Pass the JSON as a string and let the column cast do
	// the jsonb conversion.
	_, err = pool.Exec(ctx, `
		INSERT INTO approval_queue (
			id, session_id, tenant_id, request_id,
			detect_result, snapshot, status,
			created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5::text::jsonb, $6::text::jsonb, 'pending', now(), now() + interval '15 minutes')
	`, id, snap.SessionID, tenantID, snap.RequestID, string(detectJSON), string(snapJSON))
	if err != nil {
		t.Fatalf("insert pending row: %v", err)
	}
}

func readStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var status string
	err := pool.QueryRow(ctx, `SELECT status FROM approval_queue WHERE id = $1`, id).Scan(&status)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// ──────────────────────────────────────────────────────────────────────────────
// Stub collaborators (in-memory; no Redis / no LLM upstream)
// ──────────────────────────────────────────────────────────────────────────────

type capturingLLMCaller struct {
	mu           sync.Mutex
	calls        int
	lastSnapshot *sessionaudit.RequestSnapshot

	// optional: when wired, mirrors the real createLLMCaller behaviour and
	// writes a "completed" pending entry on success.
	pending *capturingPendingWriter
}

func (c *capturingLLMCaller) CallFromSnapshot(ctx context.Context, snap *sessionaudit.RequestSnapshot, execution session.ResumeExecution) error {
	c.mu.Lock()
	c.calls++
	c.lastSnapshot = snap
	c.mu.Unlock()

	if c.pending != nil && snap != nil {
		_ = c.pending.Save(ctx, &session.PendingResumeEntry{
			SessionID:     snap.SessionID,
			TenantID:      snap.TenantID,
			RequestID:     snap.RequestID,
			Status:        "completed",
			Body:          `{"choices":[{"message":{"content":"stub-e2e-response"}}]}`,
			ContentType:   "application/json",
			CompletedAt:   time.Now().Unix(),
			TaskID:        execution.ApprovalID,
			FencingToken:  execution.FencingToken,
			ResultVersion: execution.FencingToken,
		})
	}
	return nil
}

type capturingResponder struct {
	mu         sync.Mutex
	rejections int
	lastReason string
	responses  int
}

func (r *capturingResponder) Respond(_ context.Context, _ *sessionaudit.RequestSnapshot, _ any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses++
	return nil
}

func (r *capturingResponder) RespondRejection(_ context.Context, _ *sessionaudit.RequestSnapshot, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejections++
	r.lastReason = reason
	return nil
}

type capturingPendingWriter struct {
	mu      sync.Mutex
	entries int
	all     []*session.PendingResumeEntry
}

func (p *capturingPendingWriter) Save(_ context.Context, e *session.PendingResumeEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries++
	p.all = append(p.all, e)
	return nil
}

func (p *capturingPendingWriter) statuses() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.all))
	for _, e := range p.all {
		out = append(out, e.Status)
	}
	return out
}
