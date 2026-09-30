package main

import (
	"testing"

	outputcompliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

func TestRestoreRunsBeforeOutputCompliance(t *testing.T) {
	goal := response.NewInterceptorChain()
	audit := response.NewInterceptorChain()
	restore := response.NewInterceptorChain()
	compliance := outputcompliancehook.NewOutputComplianceInterceptor(nil, nil)
	got := insertRestoreBeforeCompliance([]response.ResponseInterceptor{goal, audit, compliance}, restore)
	if len(got) != 4 || got[0] != goal || got[1] != audit || got[2] != restore || got[3] != compliance {
		t.Fatalf("unexpected interceptor order: %#v", got)
	}
	withoutCompliance := insertRestoreBeforeCompliance([]response.ResponseInterceptor{goal, audit}, restore)
	if len(withoutCompliance) != 3 || withoutCompliance[2] != restore {
		t.Fatalf("restore must remain present in data-plane mode: %#v", withoutCompliance)
	}
}

func TestLiteWithoutRedisStillInstallsOutputCompliance(t *testing.T) {
	handler := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	ensureOutputComplianceFallback(handler)
	chain := handler.ResponseInterceptorForWire()
	if chain == nil || !hasOutputComplianceInterceptor(chain.ListInterceptors()) {
		t.Fatal("lite response chain has no output compliance hook")
	}
	ensureOutputComplianceFallback(handler)
	if got := len(handler.ResponseInterceptorForWire().ListInterceptors()); got != 1 {
		t.Fatalf("fallback must be idempotent, got %d interceptors", got)
	}
}

func TestLiteResponseChainAddsBuiltinComplianceBeforeRestore(t *testing.T) {
	interceptors := []response.ResponseInterceptor{buildOutputComplianceInterceptor(nil)}
	if !hasOutputComplianceInterceptor(interceptors) {
		t.Fatal("lite response chain has no output compliance hook")
	}
	restore := response.NewInterceptorChain()
	ordered := insertRestoreBeforeCompliance(interceptors, restore)
	if len(ordered) != 2 || ordered[0] != restore || ordered[1] != interceptors[0] {
		t.Fatalf("lite restore/compliance order: %#v", ordered)
	}
}
