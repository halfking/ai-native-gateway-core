package autoroute

// role_llm_router_test.go — R48（2026-09-20）role × kind LLM 偏好路由器
// 单元测试：内存默认表（用户口径）、DB 快照覆盖、promoteFirstPresent
// 偏好顺序语义。

import (
	"reflect"
	"testing"
	"time"
)

func TestSelectLLM_BuiltinDefaults(t *testing.T) {
	r := NewRoleLLMRouter(nil) // 无 DB → 纯内存默认表
	cases := []struct {
		role AgentRole
		kind TaskKind
		want []string
	}{
		// 用户口径：轻量池（搜索/总结/git/运维/unknown 兜底）+ 重量池
		//（分析/规划/方案编写），主备顺序按 role_llm_router.go 表。
		{RoleWorker, KindSearch, []string{"minimax-m3", "glm-5.3-flash"}},
		{RoleWorker, KindSummarize, []string{"glm-5.3-flash", "minimax-m3"}},
		{RoleWorker, KindGitOps, []string{"kimi-k3", "deepseek-v4-flash"}},
		{RoleWorker, KindOps, []string{"deepseek-v4-flash", "kimi-k3"}},
		{RoleWorker, KindAnalysis, []string{"glm-5.3", "deepseek-v4-pro"}},
		{RoleWorker, KindPlanning, []string{"claude-opus-5", "glm-5.3"}},
		{RoleWorker, KindSolution, []string{"gpt-5.6-sol", "grok-4.6"}},
		// 用户口径 #4：无法确定任务类型时默认 glm-5.3-flash / minimax-m3。
		{RoleWorker, KindUnknown, []string{"glm-5.3-flash", "minimax-m3"}},
		// 三个子代理角色共用 kind→池映射。
		{RolePlanner, KindSearch, []string{"minimax-m3", "glm-5.3-flash"}},
		{RoleOrchestrator, KindAnalysis, []string{"glm-5.3", "deepseek-v4-pro"}},
	}
	for _, tc := range cases {
		got := r.SelectLLM(tc.role, tc.kind)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SelectLLM(%s, %s) = %v, want %v", tc.role, tc.kind, got, tc.want)
		}
	}
	// main / unknown 角色不介入（保持既有默认路由）。
	for _, role := range []AgentRole{RoleMain, RoleUnknown, AgentRole(""), AgentRole("bogus")} {
		if got := r.SelectLLM(role, KindSearch); got != nil {
			t.Errorf("SelectLLM(%q, search) = %v, want nil（role 不介入）", role, got)
		}
	}
}

// storeSnapshotForTest 同包注入 DB 快照（绕过 pgxpool），验证覆盖语义。
func storeSnapshotForTest(r *RoleLLMRouter, byRoleKind map[roleKindKey][]roleRouteEntry) {
	r.snapshot.Store(&roleRouteSnapshot{
		byRoleKind: byRoleKind,
		LoadedAt:   time.Now(),
		Version:    7,
	})
}

func TestSelectLLM_SnapshotOverride(t *testing.T) {
	r := NewRoleLLMRouter(nil)
	storeSnapshotForTest(r, map[roleKindKey][]roleRouteEntry{
		{Role: RoleWorker, Kind: KindSearch}: {
			{CanonicalName: "kimi-k3", Priority: 100},
			{CanonicalName: "minimax-m3", Priority: 110},
		},
		// 管理员显式给 main 配行 → DB 覆盖被尊重（内存默认表不含 main，
		// 但 admin 的显式配置优先于内置保守口径）。
		{Role: RoleMain, Kind: KindAnalysis}: {
			{CanonicalName: "grok-4.6", Priority: 100},
		},
	})
	// (worker, search) 被 DB 行整体覆盖（含顺序）。
	if got := r.SelectLLM(RoleWorker, KindSearch); !reflect.DeepEqual(got, []string{"kimi-k3", "minimax-m3"}) {
		t.Fatalf("DB override: got %v", got)
	}
	// 未覆盖的 (worker, ops) 回退内存默认表。
	if got := r.SelectLLM(RoleWorker, KindOps); !reflect.DeepEqual(got, []string{"deepseek-v4-flash", "kimi-k3"}) {
		t.Fatalf("builtin fallback: got %v", got)
	}
	// main 的显式 DB 行被尊重。
	if got := r.SelectLLM(RoleMain, KindAnalysis); !reflect.DeepEqual(got, []string{"grok-4.6"}) {
		t.Fatalf("main explicit row: got %v", got)
	}
	if r.Version() != 7 {
		t.Fatalf("Version: got %d, want 7", r.Version())
	}
}

func TestSelectLLM_ReturnsCopy(t *testing.T) {
	// 返回切片必须是拷贝——调用方排序/截断不得污染内存默认表。
	r := NewRoleLLMRouter(nil)
	got := r.SelectLLM(RoleWorker, KindSearch)
	got[0] = "mutated"
	again := r.SelectLLM(RoleWorker, KindSearch)
	if again[0] != "minimax-m3" {
		t.Fatalf("builtin table polluted: %v", again)
	}
}

func TestPromoteFirstPresent(t *testing.T) {
	cands := func(names ...string) []ScoredCandidate {
		out := make([]ScoredCandidate, 0, len(names))
		for _, n := range names {
			out = append(out, ScoredCandidate{Candidate: Candidate{CanonicalName: n}})
		}
		return out
	}
	// 首选在场（非首位）→ 提升且保持其余顺序。
	got, hit := promoteFirstPresent(cands("a", "b", "c"), []string{"b", "z"})
	if hit != "b" || got[0].Candidate.CanonicalName != "b" || len(got) != 3 {
		t.Fatalf("promote mid: hit=%q order=%v", hit, got)
	}
	// 首选已是 winner → 原样返回。
	got, hit = promoteFirstPresent(cands("a", "b"), []string{"a"})
	if hit != "a" || got[0].Candidate.CanonicalName != "a" || len(got) != 2 {
		t.Fatalf("already winner: hit=%q order=%v", hit, got)
	}
	// 首选缺席 → 依次尝试备选。
	got, hit = promoteFirstPresent(cands("a", "b", "c"), []string{"x", "c", "b"})
	if hit != "c" || got[0].Candidate.CanonicalName != "c" {
		t.Fatalf("fallback pref: hit=%q order=%v", hit, got)
	}
	// 全不在场 → 原样返回 + 空命中（role 路由静默让位）。
	got, hit = promoteFirstPresent(cands("a", "b"), []string{"x", "y"})
	if hit != "" || len(got) != 2 || got[0].Candidate.CanonicalName != "a" {
		t.Fatalf("no hit: hit=%q order=%v", hit, got)
	}
	// 空偏好/空候选。
	if _, hit = promoteFirstPresent(cands("a"), nil); hit != "" {
		t.Fatalf("nil prefs: hit=%q", hit)
	}
	if got, hit = promoteFirstPresent(nil, []string{"a"}); hit != "" || got != nil {
		t.Fatalf("nil candidates: hit=%q got=%v", hit, got)
	}
}
