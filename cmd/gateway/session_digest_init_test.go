package main

// 分段摘要装配的测试（2026-11-23 第三步）：env 解析、配置真的进了请求 ctx、关闭时不进。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestParseSessionDigestEnv(t *testing.T) {
	t.Run("默认开启", func(t *testing.T) {
		cfg := parseSessionDigestEnv(envMap(nil))
		if cfg.Disabled || cfg.SegmentBytes != 0 || cfg.BudgetBytes != 0 {
			t.Fatalf("defaults drifted: %+v", cfg)
		}
	})
	t.Run("关闭的拼法", func(t *testing.T) {
		for _, off := range []string{"off", "OFF", "0", "false", "no", "disabled", " off "} {
			if cfg := parseSessionDigestEnv(envMap(map[string]string{"LLM_GATEWAY_SESSION_DIGEST": off})); !cfg.Disabled {
				t.Fatalf("%q 必须关闭", off)
			}
		}
		for _, on := range []string{"on", "1", "true", "yes", "enabled"} {
			if cfg := parseSessionDigestEnv(envMap(map[string]string{"LLM_GATEWAY_SESSION_DIGEST": on})); cfg.Disabled {
				t.Fatalf("%q 必须保持开启", on)
			}
		}
	})
	t.Run("自定义", func(t *testing.T) {
		cfg := parseSessionDigestEnv(envMap(map[string]string{
			"LLM_GATEWAY_SESSION_DIGEST_SEGMENT_BYTES": "131072",
			"LLM_GATEWAY_SESSION_DIGEST_BUDGET_BYTES":  "4096",
		}))
		if cfg.SegmentBytes != 131072 || cfg.BudgetBytes != 4096 {
			t.Fatalf("custom values not honored: %+v", cfg)
		}
	})
	t.Run("非法值回落默认", func(t *testing.T) {
		cfg := parseSessionDigestEnv(envMap(map[string]string{
			"LLM_GATEWAY_SESSION_DIGEST":               "maybe",
			"LLM_GATEWAY_SESSION_DIGEST_SEGMENT_BYTES": "-5",
			"LLM_GATEWAY_SESSION_DIGEST_BUDGET_BYTES":  "abc",
		}))
		if cfg.Disabled || cfg.SegmentBytes != 0 || cfg.BudgetBytes != 0 {
			t.Fatalf("invalid values must fall back: %+v", cfg)
		}
	})
}

func TestInstallSessionDigestPutsConfigInRequestContext(t *testing.T) {
	h := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	installSessionDigestWithEnv(h, envMap(map[string]string{
		"LLM_GATEWAY_SESSION_DIGEST_SEGMENT_BYTES": "2000000", // 越界 ⇒ 夹回上限
		"LLM_GATEWAY_SESSION_DIGEST_BUDGET_BYTES":  "100",     // 越界 ⇒ 夹回下限
	}))
	ctx := historyGuardCtxForTest(h)
	cfg, ok := sessioncontext.DigestConfigFrom(ctx)
	if !ok || !cfg.Enabled {
		t.Fatalf("默认开启时请求 ctx 必须带摘要配置: ok=%v cfg=%+v", ok, cfg)
	}
	if cfg.SegmentBytes != 256<<10 || cfg.BudgetBytes != 1<<10 {
		t.Fatalf("越界值应被夹回区间: %+v", cfg)
	}
	// 装配层与归约器对「归一化」的口径一致。
	if got := h.DigestConfigured().Normalized(); got != cfg {
		t.Fatalf("handler 里的配置与 ctx 里的不一致: %+v vs %+v", got, cfg)
	}

	// 关闭：请求 ctx 不带摘要配置 ⇒ 归约输出与此前逐字节相同。
	off := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	installSessionDigestWithEnv(off, envMap(map[string]string{"LLM_GATEWAY_SESSION_DIGEST": "off"}))
	if cfg, ok := sessioncontext.DigestConfigFrom(historyGuardCtxForTest(off)); ok || cfg.Enabled {
		t.Fatalf("关闭时请求 ctx 不该带摘要配置: ok=%v cfg=%+v", ok, cfg)
	}
}

func TestSessionDigestSwitchIsIndependentOfMemoSwitch(t *testing.T) {
	// 两个开关各管各的：关缓存不影响摘要，关摘要不影响并行度。
	h := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	installHistoryGuardMemoWithEnv(h, sanitize.NewPatternDetector(), nil, envMap(map[string]string{"LLM_GATEWAY_HISTORY_GUARD_MEMO": "off"}))
	installSessionDigestWithEnv(h, envMap(nil))
	ctx := historyGuardCtxForTest(h)
	if cfg, ok := sessioncontext.DigestConfigFrom(ctx); !ok || !cfg.Enabled {
		t.Fatal("关缓存不应影响摘要")
	}
	if sessioncontext.ReduceWorkers(ctx) <= 1 {
		t.Fatal("关缓存不应影响并行度")
	}
}
