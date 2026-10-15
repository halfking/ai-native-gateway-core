package sanitize

import (
	"context"
	"encoding/json"
	compliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"os"
	"strings"
	"testing"
)

func TestR1034Production1971ByteArguments(t *testing.T) {
	args, err := os.ReadFile("testdata/r1034_minimax_sed_arguments.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1971 || !json.Valid(args) {
		t.Fatal("production fixture no longer matches captured lane")
	}
	blocked, wire := runEgressGuardTool(t, "Bash", string(args))
	if blocked || !strings.Contains(wire, "Find MySQL root password") {
		t.Fatal("captured legitimate tool call did not reach the client intact")
	}
	s := productionConfiguredSanitizer(t)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	chain := response.NewInterceptorChain(NewOutputSensitiveInterceptor(s, OutputMask), restore, compliancehook.NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil))
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "Bash", "arguments": string(args)}}}}}}})
	result, err := chain.InterceptNonStream(context.Background(), &response.InterceptRequest{TenantID: "default", ClientProtocol: "openai-chat", ResponseBody: body})
	if err != nil || result != nil && result.ShouldBlock {
		t.Fatalf("full chain rejected captured tool: %v", err)
	}
	if result != nil && len(result.ModifiedBody) > 0 {
		body = result.ModifiedBody
	}
	var got struct {
		Choices []struct {
			Message struct {
				Calls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 1 || len(got.Choices[0].Message.Calls) != 1 || got.Choices[0].Message.Calls[0].Function.Arguments != string(args) {
		t.Fatal("full chain changed captured arguments")
	}
}
