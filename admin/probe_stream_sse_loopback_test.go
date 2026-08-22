package admin

import "testing"

// TestProbeSSEHub_ShouldSkipNotification pins the 2026-08-12 audit fix that
// prevents the hub from double-delivering its own Pub/Sub notifications to
// SSE clients. Without this filter, every transition reached the dashboard
// twice: once from Publish's local fanOut, once from the Redis subscriber
// echoing the same payload back.
func TestProbeSSEHub_ShouldSkipNotification(t *testing.T) {
	hub := &ProbeSSEHub{instanceID: "p-aabbccdd"}

	// Same-instance payload: must be skipped.
	if !hub.shouldSkipNotification(
		`{"task_id":"node_probe:7:gpt-5.6","source":"node_probe","status":"ok","instance_id":"p-aabbccdd"}`,
	) {
		t.Fatalf("same-instance payload must be skipped")
	}

	// Different-instance payload: must NOT be skipped (the cross-instance
	// forwarding path is the entire reason Pub/Sub exists).
	if hub.shouldSkipNotification(
		`{"task_id":"node_probe:7:gpt-5.6","source":"node_probe","status":"ok","instance_id":"p-other"}`,
	) {
		t.Fatalf("cross-instance payload must NOT be skipped")
	}

	// Legacy payload without instance_id (older versions): not skipped —
	// preserves forward compatibility so a single-instance deploy with
	// old notify shape still receives cross-process events.
	if hub.shouldSkipNotification(
		`{"task_id":"node_probe:7:gpt-5.6","source":"node_probe","status":"ok"}`,
	) {
		t.Fatalf("legacy payload (no instance_id) must NOT be skipped")
	}

	// Empty hub instanceID: filter is permissive (returns false) so an
	// unconfigured hub still forwards everything.
	hub2 := &ProbeSSEHub{}
	if hub2.shouldSkipNotification(
		`{"task_id":"x","source":"node_probe","status":"ok","instance_id":"p-anything"}`,
	) {
		t.Fatalf("hub with empty instanceID must not skip anything")
	}
}
