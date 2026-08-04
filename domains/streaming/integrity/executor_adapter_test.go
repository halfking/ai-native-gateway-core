package integrity

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// Direct fields map 1:1 from the executor candidate into the integrity candidate.
func TestExecutorAdapter_FieldMapping(t *testing.T) {
	rec := &captureRecorder{}
	a := NewExecutorAdapter(NewDetector(rec))

	toInt := func(v int) *int { return &v }
	pt, ct, tot := 11, 22, 33
	a.Observe(context.Background(), executors.IntegrityCandidate{
		RequestID:          "req-1",
		TenantID:           "tenant-7",
		ApplicationID:      toInt(9),
		APIKeyID:           toInt(21),
		ProviderID:         toInt(3),
		ProviderCode:       "anthropic",
		CredentialID:       toInt(99),
		ClientModel:        "glm-5.2",
		OutboundModel:      "glm-5.2",
		RawModel:           "glm-5.2-raw",
		RespModel:          "glm-5.2",
		FinishReason:       "stop",
		PromptTokens:       &pt,
		CompletionTokens:   &ct,
		TotalTokens:        &tot,
		ChunkCount:         5,
		ChunksSent:         4,
		ProviderResponseID: "resp_abc",
		SystemFingerprint:  "fp_1",
		UsageSource:        "response.usage",
		IsStream:           true,
	})

	// No anomaly should fire (model matches, finish reason is benign, tokens
	// consistent), but the candidate must have reached the detector intact.
	// We assert mapping indirectly: feed a mismatched RespModel in a second
	// call and confirm the detector recorded it, proving field copy works.
	if got := rec.byType(AnomalyModelMismatch); len(got) != 0 {
		t.Fatalf("unexpected mismatch on matching models: %d", len(got))
	}

	a.Observe(context.Background(), executors.IntegrityCandidate{
		RequestID:     "req-2",
		OutboundModel: "glm-5.2",
		RespModel:     "glm-5.1",
		ProviderCode:  "test",
	})
	ev := rec.byType(AnomalyModelMismatch)
	if len(ev) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(ev))
	}
	if ev[0].RequestID != "req-2" {
		t.Fatalf("RequestID not mapped: %q", ev[0].RequestID)
	}
}

// ResponseBody fills only empty fields; an already-set RespModel survives
// (the body's model is NOT extracted over it).
func TestExecutorAdapter_BodyFillsGapsWithoutOverwriting(t *testing.T) {
	rec := &captureRecorder{}
	a := NewExecutorAdapter(NewDetector(rec))

	// Body carries a DIFFERENT model than the candidate. If the body
	// overwrote the candidate, a model mismatch would fire (body model !=
	// outbound). No mismatch ⇒ the candidate's RespModel was preserved.
	body := []byte(`{"model":"glm-5.1","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)

	a.Observe(context.Background(), executors.IntegrityCandidate{
		OutboundModel: "glm-5.2",
		RespModel:     "glm-5.2", // preserved → matches outbound
		FinishReason:  "stop",
		ResponseBody:  body,
	})
	if got := rec.byType(AnomalyModelMismatch); len(got) != 0 {
		t.Fatalf("body model overwrote candidate RespModel: %d mismatch(es)", len(got))
	}
	// Token arith also stays consistent: body fills completion/total only when
	// nil, and 10+5 == 15, so no arith anomaly either.
	if got := rec.byType(AnomalyTokenArithFail); len(got) != 0 {
		t.Fatalf("unexpected token arith anomaly: %d", len(got))
	}
}

// ResponseBody fills empty RespModel from the body (used by non-stream paths).
func TestExecutorAdapter_BodyFillsEmptyRespModel(t *testing.T) {
	rec := &captureRecorder{}
	a := NewExecutorAdapter(NewDetector(rec))

	body := []byte(`{"model":"glm-5.2","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	// RespModel empty on the candidate → extracted from body → matches OutboundModel.
	a.Observe(context.Background(), executors.IntegrityCandidate{
		OutboundModel: "glm-5.2",
		ResponseBody:  body,
	})
	if got := rec.byType(AnomalyModelMismatch); len(got) != 0 {
		t.Fatalf("body-extracted model should match outbound: %d", len(got))
	}

	// And when body model differs → mismatch fires (proves extraction ran).
	rec2 := &captureRecorder{}
	a2 := NewExecutorAdapter(NewDetector(rec2))
	body2 := []byte(`{"model":"glm-5.1"}`)
	a2.Observe(context.Background(), executors.IntegrityCandidate{
		OutboundModel: "glm-5.2",
		ResponseBody:  body2,
	})
	if got := rec2.byType(AnomalyModelMismatch); len(got) != 1 {
		t.Fatalf("expected mismatch from body model, got %d", len(got))
	}
}

// Each call to NewStreamTextObserver returns an independent tracker so a
// breach in one request never bleeds into another.
func TestExecutorAdapter_NewStreamTextObserverIndependent(t *testing.T) {
	rec := &captureRecorder{}
	a := NewExecutorAdapterWithConfig(NewDetector(rec), StreamTrackerConfig{
		AbortEnabled: true, AbortMinHits: 2, RecordMinHits: 2,
	})

	o1 := a.NewStreamTextObserver()
	o2 := a.NewStreamTextObserver()
	if o1 == nil || o2 == nil {
		t.Fatal("observer must not be nil when detector is set")
	}
	if o1 == o2 {
		t.Fatal("observers must be distinct instances per request")
	}

	block := make([]byte, repeatedBlockBytes)
	for i := range block {
		block[i] = 'z'
	}
	// Trip o1 into an abort.
	o1.ObserveText(string(block))
	if !o1.ObserveText(string(block)) {
		t.Fatal("o1 should abort on second identical block")
	}
	// o2 is untouched.
	if _, _, _, _, ok := o2.RepeatedContentHash(); ok {
		t.Fatal("o2 flagged by activity on o1")
	}
}

// Nil detector → Observe is a no-op and observer factory returns nil.
func TestExecutorAdapter_NilDetectorSafe(t *testing.T) {
	a := NewExecutorAdapter(nil)                                  // d == nil
	a.Observe(context.Background(), executors.IntegrityCandidate{ // must not panic
		OutboundModel: "a", RespModel: "b",
	})
	if got := a.NewStreamTextObserver(); got != nil {
		t.Fatalf("nil detector should return nil observer, got %T", got)
	}

	var nilA *ExecutorAdapter
	nilA.Observe(context.Background(), executors.IntegrityCandidate{}) // must not panic
	if got := nilA.NewStreamTextObserver(); got != nil {
		t.Fatalf("nil adapter should return nil observer, got %T", got)
	}
}
