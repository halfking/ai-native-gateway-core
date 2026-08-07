package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/autocombo"
)

func TestChatHandler_ShouldTryOmniFree(t *testing.T) {
	h := &ChatHandler{} // no resolver/factory → 全部 false
	if h.shouldTryOmniFree("auto/free") {
		t.Errorf("nil resolver/factory should disable OmniFree")
	}

	h.autoComboResolver = autocombo.NewResolver(nil)
	h.autoComboFactory = autocombo.NewVirtualFactory(nil, nil)

	cases := map[string]bool{
		"":                 false,
		"auto":             false, // 精确 auto, 仍归 autoroute.Decider
		"auto/free":        true,
		"auto/best-free":   true,
		"auto/coding:free": true,
		"openai/gpt-3.5":   false,
		"gpt-4o":           false,
	}
	for model, want := range cases {
		if got := h.shouldTryOmniFree(model); got != want {
			t.Errorf("shouldTryOmniFree(%q) = %v, want %v", model, got, want)
		}
	}
}

// shouldTryOmniFree 在 nil deps / 精确 auto / 普通模型 三种情形下的行为由
// TestChatHandler_ShouldTryOmniFree 覆盖。resolveOmniFreeCandidates 需要
// Resolver + Provider 完整接口, 通过 main.go 注入 production 依赖; 单元级
// 集成测试放在 cmd/gateway 的 E2E 路径中处理, 不在此 mock 整个 PG pool。