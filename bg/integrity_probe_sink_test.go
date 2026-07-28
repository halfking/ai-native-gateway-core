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
}

func TestProbeSampleTruncatesResponse(t *testing.T) {
	result := &ProbeResult{ResponseBody: "0123456789"}
	if got := probeSample(result); got != "0123456789" {
		t.Fatalf("sample = %q", got)
	}
	result.ResponseBody = ""
	result.RespPreview = "preview"
	if got := probeSample(result); got != "preview" {
		t.Fatalf("preview sample = %q", got)
	}
}

func TestProbeQueueWorkerRecordsOnlyIntegrityTasks(t *testing.T) {
	sink := &recordingIntegritySink{}
	worker := &ProbeQueueWorker{cfg: ProbeQueueWorkerConfig{ResultSink: sink}}
	worker.recordIntegrityResult(t.Context(), ProbeQueueTask{Command: "single"}, nil, &ProbeResult{}, nil)
	if sink.calls != 0 {
		t.Fatalf("ordinary task called sink %d times", sink.calls)
	}
	worker.recordIntegrityResult(t.Context(), ProbeQueueTask{Command: "integrity_verify"}, nil, &ProbeResult{}, nil)
	if sink.calls != 1 {
		t.Fatalf("integrity task called sink %d times, want 1", sink.calls)
	}
}

type recordingIntegritySink struct{ calls int }

func (s *recordingIntegritySink) Record(_ context.Context, _ ProbeQueueTask, _ *ProbeTarget, _ *ProbeResult, _ error) error {
	s.calls++
	return nil
}
