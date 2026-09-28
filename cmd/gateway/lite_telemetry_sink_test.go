// Package main — lite_telemetry_sink_test.go
//
// 审计 B2 接线（2026-09-05）的回归测试：lite 模式下 telemetry 请求日志
// 经 liteRequestLogSink 落盘到存储工厂（SQLite request_logs / sessions /
// session_turns + FileBodies 原文文件），覆盖幂等 UPSERT、终态记轮门槛、
// 重启后续排轮号与 Shutdown 落盘。
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/storage"
	storagefactory "github.com/kaixuan/llm-gateway-go/storage/factory"
)

type blockingBodiesStore struct {
	storage.BodiesStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingBodiesStore) Write(ctx context.Context, body *storage.SessionBody) error {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.BodiesStore.Write(ctx, body)
}

type failFirstBodiesStore struct {
	storage.BodiesStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *failFirstBodiesStore) Write(ctx context.Context, body *storage.SessionBody) error {
	fail := false
	b.once.Do(func() {
		fail = true
		close(b.entered)
		<-b.release
	})
	if fail {
		return errors.New("synthetic body-store failure")
	}
	return b.BodiesStore.Write(ctx, body)
}

// newLiteSinkForTest 装配一份 lite runtime 并返回其 telemetry sink。
func newLiteSinkForTest(t *testing.T) (*storageRuntime, *liteRequestLogSink) {
	t.Helper()
	rt, err := initStorageMode(nil, liteStorageConfigForTest(t))
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	sink := rt.newLiteRequestLogSink()
	if sink == nil {
		rt.Shutdown()
		t.Fatal("newLiteRequestLogSink = nil")
	}
	return rt, sink
}

func liteSinkIntPtr(i int) *int              { return &i }
func liteSinkStrPtr(s string) *string        { return &s }
func liteSinkTimePtr(t time.Time) *time.Time { return &t }

// liteTerminalEntry 构造一条终态请求日志条目（带会话与请求/响应原文）。
func liteTerminalEntry(requestID, sessionID string) *telemetry.RequestLogEntry {
	status := telemetry.RequestStatusSuccess
	ep := "/v1/chat/completions"
	req := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	resp := `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
	return &telemetry.RequestLogEntry{
		RequestID:          requestID,
		TenantID:           "tenant-a",
		GwSessionID:        liteSinkStrPtr(sessionID),
		RequestStatus:      &status,
		Success:            true,
		RequestBody:        &req,
		ResponseBody:       &resp,
		ClientEndpoint:     &ep,
		UpstreamStatusCode: liteSinkIntPtr(200),
	}
}

// TestLiteRequestLogSink_PersistsFullJournal 验证终态条目落盘全链路：
// request_logs 行、sessions 行、session_turns 元数据与 FileBodies 原文
// 文件均可读回。
func TestLiteRequestLogSink_PersistsFullJournal(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()
	ctx := context.Background()

	entry := liteTerminalEntry("req-001", "sess-001")
	entry.EventAt = liteSinkTimePtr(time.Now())
	if err := sink.PersistRequestLog(ctx, entry); err != nil {
		t.Fatalf("PersistRequestLog: %v", err)
	}

	// request_logs 行读回
	log, err := sink.logs.GetRequest(ctx, "req-001")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if log.SessionID != "sess-001" {
		t.Errorf("SessionID = %q, want sess-001", log.SessionID)
	}

	// sessions 行读回
	sess, err := sink.sessions.GetSession(ctx, "tenant-a", "sess-001")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil || sess.ID != "sess-001" {
		t.Errorf("GetSession = %+v, want sess-001", sess)
	}

	// turn 元数据读回
	turns, err := sink.turns.GetTurnsMeta(ctx, "tenant-a", "sess-001")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(turns) != 1 || turns[0].TurnNo != 1 {
		t.Fatalf("GetTurnsMeta = %+v, want single turn 1", turns)
	}

	// body 原文读回
	body, err := sink.bodies.Read(ctx, "tenant-a", "sess-001", 1)
	if err != nil {
		t.Fatalf("Read body: %v", err)
	}
	if body == nil || len(body.Request) == 0 || len(body.Response) == 0 {
		t.Fatalf("body roundtrip missing content: %+v", body)
	}
}

// TestLiteRequestLogSink_IdempotentUpsert 同一 request_id 重复持久化
// （in_progress 占位 → 终态 → 重放）不产生重复行、不重复记轮。
func TestLiteRequestLogSink_IdempotentUpsert(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()
	ctx := context.Background()

	// in_progress 占位（无 body、非终态）→ 只写 request_logs 行，不记轮
	inProgress := telemetry.RequestStatusInProgress
	placeholder := &telemetry.RequestLogEntry{
		RequestID:     "req-002",
		TenantID:      "tenant-a",
		GwSessionID:   liteSinkStrPtr("sess-002"),
		RequestStatus: &inProgress,
	}
	if err := sink.PersistRequestLog(ctx, placeholder); err != nil {
		t.Fatalf("PersistRequestLog(placeholder): %v", err)
	}
	turns, err := sink.turns.GetTurnsMeta(ctx, "tenant-a", "sess-002")
	if err != nil {
		t.Fatalf("GetTurnsMeta after placeholder: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("placeholder entry journaled a turn: %+v", turns)
	}

	// 终态条目 → 记一轮
	if err := sink.PersistRequestLog(ctx, liteTerminalEntry("req-002", "sess-002")); err != nil {
		t.Fatalf("PersistRequestLog(terminal): %v", err)
	}
	// 同一 request_id 的终态重放 → 不再记轮
	if err := sink.PersistRequestLog(ctx, liteTerminalEntry("req-002", "sess-002")); err != nil {
		t.Fatalf("PersistRequestLog(replay): %v", err)
	}

	turns, err = sink.turns.GetTurnsMeta(ctx, "tenant-a", "sess-002")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("GetTurnsMeta = %d turns, want 1 (replay must not double-journal)", len(turns))
	}
}

// TestLiteRequestLogSink_ConcurrentReplayJournalsOnce forces one journal
// operation to overlap a replay of the same RequestID. The replay must wait
// for the first write to finish and then observe the completed idempotency key.
func TestLiteRequestLogSink_ConcurrentReplayJournalsOnce(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()

	base := sink.bodies
	blocked := &blockingBodiesStore{
		BodiesStore: base,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	sink.bodies = blocked
	ctx := context.Background()
	entry := liteTerminalEntry("req-concurrent", "sess-concurrent")
	firstDone := make(chan error, 1)
	go func() { firstDone <- sink.PersistRequestLog(ctx, entry) }()
	<-blocked.entered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- sink.PersistRequestLog(ctx, liteTerminalEntry("req-concurrent", "sess-concurrent"))
	}()

	select {
	case err := <-secondDone:
		t.Fatalf("concurrent replay returned before first journal completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(blocked.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first PersistRequestLog: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("replay PersistRequestLog: %v", err)
	}

	turns, err := sink.turns.GetTurnsMeta(ctx, "tenant-a", "sess-concurrent")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(turns) != 1 || turns[0].TurnNo != 1 {
		t.Fatalf("GetTurnsMeta = %+v, want exactly turn 1", turns)
	}
}

func TestLiteRequestLogSink_JournalClaimSerializesSameRequestID(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()
	claimed, err := sink.beginJournal(context.Background(), "req-claim")
	if err != nil || !claimed {
		t.Fatalf("first beginJournal() = (%v, %v), want (true, nil)", claimed, err)
	}

	secondDone := make(chan struct {
		claimed bool
		err     error
	}, 1)
	go func() {
		claimed, err := sink.beginJournal(context.Background(), "req-claim")
		secondDone <- struct {
			claimed bool
			err     error
		}{claimed: claimed, err: err}
	}()
	select {
	case got := <-secondDone:
		t.Fatalf("second beginJournal() returned before release: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	sink.completeJournal("req-claim")
	select {
	case got := <-secondDone:
		if got.err != nil || got.claimed {
			t.Fatalf("second beginJournal() = (%v, %v), want (false, nil)", got.claimed, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("second beginJournal() did not wake after completion")
	}
}

func TestLiteRequestLogSink_FailedJournalRetryReusesTurnNumber(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()

	base := sink.bodies
	flaky := &failFirstBodiesStore{BodiesStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	sink.bodies = flaky
	ctx := context.Background()
	firstDone := make(chan error, 1)
	go func() { firstDone <- sink.PersistRequestLog(ctx, liteTerminalEntry("req-retry", "sess-retry")) }()
	<-flaky.entered
	secondDone := make(chan error, 1)
	go func() { secondDone <- sink.PersistRequestLog(ctx, liteTerminalEntry("req-retry", "sess-retry")) }()
	close(flaky.release)
	if err := <-firstDone; err == nil {
		t.Fatal("first PersistRequestLog succeeded, want synthetic body-store failure")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("retry PersistRequestLog: %v", err)
	}

	turns, err := sink.turns.GetTurnsMeta(ctx, "tenant-a", "sess-retry")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(turns) != 1 || turns[0].TurnNo != 1 {
		t.Fatalf("GetTurnsMeta = %+v, want exactly retried turn 1", turns)
	}
	body, err := sink.bodies.Read(ctx, "tenant-a", "sess-retry", 1)
	if err != nil || body == nil || len(body.Response) == 0 {
		t.Fatalf("retry body at turn 1 = (%+v, %v), want persisted response", body, err)
	}
}

func TestLiteRequestLogSink_TurnReservationsStayBoundedAndRetainNewReservation(t *testing.T) {
	rt, sink := newLiteSinkForTest(t)
	defer rt.Shutdown()

	now := time.Now()
	sink.journalTurnReservations = make(map[string]journalTurnReservation, maxJournaledEntries+1)
	for i := 0; i < maxJournaledEntries; i++ {
		id := fmt.Sprintf("reserved-%05d", i)
		sink.journalTurnReservations[id] = journalTurnReservation{
			tenantID: "tenant-a", sessionID: "session-a", turnNo: i + 1,
			expiresAt: now.Add(time.Duration(i+1) * time.Minute),
		}
	}
	// An expired reservation may be retried with the same ID. There must not
	// be a stale auxiliary queue entry capable of evicting the new reservation.
	sink.journalTurnReservations["retry-id"] = journalTurnReservation{
		tenantID: "tenant-a", sessionID: "session-a", turnNo: 999,
		expiresAt: now.Add(-time.Minute),
	}

	turnNo, err := sink.journalTurnNo(context.Background(), "retry-id", "tenant-a", "session-a")
	if err != nil {
		t.Fatalf("journalTurnNo: %v", err)
	}
	reserved, ok := sink.journalTurnReservations["retry-id"]
	if !ok || reserved.turnNo != turnNo {
		t.Fatalf("new reservation = %+v, present=%v; allocated turn=%d", reserved, ok, turnNo)
	}
	if got := len(sink.journalTurnReservations); got > maxJournaledEntries {
		t.Fatalf("reservation count = %d, exceeds bound %d", got, maxJournaledEntries)
	}
}

// TestLiteRequestLogSink_TurnNoContinuesAcrossRestart 重启后（新 sink 实例）
// 轮号从已落盘 body 的最大 turn+1 续排，不回退覆盖既有轮。
func TestLiteRequestLogSink_TurnNoContinuesAcrossRestart(t *testing.T) {
	cfg := liteStorageConfigForTest(t)
	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode: %v", err)
	}
	ctx := context.Background()

	// 第一个生命周期：记两轮
	sink := rt.newLiteRequestLogSink()
	for _, id := range []string{"req-a", "req-b"} {
		if err := sink.PersistRequestLog(ctx, liteTerminalEntry(id, "sess-restart")); err != nil {
			t.Fatalf("PersistRequestLog(%s): %v", id, err)
		}
	}
	rt.Shutdown() // 排空异步写、关 SQLite

	// 第二个生命周期：同一路径重建（模拟进程重启）
	rt2, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode(restart): %v", err)
	}
	defer rt2.Shutdown()
	sink2 := rt2.newLiteRequestLogSink()

	if err := sink2.PersistRequestLog(ctx, liteTerminalEntry("req-c", "sess-restart")); err != nil {
		t.Fatalf("PersistRequestLog(req-c): %v", err)
	}

	turns, err := sink2.turns.GetTurnsMeta(ctx, "tenant-a", "sess-restart")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("GetTurnsMeta = %d turns, want 3 (1,2 pre-restart + 3 continued)", len(turns))
	}
	// 第三轮 body 可读回且不覆盖前两轮
	body, err := sink2.bodies.Read(ctx, "tenant-a", "sess-restart", 3)
	if err != nil {
		t.Fatalf("Read body(turn 3): %v", err)
	}
	if body == nil {
		t.Fatal("Read body(turn 3) = nil, want journaled body")
	}
}

// TestLiteRequestLogSink_DataSurvivesShutdown Shutdown 排空后数据仍在
// （FileBodies 异步写队列被 Close 排空，落盘完整）。
func TestLiteRequestLogSink_DataSurvivesShutdown(t *testing.T) {
	cfg := liteStorageConfigForTest(t)
	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode: %v", err)
	}
	ctx := context.Background()
	sink := rt.newLiteRequestLogSink()
	if err := sink.PersistRequestLog(ctx, liteTerminalEntry("req-durable", "sess-durable")); err != nil {
		t.Fatalf("PersistRequestLog: %v", err)
	}
	rt.Shutdown()

	// 直接用存储工厂验证落盘（不经 runtime 单例）。
	f, err := storagefactory.NewStorageFactory(&storage.StorageConfig{
		Mode:       storage.StorageModeLite,
		SQLitePath: cfg.Lite.SQLitePath,
		BodiesDir:  cfg.Lite.BodiesDir,
		CacheDir:   cfg.Lite.CacheDir,
		LogsDir:    cfg.Lite.LogsDir,
	})
	if err != nil {
		t.Fatalf("NewStorageFactory(verify): %v", err)
	}
	defer f.Close()
	got, err := f.NewRequestLogStore().GetRequest(ctx, "req-durable")
	if err != nil {
		t.Fatalf("GetRequest after shutdown: %v", err)
	}
	if got == nil || got.SessionID != "sess-durable" {
		t.Errorf("GetRequest after shutdown = %+v, want sess-durable row", got)
	}
	body, err := f.NewBodiesStore().Read(ctx, "tenant-a", "sess-durable", 1)
	if err != nil {
		t.Fatalf("Read body after shutdown: %v", err)
	}
	if body == nil || len(body.Response) == 0 {
		t.Errorf("Read body after shutdown = %+v, want persisted response body", body)
	}
}
