package main

import (
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"testing"
)

func TestSmartSaniGuardWithoutRedisWiresOutputBeforeRestoration(t *testing.T) {
	h := &streaming.ChatHandler{}
	detector := installSmartSaniGuard(h, nil, nil)
	if detector == nil {
		t.Fatal("Redis-free gateway lost sanitization")
	}
	hooks := h.ResponseInterceptorForWire().ListInterceptors()
	if len(hooks) != 2 {
		t.Fatalf("hooks=%d, want generated-output gate and restoration", len(hooks))
	}
	if _, ok := hooks[1].(*sanitize.SanitizeRestoreInterceptor); !ok {
		t.Fatal("output gate must precede restoration")
	}
}
