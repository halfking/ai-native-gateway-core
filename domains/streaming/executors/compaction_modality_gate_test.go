package executors

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ── F32 的执行器侧闸门（2026-11-02 第十轮）───────────────────────────────
//
// 压缩摘要链（context_summarize.go 的 pickCompactionCandidates）此前走
// **无模态过滤**的 `GetCandidates` ⇒ 登记为 embedding 的绑定会被塞进压缩
// 请求 ⇒ 上游 400（"does not support chat"）⇒ **压缩静默失败**。
//
// ★ 这一条**不是放大器**（审计档 §9.6 已核实）：压缩链不写绑定/节点状态
//   （doCompactionUpstream 无 WriteOnError / nodehealth 调用）⇒ 失败只让自己
//   这次压缩失败（链上有下一个候选兜底）⇒ 不连坐、不翻红。
//   但它是**功能坏的**：压缩模型名被误配成 embedding 登记绑定时，压缩静默
//   失败 ⇒ 请求带着超长历史打回客户端。
//
// ★ 口径：压缩请求是**纯文本**的摘要调用 ⇒ 模态固定 "text"；「text 只排除
//   embedding」（vision/audio 仍放行）⇒ 风险极低。

// compactionGateProvider 模拟生产 provider 的模态过滤：
// text 查询**不含** embedding 绑定；无过滤查询**含**它。
type compactionGateProvider struct {
	byModality map[string][]provider.Candidate
	unfiltered []provider.Candidate

	modalityCalls []string
	plainCalls    int
}

func (p *compactionGateProvider) Enabled() bool                           { return true }
func (p *compactionGateProvider) ModelKnown(context.Context, string) bool { return true }

func (p *compactionGateProvider) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls++
	return append([]provider.Candidate(nil), p.unfiltered...), provider.DefaultPolicy(), nil
}

func (p *compactionGateProvider) GetCandidatesByModality(_ context.Context, _, _, _, modality string) ([]provider.Candidate, *provider.Policy, error) {
	p.modalityCalls = append(p.modalityCalls, modality)
	return append([]provider.Candidate(nil), p.byModality[modality]...), provider.DefaultPolicy(), nil
}

// compactionBlindProvider 只实现无模态查询——模拟「provider 过滤不了模态」。
type compactionBlindProvider struct {
	plainCalls int
}

func (p *compactionBlindProvider) Enabled() bool                           { return true }
func (p *compactionBlindProvider) ModelKnown(context.Context, string) bool { return true }

func (p *compactionBlindProvider) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls++
	return []provider.Candidate{{CredentialID: 999, ProviderID: 9, RawModel: "m-embed", Routable: true}}, provider.DefaultPolicy(), nil
}

func compactionGateFixture() (*compactionGateProvider, *Executor) {
	spy := &compactionGateProvider{
		byModality: map[string][]provider.Candidate{
			"text": {
				{CredentialID: 11, ProviderID: 1, RawModel: "m", Routable: true, StreamMode: provider.StreamModeBoth},
			},
		},
		unfiltered: []provider.Candidate{
			{CredentialID: 11, ProviderID: 1, RawModel: "m", Routable: true, StreamMode: provider.StreamModeBoth},
			{CredentialID: 99, ProviderID: 9, RawModel: "m-embed", Routable: true, StreamMode: provider.StreamModeBoth},
		},
	}
	return spy, &Executor{Provider: spy}
}

// TestPickCompactionCandidates_ExcludesEmbeddingBinding 钉门 J：压缩候选
// **不含** embedding 绑定，**且**仍然拿到 text 绑定（不是把候选全滤光）。
func TestPickCompactionCandidates_ExcludesEmbeddingBinding(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	_, executor := compactionGateFixture()

	got := executor.pickCompactionCandidates(context.Background(), "profile-a")

	for _, candidate := range got {
		if candidate.CredentialID == 99 {
			t.Fatalf("压缩候选里出现了 embedding 绑定（credential 99）⇒ 压缩请求会被上游 400 ⇒ 压缩静默失败")
		}
	}
	if len(got) != 1 || got[0].CredentialID != 11 {
		t.Fatalf("压缩候选 = %+v, want 1 个（credential 11 的 text 绑定）", got)
	}
}

// TestPickCompactionCandidates_UsesModalityGate 钉门 K：压缩链**必须**以
// "text" 模态查询，且**不得**调用无模态过滤的 GetCandidates。
func TestPickCompactionCandidates_UsesModalityGate(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy, executor := compactionGateFixture()

	executor.pickCompactionCandidates(context.Background(), "profile-a")

	if spy.plainCalls != 0 {
		t.Errorf("无模态过滤的 GetCandidates 被调用 %d 次 ⇒ 压缩链绕过了模态门", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 1 || spy.modalityCalls[0] != "text" {
		t.Fatalf("GetCandidatesByModality 调用 = %v, want [text]（压缩是纯文本摘要调用）", spy.modalityCalls)
	}
}

// TestPickCompactionCandidates_BlindProviderDoesNotFallBack 是门 L
// （阴性对照）：provider 过滤不了模态时**不得**退回无过滤查询。
//
// ★ 与第五轮同一个原则：「宁可不换候选，也不换一个模态不对的候选」。
// 退回无过滤 = 把 embedding 绑定塞回压缩链 = 回到 F32 的老问题。
// ⇒ 压缩链在该模型上没有候选 ⇒ 退到下一个模型（压缩是尽力而为的链路）。
func TestPickCompactionCandidates_BlindProviderDoesNotFallBack(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy := &compactionBlindProvider{}
	executor := &Executor{Provider: spy}

	got := executor.pickCompactionCandidates(context.Background(), "profile-a")

	if spy.plainCalls != 0 {
		t.Errorf("无模态过滤的 GetCandidates 被调用 %d 次 ⇒ provider 过滤不了模态时退回了无过滤查询", spy.plainCalls)
	}
	if len(got) != 0 {
		t.Fatalf("压缩候选 = %+v, want 空（provider 过滤不了模态 ⇒ 不退回无过滤）", got)
	}
}

// TestPickCompactionCandidates_SourceHasNoDirectGetCandidates 是门 M
// （结构性）：`pickCompactionCandidates` 的函数体里**不得**直接调用
// `GetCandidates` ⇒ 必须走 `resolveCandidatesByModality`（唯一入口）。
//
// ★ 为什么要有这道结构门：门 J/K/L 靠 fake provider 的行为来判；若有人把
// 实现改回直接调 `e.Provider.GetCandidates`，行为门会红，但若同时把
// fake 换成「无过滤也返回 text 绑定」，行为门就可能误绿 ⇒ 结构门兜住。
func TestPickCompactionCandidates_SourceHasNoDirectGetCandidates(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "context_summarize.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 context_summarize.go 失败：%v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if ok && f.Name.Name == "pickCompactionCandidates" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatal("在 context_summarize.go 里找不到 pickCompactionCandidates")
	}
	// ★ 判据用「**任何** SelectorExpr 形态的 GetCandidates 引用」，不只是
	//   直接调用：`getCands := e.Provider.GetCandidates; getCands(...)`
	//   这种函数值别名**同样**会把无过滤查询塞回压缩链，而只看 CallExpr 的
	//   写法会让它静默通过（AliasExpr 不在 CallExpr 里）。
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "GetCandidates" {
			found = true
		}
		return true
	})
	if found {
		t.Fatal("pickCompactionCandidates 里出现了 GetCandidates 引用（直接调用或函数值别名）⇒ " +
			"必须走 resolveCandidatesByModality（唯一入口，否则 embedding 绑定会进压缩链）")
	}
	// 恒绿护栏：这道门必须真的看到了那个函数体（否则「找不到函数」之外的情况
	// 会让门静默通过）。
	var buf strings.Builder
	_ = ast.Fprint(&buf, fset, fn.Body, nil)
	if buf.Len() == 0 {
		t.Fatal("pickCompactionCandidates 的函数体是空的 ⇒ 门失去了意义")
	}
}
