package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestMinuteStatsAggregatorAtomicallyAggregatesDimensions(t *testing.T) {
	client, _ := newRedisBackendTestClient(t)
	aggregator := NewMinuteStatsAggregator(client)
	credential := CredentialRef{ProviderID: 2, CredentialID: 7}
	for i := 0; i < 2; i++ {
		if err := aggregator.Record(context.Background(), credential, "model:with/slash", 11, ForwardOutcome{}); err != nil {
			t.Fatalf("Record success: %v", err)
		}
	}
	if err := aggregator.Record(context.Background(), credential, "model:with/slash", 5, ForwardOutcome{Err: errPaceTimeout}); err != nil {
		t.Fatalf("Record failure: %v", err)
	}

	stats, err := aggregator.List(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats length = %d, want 2", len(stats))
	}
	if stats[0].Outcome != "success" || stats[0].Requests != 2 || stats[0].Successes != 2 || stats[0].EstimatedTokens != 22 {
		t.Fatalf("success stat = %+v", stats[0])
	}
	if stats[1].Outcome != "fail_prefirstbyte" || stats[1].Requests != 1 || stats[1].Failures != 1 || stats[1].EstimatedTokens != 5 {
		t.Fatalf("failure stat = %+v", stats[1])
	}
}
