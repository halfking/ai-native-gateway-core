package db

import (
	"testing"
)

// TestPoolCapForInstance 覆盖「池子上限随实例容量自适应」的判据。
//
// ★ 判据（★ 2026-10-30 · 211 生产读数）：
//
//	★ cap = clamp(instanceMax/8, 4, 32)
//	★ 100 槽 ⇒ 12（★ 211 共享实例：★ 网关一家 idle 就占 19~30 槽，★ 并发实测 0~1）
//	★ 1000 槽 ⇒ 32（★ 大集群**行为完全不变** ⇒ 零回归面）
//	★ 未知（探针失败）⇒ 32（★ fail-open：★ 保持既有行为）
func TestPoolCapForInstance(t *testing.T) {
	cases := []struct {
		name        string
		instanceMax int32
		want        int32
	}{
		{name: "211 shared instance: 100 slots", instanceMax: 100, want: 12},
		{name: "184 cluster: 1000 slots keeps 32", instanceMax: 1000, want: 32},
		{name: "between: 200 slots", instanceMax: 200, want: 25},
		{name: "between: 400 slots", instanceMax: 400, want: 32},
		{name: "floor: 8 slots", instanceMax: 8, want: 4},
		{name: "floor: 32 slots", instanceMax: 32, want: 4},
		{name: "unknown (probe failed) keeps default", instanceMax: 0, want: 32},
		{name: "negative (probe failed) keeps default", instanceMax: -1, want: 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolCapForInstance(tc.instanceMax); got != tc.want {
				t.Fatalf("poolCapForInstance(%d) = %d, want %d", tc.instanceMax, got, tc.want)
			}
		})
	}
}

// TestPoolCapForInstanceNeverExceedsInstanceShare 钉住「不许超过实例的 1/8」
// 与「不许超过 32」两条上限（★ 防止有人把分母改小）。
//
// ★ 注：★ 下限 4 会让**极小实例**（instanceMax < 32）的 cap 合法地大于 instanceMax/8
// ⇒ ★ 不变式写成 cap ≤ max(4, instanceMax/8)（★ 上下限 clamp 的定义式）。
func TestPoolCapForInstanceNeverExceedsInstanceShare(t *testing.T) {
	for instanceMax := int32(1); instanceMax <= 2000; instanceMax++ {
		got := poolCapForInstance(instanceMax)
		if got > 32 {
			t.Fatalf("instanceMax=%d ⇒ cap=%d exceeds the absolute cap 32", instanceMax, got)
		}
		share := instanceMax / 8
		if share < 4 {
			share = 4
		}
		if got > share {
			t.Fatalf("instanceMax=%d ⇒ cap=%d exceeds clamp(instanceMax/8, 4, 32) = %d", instanceMax, got, share)
		}
		if got < 4 {
			t.Fatalf("instanceMax=%d ⇒ cap=%d below the floor 4", instanceMax, got)
		}
	}
}

// TestPoolMaxConnsEnvExplicit 钉住「运维显式值优先于实例自适应」。
func TestPoolMaxConnsEnvExplicit(t *testing.T) {
	cases := []struct {
		name string
		env  string
		set  bool
		want bool
	}{
		{name: "unset", env: "", set: false, want: false},
		{name: "valid positive", env: "12", set: true, want: true},
		{name: "spaced valid", env: " 24 ", set: true, want: true},
		{name: "zero is not an explicit choice", env: "0", set: true, want: false},
		{name: "negative is not an explicit choice", env: "-5", set: true, want: false},
		{name: "garbage is not an explicit choice", env: "abc", set: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", tc.env)
			}
			if got := poolMaxConnsEnvExplicit(); got != tc.want {
				t.Fatalf("poolMaxConnsEnvExplicit() with env %q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestPoolCapRespectsExplicitEnvOverride 端到端口径：显式设了 env ⇒
// 池上限**不被**实例自适应改写（★ 运维意图优先于自动推导）。
func TestPoolCapRespectsExplicitEnvOverride(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", "20")
	if !poolMaxConnsEnvExplicit() {
		t.Fatal("explicit env must be recognized as explicit")
	}
	if got := poolMaxConnsFromEnv(32); got != 20 {
		t.Fatalf("poolMaxConnsFromEnv(32) with explicit env = %d, want 20", got)
	}
	// ★ 未显式设时，实例自适应才生效（★ 32 → 12 for a 100-slot instance）。
	t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", "")
	if poolMaxConnsEnvExplicit() {
		t.Fatal("unset env must not be treated as explicit")
	}
	if got := poolCapForInstance(100); got != 12 {
		t.Fatalf("poolCapForInstance(100) = %d, want 12", got)
	}
}
