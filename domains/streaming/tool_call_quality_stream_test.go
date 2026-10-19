package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

func qualityToolFrame(choice, tool int, id, name, args string) string {
	call := map[string]any{"index": tool, "function": map[string]any{"arguments": args}}
	if id != "" {
		call["id"] = id
	}
	if name != "" {
		call["function"].(map[string]any)["name"] = name
	}
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": choice, "delta": map[string]any{"tool_calls": []any{call}},
	}}})
	return "data: " + string(body) + "\n"
}

func TestStreamQualityKeepsToolIDsStableAcrossChoicesAndDeltas(t *testing.T) {
	for _, mode := range []string{QualityModeFix, QualityModeDetectOnly} {
		t.Run(mode, func(t *testing.T) {
			var flags []string
			var state map[string]int
			for _, step := range []struct {
				choice, tool int
				name, wantID string
				duplicate    bool
			}{
				{0, 0, "bash", "shared", false},
				{0, 0, "", "shared", false},
				{0, 1, "search", "shared__dup2", true},
				{0, 1, "", "shared__dup2", true},
				{1, 0, "read", "shared__dup3", true},
			} {
				frame := qualityToolFrame(step.choice, step.tool, "shared", step.name, "{}")
				out, nextFlags, nextState := ProcessStreamLine(frame, mode, flags, state)
				flags, state = nextFlags, nextState
				wantID := step.wantID
				if mode == QualityModeDetectOnly {
					wantID = "shared"
				}
				if !strings.Contains(out, `"id":"`+wantID+`"`) {
					t.Fatalf("tool identity changed: choice=%d tool=%d out=%s wantID=%s", step.choice, step.tool, out, wantID)
				}
				if strings.Contains(strings.Join(flags, ","), QualityFlagDuplicateToolID) != step.duplicate {
					t.Fatalf("duplicate detection confused continuation with a new tool: flags=%v step=%+v", flags, step)
				}
			}
		})
	}
}

func TestStreamQualityPreservesToolArgumentContinuation(t *testing.T) {
	for _, mode := range []string{QualityModeFix, QualityModeDetectOnly} {
		t.Run(mode, func(t *testing.T) {
			first := qualityToolFrame(0, 0, "call1", "bash", "")
			_, flags, state := ProcessStreamLine(first, mode, nil, nil)
			next := qualityToolFrame(0, 0, "", "", `{"command":"git status"}`)
			out, flags, _ := ProcessStreamLine(next, mode, flags, state)
			if out != next || len(flags) != 0 {
				t.Fatalf("valid argument continuation changed or flagged: out=%s flags=%v", out, flags)
			}
		})
	}
}

func TestStreamQualityExplicitEmptyNameNeverInventsATool(t *testing.T) {
	for _, known := range []bool{false, true} {
		var flags []string
		var state map[string]int
		if known {
			_, flags, state = ProcessStreamLine(qualityToolFrame(0, 0, "call1", "bash", ""), QualityModeFix, nil, nil)
		}
		frame := strings.Replace(qualityToolFrame(0, 0, "", "", "{}"), `"function":{`, `"function":{"name":"",`, 1)
		out, flags, _ := ProcessStreamLine(frame, QualityModeFix, flags, state)
		wantFlag := ""
		if !known {
			wantFlag = QualityFlagEmptyToolName
		}
		if out != frame || strings.Join(flags, ",") != wantFlag {
			t.Fatalf("empty name invented a tool or misclassified continuation: known=%v out=%s flags=%v", known, out, flags)
		}
	}
}
