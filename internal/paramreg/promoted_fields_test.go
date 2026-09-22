package paramreg

import (
	"encoding/json"
	"testing"
)

// 2026-09-21 audit (P1-1, P2-3, P2-4): three fields moved from
// KindPortable / KindDialectOnly to KindIRHandled so they can be
// type-checked and normalized at the IR layer rather than relying on
// Extensions passthrough. This test pins the new contract so a future
// refactor cannot quietly revert any of them.
func TestPromotedFieldsAreKindIRHandled(t *testing.T) {
	want := map[string]Dialect{
		"repetition_penalty": DialectVLLM,        // P1-1
		"mask_sensitive_info": DialectMiniMax,    // P2-3
		"bot_setting":        DialectMiniMax,     // P2-4
	}

	for field, probeDialect := range want {
		var spec *FieldSpec
		for i := range specs {
			if specs[i].Name == field {
				spec = &specs[i]
				break
			}
		}
		if spec == nil {
			t.Errorf("field %q is no longer registered in paramreg", field)
			continue
		}
		if spec.Kind != KindIRHandled {
			t.Errorf("%q Kind = %q, want KindIRHandled (P1-1/P2-3/P2-4 promotion)", field, spec.Kind)
		}
		if spec.IRPath == "" {
			t.Errorf("%q IRPath is empty; KindIRHandled fields must point at an IR path", field)
		}
		// Sanity: the probe dialect must be in the Dialects list — otherwise
		// the field would never reach the upstream it was promoted for.
		// R52 修订：空 Dialects 表示"通用字段，全方言允许发射"（见
		// IRFieldAllowedForDialect 与 registry.go:256 R52 设计注记），
		// 此时 probe-dialect 检查不适用，跳过。
		if len(spec.Dialects) == 0 {
			continue
		}
		found := false
		for _, d := range spec.Dialects {
			if d == probeDialect {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q.Dialects does not include %s (so promotion loses coverage)", field, probeDialect)
		}
	}
}

// Decide() must produce ActionTranslate (or ActionRestore) for the promoted
// fields when the source and target both include the dialect, so the
// extensions_restore path stays consistent with the IR-first parse.
func TestDecide_PromotedFieldsRoundTrip(t *testing.T) {
	cases := []struct {
		field string
		src   Dialect
		dst   Dialect
	}{
		{"repetition_penalty", DialectVLLM, DialectVLLM},
		{"repetition_penalty", DialectQwen, DialectVLLM},
		{"mask_sensitive_info", DialectMiniMax, DialectMiniMax},
		{"bot_setting", DialectMiniMax, DialectMiniMax},
	}
	for _, c := range cases {
		action, spec := Decide(c.field, c.src, c.dst)
		if spec == nil {
			t.Errorf("%s %s→%s: nil spec, want a registered spec", c.field, c.src, c.dst)
			continue
		}
		if action != ActionRestore && action != ActionTranslate {
			t.Errorf("%s %s→%s: action = %s, want ActionRestore or ActionTranslate",
				c.field, c.src, c.dst, action)
		}
		// Translate the value through to make sure the helper doesn't choke
		// on a representative JSON payload.
		outKey, _, action, _ := Apply(c.field, json.RawMessage(`1.05`), c.src, c.dst)
		if outKey == "" {
			t.Errorf("Apply(%s) returned an empty output key", c.field)
		}
		if action != ActionRestore && action != ActionTranslate {
			t.Errorf("Apply(%s) action = %s, want ActionRestore or ActionTranslate", c.field, action)
		}
	}
}
