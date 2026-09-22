package anthropic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// Wave4-D5 (2026-09-22): the request direction has two live implementations —
// the handwritten ConvertChatRequestToAnthropic (this package) and the IR
// pipeline (internal/ir.ParseOpenAI → SerializeAnthropic). This golden harness
// runs a payload matrix through BOTH and pins their outputs under
// testdata/parity-golden so drift on either side fails loudly.
//
// ADJUDICATION (rulings encoded in parityAllowedDiffs; goldens pin behavior):
//
// By-design divergences (accepted, no code change):
//   - max_tokens: handwritten defaults 4096; IR emits as-parsed (0 when the
//     client omitted it) — defaulting is a converter shim vs pipeline-layer
//     responsibility. (Anthropic requires the field, so IR-path clients must
//     send it; ledgered for the IR pipeline owner.)
//   - Unknown top-level fields: IR preserves them losslessly (with an
//     ir_unknown_field anomaly) for round-trip fidelity; the handwritten
//     converter drops them.
//   - reasoning_effort → thinking: IR-only capability; handwritten drops it.
//
// IR-side fidelity gaps found by this harness (GAP-1/2/3, CLOSED Wave5
// 2026-09-22 — the allowlist entries below were removed when each gap
// closed, so any recurrence now fails loudly):
//   - GAP-1 system array content: IR kept only the FIRST text block
//     (data loss); now joins all text blocks with newline like the
//     handwritten converter (internal/ir/parse_openai.go extractSystemPrompt).
//   - GAP-2 named tool_choice: IR serialized {"type":"function",
//     "function":{"name":X}} to the bare string "function" — not a valid
//     Anthropic form; now emits {"type":"tool","name":X}
//     (internal/ir/serialize_anthropic.go serializeAnthropicToolChoice).
//   - GAP-3 empty text block: IR preserved assistant content:"" as a text
//     block next to tool_use — Anthropic rejects empty text blocks; now
//     suppressed (serializeAnthropicMessageContent).
//
// Convergence ruling: the handwritten side is NOT swapped for an IR wrapper
// this round. Its caller (anthropic_bridge.go) runs on a path with different
// error/fallback semantics (vendorstrip interpose, Q2 gate) than the executor
// IR path (executor_anthropic.go), and IR currently carries GAP-1/2/3 — a
// wrapper swap today would regress those shapes on the bridge path. Golden
// files pin both sides; convergence re-evaluated after the gaps close.

type parityCase struct {
	name string
	in   string
	// skipIR marks inputs that are valid for the handwritten converter but
	// intentionally outside the IR parse contract (documented ruling).
	skipIR bool
}

func parityMatrix() []parityCase {
	return []parityCase{
		{
			name: "single_user_text",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "system_string_multi_turn",
			in:   `{"model":"gpt-x","system_ignored":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"}],"max_tokens":77}`,
		},
		{
			name: "system_array_joins",
			in:   `{"model":"gpt-x","messages":[{"role":"system","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]},{"role":"user","content":"go"}]}`,
		},
		{
			name: "sampling_and_stop_and_user",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"m"}],"temperature":0.5,"top_p":0.9,"top_k":40,"stop":["END","STOP"],"user":"u-1","stream":true,"max_tokens":100}`,
		},
		{
			name: "multi_function_tools_and_choice",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}},{"type":"function","function":{"name":"get_time","description":"t","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"get_weather"}},"max_tokens":50}`,
		},
		{
			name: "assistant_tool_calls_then_tool_result",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"weather in Paris?"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"18C"}],"max_tokens":99}`,
		},
		{
			name: "multimodal_image_data_uri_and_https",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}},{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}],"max_tokens":10}`,
		},
		{
			name: "ir_thinking_extension",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"deep"}],"max_tokens":1200,"reasoning_effort":"high"}`,
		},
		{
			name: "no_max_tokens_default",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "nonfunction_tool_passthrough",
			in:   `{"model":"gpt-x","messages":[{"role":"user","content":"draw"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"max_tokens":5}`,
		},
	}
}

// canonicalJSON re-marshals through map[string]any so both sides compare on
// normalized float64 numbers and map key order.
func canonicalJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("golden output is not valid JSON: %v\n%s", err, string(raw))
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(out)
}

// jsonDiff returns sorted path-level differences between two JSON docs.
func jsonDiff(a, b any, prefix string) []string {
	var diffs []string
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: object vs %T", prefix, b)}
		}
		keys := map[string]struct{}{}
		for k := range av {
			keys[k] = struct{}{}
		}
		for k := range bv {
			keys[k] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			av2, aok := av[k]
			bv2, bok := bv[k]
			switch {
			case aok && !bok:
				diffs = append(diffs, fmt.Sprintf("%s.%s: only-in-A %v", prefix, k, av2))
			case !aok && bok:
				diffs = append(diffs, fmt.Sprintf("%s.%s: only-in-B %v", prefix, k, bv2))
			default:
				diffs = append(diffs, jsonDiff(av2, bv2, prefix+"."+k)...)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: array vs %T", prefix, b)}
		}
		if len(av) != len(bv) {
			return append(diffs, fmt.Sprintf("%s: len %d vs %d", prefix, len(av), len(bv)))
		}
		for i := range av {
			diffs = append(diffs, jsonDiff(av[i], bv[i], fmt.Sprintf("%s[%d]", prefix, i))...)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			diffs = append(diffs, fmt.Sprintf("%s: %v vs %v", prefix, a, b))
		}
	}
	return diffs
}

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

const updateGoldenEnv = "PARITY_UPDATE_GOLDEN"

func goldenPath(name, side string) string {
	return filepath.Join("testdata", "parity-golden", name+"."+side+".json")
}

func loadOrUpdateGolden(t *testing.T, path, content string) (string, bool) {
	t.Helper()
	if os.Getenv(updateGoldenEnv) != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return content, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden missing (run with %s=1 to write): %v", updateGoldenEnv, err)
	}
	return strings.TrimRight(string(data), "\n"), false
}

// TestChatToAnthropic_IRParityGolden runs the matrix through both request
// converters and diffs them field-by-field. Golden files pin each side's
// output; the in-test adjudication list pins the KNOWN, RULING-BACKED
// differences so a silent regression on either side fails loudly.
func TestChatToAnthropic_IRParityGolden(t *testing.T) {
	for _, tc := range parityMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			handWritten, err := ConvertChatRequestToAnthropic([]byte(tc.in))
			if err != nil {
				t.Fatalf("handwritten converter error: %v", err)
			}
			hwCanonical := canonicalJSON(t, handWritten)
			hwGolden, updated := loadOrUpdateGolden(t, goldenPath(tc.name, "handwritten"), hwCanonical)
			if !updated && hwGolden != hwCanonical {
				t.Fatalf("handwritten output drifted from golden:\n golden: %s\n actual: %s", hwGolden, hwCanonical)
			}

			irBody, err := ir.ParseOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatalf("ir.ParseOpenAI error: %v", err)
			}
			irOut, err := ir.SerializeAnthropic(irBody)
			if err != nil {
				t.Fatalf("ir.SerializeAnthropic error: %v", err)
			}
			irCanonical := canonicalJSON(t, irOut)
			irGolden, updated := loadOrUpdateGolden(t, goldenPath(tc.name, "ir"), irCanonical)
			if !updated && irGolden != irCanonical {
				t.Fatalf("ir output drifted from golden:\n golden: %s\n actual: %s", irGolden, irCanonical)
			}

			// Everything not covered by a ruling must match field-for-field.
			diffs := jsonDiff(decode(t, handWritten), decode(t, irOut), "$")
			allowed := parityAllowedDiffs(tc.name)
			var unexpected []string
			for _, d := range diffs {
				matched := false
				for _, a := range allowed {
					if strings.Contains(d, a) {
						matched = true
						break
					}
				}
				if !matched {
					unexpected = append(unexpected, d)
				}
			}
			if len(unexpected) > 0 {
				t.Fatalf("unadjudicated differences between handwritten and IR (all diffs):\n  %s",
					strings.Join(diffs, "\n  "))
			}
			if len(diffs) > 0 {
				t.Logf("case %s: %d adjudicated difference(s)", tc.name, len(diffs))
			}
		})
	}
}

// parityAllowedDiffs encodes the per-case rulings (see the file-level
// adjudication table). Each entry is a substring matched against a diff line;
// comments carry the ruling reference.
func parityAllowedDiffs(caseName string) []string {
	switch caseName {
	case "single_user_text":
		// max_tokens converter-default ruling.
		return []string{"$.max_tokens"}
	case "system_string_multi_turn":
		// max_tokens default + IR lossless unknown-field preservation
		// ("system_ignored" probe: kept with ir_unknown_field anomaly).
		return []string{"$.max_tokens", "$.system_ignored"}
	case "system_array_joins":
		// GAP-1 closed (Wave5): system now joins all text blocks; only the
		// max_tokens converter-default ruling remains.
		return []string{"$.max_tokens"}
	case "sampling_and_stop_and_user":
		// Fully converged: no rulings expected.
		return nil
	case "multi_function_tools_and_choice":
		// GAP-2 closed (Wave5): named tool_choice converges to
		// {"type":"tool","name":X} on both sides; input pins max_tokens so
		// no default ruling needed.
		return nil
	case "assistant_tool_calls_then_tool_result":
		// GAP-3 closed (Wave5): empty assistant text block suppressed;
		// input pins max_tokens so no default ruling needed.
		return nil
	case "multimodal_image_data_uri_and_https":
		return []string{"$.max_tokens"}
	case "ir_thinking_extension":
		// IR-only reasoning_effort → thinking capability + default ruling.
		return []string{"$.max_tokens", "$.thinking"}
	case "no_max_tokens_default":
		// max_tokens converter-default ruling.
		return []string{"$.max_tokens"}
	case "nonfunction_tool_passthrough":
		return []string{"$.max_tokens"}
	default:
		return nil
	}
}
