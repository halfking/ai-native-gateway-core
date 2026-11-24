package main

// main_reconciliation_shadow_test.go —— 对账影子台账开关的判定门
// （2026-11-24，12h 修订审计轮 L6）。
//
// ★ 这道门在防什么：
//   影子台账的开关此前是裸的一行 `os.Getenv(...) == "true" && …`，
//   **关闭时不留任何痕迹** ⇒ 运营侧看到 public.provider_call_attempts
//   是空表时，分不清「开关没开」与「真的没有调用」。
//   ⇒ 判定必须返回**未启用原因**，调用方据此打可观测日志。
//
// ★ 生产实测（211 / acc-pg17，2026-11-24 只读）：表在、零行、容器 env 里
//   没有这个开关 ⇒ 生产上就是「没开」⇒ 这正是 L6 的实锤场景。

import "testing"

func TestReconciliationShadowDecision(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		poolReady  bool
		wantEnable bool
		wantReason string
	}{
		{name: "enabled", env: "true", poolReady: true, wantEnable: true, wantReason: ""},
		{name: "switch off by unset", env: "", poolReady: true, wantEnable: false, wantReason: "switch_not_enabled"},
		{name: "switch off by false", env: "false", poolReady: true, wantEnable: false, wantReason: "switch_not_enabled"},
		{name: "switch off by one", env: "1", poolReady: true, wantEnable: false, wantReason: "switch_not_enabled"},
		{name: "switch off by other case", env: "TRUE", poolReady: true, wantEnable: false, wantReason: "switch_not_enabled"},
		{name: "switch off by padded", env: " true ", poolReady: true, wantEnable: false, wantReason: "switch_not_enabled"},
		{name: "no pool", env: "true", poolReady: false, wantEnable: false, wantReason: "database_pool_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, reason := reconciliationShadowDecision(tc.env, tc.poolReady)
			if enabled != tc.wantEnable {
				t.Fatalf("enabled = %v, want %v (reason %q)", enabled, tc.wantEnable, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			// 未启用时必须**给得出原因** ⇒ 否则调用方打不出可观测日志，
			// L6 就等于没修。
			if !enabled && reason == "" {
				t.Fatal("a disabled shadow ledger must carry a reason")
			}
		})
	}
}
