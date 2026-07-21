package recovery

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMini(t *testing.T) *redis.Client {
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestReadyGateStartsClosed(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if m.Ready(context.Background()) {
		t.Fatalf("manager must start with ready=0")
	}
}

func TestReadyGateOpens(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if err := m.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	if !m.Ready(context.Background()) {
		t.Fatalf("manager must be ready after SetReady(true)")
	}
}

func TestEnterRecoveryClosesGate(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	_ = m.SetReady(context.Background(), true)
	_ = m.EnterRecovery(context.Background(), "manual")
	if m.Ready(context.Background()) {
		t.Fatalf("EnterRecovery must close gate")
	}
}
