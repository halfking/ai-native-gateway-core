package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

type durableWiringPGStub struct{}

type durableWiringRowStub struct{}

func (durableWiringPGStub) QueryRow(context.Context, string, ...any) pgx.Row {
	return durableWiringRowStub{}
}

func (durableWiringRowStub) Scan(...any) error { return pgx.ErrNoRows }

type recoveryWorkerStub struct {
	starts int
	stops  int
	ctx    context.Context
}

type durableCreateClaimerStub struct {
	gotInput durable.CreateTaskInput
	gotOwner string
	gotLease time.Duration
	task     *durable.Task
	err      error
}

func (s *durableCreateClaimerStub) CreateAndClaim(_ context.Context, input durable.CreateTaskInput, owner string, lease time.Duration) (*durable.Task, error) {
	s.gotInput = input
	s.gotOwner = owner
	s.gotLease = lease
	return s.task, s.err
}

type recoveryRunnerStub struct {
	started chan struct{}
	stopped chan struct{}
}

func (r *recoveryRunnerStub) Run(ctx context.Context) {
	close(r.started)
	<-ctx.Done()
	close(r.stopped)
}

func (w *recoveryWorkerStub) Start(ctx context.Context) {
	w.starts++
	w.ctx = ctx
}

func (w *recoveryWorkerStub) Stop() { w.stops++ }

func TestUpgradePendingStoreForDurabilityFlagOffPreservesStore(t *testing.T) {
	original := pending.NewStore(nil, time.Minute)
	got, upgraded := upgradePendingStoreForDurability(
		original, nil, time.Minute, false, durableWiringPGStub{}, durableWiringKeyring(t),
	)
	if upgraded {
		t.Fatal("durable flag off must not enable PG fallback")
	}
	if got != original {
		t.Fatal("durable flag off must preserve the original pending store pointer")
	}
}

func TestUpgradePendingStoreForDurabilityRequiresDBAndKeyring(t *testing.T) {
	original := pending.NewStore(nil, time.Minute)
	tests := []struct {
		name string
		db   pending.PGQuerier
		kr   *secret.Keyring
	}{
		{name: "missing DB", kr: durableWiringKeyring(t)},
		{name: "missing keyring", db: durableWiringPGStub{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, upgraded := upgradePendingStoreForDurability(
				original, nil, time.Minute, true, tt.db, tt.kr,
			)
			if upgraded || got != original {
				t.Fatalf("incomplete prerequisites changed store: upgraded=%v got=%p original=%p", upgraded, got, original)
			}
		})
	}
}

func TestUpgradePendingStoreForDurabilityEnablesPGFallback(t *testing.T) {
	original := pending.NewStore(nil, time.Minute)
	got, upgraded := upgradePendingStoreForDurability(
		original, nil, time.Minute, true, durableWiringPGStub{}, durableWiringKeyring(t),
	)
	if !upgraded {
		t.Fatal("complete durable prerequisites must enable PG fallback")
	}
	if got == original {
		t.Fatal("fallback upgrade must return the NewStoreWithFallback instance")
	}
	r, found, err := got.Get(context.Background(), "session", "request")
	if err != nil || found || r != nil {
		t.Fatalf("upgraded fallback result = (%v,%v,%v), want clean PG miss", r, found, err)
	}
}

func TestDurableRequestStoreAdapterMapsCreateAndClaim(t *testing.T) {
	stub := &durableCreateClaimerStub{task: &durable.Task{
		ID: "task-1", LeaseOwner: "gateway-a/request-1", FencingToken: 7,
	}}
	adapter := durableRequestStoreAdapter{
		repository: stub, ownerPrefix: "gateway-a", leaseDuration: 45 * time.Second,
	}
	deadline := time.Now().Add(time.Hour)
	lease, err := adapter.CreateAndClaim(context.Background(), streaming.DurableCreateRequest{
		TaskID: "task-1", TenantID: "tenant-a", RequestID: "request-1", SessionID: "session-1",
		RequestHash: "hash-1", Protocol: "openai-chat", Endpoint: "/v1/chat/completions",
		SnapshotCiphertext: "ciphertext", SnapshotVersion: 1, EncryptionKeyID: "kid-1",
		APIKeyID: 3, ApplicationID: 4, DeadlineAt: deadline,
	})
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if lease.TaskID != "task-1" || lease.LeaseOwner != stub.task.LeaseOwner || lease.FencingToken != 7 {
		t.Fatalf("lease = %+v", lease)
	}
	if stub.gotOwner != "gateway-a/request-1" || stub.gotLease != 45*time.Second {
		t.Fatalf("owner/lease = %q/%s", stub.gotOwner, stub.gotLease)
	}
	if stub.gotInput.ID != "task-1" || stub.gotInput.SnapshotCiphertext != "ciphertext" || stub.gotInput.DeadlineAt != deadline {
		t.Fatalf("mapped input = %+v", stub.gotInput)
	}
	if len(stub.gotInput.Policy) == 0 {
		t.Fatal("adapter must freeze API/application policy metadata")
	}
}

func TestDurableRequestStoreAdapterPropagatesFailure(t *testing.T) {
	want := errors.New("db unavailable")
	adapter := durableRequestStoreAdapter{repository: &durableCreateClaimerStub{err: want}}
	if _, err := adapter.CreateAndClaim(context.Background(), streaming.DurableCreateRequest{}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestRecoveryWorkerManagerCancelsAndWaits(t *testing.T) {
	runner := &recoveryRunnerStub{started: make(chan struct{}), stopped: make(chan struct{})}
	manager := newRecoveryWorkerManager(runner)
	manager.Start(context.Background())
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	manager.Stop()
	select {
	case <-runner.stopped:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestRecoveryWorkerLifecycleGate(t *testing.T) {
	ctx := context.Background()
	worker := &recoveryWorkerStub{}

	if startRecoveryWorker(ctx, false, worker) {
		t.Fatal("flag-off worker must not start")
	}
	stopRecoveryWorker(false, worker)
	if worker.starts != 0 || worker.stops != 0 {
		t.Fatalf("flag-off lifecycle calls = start:%d stop:%d", worker.starts, worker.stops)
	}

	started := startRecoveryWorker(ctx, true, worker)
	if !started || worker.starts != 1 || worker.ctx != ctx {
		t.Fatalf("enabled worker start = %v calls=%d ctx=%p", started, worker.starts, worker.ctx)
	}
	stopRecoveryWorker(started, worker)
	if worker.stops != 1 {
		t.Fatalf("enabled worker stops = %d, want 1", worker.stops)
	}
}

func durableWiringKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, err := secret.NewKeyring(map[string][32]byte{"test": key}, "test")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return kr
}
