package outputcompliance

// r1040_address_family_canonical_parity_test.go — 2026-10-28：
// 「声明类型 → 托管规则类型」这条规范化**必须只有一份实现**。
//
// ## 背景（r1040 普查的发现）
//
// r1038b（2026-10-27）把「地址族 = 域名 / 地址 / ip」这条口径收进了
// `domains/sanitizerules.IsAddressFamilyType`，并把**已知的两个面**改成调它：
//
//  1. `security/sanitize/detector.go` 的 `scopedType`（脱敏面豁免）；
//  2. `domains/outputcompliance/checker.go` 的 `isAddressPIIType`（合规面只检出）。
//
// ★ 但「口径收进 SSOT」只保证**已知的判定点**调同一个函数，**不保证下一个**。
// r1040 全仓普查（`cmd/tools/address_family_decision_audit`）发现**还有第三个面**：
// `domains/outputcompliance/managed_rules.go` 的 `Checker.RuleConfig()`，
// 它持有一份**自己的类型映射副本**，而且**已经漂移**：
//
//	声明 domain / hostname / address
//	  → legacy 导入路径（sanitizerules.LegacyPIIRule）：(server_ip, log)  只记录
//	  → 合规面导出路径（Checker.RuleConfig）：           (custom, mask)  照遮蔽
//
// 同一个**声明类型**（运维写的那一列 `pii_patterns.pattern_type`）在两条路径上
// 得到**相反待遇** —— 这正是 r1038b 的 D1（两份名单漂移）在新位置上的复发。
//
// ⇒ 修法不是「把 domain 也补进第二份 switch」（那还是两份），而是**删掉第二份**：
// 两边都调 `sanitizerules.LegacyPIIRule`（同一张 `pii_patterns` 表的同一种行）。
//
// ## 这道门钉的是什么
//
// 1. **行为对拍**：同一批**声明类型**，两条路径必须给出**同样的 (type, action)**。
//    ⇒ 任何一处再抄一份映射、或只改一边，门立刻红。
// 2. **口径对拍**：声明为地址族的类型，导出的规则**必须只记录不遮蔽**
//    （action=log）⇒ 「脱敏范围不含域名/地址/ip」这条用户口径在**两个面**都成立。
// 3. **反向对照**：`url` / `custom` 这类**能携带凭据**的类型**必须照遮**
//    （action=mask）⇒ 豁免名单不许被"顺手"放宽成安全后门。
//
// ★ 变异验证：
//   M1 把 `Checker.RuleConfig()` 改回本地 switch（domain → custom）⇒ 判据 1 红；
//   M2 把豁免放宽到 url ⇒ 判据 3 红。

import (
	"regexp"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1040DeclaredTypes 是本门遍历的**声明类型**集合。
//
// ★ 它同时被审计工具当成「自己写了一份类型名单」量到（type-list 面）——
// 那是**故意**的：反向断言清单必须**独立复述**一遍名单，SSOT 少一个类型时
// 门才会红。（清单登记见 cmd/tools/address_family_decision_audit/manifest_test.go。）
var r1040DeclaredTypes = []string{
	"ip", "ip_address", "ipv6",
	"domain", "hostname", "address",
	"server_ip", "internal_ip",
	"url", "custom", "secret", "account", "email", "phone", "credit_card", "id_card",
}

// r1040CheckerExport 走**合规面导出**那条路（Checker.RuleConfig）。
func r1040CheckerExport(t *testing.T, declared string) sanitizerules.Rule {
	t.Helper()
	c := &Checker{piiPatterns: []*PIIPattern{{
		PatternName: declared, PatternType: declared,
		RegexPattern: `x`, Enabled: true, regex: regexp.MustCompile(`x`),
	}}}
	for _, r := range c.RuleConfig().Rules {
		if r.Name == declared {
			return r
		}
	}
	t.Fatalf("合规面导出里找不到声明类型 %q 的规则", declared)
	return sanitizerules.Rule{}
}

// TestR1040ManagedExportAndLegacyImportAgreeOnEveryDeclaredType 是行为对拍。
//
// ★ 前提：两条路径读的是**同一张表**（`pii_patterns`）的同一种行 ⇒ 规范化必须同源。
// 一旦有一侧另抄一份映射，这里立刻红（domain / hostname / address 三条先红）。
func TestR1040ManagedExportAndLegacyImportAgreeOnEveryDeclaredType(t *testing.T) {
	for _, declared := range r1040DeclaredTypes {
		got := r1040CheckerExport(t, declared)
		want := sanitizerules.LegacyPIIRule(1, declared, declared, `x`, true)
		if got.Type != want.Type {
			t.Errorf("声明类型 %q：合规面导出映射成 %q，legacy 导入映射成 %q ⇒ 两份映射已漂移", declared, got.Type, want.Type)
		}
		if got.Action != want.Action {
			t.Errorf("声明类型 %q：合规面导出动作 %q，legacy 导入动作 %q ⇒ 同一个声明类型在两个面待遇相反", declared, got.Action, want.Action)
		}
	}
}

// TestR1040DeclaredAddressFamilyIsNeverMasked 钉住用户口径：
// 「脱敏的范围应该不包含域名、地址、ip」⇒ 地址族**只记录、不遮蔽**。
func TestR1040DeclaredAddressFamilyIsNeverMasked(t *testing.T) {
	for _, declared := range []string{"ip", "ip_address", "ipv6", "domain", "hostname", "address", "server_ip", "internal_ip"} {
		got := r1040CheckerExport(t, declared)
		if got.Action != "log" {
			t.Errorf("声明类型 %q 导出成 action=%q ⇒ 地址族被遮蔽（与用户口径相违；口径 SSOT = %v）", declared, got.Action, sanitizerules.AddressFamilyTypes())
		}
	}
}

// TestR1040CredentialBearingTypesStayMasked 是反向对照：
// URL 能携带凭据（?token=…），整类豁免等于开后门 ⇒ 必须照遮。
func TestR1040CredentialBearingTypesStayMasked(t *testing.T) {
	for _, declared := range []string{"url", "custom", "secret", "account", "email", "phone", "credit_card", "id_card"} {
		got := r1040CheckerExport(t, declared)
		if got.Action != "mask" {
			t.Errorf("声明类型 %q 导出成 action=%q ⇒ 凭据类不再被遮蔽（安全后门）", declared, got.Action)
		}
	}
}
