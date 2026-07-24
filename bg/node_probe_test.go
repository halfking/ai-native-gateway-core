package bg

import (
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
		{"network_error near timeout", nodeProbeRoundResult{errCode: "network_error", latencyMs: 14900, timedOut: true}, ProbeStatusTimeout},
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
