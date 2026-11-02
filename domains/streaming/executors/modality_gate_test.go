package executors

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ── F10 / F11 的执行器侧闸门（2026-11-02 第五轮）──────────────────────────
//
// ★ 这道门守的是「执行器里的候选再解析**必须**过请求模态门」：
//
//	① refreshedCandidatesAfterProbe —— 同步探针恢复节点后的候选刷新（F10）；
//	② dispatchCandidatesForModel —— dispatch 换替代模型时的候选解析（F11）。
//
// 此前两处都走**无模态过滤**的 GetCandidates（$3 为空 ⇒ 模态接受判据的
// 「全放行」臂）⇒ 登记为其它模态的绑定（典型：embedding）会被塞回本次
// chat 请求 ⇒ 上游 400 ⇒ 该 (凭据,模型) 绑定翻红 ⇒ 与 2026-11-02 事故同族
// （/v1/embeddings 对所有租户 503）。
//
// ★ 阴性对照是这道门的**另一半**：模态为空时**必须**仍然走无过滤的旧路
// （没有模态概念的调用方——例如探针自检——不能被这道门误伤）。
// 没有阴性对照的话，「永远走过滤路径」的退化实现也能让本文件全绿。
//
// ★ 2026-11-02（第十轮 · F-32）：**压缩摘要链曾经被列在这里**
// （原文是「例如压缩摘要链、探针自检」）。第十轮把压缩链改成**固定过
// text 模态门**（压缩是纯文本摘要调用 ⇒ 模态固定 text ⇒ 登记为
// embedding 的绑定不该被塞进压缩请求）。
// ⇒ 「模态为空 ⇒ 走无过滤旧路」这条**仍然成立**，但压缩链不再是它的例子；
// ⇒ 它的门在 domains/streaming/executors/compaction_modality_gate_test.go。
type modalityGateProvider struct {
	// byModality 记录 (modality → 候选集)，模拟生产 provider 的模态过滤：
	// 文本请求只拿到文本绑定，embedding 绑定不出现。
	byModality map[string][]provider.Candidate
	// unfiltered 是「无模态过滤」时返回的候选集（含 embedding 绑定）。
	unfiltered []provider.Candidate

	modalityCalls []string
	plainCalls    int
}

func (p *modalityGateProvider) Enabled() bool                           { return true }
func (p *modalityGateProvider) ModelKnown(context.Context, string) bool { return true }

func (p *modalityGateProvider) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls++
	return append([]provider.Candidate(nil), p.unfiltered...), provider.DefaultPolicy(), nil
}

func (p *modalityGateProvider) GetCandidatesByModality(_ context.Context, _, _, _, modality string) ([]provider.Candidate, *provider.Policy, error) {
	p.modalityCalls = append(p.modalityCalls, modality)
	return append([]provider.Candidate(nil), p.byModality[modality]...), provider.DefaultPolicy(), nil
}

// modalityBlindProvider 只实现无模态查询——模拟「provider 过滤不了模态」。
type modalityBlindProvider struct {
	plainCalls int
}

func (p *modalityBlindProvider) Enabled() bool                           { return true }
func (p *modalityBlindProvider) ModelKnown(context.Context, string) bool { return true }
func (p *modalityBlindProvider) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls++
	return []provider.Candidate{{CredentialID: 999, ProviderID: 9, RawModel: "m", StreamMode: provider.StreamModeBoth}}, provider.DefaultPolicy(), nil
}

func modalityGateFixture() (*modalityGateProvider, *Executor) {
	spy := &modalityGateProvider{
		// 文本请求拿到的候选集里**没有** embedding 绑定——这正是生产
		// provider.GetCandidatesByModality 的行为（模态接受判据：
		// P='text' ⇒ 放行，但排除 R='embedding'）。
		byModality: map[string][]provider.Candidate{
			"text": {
				{CredentialID: 11, ProviderID: 1, RawModel: "m", StreamMode: provider.StreamModeBoth},
			},
			"vision": {
				{CredentialID: 22, ProviderID: 2, RawModel: "m", StreamMode: provider.StreamModeBoth},
			},
		},
		// 无过滤路径会把 embedding 绑定一起端上来——事故链的那一步。
		unfiltered: []provider.Candidate{
			{CredentialID: 11, ProviderID: 1, RawModel: "m", StreamMode: provider.StreamModeBoth},
			{CredentialID: 99, ProviderID: 9, RawModel: "m-embed", StreamMode: provider.StreamModeBoth},
		},
	}
	return spy, &Executor{Provider: spy}
}

func modalityGateParams(modality string) *ExecParams {
	return &ExecParams{
		R:           httptest.NewRequest("POST", "/v1/chat/completions", nil),
		ClientModel: "m",
		ClientID: identity.ClientIdentity{
			Fingerprint: identity.ClientFingerprint{ClientProfile: "profile-a"},
		},
		TenantID:        "tenant-a",
		RequestModality: modality,
	}
}

// TestRefreshedCandidatesAfterProbe_UsesRequestModality 钉住 F10：探针后刷新
// 必须用请求模态过滤，且**不得**退回无过滤的查询。
func TestRefreshedCandidatesAfterProbe_UsesRequestModality(t *testing.T) {
	for _, modality := range []string{"text", "vision"} {
		t.Run(modality, func(t *testing.T) {
			spy, executor := modalityGateFixture()
			got := executor.refreshedCandidatesAfterProbe(modalityGateParams(modality))

			if spy.plainCalls != 0 {
				t.Errorf("GetCandidates（无模态过滤）被调用 %d 次 ⇒ 刷新路径绕过了模态门 ⇒ embedding 绑定会被塞回本次请求", spy.plainCalls)
			}
			if len(spy.modalityCalls) != 1 || spy.modalityCalls[0] != modality {
				t.Fatalf("GetCandidatesByModality 调用 = %v, want [%s]", spy.modalityCalls, modality)
			}
			for _, candidate := range got {
				if candidate.CredentialID == 99 {
					t.Fatalf("候选里出现了 embedding 绑定（credential 99）⇒ 模态门没生效")
				}
			}
			if len(got) != 1 {
				t.Fatalf("候选数 = %d, want 1: %+v", len(got), got)
			}
		})
	}
}

// TestRefreshedCandidatesAfterProbe_NoModalityKeepsLegacyPath 是阴性对照：
// 模态为空 ⇒ 走无过滤的旧路（没有模态概念的调用方不被误伤）。
func TestRefreshedCandidatesAfterProbe_NoModalityKeepsLegacyPath(t *testing.T) {
	spy, executor := modalityGateFixture()
	got := executor.refreshedCandidatesAfterProbe(modalityGateParams(""))

	if spy.plainCalls != 1 {
		t.Errorf("GetCandidates 调用 = %d, want 1（模态为空 ⇒ 仍走无过滤旧路）", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 0 {
		t.Errorf("GetCandidatesByModality 被调用 %d 次，want 0（模态为空不该走过滤路径）", len(spy.modalityCalls))
	}
	if len(got) != 2 {
		t.Fatalf("候选数 = %d, want 2（阴性对照：老路径原样返回）", len(got))
	}
}

// TestRefreshedCandidatesAfterProbe_BlindProviderRefusesUnfilteredFallback 钉住
// 「宁可不换候选，也不换成模态不对的候选」：provider 过滤不了模态时，
// 刷新**不得**退回无过滤的查询（那正是事故链的那一步）。
func TestRefreshedCandidatesAfterProbe_BlindProviderRefusesUnfilteredFallback(t *testing.T) {
	blind := &modalityBlindProvider{}
	executor := &Executor{Provider: blind}

	got := executor.refreshedCandidatesAfterProbe(modalityGateParams("text"))

	if blind.plainCalls != 0 {
		t.Errorf("GetCandidates 被调用 %d 次 ⇒ provider 过滤不了模态时仍然退回了无过滤查询", blind.plainCalls)
	}
	if len(got) != 0 {
		t.Fatalf("候选 = %+v, want 空（调用方会退回 params.Candidates——那份已经过模态门）", got)
	}
}

// TestDispatchCandidatesForModel_UsesRequestModality 钉住 F11：dispatch 换
// 替代模型时的候选解析必须过请求模态门。
func TestDispatchCandidatesForModel_UsesRequestModality(t *testing.T) {
	spy, executor := modalityGateFixture()
	dctx := &dispatchCtx{
		initialModel: "initial",
		params:       modalityGateParams("vision"),
	}

	got := executor.dispatchCandidatesForModel(context.Background(), dctx, "alternate")

	if spy.plainCalls != 0 {
		t.Errorf("GetCandidates（无模态过滤）被调用 %d 次 ⇒ 换模态路径绕过了模态门", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 1 || spy.modalityCalls[0] != "vision" {
		t.Fatalf("GetCandidatesByModality 调用 = %v, want [vision]", spy.modalityCalls)
	}
	for _, candidate := range got {
		if candidate.CredentialID == 99 {
			t.Fatalf("替代模型的候选里出现了 embedding 绑定（credential 99）⇒ 模态门没生效")
		}
	}
	if len(got) != 1 {
		t.Fatalf("候选数 = %d, want 1: %+v", len(got), got)
	}
}

// TestDispatchCandidatesForModel_NoModalityKeepsLegacyPath 是阴性对照。
func TestDispatchCandidatesForModel_NoModalityKeepsLegacyPath(t *testing.T) {
	spy, executor := modalityGateFixture()
	dctx := &dispatchCtx{
		initialModel: "initial",
		params:       modalityGateParams(""),
	}

	got := executor.dispatchCandidatesForModel(context.Background(), dctx, "alternate")

	if spy.plainCalls != 1 {
		t.Errorf("GetCandidates 调用 = %d, want 1（模态为空 ⇒ 仍走无过滤旧路）", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 0 {
		t.Errorf("GetCandidatesByModality 被调用 %d 次，want 0", len(spy.modalityCalls))
	}
	if len(got) != 2 {
		t.Fatalf("候选数 = %d, want 2（阴性对照：老路径原样返回）", len(got))
	}
}

// TestProductionProviderSupportsModalityFiltering 钉住生产接线。
//
// ★ 执行器的候选再解析在**有模态**时依赖 provider 暴露
// GetCandidatesByModality。生产接线是
// `routingExec.Provider = provider.NewClient()`（cmd/gateway/main.go）。
// ⇒ 若哪天它被换成不暴露该方法的包装类型，那两条再解析路径会**静默降级**
//
//	（dispatch 换替代模型悄悄不换了、探针后刷新退回原候选），只在日志里留
//	一行 warning ⇒ 没有编译期信号、没有测试期信号。
//
// ⇒ executor.go 里有一条同源的**编译期断言**（`var _ modalityProviderResolver
// = (*provider.Client)(nil)`）；本测试是它的可读版本：把「为什么必须这样」
// 写在门里，而不是只留一行断言。
func TestProductionProviderSupportsModalityFiltering(t *testing.T) {
	var client any = &provider.Client{}
	if _, ok := client.(modalityProviderResolver); !ok {
		t.Fatal("*provider.Client 不实现 modalityProviderResolver ⇒ 生产上的候选再解析会静默降级：dispatch 换替代模型悄悄不换了、探针后刷新退回原候选")
	}
}

// TestDispatchCandidatesForModel_BlindProviderDoesNotSwitchUnfiltered 是
// F11 的「静默降级」阴性对照：provider 过滤不了模态时，换替代模型的候选
// 解析必须**拿不到候选**，而不是退回无过滤的查询。
//
// ★ 与 refresh 那条同源：退回无过滤的查询正是把 embedding 绑定塞回 chat
// 请求的那条路（上游 400 ⇒ 绑定翻红 ⇒ 事故同族）。
func TestDispatchCandidatesForModel_BlindProviderDoesNotSwitchUnfiltered(t *testing.T) {
	blind := &modalityBlindProvider{}
	executor := &Executor{Provider: blind}
	dctx := &dispatchCtx{
		initialModel: "initial",
		params:       modalityGateParams("text"),
	}

	got := executor.dispatchCandidatesForModel(context.Background(), dctx, "alternate")

	if blind.plainCalls != 0 {
		t.Errorf("GetCandidates 被调用 %d 次 ⇒ provider 过滤不了模态时仍然退回了无过滤查询", blind.plainCalls)
	}
	if len(got) != 0 {
		t.Fatalf("候选 = %+v, want 空（宁可不换模型，也不换成一个模态不对的模型）", got)
	}
}
