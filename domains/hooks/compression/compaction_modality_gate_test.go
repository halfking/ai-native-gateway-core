package compression

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	summarymodel "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary"
)

// ── F-1 的 hooks 侧闸门（2026-11-04，40bbe7a54 同族第二处）────────────────
//
// 压缩/摘要链（compaction.go 的 pickCompactionCandidates +
// summary_client.go 的 getSummaryCandidates）此前走**无模态过滤**的
// GetCandidates/GetCandidatesForTenant ⇒ 登记为 embedding 的绑定会被发
// chat 形状的压缩/摘要请求 ⇒ 上游 400（"does not support chat"）⇒
// **压缩/摘要静默失败**退机械裁剪。
//
// ★ 口径与 executors 侧（40bbe7a54）逐字对齐：压缩/摘要都是**纯文本**的
//   摘要调用 ⇒ 模态固定 "text"（「text 只排除 embedding」，vision/audio
//   仍放行）⇒ 风险极低。
// ★ provider 侧过滤不了模态（adapter 返回 errProviderModalityUnsupported）
//   ⇒ 该模型没有候选 ⇒ 退下一个压缩模型 / 降级，**不退回无过滤查询**。

// gateSpyProvider 模拟生产 provider 的模态过滤：text 查询只含 text 绑定，
// 无过滤查询会额外含 embedding 绑定；调用被记录供门判据使用。
type gateSpyProvider struct {
	byModality map[string][]ProviderCandidate
	unfiltered []ProviderCandidate

	modalityCalls   []string
	modalityTenants []string
	plainCalls      int
	resolveErr      error
}

func (p *gateSpyProvider) Enabled() bool { return true }

func (p *gateSpyProvider) GetCandidates(_ context.Context, _, _ string) ([]ProviderCandidate, error) {
	p.plainCalls++
	return append([]ProviderCandidate(nil), p.unfiltered...), nil
}

func (p *gateSpyProvider) GetCandidatesByModality(_ context.Context, _, _, tenantID, modality string) ([]ProviderCandidate, error) {
	p.modalityCalls = append(p.modalityCalls, modality)
	p.modalityTenants = append(p.modalityTenants, tenantID)
	if p.resolveErr != nil {
		return nil, p.resolveErr
	}
	return append([]ProviderCandidate(nil), p.byModality[modality]...), nil
}

func gateSpyFixture() *gateSpyProvider {
	return &gateSpyProvider{
		byModality: map[string][]ProviderCandidate{
			"text": {
				{CredentialID: 11, ProviderID: 1, RawModel: "m", BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true},
			},
		},
		unfiltered: []ProviderCandidate{
			{CredentialID: 11, ProviderID: 1, RawModel: "m", BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true},
			{CredentialID: 99, ProviderID: 9, RawModel: "m-embed", BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true},
		},
	}
}

// TestPickCompactionCandidates_ExcludesEmbeddingBinding 钉门 J：压缩候选
// **不含** embedding 绑定，**且**仍然拿到 text 绑定（不是把候选全滤光）。
func TestPickCompactionCandidates_ExcludesEmbeddingBinding(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy := gateSpyFixture()

	got := pickCompactionCandidates(context.Background(), &Dependencies{Provider: spy}, "profile-a")

	for _, candidate := range got {
		if candidate.CredentialID == 99 {
			t.Fatalf("压缩候选里出现了 embedding 绑定（credential 99）⇒ 压缩请求会被上游 400 ⇒ 压缩静默失败退机械裁剪")
		}
	}
	if len(got) != 1 || got[0].CredentialID != 11 {
		t.Fatalf("压缩候选 = %+v, want 1 个（credential 11 的 text 绑定）", got)
	}
}

// TestPickCompactionCandidates_UsesTextModalityGate 钉门 K：压缩链**必须**
// 以 "text" 模态查询，且**不得**调用无模态过滤的 GetCandidates。
func TestPickCompactionCandidates_UsesTextModalityGate(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy := gateSpyFixture()

	pickCompactionCandidates(context.Background(), &Dependencies{Provider: spy}, "profile-a")

	if spy.plainCalls != 0 {
		t.Errorf("无模态过滤的 GetCandidates 被调用 %d 次 ⇒ 压缩链绕过了模态门", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 1 || spy.modalityCalls[0] != "text" {
		t.Fatalf("GetCandidatesByModality 调用 = %v, want [text]（压缩是纯文本摘要调用）", spy.modalityCalls)
	}
}

// TestPickCompactionCandidates_ResolveFailureDoesNotFallBack 钉门 L（阴性
// 对照）：模态过滤解析失败（含 provider 侧过滤不了模态：adapter 返回
// errProviderModalityUnsupported 的形状）时**不得**退回无过滤查询。
//
// ★ 与 executors 侧门 L 同一原则：「宁可不换候选，也不换一个模态不对的
// 候选」。退回无过滤 = 把 embedding 绑定塞回压缩链 = 回到 F-1 的老问题。
// ⇒ 该模型没有候选 ⇒ 调用方退下一个模型 / 降级机械裁剪。
func TestPickCompactionCandidates_ResolveFailureDoesNotFallBack(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy := &gateSpyProvider{resolveErr: errors.New("provider resolver does not support modality filtering")}

	got := pickCompactionCandidates(context.Background(), &Dependencies{Provider: spy}, "profile-a")

	if spy.plainCalls != 0 {
		t.Errorf("无模态过滤的 GetCandidates 被调用 %d 次 ⇒ 解析失败时退回了无过滤查询", spy.plainCalls)
	}
	if len(got) != 0 {
		t.Fatalf("压缩候选 = %+v, want 空（模态解析失败 ⇒ 不退回无过滤）", got)
	}
}

// TestPickCompactionCandidates_ResolveFailureDegradesWithLog 钉门 M：解析
// 全军覆没 ⇒ 压缩链降级（返回原 body 交机械裁剪），且**留一条可观测日志**
// （tryLLMContextCompaction 的 "compaction: no candidate" WARN），不得静默。
func TestPickCompactionCandidates_ResolveFailureDegradesWithLog(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "m")
	spy := &gateSpyProvider{resolveErr: errors.New("modality resolve unavailable")}

	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, ok := tryLLMContextCompaction(context.Background(), &Dependencies{Provider: spy}, "default", "openai-completions", body)

	if ok {
		t.Fatal("模态解析失败 ⇒ LLM 压缩必须降级（ok=false）")
	}
	if string(got) != string(body) {
		t.Error("降级时应原样返回 body 交机械裁剪")
	}
	if !strings.Contains(buf.String(), "compaction: no candidate") {
		t.Fatalf("降级时没有可观测日志：log=%q, want \"compaction: no candidate\"", buf.String())
	}
}

// TestSummaryClientComplete_UsesTextModalityGate 钉门 N：摘要链同样以
// "text" 模态解析候选，且纯文本模型照常完成摘要（不是把链路掐死）。
func TestSummaryClientComplete_UsesTextModalityGate(t *testing.T) {
	oldDoer := defaultHTTPDoer
	t.Cleanup(func() { defaultHTTPDoer = oldDoer })
	defaultHTTPDoer = func(req *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}
	spy := gateSpyFixture()

	out, err := newSummaryClientAdapter(&Dependencies{Provider: spy}, "", "tenant-a").
		Complete(context.Background(), "prompt", summarymodel.WithModel("m"))

	if err != nil || out != "ok" {
		t.Fatalf("Complete = (%q, %v), want (ok, nil)（纯文本模型必须照常选中）", out, err)
	}
	if spy.plainCalls != 0 {
		t.Errorf("无模态过滤的 GetCandidates 被调用 %d 次 ⇒ 摘要链绕过了模态门", spy.plainCalls)
	}
	if len(spy.modalityCalls) != 1 || spy.modalityCalls[0] != "text" {
		t.Fatalf("GetCandidatesByModality 调用 = %v, want [text]", spy.modalityCalls)
	}
	if len(spy.modalityTenants) != 1 || spy.modalityTenants[0] != "tenant-a" {
		t.Fatalf("tenantID 透传 = %v, want [tenant-a]（租户路由策略不能因模态门丢失）", spy.modalityTenants)
	}
}

// TestSummaryClientResolveFailureLogsWarn 钉门 O：摘要候选解析失败 ⇒
// Complete 返回错误（上层跳过摘要）且**留一条可观测日志**，不得静默。
func TestSummaryClientResolveFailureLogsWarn(t *testing.T) {
	spy := &gateSpyProvider{resolveErr: errors.New("modality resolve unavailable")}

	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	out, err := newSummaryClientAdapter(&Dependencies{Provider: spy}, "", "tenant-a").
		Complete(context.Background(), "prompt", summarymodel.WithModel("m"))

	if err == nil || out != "" {
		t.Fatalf("解析失败 ⇒ Complete 必须报错，got (%q, %v)", out, err)
	}
	if !strings.Contains(buf.String(), "summary: resolve candidates failed") {
		t.Fatalf("解析失败时没有可观测日志：log=%q, want \"summary: resolve candidates failed\"", buf.String())
	}
}

// TestCompressionCandidateResolution_SourceHasNoDirectUnfilteredQuery 是
// 结构门（与 executors 侧门 M 同族）：pickCompactionCandidates 与
// getSummaryCandidates 的函数体里**不得**直接引用 GetCandidates /
// GetCandidatesForTenant ⇒ 必须走 resolveCandidatesByModality（唯一入口）。
//
// ★ 为什么要有这道结构门：门 J/K/L/O 靠 fake provider 的行为来判；若有人把
// 实现改回直接调 Provider.GetCandidates，行为门会红，但若同时把 fake 换成
// 「无过滤也返回 text 绑定」，行为门就可能误绿 ⇒ 结构门兜住。
func TestCompressionCandidateResolution_SourceHasNoDirectUnfilteredQuery(t *testing.T) {
	targets := map[string]string{
		"pickCompactionCandidates": "compaction.go",
		"getSummaryCandidates":     "summary_client.go",
	}
	for fnName, file := range targets {
		t.Run(fnName, func(t *testing.T) {
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatalf("解析 %s 失败：%v", file, err)
			}
			var fn *ast.FuncDecl
			for _, decl := range parsed.Decls {
				f, ok := decl.(*ast.FuncDecl)
				if ok && f.Name.Name == fnName {
					fn = f
					break
				}
			}
			if fn == nil {
				t.Fatalf("在 %s 里找不到 %s", file, fnName)
			}
			// 判据用「任何 SelectorExpr 形态的无过滤查询引用」，不只是直接调用：
			// 函数值别名（`getCands := p.GetCandidates; getCands(...)`）同样会把
			// 无过滤查询塞回压缩链，只看 CallExpr 的写法会让它静默通过。
			found := ""
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "GetCandidates" || sel.Sel.Name == "GetCandidatesForTenant" {
					found = sel.Sel.Name
				}
				return true
			})
			if found != "" {
				t.Fatalf("%s 里出现了 %s 引用（直接调用或函数值别名）⇒ 必须走 resolveCandidatesByModality（唯一入口，否则 embedding 绑定会进压缩/摘要链）", fnName, found)
			}
			// 恒绿护栏：这道门必须真的看到了那个函数体（否则「找不到函数」之外的
			// 情况会让门静默通过）。
			var buf strings.Builder
			_ = ast.Fprint(&buf, fset, fn.Body, nil)
			if buf.Len() == 0 {
				t.Fatalf("%s 的函数体是空的 ⇒ 门失去了意义", fnName)
			}
		})
	}
}
