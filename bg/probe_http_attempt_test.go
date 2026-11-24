package bg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

type attemptStoreProbeFake struct {
	attempts                             []reconciliation.Attempt
	dispatched, sent, httpRows, finished int
	httpEvidence                         map[string]probeHTTPObservation
	outcomes                             map[string]string
	usage                                map[string]reconciliation.UsageEvidence
}

type probeHTTPObservation struct {
	status int
	id     string
}

func (f *attemptStoreProbeFake) Prepare(_ context.Context, a reconciliation.Attempt) error {
	f.attempts = append(f.attempts, a)
	return nil
}
func (f *attemptStoreProbeFake) MarkDispatched(context.Context, string) error {
	f.dispatched++
	return nil
}
func (f *attemptStoreProbeFake) MarkNotSent(context.Context, string) error { return nil }
func (f *attemptStoreProbeFake) MarkSent(context.Context, string, time.Time) error {
	f.sent++
	return nil
}
func (f *attemptStoreProbeFake) RecordHTTP(_ context.Context, id string, status int, requestID string, _ time.Time) error {
	f.httpRows++
	if f.httpEvidence == nil {
		f.httpEvidence = make(map[string]probeHTTPObservation)
	}
	f.httpEvidence[id] = probeHTTPObservation{status: status, id: requestID}
	return nil
}
func (f *attemptStoreProbeFake) RecordUsage(_ context.Context, id string, usage reconciliation.UsageEvidence) error {
	if f.usage == nil {
		f.usage = make(map[string]reconciliation.UsageEvidence)
	}
	f.usage[id] = usage
	return nil
}
func (f *attemptStoreProbeFake) Finish(_ context.Context, id, outcome string) error {
	f.finished++
	if f.outcomes == nil {
		f.outcomes = make(map[string]string)
	}
	f.outcomes[id] = outcome
	return nil
}

type probeRoundTripper func(*http.Request) (*http.Response, error)

func (f probeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPlatformProbeDoRecordsTrustedPlatformAttempt(t *testing.T) {
	fake := &attemptStoreProbeFake{}
	SetAttemptRecorder(fake)
	defer SetAttemptRecorder(nil)
	const body = `{"id":"completion-id","usage":{"prompt_tokens":4,"completion_tokens":0,"cost":0.003}}`
	client := &http.Client{Transport: probeRoundTripper(func(req *http.Request) (*http.Response, error) {
		headers := make(http.Header)
		headers.Set("OpenAI-Request-Id", "vendor-request")
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req, ContentLength: int64(len(body))}, nil
	})}
	ctx := withPlatformProbeProvider(t.Context(), 7)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := platformProbeDo(ctx, client, req, 42, "model-a", "probe-test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(body))))
	if err != nil || string(got) != body {
		t.Fatalf("probe body=%q err=%v", got, err)
	}
	if len(fake.attempts) != 1 || fake.attempts[0].TrafficClass != "platform" || fake.attempts[0].ClientID != "platform-automation" ||
		fake.attempts[0].TenantID != "" || fake.attempts[0].CredentialID != 42 || fake.attempts[0].ProviderID != 7 {
		t.Fatalf("attempt=%+v", fake.attempts)
	}
	if fake.dispatched != 1 || fake.httpRows != 1 || fake.finished != 1 {
		t.Fatalf("persist transitions: %+v", fake)
	}
	id := fake.attempts[0].ID
	usage := fake.usage[id]
	if fake.httpEvidence[id] != (probeHTTPObservation{http.StatusOK, "vendor-request"}) || fake.outcomes[id] != "success" ||
		usage.InputTokens == nil || *usage.InputTokens != 4 || usage.OutputTokens == nil || *usage.OutputTokens != 0 ||
		usage.ReportedAmount == nil || *usage.ReportedAmount != "0.003" || usage.ReportedCurrency != "USD" {
		t.Fatalf("platform response evidence=%+v usage=%+v", fake.httpEvidence[id], usage)
	}
}

func TestPlatformProbeRedirectSeparatesAttemptEvidence(t *testing.T) {
	const body = `{"id":"redirect-target","usage":{"prompt_tokens":5}}`
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("redirect target method=%s", r.Method)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer target.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "redirect-origin")
		http.Redirect(w, r, target.URL+"/next", http.StatusTemporaryRedirect)
	}))
	defer entry.Close()
	fake := &attemptStoreProbeFake{}
	SetAttemptRecorder(fake)
	defer SetAttemptRecorder(nil)
	ctx := withPlatformProbeProvider(t.Context(), 7)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL+"/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := platformProbeDo(ctx, &http.Client{Timeout: 3 * time.Second}, req, 42, "model-a", "probe-test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != body || len(fake.attempts) != 2 {
		t.Fatalf("body=%q err=%v attempts=%+v", got, err, fake.attempts)
	}
	first, last := fake.attempts[0], fake.attempts[1]
	if first.AttemptNo != 1 || first.CredentialID != 42 || first.ProviderID != 7 || first.Model != "model-a" ||
		fake.httpEvidence[first.ID] != (probeHTTPObservation{http.StatusTemporaryRedirect, "redirect-origin"}) ||
		fake.outcomes[first.ID] != "unknown" || fake.usage[first.ID].Source != "" {
		t.Fatalf("first hop=%+v response=%+v outcome=%s", first, fake.httpEvidence[first.ID], fake.outcomes[first.ID])
	}
	usage := fake.usage[last.ID]
	if last.AttemptNo != 2 || last.ProviderID != 0 || last.CredentialID != 0 || last.Model != "" || last.Source != "http_redirect:probe-test" ||
		last.TrafficClass != "platform" || last.ClientID != "platform-automation" || fake.outcomes[last.ID] != "success" ||
		fake.httpEvidence[last.ID] != (probeHTTPObservation{http.StatusOK, "redirect-target"}) || usage.InputTokens == nil || *usage.InputTokens != 5 {
		t.Fatalf("last hop=%+v response=%+v usage=%+v", last, fake.httpEvidence[last.ID], usage)
	}
}

func TestProbeEvidenceBodyLeavesIncompleteResponsesMissing(t *testing.T) {
	const body = `{"usage":{"prompt_tokens":5}}`
	for _, tc := range []struct {
		name  string
		read  io.Reader
		size  int64
		limit int64
	}{
		{"reader stopped", strings.NewReader(body + " suffix"), -1, int64(len(body))},
		{"short framed body", strings.NewReader(body), int64(len(body) + 1), int64(len(body) + 1)},
		{"read failed", io.MultiReader(strings.NewReader(body), iotest.ErrReader(io.ErrUnexpectedEOF)), -1, int64(len(body) + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := &probeEvidenceBody{ReadCloser: io.NopCloser(tc.read), length: tc.size, complete: func([]byte) {
				t.Error("incomplete response used as evidence")
			}}
			got, err := io.ReadAll(io.LimitReader(observed, tc.limit))
			if string(got) != body || (err != nil && tc.name != "read failed") || (err == nil && tc.name == "read failed") {
				t.Fatalf("observed body=%q err=%v", got, err)
			}
		})
	}
}

func TestPlatformProbeWorkersPreserveCredentialBinding(t *testing.T) {
	const providerID, credID = 71, 42
	const rawModel, outbound = "invoice-model", "wire-alias"
	const chat = `{"id":"chat-probe","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`
	for _, tc := range []struct {
		name  string
		model string
		sends int
		run   func(*testing.T)
	}{
		{"model巡检", rawModel, 1, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, chat)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			target := probeTarget{CredentialID: credID, ProviderID: providerID, RawModel: rawModel, BaseURL: server.URL}
			if result := probeWithRetry(ctx, probeDescriptorFor("openai-completions"), target, ProbeModeChatPing); result.status != "ok" {
				t.Fatalf("model probe result=%+v", result)
			}
		}},
		{"能力回填", rawModel, 1, func(t *testing.T) {
			h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, realCodexResponsesOKBody)
			}, "openai-responses")
			h.backfill.scan = func(context.Context) ([]dueBinding, error) {
				return []dueBinding{admitAll(func(row *dueBinding) {
					row.BindingID, row.ProviderID, row.CredentialID = 4242, providerID, credID
					row.Ciphertext, row.BaseURL, row.Protocol = h.ciphertext, h.baseURL, "openai-responses"
					row.RawModel, row.OutboundModel = rawModel, outbound
				})}, nil
			}
			if n := h.run(t); n != 1 {
				t.Fatalf("backfill rows=%d", n)
			}
		}},
		{"模态核实", rawModel, 2, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != outbound {
					t.Errorf("outbound model=%q err=%v", payload.Model, err)
				}
				_, _ = io.WriteString(w, chat)
			}))
			defer server.Close()
			worker := NewModalityVerification(nil, nil, nil)
			target := modalityVerifyTarget{ProviderID: providerID, CredentialID: credID, RawModel: rawModel, OutboundModel: outbound,
				BaseURL: server.URL, Protocol: "openai-completions"}
			if result := worker.probe(t.Context(), target, "fixture-key"); result.Carry != ModalityLevelAccepted {
				t.Fatalf("modality probe result=%+v", result)
			}
		}},
		{"凭据轮询", "", 1, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"data":[]}`)
			}))
			defer server.Close()
			if ok, detail := probeCredential(withPlatformProbeBinding(t.Context(), providerID, credID, ""), server.URL, "fixture-key"); !ok {
				t.Fatalf("credential probe: %s", detail)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &attemptStoreProbeFake{}
			SetAttemptRecorder(fake)
			t.Cleanup(func() { SetAttemptRecorder(nil) })
			tc.run(t)
			if len(fake.attempts) != tc.sends {
				t.Fatalf("ledger sends=%d want %d", len(fake.attempts), tc.sends)
			}
			for _, a := range fake.attempts {
				if a.ProviderID != providerID || a.CredentialID != credID || a.Model != tc.model || a.RequestID != "" ||
					a.TenantID != "" || a.TrafficClass != "platform" || a.ClientID != "platform-automation" {
					t.Fatalf("probe lost its binding: %+v", a)
				}
				if tc.model != "" && fake.usage[a.ID].InputTokens == nil {
					t.Fatalf("probe lost its usage: %+v", a)
				}
			}
		})
	}
}

func TestPlatformProbeSkipsBindingFromDifferentCredential(t *testing.T) {
	fake := &attemptStoreProbeFake{}
	SetAttemptRecorder(fake)
	t.Cleanup(func() { SetAttemptRecorder(nil) })
	ctx := withPlatformProbeBinding(t.Context(), 71, 42, "binding-model")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.invalid/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: probeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	resp, err := platformProbeDo(ctx, client, req, 43, "request-model", "probe-test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(fake.attempts) != 1 || fake.attempts[0].ProviderID != 0 || fake.attempts[0].CredentialID != 43 || fake.attempts[0].Model != "request-model" {
		t.Fatalf("conflicting binding attributed to send: %+v", fake.attempts)
	}
}
