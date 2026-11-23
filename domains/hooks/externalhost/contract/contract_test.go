package contract

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestContractVersionIsPinned(t *testing.T) {
	if ContractVersion != "gateway-external-hook-v1" {
		t.Fatalf("contract version changed unexpectedly: %q", ContractVersion)
	}
}

func TestHookPointPaths(t *testing.T) {
	cases := map[HookPoint]string{
		HookPointOnRequest:  "/hooks/v1/on_request",
		HookPointOnResponse: "/hooks/v1/on_response",
	}
	for point, want := range cases {
		if got := point.Path(); got != want {
			t.Errorf("HookPoint(%q).Path() = %q, want %q", point, got, want)
		}
	}
	if HookPoint("bogus").IsValid() {
		t.Error("unknown hook point must be invalid")
	}
}

func TestOriginClassification(t *testing.T) {
	for _, o := range []Origin{OriginLocal, OriginGatewayKey, OriginRemote} {
		if !o.IsValid() {
			t.Errorf("origin %q must be valid", o)
		}
	}
	if Origin("spoofed").IsValid() {
		t.Error("unknown origin must be invalid (fail-closed)")
	}
	// W3.4 隐私原则：remote 默认不捕获正文。
	if OriginRemote.AllowsBodyCapture() {
		t.Error("remote origin must not allow body capture")
	}
	if !OriginLocal.AllowsBodyCapture() || !OriginGatewayKey.AllowsBodyCapture() {
		t.Error("local/gateway-key origins must allow body capture")
	}
}

func TestNormalizeTimeoutClampsToRange(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, DefaultTimeout},
		{-5 * time.Second, DefaultTimeout},
		{50 * time.Millisecond, MinTimeout},
		{time.Second, time.Second},
		{30 * time.Second, MaxTimeout},
	}
	for _, c := range cases {
		if got := NormalizeTimeout(c.in); got != c.want {
			t.Errorf("NormalizeTimeout(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBoundedCopyTruncatesAt8MiB(t *testing.T) {
	small := BoundedCopy([]byte("hello"))
	if string(small.Body) != "hello" || small.Truncated {
		t.Errorf("small copy = %+v", small)
	}
	if small.OriginalBytes != 5 {
		t.Errorf("OriginalBytes = %d, want 5", small.OriginalBytes)
	}

	big := make([]byte, MaxBodyBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	bounded := BoundedCopy(big)
	if len(bounded.Body) != MaxBodyBytes {
		t.Errorf("bounded body len = %d, want %d", len(bounded.Body), MaxBodyBytes)
	}
	if !bounded.Truncated {
		t.Error("oversized body must be flagged truncated")
	}
	if bounded.OriginalBytes != len(big) {
		t.Errorf("OriginalBytes = %d, want %d", bounded.OriginalBytes, len(big))
	}
	// 不得修改入参。
	if big[MaxBodyBytes] != 'x' {
		t.Error("BoundedCopy must not mutate the input slice")
	}
}

func TestValidateInvocationRejectsVersionMismatch(t *testing.T) {
	inv := &Invocation{
		ContractVersion: "gateway-external-hook-v0",
		HookPoint:       HookPointOnRequest,
		Context:         &Context{RequestID: "r1", Origin: OriginLocal},
		Request:         &RequestPayload{Method: "POST"},
	}
	if err := ValidateInvocation(inv); !errors.Is(err, ErrContractVersionMismatch) {
		t.Fatalf("expected version mismatch, got %v", err)
	}
}

func TestValidateInvocationHookPointPayloadPairing(t *testing.T) {
	base := func() *Invocation {
		return &Invocation{
			ContractVersion: ContractVersion,
			HookPoint:       HookPointOnRequest,
			Context:         &Context{RequestID: "r1", Origin: OriginLocal},
			Request:         &RequestPayload{Method: "POST"},
		}
	}
	if err := ValidateInvocation(base()); err != nil {
		t.Fatalf("valid on_request invocation rejected: %v", err)
	}

	// on_request 不得携带 response。
	bad := base()
	bad.Response = &ResponsePayload{Status: 200}
	if err := ValidateInvocation(bad); err == nil {
		t.Fatal("on_request with response payload must be rejected")
	}

	// on_response 必须携带 response，且不得携带 request。
	bad2 := base()
	bad2.HookPoint = HookPointOnResponse
	bad2.Request = nil
	if err := ValidateInvocation(bad2); err == nil {
		t.Fatal("on_response without response payload must be rejected")
	}

	bad3 := base()
	bad3.HookPoint = HookPointOnResponse
	if err := ValidateInvocation(bad3); err == nil {
		t.Fatal("on_response carrying request payload must be rejected")
	}
}

func TestValidateInvocationRequiresRequestIDAndValidOrigin(t *testing.T) {
	inv := &Invocation{
		ContractVersion: ContractVersion,
		HookPoint:       HookPointOnRequest,
		Context:         &Context{RequestID: "", Origin: OriginLocal},
		Request:         &RequestPayload{},
	}
	if err := ValidateInvocation(inv); err == nil {
		t.Fatal("missing request_id must be rejected")
	}

	inv2 := &Invocation{
		ContractVersion: ContractVersion,
		HookPoint:       HookPointOnRequest,
		Context:         &Context{RequestID: "r1", Origin: Origin("forged")},
		Request:         &RequestPayload{},
	}
	if err := ValidateInvocation(inv2); err == nil {
		t.Fatal("forged origin must be rejected")
	}
}

func TestValidateReplyRules(t *testing.T) {
	// 版本不匹配。
	if err := ValidateReply(&Reply{ContractVersion: "v0", Decision: DecisionContinue}); !errors.Is(err, ErrContractVersionMismatch) {
		t.Fatalf("expected version mismatch, got %v", err)
	}
	// 非法 decision。
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: "maybe"}); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("expected invalid decision, got %v", err)
	}
	// block 必须带原因。
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: DecisionBlock}); err == nil {
		t.Fatal("block without reason must be rejected")
	}
	// rewrite 必须带改写载荷。
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: DecisionRewrite}); err == nil {
		t.Fatal("rewrite without payload must be rejected")
	}
	// 合法组合。
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: DecisionContinue}); err != nil {
		t.Fatalf("valid continue reply rejected: %v", err)
	}
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: DecisionBlock, Reason: "policy"}); err != nil {
		t.Fatalf("valid block reply rejected: %v", err)
	}
	if err := ValidateReply(&Reply{ContractVersion: ContractVersion, Decision: DecisionRewrite, Request: &RequestPatch{}}); err != nil {
		t.Fatalf("valid rewrite reply rejected: %v", err)
	}
}

func TestCheckProtectedFields(t *testing.T) {
	base := &RequestPayload{Model: "gpt-4", Stream: true, ToolDeclarations: []string{"a", "b"}}

	// 未触碰受保护字段。
	same := &RequestPayload{Model: "gpt-4", Stream: true, ToolDeclarations: []string{"b", "a"}}
	if touched := CheckProtectedFields(base, same); len(touched) != 0 {
		t.Errorf("reordered tools must not count as touched, got %v", touched)
	}

	// 改 model。
	mutModel := &RequestPayload{Model: "gpt-4o", Stream: true, ToolDeclarations: []string{"a", "b"}}
	touched := CheckProtectedFields(base, mutModel)
	if len(touched) != 1 || touched[0] != ProtectedFieldModel {
		t.Errorf("expected model touched, got %v", touched)
	}

	// 改 stream。
	mutStream := &RequestPayload{Model: "gpt-4", Stream: false, ToolDeclarations: []string{"a", "b"}}
	touched = CheckProtectedFields(base, mutStream)
	if len(touched) != 1 || touched[0] != ProtectedFieldStream {
		t.Errorf("expected stream touched, got %v", touched)
	}

	// 改 tools（增删都算）。
	mutTools := &RequestPayload{Model: "gpt-4", Stream: true, ToolDeclarations: []string{"a", "c"}}
	touched = CheckProtectedFields(base, mutTools)
	if len(touched) != 1 || touched[0] != ProtectedFieldTools {
		t.Errorf("expected tools touched, got %v", touched)
	}

	// 清空 model 也算触碰。
	mutClear := &RequestPayload{Model: "", Stream: true, ToolDeclarations: []string{"a", "b"}}
	touched = CheckProtectedFields(base, mutClear)
	if len(touched) != 1 || touched[0] != ProtectedFieldModel {
		t.Errorf("expected model touched on clear, got %v", touched)
	}
}

func TestContextDeadline(t *testing.T) {
	var empty *Context
	if !empty.Deadline().IsZero() {
		t.Error("nil context deadline must be zero")
	}
	c := &Context{DeadlineUnixNano: time.Now().Add(time.Minute).UnixNano()}
	if c.Deadline().IsZero() {
		t.Error("deadline must be derived from unix nano")
	}
	if d := (&Context{}).Deadline(); !d.IsZero() {
		t.Error("zero deadline must stay zero")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	inv := &Invocation{
		ContractVersion: ContractVersion,
		HookPoint:       HookPointOnRequest,
		Context: &Context{
			Protocol: "anthropic", Model: "claude", RequestID: "r1",
			Origin: OriginLocal, State: map[string]string{"k": "v"},
		},
		Request: &RequestPayload{Method: "POST", Path: "/v1/messages", Body: Payload{Body: []byte("hi")}},
	}
	raw, err := EncodeInvocation(inv)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if !strings.Contains(string(raw), "gateway-external-hook-v1") {
		t.Error("encoded invocation must carry contract version")
	}

	reply, err := DecodeReply([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"block","reason":"policy"}`))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if reply.Decision != DecisionBlock || reply.Reason != "policy" {
		t.Errorf("decoded reply = %+v", reply)
	}
}

func TestMergeRequestPatchSemantics(t *testing.T) {
	base := &RequestPayload{
		Method: "POST", Path: "/v1/chat", Model: "m1", Stream: true,
		ToolDeclarations: []string{"a"},
		Headers:          map[string]string{"h": "1"},
	}

	// 空补丁：全字段保持原值，且不得共享底层切片。
	out := MergeRequest(base, &RequestPatch{})
	if out.Method != "POST" || out.Path != "/v1/chat" || out.Model != "m1" || !out.Stream {
		t.Errorf("empty patch must not change fields, got %+v", out)
	}
	out.Headers["h"] = "mutated"
	if base.Headers["h"] != "1" {
		t.Error("merge must not share header map with the original")
	}
	out.ToolDeclarations[0] = "mutated"
	if base.ToolDeclarations[0] != "a" {
		t.Error("merge must not share tool declaration slice with the original")
	}

	// 显式改写：只有给出的字段变。
	newPath := "/v1/messages"
	merged := MergeRequest(base, &RequestPatch{Path: &newPath})
	if merged.Path != "/v1/messages" {
		t.Errorf("path = %q", merged.Path)
	}
	if merged.Model != "m1" || !merged.Stream {
		t.Errorf("unspecified fields must stay unchanged, got %+v", merged)
	}
	if base.Path != "/v1/chat" {
		t.Error("merge must not mutate the original payload")
	}

	// 显式置零：视为改写（受保护字段比对应判定为触碰）。
	empty := ""
	zero := false
	cleared := MergeRequest(base, &RequestPatch{Model: &empty, Stream: &zero})
	if cleared.Model != "" || cleared.Stream {
		t.Errorf("explicit zero must be applied, got %+v", cleared)
	}
	touched := CheckProtectedFields(base, cleared)
	if len(touched) != 2 {
		t.Errorf("expected model+stream touched, got %v", touched)
	}
}

func TestMergeResponsePatchSemantics(t *testing.T) {
	base := &ResponsePayload{Status: 200, Model: "m1", Stream: true, Headers: map[string]string{"h": "1"}}
	out := MergeResponse(base, &ResponsePatch{})
	if out.Status != 200 || out.Model != "m1" || !out.Stream {
		t.Errorf("empty patch must not change fields, got %+v", out)
	}
	status := 502
	merged := MergeResponse(base, &ResponsePatch{Status: &status})
	if merged.Status != 502 || merged.Model != "m1" {
		t.Errorf("patch applied incorrectly: %+v", merged)
	}
	if base.Status != 200 {
		t.Error("merge must not mutate the original payload")
	}
}

func TestAllProtectedFields(t *testing.T) {
	got := AllProtectedFields()
	if len(got) != 3 {
		t.Fatalf("expected 3 protected fields, got %v", got)
	}
	seen := map[ProtectedField]bool{}
	for _, f := range got {
		seen[f] = true
	}
	for _, want := range []ProtectedField{ProtectedFieldModel, ProtectedFieldStream, ProtectedFieldTools} {
		if !seen[want] {
			t.Errorf("protected field %q missing", want)
		}
	}
}
