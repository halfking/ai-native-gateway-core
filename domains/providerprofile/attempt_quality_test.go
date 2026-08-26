package providerprofile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

type attemptFactReaderStub struct {
	tenantID string
	facts    []requestjourney.AttemptFact
	err      error
}

func (s *attemptFactReaderStub) AttemptFacts(_ context.Context, tenantID string, _, _ time.Time) ([]requestjourney.AttemptFact, error) {
	s.tenantID = tenantID
	return s.facts, s.err
}

func TestAttemptQualityAnalyzerUsesTenantScopedFactReader(t *testing.T) {
	reader := &attemptFactReaderStub{facts: []requestjourney.AttemptFact{{
		RequestID: "request-1", ProviderID: 101, CredentialID: 11, Model: "model-a",
		AttemptNo: 1, Outcome: requestjourney.OutcomeSuccess, ObservationStatus: requestjourney.ObservationComplete,
	}}}
	result, err := NewAttemptQualityAnalyzer(reader).Analyze(context.Background(), "tenant-a", time.Unix(1, 0), time.Unix(2, 0))
	if err != nil || len(result) != 1 || result[0].AttemptSuccess != 1 || reader.tenantID != "tenant-a" {
		t.Fatalf("Analyze() = (%+v, %v), tenant=%q", result, err, reader.tenantID)
	}

	_, err = NewAttemptQualityAnalyzer(&attemptFactReaderStub{err: errors.New("database unavailable")}).Analyze(context.Background(), "tenant-a", time.Unix(1, 0), time.Unix(2, 0))
	if err == nil {
		t.Fatal("Analyze() error = nil, want reader failure")
	}
}

func TestAggregateAttemptQualityCountsFailedNodeAndSuccessfulReplacementSeparately(t *testing.T) {
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	aggregates := AggregateAttemptQuality([]requestjourney.AttemptFact{
		{
			ProviderID: 101, CredentialID: 11, Model: "model-a", AttemptNo: 1,
			StartedAt: startedAt, Outcome: requestjourney.OutcomeFailure,
			ErrorKind: requestjourney.ErrorKindUpstreamError, HTTPStatus: 503,
			NodeSwitched: true, RetryScheduled: true, ObservationStatus: requestjourney.ObservationComplete,
		},
		{
			ProviderID: 202, CredentialID: 22, Model: "model-b", AttemptNo: 2,
			StartedAt: startedAt.Add(time.Second), Outcome: requestjourney.OutcomeSuccess,
			ObservationStatus: requestjourney.ObservationComplete,
		},
	})
	if len(aggregates) != 2 {
		t.Fatalf("aggregate count = %d, want 2", len(aggregates))
	}
	first := aggregates[0]
	if first.ProviderID != 101 || first.CredentialID != 11 || first.Model != "model-a" || first.AttemptTotal != 1 || first.AttemptFailure != 1 || first.AttemptSuccess != 0 || first.FirstAttemptSuccessRate != 0 || first.AttemptSuccessRate != 0 || first.RetryRate != 1 || first.SameNodeRetryRate != 1 || first.NodeSwitchOutRate != 1 || first.ErrorKinds[requestjourney.ErrorKindUpstreamError] != 1 || first.HTTPStatuses[503] != 1 {
		t.Fatalf("first node aggregate = %+v", first)
	}
	second := aggregates[1]
	if second.ProviderID != 202 || second.CredentialID != 22 || second.Model != "model-b" || second.AttemptTotal != 1 || second.AttemptSuccess != 1 || second.AttemptFailure != 0 || second.FirstAttemptSuccessRate != 0 || second.AttemptSuccessRate != 1 || second.NodeSwitchInSuccessRate != 1 {
		t.Fatalf("replacement node aggregate = %+v", second)
	}
}

func TestAggregateAttemptQualityReportsDegradedObservationsOutsideFailures(t *testing.T) {
	aggregates := AggregateAttemptQuality([]requestjourney.AttemptFact{{
		ProviderID: 101, CredentialID: 11, Model: "model-a", AttemptNo: 1,
		Outcome: requestjourney.OutcomeFailure, ObservationStatus: requestjourney.ObservationDegraded,
	}})
	if len(aggregates) != 1 {
		t.Fatalf("aggregate count = %d, want 1", len(aggregates))
	}
	got := aggregates[0]
	if got.AttemptFailure != 1 || got.ObservationDegradedCount != 1 {
		t.Fatalf("degraded aggregate = %+v", got)
	}
}
