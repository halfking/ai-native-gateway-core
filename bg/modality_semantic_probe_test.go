// bg/modality_semantic_probe_test.go — 语义级探测的判级
//
// 这组测试对着 httptest 起的**真实上游帧**跑，判级走的是
// ProbeVisionSemantics 本身，只有出网那一步被 httptest 接管。
//
// 三条不可让步的断言：
//
//  1. 「收下图片但看不见」判 negative —— 这正是今天把 200 当证据会
//     误标为多模态的形态，也是整套分级存在的理由。
//  2. 结构级 rejected 时**不再发挑战**（出网次数 = 1）。载都载不下的
//     上游不可能读出图里的编码，那一次请求是纯浪费。
//  3. inconclusive 绝不塌成 negative。截断、拒答、推理模型把预算花光
//     都属于「没探到」，写 negative 等于用默认值关掉一个正常绑定。
package bg

import (
	"context"
	"encoding/json"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// challengeEchoServer 对每次挑战请求，把图里真正的 4 个颜色解出来当作
// 「一个真的看得见的模型」的答案——模拟正样本，不硬编码某一张图。
func challengeEchoServer(t *testing.T, calls *int32, authStyle string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		body, _ := io.ReadAll(r.Body)
		if !containsSubstring(string(body), "image") {
			t.Errorf("probe payload must carry an image, got: %s", truncateProbeBody(string(body), 300))
		}
		if authStyle == "anthropic" && r.Header.Get("x-api-key") == "" {
			t.Errorf("anthropic probe must use x-api-key auth")
		}
		// 不管请求里带的是哪张图，回一个结构上正确的 200 —— 让判级
		// 决定结论，而不是让 HTTP 状态码决定。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"red, green, blue, yellow"},"finish_reason":"stop"}]}`))
	}))
}

func containsSubstring(hay, needle string) bool { return strings.Contains(hay, needle) }

// challengeBlockColorsFromRequest 解析挑战请求体里那张 data URL，采样出
// 4 个色块**真实**的颜色。
//
// 有了它，假模型就能「看见」图再答错，判负成为确定性的；而不是靠一个
// 硬编码颜色四元组去碰运气——挑战是随机的，固定四元组有 1/1680 的概率
// 真的对上，那会让这条用例变成偶发红的 flaky 测试。
func challengeBlockColorsFromRequest(t *testing.T, body []byte) []string {
	t.Helper()
	s := string(body)
	const marker = "data:image/png;base64,"
	idx := strings.Index(s, marker)
	if idx < 0 {
		t.Fatalf("challenge payload carries no image data URL: %s", truncateProbeBody(s, 300))
	}
	encoded := s[idx+len(marker):]
	if end := strings.IndexAny(encoded, `"\`); end >= 0 {
		encoded = encoded[:end]
	}
	img := decodeChallengePNG(t, marker+encoded)

	var out []string
	for i := 0; i < visionChallengeBlocks; i++ {
		x := i*(challengeBlockPx+challengeSeparatorPx) + challengeBlockPx/2
		got := color.RGBAModel.Convert(img.At(x, challengeBlockPx/2)).(color.RGBA)
		for _, c := range challengePalette {
			if c.rgb.R == got.R && c.rgb.G == got.G && c.rgb.B == got.B {
				out = append(out, c.name)
			}
		}
	}
	if len(out) != visionChallengeBlocks {
		t.Fatalf("sampled %d block colors, want %d", len(out), visionChallengeBlocks)
	}
	return out
}

// challengeAwareServer 起一个假上游：结构探针回 200，语义挑战按
// answerFor 给答案。answerFor 拿到图里的真实颜色。
func challengeAwareServer(t *testing.T, calls *int32, answerFor func(trueColors []string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		// 语义挑战带 semanticProbeMaxTokens；结构探针带 max_tokens=1。
		if !strings.Contains(string(body), strconv.Itoa(semanticProbeMaxTokens)) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"."}}]}`))
			return
		}
		answer := answerFor(challengeBlockColorsFromRequest(t, body))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(answer) +
			`},"finish_reason":"stop"}]}`))
	}))
}

func TestSemanticProbe_SeesImageButMisorders_IsNegative(t *testing.T) {
	var calls int32
	// 典型失效形态：看得到颜色，但把顺序说反了。
	srv := challengeAwareServer(t, &calls, func(colors []string) string {
		rotated := append(append([]string{}, colors[1:]...), colors[0])
		return strings.Join(rotated, ", ")
	})
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "k", "misorders", "openai-chat")
	if got.Carry != ModalityLevelAccepted {
		t.Fatalf("Carry = %q want accepted (err=%s)", got.Carry, got.ErrCode)
	}
	if got.Read != ModalityLevelNegative {
		t.Fatalf("Read = %q want %q —— 颜色全对但顺序错必须判负"+
			"（顺序是判据的一部分）（answer=%q expected=%v）",
			got.Read, ModalityLevelNegative, got.Answer, got.Expected)
	}
	// 旋转后无一位置对齐，位置分必须是 0 —— 这把「顺序也是判据」在
	// 证据里也钉住。
	if got.Score != 0 {
		t.Errorf("Score = %d want 0 (answer=%q expected=%v)", got.Score, got.Answer, got.Expected)
	}
}

func TestSemanticProbe_InventsColors_IsNegative(t *testing.T) {
	var calls int32
	// 「收下图片但看不见」：编 4 个图里没有的颜色。色板 8 色、图里占 4
	// 个，所以总有 4 个可编 ⇒ 这条用例零概率巧合。
	srv := challengeAwareServer(t, &calls, func(trueColors []string) string {
		in := map[string]bool{}
		for _, c := range trueColors {
			in[c] = true
		}
		var invented []string
		for _, c := range challengePalette {
			if !in[c.name] {
				invented = append(invented, c.name)
			}
		}
		return strings.Join(invented, ", ")
	})
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "k", "text-only-model", "openai-chat")
	if got.Carry != ModalityLevelAccepted {
		t.Fatalf("Carry = %q want %q (err=%s msg=%s)", got.Carry, ModalityLevelAccepted, got.ErrCode, got.ErrMsg)
	}
	if got.Read != ModalityLevelNegative {
		t.Fatalf("Read = %q want %q —— 「收下图片但答不出图里是什么」必须判负，"+
			"判成 confirmed 就是今天那个缺陷（answer=%q expected=%v）",
			got.Read, ModalityLevelNegative, got.Answer, got.Expected)
	}
}

func TestSemanticProbe_TruncatedAnswerStaysUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"max_tokens":1`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"."}}]}`))
			return
		}
		// 推理模型把预算花光：正文空，思维链里有"看起来像红绿蓝黄"。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"I think the blocks are red, green, blue, yellow"},"finish_reason":"length"}]}`))
	}))
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "k", "reasoning-model", "openai-chat")
	if got.Read != ModalityLevelUnknown {
		t.Fatalf("Read = %q want %q —— 截断的正文不得塌成 negative，"+
			"也不得因为 reasoning_content 里出现了颜色词就算 confirmed（answer=%q）",
			got.Read, ModalityLevelUnknown, got.Answer)
	}
	if got.ErrCode != "inconclusive" {
		t.Errorf("ErrCode = %q want inconclusive", got.ErrCode)
	}
}

func TestSemanticProbe_CarryRejected_SkipsChallenge(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"this model does not support image input","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "k", "no-vision-model", "openai-chat")
	if got.Carry != ModalityLevelRejected {
		t.Errorf("Carry = %q want %q", got.Carry, ModalityLevelRejected)
	}
	if got.Read != ModalityLevelUnknown {
		t.Errorf("Read = %q want %q —— 载不下就不该有语义结论", got.Read, ModalityLevelUnknown)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("upstream calls = %d want 1 —— 结构级拒绝后不得再发挑战请求", n)
	}
}

func TestSemanticProbe_AuthFailureCarriesNoVerdict(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "bad", "m", "openai-chat")
	if got.Carry != ModalityLevelUnknown {
		t.Errorf("Carry = %q want %q", got.Carry, ModalityLevelUnknown)
	}
	if got.Read != ModalityLevelUnknown {
		t.Errorf("Read = %q want %q", got.Read, ModalityLevelUnknown)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("upstream calls = %d want 1", n)
	}
}

// 正向路径：模型把图里真实的 4 个颜色按顺序说出来 ⇒ confirmed。
// 没有这条，整套系统无法证明自己真能把模型标成多模态——只会判负而已。
func TestSemanticProbe_ReadsChallengeCorrectly_IsConfirmed(t *testing.T) {
	var calls int32
	srv := challengeAwareServer(t, &calls, func(colors []string) string {
		return strings.Join(colors, ", ")
	})
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "k", "real-vision-model", "openai-chat")
	if got.Carry != ModalityLevelAccepted {
		t.Fatalf("Carry = %q want accepted (err=%s msg=%s)", got.Carry, got.ErrCode, got.ErrMsg)
	}
	if got.Read != ModalityLevelConfirmed {
		t.Fatalf("Read = %q want %q (answer=%q expected=%v mention=%v)",
			got.Read, ModalityLevelConfirmed, got.Answer, got.Expected, got.Mentioned)
	}
	if got.Score != visionChallengeBlocks {
		t.Errorf("Score = %d want %d", got.Score, visionChallengeBlocks)
	}
	if len(got.Expected) != visionChallengeBlocks {
		t.Errorf("Expected = %v, want %d entries", got.Expected, visionChallengeBlocks)
	}
}

func TestSemanticProbe_ReadsAnthropicFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"max_tokens":1`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"."}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"red, green, blue, yellow"}]}`))
	}))
	defer srv.Close()

	got := ProbeVisionSemantics(context.Background(), srv.URL, "sk-ant", "claude-vision", "anthropic-messages")
	if got.Carry != ModalityLevelAccepted {
		t.Fatalf("Carry = %q want accepted (err=%s msg=%s)", got.Carry, got.ErrCode, got.ErrMsg)
	}
	// 挑战图的真实颜色未知，所以这里只断言「不是 unknown/negative」：
	// 一个能读图的模型绝不会拿不到结论。
	if got.Read == ModalityLevelUnknown {
		t.Fatalf("Read = unknown —— anthropic 帧没能解析出答案（answer=%q err=%s）", got.Answer, got.ErrCode)
	}
}

func TestExtractAnswerText_Shapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"chat string", `{"choices":[{"message":{"content":"red, blue"}}]}`, "red, blue"},
		{"chat array", `{"choices":[{"message":{"content":[{"type":"text","text":"red"},{"type":"text","text":"blue"}]}}]}`, "red blue"},
		{"responses", `{"output":[{"content":[{"type":"output_text","text":"red, blue"}]}]}`, "red, blue"},
		{"anthropic", `{"content":[{"type":"text","text":"red, blue"}]}`, "red, blue"},
		{"reasoning ignored", `{"choices":[{"message":{"content":"","reasoning_content":"red, blue"}}]}`, ""},
		{"garbage", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractAnswerText([]byte(tc.body)); got != tc.want {
				t.Errorf("extractAnswerText = %q want %q", got, tc.want)
			}
		})
	}
}

// 挑战请求本身必须带够 max_tokens。既有结构探针用 1，那对语义判别
// 是错的：答案本身就有 10 个 token 上下，被截断后每次核实都白跑。
func TestChallengePayloadHasSufficientMaxTokens(t *testing.T) {
	ch, err := NewVisionChallenge(newVisionChallengeRNG())
	if err != nil {
		t.Fatal(err)
	}
	for _, isAnthropic := range []bool{false, true} {
		body := visionChallengePayload("m", ch, isAnthropic)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("anthropic=%v: payload not valid json: %v", isAnthropic, err)
		}
		mt, ok := payload["max_tokens"].(float64)
		if !ok {
			t.Fatalf("anthropic=%v: max_tokens missing", isAnthropic)
		}
		if int(mt) != semanticProbeMaxTokens {
			t.Errorf("anthropic=%v: max_tokens = %v want %d", isAnthropic, mt, semanticProbeMaxTokens)
		}
		if semanticProbeMaxTokens < 16 {
			t.Errorf("semanticProbeMaxTokens = %d is below the OpenAI Responses floor of 16", semanticProbeMaxTokens)
		}
	}
}
