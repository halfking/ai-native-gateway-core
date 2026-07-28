package bg

import (
	"context"
	"testing"
)

func TestNormalizeIntegrityAnomaly(t *testing.T) {
	if got := normalizeIntegrityAnomaly("fingerprint_drift"); got != "fingerprint_drift" {
		t.Fatalf("known anomaly = %q", got)
	}
	if got := normalizeIntegrityAnomaly("anomaly:unknown"); got != "model_mismatch" {
		t.Fatalf("unknown anomaly = %q, want model_mismatch", got)
	}
	if got := normalizeIntegrityAnomaly(""); got != "model_mismatch" {
		t.Fatalf("empty anomaly = %q, want model_mismatch", got)
	}
}

func TestProbeSamplePrefersRequestURL(t *testing.T) {
	// sample must hold a PII-safe identifier, not the response body.
	if got := probeSample(&ProbeResult{}); got != "" {
		t.Fatalf("empty result = %q, want empty", got)
	}
	if got := probeSample(&ProbeResult{RequestURL: "https://api.openai.com/v1/chat/completions"}); got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("request url = %q", got)
	}
	if got := probeSample(&ProbeResult{ErrCode: "load_target", RequestURL: "https://api.openai.com/v1/chat/completions"}); got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("request url preferred over err code = %q", got)
	}
	if got := probeSample(&ProbeResult{ErrCode: "load_target"}); got != "load_target" {
		t.Fatalf("err code fallback = %q", got)
	}
}

func TestProbeQueueWorkerRecordsOnlyIntegrityTasks(t *testing.T) {
	sink := &recordingIntegritySink{}
	worker := &ProbeQueueWorker{cfg: ProbeQueueWorkerConfig{ResultSink: sink}}
	ctx := context.Background()
	worker.recordIntegrityResult(ctx, ProbeQueueTask{Command: "single"}, nil, &ProbeResult{}, nil)
	if sink.calls != 0 {
		t.Fatalf("ordinary task called sink %d times", sink.calls)
	}
	worker.recordIntegrityResult(ctx, ProbeQueueTask{Command: "integrity_verify"}, nil, &ProbeResult{}, nil)
	if sink.calls != 1 {
		t.Fatalf("integrity task called sink %d times, want 1", sink.calls)
	}
}

type recordingIntegritySink struct{ calls int }

func (s *recordingIntegritySink) Record(_ context.Context, _ ProbeQueueTask, _ *ProbeTarget, _ *ProbeResult, _ error) error {
	s.calls++
	return nil
}
