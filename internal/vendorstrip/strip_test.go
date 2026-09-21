package strip

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestRegistrySanitizeResolvedDetectsMiniMaxErrorBeforeStrip(t *testing.T) {
	body := []byte(`{"base_resp":{"status_code":1008,"status_msg":"quota"},"request_id":"private"}`)

	got, vendor, signal, isError := DefaultRegistry.SanitizeResolved(body, "")

	if !isError {
		t.Fatal("expected MiniMax error")
	}
	if vendor != VendorMiniMax {
		t.Fatalf("vendor = %q, want %q", vendor, VendorMiniMax)
	}
	if signal.Code != 1008 || signal.Message != "quota" || signal.Kind != errorsx.KindQuota {
		t.Fatalf("signal = %#v, want MiniMax quota signal", signal)
	}
	if string(got) != string(body) {
		t.Fatalf("error body was stripped before detection: got %s, want %s", got, body)
	}
}

func TestRegistrySanitizeResolvedFiltersOnlyTopLevelVendorFields(t *testing.T) {
	body := []byte(`{"deepseek_request_id":"private","choices":[{"message":{"content":"deepseek_request_id"}}]}`)

	got, vendor, _, isError := DefaultRegistry.SanitizeResolved(body, "")

	if isError || vendor != VendorDeepSeek {
		t.Fatalf("resolved vendor = %q, error = %t; want deepseek and no error", vendor, isError)
	}
	var response map[string]any
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatalf("unmarshal sanitized response: %v", err)
	}
	if _, ok := response["deepseek_request_id"]; ok {
		t.Fatalf("top-level DeepSeek field remained: %s", got)
	}
	content := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "deepseek_request_id" {
		t.Fatalf("nested content changed: %q", content)
	}
}

func TestRegistryResolveDoesNotInferFromNestedUserText(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"content":"mentions base_resp and doubao_request_id"}}]}`)

	vendor, policy := DefaultRegistry.Resolve(body, "")

	if vendor != "" || policy != nil {
		t.Fatalf("nested text inferred vendor %q with policy %T", vendor, policy)
	}
}

func TestRegistryPreservesSensitiveFieldsAndPassthroughVendors(t *testing.T) {
	miniMax := []byte(`{"request_id":"private","input_sensitive":true,"input_sensitive_type":7,"output_sensitive":true,"output_sensitive_type":3}`)
	got := DefaultRegistry.Strip(miniMax, VendorMiniMax)
	var response map[string]any
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatalf("unmarshal MiniMax response: %v", err)
	}
	for _, field := range []string{"input_sensitive", "input_sensitive_type", "output_sensitive", "output_sensitive_type"} {
		if _, ok := response[field]; !ok {
			t.Errorf("sensitive field %q was stripped", field)
		}
	}
	if _, ok := response["request_id"]; ok {
		t.Error("MiniMax private request_id was retained")
	}

	ernie := []byte(`{"search_info":{"id":"preserve"}}`)
	if got := DefaultRegistry.Strip(ernie, VendorErnie); string(got) != string(ernie) {
		t.Fatalf("Ernie response changed: got %s, want %s", got, ernie)
	}
	if got := DefaultRegistry.Strip(ernie, "unknown"); string(got) != string(ernie) {
		t.Fatalf("unknown vendor response changed: got %s, want %s", got, ernie)
	}
}

func TestClassifyMiniMaxStatusCodeExactMapping(t *testing.T) {
	cases := map[int]errorsx.ErrorKind{
		0:    "",
		1000: errorsx.KindUpstreamDown,
		1001: errorsx.KindTimeout,
		1002: errorsx.KindRateLimit,
		1004: errorsx.KindAuth,
		1008: errorsx.KindQuota,
		1013: errorsx.KindUpstreamDown,
		1027: errorsx.KindContentFilter,
		1039: errorsx.KindContextLength,
		2013: errorsx.KindClientBug,
		9999: errorsx.KindUpstreamDown,
	}
	for code, want := range cases {
		if got := ClassifyMiniMaxStatusCode(code); got != want {
			t.Errorf("ClassifyMiniMaxStatusCode(%d) = %q, want %q", code, got, want)
		}
	}
}
