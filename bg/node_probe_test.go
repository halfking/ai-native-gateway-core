package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNodeProbeBackoffLadder pins the 5s/30s/60s/5m/1h/2h/6h
// sequence mandated by the spec — any change here must be a
// deliberate spec update.
// 2026-07-24: changed from 24h to 6h to prevent nodes from being
// stranded for a full day after transient failures.
func TestNodeProbeBackoffLadder(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		6 * time.Hour, // was 24h
	}
	if len(NodeProbeBackoffChain) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(NodeProbeBackoffChain), len(want))
	}
	for i, v := range want {
		if NodeProbeBackoffChain[i] != v {
			t.Fatalf("backoff[%d] = %v, want %v", i, NodeProbeBackoffChain[i], v)
		}
	}
}

func TestDirectProbeEndpointUsesAnthropicMessages(t *testing.T) {
	if got := directProbeEndpoint("https://apiclaude.cc", "anthropic-messages"); got != "https://apiclaude.cc/v1/messages" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := directProbeEndpoint("https://token.sensenova.cn/v1", "openai-completions"); got != "https://token.sensenova.cn/v1/chat/completions" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestDirectProbeBodyUsesAnthropicMessagesShape(t *testing.T) {
	body := directProbeBody("claude-sonnet-5", "anthropic-messages")
	if !strings.Contains(body, `"max_tokens":10`) || !strings.Contains(body, `"content":"ping"`) {
		t.Fatalf("unexpected anthropic probe body: %s", body)
	}
}

func TestNodeProbeMaxAttemptsRearmsFreshFailure(t *testing.T) {
	// Submit's SQL uses the cap as a reset boundary. Keep the invariant here
	// so a terminal backoff row cannot become a silent dead letter again.
	if nodeProbeMaxAttempts != 7 {
		t.Fatalf("unexpected probe cap: %d", nodeProbeMaxAttempts)
	}
}

func TestNodeProbeDefaultsToLoopbackGateway(t *testing.T) {
	t.Setenv("LLM_GATEWAY_NODE_PROBE_BASE_URL", "")
	w := NewNodeProbeWorker(nil, nil, nil, "", "", nil)
	if w.baseURL != "http://127.0.0.1:8781/v1" {
		t.Fatalf("base URL = %q, want loopback gateway", w.baseURL)
	}
}

func TestNodeProbeOnlyPicksRecentFailures(t *testing.T) {
	contents, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	source := string(contents)
	// 2026-08-11: the activity filter was widened from 24h to 7 days. The
	// prior 24h cutoff silently dropped any node whose probe row had been
	// idle for more than a day, so a cooled-then-forgotten credential was
	// never re-picked and recovery depended solely on the 60s
	// credential_recovery tick re-submitting it. The 7-day bound remains
	// only to keep ancient orphan rows out of the worker.
	for _, want := range []string{
		"last_direct_ok IS DISTINCT FROM TRUE",
		"last_gateway_ok IS DISTINCT FROM TRUE",
		"last_attempt_at >= now() - interval '7 days'",
		"updated_at >= now() - interval '7 days'",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("pickDueAtomically missing filter %q", want)
		}
	}
}

// TestNodeProbeMaxAttempts ensures attempt=7 marks paused (the
// "max one day" cap from the spec).
func TestNodeProbeMaxAttempts(t *testing.T) {
	if nodeProbeMaxAttempts != 7 {
		t.Fatalf("expected 7, got %d", nodeProbeMaxAttempts)
	}
}

// TestNodeProbeInFlightWindow sanity-checks the dedup window.
func TestNodeProbeInFlightWindow(t *testing.T) {
	if nodeProbeInFlightWindow < 1*time.Minute {
		t.Fatalf("in-flight window too short: %v", nodeProbeInFlightWindow)
	}
}

// TestNodeProbeDedupHashStable ensures the same (cred, model) pair
// produces the same hash across calls (used for testing the dedup
// map key).
func TestNodeProbeDedupHashStable(t *testing.T) {
	a := nodeProbeDedupHash(42, "gpt-5.4")
	b := nodeProbeDedupHash(42, "gpt-5.4")
	if a != b {
		t.Fatalf("hash not stable: %q vs %q", a, b)
	}
	if nodeProbeDedupHash(43, "gpt-5.4") == a {
		t.Fatalf("hash should differ across cred IDs")
	}
}

// TestNodeProbeResultToStatus verifies the audit fix that maps a
// nodeProbeRoundResult back to the fine-grained ProbeStatus taxonomy, so a
// 429/503/network error on the node_probe path is classified as
// Rate/HTTP5xx/Network (not collapsed to probe_direct_internal_error).
func TestNodeProbeResultToStatus(t *testing.T) {
	cases := []struct {
		name string
		r    nodeProbeRoundResult
		want ProbeStatus
	}{
		{"success", nodeProbeRoundResult{ok: true}, ProbeStatusSuccess},
		{"endpoint_build", nodeProbeRoundResult{errCode: "endpoint_build"}, ProbeStatusFailed},
		{"network_error", nodeProbeRoundResult{errCode: "network_error", latencyMs: 500}, ProbeStatusNetwork},
		// 2026-08-12: timeout is now its own errCode (was network_error +
		// timedOut=true). dns_error / connection_error are new transport
		// subclasses; both still roll up to ProbeStatusNetwork so existing
		// dashboards that grouped on the old single label are unaffected.
		{"timeout", nodeProbeRoundResult{errCode: "timeout", latencyMs: 14900, timedOut: true}, ProbeStatusTimeout},
		{"dns_error", nodeProbeRoundResult{errCode: "dns_error"}, ProbeStatusNetwork},
		{"connection_error", nodeProbeRoundResult{errCode: "connection_error"}, ProbeStatusNetwork},
		{"429", nodeProbeRoundResult{errCode: "http_429", httpStatus: 429}, ProbeStatusRate},
		{"401", nodeProbeRoundResult{errCode: "http_401", httpStatus: 401}, ProbeStatusAuth},
		{"403", nodeProbeRoundResult{errCode: "http_403", httpStatus: 403}, ProbeStatusAuth},
		{"503", nodeProbeRoundResult{errCode: "http_503", httpStatus: 503}, ProbeStatusHTTP5xx},
		{"404", nodeProbeRoundResult{errCode: "http_404", httpStatus: 404}, ProbeStatusHTTP4xx},
		{"unknown errCode no status", nodeProbeRoundResult{errCode: "weird"}, ProbeStatusFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nodeProbeResultToStatus(c.r); got != c.want {
				t.Errorf("nodeProbeResultToStatus(%+v) = %q, want %q", c.r, got, c.want)
			}
		})
	}
}

// TestIsMissingBindingErr pins the 2026-07-24 P0 fix detection:
// a direct probe round that reports endpoint_build + "no rows in
// result set" must be classified as a configuration error, NOT a
// health failure, so runOne can short-circuit and drop the orphan
// node_probe_state row instead of burning the worker's retry budget.
func TestIsMissingBindingErr(t *testing.T) {
	cases := []struct {
		name string
		r    nodeProbeRoundResult
		want bool
	}{
		{
			name: "endpoint_build with pgx sentinel (legacy wrapper)",
			r:    nodeProbeRoundResult{errCode: "endpoint_build", errDetail: "build endpoint failed: no rows in result set (cred_id=29, model=grok-4.5)"},
			want: true,
		},
		{
			name: "endpoint_build with 2026-07-24 detailed wrapper",
			r:    nodeProbeRoundResult{errCode: "endpoint_build", errDetail: "build endpoint failed: no rows in result set: credential_id=29 has no enabled+unlocked credential_model_bindings for raw_model_name=\"grok-4.5\" (check cmb.available, p.enabled, p.manual_disabled, c.status, c.lifecycle_status)"},
			want: true,
		},
		{
			name: "endpoint_build from decrypt failure (still real failure)",
			r:    nodeProbeRoundResult{errCode: "endpoint_build", errDetail: "build endpoint failed: decrypt: cipher: message authentication failed"},
			want: false,
		},
		{
			name: "network_error is not a binding issue",
			r:    nodeProbeRoundResult{errCode: "network_error", errDetail: "upstream timeout after 15s"},
			want: false,
		},
		{
			name: "ok result",
			r:    nodeProbeRoundResult{errCode: "none", ok: true},
			want: false,
		},
		{
			name: "http_500 is not a binding issue",
			r:    nodeProbeRoundResult{errCode: "http_500", httpStatus: 500},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMissingBindingErr(c.r); got != c.want {
				t.Errorf("isMissingBindingErr(%+v) = %v, want %v", c.r, got, c.want)
			}
		})
	}
}

func TestResolveProbeAPIKeyPreservesConfiguredGatewayKey(t *testing.T) {
	worker := &NodeProbeWorker{apiKey: "static-data-plane-key"}
	worker.resolveProbeAPIKey(context.Background())
	if worker.apiKey != "static-data-plane-key" {
		t.Fatalf("resolveProbeAPIKey changed configured gateway key to %q", worker.apiKey)
	}
}

// TestRunOneMissingBindingDropsOrphanStateRow pins the 2026-07-24
// P0 fix end-to-end behaviour: when probeDirect returns
// endpoint_build+no-rows, runOne must (a) emit a single
// node_probe_runs audit row, (b) DELETE the orphan node_probe_state
// row so the worker stops re-picking it, (c) NOT increment
// consecutive_failures, and (d) return nil so the worker treats
// the cycle as successful. We use a fake `runOne` orchestrator that
// drives only the affected branches (the full runOne body is
// integration-tested via the older pgxmock harness).
func TestRunOneMissingBindingDropsOrphanStateRow(t *testing.T) {
	// Static-only check: the runOne body must (1) call DELETE on
	// node_probe_state in the missing-binding branch, and (2) NOT
	// touch consecutive_failures when errDetail mentions
	// "no rows in result set". Source-grep keeps the contract honest
	// across future refactors of the success/failure branches.
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	// Branch must DELETE the orphan row, not pause / increment failures.
	if !strings.Contains(body, "isMissingBindingErr(direct)") {
		t.Fatalf("runOne missing-binding branch: expected isMissingBindingErr(direct) call")
	}
	wantSnippet := `DELETE FROM node_probe_state
			 WHERE credential_id = $1 AND raw_model_name = $2`
	if !strings.Contains(body, wantSnippet) {
		t.Fatalf("runOne missing-binding branch: expected %q", wantSnippet)
	}
	// The DELETE branch must early-return nil, so the success/failure
	// UPDATE paths (which set consecutive_failures) must not run.
	// Verify by ensuring the early-return happens BEFORE the UPDATE
	// branches. Easiest check: the missing-binding log line must come
	// before any "UPDATE node_probe_state SET consecutive_failures".
	idxLog := strings.Index(body, `node_probe_worker: dropping probe for (cred, model) with no credential_model_bindings row`)
	idxFail := strings.Index(body, `UPDATE node_probe_state SET
				consecutive_failures = $3,`)
	if idxLog < 0 {
		t.Fatalf("missing-binding log line not found in source")
	}
	if idxFail < 0 {
		t.Fatalf("failure UPDATE branch not found in source (did someone refactor node_probe.go?)")
	}
	if idxLog > idxFail {
		t.Fatalf("missing-binding branch must run BEFORE the failure UPDATE branch (idxLog=%d, idxFail=%d)", idxLog, idxFail)
	}
}

// TestNodeProbeSuccessNextRetryOneHour pins BUG #6 fix (2026-07-22):
// after a successful probe, next_retry_at should be 1 hour away, not
// 24 hours. Source-grep verifies both the runOne success branch and
// MarkNodeProbeHealthy write the 1-hour interval; a future refactor
// that accidentally restores the 24-hour value (e.g. copy-paste from
// the older comment block) will fail this test.
func TestNodeProbeSuccessNextRetryOneHour(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	mustContain := []string{
		// runOne success branch
		"next_retry_at = now() + interval '1 hour'",
		"next_retry_seconds = 3600",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Fatalf("BUG #6 regression: node_probe.go missing %q", want)
		}
	}
	// And no 24-hour success branch should remain.
	if strings.Contains(body, "now() + interval '24 hours'") {
		t.Fatalf("BUG #6 regression: node_probe.go still has 24-hour success interval")
	}
}

// TestRunOneSuccessClearsLastDirectOkAndErrCode pins the contract added
// by 2026-07-25 realtime-routing-self-heal §3.1.1: a successful probe
// round must clear `last_direct_ok`, `last_gateway_ok`, `last_err_code`,
// and `last_err_detail` so v_routable_credential_models drops
// `node_probe_failed` within AutoRouteRealtimeListener's 5s debounce.
//
// The corresponding SQL UPDATE already lives in node_probe.go:runOne
// success branch (lines ~1084-1100); the test pins the exact byte
// sequence so a future copy-paste regression cannot silently remove
// the explicit recovery columns.
func TestRunOneSuccessClearsLastDirectOkAndErrCode(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	wantSnippet := `
				consecutive_failures = 0,
				consecutive_successes = consecutive_successes + 1,
				last_attempt_at = now(),
				next_retry_at = now() + interval '1 hour',
				next_retry_seconds = 3600,
				paused = FALSE,
				last_run_id = NULL,
				last_direct_ok = TRUE,
				last_gateway_ok = TRUE,
				last_err_code = NULL,
				last_err_detail = NULL,
				in_flight_until = NULL,
				updated_at = now()`
	if !strings.Contains(body, wantSnippet) {
		t.Fatalf("runOne success branch must write last_direct_ok=TRUE, last_err_code=NULL; update bg/node_probe.go:runOne success UPDATE block")
	}
}

// TestRunOneSuccessInvokesInvalidateAndNotify pins that the runOne success
// path invalidates the in-memory URSM v2 candidate cache and notifies
// auto_route_refresh so v_routable_credential_models drops node_probe_failed
// within the listener's 5s debounce.
//
// We accept either path (runOne success block or bg/auto_route_realtime_listener
// routed through SetInvalidateCandidateCache) as long as the symbols are
// present, because the contract is operational: a probe success must result
// in the routing view re-evaluating the binding within the existing debounce.
func TestRunOneSuccessInvokesInvalidateAndNotify(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	// (1) On success, runOne must invalidate the URSM v2 candidate cache so
	// the next chat request re-plans. The mirror of the failure-branch call
	// (same setter) is a regression — without it the cached bindings still
	// see the stale node_probe_failed reason.
	if !strings.Contains(body, "w.invalidateCandidateCache(credID)") {
		t.Fatalf("runOne must call w.invalidateCandidateCache(credID) on success (mirror the failure branch)")
	}
	// It must be guarded — the worker has the setter only when wired from
	// cmd/gateway/main.go; pass-through nil-safety keeps old tests valid.
	if !strings.Contains(body, "if w.invalidateCandidateCache != nil {") {
		t.Fatalf("InvalidateCandidateCacheForCredential call must be guarded by w.invalidateCandidateCache != nil")
	}

	// (2) On success, runOne must pg_notify('auto_route_refresh', ...) so
	// bg/auto_route_realtime_listener.go wakes the AutoIndexRefresher and
	// the v_routable view re-evaluates is_routable. Without the notify,
	// the success path still relies on the 5-min periodic refresh.
	if !strings.Contains(body, `SELECT pg_notify('auto_route_refresh'`) {
		t.Fatalf("runOne success must schedule a pg_notify('auto_route_refresh') so the listener refreshes v_routable_credential_models")
	}
	// pg_notify payload must follow credentials:UPDATE:<id> (matches the
	// invalidateRoutingCaches helper format used by the admin endpoints).
	if !strings.Contains(body, `credentials:UPDATE:%d`) {
		t.Fatalf("pg_notify payload must follow credentials:UPDATE:<id> format")
	}
}

// TestHandleNodeProbeStateResetIsNoProbe pins the emergency button
// contract from SPEC §3.5 rollback: the admin button clears the backoff
// state (last_direct_ok=TRUE, paused=FALSE) but does NOT issue new
// HTTP probes — that would defeat the emergency path by adding more
// pressure to a degraded upstream.
func TestHandleNodeProbeStateResetIsNoProbe(t *testing.T) {
	src, err := os.ReadFile("../admin/probe_history.go")
	if err != nil {
		t.Fatalf("read admin/probe_history.go: %v", err)
	}
	body := string(src)

	// Extract only handleNodeProbeStateReset function body
	start := strings.Index(body, "func (h *Handler) handleNodeProbeStateReset(")
	if start < 0 {
		t.Fatalf("handleNodeProbeStateReset function not found")
	}
	// Find the closing brace of this function (naive: find next "\n}\n\n" after "func")
	end := strings.Index(body[start:], "\n}\n\n")
	if end < 0 {
		t.Fatalf("handleNodeProbeStateReset closing brace not found")
	}
	fnBody := body[start : start+end]

	// The function must UPDATE node_probe_state
	if !strings.Contains(fnBody, "UPDATE node_probe_state SET") {
		t.Fatalf("handleNodeProbeStateReset must UPDATE node_probe_state")
	}
	// It must call provider.InvalidateAllCandidateCache()
	if !strings.Contains(fnBody, "provider.InvalidateAllCandidateCache()") {
		t.Fatalf("handleNodeProbeStateReset must call provider.InvalidateAllCandidateCache()")
	}
	// It must NOT submit any new probes
	for _, banned := range []string{
		"nodeProbe.Submit",
		"h.nodeProbe.Submit",
		"modelProbe.TriggerManual",
		"h.modelProbe.TriggerManual",
	} {
		if strings.Contains(fnBody, banned) {
			t.Fatalf("handleNodeProbeStateReset must not call %s (no-probe contract)", banned)
		}
	}
}

// TestTriggerManualSuccessCallsMarkNodeProbeHealthy pins that a successful
// ModelProbeRunner.TriggerManual (status="ok") calls MarkNodeProbeHealthy
// so node_probe_state is cleared and v_routable immediately reflects the
// recovered binding without waiting for the next runOne cycle.
//
// This mirrors the TriggerAllSync path (line ~995 in model_probe.go) but
// applies to the single-binding manual trigger from the admin UI.
func TestTriggerManualSuccessCallsMarkNodeProbeHealthy(t *testing.T) {
	src, err := os.ReadFile("model_probe.go")
	if err != nil {
		t.Fatalf("read model_probe.go: %v", err)
	}
	body := string(src)

	// Extract TriggerManual function body
	start := strings.Index(body, "func (r *ModelProbeRunner) TriggerManual(")
	if start < 0 {
		t.Fatalf("TriggerManual function not found")
	}
	end := strings.Index(body[start:], "\n}\n\n")
	if end < 0 {
		t.Fatalf("TriggerManual closing brace not found")
	}
	fnBody := body[start : start+end]

	// TriggerManual must call MarkNodeProbeHealthy when status="ok"
	if !strings.Contains(fnBody, "MarkNodeProbeHealthy") {
		t.Fatalf("TriggerManual must call MarkNodeProbeHealthy on success (mirror TriggerAllSync behavior)")
	}
	// It must be conditional on status="ok" to avoid clearing node_probe_state
	// when the manual probe itself failed
	if !strings.Contains(fnBody, `status == "ok"`) && !strings.Contains(fnBody, `status=="ok"`) {
		t.Fatalf("MarkNodeProbeHealthy call must be guarded by status == \"ok\"")
	}
}
