package integrity

import "context"

// NotifyRecorder wraps an inner Recorder and fires Notify for every Event
// that would be persisted — **before** sampling — so high-signal anomalies
// (model_mismatch / fingerprint_drift) still drive side effects even when
// the PoolRecorder drops the row under sampleRatio.
//
// Notify MUST be non-blocking (schedule work; never wait on I/O). A slow
// Notify would stall the request hot path that called Detector.Observe.
type NotifyRecorder struct {
	Inner  Recorder
	Notify func(Event)
}

// Record implements Recorder. Nil-safe.
func (n *NotifyRecorder) Record(ctx context.Context, ev Event) error {
	if n == nil {
		return nil
	}
	if n.Notify != nil && CredibilityAutoTriggerWorthy(ev) {
		n.Notify(ev)
	}
	if n.Inner == nil {
		return nil
	}
	return n.Inner.Record(ctx, ev)
}

// CredibilityAutoTriggerWorthy is the shared predicate for "this integrity
// signal should enqueue an independent model-credibility ScanNode".
// High/critical model_mismatch and fingerprint_drift only — other anomaly
// types (finish_*, empty_response, …) do not imply watering.
func CredibilityAutoTriggerWorthy(ev Event) bool {
	if ev.Severity != SeverityHigh && ev.Severity != SeverityCritical {
		return false
	}
	switch ev.AnomalyType {
	case AnomalyModelMismatch, AnomalyFingerprintDrift:
		return true
	default:
		return false
	}
}
