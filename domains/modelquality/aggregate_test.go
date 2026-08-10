package modelquality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNodeKey_DistinguishesCredential(t *testing.T) {
	// 同 provider+model 不同 CredentialID 必须落不同 key
	k0 := nodeKey("openai", "gpt-4", 0)
	k1 := nodeKey("openai", "gpt-4", 1)
	k2 := nodeKey("openai", "gpt-4", 2)
	if k0 == k1 || k0 == k2 || k1 == k2 {
		t.Errorf("nodeKey must differ by CredentialID: %q %q %q", k0, k1, k2)
	}
	// CredentialID==0 保持旧格式（向后兼容）
	if k0 != "openai:gpt-4" {
		t.Errorf("CredentialID=0 should keep old format, got %q", k0)
	}
	if k1 != "openai:gpt-4:1" {
		t.Errorf("CredentialID=1 format wrong, got %q", k1)
	}
}

func TestCatalogModelIQ_Aggregation(t *testing.T) {
	scores := []*QualityScore{
		{Provider: "openai", ModelName: "gpt-4", CanonicalModel: "gpt-4", CredentialID: 1, OverallScore: 90},
		{Provider: "openai", ModelName: "gpt-4", CanonicalModel: "gpt-4", CredentialID: 2, OverallScore: 80},
		{Provider: "azure", ModelName: "gpt-4-azure", CanonicalModel: "gpt-4", CredentialID: 3, OverallScore: 85},
		{Provider: "zhipu", ModelName: "glm-4", CanonicalModel: "glm-4", CredentialID: 4, OverallScore: 70},
	}
	out := catalogModelIQFromScores(scores)
	if len(out) != 2 {
		t.Fatalf("expected 2 catalog models, got %d", len(out))
	}
	// gpt-4: (90+80+85)/3 = 85; glm-4: 70。按智商降序 gpt-4 在前。
	if out[0].CanonicalModel != "gpt-4" {
		t.Errorf("expected gpt-4 first (higher IQ), got %s", out[0].CanonicalModel)
	}
	if out[0].AvgIQ != 85 {
		t.Errorf("gpt-4 avg IQ = %.1f, want 85", out[0].AvgIQ)
	}
	if out[0].NodeCount != 3 {
		t.Errorf("gpt-4 node count = %d, want 3", out[0].NodeCount)
	}
	// ByProvider: openai=(90+80)/2=85, azure=85
	if out[0].ByProvider["openai"] != 85 || out[0].ByProvider["azure"] != 85 {
		t.Errorf("ByProvider wrong: %+v", out[0].ByProvider)
	}
	// glm-4
	if out[1].CanonicalModel != "glm-4" || out[1].AvgIQ != 70 {
		t.Errorf("glm-4 wrong: %+v", out[1])
	}
}

func TestNodeIQ_Aggregation(t *testing.T) {
	scores := []*QualityScore{
		{Provider: "openai", ModelName: "gpt-4", CredentialID: 1, OverallScore: 90},
		{Provider: "openai", ModelName: "gpt-3.5", CredentialID: 1, OverallScore: 70},
		{Provider: "openai", ModelName: "gpt-4", CredentialID: 2, OverallScore: 60},
		// CredentialID=0（经网关）必须被节点聚合排除
		{Provider: "openai", ModelName: "gpt-4", CredentialID: 0, OverallScore: 99},
	}
	out := nodeIQFromScores(scores)
	if len(out) != 2 {
		t.Fatalf("expected 2 nodes (cred 1 and 2), got %d", len(out))
	}
	// cred 1: (90+70)/2=80, 2 models; cred 2: 60, 1 model。降序 cred1 在前。
	if out[0].CredentialID != 1 {
		t.Errorf("expected cred 1 first, got %d", out[0].CredentialID)
	}
	if out[0].AvgIQ != 80 {
		t.Errorf("cred1 avg IQ = %.1f, want 80", out[0].AvgIQ)
	}
	if out[0].ModelCount != 2 {
		t.Errorf("cred1 model count = %d, want 2", out[0].ModelCount)
	}
	if out[0].WeakestModel != "gpt-3.5" || out[0].BestModel != "gpt-4" {
		t.Errorf("cred1 weakest/best wrong: weakest=%s best=%s", out[0].WeakestModel, out[0].BestModel)
	}
	// cred 0 不应出现
	for _, n := range out {
		if n.CredentialID == 0 {
			t.Error("CredentialID=0 (gateway-aggregated) should be excluded from node IQ")
		}
	}
}

// TestDirectNodeInvoker_BuildsRequest 用 httptest 断言请求 URL/model/header/body 正确。
func TestDirectNodeInvoker_BuildsRequest(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotModel  string
		gotMaxTok int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model       string              `json:"model"`
			MaxTokens   int                 `json:"max_tokens"`
			Messages    []map[string]string `json:"messages"`
			Temperature float64             `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		gotMaxTok = body.MaxTokens
		// 返回一个标准 OpenAI 格式响应，答案是 B
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"B"}}],"usage":{"total_tokens":42}}`))
	}))
	defer srv.Close()

	inv := NewDirectNodeInvoker(5 * time.Second)
	node := CredentialNode{
		CredentialID: 7,
		Provider:     "openai",
		BaseURL:      srv.URL,
		APIKey:       "sk-test",
		RawModel:     "gpt-4o",
	}
	q := Question{ID: "q1", Question: "Q?", Options: []string{"A", "B", "C", "D"}, Answer: "B"}

	resp, tokens, latency, err := inv.InvokeModel(context.Background(), node, q)
	if err != nil {
		t.Fatalf("InvokeModel failed: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %s, want /v1/chat/completions", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer sk-test") {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotModel != "gpt-4o" {
		t.Errorf("model = %s, want gpt-4o", gotModel)
	}
	if gotMaxTok != 50 {
		t.Errorf("max_tokens = %d, want 50", gotMaxTok)
	}
	if resp != "B" {
		t.Errorf("response = %q, want B", resp)
	}
	if tokens != 42 {
		t.Errorf("tokens = %d, want 42", tokens)
	}
	if latency <= 0 {
		t.Error("expected positive latency")
	}
}

// TestDirectNodeInvoker_MissingNodeConfig 缺关键字段应报错而不是发请求。
func TestDirectNodeInvoker_MissingNodeConfig(t *testing.T) {
	inv := NewDirectNodeInvoker(time.Second)
	q := Question{Answer: "B"}
	_, _, _, err := inv.InvokeModel(context.Background(), CredentialNode{}, q)
	if err == nil {
		t.Error("expected error for empty node config")
	}
}

// TestProbeKind_PropagatesToScore 验证 ProbeKind 从 report 透传到 score，
// 且 CalculateScore 后 score.ProbeKind == report.ProbeKind。
func TestProbeKind_PropagatesToScore(t *testing.T) {
	calc := &ScoreCalculator{}
	for _, kind := range []ProbeKind{ProbeKindGateway, ProbeKindDirect, ProbeKindMock} {
		report := &BenchmarkReport{
			ModelName: "gpt-4", Provider: "openai",
			CredentialID: 7, ProbeKind: kind,
			TotalQuestions: 50, CorrectCount: 45, Accuracy: 90,
		}
		score := calc.CalculateScore(report)
		if score.ProbeKind != kind {
			t.Errorf("ProbeKind %s: score got %s", kind, score.ProbeKind)
		}
		if score.CredentialID != 7 {
			t.Errorf("CredentialID not propagated: %d", score.CredentialID)
		}
	}
}

// TestExecutorExecute_SetsInferredProbeKind 验证 Execute 按 invoker 类型推断 ProbeKind：
// GatewayModelInvoker→gateway，MockModelInvoker→mock，显式 SetProbeKind 优先。
func TestExecutorExecute_SetsInferredProbeKind(t *testing.T) {
	suite := &BenchmarkSuite{Type: BenchmarkTypeCustom, Questions: []Question{
		{ID: "q1", Subject: "t", Question: "Q?", Options: []string{"A", "B", "C", "D"}, Answer: "B"},
	}}

	// mock invoker
	mockInv := NewMockModelInvoker()
	mockInv.sleepFn = func(time.Duration) {}
	mockExec := NewBenchmarkExecutor(mockInv, time.Second)
	rep, err := mockExec.Execute(context.Background(), "m", "p", suite)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProbeKind != ProbeKindMock {
		t.Errorf("mock invoker → ProbeKind = %s, want mock", rep.ProbeKind)
	}

	// gateway invoker（httptest，不真连）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"B"}}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()
	gwInv := NewGatewayModelInvoker(srv.URL, "sk-x", time.Second)
	gwExec := NewBenchmarkExecutor(gwInv, time.Second)
	rep2, err := gwExec.Execute(context.Background(), "m", "p", suite)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.ProbeKind != ProbeKindGateway {
		t.Errorf("gateway invoker → ProbeKind = %s, want gateway", rep2.ProbeKind)
	}

	// 显式 SetProbeKind 优先于推断
	mockExec2 := NewBenchmarkExecutor(mockInv, time.Second)
	mockExec2.SetProbeKind(ProbeKindGateway) // 强制标记为 gateway
	rep3, _ := mockExec2.Execute(context.Background(), "m", "p", suite)
	if rep3.ProbeKind != ProbeKindGateway {
		t.Errorf("explicit SetProbeKind(gateway) should win, got %s", rep3.ProbeKind)
	}
}

// TestCatalogModelIQ_ByProbeKind 验证 catalog 聚合按调用路径分组。
func TestCatalogModelIQ_ByProbeKind(t *testing.T) {
	scores := []*QualityScore{
		{Provider: "openai", ModelName: "gpt-4", CanonicalModel: "gpt-4", CredentialID: 1, ProbeKind: ProbeKindGateway, OverallScore: 90},
		{Provider: "openai", ModelName: "gpt-4", CanonicalModel: "gpt-4", CredentialID: 2, ProbeKind: ProbeKindDirect, OverallScore: 80},
	}
	out := catalogModelIQFromScores(scores)
	if len(out) != 1 {
		t.Fatalf("expected 1 catalog model, got %d", len(out))
	}
	if out[0].ByProbeKind["gateway"] != 90 {
		t.Errorf("ByProbeKind gateway = %.1f, want 90", out[0].ByProbeKind["gateway"])
	}
	if out[0].ByProbeKind["direct"] != 80 {
		t.Errorf("ByProbeKind direct = %.1f, want 80", out[0].ByProbeKind["direct"])
	}
}

// TestGatewayModelInvoker_SetsOriginHeaders 验证经网关的 IQ 测试请求带了
// X-LLM-Origin-Stage=self_check 和 X-LLM-Origin-Actor 头，这样网关侧 request_logs
// 才能把 IQ 测试请求归到自检（而非 business）。
func TestGatewayModelInvoker_SetsOriginHeaders(t *testing.T) {
	var gotStage, gotActor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStage = r.Header.Get("X-LLM-Origin-Stage")
		gotActor = r.Header.Get("X-LLM-Origin-Actor")
		w.Write([]byte(`{"choices":[{"message":{"content":"B"}}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	inv := NewGatewayModelInvoker(srv.URL, "sk-x", time.Second)
	q := Question{Question: "Q?", Options: []string{"A", "B", "C", "D"}, Answer: "B"}
	_, _, _, err := inv.InvokeModel(context.Background(), "openai", "gpt-4", q)
	if err != nil {
		t.Fatalf("InvokeModel: %v", err)
	}
	if gotStage != "self_check" {
		t.Errorf("X-LLM-Origin-Stage = %q, want self_check", gotStage)
	}
	if gotActor != "model-quality-worker" {
		t.Errorf("X-LLM-Origin-Actor = %q, want model-quality-worker", gotActor)
	}
}
