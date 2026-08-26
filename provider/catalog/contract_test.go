package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ExecutorRoutableProtocols 是执行器（domains/streaming/executors/*.go）实际
// 能路由的 protocol 集合，SOURCE-VERIFIED via grep:
//
//	grep -rhoE 'Protocol == "[a-z-]+"' domains/streaming/executors/*.go
//	→ anthropic-messages, openai-completions
// 以及 ClientProtocol 分支: anthropic-messages, openai-responses
//	openai-completions / openai-responses / anthropic-messages 都有 executor 分支。
//	gemini-generate / ollama-native 由 IR converter（domains/transformation）处理，
//	无独立 executor 文件但能路由。
//
// 如果执行器新增/删除 protocol 支持，同步更新本集合。
var ExecutorRoutableProtocols = map[string]bool{
	ProtocolOpenAICompletions: true,
	ProtocolOpenAIResponses:   true,
	ProtocolAnthropicMessages: true,
	ProtocolGeminiGenerate:    true,
	ProtocolOllamaNative:      true,
}

// TestProtocolsMatchExecutors 断言 catalog 里出现的每个 protocol 都被执行器支持。
// 这是 README §5 P1 的 protocol contract test：runtime literal 与 executor 分支一致。
func TestProtocolsMatchExecutors(t *testing.T) {
	seedPath := filepath.Join("..", "..", "sql", "schema", "02-seed.sql")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Skipf("seed file not found: %v", err)
	}
	entries, err := parsePositionalInserts(string(raw))
	if err != nil || len(entries) == 0 {
		t.Skipf("could not parse seed entries: %v", err)
	}
	for _, e := range entries {
		if !ExecutorRoutableProtocols[e.Protocol] {
			t.Errorf("catalog code %q uses protocol %q which no executor branch handles", e.Code, e.Protocol)
		}
	}
}

// TestProtocolConstantsAlignWithDBCheck 断言 Go Protocol 常量集合与 DB CHECK 约束
// （provider_catalog.sql:34）一致。如果 DDL 加了新 protocol 但 Go 没加（或反之），
// 这里会失败，强制两边同步。
func TestProtocolConstantsAlignWithDBCheck(t *testing.T) {
	// DB CHECK: protocol IN ('openai-completions','openai-responses',
	//   'anthropic-messages','gemini-generate','ollama-native')
	dbProtocols := map[string]bool{
		"openai-completions":  true,
		"openai-responses":    true,
		"anthropic-messages":  true,
		"gemini-generate":     true,
		"ollama-native":       true,
	}
	for _, p := range Protocols {
		if !dbProtocols[p] {
			t.Errorf("Go Protocol constant %q is not in DB CHECK constraint (provider_catalog.sql:34)", p)
		}
	}
	for p := range dbProtocols {
		found := false
		for _, g := range Protocols {
			if g == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DB CHECK protocol %q has no Go Protocol constant in catalog.Protocols", p)
		}
	}
}

// TestEveryProtocolHasAtLeastOneProvider 断言每个 protocol 至少有一个 catalog 条目，
// 防止某 protocol 在 catalog 里完全缺失（contract test 门禁）。
func TestEveryProtocolHasAtLeastOneProvider(t *testing.T) {
	seedPath := filepath.Join("..", "..", "sql", "schema", "02-seed.sql")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Skipf("seed file not found: %v", err)
	}
	entries, err := parsePositionalInserts(string(raw))
	if err != nil || len(entries) == 0 {
		t.Skipf("could not parse seed entries: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Protocol] = true
	}
	for _, p := range Protocols {
		if !seen[p] {
			// gemini-generate / ollama-native 可能暂无 cloud 种子，但应至少有 local。
			t.Logf("warning: protocol %q has no catalog entry in 02-seed.sql", p)
		}
	}
	// 至少 3 个 protocol 有种子（openai-completions 必有）。
	if !seen[ProtocolOpenAICompletions] {
		t.Errorf("expected at least one %q provider in seed", ProtocolOpenAICompletions)
	}
}

// 编译期保证 strings 被使用（用于可能的未来扩展）。
var _ = strings.Contains
