package bg

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

func TestAvailabilityKeyCounterCountOnce(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Seed 3 keys under the namespace and 1 unrelated key.
	for _, k := range []string{
		"llmgw:avail:1:minimax-m3",
		"llmgw:avail:2:glm-5.2",
		"llmgw:avail:3:claude-opus-4-6",
		"unrelated:counter:1",
	} {
		if err := client.HSet(ctx, k, map[string]any{"state": "ok"}).Err(); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	k := NewAvailabilityKeyCounter(client, time.Hour)
	count, err := scanAvailabilityKeys(ctx, client)
	if err != nil {
		t.Fatalf("scanAvailabilityKeys: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3 (only llmgw:avail:* keys)", count)
	}
	k.CountOnce(ctx)
}

func TestAvailabilityKeyCounterCountOnceEmpty(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	NewAvailabilityKeyCounter(client, time.Hour).CountOnce(context.Background())
}

func TestAvailabilityKeyCounterWritesGauge(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := client.HSet(ctx, "llmgw:avail:"+itoa(i)+":m", "state", "ok").Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Pre-condition: gauge is zero.
	pre := getGaugeValue(t, "llmgw_availability_keys_count")
	if pre != 0 {
		t.Fatalf("pre gauge = %v, want 0", pre)
	}

	NewAvailabilityKeyCounter(client, time.Hour).CountOnce(ctx)

	post := getGaugeValue(t, "llmgw_availability_keys_count")
	if post != 4 {
		t.Fatalf("post gauge = %v, want 4", post)
	}
}

// getGaugeValue fetches the current value of an llmgw_availability_keys
// gauge. Used to verify the SCAN-based counter reached the Prometheus
// default registry.
func getGaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	if availabilityKeys == nil {
		t.Fatalf("availabilityKeys gauge not initialised")
	}
	m := &dto.Metric{}
	if err := availabilityKeys.Write(m); err != nil {
		t.Fatalf("gauge write: %v", err)
	}
	return m.Gauge.GetValue()
}

// itoa is a tiny helper so the test stays self-contained.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
