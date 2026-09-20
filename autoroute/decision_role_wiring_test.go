package autoroute

// decision_role_wiring_test.go — R48（2026-09-20）Decide/DecideV2 管线
// role × kind promotion 的端到端接线测试：
//   1. flag 开启 + worker 角色 + 搜索任务 → 轻量池模型被提升，
//      RoutingSource=role_route，审计字段 SessionRole/TaskKind 落值；
//   2. flag 关闭 → 决策结果与无角色信号时完全一致（字节级不变的
//      行为级等价断言：同候选、同 winner、审计字段零值）；
//   3. main 角色不介入 promotion；
//   4. 会话角色跨轮变化 → intent 缓存失效重判；
//   5. DecideV2（生产默认路径）同样命中 role_route。
//
// 复用 decision_test.go 的 stubClassifier/stubIndex（同包）。

import (
	"context"
	"testing"
	"time"
)

// roleTestCandidates 构造重模型分高在前、轻模型分低在后的候选池：
// 无 role 路由时 winner 恒为 glm-5.3（重量模型），role 路由命中后
// 应翻转为 minimax-m3（轻量池 search 首选）。
func roleTestCandidates() []ScoredCandidate {
	return []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "glm-5.3", CredentialID: 1, RawModel: "glm-5.3"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "minimax-m3", CredentialID: 2, RawModel: "minimax-m3"}, Breakdown: ScoringBreakdown{Composite: 40}},
		{Candidate: Candidate{CanonicalName: "deepseek-v4-flash", CredentialID: 3, RawModel: "deepseek-v4-flash"}, Breakdown: ScoringBreakdown{Composite: 35}},
	}
}

func newRoleTestDecider(t *testing.T) *Decider {
	t.Helper()
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	d := NewDecider(cls, nil, &stubIndex{cands: roleTestCandidates()}, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil)) // 内存默认表（无 DB）
	return d
}

func roleSearchSignals(role AgentRole) ClassificationSignals {
	return ClassificationSignals{
		AgentRole:      role,
		LastUserPrompt: "帮我搜索一下 pg 索引优化的资料", // kind=search
	}
}

func TestDecide_RoleRouting_PromotesLightModel(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	d := newRoleTestDecider(t)
	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "minimax-m3" {
		t.Fatalf("worker+search should promote minimax-m3, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
	if dec.SessionRole != "worker" || dec.TaskKind != "search" {
		t.Fatalf("audit fields: role=%q kind=%q, want worker/search", dec.SessionRole, dec.TaskKind)
	}
}

func TestDecide_RoleRouting_FlagOff_IdenticalToNoRole(t *testing.T) {
	// flag-off 时带角色头与不带角色头的决策完全一致（角色信号被整体
	// 忽略，审计字段零值）。
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{})
	defer SetGlobalFeatureFlagsForTest(old)

	d := newRoleTestDecider(t)
	withRole, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "s-off")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	d2 := newRoleTestDecider(t)
	noRole := roleSearchSignals(RoleWorker)
	noRole.AgentRole = ""
	withoutRole, err := d2.Decide(context.Background(), noRole, 0, "", "", "s-off-2")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if withRole.ChosenModel != withoutRole.ChosenModel || withRole.ChosenModel != "glm-5.3" {
		t.Fatalf("flag-off must keep default winner: with=%s without=%s", withRole.ChosenModel, withoutRole.ChosenModel)
	}
	if withRole.RoutingSource != withoutRole.RoutingSource {
		t.Fatalf("flag-off RoutingSource diverged: %q vs %q", withRole.RoutingSource, withoutRole.RoutingSource)
	}
	if withRole.SessionRole != "" || withRole.TaskKind != "" {
		t.Fatalf("flag-off audit fields must stay empty: role=%q kind=%q", withRole.SessionRole, withRole.TaskKind)
	}
}

func TestDecide_RoleRouting_MainNotPromoted(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	d := newRoleTestDecider(t)
	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleMain), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "glm-5.3" {
		t.Fatalf("main must keep default winner, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource == "role_route" {
		t.Fatalf("main must not be role_route")
	}
	// flag 开启时审计字段仍记录观察值（无论是否命中提升）。
	if dec.SessionRole != "main" || dec.TaskKind != "search" {
		t.Fatalf("audit fields: role=%q kind=%q, want main/search", dec.SessionRole, dec.TaskKind)
	}
}

func TestDecide_RoleRouting_RoleChangeInvalidatesCache(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	d := newRoleTestDecider(t)
	ctx := context.Background()

	// 第一轮：worker 会话建立 intent 缓存（ChosenModel=minimax-m3）。
	first, err := d.Decide(ctx, roleSearchSignals(RoleWorker), 0, "", "", "sess-role")
	if err != nil {
		t.Fatalf("first Decide err: %v", err)
	}
	if first.ChosenModel != "minimax-m3" || first.RoutingSource != "role_route" {
		t.Fatalf("first: model=%s source=%s", first.ChosenModel, first.RoutingSource)
	}
	// 缓存里带上了角色（R48 CachedIntent.Role）。
	cached, ok := d.intentCache.Get("sess-role")
	if !ok || cached.Role != RoleWorker || cached.Kind != KindSearch {
		t.Fatalf("cached intent role/kind: ok=%v role=%q kind=%q", ok, cached.Role, cached.Kind)
	}

	// 第二轮：同 session_id 但角色变为 planner → 身份变化，缓存必须失效，
	// 走完整重判（Classifier 不能是 session_cache），仍命中 role_route。
	second, err := d.Decide(ctx, roleSearchSignals(RolePlanner), 0, "", "", "sess-role")
	if err != nil {
		t.Fatalf("second Decide err: %v", err)
	}
	if second.Classifier == "session_cache" {
		t.Fatalf("role change must invalidate cache, got session_cache reuse")
	}
	if second.SessionRole != "planner" {
		t.Fatalf("second decision role: got %q", second.SessionRole)
	}
	if second.RoutingSource != "role_route" || second.ChosenModel != "minimax-m3" {
		t.Fatalf("second: model=%s source=%s", second.ChosenModel, second.RoutingSource)
	}
}

func TestDecide_RoleRouting_PreferenceFallback(t *testing.T) {
	// 候选池缺首选（minimax-m3 不在）→ 依次尝试备选 glm-5.3-flash；
	// 也不在 → 静默让位保持原 winner。
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	// 池里只有 glm-5.3 和 deepseek-v4-flash（search 备选 glm-5.3-flash 缺席，
	// 其余 kind 的首选也不在；deepseek-v4-flash 是 ops 首选）。
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "glm-5.3", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "deepseek-v4-flash", CredentialID: 3}, Breakdown: ScoringBreakdown{Composite: 35}},
	}}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	// search 偏好 [minimax-m3, glm-5.3-flash] 全不在场 → 让位，winner 不变。
	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "glm-5.3" || dec.RoutingSource == "role_route" {
		t.Fatalf("no pref present should keep default: model=%s source=%s", dec.ChosenModel, dec.RoutingSource)
	}

	// ops 偏好 [deepseek-v4-flash, kimi-k3]：首选在场 → 提升。
	opsSig := roleSearchSignals(RoleWorker)
	opsSig.LastUserPrompt = "帮我重启服务并清理磁盘"
	dec2, err := d.Decide(context.Background(), opsSig, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec2.ChosenModel != "deepseek-v4-flash" || dec2.RoutingSource != "role_route" {
		t.Fatalf("ops should promote deepseek-v4-flash: model=%s source=%s", dec2.ChosenModel, dec2.RoutingSource)
	}
}

// ── DecideV2（生产默认路径，channel-quality routing 开启时）──

func TestDecideV2_RoleRouting_PromotesLightModel(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
		AutoRoleRoutingEnabled:   true,
	})
	defer SetGlobalFeatureFlagsForTest(old)

	// 真实 *Index（DecideV2 类型断言要求）：重模型成功率更高，自然评分
	// 下 winner=glm-5.3；role 路由应翻转成 minimax-m3 并标 role_route。
	// 构造方式对齐 decision_v2_routing_source_test.go 的 newV2Index。
	idx := &Index{
		entries: []Candidate{
			{CredentialID: 1, CanonicalID: 1, CanonicalName: "glm-5.3", Tags: []string{"chat"}, SuccessRate: 0.95},
			{CredentialID: 2, CanonicalID: 2, CanonicalName: "minimax-m3", Tags: []string{"chat"}, SuccessRate: 0.90},
		},
		lastRefresh: time.Now(),
	}
	cls := &v2TestClassifier{task: TaskChat}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	dec, err := d.DecideV2(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if dec.ChosenModel != "minimax-m3" {
		t.Fatalf("V2 worker+search should promote minimax-m3, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("V2 RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
	if dec.SessionRole != "worker" || dec.TaskKind != "search" {
		t.Fatalf("V2 audit fields: role=%q kind=%q", dec.SessionRole, dec.TaskKind)
	}
}

func TestDecideV2_RoleRouting_FlagOff_Unchanged(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(old)

	idx := &Index{
		entries: []Candidate{
			{CredentialID: 1, CanonicalID: 1, CanonicalName: "glm-5.3", Tags: []string{"chat"}, SuccessRate: 0.95},
			{CredentialID: 2, CanonicalID: 2, CanonicalName: "minimax-m3", Tags: []string{"chat"}, SuccessRate: 0.90},
		},
		lastRefresh: time.Now(),
	}
	cls := &v2TestClassifier{task: TaskChat}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	dec, err := d.DecideV2(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if dec.ChosenModel != "glm-5.3" {
		t.Fatalf("flag-off V2 must keep default winner, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "implicit_tag" {
		t.Fatalf("flag-off V2 RoutingSource: got %q", dec.RoutingSource)
	}
	if dec.SessionRole != "" || dec.TaskKind != "" {
		t.Fatalf("flag-off V2 audit fields must stay empty: %q/%q", dec.SessionRole, dec.TaskKind)
	}
}

// tierFilterStore 构造一个 work-type tier 路由快照：chat 任务只配置
// glm-5.2（secondary）——刻意不含任何 role 偏好模型，复刻 245 e2e 实测
// 事故形态（worker+solution 的 gpt-5.6-sol 被 secondary 档硬过滤）。
func tierFilterStore() *WorkTypeRouteStore {
	wt := NewWorkTypeRouteStore(nil)
	wt.snapshot.Store(&wtRouteSnapshot{
		byTaskType: map[string][]WorkTypeRoute{
			"chat": {{WorkTypeKey: "acc-chat", L1TaskType: "chat", CanonicalName: "glm-5.2", Tier: "secondary", Weight: 1}},
		},
		byWorkTypeKey: map[string][]WorkTypeRoute{},
		workTypeL1:    map[string]string{},
	})
	return wt
}

// TestDecide_RoleRouting_SurvivesTierFilter —— R48 修订（2026-09-20，245
// e2e 实测抓到）：work-type tier 硬过滤在 role promotion 之前执行，若
// tier 配置不含偏好模型，gpt-5.6-sol 被滤除 → role_route 静默让位，
// 违背文档化级联 pin > role_route > work_type tier。修订后 role 偏好
// 并入 tier 豁免名单（无 pin 提升语义），必须仍命中 role_route。
func TestDecide_RoleRouting_SurvivesTierFilter(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	// 候选池：glm-5.2 分高在前（tier 命中者），gpt-5.6-sol 分低在尾部。
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "glm-5.2", CredentialID: 11, RawModel: "glm-5.2"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "glm-5.3", CredentialID: 12, RawModel: "glm-5.3"}, Breakdown: ScoringBreakdown{Composite: 70}},
		{Candidate: Candidate{CanonicalName: "gpt-5.6-sol", CredentialID: 13, RawModel: "gpt-5.6-sol"}, Breakdown: ScoringBreakdown{Composite: 30}},
	}
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test"}}
	d := NewDecider(cls, nil, &stubIndex{cands: cands}, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))
	d.SetWorkTypeRouteStore(tierFilterStore())

	dec, err := d.Decide(context.Background(), ClassificationSignals{
		AgentRole:      RoleWorker,
		LastUserPrompt: "帮我写一份技术方案", // kind=solution → 重量池 gpt-5.6-sol
	}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "gpt-5.6-sol" {
		t.Fatalf("worker+solution must promote gpt-5.6-sol through tier filter, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
}

// TestDecideV2_RoleRouting_SurvivesTierFilter —— V2（生产默认路径）同修
// 的对应用例：tier 过滤后 gpt-5.6-sol 必须幸存并翻盘。
func TestDecideV2_RoleRouting_SurvivesTierFilter(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{AutoRoleRoutingEnabled: true})
	defer SetGlobalFeatureFlagsForTest(old)

	idx := &Index{
		entries: []Candidate{
			{CredentialID: 11, CanonicalID: 11, CanonicalName: "glm-5.2", Tags: []string{"chat"}, SuccessRate: 0.95},
			{CredentialID: 12, CanonicalID: 12, CanonicalName: "glm-5.3", Tags: []string{"chat"}, SuccessRate: 0.94},
			{CredentialID: 13, CanonicalID: 13, CanonicalName: "gpt-5.6-sol", Tags: []string{"chat"}, SuccessRate: 0.90},
		},
		lastRefresh: time.Now(),
	}
	cls := &v2TestClassifier{task: TaskChat}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))
	d.SetWorkTypeRouteStore(tierFilterStore())

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{
		AgentRole:      RoleWorker,
		LastUserPrompt: "帮我写一份技术方案",
	}, 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if dec.ChosenModel != "gpt-5.6-sol" {
		t.Fatalf("V2 worker+solution must promote gpt-5.6-sol through tier filter, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("V2 RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
}
