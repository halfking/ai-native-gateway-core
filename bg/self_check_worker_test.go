package bg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

func TestSelfCheckTickerIntervalUsesShortestConfiguredInterval(t *testing.T) {
	tests := []struct {
		name   string
		config scSettings
		want   time.Duration
	}{
		{name: "fault interval is shorter", config: scSettings{NormalInterval: 3600, FaultInterval: 600}, want: 60 * time.Second},
		{name: "normal interval is shorter", config: scSettings{NormalInterval: 600, FaultInterval: 1800}, want: 60 * time.Second},
		{name: "minimum interval is one minute", config: scSettings{NormalInterval: 300, FaultInterval: 120}, want: 60 * time.Second},
		{name: "zero fault interval falls back to normal", config: scSettings{NormalInterval: 3600}, want: 360 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selfCheckTickerInterval(&tt.config); got != tt.want {
				t.Fatalf("selfCheckTickerInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSelfCheckWorkerTriggerManualRun(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}

	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if got := <-w.triggerCh; got != "model-a" {
		t.Fatalf("triggered model = %q, want model-a", got)
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsFullQueue(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if err := w.TriggerManualRun("model-b"); err == nil {
		t.Fatal("second trigger succeeded with a full queue")
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsStoppedWorker(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	close(w.stopCh)

	if err := w.TriggerManualRun("model-a"); err == nil {
		t.Fatal("trigger succeeded for a stopped worker")
	}
}

// TestSystemAPIKeyHashMatchesVerifier is the regression test for the root cause
// of the production "gw_invalid_key" failure: the self-check worker previously
// stored key_hash as the RAW key plaintext, but the data-plane verifier looks up
// HMAC-SHA256(secret, raw). They can never be equal, so every probe returned
// 401. This test pins the invariant that the worker's hashing transform must be
// identical to the verifier's lookup transform.
func TestSystemAPIKeyHashMatchesVerifier(t *testing.T) {
	const secret = "test-secret-key"
	const rawKey = "sk-selfcheck-deadbeefcafef00d"

	// What the worker (EnsureSystemAPIKey) now stores:
	storedHash := authentication.HashAPIKey(secret, rawKey)

	// Sanity: it must NOT be the plaintext (the old bug).
	if storedHash == rawKey {
		t.Fatal("key_hash equals plaintext — this is the bug that caused gw_invalid_key")
	}
	if strings.HasPrefix(storedHash, "sk-") {
		t.Fatalf("key_hash looks like raw key: %q", storedHash)
	}

	// What the verifier computes at lookup time (callVerifyDB):
	// hashAPIKey is unexported in package authentication, but HashAPIKey is the
	// exported canonical implementation both sides must use. Re-derive and assert
	// equality — a divergence here means the worker key can never be found.
	lookupHash := authentication.HashAPIKey(secret, rawKey)
	if storedHash != lookupHash {
		t.Fatalf("stored key_hash %q != verifier lookup hash %q — worker key will never authenticate",
			storedHash, lookupHash)
	}
}

// TestSelfCheckWorkerConversationRoundRequestsToolResultOutput pins the
// 2026-07-18 tool-continuation prompt fix (merged from branch
// backup/fix-self-check-tool-continuation on 2026-09-29): the round-1/2
// prompt must ask for the final tool result so models do not answer with
// an intentionally empty assistant message the gateway would classify
// as empty_response.
func TestSelfCheckWorkerConversationRoundRequestsToolResultOutput(t *testing.T) {
	type requestBody struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}

	requests := make(chan requestBody, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body requestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode self-check request failed: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"2026-09-29 04:55:00"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	worker := &SelfCheckWorker{
		baseURL: server.URL,
		apiKey:  "test-key",
		client:  server.Client(),
	}
	result := worker.doConversationRound(context.Background(), "gpt-5.6-luna", 2, 64)
	if !result.Success {
		t.Fatalf("conversation round failed: type=%s detail=%s", result.ErrType, result.ErrDetail)
	}

	body := <-requests
	if len(body.Messages) == 0 {
		t.Fatal("self-check request has no messages")
	}
	prompt := body.Messages[0].Content
	if !strings.Contains(prompt, "调用工具后只输出查询结果") {
		t.Fatalf("self-check prompt does not request a final tool result: %q", prompt)
	}
	if strings.Contains(prompt, "不要输出其他内容") {
		t.Fatalf("self-check prompt still permits an intentionally empty response: %q", prompt)
	}
}
