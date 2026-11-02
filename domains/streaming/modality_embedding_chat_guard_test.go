package streaming

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
//
// ★ reportMismatch=false 用来模拟「登记模态不是 embedding」的库结论 ⇒
// 阴性对照：判决必须由库给出，handler 不得按名字自己下判断。
type embeddingGuardResolver struct {
	modalityMismatchResolver
	calls          int
	reportMismatch bool
}

func (r *embeddingGuardResolver) ModelModalityMismatch(_ context.Context, model, _, _, requestModality string) (*provider.ModalityMismatch, error) {
	r.calls++
	if r.reportMismatch && requestModality == modalityText {
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
	res := &embeddingGuardResolver{reportMismatch: true}
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

// TestDiagnoseModalityMismatch_TextRequestOnOrdinaryModel 钉「判决权归登记模态」。
//
// ★★ 2026-11-02 第六轮（F12）改写了这条口径：此前这里断言「普通文本模型
// **不该**触发那次诊断查询」（一道名字粗筛守着）。那条口径本身是错的：
//
//	· 粗筛用**名字**猜登记模态 ⇒ 名字不含 embed 字样的 embedding 模型
//	  （bge-m3 / nemotron-3-embed-1b / nemotron-3.5-embed-*）永远漏判 ⇒
//	  客户端拿 503 ⇒ 无限重试（211 实测 21 天 522 次注定失败的请求）；
//	· 粗筛也没买到性能：实测（211，EXPLAIN ANALYZE）「查库的粗筛」3.34ms、
//	  完整诊断 3.86ms ⇒ 两者等价 ⇒ 粗筛只贡献了错误答案。
//
// ⇒ 现在判决 100% 由登记模态（provider.ModelModalityMismatch 查库）给出：
// **跑它的次数变多，结论一个字节都没变**——普通文本模型仍然拿到「不是模态
// 问题」⇒ 仍然走「零候选即 503」的既有路径。
func TestDiagnoseModalityMismatch_TextRequestOnOrdinaryModel(t *testing.T) {
	res := &embeddingGuardResolver{}
	h := &ChatHandler{provider: res}

	m := h.diagnoseModalityMismatch(context.Background(), "qwen3.5:4b", "", "default", modalityText)
	if !m.IsZero() {
		t.Fatalf("普通文本模型的零候选被判成了模态不匹配：%+v", m)
	}
	if res.calls != 1 {
		t.Errorf("ModelModalityMismatch 被调用 %d 次, want 1 —— 判决权必须归登记模态（权威源），不得由名字规则代答", res.calls)
	}
}

// TestDiagnoseModalityMismatch_TextRequestOnEmbeddingModelNameHeuristicMisses
// 是 F12 的回归钉：**名字规则认不出来**的 embedding 模型，诊断也必须被触发。
//
// ★ 这四个名字是 211 生产登记在册、且**被生产流量打中过**的 embedding 模型
// （modelname.InferModality 对它们一律返回 "text" ⇒ 旧粗筛全部漏判）：
//
//	bge-m3 / nemotron-3-embed-1b / nemotron-3.5-embed-1b / nemotron-3.5-embed-8b
//
// ⇒ 修之前它们拿到的是没有原因的 503（客户端无限重试）；修之后必须拿到
// 可据以行动的 400。
func TestDiagnoseModalityMismatch_TextRequestOnEmbeddingModelNameHeuristicMisses(t *testing.T) {
	for _, name := range []string{
		"bge-m3",
		"nemotron-3-embed-1b",
		"nemotron-3.5-embed-1b",
		"nemotron-3.5-embed-8b",
		"bge_m3", // 别名形态：同样漏判（旧粗筛只认名字）
	} {
		t.Run(name, func(t *testing.T) {
			res := &embeddingGuardResolver{reportMismatch: true}
			h := &ChatHandler{provider: res}

			m := h.diagnoseModalityMismatch(context.Background(), name, "", "default", modalityText)
			if m.IsZero() {
				t.Fatalf("%q（登记为 embedding）没有拿到模态不匹配诊断 ⇒ 客户端只会拿到一个没有原因的 503，然后无限重试", name)
			}
			if res.calls != 1 {
				t.Errorf("ModelModalityMismatch 被调用 %d 次, want 1 —— 名字规则漏判时也必须由登记模态来判", res.calls)
			}
		})
	}
}

// TestDiagnoseModalityMismatch_VerdictComesFromRegistry 是阴性对照：
// 判决**不是** handler 自己按名字下的——resolver 说不是，就不是。
//
// ★ 没有这条对照，一个「看到名字里有 embed 就判不匹配」的退化实现也能让
// 上面那条 F12 回归钉全绿。
func TestDiagnoseModalityMismatch_VerdictComesFromRegistry(t *testing.T) {
	res := &embeddingGuardResolver{reportMismatch: false}
	h := &ChatHandler{provider: res}

	m := h.diagnoseModalityMismatch(context.Background(), "qwen3-embedding:0.6b", "", "default", modalityText)
	if !m.IsZero() {
		t.Fatalf("登记模态说不是模态问题，handler 却自己判成了不匹配：%+v", m)
	}
	if res.calls != 1 {
		t.Errorf("ModelModalityMismatch 被调用 %d 次, want 1", res.calls)
	}
}

// TestModalityDiagnosisDoesNotUseNameInference 钉「名字推断不参与裁决」。
//
// ★ 这是 §四.1 的口径在代码层的落点：诊断路径**不得**再调用
// modelname.InferModality（名字推断）。名字推断只允许出现在
// 「注册/播种」这类一次性动作里，绝不能出现在「判决」路径上——
// 名字推断对 bge-m3 / nemotron-3-embed-1b 这类模型一律返回 "text"
// （生产在册、且被生产流量打中过），用它守判决 ⇒ 客户端拿 503 ⇒ 无限重试。
//
// ⇒ 用**结构**（源码里不许再出现这个调用）而不是行为来钉：行为钉不住
// 「下一次有人又把名字推断塞回来」这件事。
func TestModalityDiagnosisDoesNotUseNameInference(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range productionGoFiles(t) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		parsed, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "modelname" && sel.Sel.Name == "InferModality" {
				t.Errorf("%s: 调用了 modelname.InferModality ⇒ 名字推断回到了判决路径 ⇒ 「名字不含 embed 字样的 embedding 模型」又会拿不到诊断 ⇒ 客户端拿 503 ⇒ 无限重试", name)
			}
			return true
		})
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
