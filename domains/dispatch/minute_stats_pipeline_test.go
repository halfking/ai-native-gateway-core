package dispatch

import (
	"context"
	"testing"
)

func TestPipelineRecordsMinuteStatsAtTerminal(t *testing.T) {
	client, _ := newRedisBackendTestClient(t)
	aggregator := NewMinuteStatsAggregator(client)
	p := NewPipeline(Deps{MinuteStatsSink: aggregator})
	qr := NewQueuedRequest("minute-request", "tenant", "model", context.Background(), nil)
	qr.SelectedCred = CredentialRef{ProviderID: 4, CredentialID: 9}
	qr.ResolvedModel = "model"
	qr.EstimatedTokens = 13
	p.complete(qr, ForwardOutcome{Result: "ok"})

	if qr.T9_ResponseEndAt == nil {
		t.Fatal("terminal timestamp was not recorded")
	}
	stats, err := aggregator.List(context.Background(), *qr.T9_ResponseEndAt)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stats) != 1 || stats[0].Requests != 1 || stats[0].Successes != 1 || stats[0].EstimatedTokens != 13 {
		t.Fatalf("stats = %+v, want one successful aggregate", stats)
	}
}
