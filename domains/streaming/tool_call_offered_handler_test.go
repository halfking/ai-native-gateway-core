package streaming

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// This exercises the actual three handlers, dispatch, Executor and HTTP client;
// the upstream independently verifies the offered tool survived conversion.
func TestOfferedActualHandlersUseCallerNamesAcrossSixPaths(t *testing.T) {
	for _, client := range []string{"chat", "messages", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []string{"known", "unknown", "no_offer", "incomplete"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", client, stream, scenario), func(t *testing.T) {
					name := "search"
					if scenario == "unknown" {
						name = "not_offered"
					}
					text := `<tool_call>{"name":"` + name + `","arguments":{"q":"caller-value"}}</tool_call>`
					if scenario == "incomplete" {
						text = `minimax[>[<tool_call><function=search>unfinished`
					} else if scenario != "known" {
						text = `minimax[>[` + text
					}
					var observed atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						var request struct {
							Tools []struct {
								Type     string
								Function struct{ Name string }
							}
							Stream bool
						}
						if err := json.Unmarshal(body, &request); err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						expected := scenario != "no_offer"
						offered := len(request.Tools) == 1 && request.Tools[0].Type == "function" && request.Tools[0].Function.Name == "search"
						if offered != expected || request.Stream != stream {
							t.Errorf("upstream offer/stream premise missing: %s", body)
							w.WriteHeader(400)
							return
						}
						observed.Add(1)
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, offeredFrame(t, text[:7])+offeredFrame(t, text[7:])+"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.Write(offeredResponse(t, text))
						}
					}))
					defer server.Close()
					candidate := provider.Candidate{ProviderID: 811, CredentialID: 812, BaseURL: server.URL, Protocol: "openai-completions", CatalogCode: "minimax", RawModel: "m", OfferRawModel: "m", APIKey: "fixture-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
					request := map[string]any{"model": "m", "stream": stream, "max_tokens": 128}
					if client == "responses" {
						request["input"] = "respond"
					} else {
						request["messages"] = []any{map[string]any{"role": "user", "content": "respond"}}
					}
					if scenario != "no_offer" {
						switch client {
						case "messages":
							request["tools"] = []any{map[string]any{"name": "search", "input_schema": map[string]any{"type": "object"}}}
						case "responses":
							request["tools"] = []any{map[string]any{"type": "function", "name": "search", "parameters": map[string]any{"type": "object"}}}
						default:
							request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "search", "parameters": map[string]any{"type": "object"}}}}
						}
					}
					recorder := upstreamBytesRequest(t, candidate, client, offeredJSON(t, request))
					if recorder.Code != 200 || observed.Load() != 1 {
						t.Fatalf("status=%d upstream=%d body=%s", recorder.Code, observed.Load(), recorder.Body.String())
					}
					actual, names, args := offeredClientResult(t, recorder.Body.String(), client, stream)
					if scenario == "known" {
						if len(names) == 0 || actual != "" || !strings.Contains(args, "caller-value") {
							t.Fatalf("known offer not recovered: text=%q names=%v args=%q wire=%s", actual, names, args, recorder.Body.String())
						}
						for _, n := range names {
							if n != "search" {
								t.Fatalf("wrong executable name: %v", names)
							}
						}
					} else if len(names) != 0 || actual != text {
						t.Fatalf("unknown/incomplete/no-offer changed: text=%q want=%q names=%v wire=%s", actual, text, names, recorder.Body.String())
					}
				})
			}
		}
	}
}

// Independent protocol-field assertions avoid using the repair parser as oracle.
func offeredClientResult(t *testing.T, wire, client string, stream bool) (string, []string, string) {
	t.Helper()
	var text, args strings.Builder
	var names []string
	var consume func(map[string]any)
	consume = func(root map[string]any) {
		switch client {
		case "chat":
			choices, _ := root["choices"].([]any)
			for _, raw := range choices {
				choice, _ := raw.(map[string]any)
				key := "message"
				if stream {
					key = "delta"
				}
				message, _ := choice[key].(map[string]any)
				if value, ok := message["content"].(string); ok {
					text.WriteString(value)
				}
				calls, _ := message["tool_calls"].([]any)
				for _, callRaw := range calls {
					call, _ := callRaw.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					if n, ok := fn["name"].(string); ok && n != "" {
						names = append(names, n)
					}
					if a, ok := fn["arguments"].(string); ok {
						args.WriteString(a)
					}
				}
			}
		case "messages":
			blocks, _ := root["content"].([]any)
			if block, ok := root["content_block"].(map[string]any); ok {
				blocks = append(blocks, block)
			}
			for _, raw := range blocks {
				block, _ := raw.(map[string]any)
				if block["type"] == "tool_use" {
					n, _ := block["name"].(string)
					names = append(names, n)
					if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
						args.WriteString(offeredJSON(t, input))
					}
				}
				if block["type"] == "text" {
					v, _ := block["text"].(string)
					text.WriteString(v)
				}
			}
			if delta, ok := root["delta"].(map[string]any); ok {
				if delta["type"] == "text_delta" {
					v, _ := delta["text"].(string)
					text.WriteString(v)
				}
				if delta["type"] == "input_json_delta" {
					v, _ := delta["partial_json"].(string)
					args.WriteString(v)
				}
			}
		case "responses":
			items, _ := root["output"].([]any)
			if item, ok := root["item"].(map[string]any); ok && root["type"] == "response.output_item.added" {
				items = append(items, item)
			}
			for _, raw := range items {
				item, _ := raw.(map[string]any)
				if item["type"] == "function_call" {
					n, _ := item["name"].(string)
					names = append(names, n)
					a, _ := item["arguments"].(string)
					args.WriteString(a)
				}
				if item["type"] == "message" {
					blocks, _ := item["content"].([]any)
					for _, raw := range blocks {
						block, _ := raw.(map[string]any)
						v, _ := block["text"].(string)
						text.WriteString(v)
					}
				}
			}
			if root["type"] == "response.output_text.delta" {
				v, _ := root["delta"].(string)
				text.WriteString(v)
			}
			if root["type"] == "response.function_call_arguments.delta" {
				v, _ := root["delta"].(string)
				args.WriteString(v)
			}
		}
	}
	if stream {
		for _, line := range strings.Split(wire, "\n") {
			if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
				var root map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &root); err != nil {
					t.Fatal(err)
				}
				consume(root)
			}
		}
	} else {
		var root map[string]any
		if err := json.Unmarshal([]byte(wire), &root); err != nil {
			t.Fatal(err)
		}
		consume(root)
	}
	return text.String(), names, args.String()
}

func TestOfferedActualChatPreservesCandidateContinuationAcrossVendorStrip(t *testing.T) {
	for _, catalog := range []string{"minimax", "", "inferred_late"} {
		for _, scenario := range []string{"overflow", "no_offer"} {
			t.Run(catalog+"/"+scenario, func(t *testing.T) {
				first := `minimax[>[<tool_call>`
				if scenario == "overflow" {
					first += strings.Repeat("x", maxStreamXMLToolCallBytes)
				}
				parts := []string{first, `minimax[>[middle payload]<]`, `minimax[>[</tool_`, `call>]<]`}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					tools, _ := request["tools"].([]any)
					if (len(tools) > 0) != (scenario == "overflow") {
						t.Error("wrong actual caller offer premise")
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for i, part := range parts {
						frame := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}}
						if catalog != "inferred_late" || i > 0 {
							frame["nvext"] = map[string]any{}
						}
						io.WriteString(w, "data: "+offeredJSON(t, frame)+"\n\n")
					}
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				wireCatalog := catalog
				if catalog == "inferred_late" {
					wireCatalog = ""
				}
				candidate := provider.Candidate{ProviderID: 821, CredentialID: 822, BaseURL: server.URL, Protocol: "openai-completions", CatalogCode: wireCatalog, RawModel: "m", OfferRawModel: "m", APIKey: "fixture-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
				request := map[string]any{"model": "m", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "respond"}}}
				if scenario == "overflow" {
					request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "search", "parameters": map[string]any{"type": "object"}}}}
				}
				recorder := upstreamBytesRequest(t, candidate, "chat", offeredJSON(t, request))
				text, names, _ := offeredClientResult(t, recorder.Body.String(), "chat", true)
				want := strings.Join(parts, "")
				if recorder.Code != 200 || len(names) != 0 || text != want {
					t.Fatalf("candidate continuation changed status=%d tools=%v got bytes=%d want=%d suffix=%.120s", recorder.Code, names, len(text), len(want), text[max(0, len(text)-100):])
				}
			})
		}
	}
}

func TestOfferedReleasedFramesPassThreeProtocolReaders(t *testing.T) {
	for _, client := range []string{"chat", "messages", "responses"} {
		for _, terminal := range []string{"done", "finish"} {
			t.Run(client+"/"+terminal, func(t *testing.T) {
				parts := []string{"<tool_call>" + strings.Repeat("漢字\x01", 4000), strings.Repeat("漢字\x01", 4000), strings.Repeat("漢字\x01", 3000)}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct{ Tools []json.RawMessage }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Tools) != 1 {
						t.Error("offered premise missing")
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, part := range parts {
						io.WriteString(w, offeredFrame(t, part))
					}
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
					if terminal == "done" {
						io.WriteString(w, "data: [DONE]\n\n")
					}
				}))
				defer server.Close()
				candidate := provider.Candidate{ProviderID: 831, CredentialID: 832, BaseURL: server.URL, Protocol: "openai-completions", CatalogCode: "minimax", RawModel: "m", OfferRawModel: "m", APIKey: "fixture-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
				request := map[string]any{"model": "m", "stream": true, "max_tokens": 128}
				if client == "responses" {
					request["input"] = "respond"
					request["tools"] = []any{map[string]any{"type": "function", "name": "search", "parameters": map[string]any{"type": "object"}}}
				} else {
					request["messages"] = []any{map[string]any{"role": "user", "content": "respond"}}
					if client == "messages" {
						request["tools"] = []any{map[string]any{"name": "search", "input_schema": map[string]any{"type": "object"}}}
					} else {
						request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "search", "parameters": map[string]any{"type": "object"}}}}
					}
				}
				recorder := upstreamBytesRequest(t, candidate, client, offeredJSON(t, request))
				text, names, _ := offeredClientResult(t, recorder.Body.String(), client, true)
				if recorder.Code != 200 || text != strings.Join(parts, "") || len(names) != 0 || strings.Contains(recorder.Body.String(), `"type":"error"`) {
					t.Fatalf("bridge lost bounded failed text status=%d text=%d want=%d tools=%v", recorder.Code, len(text), len(strings.Join(parts, "")), names)
				}
			})
		}
	}
}
