package providerprofile

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// AttemptQualityAggregate is an attempt-level supplier quality metric. It is
// intentionally separate from request-level availability and cost metrics.
type AttemptQualityAggregate struct {
	ProviderID               int64          `json:"provider_id"`
	CredentialID             int64          `json:"credential_id"`
	Model                    string         `json:"model"`
	AttemptTotal             int            `json:"attempt_total"`
	AttemptSuccess           int            `json:"attempt_success"`
	AttemptFailure           int            `json:"attempt_failure"`
	AttemptCanceled          int            `json:"attempt_canceled"`
	FirstAttemptTotal        int            `json:"first_attempt_total"`
	FirstAttemptSuccess      int            `json:"first_attempt_success"`
	FirstAttemptSuccessRate  float64        `json:"first_attempt_success_rate"`
	AttemptSuccessRate       float64        `json:"attempt_success_rate"`
	RetryRate                float64        `json:"retry_rate"`
	SameNodeRetryRate        float64        `json:"same_node_retry_rate"`
	NodeSwitchOutRate        float64        `json:"node_switch_out_rate"`
	NodeSwitchInTotal        int            `json:"node_switch_in_total"`
	NodeSwitchInSuccess      int            `json:"node_switch_in_success"`
	NodeSwitchInSuccessRate  float64        `json:"node_switch_in_success_rate"`
	ModelSwitchRate          float64        `json:"model_switch_rate"`
	ObservationDegradedCount int            `json:"observation_degraded_count"`
	ErrorKinds               map[string]int `json:"error_kinds"`
	HTTPStatuses             map[int]int    `json:"http_statuses"`
	TTFTP50Ms                int            `json:"ttft_p50_ms"`
	TTFTP95Ms                int            `json:"ttft_p95_ms"`
	TTFTP99Ms                int            `json:"ttft_p99_ms"`
	LatencyP50Ms             int            `json:"latency_p50_ms"`
	LatencyP95Ms             int            `json:"latency_p95_ms"`
	LatencyP99Ms             int            `json:"latency_p99_ms"`
	ttftSamples              []int
	latencySamples           []int
}

type attemptQualityKey struct {
	providerID   int64
	credentialID int64
	model        string
}

// AttemptFactReader provides tenant-scoped, content-free attempt facts.
type AttemptFactReader interface {
	AttemptFacts(context.Context, string, time.Time, time.Time) ([]requestjourney.AttemptFact, error)
}

// AttemptQualityAnalyzer connects durable journey facts to supplier-quality
// aggregation without changing final-request metrics.
type AttemptQualityAnalyzer struct {
	reader AttemptFactReader
}

func NewAttemptQualityAnalyzer(reader AttemptFactReader) *AttemptQualityAnalyzer {
	return &AttemptQualityAnalyzer{reader: reader}
}

func (a *AttemptQualityAnalyzer) Analyze(ctx context.Context, tenantID string, start, end time.Time) ([]AttemptQualityAggregate, error) {
	if a == nil || a.reader == nil {
		return nil, fmt.Errorf("analyze attempt quality failed: attempt fact reader is unavailable (context: tenant_id=%s)", tenantID)
	}
	facts, err := a.reader.AttemptFacts(ctx, tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("read attempt facts failed: %w (context: tenant_id=%s)", err, tenantID)
	}
	return AggregateAttemptQuality(facts), nil
}

// AggregateAttemptQuality groups content-free attempt facts by the exact
// supplier node that served each attempt.
func AggregateAttemptQuality(facts []requestjourney.AttemptFact) []AttemptQualityAggregate {
	byRequest := make(map[string][]requestjourney.AttemptFact)
	for _, fact := range facts {
		byRequest[fact.RequestID] = append(byRequest[fact.RequestID], fact)
	}
	aggregates := make(map[attemptQualityKey]*AttemptQualityAggregate)
	for _, requestFacts := range byRequest {
		sort.Slice(requestFacts, func(i, j int) bool { return requestFacts[i].AttemptNo < requestFacts[j].AttemptNo })
		for index, fact := range requestFacts {
			key := attemptQualityKey{fact.ProviderID, fact.CredentialID, fact.Model}
			aggregate := aggregates[key]
			if aggregate == nil {
				aggregate = &AttemptQualityAggregate{
					ProviderID: fact.ProviderID, CredentialID: fact.CredentialID, Model: fact.Model,
					ErrorKinds: map[string]int{}, HTTPStatuses: map[int]int{},
				}
				aggregates[key] = aggregate
			}
			addAttemptQualityFact(aggregate, fact, index > 0 && requestFacts[index-1].NodeSwitched)
		}
	}
	result := make([]AttemptQualityAggregate, 0, len(aggregates))
	for _, aggregate := range aggregates {
		finishAttemptQualityRates(aggregate)
		result = append(result, *aggregate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderID != result[j].ProviderID {
			return result[i].ProviderID < result[j].ProviderID
		}
		if result[i].CredentialID != result[j].CredentialID {
			return result[i].CredentialID < result[j].CredentialID
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func addAttemptQualityFact(aggregate *AttemptQualityAggregate, fact requestjourney.AttemptFact, switchedIn bool) {
	aggregate.AttemptTotal++
	if fact.AttemptNo == 1 {
		aggregate.FirstAttemptTotal++
	}
	switch fact.Outcome {
	case requestjourney.OutcomeSuccess:
		aggregate.AttemptSuccess++
		if fact.AttemptNo == 1 {
			aggregate.FirstAttemptSuccess++
		}
		if switchedIn {
			aggregate.NodeSwitchInSuccess++
		}
	case requestjourney.OutcomeFailure:
		aggregate.AttemptFailure++
	case requestjourney.OutcomeCanceled:
		aggregate.AttemptCanceled++
	}
	if fact.RetryScheduled {
		aggregate.RetryRate++
		aggregate.SameNodeRetryRate++
	}
	if fact.NodeSwitched {
		aggregate.NodeSwitchOutRate++
	}
	if switchedIn {
		aggregate.NodeSwitchInTotal++
	}
	if fact.ModelSwitched {
		aggregate.ModelSwitchRate++
	}
	if fact.ObservationStatus == requestjourney.ObservationDegraded {
		aggregate.ObservationDegradedCount++
	}
	if fact.ErrorKind != "" {
		aggregate.ErrorKinds[fact.ErrorKind]++
	}
	if fact.HTTPStatus != 0 {
		aggregate.HTTPStatuses[fact.HTTPStatus]++
	}
	if fact.FirstByteAt != nil {
		ttft := int(fact.FirstByteAt.Sub(fact.StartedAt).Milliseconds())
		if ttft >= 0 {
			aggregate.ttftSamples = append(aggregate.ttftSamples, ttft)
		}
	}
	if fact.EndedAt != nil {
		latency := int(fact.EndedAt.Sub(fact.StartedAt).Milliseconds())
		if latency >= 0 {
			aggregate.latencySamples = append(aggregate.latencySamples, latency)
		}
	}
}

func finishAttemptQualityRates(aggregate *AttemptQualityAggregate) {
	if aggregate.AttemptTotal > 0 {
		total := float64(aggregate.AttemptTotal)
		aggregate.AttemptSuccessRate = float64(aggregate.AttemptSuccess) / total
		aggregate.RetryRate /= total
		aggregate.SameNodeRetryRate /= total
		aggregate.NodeSwitchOutRate /= total
		aggregate.ModelSwitchRate /= total
	}
	if aggregate.FirstAttemptTotal > 0 {
		aggregate.FirstAttemptSuccessRate = float64(aggregate.FirstAttemptSuccess) / float64(aggregate.FirstAttemptTotal)
	}
	if aggregate.NodeSwitchInTotal > 0 {
		aggregate.NodeSwitchInSuccessRate = float64(aggregate.NodeSwitchInSuccess) / float64(aggregate.NodeSwitchInTotal)
	}
	aggregate.TTFTP50Ms, aggregate.TTFTP95Ms, aggregate.TTFTP99Ms = percentiles(aggregate.ttftSamples)
	aggregate.LatencyP50Ms, aggregate.LatencyP95Ms, aggregate.LatencyP99Ms = percentiles(aggregate.latencySamples)
	aggregate.ttftSamples = nil
	aggregate.latencySamples = nil
}

func percentiles(samples []int) (int, int, int) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	return percentile(sorted, 50), percentile(sorted, 95), percentile(sorted, 99)
}

func percentile(sorted []int, p int) int {
	index := (len(sorted)*p + 99) / 100
	if index > 0 {
		index--
	}
	return sorted[index]
}
