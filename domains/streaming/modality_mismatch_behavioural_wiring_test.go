package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestModalityMismatch_BehaviouralWiring drives each protocol entry point's
// REAL request pipeline (serveWithExecutor / serveHTTPInner) end to end with a
// bare handler and asserts the diagnosis actually answers the request.
//
// ★ Why this exists (2026-10-28 批判式审计 §1.3 遗留 2 / LESSONS §497):
//
//	上一轮的接线守卫（modality_mismatch_wiring_guard_test.go）是【文本守卫】
//	—— 它只能证明「调用存在、位置在 fallback 之前」，证明不了控制流真的
//	能到达调用：把调用挪进一个不可达分支、或在它之前插一个提前 return，
//	文本守卫仍然全绿。审计原话：「调用点挪到 fallback 之后」这类纯语义
//	变异无法用文本守卫覆盖，留待行为测试 —— 就是本文件。
//
//	行为通道曾经被认为不可行（「serveWithExecutor panics on a bare
//	ChatHandler, measured」）—— 那次测量的缺口是缺一个 auditor：分支前的
//	defer 里 h.auditor.Emit 是唯一没有 nil 保护的硬依赖。补上 LogSink 之后
//	三条协议路径全链路可达（本轮实测），文本守卫升级为行为守卫的通道打开。
//
//	保留文本守卫不删：它锁「调用存在」（改名/删除当场红且指出文件），
//	本文件锁「控制流可达」（挪进死分支 / 提前 return 当场红且给出状态码）。
//	两者同族不同牙，§497 的变异形态（build failed = 编译器抓的）由本测试
//	补上纯语义变异的覆盖。
//
// ★ 与 TestProtocolPaths_AllDiagnoseModalityMismatch 的区别：那个测试调的是
// 共享诊断函数（调用点被删它仍绿）；本测试走的是各协议自己的入口管线，
// 管线里的门（auth/rate-limit/session/policy 全 nil 安全跳过）真实执行，
// 任何一个门提前 return 都会让断言拿到错误的状态码。
func TestModalityMismatch_BehaviouralWiring(t *testing.T) {
	newChat := func() *ChatHandler {
		return &ChatHandler{
			provider: &modalityMismatchResolver{
				known: true,
				mismatch: &provider.ModalityMismatch{
					RequestedModality:  "vision",
					RegisteredModality: "text",
					CanonicalModel:     "glm-5.3-flash",
				},
			},
			// 零候选分支之前的 defer 链唯一没有 nil 保护的依赖：
			// h.auditor.Emit（handler.go preStream defer）。LogSink 是
			// 无副作用实现，满足依赖且不写盘。
			auditor: &audit.LogSink{},
		}
	}

	const wantFragment = "modality_mismatch"

	t.Run("openai_chat_completions", func(t *testing.T) {
		h := newChat()
		// 检测器产 vision 的请求形态（image_url 内容块）。
		body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":[{"type":"text","text":"看下这张图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-bw-openai", time.Now()))

		assertModalityMismatchResponse(t, w, wantFragment)
	})

	t.Run("anthropic_messages", func(t *testing.T) {
		h := NewMessagesHandler(newChat())
		// Anthropic 形态的图片块 + max_tokens（该路径对 max_tokens<=0 有
		// 更早的 400 校验，先过它才能到候选解析）。
		body := `{"model":"glm-5.3-flash","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"看下这张图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		h.serveHTTPInner(w, r)

		assertModalityMismatchResponse(t, w, wantFragment)
	})

	t.Run("openai_responses", func(t *testing.T) {
		h := NewResponsesHandler(newChat())
		// Responses API 的 input[] 形态（2026-10-28 fea5b02af 补的检测面）。
		body := `{"model":"glm-5.3-flash","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		h.serveHTTPInner(w, r)

		assertModalityMismatchResponse(t, w, wantFragment)
	})
}

// TestModalityMismatch_BehaviouralWiring_NoCandidateFallback 是行为通道的
// 阴性对照：同一个模型、同样的文本请求形态，诊断必须**不**触发，
// 走通用 no_candidate 503。它同时钉住「行为通道真的走完了管线」——
// 若未来某个重构让管线在候选解析之前就短路，这里的状态码会先变。
func TestModalityMismatch_BehaviouralWiring_NoCandidateFallback(t *testing.T) {
	h := &ChatHandler{
		provider: &modalityMismatchResolver{known: true, mismatch: nil},
		auditor:  &audit.LogSink{},
	}
	body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":[{"type":"text","text":"纯文本问候"}]}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-bw-nohit", time.Now()))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("text request against a known model with no live binding must fall through to the generic 503 no_candidate; got %d\nbody: %s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "modality_mismatch") {
		t.Errorf("a text request must never be diagnosed as modality_mismatch\nbody: %s", w.Body.String())
	}
}

func assertModalityMismatchResponse(t *testing.T, w *httptest.ResponseRecorder, wantFragment string) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — the zero-candidate exit did not reach the modality diagnosis "+
			"(a gate upstream of candidate resolution now returns early, or the diagnosis wiring moved "+
			"into an unreachable branch; the text guard cannot see this, this test exists for it)\nbody: %s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), wantFragment) {
		t.Errorf("body lacks %q\nbody: %s", wantFragment, w.Body.String())
	}
}
