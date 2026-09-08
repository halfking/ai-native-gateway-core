package executors

// executor_dispatch_requestflow_test.go covers the three dispatch V2
// request_flow hook points added for audit 2026-09-08 #3:
//
//   - node_switch: bridgeDispatchNotice turns a NoticeKindNodeSwitch /
//     NoticeKindModelSwitch notice into a switch_node request_flow line with
//     the from→to credential endpoints;
//   - preflight_reject: logDispatchPreflightRejection emits the line even when
//     the candidate_failure_logs_hot writer is nil;
//   - stream_interrupted: logDispatchStreamInterrupted carries the
//     interruption reason and resumability from the streamInterruptedError.
//
// The lines are captured through a slog default-handler swap; every hook is a
// fire-and-forget slog write, so no goroutine ordering is involved.

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// captureRequestFlow swaps the default slog handler for a buffer, runs body,
// and returns every request_flow line written during it.
func captureRequestFlow(t *testing.T, body func()) []string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	body()
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, "event=request_flow") {
			lines = append(lines, line)
		}
	}
	return lines
}

func attrValue(line, key string) string {
	for _, part := range strings.Split(line, " ") {
		if v, ok := strings.CutPrefix(part, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestBridgeDispatchNoticeNodeSwitchRequestFlow(t *testing.T) {
	params := &ExecParams{RequestID: "req-node-switch", IsStream: true}
	bridge := bridgeDispatchNotice(params)

	lines := captureRequestFlow(t, func() {
		bridge(dispatch.DispatchNotice{
			Kind:             dispatch.NoticeKindNodeSwitch,
			Message:          "上游请求失败（transient），正在切换到备用节点...",
			ErrorKind:        "transient",
			FromCredentialID: 11,
			ToCredentialID:   22,
			FromModel:        "glm-5",
			ToModel:          "glm-5",
			Vendor:           "zhipu",
			Attempt:          2,
		})
	})
	if len(lines) != 1 {
		t.Fatalf("request_flow lines = %d, want 1: %q", len(lines), lines)
	}
	line := lines[0]
	for key, want := range map[string]string{
		"stage":              "dispatch",
		"request_id":         "req-node-switch",
		"kind":               "node_switch",
		"action":             "switch_node",
		"reason":             "transient",
		"from_credential_id": "11",
		"to_credential_id":   "22",
		"attempt":            "2",
	} {
		if got := attrValue(line, key); got != want {
			t.Fatalf("node_switch line %s = %q, want %q", key, got, want)
		}
	}
}

func TestBridgeDispatchNoticeRetryDoesNotEmitSwitchLine(t *testing.T) {
	params := &ExecParams{RequestID: "req-retry", IsStream: true}
	bridge := bridgeDispatchNotice(params)

	lines := captureRequestFlow(t, func() {
		bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, ErrorKind: "transient"})
		bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindQueued})
	})
	if len(lines) != 0 {
		t.Fatalf("same-cred retry / queued notices must not emit switch_node lines, got %q", lines)
	}
}

func TestLogDispatchPreflightRejectionRequestFlow(t *testing.T) {
	params := &ExecParams{
		R:         httptest.NewRequest("POST", "/v1/chat/completions", nil),
		RequestID: "req-preflight",
		AttemptNo: 1,
	}
	cand := provider.Candidate{
		CredentialID: 33,
		ProviderID:   7,
		RawModel:     "glm-5",
		CatalogCode:  "zhipu",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{cand, cand}}

	// writer == nil must not skip the request_flow line (the hot-table write
	// is skipped, the diagnostic channel is not).
	lines := captureRequestFlow(t, func() {
		logDispatchPreflightRejection(nil, params, cand, dctx, time.Now(),
			errDispatchCircuitOpen, errorsx.KindCircuitOpen,
			map[string]any{"rejection_type": "circuit_breaker"})
	})
	if len(lines) != 1 {
		t.Fatalf("request_flow lines = %d, want 1: %q", len(lines), lines)
	}
	line := lines[0]
	for key, want := range map[string]string{
		"stage":         "preflight",
		"request_id":    "req-preflight",
		"kind":          "circuit_open",
		"action":        "preflight_reject",
		"reason":        "circuit_breaker",
		"credential_id": "33",
	} {
		if got := attrValue(line, key); got != want {
			t.Fatalf("preflight line %s = %q, want %q", key, got, want)
		}
	}
}

func TestLogDispatchStreamInterruptedRequestFlow(t *testing.T) {
	params := &ExecParams{
		R:         httptest.NewRequest("POST", "/v1/chat/completions", nil),
		RequestID: "req-interrupted",
		AttemptNo: 3,
	}
	cand := provider.Candidate{CredentialID: 44, ProviderID: 9, RawModel: "glm-5"}

	lines := captureRequestFlow(t, func() {
		logDispatchStreamInterrupted(params, cand, errorsx.KindStreamTimeout,
			&streamInterruptedError{reason: "upstream_timeout", credentialID: 44, resumable: true})
	})
	if len(lines) != 1 {
		t.Fatalf("request_flow lines = %d, want 1: %q", len(lines), lines)
	}
	line := lines[0]
	for key, want := range map[string]string{
		"stage":      "dispatch",
		"request_id": "req-interrupted",
		"kind":       "stream_timeout",
		"action":     "stream_interrupted",
		"reason":     "upstream_timeout",
		"retryable":  "true",
		"attempt":    "3",
	} {
		if got := attrValue(line, key); got != want {
			t.Fatalf("stream_interrupted line %s = %q, want %q", key, got, want)
		}
	}
}

func TestPreflightRejectionReasonFallsBackToError(t *testing.T) {
	rejErr := errors.New("dispatch: fp slot saturated")
	if got := preflightRejectionReason(map[string]any{}, rejErr); got != rejErr.Error() {
		t.Fatalf("fallback reason = %q, want %q", got, rejErr.Error())
	}
	if got := preflightRejectionReason(map[string]any{"rejection_type": "fp_slot_saturated"}, rejErr); got != "fp_slot_saturated" {
		t.Fatalf("rejection_type reason = %q, want fp_slot_saturated", got)
	}
}
