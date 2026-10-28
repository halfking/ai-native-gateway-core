package taskbus_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/taskbus"
)

func TestBus_EnqueueDispatch_Idempotent(t *testing.T) {
	bus := taskbus.NewDemoBus()
	var hits atomic.Int64
	bus.Registry.MustRegister("demo.echo.v1", taskbus.HandlerFunc(func(ctx context.Context, task taskbus.Task) error {
		hits.Add(1)
		return nil
	}))

	ctx := context.Background()
	payload, _ := json.Marshal(map[string]string{"k": "v"})
	if err := bus.Enqueue(ctx, "demo.echo.v1", "idem-1", 1, payload); err != nil {
		t.Fatal(err)
	}
	if err := bus.Enqueue(ctx, "demo.echo.v1", "idem-1", 1, payload); err != nil {
		t.Fatal(err)
	}
	pending, _ := bus.Store.CountByStatus(ctx, "demo.echo.v1", taskbus.StatusPending)
	if pending != 1 {
		t.Fatalf("pending=%d want 1 (idempotent)", pending)
	}
	n, err := bus.DispatchOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("handled=%d want 1", n)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d want 1", hits.Load())
	}
	sent, _ := bus.Store.CountByStatus(ctx, "demo.echo.v1", taskbus.StatusSent)
	if sent != 1 {
		t.Fatalf("sent=%d want 1", sent)
	}
}

func TestASMTenantRegistered(t *testing.T) {
	bus := taskbus.NewDemoBus()
	if !bus.HasHandler(taskbus.TaskTypeASMEventV1) {
		t.Fatal("asm.event.v1 must be registered as first tenant")
	}
}

func TestHasHandler_ImplementsLookup(t *testing.T) {
	bus := taskbus.NewDemoBus()
	var _ interface{ HasHandler(string) bool } = bus
}
