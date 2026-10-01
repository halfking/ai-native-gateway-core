package admin

import "testing"

// 2026-10-01 245 半切换态回归钉测：admin /api/system/version 此前只认硬编码
// 根路径 /opt/llm-gateway-go/version.json；seamless slots 布局下根文件可能
// 是滞留真文件（部署中断遗留），端点恒报陈旧版本，与 healthz（读
// LLM_GATEWAY_VERSION_FILE=slots/<port>/version.json）口径分叉。
func TestVersionJSONCandidatesPrefersEnvFile(t *testing.T) {
	t.Setenv("LLM_GATEWAY_VERSION_FILE", "/tmp/hz-test/slots/8781/version.json")
	got := versionJSONCandidates()
	if len(got) == 0 || got[0] != "/tmp/hz-test/slots/8781/version.json" {
		t.Fatalf("env path must be the first candidate, got %v", got)
	}
	rootSeen := false
	for _, p := range got[1:] {
		if p == "/opt/llm-gateway-go/"+llmGatewayVersionJSON {
			rootSeen = true
		}
	}
	if !rootSeen {
		t.Fatalf("legacy root path must remain as fallback, got %v", got)
	}
}

func TestVersionJSONCandidatesWithoutEnvKeepsLegacyOrder(t *testing.T) {
	t.Setenv("LLM_GATEWAY_VERSION_FILE", "")
	got := versionJSONCandidates()
	if len(got) == 0 || got[0] != "/opt/llm-gateway-go/"+llmGatewayVersionJSON {
		t.Fatalf("legacy root path must be first when env unset, got %v", got)
	}
	for i, p := range got[1:] {
		if p == got[i] {
			t.Fatalf("duplicate candidate path: %v", got)
		}
	}
}
