package sessionv2mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// --- fakes -------------------------------------------------------------

// recordingDB captures Exec calls so tests can assert the state-machine
// statements the reaper issues (delete / requeue / dead). Begin returns a
// recording tx so the R29 RLS-bypass wrappers (execBypass / setBypassGUCs)
// run unchanged against the fake; GUC lift statements land in the same
// recording and the Contains-style assertions ignore them.
type recordingDB struct {
	mu    sync.Mutex
	calls []string // rendered SQL + args, in call order
}

func (d *recordingDB) Begin(context.Context) (pgx.Tx, error) {
	return &recordingTx{db: d}, nil
}

func (d *recordingDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.record(sql, args)
}

func (d *recordingDB) record(sql string, args []any) (pgconn.CommandTag, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, strings.TrimSpace(sql)+" | "+fmt.Sprint(args...))
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

type recordingTx struct {
	db *recordingDB
}

func (t *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.record(sql, args)
}

func (t *recordingTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("Query not supported by recordingTx")
}

func (t *recordingTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (t *recordingTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("nested Begin not supported by recordingTx")
}

func (t *recordingTx) Conn() *pgx.Conn { return nil }

func (t *recordingTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("CopyFrom not supported by recordingTx")
}

func (t *recordingTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (t *recordingTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("Prepare not supported by recordingTx")
}

func (t *recordingTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (t *recordingTx) Commit(context.Context) error   { return nil }
func (t *recordingTx) Rollback(context.Context) error { return nil }

func (d *recordingDB) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// capturingWriter records the ProcessedRequest instances it is asked to
// write and returns a configurable error.
type capturingWriter struct {
	mu   sync.Mutex
	reqs []*v2.ProcessedRequest
	err  error
}

func (w *capturingWriter) Write(_ context.Context, req *v2.ProcessedRequest) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reqs = append(w.reqs, req)
	return w.err
}

func (w *capturingWriter) written() []*v2.ProcessedRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*v2.ProcessedRequest(nil), w.reqs...)
}

// --- helpers -----------------------------------------------------------

func mustPayload(t *testing.T, entry *telemetry.RequestLogEntry) []byte {
	t.Helper()
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return raw
}

func newTestReaper(db replayDB, writer V2Writer) *MirrorOutboxReaper {
	return &MirrorOutboxReaper{
		db:        db,
		writer:    writer,
		interval:  mirrorReplayDefaultInterval,
		batchSize: mirrorReplayDefaultBatch,
		maxAtts:   mirrorReplayDefaultMaxAtts,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func terminalEntry(id string) *telemetry.RequestLogEntry {
	success := true
	now := time.Now().UTC()
	gwSID := "gw_test_session"
	return &telemetry.RequestLogEntry{
		RequestID:    id,
		TenantID:     "default",
		GwSessionID:  &gwSID,
		Success:      success,
		PromptTokens: intPtr(10),
		CostUSD:      floatPtr(0.00001870),
		EventAt:      &now,
	}
}

// --- tests -------------------------------------------------------------

// TestEnqueueMirrorFailure_NilPoolReturnsFalse pins the degradation
// contract: with no registration pool wired (unit-test binary, or DB
// down at wiring time) the hook's failure paths must fall back to the
// in-process backlog — the pre-GAP-2 behaviour stays intact.
func TestEnqueueMirrorFailure_NilPoolReturnsFalse(t *testing.T) {
	InitMirrorOutbox(nil)
	entry := terminalEntry("req_nil_pool")
	if EnqueueMirrorFailure(entry, "gw_test_session", "write_failed") {
		t.Fatal("EnqueueMirrorFailure must return false when no pool is wired")
	}
}

// TestReplayOne_SuccessDeletesRow pins the happy path: a terminal business
// entry replays through the real entryToProcessedRequest bridge into the
// writer, then the row is deleted (no 'done' state by design).
func TestReplayOne_SuccessDeletesRow(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{}
	r := newTestReaper(db, writer)

	entry := terminalEntry("req_ok")
	r.replayOne(context.Background(), claimRow{
		id: 1, requestID: entry.RequestID, sessionID: *entry.GwSessionID,
		payload: mustPayload(t, entry),
	})

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("expected 1 replayed write, got %d", len(written))
	}
	if written[0].RequestID != entry.RequestID || written[0].SessionID != *entry.GwSessionID {
		t.Fatalf("replayed req mismatch: %s/%s", written[0].RequestID, written[0].SessionID)
	}
	if written[0].CostUSD != 0.00001870 {
		t.Fatalf("cost precision must survive the payload round-trip, got %v", written[0].CostUSD)
	}
	joined := strings.Join(db.statements(), "\n")
	if !strings.Contains(joined, "DELETE FROM public.session_mirror_outbox") {
		t.Fatalf("expected DELETE after successful replay, got %v", db.statements())
	}
	// R29: the table is ENABLE+FORCE RLS (712) — every reaper statement must
	// run inside the transaction-scoped bypass, otherwise non-'default'
	// tenants are invisible (orphan/claim/dead paths silently no-op).
	if !strings.Contains(joined, "set_config('app.bypass_rls', 'true', true)") {
		t.Fatalf("expected RLS bypass GUC lift before outbox statements, got %v", db.statements())
	}
}

// TestReplayOne_SyntheticSessionMarksSystemClient pins D4 parity on replay:
// header-less traffic keeps its system-session classification.
func TestReplayOne_SyntheticSessionMarksSystemClient(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{}
	r := newTestReaper(db, writer)

	entry := terminalEntry("req_probe")
	entry.GwSessionID = nil
	sid := SyntheticSessionID(entry)
	if sid == "" {
		t.Fatal("SyntheticSessionID returned empty for a probe entry")
	}
	r.replayOne(context.Background(), claimRow{
		id: 2, requestID: entry.RequestID, sessionID: "placeholder",
		payload: mustPayload(t, entry),
	})

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("expected 1 replayed write, got %d", len(written))
	}
	if written[0].SessionID != sid {
		t.Fatalf("expected synthetic session id %s, got %s", sid, written[0].SessionID)
	}
	if written[0].ClientType != "system" {
		t.Fatalf("synthetic replay must set client_type=system, got %q", written[0].ClientType)
	}
}

// TestReplayOne_SkipsHookExclusionClasses pins gate parity: non-terminal
// entries and internal loopbacks are deleted, never written, never retried.
func TestReplayOne_SkipsHookExclusionClasses(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{}
	r := newTestReaper(db, writer)

	nonTerminal := &telemetry.RequestLogEntry{
		RequestID:   "req_in_progress",
		GwSessionID: strPtr("gw_x"),
	}
	r.replayOne(context.Background(), claimRow{id: 3, payload: mustPayload(t, nonTerminal)})

	loopback := terminalEntry("req_loopback")
	autoReq := true
	loopback.IsAutoRequest = &autoReq
	loopback.TaskType = nil // gateway-internal loopback class (see isInternalAutoEntry)
	r.replayOne(context.Background(), claimRow{id: 4, payload: mustPayload(t, loopback)})

	if len(writer.written()) != 0 {
		t.Fatal("hook exclusion classes must not be replayed into V2")
	}
	joined := strings.Join(db.statements(), "\n")
	if strings.Count(joined, "DELETE FROM public.session_mirror_outbox") < 2 {
		t.Fatalf("expected both skipped rows deleted, got %v", db.statements())
	}
}

// TestReplayOne_SkipsProbeSyntheticSession pins the R60 S2-F4 gate parity:
// 存量的 probe synthetic 失败行（历史上多为 advisory lock 超时噪声，
// ~115/min）在重放时按 hook 同款谓词删除——不写 V2、不重试。
func TestReplayOne_SkipsProbeSyntheticSession(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{}
	r := newTestReaper(db, writer)

	probe := terminalEntry("req_probe_no_session")
	probe.GwSessionID = nil // 无会话头 ⇒ 合成路径
	probe.OriginStage = strPtr("node_probe")
	r.replayOne(context.Background(), claimRow{id: 11, requestID: probe.RequestID, payload: mustPayload(t, probe)})

	if len(writer.written()) != 0 {
		t.Fatal("probe synthetic row must not be replayed into V2")
	}
	joined := strings.Join(db.statements(), "\n")
	if !strings.Contains(joined, "DELETE FROM public.session_mirror_outbox") {
		t.Fatalf("expected skipped row deleted, got %v", db.statements())
	}
}

// TestReplayOne_CorruptPayloadMarksDead pins the decode terminal path.
func TestReplayOne_CorruptPayloadMarksDead(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{}
	r := newTestReaper(db, writer)

	r.replayOne(context.Background(), claimRow{
		id: 5, requestID: "req_bad", sessionID: "gw_x", payload: []byte("{not json"),
	})

	if len(writer.written()) != 0 {
		t.Fatal("corrupt payload must not reach the writer")
	}
	joined := strings.Join(db.statements(), "\n")
	if !strings.Contains(joined, "status = 'dead'") {
		t.Fatalf("expected dead-mark update, got %v", db.statements())
	}
}

// TestReplayOne_WriteFailRequeuesThenDead pins the retry state machine:
// first failure re-queues with backoff; a row already at max attempts dies.
func TestReplayOne_WriteFailRequeuesThenDead(t *testing.T) {
	db := &recordingDB{}
	writer := &capturingWriter{err: errors.New("simulated db down")}
	r := newTestReaper(db, writer)

	entry := terminalEntry("req_flaky")
	payload := mustPayload(t, entry)

	r.replayOne(context.Background(), claimRow{
		id: 6, requestID: entry.RequestID, sessionID: *entry.GwSessionID,
		payload: payload, attempts: 3,
	})
	joined := strings.Join(db.statements(), "\n")
	if !strings.Contains(joined, "status = 'pending'") || !strings.Contains(joined, "next_retry_at") {
		t.Fatalf("expected requeue update, got %v", db.statements())
	}
	if strings.Contains(joined, "status = 'dead'") {
		t.Fatalf("row below max attempts must not die, got %v", db.statements())
	}

	db2 := &recordingDB{}
	r2 := newTestReaper(db2, &capturingWriter{err: errors.New("simulated db down")})
	r2.replayOne(context.Background(), claimRow{
		id: 7, requestID: entry.RequestID, sessionID: *entry.GwSessionID,
		payload: payload, attempts: mirrorReplayDefaultMaxAtts - 1,
	})
	if !strings.Contains(strings.Join(db2.statements(), "\n"), "status = 'dead'") {
		t.Fatalf("row at max attempts must die, got %v", db2.statements())
	}
}
