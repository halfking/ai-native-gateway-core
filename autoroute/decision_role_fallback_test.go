package autoroute

// decision_role_fallback_test.go — R52（2026-10-01）role 路由**逐层回退**
// 的回归门。
//
// 被测契约：
//
//	role 路由按 kind 判定任务类型后，偏好列表必须是**逐层**的——
//	第 1 层 = SelectLLM 的 kind 偏好（多为轻量池低价模型），
//	第 2 层 = 主流兜底层（重量池）。整层不可用才进下一层；
//	两层皆不可用才静默让位给评分结果。
//
// 本文件特意区分两类门：
//   - **鉴别门**（failing-if-reverted）：候选池里唯一的主流模型**不是**
//     当前 winner。回退到 R51（无兜底层）时它必然变红。
//   - **护栏门**（不依赖修复、只防新缺陷）：轻量层在池时必须仍然先选
//     低价模型，兜底层不得抢占首选。回退修复不会让它变红，它防的是
//     "追加层插队"这一类修复自身引入的回归。
//
// 既有 TestDecide_RoleRouting_PreferenceFallback 的 search 分支**没有
// 鉴别力**：它的池是 {glm-5.3(90), deepseek-v4-flash(35)}，而 glm-5.3
// 既是主流层首项、又是池内天然 winner，promote 命中索引 0 空转——
// 加不加兜底层该门都绿。它现在被显式钉在 opt-out 口径上（见该文件）。

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// roleFallbackFlags 打开 role 路由 + 主流兜底层（生产默认口径）。
func roleFallbackFlags() *FeatureFlags {
	return &FeatureFlags{
		AutoRoleRoutingEnabled:     true,
		AutoRoleMainstreamFallback: true,
		UseChannelQualityRouting:   true,
	}
}

// ── 纯函数层 ──────────────────────────────────────────────────────

func TestWithMainstreamFallback_AppendsTailAndDedups(t *testing.T) {
	// 轻量 kind：两层完整展开，主流层全部在尾部。
	got := withMainstreamFallback([]string{"minimax-m3", "glm-5.3-flash"})
	if len(got) != 2+len(builtinMainstreamFallback) {
		t.Fatalf("len = %d, want %d", len(got), 2+len(builtinMainstreamFallback))
	}
	if got[0] != "minimax-m3" || got[1] != "glm-5.3-flash" {
		t.Fatalf("kind 偏好必须保持原序在前: %v", got[:2])
	}
	for i, want := range builtinMainstreamFallback {
		if got[2+i] != want {
			t.Fatalf("主流层第 %d 项 = %q, want %q", i, got[2+i], want)
		}
	}

	// 重量 kind：偏好本身已在主流池内，不得重复出现。
	heavy := withMainstreamFallback([]string{"glm-5.3", "deepseek-v4-pro"})
	want := []string{"glm-5.3", "deepseek-v4-pro", "claude-opus-5", "gpt-5.6-sol", "grok-4.6"}
	if !reflect.DeepEqual(heavy, want) {
		t.Fatalf("重量 kind 去重结果 = %v, want %v", heavy, want)
	}

	// 空偏好 = role 路由无意见，兜底层不该凭空造出偏好（否则 main/
	// unknown 角色会被兜底层拖进 role 路由）。
	if got := withMainstreamFallback(nil); got != nil {
		t.Fatalf("nil 偏好应返回 nil, got %v", got)
	}
}

func TestWithMainstreamFallback_DoesNotMutateInput(t *testing.T) {
	// SelectLLM 对 DB 快照返回的也是副本，但兜底层是**请求路径**上对
	// 它的再次加工：一旦入参被就地改写，DB 快照的排序语义会被污染且
	// 无任何报错（这正是 R49 修过一次的快照共享型缺陷）。
	in := []string{"minimax-m3"}
	_ = withMainstreamFallback(in)
	if !reflect.DeepEqual(in, []string{"minimax-m3"}) {
		t.Fatalf("入参被改写: %v", in)
	}
}

// ── 层号判定 ─────────────────────────────────────────────────────

func TestRolePrefPlan_LayerOf(t *testing.T) {
	plan := rolePrefPlan{
		prefs:        []string{"minimax-m3", "glm-5.3-flash", "glm-5.3", "claude-opus-5"},
		kindLayerLen: 2,
	}
	cases := map[string]int{
		"minimax-m3":    1, // kind 层
		"glm-5.3-flash": 1,
		"glm-5.3":       2, // 主流层
		"claude-opus-5": 2,
		"not-in-plan":   0,
		"":              0,
	}
	for hit, want := range cases {
		if got := plan.layerOf(hit); got != want {
			t.Errorf("layerOf(%q) = %d, want %d", hit, got, want)
		}
	}

	// 重量 kind 的边界：glm-5.3 既是 kind 偏好又是主流层成员，按**首次
	// 出现处**判层（kind 层），不能因为它也在主流表里就误报 mainstream。
	heavy := rolePrefPlan{
		prefs:        withMainstreamFallback([]string{"glm-5.3", "deepseek-v4-pro"}),
		kindLayerLen: 2,
	}
	if got := heavy.layerOf("glm-5.3"); got != 1 {
		t.Fatalf("重量 kind 的首选应判为第 1 层, got %d", got)
	}
	if got := layerName(heavy.layerOf("glm-5.3")); got != "kind" {
		t.Fatalf("layerName = %q, want kind", got)
	}
	if got := layerName(0); got != "" {
		t.Fatalf("未命中时 layerName 必须为空串（omitempty 字节级不变）, got %q", got)
	}
}

// ── 鉴别门：轻量层整层掉线 → 必须落到主流层 ──────────────────────

// fallbackDiscriminatingCandidates 构造"唯一主流模型不是 winner"的池：
// kimi-k3 分最高（kind=search 的偏好里**没有** kimi-k3，kimi-k3 是
// gitops 首选），claude-opus-5 在池内但分最低。
//   - R51（无兜底层）：让位 → winner=kimi-k3，source=implicit_tag。
//   - R52：layer1 全缺席 → layer2 命中 claude-opus-5 → role_route。
//
// 这条区分是本文件全部鉴别门的立足点：若把主流模型设成天然 winner，
// promote 会命中索引 0 空转，门将失去鉴别力。
func fallbackDiscriminatingCandidates() []ScoredCandidate {
	return []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "kimi-k3", CredentialID: 1, RawModel: "kimi-k3"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude-opus-5", CredentialID: 2, RawModel: "claude-opus-5"}, Breakdown: ScoringBreakdown{Composite: 20}},
	}
}

func TestDecide_RoleRouting_MainstreamFallbackWhenLightLayerAbsent(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(roleFallbackFlags())
	defer SetGlobalFeatureFlagsForTest(old)

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	idx := &stubIndex{cands: fallbackDiscriminatingCandidates()}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "claude-opus-5" {
		t.Fatalf("轻量层整层不可用时必须落到主流模型, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
	if dec.RoleFallbackLayer != "mainstream" {
		t.Fatalf("RoleFallbackLayer: got %q, want mainstream", dec.RoleFallbackLayer)
	}
}

// TestDecideV2_RoleRouting_MainstreamFallbackWhenLightLayerAbsent 是上面那条
// 在**生产默认路径**（DecideV2 + channel-quality routing）上的同款门。
// V1/V2 是两段独立代码，V1 绿不能替 V2 作证。
func TestDecideV2_RoleRouting_MainstreamFallbackWhenLightLayerAbsent(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(roleFallbackFlags())
	defer SetGlobalFeatureFlagsForTest(old)

	// 真实 *Index（DecideV2 要求类型断言）：成功率决定自然 winner。
	idx := &Index{
		entries: []Candidate{
			{CredentialID: 1, CanonicalID: 1, CanonicalName: "kimi-k3", Tags: []string{"chat"}, SuccessRate: 0.95},
			{CredentialID: 2, CanonicalID: 2, CanonicalName: "claude-opus-5", Tags: []string{"chat"}, SuccessRate: 0.30},
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
	if dec.ChosenModel != "claude-opus-5" {
		t.Fatalf("V2 轻量层整层不可用时必须落到主流模型, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "role_route" {
		t.Fatalf("V2 RoutingSource: got %q, want role_route", dec.RoutingSource)
	}
	if dec.RoleFallbackLayer != "mainstream" {
		t.Fatalf("V2 RoleFallbackLayer: got %q, want mainstream", dec.RoleFallbackLayer)
	}
}

// ── 护栏门：兜底层不得抢占低价首选 ───────────────────────────────

func TestDecide_RoleRouting_MainstreamLayerDoesNotOutrankLightLayer(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(roleFallbackFlags())
	defer SetGlobalFeatureFlagsForTest(old)

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	// 主流模型分最高、低价首选分最低——追加层一旦插队就会选错。
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "claude-opus-5", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 95}},
		{Candidate: Candidate{CanonicalName: "minimax-m3", CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 10}},
	}}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "minimax-m3" {
		t.Fatalf("轻量层在池时必须仍然选低价模型, got %s", dec.ChosenModel)
	}
	if dec.RoleFallbackLayer != "kind" {
		t.Fatalf("RoleFallbackLayer: got %q, want kind", dec.RoleFallbackLayer)
	}
}

// ── opt-out 门：关掉兜底层即回到 R51 口径 ───────────────────────

func TestDecide_RoleRouting_MainstreamFallbackOptOut(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		AutoRoleRoutingEnabled:     true,
		AutoRoleMainstreamFallback: false,
	})
	defer SetGlobalFeatureFlagsForTest(old)

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	idx := &stubIndex{cands: fallbackDiscriminatingCandidates()}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))

	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "kimi-k3" {
		t.Fatalf("opt-out 时必须回到让位语义, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource == "role_route" {
		t.Fatalf("opt-out 时不得标记 role_route, got %q", dec.RoutingSource)
	}
	if dec.RoleFallbackLayer != "" {
		t.Fatalf("opt-out 时 RoleFallbackLayer 必须为空（omitempty 字节级不变）, got %q", dec.RoleFallbackLayer)
	}
}

// ── 默认值门：兜底层默认开，与父开关默认关互不干扰 ───────────────

func TestDefaultFlags_MainstreamFallbackOnByDefault(t *testing.T) {
	def := DefaultFeatureFlags()
	if !def.AutoRoleMainstreamFallback {
		t.Fatal("主流兜底层默认必须为 true——「低价不可用就让位」本身是要修的缺陷")
	}
	if def.AutoRoleRoutingEnabled {
		t.Fatal("父开关 AutoRoleRoutingEnabled 必须仍默认 false（对既有部署零影响）")
	}
}

func TestLoadFeatureFlags_MainstreamFallbackOptOutEnv(t *testing.T) {
	// 环境变量名写进测试：开关名拼错时 getEnvBool 静默吃默认值，
	// 运维会以为关掉了兜底层其实没有。
	old := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(old)

	t.Setenv("AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK", "false")
	if got := LoadFeatureFlagsFromEnv(); got.AutoRoleMainstreamFallback {
		t.Fatal("AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK=false 未生效")
	}
	t.Setenv("AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK", "true")
	if got := LoadFeatureFlagsFromEnv(); !got.AutoRoleMainstreamFallback {
		t.Fatal("AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK=true 未生效")
	}
	t.Setenv("AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK", "")
	if got := LoadFeatureFlagsFromEnv(); !got.AutoRoleMainstreamFallback {
		t.Fatal("未设置时应回落到默认 true")
	}
}

// ── 审计续：生产走的是 DB 行，不是内存表 ──────────────────────────
//
// 上面所有门都用 NewRoleLLMRouter(nil)（内存默认表）。**生产不同**：
// 真库里 role_task_llm_mapping 有 48 行 / 24 组（本地实测 2026-10-01），
// SelectLLM 优先读 DB 快照，内存表只在无行时兜底。只测内存表等于
// 没测生产路径——DB 行整体覆盖语义下兜底层是否仍生效，必须单独钉。

func TestDecide_RoleRouting_MainstreamFallbackOnDBRowPath(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(roleFallbackFlags())
	defer SetGlobalFeatureFlagsForTest(old)

	router := NewRoleLLMRouter(nil)
	// 管理员为 worker×search 配的行：与内存表同形（轻量池两臂）。
	storeSnapshotForTest(router, map[roleKindKey][]roleRouteEntry{
		{Role: RoleWorker, Kind: KindSearch}: {
			{CanonicalName: "minimax-m3", Priority: 100},
			{CanonicalName: "glm-5.3-flash", Priority: 110},
		},
	})

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	d := NewDecider(cls, nil, &stubIndex{cands: fallbackDiscriminatingCandidates()}, NewMemoryProfileStore())
	d.SetRoleLLMRouter(router)

	dec, err := d.Decide(context.Background(), roleSearchSignals(RoleWorker), 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.ChosenModel != "claude-opus-5" {
		t.Fatalf("DB 行路径也必须逐层回退（生产实际走这条）, got %s", dec.ChosenModel)
	}
	if dec.RoleFallbackLayer != "mainstream" {
		t.Fatalf("DB 行路径 RoleFallbackLayer: got %q, want mainstream", dec.RoleFallbackLayer)
	}
}

// TestDecide_RoleRouting_MainstreamFallbackSurvivesSessionCache —— 审计字段
// 不得在缓存命中时丢成空串：首轮落主流模型后，后续同会话轮次复用缓存，
// 若 RoleFallbackLayer 不随缓存走，该会话在审计里表现成"没走过兜底"，
// 主流层用量被系统性低估。
func TestDecide_RoleRouting_MainstreamFallbackSurvivesSessionCache(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(roleFallbackFlags())
	defer SetGlobalFeatureFlagsForTest(old)

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic", Reason: "test",
	}}
	d := NewDecider(cls, nil, &stubIndex{cands: fallbackDiscriminatingCandidates()}, NewMemoryProfileStore())
	d.SetRoleLLMRouter(NewRoleLLMRouter(nil))
	d.SetIntentCache(NewSessionIntentCache(10 * time.Minute))

	ctx := context.Background()
	first, err := d.Decide(ctx, roleSearchSignals(RoleWorker), 0, "", "", "s-cache")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if first.RoleFallbackLayer != "mainstream" {
		t.Fatalf("首轮应落兜底层, got %q", first.RoleFallbackLayer)
	}
	second, err := d.Decide(ctx, roleSearchSignals(RoleWorker), 0, "", "", "s-cache")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	// 命中信号用 Classifier 而非 CacheReused：V1 缓存命中的分类器名是
	// "session_cache"，这是本路径最直接的证据（顺带钉住 R52 补齐的
	// CacheReused，避免它再次回退成 false）。
	if second.Classifier != "session_cache" {
		t.Fatalf("第二轮应命中会话缓存, got classifier=%q", second.Classifier)
	}
	if !second.CacheReused {
		t.Fatalf("V1 缓存命中必须置 CacheReused（与 V2 口径一致）, got false")
	}
	if second.RoleFallbackLayer != "mainstream" {
		t.Fatalf("缓存命中轮审计字段丢成 %q —— 主流层用量会被系统性低估", second.RoleFallbackLayer)
	}
}
