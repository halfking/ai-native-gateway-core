package reqprobe

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDiagnoseNamedParamRejected(t *testing.T) {
	outbound := []byte(`{"model":"grok-4.6","messages":[],"reasoning_effort":"x-high","temperature":0.7}`)
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"Invalid value for 'reasoning_effort': 'x-high' is not one of ['low','high']","type":"invalid_request_error"}}`),
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
		Protocol:     "openai-chat",
	}
	d, ok := Diagnose(in)
	if !ok {
		t.Fatal("expected diagnosis")
	}
	if d.Trigger != TriggerParamRejected {
		t.Fatalf("trigger = %s, want param_rejected", d.Trigger)
	}
	if d.Param != "reasoning_effort" {
		t.Fatalf("param = %q, want reasoning_effort", d.Param)
	}
}

func TestDiagnoseOpenAIUnrecognizedArgument(t *testing.T) {
	outbound := []byte(`{"model":"gpt-x","messages":[],"thinking":{"type":"enabled"}}`)
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"Unrecognized request argument supplied: thinking","type":"invalid_request_error"}}`),
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
	}
	d, ok := Diagnose(in)
	if !ok || d.Trigger != TriggerParamRejected || d.Param != "thinking" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestDiagnoseModeMismatchNativeResponses404(t *testing.T) {
	in := Input{
		HTTPStatus:      404,
		ErrorBody:       []byte(`{"error":{"message":"not found"}}`),
		OutboundBody:    []byte(`{"model":"m","input":[]}`),
		ErrorKind:       "client_bug",
		Protocol:        "openai-responses",
		NativeResponses: true,
	}
	d, ok := Diagnose(in)
	if !ok || d.Trigger != TriggerModeMismatch || d.SuggestMode != "chat" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestDiagnoseResponsesOnlyHint(t *testing.T) {
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"This model only supports the Responses API","type":"invalid_request_error"}}`),
		OutboundBody: []byte(`{"model":"o9","messages":[]}`),
		ErrorKind:    "unsupported_feature",
		Protocol:     "openai-chat",
	}
	d, ok := Diagnose(in)
	if !ok || d.Trigger != TriggerModeMismatch || d.SuggestMode != "responses" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestDiagnoseSkipsCredentialFamily(t *testing.T) {
	for _, kind := range []string{"rate_limit", "auth", "model_not_found", "context_length_exceeded"} {
		if _, ok := Diagnose(Input{HTTPStatus: 400, ErrorBody: []byte(`{"message":"x"}`), ErrorKind: kind}); ok {
			t.Fatalf("kind %s should not be diagnosed", kind)
		}
	}
	if _, ok := Diagnose(Input{HTTPStatus: 429, ErrorBody: []byte(`{"message":"slow down"}`), ErrorKind: ""}); ok {
		t.Fatal("429 should not be diagnosed")
	}
}

func TestDiagnoseGenericFallback(t *testing.T) {
	outbound := []byte(`{"model":"m","messages":[],"top_k":40,"seed":7}`)
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"invalid_request_error","type":"invalid_request_error"}}`),
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
	}
	d, ok := Diagnose(in)
	if !ok {
		t.Fatal("expected diagnosis")
	}
	if d.Trigger != TriggerParamRejected {
		t.Fatalf("trigger = %s, want param_rejected fallback", d.Trigger)
	}
	if !strings.Contains(d.Param, "top_k") || !strings.Contains(d.Param, "seed") {
		t.Fatalf("param = %q, want strippable list", d.Param)
	}
}

func TestStripParamsNamed(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"x-high","temperature":1}`)
	out, stripped := StripParams(body, "reasoning_effort")
	if len(stripped) != 1 || stripped[0] != "reasoning_effort" {
		t.Fatalf("stripped = %v", stripped)
	}
	if strings.Contains(string(out), "reasoning_effort") {
		t.Fatal("param still present after strip")
	}
	if !strings.Contains(string(out), `"model"`) || !strings.Contains(string(out), `"messages"`) {
		t.Fatal("core params must survive")
	}
}

func TestStripParamsNeverTouchCore(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"max_tokens":100,"stream":true}`)
	_, stripped := StripParams(body, "max_tokens")
	if len(stripped) != 0 {
		t.Fatalf("core param must never strip: %v", stripped)
	}
}

func TestStripParamsAllUncommon(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"thinking":{"type":"enabled"},"top_k":10,"frequency_penalty":0.5,"tools":[]}`)
	out, stripped := StripParams(body, "")
	joined := strings.Join(stripped, ",")
	for _, want := range []string{"thinking", "top_k", "frequency_penalty"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stripped %v missing %s", stripped, want)
		}
	}
	if strings.Contains(string(out), "thinking") || strings.Contains(string(out), "top_k") {
		t.Fatal("params still present")
	}
	if !strings.Contains(string(out), `"tools"`) {
		t.Fatal("tools must survive")
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	meta := TerminalMeta{RequestID: "req-1", ProviderID: 7, ProviderCode: "xai", ClientModel: "grok-4.6", OutboundModel: "grok-4.6"}
	c := NewCoordinator(s)
	defer c.Close()

	c.RecordTerminal(Input{HTTPStatus: 400, ErrorBody: []byte(`boom`), ErrorKind: "client_bug", Protocol: "openai-chat"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "reasoning_effort"}, meta, true)
	waitFor(t, func() bool {
		recs, _, _ := s.List(ctx, Filter{})
		return len(recs) == 1
	})

	recs, total, err := s.List(ctx, Filter{})
	if err != nil || total != 1 {
		t.Fatalf("list err=%v total=%d", err, total)
	}
	if recs[0].Occurrences != 1 || recs[0].RecoveredCount != 1 {
		t.Fatalf("record = %+v", recs[0])
	}
	// 同指纹再失败一次（未恢复）→ occurrences=2, recovered 保持 1。
	c.RecordTerminal(Input{HTTPStatus: 400, ErrorBody: []byte(`boom`), ErrorKind: "client_bug", Protocol: "openai-chat"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "reasoning_effort"}, meta, false)
	waitFor(t, func() bool {
		recs, _, _ := s.List(ctx, Filter{})
		return recs[0].Occurrences == 2
	})
	recs, _, _ = s.List(ctx, Filter{})
	if recs[0].RecoveredCount != 1 {
		t.Fatalf("recoveredCount = %d, want 1", recs[0].RecoveredCount)
	}

	// 学习规则：剔除后成功过 → 可前置剔除。
	learned := s.LearnedParams(ctx, "xai", "grok-4.6")
	if len(learned) != 1 || learned[0] != "reasoning_effort" {
		t.Fatalf("learned = %v", learned)
	}

	// 计数与解决。
	counts, _ := s.Counts(ctx)
	if counts.Unresolved != 1 || counts.NewToday != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	n, _ := s.Resolve(ctx, []int64{recs[0].ID}, "fixed by config")
	if n != 1 {
		t.Fatalf("resolve n = %d", n)
	}
	counts, _ = s.Counts(ctx)
	if counts.Unresolved != 0 {
		t.Fatalf("counts after resolve = %+v", counts)
	}
	// 解决后学习规则失效。
	if got := s.LearnedParams(ctx, "xai", "grok-4.6"); len(got) != 0 {
		t.Fatalf("learned after resolve = %v", got)
	}
	// 未解决过滤。
	recs, _, _ = s.List(ctx, Filter{UnresolvedOnly: true})
	if len(recs) != 0 {
		t.Fatalf("unresolved only should be empty, got %d", len(recs))
	}
}

func TestMemoryStoreResolveFilter(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	s.nowFn = func() time.Time { return time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local) }
	mk := func(provider string) Record {
		return Record{ProviderCode: provider, OutboundModel: "m", Trigger: TriggerParamRejected,
			Param: "top_k", HTTPStatus: 400, ErrorKind: "client_bug"}
	}
	s.Upsert(ctx, mk("xai"))
	s.Upsert(ctx, mk("openrouter"))
	n, err := s.ResolveFilter(ctx, Filter{ProviderCode: "xai"}, "batch")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	counts, _ := s.Counts(ctx)
	if counts.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", counts.Unresolved)
	}
}

func TestMemoryStoreFIFOCap(t *testing.T) {
	s := NewMemoryStore()
	s.nowFn = func() time.Time { return time.Now() }
	ctx := context.Background()
	for i := 0; i < MemoryMaxRecords+50; i++ {
		s.Upsert(ctx, Record{ProviderCode: "p", OutboundModel: string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Trigger: TriggerUpstreamError, HTTPStatus: 400})
	}
	_, total, _ := s.List(ctx, Filter{})
	if total > MemoryMaxRecords {
		t.Fatalf("total = %d exceeds cap", total)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
