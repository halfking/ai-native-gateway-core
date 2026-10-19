package outputcompliance

// r1038_address_scope_test.go — 2026-10-20 输出合规链的地址族范围门禁。
//
// 用户口径（2026-10-20）：「脱敏的范围应该不包含域名、地址、ip这些内容，
// 这些不是足够敏感」。
//
// 这条链路的规则来自 DB 的 `pii_patterns`（316 播种），其中 `ip_address` 一行
// 的正则是 `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`——连版本号与环回地址一并
// 命中，命中后整段被换成遮蔽物，于是
// 「export https_proxy=http://127.0.0.1:7890」这类可复制执行的命令变成坏命令。
//
// 851 迁移停用的是**DB 行**（数据修复），不是代码：运维随时可以把它改回
// enabled，而链路上没有任何代码级保证。本文件钉住那道保证。
//
// 口径与 2026-10-10 的 internal_ip 一致：**只撤遮蔽、不撤检出**——
// issue 照记（合规统计 / 复核队列 / 审计行不变），只是不再改写客户端可见文本。

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// r1038AddressChecker 复刻「DB 里的 pii_patterns.ip_address 仍然开着」这一形态。
//
// 它不是假设：851 之前每台全新安装的机器上这条规则确实还在跑，运维也可以随时
// 把它改回 enabled。代码必须对此免疫。
func r1038AddressChecker() *Checker {
	c := NewBuiltinChecker()
	c.piiPatterns = append(c.piiPatterns, &PIIPattern{
		PatternName:  "ip_address",
		PatternType:  "ip",
		RegexPattern: `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`,
		Enabled:      true,
		Severity:     5,
		regex:        regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`),
	})
	return c
}

// TestR1038AddressFamilyIsNotRedacted 地址族不进遮蔽面。
//
// 判据取**模型自造**的地址（不是调用方原值）——这样测的是**范围**决定，
// 而不是 r1013 的**来源**豁免。
func TestR1038AddressFamilyIsNotRedacted(t *testing.T) {
	for _, text := range []string{
		"connect to 203.0.113.8 first",
		"export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890",
		"数据库在 10.20.30.40:5432",
	} {
		result, err := r1038AddressChecker().Check(context.Background(), "tenant-a", text)
		if err != nil {
			t.Fatalf("Check(%q): %v", text, err)
		}
		if result.RedactedOutput != text {
			t.Errorf("地址族被改写了\n in: %s\nout: %s", text, result.RedactedOutput)
		}
	}
}

// TestR1038AddressFamilyIsStillDetected 钉住「只撤遮蔽、不撤检出」。
//
// 这条与上一条成组存在：只测「没被遮蔽」，下一次有人把 issue 一起跳过，
// 测试仍会全绿，而审计面已经空了。
func TestR1038AddressFamilyIsStillDetected(t *testing.T) {
	result, err := r1038AddressChecker().Check(context.Background(), "tenant-a", "connect to 203.0.113.8")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(result.Issues) == 0 {
		t.Fatal("地址族的检出被一起丢掉了：审计面被掏空")
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Subtype == "ip" {
			found = true
			if issue.Redacted {
				t.Error("地址族 issue 仍标着 Redacted：遮蔽面没有被真正撤掉")
			}
		}
	}
	if !found {
		t.Errorf("没有记录 ip 类型的 issue: %+v", result.Issues)
	}
}

// TestR1038NonAddressPIIStillRedacted 范围收窄不能把真正的 PII 一起放过去。
func TestR1038NonAddressPIIStillRedacted(t *testing.T) {
	for _, tc := range []struct{ text, secret string }{
		{"邮箱 test@example.com", "test@example.com"},
		{"手机 13800138000", "13800138000"},
		{"身份证 110101199003078515", "110101199003078515"},
	} {
		result, err := r1038AddressChecker().Check(context.Background(), "tenant-a", tc.text)
		if err != nil {
			t.Fatalf("Check(%q): %v", tc.text, err)
		}
		if strings.Contains(result.RedactedOutput, tc.secret) {
			t.Errorf("PII 漏出: %q -> %q", tc.text, result.RedactedOutput)
		}
		if result.RedactedOutput == tc.text {
			t.Errorf("PII 完全未被遮蔽: %q", tc.text)
		}
	}
}

// TestR1038BaselineShapeMarksAddressRulesAsLog 钉住**基线形状**。
//
// 这是「数据修复失效时唯一的兜底」：legacy 导入（cmd/gateway）与内置
// Checker.RuleConfig 都必须把地址族做成 log（只记录）而不是 mask。
// 形状错了，851 那一行被人改回 enabled 时遮蔽就会回来。
func TestR1038BaselineShapeMarksAddressRulesAsLog(t *testing.T) {
	cfg := NewBuiltinChecker().RuleConfig()
	seen := false
	for _, rule := range cfg.Rules {
		if rule.Type != "server_ip" && rule.Type != "internal_ip" {
			continue
		}
		seen = true
		if rule.Action != "log" {
			t.Errorf("地址族基线规则 %q 的 action = %q，应为 log（只记录不遮蔽）", rule.ID, rule.Action)
		}
	}
	if !seen {
		t.Fatal("基线里没有地址族规则：判据已失去意义（形状可能变了）")
	}
}
