package ursm_test

// taskbus_g4_wiring_ledger_test.go — G4 接线证明：入队→消费→sent 台账上涨。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"
	"github.com/kaixuan/llm-gateway-go/internal/taskbus"
)

func TestTaskBusG4_EnqueueConsumeLedger(t *testing.T) {
	bus := taskbus.NewDemoBus()
	store := cache.NewInMemoryStore()
	cache.RegisterCacheSaveHandler(bus.Registry, store)

	body := []byte("prompt-g4")
	hook := cache.NewCacheSaveTaskHook(bus, time.Minute)
	env := &domain.PipelineRequest{
		TenantID:           "t-g4",
		TransformedRequest: body,
		UpstreamResponse:   []byte(`{"ok":true}`),
		Metadata:           map[string]any{cache.MetaKeyModel: "m1", cache.MetaKeyCacheHit: false},
		Envelope:           &domain.RequestEnvelope{RequestID: "req-g4"},
	}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	beforeSent, _ := bus.Store.CountByStatus(context.Background(), taskbus.TaskTypeCacheSaveV1, taskbus.StatusSent)
	n, err := bus.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("handled=%d want >=1", n)
	}
	afterSent, _ := bus.Store.CountByStatus(context.Background(), taskbus.TaskTypeCacheSaveV1, taskbus.StatusSent)
	if afterSent != beforeSent+1 {
		t.Fatalf("sent ledger did not rise: before=%d after=%d", beforeSent, afterSent)
	}
	sum := sha256.Sum256(body)
	got, hit, err := store.Get(cache.CacheKey{TenantID: "t-g4", Model: "m1", Hash: hex.EncodeToString(sum[:])})
	if err != nil || !hit {
		t.Fatalf("cache miss after consume: hit=%v err=%v", hit, err)
	}
	if string(got.Value) != `{"ok":true}` {
		t.Fatalf("value=%q", got.Value)
	}
}
