package sanitize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	compliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestR1034ThreeProtocolStreamsRestoreHumanInput(t *testing.T) {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	for _, command := range []string{`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env`, `sed "s/PASSWORD=.*/PASSWORD=[REDACTED]/" .env`} {
		for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
			t.Run(protocol, func(t *testing.T) {
				s := productionConfiguredSanitizer(t)
				input, err := s.SanitizeInput(context.Background(), "管理员帐号改成admin/R7#kQ92!，继续")
				if err != nil {
					t.Fatal(err)
				}
				marker := ""
				for k, v := range input.SanitizeMap {
					if v == "R7#kQ92!" {
						marker = k
					}
				}
				if marker == "" {
					t.Fatal("real input did not allocate a secret marker")
				}
				b, _ := json.Marshal(map[string]string{"command": command, "password": marker})
				args := string(b)
				split := strings.Index(args, marker) + len(marker)/2
				head, tail := q(args[:split]), q(args[split:])
				var frames []string
				switch protocol {
				case "openai-chat":
					frames = []string{
						fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"Bash","arguments":%s}}]}}]}`, head),
						fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]}}]}`, tail),
						`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]",
					}
				case "openai-responses":
					frames = []string{
						`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"Bash","arguments":""}}`,
						fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc1","output_index":0,"delta":%s}`, head),
						fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc1","output_index":0,"delta":%s}`, tail),
						`{"type":"response.completed","response":{"status":"completed"}}`,
					}
				case "anthropic-messages":
					frames = []string{
						`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"Bash","input":{}}}`,
						fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, head),
						fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, tail),
						`{"type":"content_block_stop","index":0}`, `{"type":"message_stop"}`,
					}
				}
				restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				chain := response.NewInterceptorChain(NewOutputSensitiveInterceptor(s, OutputMask), restore, compliancehook.NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil))
				ctx := WithSanitizeMap(context.Background(), input.SanitizeMap)
				meta := &response.StreamMeta{TenantID: "default", SessionID: "r1034", ClientProtocol: protocol, State: response.NewStreamState()}
				var wire strings.Builder
				for _, frame := range frames {
					chunk := []byte("data: " + frame + "\n\n")
					result, err := chain.InterceptStreamChunk(ctx, chunk, meta)
					if err != nil {
						t.Fatal(err)
					}
					if result == nil {
						wire.Write(chunk)
						continue
					}
					if result.ShouldBlock {
						t.Fatal("legitimate tool stream blocked")
					}
					if len(result.ModifiedChunk) > 0 {
						wire.Write(result.ModifiedChunk)
					} else if !result.SuppressChunk {
						wire.Write(chunk)
					}
				}
				pending, err := chain.FlushStreamPending(ctx, meta)
				if err != nil {
					t.Fatal(err)
				}
				wire.Write(pending)
				var collected strings.Builder
				for _, line := range strings.Split(wire.String(), "\n") {
					if !strings.HasPrefix(line, "data: {") {
						continue
					}
					var frame map[string]json.RawMessage
					if err = json.Unmarshal([]byte(line[6:]), &frame); err != nil {
						t.Fatal(err)
					}
					var typ string
					_ = json.Unmarshal(frame["type"], &typ)
					switch protocol {
					case "openai-chat":
						var c struct {
							Choices []struct {
								Delta struct {
									ToolCalls []struct {
										Function struct {
											Arguments string `json:"arguments"`
										} `json:"function"`
									} `json:"tool_calls"`
								} `json:"delta"`
							} `json:"choices"`
						}
						if err = json.Unmarshal([]byte(line[6:]), &c); err != nil {
							t.Fatal(err)
						}
						for _, choice := range c.Choices {
							for _, call := range choice.Delta.ToolCalls {
								collected.WriteString(call.Function.Arguments)
							}
						}
					case "openai-responses":
						if typ == "response.function_call_arguments.delta" {
							var v string
							_ = json.Unmarshal(frame["delta"], &v)
							collected.WriteString(v)
						}
					case "anthropic-messages":
						if typ == "content_block_delta" {
							var v struct {
								Partial string `json:"partial_json"`
							}
							_ = json.Unmarshal(frame["delta"], &v)
							collected.WriteString(v.Partial)
						}
					}
				}
				var got map[string]string
				if err = json.Unmarshal([]byte(collected.String()), &got); err != nil {
					t.Fatalf("client tool JSON invalid: %v", err)
				}
				if got["command"] != command || got["password"] != "R7#kQ92!" || strings.Contains(wire.String(), "SENSITIVE:") {
					t.Fatalf("tool values did not round trip exactly: %#v", got)
				}
			})
		}
	}
}
