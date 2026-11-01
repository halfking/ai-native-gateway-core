package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ★★ 2026-11-02（211 生产事故 · embedding 模型投毒链）回归钉。
//
// 事故链（生产日志 request_id=c5a038a96333aeb002c8f9824d4cdb5b 逐环可查）：
//
//	POST /v1/chat/completions {"model":"qwen3-embedding:0.6b", ...}
//	⇒ 路由器把该 (凭据,模型) 绑定选成候选（文本请求此前放行一切登记模态）
//	⇒ 网关按 **chat 形态** 转发到 Ollama 的 /v1/chat/completions
//	⇒ 上游 400（"qwen3-embedding:0.6b" does not support chat），连试三次
//	⇒ 该绑定被翻红（avail=false，5 分钟冷却）
//	⇒ **/v1/embeddings 对所有租户 503**（no_candidates / db_empty）
//	⇒ 探针队列行被退避到 6 小时（next_run_at=07:30）⇒ 绑定只能等自动恢复。
//
// 修复分两道关口：
//
//	① **候选解析层**（provider/modality_accept.go 的唯一片段）：文本请求不接
//	   登记为 embedding 的模型 ⇒ 上游**根本不会被调用** ⇒ 绑定不会被翻红 ⇒
//	   别的租户不会被连坐。这一道由 provider 侧的对拍门钉住
//	   （provider/modality_accepts_lockstep_test.go 的
//	   (登记=embedding, 请求=text) 那一格）。
//	② **诊断层**（本文件）：这类请求要拿到一个**有原因**的 400，而不是一个
//	   没有原因的 503 —— 客户端拿到 503 会无限重试，而它重试的每个字节都
//	   注定失败。
//
// 本文件钉关口②，以及「粗筛不得让普通文本模型多付一次 DB 查询」这条成本约束。

// embeddingGuardResolver 记录 ModelModalityMismatch 被调用了几次。
type embeddingGuardResolver struct {
	modalityMismatchResolver
	calls int
}

func (r *embeddingGuardResolver) ModelModalityMismatch(_ context.Context, model, _, _, requestModality string) (*provider.ModalityMismatch, error) {
	r.calls++
	if requestModality == modalityText {
		// 模拟真库结论：登记模态是 embedding ⇒ 文本请求与它不匹配。
		return &provider.ModalityMismatch{
			RequestedModality:  modalityText,
			RegisteredModality: "embedding",
			CanonicalModel:     model,
		}, nil
	}
	return nil, nil
}

// TestDiagnoseModalityMismatch_TextRequestOnEmbeddingModel 钉关口②：
// 文本请求打 embedding 模型时，诊断必须被触发。
func TestDiagnoseModalityMismatch_TextRequestOnEmbeddingModel(t *testing.T) {
	res := &embeddingGuardResolver{}
	h := &ChatHandler{provider: res}

	m := h.diagnoseModalityMismatch(context.Background(), "qwen3-embedding:0.6b", "", "default", modalityText)
	if m.IsZero() {
		t.Fatal("文本请求打 embedding 模型时没有给出模态不匹配诊断 —— 客户端只会拿到一个没有原因的 503，然后无限重试")
	}
	if m.RegisteredModality != "embedding" {
		t.Errorf("registered_modality = %q, want %q", m.RegisteredModality, "embedding")
	}
	if res.calls != 1 {
		t.Errorf("ModelModalityMismatch 被调用 %d 次, want 1", res.calls)
	}
}

// TestDiagnoseModalityMismatch_TextRequestOnOrdinaryModel 钉成本约束：
// 普通文本模型的零候选**不该**触发那次 800ms 有界的诊断查询。
//
// ⇒ 绝大多数零候选的文本请求与模态无关（模型名写错 / 租户范围 / 探针状态），
// 它们必须继续走「零候选即 503」的既有路径，不该多付一次 DB 往返。
func TestDiagnoseModalityMismatch_TextRequestOnOrdinaryModel(t *testing.T) {
	res := &embeddingGuardResolver{}
	h := &ChatHandler{provider: res}

	m := h.diagnoseModalityMismatch(context.Background(), "qwen3.5:4b", "", "default", modalityText)
	if !m.IsZero() {
		t.Fatalf("普通文本模型的零候选被判成了模态不匹配：%+v", m)
	}
	if res.calls != 0 {
		t.Errorf("ModelModalityMismatch 被调用 %d 次, want 0 —— 普通文本模型不该为一次零候选多付 DB 查询", res.calls)
	}
}

// TestTextRequestMayTargetEmbeddingModel 钉粗筛本身。
//
// ★ 粗筛**不是裁决**：它只决定「值不值得跑诊断」。漏判 ⇒ 少给一次 400、
// 仍给 503（诚实）；误判 ⇒ 多跑一次查询，结论仍由 DB 说了算。
func TestTextRequestMayTargetEmbeddingModel(t *testing.T) {
	for _, name := range []string{
		"qwen3-embedding:0.6b",
		"qwen3-embedding:8b",
		"text-embedding-3",
		"bge-m3-embedding",
	} {
		if !textRequestMayTargetEmbeddingModel(name) {
			t.Errorf("粗筛没认出 %q 是 embedding 模型 —— 这类请求会拿到无原因的 503 而不是 400", name)
		}
	}
	for _, name := range []string{
		"qwen3.5:4b",
		"qwen2.5:7b",
		"gpt-4o",
		"claude-3-5-sonnet",
		"glm-5.3",
		"",
	} {
		if textRequestMayTargetEmbeddingModel(name) {
			t.Errorf("粗筛把 %q 当成了 embedding 模型 —— 普通文本模型会被多带一次 DB 查询", name)
		}
	}
}

// TestProviderAcceptsTextForEmbeddingModel 钉关口①的判据本体。
//
// 候选解析层用的就是这条判据（两份 SQL 抄本都由它渲染）；把它钉在测试里，
// 是为了让「chat 端点打 embedding 模型 ⇒ 上游 400 ⇒ 绑定翻红 ⇒ 全租户 503」
// 这条事故链在判据层面有一根明确的桩。
func TestProviderAcceptsTextForEmbeddingModel(t *testing.T) {
	if provider.ModalityAccepts("embedding", "text") {
		t.Fatal("文本请求仍然被判定为可以打 embedding 登记模态 —— ★ 211 生产事故：上游会回 400，绑定会被翻红，/v1/embeddings 会对所有租户 503")
	}
	// 反向不能被误伤：embedding 模型仍然接嵌入请求（兜底精确臂承重）。
	if !provider.ModalityAccepts("embedding", "embedding") {
		t.Fatal("embedding 模型不接受 embedding 请求 —— /v1/embeddings 会零候选 ⇒ 功能事故")
	}
	// 普通模型接文本请求不受影响。
	for _, reg := range []string{"text", "multimodal", "vision", "audio", "video"} {
		if !provider.ModalityAccepts(reg, "text") {
			t.Errorf("文本请求不接受登记为 %q 的模型 —— 判据收窄过头，所有文本请求都会零候选", reg)
		}
	}
}
