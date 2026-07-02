package integration

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// fakeEmitterBus 记录已发布事件并允许注入错误。
type fakeEmitterBus struct {
	events []analysis.AnalysisEvent
	err    error
}

func (f *fakeEmitterBus) Publish(_ context.Context, evt analysis.AnalysisEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, evt)
	return nil
}

func newEmitterTestSessionContext() *session.SessionContext {
	return &session.SessionContext{
		SessionID:   "sess-test",
		RequestID:   "req-test",
		TenantID:    "tenant-test",
		ClientModel: "gpt-4",
	}
}

func newMockDB(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	// Loop 启动后会立刻调一次 poll（可能 query 失败），让 mock 返回空结果
	mock.ExpectQuery("").WillReturnRows(pgxmock.NewRows([]string{"event_id", "type", "tenant_id", "session_id", "request_id", "payload", "occurred_at"}))
	return mock
}

func TestSetup_NilDB(t *testing.T) {
	_, err := SetupClientProfileIntegration(
		context.Background(),
		nil, // db
		nil,
		&fakeEmitterBus{},
		ClientProfileLoopConfig{},
	)
	if err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestSetup_NilPublisher(t *testing.T) {
	mock := newMockDB(t)
	_, err := SetupClientProfileIntegration(
		context.Background(),
		mock,
		nil,
		nil, // publisher
		ClientProfileLoopConfig{},
	)
	if err == nil {
		t.Fatal("expected error for nil publisher")
	}
}

func TestSetup_Defaults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := newMockDB(t)
	bundle, err := SetupClientProfileIntegration(
		ctx,
		mock,
		nil, // sqlDB nil → Store 留空
		&fakeEmitterBus{},
		ClientProfileLoopConfig{},
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if bundle == nil {
		t.Fatal("nil bundle")
	}
	if bundle.Worker == nil {
		t.Fatal("Worker is nil")
	}
	if bundle.Emitter == nil {
		t.Fatal("Emitter is nil")
	}
	if bundle.Cancel == nil {
		t.Fatal("Cancel is nil")
	}
	if bundle.Worker.Name() != "client_profile_worker" {
		t.Errorf("Worker.Name = %q", bundle.Worker.Name())
	}
	if bundle.Store != nil {
		t.Errorf("expected nil Store when sqlDB is nil, got %T", bundle.Store)
	}
	// 立即 cancel 避免 RunLoop goroutine 残留
	bundle.Cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestSetup_WithSQLDB(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := newMockDB(t)
	// sqlDB 用 nil，因为 nil *sql.DB 也能通过 NewPostgresStore 构造（仅查询会失败）
	bundle, err := SetupClientProfileIntegration(
		ctx,
		mock,
		nil,
		&fakeEmitterBus{},
		ClientProfileLoopConfig{},
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer bundle.Cancel()
	_ = bundle
}

func TestSetup_CustomConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := newMockDB(t)
	bundle, err := SetupClientProfileIntegration(
		ctx,
		mock,
		nil,
		&fakeEmitterBus{},
		ClientProfileLoopConfig{
			Interval:  2 * time.Second,
			BatchSize: 50,
		},
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer bundle.Cancel()
}

func TestEmitterIsUsable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := newMockDB(t)
	bus := &fakeEmitterBus{}
	bundle, err := SetupClientProfileIntegration(
		ctx,
		mock,
		nil,
		bus,
		ClientProfileLoopConfig{},
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer bundle.Cancel()

	sc := newEmitterTestSessionContext()
	if err := bundle.Emitter.EmitRequestCompleted(ctx, sc, "h", true, 10, 20); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(bus.events) != 1 {
		t.Fatalf("events = %d, want 1", len(bus.events))
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventRequestCompleted {
		t.Errorf("type = %s", evt.Type)
	}
}
