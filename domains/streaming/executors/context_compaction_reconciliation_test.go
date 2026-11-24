package executors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type compactionAttemptEvidence struct {
	attempt         reconciliation.Attempt
	status          int
	providerRequest string
	outcome         string
	usage           reconciliation.UsageEvidence
}

type compactionAttemptStore struct {
	rows map[string]*compactionAttemptEvidence
}

func (s *compactionAttemptStore) Prepare(_ context.Context, attempt reconciliation.Attempt) error {
	s.rows[attempt.ID] = &compactionAttemptEvidence{attempt: attempt}
	return nil
}

func (*compactionAttemptStore) MarkDispatched(context.Context, string) error      { return nil }
func (*compactionAttemptStore) MarkNotSent(context.Context, string) error         { return nil }
func (*compactionAttemptStore) MarkSent(context.Context, string, time.Time) error { return nil }
func (s *compactionAttemptStore) RecordHTTP(_ context.Context, id string, status int, providerRequest string, _ time.Time) error {
	s.rows[id].status = status
	s.rows[id].providerRequest = providerRequest
	return nil
}

func (s *compactionAttemptStore) RecordUsage(_ context.Context, id string, usage reconciliation.UsageEvidence) error {
	s.rows[id].usage = usage
	return nil
}

func (s *compactionAttemptStore) Finish(_ context.Context, id, outcome string) error {
	s.rows[id].outcome = outcome
	return nil
}

func TestContextCompactionRecordsUsageOnEachAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected compaction request: %s %s", r.Method, r.URL.Path)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"id":"failed-compaction","error":{"message":"Unavailable"},"usage":{"prompt_tokens":5,"completion_tokens":0}}`)
			return
		}
		w.Header().Set("OpenAI-Request-ID", "successful-compaction")
		_, _ = fmt.Fprint(w, `{"id":"completion-id","choices":[{"message":{"content":"summary"}}],"usage":{"prompt_tokens":9,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`)
	}))
	defer server.Close()
	client := upstream.NewWithRetries(1)
	defer client.Stop()
	store := &compactionAttemptStore{rows: make(map[string]*compactionAttemptEvidence)}
	executor := &Executor{Upstream: client, AttemptRecorder: store}
	params := &ExecParams{TenantID: "tenant-test", RequestID: "gateway-request", GWTaskID: "task-test", PlatformOrigin: "self_check"}
	candidate := provider.Candidate{BaseURL: server.URL, Protocol: "openai-completions", RawModel: "compact", ProviderID: 17, CredentialID: 32}
	summary, err := executor.invokeCompactionSummarize(t.Context(), params, candidate, "fixture input")
	if err != nil || summary != "summary" || len(store.rows) != 2 || calls.Load() != 2 {
		t.Fatalf("summary=%q err=%v sends=%d ledger=%d", summary, err, calls.Load(), len(store.rows))
	}
	want := map[string]struct {
		status, attemptNo int
		input, output     int64
		outcome, source   string
	}{
		"failed-compaction":     {http.StatusServiceUnavailable, 1, 5, 0, "failure", "gateway_executor:self_check:context_compaction"},
		"successful-compaction": {http.StatusOK, 2, 9, 2, "success", "gateway_executor:self_check:context_compaction:internal_retry"},
	}
	for id, row := range store.rows {
		attempt := row.attempt
		expected, ok := want[row.providerRequest]
		if !ok || attempt.ID != id || attempt.RequestID != "gateway-request" || attempt.TenantID != "tenant-test" ||
			attempt.JobID != "task-test" || attempt.TrafficClass != "platform" || attempt.ClientID != "platform-automation" ||
			attempt.ProviderID != 17 || attempt.CredentialID != 32 || attempt.Model != "compact" ||
			attempt.AttemptNo != expected.attemptNo || attempt.Source != expected.source || row.status != expected.status || row.outcome != expected.outcome ||
			row.usage.InputTokens == nil || *row.usage.InputTokens != expected.input || row.usage.OutputTokens == nil || *row.usage.OutputTokens != expected.output {
			t.Fatalf("unexpected compaction evidence: %+v", row)
		}
		if expected.outcome == "success" && (row.usage.CacheReadTokens == nil || *row.usage.CacheReadTokens != 3) {
			t.Fatalf("missing cached usage: %+v", row.usage)
		}
		delete(want, row.providerRequest)
	}
}

func TestCompactionResponseLedgerRequiresCompleteEvidence(t *testing.T) {
	const body = `{"id":"generated-summary","content":[{"type":"text","text":"summary"}],"usage":{"cost":0.002}}`
	for _, tc := range []struct {
		name, outcome string
		complete      bool
	}{
		{"complete", "success", true},
		{"interrupted", "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := reconciliation.NewAttempt(reconciliation.Attempt{})
			store := &compactionAttemptStore{rows: map[string]*compactionAttemptEvidence{attempt.ID: {attempt: attempt}}}
			executor := &Executor{AttemptRecorder: store}
			var reader io.Reader = strings.NewReader(body)
			if !tc.complete {
				reader = io.MultiReader(reader, iotest.ErrReader(io.ErrUnexpectedEOF))
			}
			request := httptest.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/messages", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reader), Request: request}
			summary, err := executor.consumeCompactionResponse(resp, attempt, "anthropic", "anthropic-messages", decodeAnthropicCompaction)
			row := store.rows[attempt.ID]
			if err != nil || summary != "summary" || row.outcome != tc.outcome {
				t.Fatalf("summary=%q err=%v outcome=%q", summary, err, row.outcome)
			}
			if tc.complete {
				if row.providerRequest != "generated-summary" || row.usage.ReportedAmount == nil || *row.usage.ReportedAmount != "0.002" || row.usage.ReportedCurrency != "USD" {
					t.Fatalf("complete monetary evidence was lost: %+v", row)
				}
			} else if row.providerRequest != "" || row.usage.ReportedAmount != nil {
				t.Fatalf("partial response became usable evidence: %+v", row)
			}
		})
	}
}

func TestCompactionHTTPRejectionKeepsObservedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html>not a summary</html>")
	}))
	defer server.Close()
	client := upstream.NewWithRetries(0)
	defer client.Stop()
	store := &compactionAttemptStore{rows: make(map[string]*compactionAttemptEvidence)}
	executor := &Executor{Upstream: client, AttemptRecorder: store}
	candidate := provider.Candidate{BaseURL: server.URL, Protocol: "anthropic-messages"}
	_, err := executor.invokeCompactionSummarize(t.Context(), nil, candidate, "fixture input")
	if err == nil || len(store.rows) != 1 {
		t.Fatalf("err=%v attempts=%d", err, len(store.rows))
	}
	for _, row := range store.rows {
		if row.status != http.StatusOK || row.outcome != "failure" || row.usage.Source != "" {
			t.Fatalf("unexpected rejected response evidence: %+v", row)
		}
	}
}
