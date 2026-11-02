package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ★★ 2026-11-02 第十五轮（F-14）：handler 级闭环门
// 「chat + embedding 模型 ⇒ 400 且 code=modality_mismatch 且**上游未被调用**」。
//
// ── 这道门补的是哪一条缺口 ────────────────────────────────────────────
//
// 《12小时内修订审计-2026-11-02-0350.md》§六 的 F14（MINOR / P3）原话：
//
//	测试覆盖缺口：只直接调 diagnoseModalityMismatch，**没有**钉住
//	「chat + embedding 模型 ⇒ 400 且 code=modality_mismatch 且上游未被调用」
//	的 handler 级闭环。
//
// ★ 2026-11-02 第十四轮（F-16）把 provider 侧的判决表修成了可单测的纯函数，
// 但**判决**和**handler 的接线**之间仍然是两截：F14 要的正是把这两截接上。
//
// ── 事故链（本门守的就是它）──────────────────────────────────────────
//
//	POST /v1/chat/completions {"model":"qwen3-embedding:0.6b", ...}
//	⇒ 路由器把该 (凭据,模型) 绑定选成候选（文本请求此前放行一切登记模态）
//	⇒ 网关按 **chat 形态**转发到上游的 /v1/chat/completions
//	⇒ 上游 400，连试三次 ⇒ 该绑定被翻红（5 分钟冷却）
//	⇒ **/v1/embeddings 对所有租户 503** ⇒ 探针队列行被退避到 6 小时。
//
// ⇒ 这个链子的**第一环**是「候选解析层没有把 embedding 绑定排除掉」。
// 本门钉的就是这一环 + 它的**可观测后果**（客户端拿到可据以行动的 400）。
//
// ── 为什么必须是「handler 级」而不是「函数级」───────────────────────
//
//	第十四轮之前的门都是函数级的（直接调 diagnoseModalityMismatch，
//	或直接调 provider.ModalityAccepts）。函数级的门证明不了：
//	① handler 走的是**带模态**的候选查询（而不是无过滤的那条）；
//	② 零候选之后 handler 真的给出了 400，而不是退化成没有原因的 503；
//	③ 这次请求**没有进派发阶段**（⇒ 上游不会被调用）。
//	⇒ 这三条都必须从**真实 handler 管线**上观测。
//
// ── 替身为什么用**真实判据** ────────────────────────────────────────
//
//	替身的「带模态查询」用 provider.ModalityAccepts 过滤（生产 provider 的
//	SQL 接受臂就是由它渲染的）⇒ 变异 provider 侧的判据 ⇒ 本门当场红
//	（⇒ 这道门不是自证自的）。
//	「无过滤查询」返回**同一条** embedding 绑定 ⇒ 那正是事故链的那一步
//	⇒ 若 handler 改用无过滤查询，本门当场红。

// embeddingClosedLoopResolver 是 providerResolver 的一副替身：候选池里
// **只有**一条登记为 embedding 的绑定（credential 99）。
//
// ★ 两条查询路径的差别就是本门要量的东西：
// GetCandidatesByModality —— 生产路径（provider 的 SQL 接受臂）；
// GetCandidates            —— 无过滤路径（$3 为空 ⇒ 全放行）。
type embeddingClosedLoopResolver struct {
	// plainCalls 统计**无过滤**查询被调用的次数 ⇒ 它必须恒为 0。
	plainCalls int
	// modalityCalls 记录带模态查询的实参 ⇒ 必须含本次请求的模态（"text"）。
	modalityCalls []string
}

func (r *embeddingClosedLoopResolver) Enabled() bool { return true }

func (r *embeddingClosedLoopResolver) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	r.plainCalls++
	// 无过滤路径：**embedding 绑定被端上来**——事故链的那一步。
	return r.pool(), provider.DefaultPolicy(), nil
}

func (r *embeddingClosedLoopResolver) GetCandidatesByModality(_ context.Context, _, _, _, modality string) ([]provider.Candidate, *provider.Policy, error) {
	r.modalityCalls = append(r.modalityCalls, modality)
	// 带模态路径：用**真实判据**过滤（生产 SQL 接受臂由它渲染）。
	if !provider.ModalityAccepts("embedding", modality) {
		return nil, provider.DefaultPolicy(), nil
	}
	return r.pool(), provider.DefaultPolicy(), nil
}

func (r *embeddingClosedLoopResolver) ModelKnown(context.Context, string) bool { return true }

func (r *embeddingClosedLoopResolver) ModelModalityMismatch(_ context.Context, model, _, _, requestModality string) (*provider.ModalityMismatch, error) {
	// 生产判决（provider/modality_mismatch.go 的 modalityMismatchDecision）：
	// 「忽略模态过滤后确实可路由」+「登记模态服务不了本次请求」两条同时成立。
	// ★ 这里用真实判据 provider.ModalityAccepts —— 与 provider 侧判决表
	//   （门 P1）同源 ⇒ 两侧不会各说各话。
	if provider.ModalityAccepts("embedding", requestModality) {
		return nil, nil
	}
	return &provider.ModalityMismatch{
		RequestedModality:  requestModality,
		RegisteredModality: "embedding",
		CanonicalModel:     model,
	}, nil
}

func (r *embeddingClosedLoopResolver) pool() []provider.Candidate {
	return []provider.Candidate{{
		CredentialID: 99,
		ProviderID:   9,
		RawModel:     "qwen3-embedding:0.6b",
		StreamMode:   provider.StreamModeBoth,
	}}
}

// TestModalityEmbeddingChat_ClosedLoop 钉住 F14 的闭环，三条协议各一条。
//
// ★ 为什么三协议都要：同一个请求在三个端点走三段不同的代码
// （handler.go / messages.go / responses.go），而模态诊断的调用点也各有
// 一个 ⇒ 只钉一条协议就等于放过了另外两条。
func TestModalityEmbeddingChat_ClosedLoop(t *testing.T) {
	const model = "qwen3-embedding:0.6b"

	cases := []struct {
		name string
		path string
		body string
		// drive 把请求送进该协议自己的真实管线。
		drive func(t *testing.T, h *ChatHandler, w *httptest.ResponseRecorder, r *http.Request)
	}{
		{
			name: "openai_chat_completions",
			path: "/v1/chat/completions",
			body: `{"model":"qwen3-embedding:0.6b","messages":[{"role":"user","content":"hello"}]}`,
			drive: func(t *testing.T, h *ChatHandler, w *httptest.ResponseRecorder, r *http.Request) {
				t.Helper()
				h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-f14-openai", time.Now()))
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			body: `{"model":"qwen3-embedding:0.6b","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
			drive: func(t *testing.T, h *ChatHandler, w *httptest.ResponseRecorder, r *http.Request) {
				t.Helper()
				NewMessagesHandler(h).serveHTTPInner(w, r)
			},
		},
		{
			name: "openai_responses",
			path: "/v1/responses",
			body: `{"model":"qwen3-embedding:0.6b","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			drive: func(t *testing.T, h *ChatHandler, w *httptest.ResponseRecorder, r *http.Request) {
				t.Helper()
				NewResponsesHandler(h).serveHTTPInner(w, r)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &embeddingClosedLoopResolver{}
			h := &ChatHandler{
				provider: res,
				// 零候选分支之前的 defer 链里唯一没有 nil 保护的依赖：
				// h.auditor.Emit。LogSink 是无副作用实现。
				auditor: &audit.LogSink{},
			}

			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			// ★ 「上游未被调用」在 handler 层的可观测形式：**请求没有进派发阶段**。
			//   这条管线在候选为空时会走到 h.executor.Execute(...)；本测试的
			//   h.executor 是 nil ⇒ 一旦真进了派发阶段就会崩 ⇒ 用 recover
			//   把「崩」翻译成一句人话（而不是一个看不懂的栈）。
			//   ★ 真正「上游收到请求」的计数在 executors 层那道门
			//     （domains/streaming/executors/modality_upstream_not_called_test.go），
			//     那里有假上游服务器可以数。
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("请求走到了派发阶段（panic=%v）⇒ 模态门没有生效 ⇒ "+
						"embedding 绑定会被塞进本次 chat 请求 ⇒ 上游会被调用 ⇒ 事故链的那一步", rec)
				}
			}()

			tc.drive(t, h, w, r)

			// ① 候选解析必须**过模态门**：无过滤查询一次都不能被用。
			if res.plainCalls != 0 {
				t.Errorf("GetCandidates（无模态过滤）被调用 %d 次 ⇒ 候选解析绕过了模态门 ⇒ "+
					"embedding 绑定会被塞回本次 chat 请求", res.plainCalls)
			}
			// ② 带模态的查询必须被调用，且实参是本次请求的模态。
			if len(res.modalityCalls) == 0 {
				t.Fatalf("GetCandidatesByModality 一次都没被调用 ⇒ 候选解析没有过模态门")
			}
			if !containsString(res.modalityCalls, "text") {
				t.Errorf("GetCandidatesByModality 的实参 = %v，want 含 %q", res.modalityCalls, "text")
			}
			// ③ 客户端必须拿到**可据以行动的 400**，而不是没有原因的 503。
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 —— 客户端拿到 503 会无限重试，而它重试的每个字节都注定失败\nbody: %s",
					w.Code, w.Body.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
					Kind string `json:"kind"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatalf("解析错误响应失败: %v\nbody: %s", err, w.Body.String())
			}
			if payload.Error.Code != "modality_mismatch" {
				t.Errorf("error.code = %q, want %q\nbody: %s", payload.Error.Code, "modality_mismatch", w.Body.String())
			}
		})
	}
}

// TestModalityEmbeddingChat_ClosedLoop_FixtureIsNotVacuouslyEmpty 是恒绿护栏。
//
// ★ 没有它，一个「恒返回空候选集」的替身也能让上面那道门全绿 ⇒ 那道门就
// 证明不了「模态门把 embedding 绑定排除掉了」——它只证明了「空候选 ⇒ 400」。
// 护栏断言两件事：
// ① 替身的池**不是空的**（它真有一条 embedding 绑定）；
// ② 真实判据把「登记=embedding + 请求=text」判成**不接受**
// （生产 SQL 接受臂由同一个函数渲染 ⇒ 这一格就是生产里那一格）。
func TestModalityEmbeddingChat_ClosedLoop_FixtureIsNotVacuouslyEmpty(t *testing.T) {
	res := &embeddingClosedLoopResolver{}
	if len(res.pool()) == 0 {
		t.Fatal("替身的候选池是空的 ⇒ 上面那道门证明不了「模态门排除了 embedding 绑定」，只证明了「空候选 ⇒ 400」")
	}
	if provider.ModalityAccepts("embedding", "text") {
		t.Fatal("真实判据认为「登记=embedding 的模型」可以服务文本请求 ⇒ 模态门在生产上根本不生效")
	}
	// 反向对照：登记为 text 的模型**必须**被文本请求接住 ⇒ 排除集是精确的，
	// 不是「把所有模型都排除掉」那种退化实现。
	if !provider.ModalityAccepts("text", "text") {
		t.Fatal("真实判据把「登记=text 的模型」也排除在文本请求之外 ⇒ 判据退化了")
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
