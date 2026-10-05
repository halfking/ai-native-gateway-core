package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestProviderCodeLookupUsesDisplayNameColumn 回归测试：
//
// 旧版 LiveStreamSSEHub.ProviderCodeFor / ProviderCodeForCredential 里的 SQL
// 引用了 providers.name 列，但 sql/schema/01-schema.sql 里 providers 表只
// 有 display_name 没有 name —— pgx 会返回 ERROR: column "name" does not
// exist (42703) 并被忽略成空字符串，前端泳道就把供应商显示成"未知"。
//
// 老板反馈 Minimax 的 minimax-m3 在供应商泳道里显示空/未知，根因就在这里。
//
// 本测试只做静态校验：保证 ProviderCodeFor / ProviderCodeForCredential 的 SQL
// 不再引用 providers.name 列。如果有任何人无意回退这个修复，本测试会失败。
func TestProviderCodeLookupUsesDisplayNameColumn(t *testing.T) {
	cases := []struct {
		name string
		want string
		body string
	}{
		// providers table has no `name` column — fall back to providers.display_name.
		{"ProviderCodeFor", "NULLIF(display_name", providerCodeForSQLBody},
		// credentials join: must use p.display_name (column rename, not display_name bare).
		{"ProviderCodeForCredential", "p.display_name", providerCodeForCredentialSQLBody},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Contains(c.body, "NULLIF(name") {
				t.Fatalf("%s SQL still references NULLIF(name, …); expected %s. body=\n%s", c.name, c.want, c.body)
			}
			if !strings.Contains(c.body, c.want) {
				t.Fatalf("%s SQL does not reference %s; column rename may be incomplete. body=\n%s", c.name, c.want, c.body)
			}
			if !strings.Contains(c.body, "FROM providers") && !strings.Contains(c.body, "JOIN providers") {
				t.Fatalf("%s SQL lost its reference to providers? body=\n%s", c.name, c.body)
			}
		})
	}
}

// TestLiveNodeStatusDisableProjectionFieldContract 锚定 OBS-BE4 (V3.3-OBS)
// node_update 新增投影字段的 JSON 契约：
//
//   - 全部字段 optional（omitempty）：健康/缺省节点不出现这些键，旧客户端忽略；
//   - 出现时类型与语义：fp_disabled=true（false 省略）、RFC3339 时间戳、
//     disable_kind ∈ {manual, system}。
//
// ADR-V3-103：LiveNodeStatus 只是投影，不落任何新状态存储。
func TestLiveNodeStatusDisableProjectionFieldContract(t *testing.T) {
	// 缺省（健康）节点：新字段必须整体缺失，不得出现零值噪声。
	minimal := LiveNodeStatus{CredentialID: 1, ProviderID: 2, ProviderCode: "anthropic"}
	b, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("marshal minimal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal minimal: %v", err)
	}
	for _, key := range []string{"fp_disabled", "fp_disabled_until", "disable_kind", "system_recover_at", "last_error_at"} {
		if _, ok := m[key]; ok {
			t.Fatalf("healthy node must omit %q, got payload %s", key, b)
		}
	}
	if _, ok := m["credential_id"]; !ok {
		t.Fatalf("legacy credential_id must remain non-optional, got %s", b)
	}

	// 禁用节点：全部字段出现且类型正确。
	disabledAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Add(5 * time.Minute)
	recoverAt := time.Date(2026, 8, 15, 12, 2, 0, 0, time.UTC)
	lastErrAt := time.Date(2026, 8, 15, 11, 58, 0, 0, time.UTC)
	trueVal := true
	full := LiveNodeStatus{
		CredentialID:    7,
		FPDisabled:      &trueVal,
		FPDisabledUntil: &disabledAt,
		DisableKind:     "system",
		SystemRecoverAt: &recoverAt,
		LastErrorAt:     &lastErrAt,
	}
	b, err = json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	if v, ok := m["fp_disabled"]; !ok || v != true {
		t.Fatalf("fp_disabled must be true when set, got %v (present=%v)", v, ok)
	}
	if v, ok := m["disable_kind"]; !ok || v != "system" {
		t.Fatalf("disable_kind must be \"system\" when set, got %v (present=%v)", v, ok)
	}
	for key, want := range map[string]time.Time{
		"fp_disabled_until": disabledAt,
		"system_recover_at": recoverAt,
		"last_error_at":     lastErrAt,
	} {
		v, ok := m[key]
		if !ok {
			t.Fatalf("%s must be present when set, payload %s", key, b)
		}
		s, isStr := v.(string)
		if !isStr {
			t.Fatalf("%s must serialise as RFC3339 string, got %T (%v)", key, v, v)
		}
		got, err := time.Parse(time.RFC3339, s)
		if err != nil || !got.Equal(want) {
			t.Fatalf("%s = %q, want %q (err=%v)", key, s, want.Format(time.RFC3339), err)
		}
	}

	// fp_disabled 是 *bool：nil（无 fpslot 数据 / 未禁用）必须省略。
	// Provider 侧只在 true 时赋值，因此线上不会出现 "fp_disabled":false。
	falseVal := false
	onlyFalse := LiveNodeStatus{CredentialID: 9, FPDisabled: &falseVal}
	b, err = json.Marshal(onlyFalse)
	if err != nil {
		t.Fatalf("marshal onlyFalse: %v", err)
	}
	// Go 的 *bool+omitempty 只对 nil 省略；显式 &false 会序列化成 false，
	// 这里固定该语义，防止有人把字段改成裸 bool（那样 false 值零噪声会
	// 出现在每个健康节点上，违背 optional 契约）。
	if !strings.Contains(string(b), `"fp_disabled":false`) {
		t.Fatalf("explicit &false must serialise as false (pointer omitempty is nil-only), got %s", b)
	}
}

// TestLiveNodeStatusDisableProjectionThroughEnvelope 确认新字段经
// node_update envelope（Nodes 数组）原样透传——initial_data 与 node_update
// 共用同一 LiveNodeStatus wire shape。
func TestLiveNodeStatusDisableProjectionThroughEnvelope(t *testing.T) {
	until := time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)
	trueVal := true
	env := LiveStreamEnvelope{
		Type: "node_update",
		Nodes: []LiveNodeStatus{{
			CredentialID:    3,
			FPDisabled:      &trueVal,
			FPDisabledUntil: &until,
			DisableKind:     "manual",
		}},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"fp_disabled":true`, `"disable_kind":"manual"`, `"fp_disabled_until":"` + until.Format(time.RFC3339) + `"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("node_update envelope missing %s, payload %s", want, s)
		}
	}
}

// TestLiveNodeStatusRawModelsFieldContract 锚定 OBS-UI (2026-08-17)：
// LiveNodeStatus 新增 raw_models（路由可见模型名列表）字段的 JSON 契约。
//
//   - 字段 optional（omitempty）：未上报时整键缺失，前端按缺省隐藏"按模型
//     分组"区块，禁止零值冒充（[] 会被省略但 nil/len==0 都不渲染）。
//   - 出现时为 []string，元素为原始模型名（与 credential_model_bindings
//     JOIN provider_models 投影一致）。
//   - 透传到 node_update envelope（initial_data / node_update 共享
//     LiveNodeStatus wire shape）。
func TestLiveNodeStatusRawModelsFieldContract(t *testing.T) {
	// 健康/未上报节点：键必须缺失。
	minimal := LiveNodeStatus{CredentialID: 1, ProviderID: 2, ProviderCode: "anthropic"}
	b, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("marshal minimal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal minimal: %v", err)
	}
	if _, ok := m["raw_models"]; ok {
		t.Fatalf("healthy node must omit raw_models, got payload %s", b)
	}
	if _, ok := m["credential_id"]; !ok {
		t.Fatalf("legacy credential_id must remain non-optional, got %s", b)
	}

	// 上报节点：键出现且元素是字符串。
	full := LiveNodeStatus{
		CredentialID: 7,
		ProviderCode: "openai",
		RawModels:    []string{"gpt-4o", "gpt-4o-mini"},
	}
	b, err = json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	v, ok := m["raw_models"]
	if !ok {
		t.Fatalf("raw_models must be present when set, payload %s", b)
	}
	arr, isArr := v.([]any)
	if !isArr {
		t.Fatalf("raw_models must serialise as JSON array, got %T (%v)", v, v)
	}
	if len(arr) != 2 || arr[0] != "gpt-4o" || arr[1] != "gpt-4o-mini" {
		t.Fatalf("raw_models elements mismatch, got %v", arr)
	}

	// 显式空切片：omitempty 语义下 len==0 会省略——与"未上报"在线上一致。
	empty := LiveNodeStatus{CredentialID: 9, ProviderCode: "anthropic", RawModels: []string{}}
	b, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(b), `"raw_models"`) {
		t.Fatalf("empty raw_models slice must be omitted (omitempty), got %s", b)
	}
}

// TestLiveNodeStatusRawModelsThroughEnvelope 确认 raw_models 字段经
// node_update envelope 原样透传。
func TestLiveNodeStatusRawModelsThroughEnvelope(t *testing.T) {
	env := LiveStreamEnvelope{
		Type: "node_update",
		Nodes: []LiveNodeStatus{{
			CredentialID: 3,
			ProviderCode: "openai",
			RawModels:    []string{"gpt-4o"},
		}},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"raw_models":["gpt-4o"]`) {
		t.Fatalf("node_update envelope missing raw_models, payload %s", s)
	}
}
