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
	// 全部默认 false
	if f.UseSimplifiedScoring || f.UseHotTop3Pool || f.UseCacheRevalidation || f.Use48hFallback ||
		f.AutoOnMessages || f.AutoOnResponses || f.AutoOnEmbeddings || f.AutoEmbeddingRoute {
		t.Error("defaults should all be false")
	}
}

func TestLoadInvalidValueFallsBack(t *testing.T) {
	t.Setenv("AUTO_USE_SIMPLIFIED_SCORING", "not-a-bool")
	f := Load()
	if f.UseSimplifiedScoring {
		t.Error("invalid bool should fall back to false")
	}
}
