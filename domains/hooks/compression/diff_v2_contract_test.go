package compression

import (
	"encoding/json"
	"testing"
)

func TestBuildOutboundMessages_AcceptsV2BareOutboundArray(t *testing.T) {
	last := []byte(`[
		{"role":"assistant","content":"[smm_v1:abc] compressed history"},
		{"role":"user","content":"recent question"},
		{"role":"assistant","content":"recent answer"}
	]`)
	client := makeBody([]map[string]string{
		userMsg("recent question"),
		assistantMsg("recent answer"),
		userMsg("follow-up"),
	})

	result, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatalf("BuildOutboundMessages returned error: %v", err)
	}
	if result.IsNewSess {
		t.Fatal("bare V2 outbound array must be treated as a continuation")
	}
	if result.DeltaCount != 1 {
		t.Fatalf("DeltaCount = %d, want 1", result.DeltaCount)
	}

	var outbound struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &outbound); err != nil {
		t.Fatalf("unmarshal rebuilt outbound: %v", err)
	}
	if len(outbound.Messages) != 4 {
		t.Fatalf("outbound message count = %d, want 4", len(outbound.Messages))
	}
	if !isSummaryMarkerMsg(outbound.Messages[0]) {
		t.Fatalf("summary marker was not retained: %s", outbound.Messages[0])
	}
}
