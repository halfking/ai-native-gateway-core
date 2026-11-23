package main

// session_digest_init.go —— 归约分段抽取式摘要的装配（2026-11-23 大会话归约提速轮·第三步）。
//
// 归约把被压缩的历史交给模型时，此前只有「best-16 摘录」：前 16 条非空消息先占位，
// 后面的消息只有查询词重叠得分严格更高才挤得掉它们——几千条的 agent 会话，模型
// 看到的早期历史基本只是会话最开头的十几条。分段摘要在它之外再给一份覆盖首/中/尾
// 的抽取式纪要（设计见 internal/sessioncontext/digest.go）。
//
// 开关（env，重启生效；与其它 LLM_GATEWAY_* 开关同一口径）：
//
//	LLM_GATEWAY_SESSION_DIGEST=on                     off/0/false/no/disabled 关闭
//	                                                  （关闭时归约输出与引入摘要之前逐字节相同）
//	LLM_GATEWAY_SESSION_DIGEST_SEGMENT_BYTES=65536    分段目标大小（夹在 [8KB, 256KB]）
//	LLM_GATEWAY_SESSION_DIGEST_BUDGET_BYTES=16384     摘要正文总预算（夹在 [1KB, 64KB]）
//
// 默认开启。它改变的是模型看到的早期历史（多了一段至多 16KB 的纪要），不改受保护的
// 消息、不改 best-16 摘录；只使用通过了历史守卫的消息。

import (
	"log/slog"
	"os"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

type sessionDigestEnv struct {
	Disabled     bool
	SegmentBytes int
	BudgetBytes  int
}

// parseSessionDigestEnv 解析 LLM_GATEWAY_SESSION_DIGEST*；非法值回落默认并记日志。
//
// ★ 「关」的拼法显式列全：strconv.ParseBool 不认 off（上一轮缓存的开关就栽过）。
func parseSessionDigestEnv(getenv func(string) string) sessionDigestEnv {
	cfg := sessionDigestEnv{}
	switch strings.ToLower(strings.TrimSpace(getenv("LLM_GATEWAY_SESSION_DIGEST"))) {
	case "off", "0", "false", "no", "disabled":
		cfg.Disabled = true
	case "", "on", "1", "true", "yes", "enabled":
	default:
		slog.Warn("session digest switch unparsable; keeping default (on)", "value", getenv("LLM_GATEWAY_SESSION_DIGEST"))
	}
	if n, ok := envPositiveInt(getenv, "LLM_GATEWAY_SESSION_DIGEST_SEGMENT_BYTES"); ok {
		cfg.SegmentBytes = n
	}
	if n, ok := envPositiveInt(getenv, "LLM_GATEWAY_SESSION_DIGEST_BUDGET_BYTES"); ok {
		cfg.BudgetBytes = n
	}
	return cfg
}

// installSessionDigest 把摘要配置交给 chat handler（归约器从请求 ctx 里读它）。
func installSessionDigest(h *streaming.ChatHandler) {
	installSessionDigestWithEnv(h, os.Getenv)
}

func installSessionDigestWithEnv(h *streaming.ChatHandler, getenv func(string) string) {
	env := parseSessionDigestEnv(getenv)
	cfg := sessioncontext.DigestConfig{Enabled: !env.Disabled, SegmentBytes: env.SegmentBytes, BudgetBytes: env.BudgetBytes}
	h.SetDigestConfig(cfg)
	normalized := cfg.Normalized()
	slog.Info("session digest configured",
		"enabled", normalized.Enabled,
		"segment_bytes", normalized.SegmentBytes,
		"budget_bytes", normalized.BudgetBytes,
	)
}
