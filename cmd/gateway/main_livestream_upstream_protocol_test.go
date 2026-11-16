// main_livestream_upstream_protocol_test.go — /live 阶段树 Phase 1：
// 兜底投影路径也必须带上游协议（2026-11-16）。
//
// ★ 为什么兜底路径也要带：只改 hub 主路径 ⇒ 「无 hub 时缓存命中率不可判定」
// 变成**偶发**现象（同一类请求有时能算有时不能）—— 偶发比恒定更难排查。
// 断言：有值 ⇒ 如实带上；缺省 / entry 为 nil ⇒ 保持空（前端显示「—」）。
package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestFallbackUpstreamProtocol(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	if got := fallbackUpstreamProtocol(&telemetry.RequestLogEntry{UpstreamProtocol: strPtr("anthropic")}); got != "anthropic" {
		t.Errorf("fallbackUpstreamProtocol = %q, want %q", got, "anthropic")
	}
	// 空白串 ⇒ 归一成空（未上报），不把 " " 当成协议名。
	if got := fallbackUpstreamProtocol(&telemetry.RequestLogEntry{UpstreamProtocol: strPtr("  ")}); got != "" {
		t.Errorf("blank upstream_protocol must normalize to empty, got %q", got)
	}
	if got := fallbackUpstreamProtocol(&telemetry.RequestLogEntry{}); got != "" {
		t.Errorf("absent upstream_protocol must stay empty, got %q", got)
	}
	if got := fallbackUpstreamProtocol(nil); got != "" {
		t.Errorf("nil entry must stay empty, got %q", got)
	}
}
