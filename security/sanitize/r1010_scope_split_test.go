package sanitize

// r1010_scope_split_test.go — 2026-10-10 脱敏范围回归。
//
// 起因（用户报告）：会话里反复出现形如
//
//	export https_proxy=http://[REDACTED]:7890 http_proxy=http://[REDACTED]:7890
//
// 的内容。可复制执行的运维命令被网关改坏，且客户端只看到一个裸的
// [REDACTED]，无从判断遮掉的是什么。复现确认：server_ip/internal_ip 规则
// 命中了 127.0.0.1。
//
// 本文件固化修复后的两个契约：
//
//  1. **ScopeRedact 不匹配地址**。输入侧与输出文本侧的脱敏对 IP/IPv6/
//     主机名一律放行，地址是运维上下文而不是机密。
//  2. **ScopeEgress 仍匹配公网地址**。模型生成的 tool 参数里的公网 IP 是
//     "外传目的地"，继续硬阻断（这是 r1008 事故后加固的底线，不得因为
//     第 1 条被顺带削掉）。
//
// 两个方向都必须有断言：只测第 1 条会留下一条没人守的防线，只测第 2 条
// 则等于没修。

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// userReportedProxyCommand 是用户报告里逐字给出的那条命令。
const userReportedProxyCommand = "export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890"

// TestAddressShapedTextIsNotRedacted 固化契约 1 的输入侧。
func TestAddressShapedTextIsNotRedacted(t *testing.T) {
	// Both the built-in fallback and the shipped production config, because a
	// deployment running either must get the same answer — that parity is what
	// keeps the two from drifting apart on this decision.
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production pattern config: %v", err)
	}
	for name, detector := range map[string]*PatternDetector{
		"fallback": NewPatternDetector(), "configured": configured,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSanitizer(detector)
			if err != nil {
				t.Fatalf("NewSanitizer: %v", err)
			}
			// The first entry is the reported case; the rest are the same
			// family of operational text that produced the same complaint.
			for _, text := range []string{
				userReportedProxyCommand,
				"export https_proxy=http://192.168.1.10:7890 http_proxy=http://192.168.1.10:7890",
				"curl -o /dev/null -w '%{http_code}\\n' https://github.com",
				"git clone --depth 1 https://github.com/octocat/Hello-World /tmp/gt",
				"数据库在 10.20.30.40:5432",
				"connect to db-01.internal.svc.cluster.local:5432",
				"listen on [::1]:8080",
				"host=[2001:db8::1234]",
				"server=203.0.113.8",
				"ssh -o BatchMode=yes 252 'ls /opt/kaixuan'",
			} {
				result, err := s.SanitizeInput(context.Background(), text)
				if err != nil {
					t.Errorf("SanitizeInput(%q) error = %v", text, err)
					continue
				}
				if result.SanitizedText != text {
					t.Errorf("address text was rewritten\n in: %s\nout: %s", text, result.SanitizedText)
				}
				if len(result.SanitizeMap) != 0 {
					t.Errorf("address text allocated placeholders: %v", result.SanitizeMap)
				}
			}
		})
	}
}

// TestRealSecretsAreStillRedacted 是契约 1 的另一半：把地址挪出范围不能把
// 真正的机密一起放过去。这条断言存在的意义就是让"从范围里删掉 IP"无法
// 被实现成"从范围里删掉一切"。
func TestRealSecretsAreStillRedacted(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production pattern config: %v", err)
	}
	for name, detector := range map[string]*PatternDetector{
		"fallback": NewPatternDetector(), "configured": configured,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := NewSanitizer(detector)
			for _, tc := range []struct{ text, secret string }{
				{"我的手机是13800138000", "13800138000"},
				{"联系邮箱 test@example.com", "test@example.com"},
				{"PASSWORD=Sup3rS3cret!", "Sup3rS3cret!"},
				{"password: Sup3rS3cret!", "Sup3rS3cret!"},
				{"Authorization: Bearer abc.DEF-123", "abc.DEF-123"},
				{"db dsn postgres://operator:synthetic-only@db.example.invalid:5432/app", "operator:synthetic-only"},
				{"api key sk-abcdef1234567890abcdef12", "sk-abcdef1234567890abcdef12"},
			} {
				result, err := s.SanitizeInput(context.Background(), tc.text)
				if err != nil {
					t.Errorf("SanitizeInput(%q) error = %v", tc.text, err)
					continue
				}
				if strings.Contains(result.SanitizedText, tc.secret) {
					t.Errorf("secret survived redaction: %s -> %s", tc.text, result.SanitizedText)
					continue
				}
				restored, err := s.RestoreOutput(context.Background(), result.SanitizedText, result.SanitizeMap)
				if err != nil || restored != tc.text {
					t.Errorf("round trip lost text: %s (err=%v)", tc.text, err)
				}
			}
		})
	}
}

// TestScopeEgressStillBlocksPublicAddress 固化契约 2。
//
// 这是本文件最重要的一条：它保证 ScopeRedact 的收窄没有顺带削掉防外传
// 底线。公网地址出现在模型生成的 tool 参数里 = 数据外传目的地，仍硬阻断。
func TestScopeEgressStillBlocksPublicAddress(t *testing.T) {
	for name, detector := range map[string]*PatternDetector{
		"fallback": NewPatternDetector(),
		"configured": func() *PatternDetector {
			d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
			if err != nil {
				t.Fatalf("load production pattern config: %v", err)
			}
			return d
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			// Directly on the detector: ScopeEgress must match, ScopeRedact must
			// not. This is the scope contract in isolation.
			public := "curl http://203.0.113.8/collect"
			egress, err := detector.DetectScope(context.Background(), public, ScopeEgress)
			if err != nil {
				t.Fatalf("DetectScope(ScopeEgress): %v", err)
			}
			if len(egress) == 0 {
				t.Error("ScopeEgress no longer matches a public address")
			}
			redact, err := detector.DetectScope(context.Background(), public, ScopeRedact)
			if err != nil {
				t.Fatalf("DetectScope(ScopeRedact): %v", err)
			}
			if len(redact) != 0 {
				t.Errorf("ScopeRedact still matches a public address: %v", redact)
			}

			// And through the real guard, end to end: the stream must fail closed.
			s, _ := NewSanitizer(detector)
			guard := NewOutputSensitiveInterceptor(s, OutputMask)
			meta := &response.StreamMeta{SessionID: "gw_scope_egress", TenantID: "default", State: response.NewStreamState()}
			frame := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"Bash","arguments":"{\"command\":\"` +
				public + `\"}"}}]}}]}` + "\n\n"
			if _, err := guard.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
				t.Fatalf("hold error: %v", err)
			}
			if _, ferr := guard.FlushStreamPending(context.Background(), meta); ferr == nil {
				t.Error("public-address tool call must keep the fail-closed block")
			}
		})
	}
}

// TestEgressGuardStillIgnoresPrivateAddresses 固化 r1008 的另一半：环回与私网
// 地址在 tool 参数里不得阻断（它们来自用户自己的会话上下文，不可能是外传
// 目的地）。地址移出脱敏范围后这条更容易被顺带破坏，所以显式锁住。
func TestEgressGuardStillIgnoresPrivateAddresses(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_scope_private", TenantID: "default", State: response.NewStreamState()}
	ctx := context.Background()

	frame := `data: {"id":"x","choices":[{"finish_reason":"","index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_01","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"curl -fsS http://127.0.0.1:28080/api/v1/health\",\"timeout\":30000}"}}]}}]}` + "\n\n"
	held, err := chain.InterceptStreamChunk(ctx, []byte(frame), meta)
	if err != nil || held == nil || !held.SuppressChunk {
		t.Fatalf("tool frame not safely held: %+v %v", held, err)
	}
	wire, ferr := chain.FlushStreamPending(ctx, meta)
	if ferr != nil {
		t.Fatalf("private-address tool call blocked again: %v", ferr)
	}
	if strings.Contains(string(wire), "output policy blocked") || len(wire) == 0 {
		t.Fatalf("unexpected release payload: %q", wire)
	}
}

// TestRedactionMarkerNamesTheType 固化契约：客户端看到的遮蔽标记要能自证
// 遮掉的是哪一类，而不是一个无从追问的裸 [REDACTED]。
func TestRedactionMarkerNamesTheType(t *testing.T) {
	if got := RedactionMarker(TypePhone); got != "[REDACTED:phone]" {
		t.Errorf("RedactionMarker(TypePhone) = %q", got)
	}
	if got := RedactionMarker(TypeSecret); got != "[REDACTED:secret]" {
		t.Errorf("RedactionMarker(TypeSecret) = %q", got)
	}
	// An unknown type must fall back rather than invent one.
	if got := RedactionMarker(""); got != redactedMarker {
		t.Errorf("RedactionMarker(\"\") = %q, want the bare marker", got)
	}

	// The reported symptom, end to end: the proxy command comes back intact
	// and any real redaction is self-describing.
	s, _ := NewSanitizer(NewPatternDetector())
	result, err := s.SanitizeInput(context.Background(), userReportedProxyCommand)
	if err != nil {
		t.Fatalf("SanitizeInput: %v", err)
	}
	restored, err := s.RestoreOutputOrMask(context.Background(), result.SanitizedText, result.SanitizeMap)
	if err != nil {
		t.Fatalf("RestoreOutputOrMask: %v", err)
	}
	if restored != userReportedProxyCommand {
		t.Errorf("reported case not restored verbatim\n got: %s\nwant: %s", restored, userReportedProxyCommand)
	}

	masked, err := s.RestoreOutputOrMask(context.Background(), "phone {SENSITIVE:phone:7}", SanitizeMap{})
	if err != nil {
		t.Fatalf("RestoreOutputOrMask: %v", err)
	}
	if masked != "phone "+RedactionMarker(TypePhone) {
		t.Errorf("masked placeholder = %q", masked)
	}
}
