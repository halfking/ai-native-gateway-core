package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestHandleTriggerAvailability_NoWorker verifies GET /trigger/availability
// reports available=false with the right reason when the legacy worker is
// not initialized (the default in new probe mode since 2026-07-14).
func TestHandleTriggerAvailability_NoWorker(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{} // worker nil

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("availability endpoint must return 200 even when worker is nil (got %d)", rr.Code)
	}
	var body struct {
		Available    bool   `json:"available"`
		NewProbeMode bool   `json:"new_probe_mode"`
		Reason       string `json:"reason"`
		ErrorCode    string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Available {
		t.Fatalf("available should be false when worker is nil")
	}
	if !body.NewProbeMode {
		t.Fatalf("new_probe_mode should mirror LLM_GATEWAY_USE_NEW_PROBE_MODE")
	}
	if body.ErrorCode != "self_check.trigger.no_probe_path" {
		t.Fatalf("unexpected error_code: %q", body.ErrorCode)
	}
	if !strings.Contains(body.Reason, "new probe mode") {
		t.Fatalf("reason should mention new probe mode, got: %q", body.Reason)
	}
}

// TestHandleTriggerAvailability_OldProbeFallback verifies that when
// LLM_GATEWAY_USE_NEW_PROBE_MODE is false but worker is still nil, we
// fall back to the generic "worker_unavailable" error code.
func TestHandleTriggerAvailability_OldProbeFallback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d)", rr.Code)
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.worker_unavailable" {
		t.Fatalf("expected generic worker_unavailable code, got %q", body.ErrorCode)
	}
}

// TestHandleTrigger_NoWorkerReturnsGone verifies POST /trigger returns 410
// (Gone) instead of 503 when the worker is nil. 503 misled the UI into
// thinking it was a transient outage; 410 is the correct semantic for
// "endpoint retired under current probe mode".
func TestHandleTrigger_NoWorkerReturnsGone(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"minimax-m2.7"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.no_probe_path" {
		t.Fatalf("expected no_probe_path, got %q", body.ErrorCode)
	}
	if !strings.Contains(body.Message, "durable probe queue") {
		t.Fatalf("message should mention the missing durable probe queue, got: %q", body.Message)
	}
}

// TestHandleTrigger_ProbeEnqueue (2026-08-18): under the new probe mode the
// trigger now fans out through the durable queue instead of 410-ing. This is
// the glm-5.2 incident follow-up: operators must always have a manual way to
// demand fresh probe evidence.
func TestHandleTrigger_ProbeEnqueue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) {
		if model != "glm-5.2" {
			t.Errorf("unexpected model %q", model)
		}
		return 3, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"glm-5.2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		OK       bool   `json:"ok"`
		Mode     string `json:"mode"`
		Enqueued int    `json:"enqueued"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Mode != "probe_queue" || body.Enqueued != 3 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

// TestHandleTrigger_ProbeEnqueueError reports individual enqueue failures in
// the unified fan-out response envelope. Both single-model and multi-model
// callers receive the same shape: 200 OK with a per-model results map that
// records which models failed and why. The single-model 503 fast-path that
// earlier lived in handleTrigger was removed by f8bf429ec
// (fix(admin): harden batch self-check model selection), so the handler now
// treats a single named model as a one-element fan-out and reports the
// failure inside results[model] while still returning 200.
func TestHandleTrigger_ProbeEnqueueError(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) {
		return 0, errors.New("db down")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"glm-5.2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with per-model failure report (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		OK           bool `json:"ok"`
		Enqueued     int  `json:"enqueued"`
		ModelsFailed int  `json:"models_failed"`
		Results      map[string]struct {
			Error    string `json:"error"`
			Enqueued int    `json:"enqueued"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Enqueued != 0 || body.ModelsFailed != 1 {
		t.Fatalf("unexpected envelope: %+v", body)
	}
	if body.Results["glm-5.2"].Error != "db down" || body.Results["glm-5.2"].Enqueued != 0 {
		t.Fatalf("unexpected per-model result: %+v", body.Results["glm-5.2"])
	}
}

// TestHandleTriggerAvailability_ProbeEnqueue verifies the availability
// endpoint reports available=true with mode=probe_queue once the enqueue
// path is wired (drives the UI button state).
func TestHandleTriggerAvailability_ProbeEnqueue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) { return 0, nil })

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d)", rr.Code)
	}
	var body struct {
		Available bool   `json:"available"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Available || body.Mode != "probe_queue" {
		t.Fatalf("expected available probe_queue mode, got: %+v", body)
	}
}

// TestHandleTrigger_OldProbeFallbackGone verifies that under old probe mode
// (rollback path) with no worker, we still return 410 but with the generic
// worker_unavailable error code so operators know to inspect worker startup.
func TestHandleTrigger_OldProbeFallbackGone(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone (got %d)", rr.Code)
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.worker_unavailable" {
		t.Fatalf("expected generic worker_unavailable, got %q", body.ErrorCode)
	}
}

// TestHandleTrigger_MethodNotAllowed guards against non-POST traffic.
func TestHandleTrigger_MethodNotAllowed(t *testing.T) {
	h := &SelfCheckHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger", nil)
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on POST endpoint, got %d", rr.Code)
	}
}

// TestScNewProbeMode_EnvMatrix locks down the env-parsing contract so future
// refactors in either cmd/gateway/main.go or admin/self_check_handlers.go
// don't drift.
func TestScNewProbeMode_EnvMatrix(t *testing.T) {
	cases := []struct {
		env   string
		unset bool
		want  bool
	}{
		{"", true, true},  // unset → default true
		{"", false, true}, // empty → default true
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", false, false},
		{"0", false, false},
		{"no", false, false},
		{"off", false, false},
		{"random", false, false},
	}
	for _, c := range cases {
		name := c.env
		if c.unset {
			name = "<unset>"
		}
		t.Run(name, func(t *testing.T) {
			if c.unset {
				// best-effort unset for the duration of the test
				old, had := os.LookupEnv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
				os.Unsetenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
				defer func() {
					if had {
						os.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", old)
					}
				}()
			} else {
				t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", c.env)
			}
			if got := scNewProbeMode(); got != c.want {
				t.Fatalf("env=%q want=%v got=%v", c.env, c.want, got)
			}
		})
	}
}
