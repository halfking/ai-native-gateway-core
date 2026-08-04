package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func TestNodeInvalidationChannel(t *testing.T) {
	if got := store.NodeInvalidationChannel("ursm:v2:"); got != "ursm:v2:meta:node_invalidation" {
		t.Fatalf("channel=%q", got)
	}
}

func TestNodeInvalidationPayloadRoundTrip(t *testing.T) {
	payload := store.NodeInvalidationPayload{TenantID: "tenant-a", CredentialID: 17, RawModel: "model:with:colon"}.String()
	parsed, ok := store.ParseNodeInvalidation(payload)
	if !ok {
		t.Fatalf("valid payload should parse: %q", payload)
	}
	if parsed.TenantID != "tenant-a" || parsed.CredentialID != 17 || parsed.RawModel != "model:with:colon" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestNodeInvalidationPayloadRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"only-one-line",
		"two\nlines",
		"tenant\nnot-a-number\nmodel",
		"tenant\n-1\nmodel",
		"tenant\n1\n",
	}
	for _, raw := range cases {
		if _, ok := store.ParseNodeInvalidation(raw); ok {
			t.Fatalf("expected rejection for %q", raw)
		}
	}
}
