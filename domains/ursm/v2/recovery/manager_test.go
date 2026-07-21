package recovery

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

func TestEnterRecoveryWritesEpochMetadata(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if err := m.EnterRecovery(context.Background(), "manual-restart"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	rdb := m.rdb
	epoch, err := rdb.HGetAll(context.Background(), "ursm:v2:meta:epoch").Result()
	if err != nil {
		t.Fatalf("hgetall: %v", err)
	}
	if epoch["reason"] != "manual-restart" {
		t.Fatalf("reason missing: %v", epoch)
	}
	if epoch["counter"] != "1" {
		t.Fatalf("counter not incremented: %v", epoch)
	}
	if _, ok := epoch["started_at"]; !ok {
		t.Fatalf("started_at missing: %v", epoch)
	}
}

func TestReadyGaugeSyncsWithRedis(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()

	// Set ready to true
	if err := m.SetReady(ctx, true); err != nil {
		t.Fatalf("SetReady(true): %v", err)
	}

	// Check gauge value is 1
	expected := `
# HELP ursm_v2_ready URSM v2 recovery gate state (1=ready, 0=not ready)
# TYPE ursm_v2_ready gauge
ursm_v2_ready 1
`
	if err := testutil.CollectAndCompare(ursmV2ReadyGauge, strings.NewReader(expected)); err != nil {
		t.Fatalf("gauge not 1 after SetReady(true): %v", err)
	}

	// Set ready to false
	if err := m.SetReady(ctx, false); err != nil {
		t.Fatalf("SetReady(false): %v", err)
	}

	// Check gauge value is 0
	expected = `
# HELP ursm_v2_ready URSM v2 recovery gate state (1=ready, 0=not ready)
# TYPE ursm_v2_ready gauge
ursm_v2_ready 0
`
	if err := testutil.CollectAndCompare(ursmV2ReadyGauge, strings.NewReader(expected)); err != nil {
		t.Fatalf("gauge not 0 after SetReady(false): %v", err)
	}

	// Call Ready() and ensure it syncs the gauge
	_ = m.SetReady(ctx, true)
	isReady := m.Ready(ctx)
	if !isReady {
		t.Fatalf("Ready() should return true")
	}

	// Check gauge is back to 1
	expected = `
# HELP ursm_v2_ready URSM v2 recovery gate state (1=ready, 0=not ready)
# TYPE ursm_v2_ready gauge
ursm_v2_ready 1
`
	if err := testutil.CollectAndCompare(ursmV2ReadyGauge, strings.NewReader(expected)); err != nil {
		t.Fatalf("gauge not 1 after Ready(): %v", err)
	}
}
