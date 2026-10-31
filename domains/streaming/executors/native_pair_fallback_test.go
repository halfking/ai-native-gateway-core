package executors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 原厂直通轮（2026-10-28）端到端契约：
//
//	首发  → 原厂对：客户端参数一字不改地上传（原厂工具 ↔ 原厂模型）
//	出错  → 上游 400 点名某参数 ⇒ reqprobe 剥掉该参数重试（只传必要参数）
//
// 两条断言同时成立才算达标：只做第一半就变成「参数删了但没回退」，
// 只做第二半就变成「原厂参数照样被网关改写」。

// nativeFallbackUpstream 记录每次收到的 body；第一次按点名参数返回 400，
// 之后返回成功。
type nativeFallbackUpstream struct {
	mu      sync.Mutex
	bodies  []string
	rejects string
}

func (u *nativeFallbackUpstream) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(body))
		n := len(u.bodies)
		u.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":[{"type":"extra_forbidden","loc":["body","` + u.rejects + `"],"msg":"Extra inputs are not permitted"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-native","object":"chat.completion","model":"glm-5.3",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}
}

func (u *nativeFallbackUpstream) received() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]string, len(u.bodies))
	copy(out, u.bodies)
	return out
}

func TestNativePair_FirstAttemptKeepsAllParams_ThenStripsOnUpstreamRejection(t *testing.T) {
	up := &nativeFallbackUpstream{rejects: "zcode_vendor_flag"}
	srv := httptest.NewServer(up.handler(t))
	defer srv.Close()

	exec, _ := newF04ExecutorWithSlots(t)
	exec.RequestProbe = reqprobe.NewCoordinator(reqprobe.NewMemoryStore())

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "zhipu",
		RawModel: "glm-5.3", APIKey: "test-key",
	}

	// zcode 客户端 → glm 原生对；body 里带一个上游不认识的厂商私有参数，
	// 外加一个 GLM 方言的显式 tool_choice（paramguard 在非原生路径上会把它
	// 强制归一为 "auto" —— 尾段 applyOpenAITailTransformsNative 也在覆盖内）。
	clientBody := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],` +
		`"zcode_vendor_flag":true,"reasoning_effort":"high",` +
		`"tool_choice":{"type":"function","function":{"name":"read_file"}}}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Client-Type", "zcode")
	params := &ExecParams{
		W:                httptest.NewRecorder(),
		R:                r,
		BodyBytes:        []byte(clientBody),
		ClientModel:      "glm-5.3",
		Model:            "glm-5.3",
		RequestID:        "native-fallback",
		TenantID:         "default",
		IsStream:         false,
		ClientProtocol:   "openai-completions",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}

	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}

	bodies := up.received()
	if len(bodies) < 2 {
		t.Fatalf("upstream saw %d request(s), want >=2 (first rejected, then stripped retry)\nbodies: %v", len(bodies), bodies)
	}

	// 第一发：客户端的全部参数（含被上游点名的那一个）必须原样上传。
	var first map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &first); err != nil {
		t.Fatalf("unmarshal first body: %v\n%s", err, bodies[0])
	}
	if _, present := first["zcode_vendor_flag"]; !present {
		t.Fatalf("first attempt dropped the client's vendor param:\n%s", bodies[0])
	}
	if first["reasoning_effort"] != "high" {
		t.Fatalf("first attempt reasoning_effort = %v, want \"high\" (verbatim)\n%s", first["reasoning_effort"], bodies[0])
	}
	// tool_choice 不得被 paramguard 归一（这是尾段 paramguard 覆盖内的一处，
	// 也是 M3 变异臂的靶点）。
	tc, ok := first["tool_choice"].(map[string]any)
	if !ok || tc["function"] == nil {
		t.Fatalf("first attempt tool_choice rewritten by paramguard:\n%s", bodies[0])
	}

	// 第二发：被点名的参数已剥掉，只剩必要参数。
	var second map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("unmarshal second body: %v\n%s", err, bodies[1])
	}
	if _, present := second["zcode_vendor_flag"]; present {
		t.Fatalf("retry did NOT strip the rejected param:\n%s", bodies[1])
	}
	if _, present := second["messages"]; !present {
		t.Fatalf("retry lost the required messages field:\n%s", bodies[1])
	}
}

// TestNativePair_LearnedPreStripSkipped：已学习「该上游拒绝某参数」的
// 记录存在时，非原生路径会在【首发】就预剥离该参数；原生对必须首发仍带
// 它（否则一次瞬态 400 会让原厂参数被静默剥离 24h）。
// 这条是 M5 变异臂的靶点。
func TestNativePair_LearnedPreStripSkipped(t *testing.T) {
	store := reqprobe.NewMemoryStore()
	coord := reqprobe.NewCoordinator(store)
	t.Cleanup(coord.Close)

	const providerID = 4242
	const rawModel = "glm-5.3"
	const wire = "openai-completions"
	const learnedParam = "zcode_vendor_flag"

	// 预置一条「已学习」记录：此前剥掉该参数后重试成功。
	store.Upsert(context.Background(), reqprobe.Record{
		ProviderID:     providerID,
		OutboundModel:  rawModel,
		Protocol:       wire,
		Trigger:        reqprobe.TriggerParamRejected,
		Param:          learnedParam,
		HTTPStatus:     400,
		RecoveredCount: 1,
		LastSeen:       time.Now(),
	})
	if learned := coord.LearnedParams(context.Background(), reqprobe.LearnedKey{
		ProviderID: providerID, OutboundModel: rawModel, ActualWire: wire,
	}); len(learned) == 0 {
		t.Fatalf("fixture setup failed: coordinator learned nothing (the arm would be vacuous)")
	}

	up := &nativeFallbackUpstream{rejects: learnedParam}
	srv := httptest.NewServer(up.handler(t))
	defer srv.Close()

	exec, _ := newF04ExecutorWithSlots(t)
	exec.RequestProbe = coord

	candidate := provider.Candidate{
		ProviderID: providerID, CredentialID: 22, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "zhipu",
		RawModel: rawModel, APIKey: "test-key",
	}
	clientBody := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"` + learnedParam + `":true}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Client-Type", "zcode")
	params := &ExecParams{
		W:                httptest.NewRecorder(),
		R:                r,
		BodyBytes:        []byte(clientBody),
		ClientModel:      rawModel,
		Model:            rawModel,
		RequestID:        "native-learned",
		TenantID:         "default",
		IsStream:         false,
		ClientProtocol:   "openai-completions",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}

	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	bodies := up.received()
	if len(bodies) == 0 {
		t.Fatal("upstream saw no request")
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &first); err != nil {
		t.Fatalf("unmarshal first body: %v\n%s", err, bodies[0])
	}
	if _, present := first[learnedParam]; !present {
		t.Fatalf("native pair: learned pre-strip removed %q from the FIRST attempt:\n%s", learnedParam, bodies[0])
	}
}

// TestNativePair_TransformStripRequestFields_BypassedOnlyForNativePair：
// 同样的 transform 剥离规则下，原生对保留客户端参数，非原生对照旧剥离。
// 这条是「门有牙」的反向臂：若本轮改动没生效，两臂会得到同样结果。
func TestNativePair_TransformStripRequestFields_BypassedOnlyForNativePair(t *testing.T) {
	clientBody := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"zcode_extra":{"a":1}}`
	cand := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: "https://upstream.invalid",
		Protocol: "openai-completions", CatalogCode: "zhipu", RawModel: "glm-5.3",
	}
	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.3",
		ClientProtocol: "openai-completions",
		Transform: &transformation.TransformResult{
			StripRequestFields: []string{"zcode_extra"},
		},
	}

	native := prepareRequestBodyWithNative(params, cand, true)
	var nObj map[string]any
	if err := json.Unmarshal(native, &nObj); err != nil {
		t.Fatalf("unmarshal native: %v", err)
	}
	if _, present := nObj["zcode_extra"]; !present {
		t.Fatalf("native pair: transform strip was NOT bypassed:\n%s", native)
	}

	legacy := prepareRequestBody(params, cand)
	var lObj map[string]any
	if err := json.Unmarshal(legacy, &lObj); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if _, present := lObj["zcode_extra"]; present {
		t.Fatalf("non-native pair: transform strip did NOT fire:\n%s", legacy)
	}
}
