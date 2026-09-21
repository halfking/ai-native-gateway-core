package autoroute

// session_role_test.go — R48（2026-09-20）会话角色识别单元测试：
// 枚举解析、头解析优先级、Source-Actor 推断、context 传递。
//
// 信任模型回归点：X-Gw-Agent-Role 是声明式提示（坏值静默降级 unknown），
// X-Gw-Source-Actor 推断仅认登记过的网关内部 loopback 组件——外部伪造的
// actor 头在到达本层之前已被 R35-R1 中间件剥离（结构性防伪造，见
// session_role.go 包注释）。

import (
	"context"
	"testing"
)

func TestParseAgentRole(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want AgentRole
	}{
		{"exact main", "main", RoleMain},
		{"exact worker", "worker", RoleWorker},
		{"exact planner", "planner", RolePlanner},
		{"exact orchestrator", "orchestrator", RoleOrchestrator},
		{"exact unknown", "unknown", RoleUnknown},
		{"case+space", "  Worker ", RoleWorker},
		{"upper", "PLANNER", RolePlanner},
		{"invalid", "subagent", RoleUnknown},
		{"empty", "", RoleUnknown},
		{"injection-ish", "worker,planner", RoleUnknown},
	}
	for _, tc := range cases {
		if got := ParseAgentRole(tc.raw); got != tc.want {
			t.Errorf("%s: ParseAgentRole(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

func TestResolveAgentRoleFromHeaders_HeaderPriority(t *testing.T) {
	// 显式角色头优先于 actor 推断。
	got := ResolveAgentRoleFromHeaders("planner", "auto-summary-generator")
	if got != RolePlanner {
		t.Fatalf("header should win: got %q, want planner", got)
	}
	// 声明头非法（unknown）时回退 actor 推断。
	got = ResolveAgentRoleFromHeaders("bogus-role", "auto-title-generator")
	if got != RoleWorker {
		t.Fatalf("actor fallback: got %q, want worker", got)
	}
	// 两路都无信息 → unknown（role 路由不介入）。
	got = ResolveAgentRoleFromHeaders("", "unknown-actor-xyz")
	if got != RoleUnknown {
		t.Fatalf("no signal: got %q, want unknown", got)
	}
	// 全空。
	got = ResolveAgentRoleFromHeaders("", "")
	if got != RoleUnknown {
		t.Fatalf("empty headers: got %q, want unknown", got)
	}
}

func TestInferRoleFromActor(t *testing.T) {
	// 已登记的网关内部 loopback 组件 → worker（摘要型轻任务）。
	registered := []string{
		"auto-title-generator", "auto-summary-generator", "session-summary",
	}
	for _, actor := range registered {
		if got := InferRoleFromActor(actor); got != RoleWorker {
			t.Errorf("InferRoleFromActor(%q) = %q, want worker", actor, got)
		}
	}
	// 未登记 actor / 空 / 大小写归一。
	if got := InferRoleFromActor("Auto-Title-Generator"); got != RoleWorker {
		t.Errorf("case-insensitive actor: got %q, want worker", got)
	}
	if got := InferRoleFromActor("some-new-internal-loopback"); got != RoleUnknown {
		t.Errorf("unregistered actor must be unknown, got %q", got)
	}
	if got := InferRoleFromActor(""); got != RoleUnknown {
		t.Errorf("empty actor: got %q, want unknown", got)
	}
}

func TestAgentRoleContext(t *testing.T) {
	ctx := context.Background()
	if got := AgentRoleFromContext(ctx); got != RoleUnknown {
		t.Fatalf("empty ctx: got %q, want unknown", got)
	}
	ctx = WithAgentRole(ctx, RoleWorker)
	if got := AgentRoleFromContext(ctx); got != RoleWorker {
		t.Fatalf("roundtrip: got %q, want worker", got)
	}
	// unknown/空值不入 context（避免占位值污染下游）。
	plain := WithAgentRole(ctx, RoleUnknown)
	if plain != ctx {
		t.Fatalf("unknown role should be a no-op")
	}
}

func TestNormalizeAgentRole(t *testing.T) {
	if got := normalizeAgentRole(""); got != RoleUnknown {
		t.Fatalf("empty: got %q, want unknown", got)
	}
	if got := normalizeAgentRole(RoleWorker); got != RoleWorker {
		t.Fatalf("worker: got %q", got)
	}
}
