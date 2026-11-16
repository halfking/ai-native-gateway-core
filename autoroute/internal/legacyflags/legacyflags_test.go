package legacyflags

import (
	"testing"
)

func TestLoadReadsAllDeprecatedEnvs(t *testing.T) {
	// 设置全部 8 个 deprecated env (UseChannelQualityRouting 不在此包)
	envs := map[string]string{
		"AUTO_USE_SIMPLIFIED_SCORING": "true",
		"AUTO_USE_HOT_TOP3_POOL":      "true",
		"AUTO_USE_CACHE_REVALIDATION": "true",
		"AUTO_USE_48H_FALLBACK":       "true",
		"AUTO_ON_MESSAGES":            "true",
		"AUTO_ON_RESPONSES":           "true",
		"AUTO_ON_EMBEDDINGS":          "true",
		"AUTO_EMBEDDING_ROUTE":        "true",
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}

	f := Load()
	if !f.UseSimplifiedScoring || !f.UseHotTop3Pool || !f.UseCacheRevalidation || !f.Use48hFallback {
		t.Error("scoring/pool/revalidation/fallback flags not loaded")
	}
	if !f.AutoOnMessages || !f.AutoOnResponses || !f.AutoOnEmbeddings || !f.AutoEmbeddingRoute {
		t.Error("auto-on/embedding flags not loaded")
	}
}

func TestLoadDefaultsAllFalse(t *testing.T) {
	// 清空所有 env (t.Setenv 设空串模拟未设)
	for _, k := range []string{
		"AUTO_USE_SIMPLIFIED_SCORING", "AUTO_USE_HOT_TOP3_POOL", "AUTO_USE_CACHE_REVALIDATION",
		"AUTO_USE_48H_FALLBACK", "AUTO_ON_MESSAGES", "AUTO_ON_RESPONSES", "AUTO_ON_EMBEDDINGS",
		"AUTO_EMBEDDING_ROUTE",
	} {
		t.Setenv(k, "")
	}
	f := Load()
	// 评分/pool/重校验/48h 族与 embedding 族仍默认 false
	if f.UseSimplifiedScoring || f.UseHotTop3Pool || f.UseCacheRevalidation || f.Use48hFallback ||
		f.AutoOnEmbeddings || f.AutoEmbeddingRoute {
		t.Error("scoring/pool/revalidation/fallback/embedding flags should default to false")
	}
	// 2026-11-16（auto 模式可用性轮）：协议面 auto 默认开启——同一个
	// model="auto" 请求换协议就变成「模型 auto 不可用」是 auto 模式没做完，
	// 不是另一个协议不支持 auto。三协议面共用同一个 Decider。
	if !f.AutoOnMessages || !f.AutoOnResponses {
		t.Error("auto on messages/responses should default to true")
	}
}

// TestLoadProtocolAutoOptOut pins the escape hatch: an operator can still
// turn the protocol-face auto routing off per protocol.
func TestLoadProtocolAutoOptOut(t *testing.T) {
	t.Setenv("AUTO_ON_MESSAGES", "false")
	t.Setenv("AUTO_ON_RESPONSES", "false")
	f := Load()
	if f.AutoOnMessages || f.AutoOnResponses {
		t.Error("explicit env=false must opt out of protocol-face auto routing")
	}
}

func TestLoadInvalidValueFallsBack(t *testing.T) {
	t.Setenv("AUTO_USE_SIMPLIFIED_SCORING", "not-a-bool")
	f := Load()
	if f.UseSimplifiedScoring {
		t.Error("invalid bool should fall back to false")
	}
}
