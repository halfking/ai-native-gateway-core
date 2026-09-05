package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordStreamRecoveryL2ShadowNormalizesClosedLabels(t *testing.T) {
	t.Parallel()

	aligned := StreamRecoveryL2ShadowTotal.WithLabelValues("anthropic", StreamRecoveryL2ShadowResultAligned)
	alignedBefore := testutil.ToFloat64(aligned)
	RecordStreamRecoveryL2Shadow("anthropic", StreamRecoveryL2ShadowResultAligned)
	if got := testutil.ToFloat64(aligned) - alignedBefore; got != 1 {
		t.Fatalf("aligned delta = %v, want 1", got)
	}

	unknown := StreamRecoveryL2ShadowTotal.WithLabelValues("unknown", StreamRecoveryL2ShadowResultUndecided)
	unknownBefore := testutil.ToFloat64(unknown)
	RecordStreamRecoveryL2Shadow("tenant-should-never-be-a-label", "request-should-never-be-a-label")
	if got := testutil.ToFloat64(unknown) - unknownBefore; got != 1 {
		t.Fatalf("unknown/undecided delta = %v, want 1", got)
	}
}

func TestStreamRecoveryL2ShadowLabelsAreClosedEnums(t *testing.T) {
	t.Parallel()

	for _, protocol := range []string{"openai_chat", "openai_responses", "anthropic"} {
		if !streamRecoveryL2ShadowProtocols[protocol] {
			t.Fatalf("protocol %q unexpectedly missing from allowlist", protocol)
		}
	}
	for _, result := range streamRecoveryL2ShadowResults {
		if !isStreamRecoveryL2ShadowResult(result) {
			t.Fatalf("result %q unexpectedly rejected", result)
		}
	}
	if isStreamRecoveryL2ShadowResult("request-123") {
		t.Fatal("arbitrary label value must not be accepted")
	}
}
