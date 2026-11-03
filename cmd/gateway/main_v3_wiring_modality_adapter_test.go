package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ── F-1（2026-11-04，40bbe7a54 同族第二处）：providerClientAdapter 的模态门 ──
//
// 压缩/摘要依赖（compression.Dependencies.Provider = providerClientAdapter）
// 的候选解析现在固定走 text 模态门（compression.resolveCandidatesByModality）。
// adapter 的两条语义在这里钉住：
//
//	① 底层 resolver 支持模态过滤 ⇒ 透传 (model, profile, tenantID, modality)
//	   并投影候选；**不得**顺带调用无过滤的 GetCandidates。
//	② 底层 resolver 过滤不了模态 ⇒ 返回 errProviderModalityUnsupported ⇒
//	   compression 包该模型无候选、退下一个压缩模型——**绝不退回无过滤查询**
//	   （那会把登记为 embedding 的绑定塞回 chat 形态的压缩请求）。

// modalityAwareStubResolver 模拟 *provider.Client：模态过滤 + 无过滤两条腿都有。
type modalityAwareStubResolver struct {
	byModality map[string][]provider.Candidate
	unfiltered []provider.Candidate

	modalityArgs []string
	plainCalls   int
}

func (s *modalityAwareStubResolver) Enabled() bool { return true }

func (s *modalityAwareStubResolver) ModelKnown(context.Context, string) bool { return true }

func (s *modalityAwareStubResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	s.plainCalls++
	return append([]provider.Candidate(nil), s.unfiltered...), provider.DefaultPolicy(), nil
}

func (s *modalityAwareStubResolver) GetCandidatesByModality(_ context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	s.modalityArgs = append(s.modalityArgs, model+"|"+profile+"|"+tenantID+"|"+modality)
	return append([]provider.Candidate(nil), s.byModality[modality]...), provider.DefaultPolicy(), nil
}

// blindStubResolver 只有无过滤腿——模拟「provider 过滤不了模态」的底层。
type blindStubResolver struct {
	plainCalls int
}

func (s *blindStubResolver) Enabled() bool { return true }

func (s *blindStubResolver) ModelKnown(context.Context, string) bool { return true }

func (s *blindStubResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	s.plainCalls++
	return []provider.Candidate{{CredentialID: 99, ProviderID: 9, RawModel: "m-embed", Routable: true}}, provider.DefaultPolicy(), nil
}

func TestProviderClientAdapter_GetCandidatesByModality_PassesThroughAndConverts(t *testing.T) {
	stub := &modalityAwareStubResolver{
		byModality: map[string][]provider.Candidate{
			"text": {{CredentialID: 11, ProviderID: 1, RawModel: "m", BaseURL: "https://up.invalid", APIKey: "k", Protocol: "openai-completions", Routable: true}},
		},
		unfiltered: []provider.Candidate{
			{CredentialID: 11, ProviderID: 1, RawModel: "m", Routable: true},
			{CredentialID: 99, ProviderID: 9, RawModel: "m-embed", Routable: true},
		},
	}
	adapter := providerClientAdapter{r: stub}

	got, err := adapter.GetCandidatesByModality(context.Background(), "m", "profile-a", "tenant-a", "text")

	if err != nil {
		t.Fatalf("GetCandidatesByModality: %v", err)
	}
	if stub.plainCalls != 0 {
		t.Errorf("无过滤的 GetCandidates 被调用 %d 次 ⇒ adapter 绕过了模态门", stub.plainCalls)
	}
	if len(stub.modalityArgs) != 1 || stub.modalityArgs[0] != "m|profile-a|tenant-a|text" {
		t.Fatalf("透传参数 = %v, want [m|profile-a|tenant-a|text]", stub.modalityArgs)
	}
	if len(got) != 1 || got[0].CredentialID != 11 || got[0].RawModel != "m" || !got[0].Available {
		t.Fatalf("投影候选 = %+v, want credential 11 的可用候选", got)
	}
}

func TestProviderClientAdapter_GetCandidatesByModality_BlindResolverDoesNotFallBack(t *testing.T) {
	stub := &blindStubResolver{}
	adapter := providerClientAdapter{r: stub}

	got, err := adapter.GetCandidatesByModality(context.Background(), "m", "profile-a", "tenant-a", "text")

	if !errors.Is(err, errProviderModalityUnsupported) {
		t.Fatalf("err = %v, want errProviderModalityUnsupported", err)
	}
	if got != nil {
		t.Fatalf("候选 = %+v, want nil（不退回无过滤查询）", got)
	}
	if stub.plainCalls != 0 {
		t.Errorf("无过滤的 GetCandidates 被调用 %d 次 ⇒ adapter 退回了无过滤查询", stub.plainCalls)
	}
}

func TestProviderClientAdapter_GetCandidatesByModality_NilResolverErrors(t *testing.T) {
	adapter := providerClientAdapter{}
	if _, err := adapter.GetCandidatesByModality(context.Background(), "m", "p", "t", "text"); err == nil {
		t.Fatal("nil resolver 必须报错，不能产出空候选让上层误判「无候选」")
	}
}
