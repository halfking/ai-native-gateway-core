package executors

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/toolhistory"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

func TestRepairOutboundToolHistoryAmbiguityIsRequestError(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"same","function":{"name":"Read","arguments":"{}"}},{"id":"same","function":{"name":"Read","arguments":"{}"}}]}]}`)
	_, err := repairOutboundToolHistory(nil, provider.Candidate{}, body, "chat")
	if !errors.Is(err, toolhistory.ErrAmbiguousHistory) {
		t.Fatalf("ambiguous cause lost: %v", err)
	}
	var typed *upstream.Error
	if !errors.As(err, &typed) || typed.StatusCode != 400 || classifyExecError(err) != errorsx.KindClientBug {
		t.Fatalf("local ambiguity must classify as non-penalizing request error: %v", err)
	}
}

func TestRepairOutboundToolHistoryUsesProviderIDFieldRegistry(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{}}]}]}`)
	for _, tc := range []struct{ catalog, field string }{{"anthropic", "tool_use_id"}, {"minimax", "tool_call_id"}} {
		fixed, err := repairOutboundToolHistory(nil, provider.Candidate{CatalogCode: tc.catalog}, body, "messages")
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Messages []struct{ Content []map[string]json.RawMessage }
		}
		if err := json.Unmarshal(fixed, &envelope); err != nil || len(envelope.Messages) != 2 {
			t.Fatalf("invalid repaired Messages body: %s (%v)", fixed, err)
		}
		result := envelope.Messages[1].Content[0]
		if !bytes.Equal(result[tc.field], []byte(`"a"`)) || !bytes.Equal(result["is_error"], []byte("true")) {
			t.Fatalf("%s did not honor field registry %s: %s", tc.catalog, tc.field, fixed)
		}
	}
}
