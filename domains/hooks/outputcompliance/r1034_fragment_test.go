package outputcompliance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestR1034BuiltinChecksJoinedToolFragments(t *testing.T) {
	const secret = "TrulySensitive987!"
	args, _ := json.Marshal(map[string]string{"command": "echo password=" + secret})
	split := strings.Index(string(args), "password=") + len("password=")
	hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil)
	meta := &response.StreamMeta{TenantID: "default", State: response.NewStreamState()}
	var wire strings.Builder
	for _, part := range []string{string(args[:split]), string(args[split:])} {
		encoded, _ := json.Marshal(part)
		frame := fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":%s}}]}}]}`+"\n\n", encoded)
		result, err := hook.InterceptStreamChunk(context.Background(), []byte(frame), meta)
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || len(result.ModifiedChunk) > 0 || !result.SuppressChunk {
			t.Fatal("unchecked tool fragment released")
		}
	}
	pending, err := hook.FlushStreamPending(context.Background(), meta)
	wire.Write(pending)
	if err == nil && strings.Contains(wire.String(), secret) {
		t.Fatal("joined tool secret escaped inspection")
	}
	if err == nil && !strings.Contains(wire.String(), "REDACTED") {
		t.Fatal("no evidence of completed lane inspection")
	}
}
