package outputcompliance

// r1010_internal_ip_default_test.go — 2026-10-10 脱敏范围回归（输出合规侧）。
//
// 用户报告 `export https_proxy=http://[REDACTED]:7890` 被网关改坏。在
// security/sanitize 的 DetectionScope 修完代码侧之后，245 上复测仍然被遮，
// 因为还有两条**互相独立**的地址遮蔽链路：
//
//	1. pii_patterns.ip_address（DB 播种，316 迁移）—— 由迁移 851 停用
//	2. detectInternalIP（本文件的宿主）—— 代码默认 ActionOnInternalIP
//
// 第 2 条本文件固化。契约与 sanitize 侧同构，但方向不同：
//
//   - detectInternalIP **仍然照常检出**。合规统计、复核队列、审计行全部不变，
//     运维在台账里依然看得到「模型输出里出现了内网地址」这件事。
//   - 只是**不再改写客户端可见文本**。默认动作 redact -> log。
//
// 为什么保留检出、只撤遮蔽：真正该对地址采取行动的是「模型 authored 的
// tool 参数指向一个可路由的外传目的地」，那件事由 security/sanitize 的
// ScopeEgress 守卫 fail-closed 地拦着（audit 154 / r1008）。把地址当 PII
// 遮蔽既拦不住外传，又会毁掉每一条可复制执行的运维命令。
//
// 只测「默认不遮蔽」是不够的：必须同时钉住「仍然检出」与「仍然不阻断」，
// 否则下一次有人把 detectInternalIP 整个删掉，这三条一起变绿。

import (
	"context"
	"testing"
)

// TestInternalIPIsDetectedButNotRedactedByDefault 固化默认动作。
func TestInternalIPIsDetectedButNotRedactedByDefault(t *testing.T) {
	c := NewBuiltinChecker()
	policy := defaultPolicy("")

	if policy.ActionOnInternalIP == "redact" {
		t.Fatal("default policy must not redact internal IPs")
	}

	const output = "connect to 10.24.7.91:5432 and host 192.168.2.3"
	issues := c.detectInternalIP(output, policy)
	if len(issues) != 2 {
		t.Fatalf("detection must still fire for both RFC1918 addresses, got %d: %+v", len(issues), issues)
	}
	for _, issue := range issues {
		if issue.Type != "internal_ip" {
			t.Errorf("issue type = %q, want internal_ip", issue.Type)
		}
		// Detection is unchanged: the hit is still recorded for the audit trail.
		if issue.Redacted {
			t.Errorf("issue marked Redacted under the default policy: %+v", issue)
		}
	}

	got := c.redactOutput(context.Background(), output, issues, policy)
	if got != output {
		t.Errorf("internal IP text was rewritten\n got: %s\nwant: %s", got, output)
	}
}

// TestInternalIPRedactionStillWorksWhenOperatorOptsIn 保留回滚路径：运维把
// action_on_internal_ip 设回 redact 时，遮蔽必须照旧生效，否则这次收窄就成了
// 单向门。
func TestInternalIPRedactionStillWorksWhenOperatorOptsIn(t *testing.T) {
	c := NewBuiltinChecker()
	policy := defaultPolicy("")
	policy.ActionOnInternalIP = "redact"

	const output = "connect to 10.24.7.91:5432"
	issues := c.detectInternalIP(output, policy)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	got := c.redactOutput(context.Background(), output, issues, policy)
	want := "connect to " + RedactionMarker("internal_ip") + ":5432"
	if got != want {
		t.Errorf("opt-in redaction = %q, want %q", got, want)
	}
}

// TestSecretsStillRedactedByDefault 是这次收窄的对照项：地址退出遮蔽范围，
// 凭据不能跟着退。detection 与 redaction 都必须不变。
func TestSecretsStillRedactedByDefault(t *testing.T) {
	c := NewBuiltinChecker()
	policy := defaultPolicy("")

	const output = "key is sk-abcd1234abcd1234 at 10.24.7.91"
	issues := c.detectSecrets(output, policy)
	if len(issues) != 1 {
		t.Fatalf("expected 1 secret issue, got %d: %+v", len(issues), issues)
	}
	got := c.redactOutput(context.Background(), output, issues, policy)
	want := "key is " + RedactionMarker("api_key") + " at 10.24.7.91"
	if got != want {
		t.Errorf("secret redaction = %q, want %q", got, want)
	}
}

// TestPIIPatternsStillRedactRealPII 覆盖 pii_patterns 那条链路：停用
// ip_address 一行不得牵连同表其余规则。
func TestPIIPatternsStillRedactRealPII(t *testing.T) {
	c := NewBuiltinChecker()
	policy := defaultPolicy("")

	issues := c.detectPII("邮箱 test@example.com 手机 13800138000", policy)
	if len(issues) != 2 {
		t.Fatalf("expected email + phone issues, got %d: %+v", len(issues), issues)
	}
	for _, issue := range issues {
		if !issue.Redacted {
			t.Errorf("PII must still be redacted by default: %+v", issue)
		}
	}
}
