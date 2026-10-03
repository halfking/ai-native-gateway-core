package sessionv2mirror

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// Audit §9.150.4 / §9.155: the telemetry entry has always carried
// RequestStatus, and the mirror used to drop it on the floor. That is what
// made model-catalog scan traffic (`rate_limited`) indistinguishable from
// real upstream failures on the session side — the single reason the
// retirement could not identify ~446k already-mirrored turns.
//
// The value must be copied VERBATIM. Re-deriving it from Success/ErrorKind
// cannot work: ResolveRequestStatus never returns `rate_limited`.
func TestEntryToProcessedRequest_CarriesRequestStatus(t *testing.T) {
	rl := telemetry.RequestStatusRateLimited
	entry := &telemetry.RequestLogEntry{

		RequestID:     "req-1",
		RequestStatus: &rl,
		Success:       false,
	}

	got := entryToProcessedRequest(entry, "sess-1")
	if got == nil {
		t.Fatal("entryToProcessedRequest returned nil for a valid entry")
	}
	if got.RequestStatus != telemetry.RequestStatusRateLimited {
		t.Errorf("RequestStatus = %q, want %q — the mirror dropped the label, "+
			"which is the exact defect audit §9.150.4 records",
			got.RequestStatus, telemetry.RequestStatusRateLimited)
	}
}

// The four states must each survive. A test that only pins `rate_limited`
// would still pass if the field were hard-coded to that one value.
func TestEntryToProcessedRequest_CarriesEveryRequestStatus(t *testing.T) {
	for _, want := range []string{
		telemetry.RequestStatusSuccess,
		telemetry.RequestStatusFailure,
		telemetry.RequestStatusRateLimited,
		telemetry.RequestStatusInProgress,
	} {
		ws := want
		got := entryToProcessedRequest(&telemetry.RequestLogEntry{
			RequestID:     "r-" + want,
			RequestStatus: &ws,
		}, "s")
		if got == nil {
			t.Fatalf("nil ProcessedRequest for status %q", want)
		}
		if got.RequestStatus != want {
			t.Errorf("RequestStatus = %q, want %q", got.RequestStatus, want)
		}
	}
}

// A nil RequestStatus must not panic and must not invent a value: the column
// stays NULL, which is what makes "written after migration 823" the only
// reliable population rule (historical rows cannot be backfilled — the signal
// lived only in request_logs).
func TestEntryToProcessedRequest_NilRequestStatusStaysEmpty(t *testing.T) {
	got := entryToProcessedRequest(&telemetry.RequestLogEntry{

		RequestID: "r",
		Success:   true,
	}, "s")
	if got == nil {
		t.Fatal("nil ProcessedRequest")
	}
	if got.RequestStatus != "" {
		t.Errorf("RequestStatus = %q, want \"\" for a nil entry field — a derived "+
			"guess would silently mislabel historical-style rows", got.RequestStatus)
	}
}

// The mirror already classified rate_limited as terminal via isTerminalFailure
// — this pins that the classification and the new field agree, so a future
// refactor cannot widen one without the other.
func TestIsTerminalFailure_AgreesWithRequestStatusField(t *testing.T) {
	rl := telemetry.RequestStatusRateLimited
	entry := &telemetry.RequestLogEntry{RequestStatus: &rl, Success: false}

	if !isTerminalFailure(entry) {
		t.Fatal("isTerminalFailure(rate_limited) = false, want true")
	}
	proc := entryToProcessedRequest(&telemetry.RequestLogEntry{
		RequestID: "r", RequestStatus: &rl,
	}, "s")
	if proc == nil {
		t.Fatal("nil ProcessedRequest")
	}
	// A turn the mirror calls terminal must carry a status that also reads as
	// terminal, otherwise downstream filters disagree.
	switch proc.RequestStatus {
	case telemetry.RequestStatusFailure, telemetry.RequestStatusRateLimited:
	default:
		t.Errorf("terminal entry carried RequestStatus %q, which isTerminalFailure "+
			"and the persisted field would classify differently", proc.RequestStatus)
	}
}
