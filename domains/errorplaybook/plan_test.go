package errorplaybook

// 方案归一化/校验与动作白名单的钉测。
//
// ★ 这些是「手册能不能安全执行」的承重门：白名单外的动作、任意 URL、
//
//	非法方法、超大 payload 都必须在这里被拒绝（fail-closed）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func TestNormalizePlanScript(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	plan, err := NormalizePlan(PlanKindScript, mustJSON(t, map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{"action": "record_note", "params": map[string]interface{}{"message": "观察"}},
		},
	}), reg)
	if err != nil {
		t.Fatalf("valid script plan rejected: %v", err)
	}
	if plan.Kind != PlanKindScript || len(plan.Steps) != 1 || plan.Steps[0].Action != ActionRecordNote {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestNormalizePlanScriptRejectsUnknownAction(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	_, err := NormalizePlan("", mustJSON(t, map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{"action": "run_shell_command", "params": map[string]interface{}{"cmd": "rm -rf /"}},
		},
	}), reg)
	if err == nil || !strings.Contains(err.Error(), "not whitelisted") {
		t.Fatalf("unknown action must be rejected, got: %v", err)
	}
	// 任意形状（无 steps）也不该被当成 script。
	_, err = NormalizePlan("", mustJSON(t, map[string]interface{}{"cmd": "rm -rf /"}), reg)
	if err == nil {
		t.Fatal("shapeless payload must be rejected")
	}
}

func TestNormalizePlanScriptRejectsTooManySteps(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	steps := []interface{}{}
	for i := 0; i < maxPlanSteps+1; i++ {
		steps = append(steps, map[string]interface{}{"action": "record_note", "params": map[string]interface{}{"message": "x"}})
	}
	_, err := NormalizePlan(PlanKindScript, mustJSON(t, map[string]interface{}{"steps": steps}), reg)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("over-limit steps must be rejected, got: %v", err)
	}
}

func TestNormalizePlanAPI(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	plan, err := NormalizePlan(PlanKindAPI, mustJSON(t, map[string]interface{}{
		"method": "POST", "path": "/api/admin/pi-analysis/trigger", "body": map[string]interface{}{"batch_size": 5},
	}), reg)
	if err != nil {
		t.Fatalf("valid api plan rejected: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Action != ActionCallAdminAPI {
		t.Fatalf("api plan must normalize to a single call_admin_api step: %+v", plan)
	}
}

func TestNormalizePlanAPIRejectsNonAdminPath(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	for _, path := range []string{"http://evil.example.com/x", "/v1/chat/completions", "/api/tenant/whatever", ""} {
		_, err := NormalizePlan(PlanKindAPI, mustJSON(t, map[string]interface{}{"path": path}), reg)
		if err == nil {
			t.Fatalf("non-admin path %q must be rejected (SSRF gate)", path)
		}
	}
}

func TestNormalizePlanAPIRejectsBadMethod(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	_, err := NormalizePlan(PlanKindAPI, mustJSON(t, map[string]interface{}{
		"method": "TRACE", "path": "/api/admin/x",
	}), reg)
	if err == nil {
		t.Fatal("TRACE must be rejected")
	}
}

func TestNormalizePlanPrompt(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	plan, err := NormalizePlan("", mustJSON(t, map[string]interface{}{
		"prompt": "先核对错误体", "expect": "analysis",
	}), reg)
	if err != nil {
		t.Fatalf("valid prompt plan rejected: %v", err)
	}
	if plan.Kind != PlanKindPrompt || plan.Prompt == "" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestNormalizePlanPromptRejectsEmptyAndBadExpect(t *testing.T) {
	reg := NewBuiltinActions(nil, nil)
	if _, err := NormalizePlan(PlanKindPrompt, mustJSON(t, map[string]interface{}{"prompt": ""}), reg); err == nil {
		t.Fatal("empty prompt must be rejected")
	}
	if _, err := NormalizePlan(PlanKindPrompt, mustJSON(t, map[string]interface{}{
		"prompt": "x", "expect": "drop_tables",
	}), reg); err == nil {
		t.Fatal("bad expect must be rejected")
	}
}

func TestPlanMutating(t *testing.T) {
	cases := []struct {
		name string
		plan Plan
		want bool
	}{
		{"prompt", Plan{Kind: PlanKindPrompt, Prompt: "x"}, false},
		{"script note only", Plan{Kind: PlanKindScript, Steps: []PlanStep{{Action: ActionRecordNote}}}, false},
		{"script settings", Plan{Kind: PlanKindScript, Steps: []PlanStep{{Action: ActionSetSettingsKV}}}, true},
		{"script disable rule", Plan{Kind: PlanKindScript, Steps: []PlanStep{{Action: ActionDisablePIRule}}}, true},
		{"api GET", Plan{Kind: PlanKindAPI, Method: "GET", Steps: []PlanStep{{Action: ActionCallAdminAPI, Params: map[string]interface{}{"method": "GET"}}}}, false},
		{"api POST", Plan{Kind: PlanKindAPI, Method: "POST", Steps: []PlanStep{{Action: ActionCallAdminAPI, Params: map[string]interface{}{"method": "POST"}}}}, true},
	}
	for _, tc := range cases {
		if got := tc.plan.Mutating(); got != tc.want {
			t.Fatalf("%s: Mutating()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSetSettingsKVAllowlist(t *testing.T) {
	b := NewBuiltinActions(nil, nil)
	// 不在 allowlist 的键 ⇒ 在碰 DB 之前就拒绝。
	_, _, err := b.Execute(t.Context(), ActionSetSettingsKV, map[string]interface{}{
		"key": "routing.breaker.enabled", "value": "false",
	}, &Event{})
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("non-allowlisted key must be rejected, got: %v", err)
	}
	// 缺 value ⇒ 拒绝。
	_, _, err = b.Execute(t.Context(), ActionSetSettingsKV, map[string]interface{}{"key": "security.enabled"}, &Event{})
	if err == nil {
		t.Fatal("missing value must be rejected")
	}
}

func TestRecordNoteAction(t *testing.T) {
	b := NewBuiltinActions(nil, nil)
	snapshot, result, err := b.Execute(t.Context(), ActionRecordNote,
		map[string]interface{}{"message": "已核对，属误报"}, &Event{})
	if err != nil {
		t.Fatalf("record_note failed: %v", err)
	}
	if snapshot != nil || result != "已核对，属误报" {
		t.Fatalf("unexpected result: snapshot=%v result=%q", snapshot, result)
	}
}

func TestCallAdminAPIRejectsUnregisteredRoute(t *testing.T) {
	b := NewBuiltinActions(nil, nil)
	_, _, err := b.Execute(t.Context(), ActionCallAdminAPI, map[string]interface{}{
		"method": "POST", "path": "/api/admin/credentials/wipe",
	}, &Event{})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered route must fail-closed, got: %v", err)
	}
}

func TestNormalizePlanAPIRouteFailFast(t *testing.T) {
	// ★ 审计轮 F4 钉测：api 形态方案引用未登记路由 ⇒ **归一化期即拒绝**
	// （执行期才发现会制造「失败 → 提炼 → 再失败」循环）。
	b := NewBuiltinActions(nil, nil)
	_, err := NormalizePlan(PlanKindAPI, mustJSON(t, map[string]interface{}{
		"method": "POST", "path": "/api/admin/credentials/wipe",
	}), b)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered route must be rejected at normalize time, got: %v", err)
	}
	// 已登记路由（内置 pi-analysis/trigger）⇒ 通过。
	plan, err := NormalizePlan(PlanKindAPI, mustJSON(t, map[string]interface{}{
		"method": "POST", "path": "/api/admin/pi-analysis/trigger",
	}), b)
	if err != nil {
		t.Fatalf("registered route must pass: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestCallAdminAPIRegisteredRoute(t *testing.T) {
	called := false
	b := NewBuiltinActions(nil, []AdminRoute{{
		Method: "POST", Path: "/api/admin/test/echo",
		Handler: func(ctx context.Context, body json.RawMessage) (int, string, error) {
			called = true
			return 200, "pong", nil
		},
	}})
	_, result, err := b.Execute(t.Context(), ActionCallAdminAPI, map[string]interface{}{
		"method": "POST", "path": "/api/admin/test/echo",
	}, &Event{})
	if err != nil {
		t.Fatalf("registered route failed: %v", err)
	}
	if !called || !strings.Contains(result, "200") {
		t.Fatalf("route handler not invoked: called=%v result=%q", called, result)
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"noise {\"a\":{\"b\":\"}\"}} tail", `{"a":{"b":"}"}}`},
		{"no json here", ""},
	}
	for _, tc := range cases {
		if got := extractJSONObject(tc.in); got != tc.want {
			t.Fatalf("extract(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseProposal(t *testing.T) {
	p, err := ParseProposal("前缀噪声 {\"root_cause\":\"配额耗尽\",\"confidence\":0.9,\"plan_kind\":\"prompt\",\"plan\":{\"kind\":\"prompt\",\"prompt\":\"先核对\"}} 后缀")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if p.RootCause != "配额耗尽" || p.Confidence != 0.9 || p.PlanKind != "prompt" {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	if _, err := ParseProposal("完全没有 JSON"); err == nil {
		t.Fatal("no-JSON response must fail parsing")
	}
}
