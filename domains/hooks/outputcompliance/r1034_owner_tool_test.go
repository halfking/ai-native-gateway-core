package outputcompliance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestR1034ToolShapeRedactionHonorsOwnerPolicy(t *testing.T) {
	const args = `{"phone":13800138000}`
	encoded, _ := json.Marshal(args)
	for _, caller := range []string{"alice", "bob"} {
		for _, mode := range []string{"body", "event", "joined"} {
			t.Run(caller+"/"+mode, func(t *testing.T) {
				hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), func(context.Context, string, string) string { return "alice" })
				blocked := false
				if mode == "body" {
					body := fmt.Sprintf(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Bash","arguments":%s}}]}}]}`, encoded)
					result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{CallerOwner: caller, TenantID: "default", ResponseBody: []byte(body)})
					if err != nil {
						t.Fatal(err)
					}
					blocked = result.ShouldBlock
				} else {
					meta := &response.StreamMeta{CallerOwner: caller, TenantID: "default", State: response.NewStreamState()}
					message := "message"
					parts := []string{args}
					if mode == "joined" {
						message = "delta"
						parts = []string{args[:9], args[9:]}
					}
					for _, part := range parts {
						q, _ := json.Marshal(part)
						frame := fmt.Sprintf(`data: {"choices":[{"index":0,"%s":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":%s}}]}}]}`+"\n\n", message, q)
						result, err := hook.InterceptStreamChunk(context.Background(), []byte(frame), meta)
						if err != nil || result != nil && result.ShouldBlock {
							blocked = true
							break
						}
					}
					if !blocked {
						_, err := hook.FlushStreamPending(context.Background(), meta)
						blocked = err != nil
					}
				}
				if blocked != (caller == "bob") {
					t.Fatalf("caller=%s mode=%s blocked=%v", caller, mode, blocked)
				}
			})
		}
	}
}

func TestR1034OwnerCannotOverridePolicyBlock(t *testing.T) {
	hook := NewOutputComplianceInterceptor(r1034PolicyBlockChecker{}, func(context.Context, string, string) string { return "alice" })
	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{CallerOwner: "alice", TenantID: "default", ResponseBody: []byte(`{"choices":[{"message":{"content":"blocked"}}]}`)})
	if err != nil || result == nil || !result.ShouldBlock {
		t.Fatal("owner bypassed an explicit policy block")
	}
}

type r1034PolicyBlockChecker struct{}

func (r1034PolicyBlockChecker) Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{Blocked: true, ToolShapeRedactionRequired: true}, nil
}
