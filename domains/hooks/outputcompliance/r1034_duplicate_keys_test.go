package outputcompliance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestR1034DuplicateToolKeysFailClosedAcrossCarriers(t *testing.T) {
	for _, args := range []string{`{"command":"password=ActualValue987!","command":"echo ok"}`, `{"nested":[{"command":"password=ActualValue987!","comm\u0061nd":"echo ok"}]}`} {
		for _, mode := range []string{"body", "event", "joined"} {
			t.Run(mode, func(t *testing.T) {
				hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil)
				q, _ := json.Marshal(args)
				if mode == "body" {
					body := fmt.Sprintf(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Bash","arguments":%s}}]}}]}`, q)
					result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{TenantID: "default", ResponseBody: []byte(body)})
					if err == nil && (result == nil || !result.ShouldBlock) {
						t.Fatal("duplicate key body released")
					}
					return
				}
				meta := &response.StreamMeta{TenantID: "default", State: response.NewStreamState()}
				message := "message"
				parts := []string{args}
				if mode == "joined" {
					message = "delta"
					parts = []string{args[:len(args)/2], args[len(args)/2:]}
				}
				blocked := false
				for _, part := range parts {
					q, _ := json.Marshal(part)
					frame := fmt.Sprintf(`data: {"choices":[{"index":0,"%s":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":%s}}]}}]}`+"\n\n", message, q)
					result, err := hook.InterceptStreamChunk(context.Background(), []byte(frame), meta)
					if err != nil || result != nil && result.ShouldBlock {
						blocked = true
						break
					}
					if result == nil || !result.SuppressChunk || len(result.ModifiedChunk) > 0 {
						t.Fatal("unchecked fragment released")
					}
				}
				if !blocked {
					_, err := hook.FlushStreamPending(context.Background(), meta)
					blocked = err != nil
				}
				if !blocked {
					t.Fatal("duplicate key stream released")
				}
			})
		}
	}
}
