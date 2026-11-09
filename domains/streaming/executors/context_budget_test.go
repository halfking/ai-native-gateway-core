package executors

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func contextBudgetCandidate(window int) provider.Candidate {
	return provider.Candidate{CredentialID: 7, ProviderID: 3, RawModel: "test-model", ContextWindow: &window}
}

func contextBudgetBody(content string) []byte {
	return []byte(`{"model":"test-model","messages":[{"role":"user","content":"` + content + `"}]}`)
}

func TestContextBudgetRejectsBeforePhysicalAttempt(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", nil), UpstreamAttempts: NewUpstreamAttemptBudget(3)}
	err := e.beginUpstreamAttempt(params, contextBudgetCandidate(128000), "openai-completions", contextBudgetBody(strings.Repeat("x", 70000)))
	if !IsContextBudgetError(err) || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("got err=%v physical attempts=%d", err, params.UpstreamAttempts.Used())
	}
	if got := classifyExecError(err); got != errorsx.KindRequestRejected {
		t.Fatalf("local budget kind=%q, want request-side rejection", got)
	}
}

func TestContextBudgetFitStartsFromEachImmutableCandidateSource(t *testing.T) {
	source := contextBudgetBody(strings.Repeat("x", 120000))
	before := append([]byte(nil), source...)
	var limits []int
	e := &Executor{EnforceContextBudget: true, FitContext: func(body []byte, limit int) ([]byte, error) {
		if !bytes.Equal(body, before) {
			t.Fatal("candidate fit received another candidate's compacted body")
		}
		limits = append(limits, limit)
		// A hook may reuse its input; it must never alter shared request bytes.
		body[0] = ' '
		return contextBudgetBody("important task and value=42"), nil
	}}
	for _, window := range []int{128000, 64000} {
		out, err := e.fitCandidateContextBudget(nil, contextBudgetCandidate(window), "openai-completions", source)
		if err != nil || !bytes.Contains(out, []byte("value=42")) {
			t.Fatalf("window=%d body=%q err=%v", window, out, err)
		}
	}
	if len(limits) != 2 || limits[1] >= limits[0] || !bytes.Equal(source, before) {
		t.Fatalf("limits=%v source changed=%t", limits, !bytes.Equal(source, before))
	}
}

func TestContextBudgetUnknownStrictCandidateIsLocalAndFinalErrorPreserved(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	cand := provider.Candidate{CredentialID: 5, RawModel: "unknown"}
	err := e.checkCandidateContextBudget(nil, cand, "chat", contextBudgetBody("hello"))
	if !IsContextBudgetError(err) {
		t.Fatalf("unknown strict context accepted: %v", err)
	}
	mapped := dispatchErrToExecuteError(errors.Join(errors.New("exhausted"), err))
	if !IsContextBudgetError(mapped) || mapped.LastKind != errorsx.KindContextLength {
		t.Fatalf("lost local budget error: %#v", mapped)
	}
	legacy := &Executor{}
	if err := legacy.checkCandidateContextBudget(nil, cand, "chat", contextBudgetBody("hello")); err != nil {
		t.Fatalf("legacy zero-value executor must remain compatible: %v", err)
	}
}

func TestContextBudgetToolsCountAnd500KCeiling(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	for _, body := range [][]byte{
		[]byte(`{"model":"test","messages":[],"tools":[{"type":"function","function":{"name":"f","description":"` + strings.Repeat("x", 70000) + `"}}]}`),
		contextBudgetBody(strings.Repeat("x", 510000)),
	} {
		window := 128000
		if len(body) > 500000 {
			window = 2000000
		}
		if err := e.checkCandidateContextBudget(nil, contextBudgetCandidate(window), "chat", body); !IsContextBudgetError(err) {
			t.Fatalf("oversized tools/input accepted: %v", err)
		}
	}
}

func TestContextBudgetUnknownForwardIsUnmeteredLocalRejection(t *testing.T) {
	params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", nil), UpstreamAttempts: NewUpstreamAttemptBudget(3)}
	// Router and all provider clients are absent: touching any downstream
	// preparation must fail this test rather than looking like supplier traffic.
	e := &Executor{EnforceContextBudget: true}
	out := e.forwardForDispatch(&dispatchCtx{params: params}, provider.Candidate{CredentialID: 8, RawModel: "unknown"}, "attempt", nil, nil)
	if !IsContextBudgetError(out.Err) || out.HTTPStatus != http.StatusUnprocessableEntity || out.ErrorKind != string(errorsx.KindRequestRejected) || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("outcome=%+v physicalAttempts=%d", out, params.UpstreamAttempts.Used())
	}
}

func TestContextBudgetCannotFitAwayUnknownMediaAccounting(t *testing.T) {
	e := &Executor{EnforceContextBudget: true, FitContext: func([]byte, int) ([]byte, error) {
		t.Fatal("an uncountable input must not invoke lossy fitting")
		return nil, nil
	}}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}]}`)
	if _, err := e.fitCandidateContextBudget(nil, contextBudgetCandidate(128000), "chat", body); !IsContextBudgetError(err) {
		t.Fatalf("unknown media bound accepted: %v", err)
	}
}
