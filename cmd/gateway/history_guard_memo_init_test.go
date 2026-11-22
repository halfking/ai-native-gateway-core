package main

// 历史守卫判决缓存的装配测试（2026-11-23）：env 解析、类型化 nil、装配后
// 归约器确实拿到并行度。

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"net/http/httptest"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// historyGuardCtxForTest 取 prepareHistoryGuard 之后的 ctx（归约器读的就是它）。
func historyGuardCtxForTest(h *streaming.ChatHandler) context.Context {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	return streaming.HistoryGuardContextForTest(h, r)
}

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseHistoryMemoEnv(t *testing.T) {
	t.Run("默认", func(t *testing.T) {
		cfg := parseHistoryMemoEnv(envMap(nil))
		// 共享层默认**不启用**：单实例/蓝绿只有本地层就够，不把 Redis 的健康
		// 状况拉进请求路径。
		if cfg.Disabled || cfg.LocalEntries != defaultHistoryMemoLocalEntries || cfg.SharedTTL != 0 || cfg.SharedMaxUsedBytes != 512<<20 || cfg.SharedMaxWrites != 500_000 {
			t.Fatalf("defaults drifted: %+v", cfg)
		}
	})
	t.Run("关闭", func(t *testing.T) {
		if cfg := parseHistoryMemoEnv(envMap(map[string]string{"LLM_GATEWAY_HISTORY_GUARD_MEMO": "off"})); !cfg.Disabled {
			t.Fatal("off 必须关闭")
		}
		if cfg := parseHistoryMemoEnv(envMap(map[string]string{"LLM_GATEWAY_HISTORY_GUARD_MEMO": "0"})); !cfg.Disabled {
			t.Fatal("0 必须关闭")
		}
	})
	t.Run("共享层按需开启 / 自定义", func(t *testing.T) {
		for _, off := range []string{"", "0", "off"} {
			if cfg := parseHistoryMemoEnv(envMap(map[string]string{"LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL": off})); cfg.SharedTTL != 0 {
				t.Fatalf("TTL=%q ⇒ 不使用共享层: %v", off, cfg.SharedTTL)
			}
		}
		if cfg := parseHistoryMemoEnv(envMap(map[string]string{"LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL": "on"})); cfg.SharedTTL != exampleHistoryMemoSharedTTL {
			t.Fatalf("TTL=on ⇒ 建议值: %v", cfg.SharedTTL)
		}
		cfg := parseHistoryMemoEnv(envMap(map[string]string{
			"LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL":                 "90m",
			"LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_USED_MB":         "512",
			"LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_WRITES_PER_HOUR": "7",
			"LLM_GATEWAY_HISTORY_MEMO_LOCAL_ENTRIES":              "4096",
		}))
		if cfg.SharedTTL != 90*time.Minute || cfg.SharedMaxUsedBytes != 512<<20 || cfg.SharedMaxWrites != 7 || cfg.LocalEntries != 4096 {
			t.Fatalf("custom values not honored: %+v", cfg)
		}
	})
	t.Run("非法值回落默认而不是 panic", func(t *testing.T) {
		cfg := parseHistoryMemoEnv(envMap(map[string]string{
			"LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL":         "later",
			"LLM_GATEWAY_HISTORY_MEMO_LOCAL_ENTRIES":      "-5",
			"LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_USED_MB": "abc",
			"LLM_GATEWAY_HISTORY_GUARD_MEMO":              "maybe",
		}))
		// 解析不了的 TTL ⇒ 共享层保持关闭（宁可少一层加速，不可带着未知配置上线）。
		if cfg.Disabled || cfg.SharedTTL != 0 || cfg.LocalEntries != defaultHistoryMemoLocalEntries || cfg.SharedMaxUsedBytes != 512<<20 {
			t.Fatalf("invalid values must fall back: %+v", cfg)
		}
	})
}

// TestNewHistoryVerdictMemoTypedNil —— 没有共享层时 Shared 必须是**真 nil**
// （类型化 nil 会让缓存层调用方法时空指针解引用）。
func TestNewHistoryVerdictMemoTypedNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  historyMemoEnv
		rc   redis.UniversalClient
	}{
		{"no redis", historyMemoEnv{LocalEntries: 64, SharedTTL: time.Hour, SharedMaxUsedBytes: 1, SharedMaxWrites: 1}, nil},
		{"ttl off", historyMemoEnv{LocalEntries: 64}, redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newHistoryVerdictMemo(tc.cfg, tc.rc)
			if m == nil {
				t.Fatal("memo must be created")
			}
			m.Store(context.Background(), [32]byte{1}, true)
			if safe, ok := m.Lookup(context.Background(), [32]byte{1}); !ok || !safe {
				t.Fatalf("local layer must work without a shared layer: ok=%v safe=%v", ok, safe)
			}
			if st := m.Stats(); st.SharedErrors != 0 {
				t.Fatalf("shared layer must be truly absent: %+v", st)
			}
		})
	}
	if newHistoryVerdictMemo(historyMemoEnv{Disabled: true}, nil) != nil {
		t.Fatal("disabled ⇒ nil")
	}
}

// TestInstallHistoryGuardMemoWiresDetectorAndParallel —— 装配后：检测器的守卫
// 走缓存、归约 ctx 带并行度；共享层默认不碰 Redis，显式开启后才写。
func TestInstallHistoryGuardMemoWiresDetectorAndParallel(t *testing.T) {
	srv := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer rc.Close()
	h := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	d := sanitize.NewPatternDetector()
	h.SetHistoryGuardFactory(d.HistoryGuard)
	installHistoryGuardMemoWithEnv(h, d, rc, envMap(nil))

	// 归约 ctx：prepareHistoryGuard 之后应当带并行度（守卫可并发已声明）。
	guardCtx := historyGuardCtxForTest(h)
	if sessioncontext.ReduceWorkers(guardCtx) <= 1 {
		t.Fatalf("装配后归约 ctx 必须带并行度，实际 %d", sessioncontext.ReduceWorkers(guardCtx))
	}
	open := func() io.Reader { return strings.NewReader("plain billing note") }
	for i := 0; i < 2; i++ {
		if safe, err := d.HistoryGuard(context.Background())(context.Background(), open); err != nil || !safe {
			t.Fatalf("guard: safe=%v err=%v", safe, err)
		}
	}
	// 即使 Redis 可用，默认也不写共享层。
	if keys := srv.Keys(); len(keys) != 0 {
		t.Fatalf("shared cache must stay off by default: %v", keys)
	}

	// 显式开启：同一消息判定两次，共享层里只应出现一个键（第二次本地命中）。
	installHistoryGuardMemoWithEnv(h, d, rc, envMap(map[string]string{"LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL": "1h"}))
	g := d.HistoryGuard(context.Background())
	for i := 0; i < 2; i++ {
		if safe, err := g(context.Background(), open); err != nil || !safe {
			t.Fatalf("guard: safe=%v err=%v", safe, err)
		}
	}
	if keys := srv.Keys(); len(keys) != 1 || !strings.HasPrefix(keys[0], "llmgw:histmemo:") {
		t.Fatalf("shared cache keys = %v", keys)
	}
}

// TestInstallHistoryGuardMemoOffKeepsGuardParallelWithoutMemo —— 关闭缓存：
// 不装缓存、共享层无键；守卫仍可并发（并行声明与缓存开关无关）。
func TestInstallHistoryGuardMemoOffKeepsGuardParallelWithoutMemo(t *testing.T) {
	srv := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer rc.Close()
	h := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	d := sanitize.NewPatternDetector()
	installHistoryGuardMemoWithEnv(h, d, rc, envMap(map[string]string{"LLM_GATEWAY_HISTORY_GUARD_MEMO": "off", "LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL": "1h"}))
	if _, err := d.HistoryGuard(context.Background())(context.Background(), func() io.Reader { return strings.NewReader("x") }); err != nil {
		t.Fatal(err)
	}
	if keys := srv.Keys(); len(keys) != 0 {
		t.Fatalf("memo off must not write the shared cache: %v", keys)
	}
	if sessioncontext.ReduceWorkers(historyGuardCtxForTest(h)) <= 1 {
		t.Fatal("关闭缓存不影响并行声明")
	}
}
