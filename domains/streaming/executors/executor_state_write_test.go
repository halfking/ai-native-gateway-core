package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestShouldWriteCredentialState(t *testing.T) {
	tests := []struct {
		name string
		kind errorsx.ErrorKind
		want bool
	}{
		{name: "model not found writes model state", kind: errorsx.KindModelNotFound, want: true},
		{name: "auth writes credential state", kind: errorsx.KindAuth, want: true},
		{name: "client cancellation is ignored", kind: errorsx.KindCanceled, want: false},
		{name: "tool mismatch is ignored", kind: errorsx.KindToolCallIdMismatch, want: false},
		{name: "transient waits for confirmation", kind: errorsx.KindTransient, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWriteCredentialState(tt.kind); got != tt.want {
				t.Fatalf("shouldWriteCredentialState(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestLegacyWritersEnabled(t *testing.T) {
	var nilExecutor *Executor
	if got := nilExecutor.legacyWritersEnabled(); !got {
		t.Fatal("nil executor should keep legacy writers enabled")
	}

	if got := (&Executor{}).legacyWritersEnabled(); !got {
		t.Fatal("executor without URSM v2 should keep legacy writers enabled")
	}
}
