package outputcompliance

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ManagedRuleCheck is resolved at request time because both checkers are built
// before the shared sanitization manager during gateway startup. Nil restores
// legacy behavior only when version management has not been installed.
type ManagedRuleCheck func(context.Context, string, string) (*ComplianceResult, error)

func (c *Checker) SetManagedRuleSource(source func() ManagedRuleCheck) { c.managedRuleSource = source }

// RuleConfig exports the old output-only PII rules into the first immutable
// snapshot. Existing disabled DB rows stay disabled; later edits use versions.
//
// r1040（2026-10-28）：类型映射**不再在本包另抄一份**。
//
// 此前这里有一份自己的 switch（bank_card→credit_card、ip/ip_address/ipv6→server_ip、
// 已知类型原样、未知→custom），与 `domains/sanitizerules.LegacyPIIRule` 那份
// **已经漂移**：r1038b 给 SSOT 那份补了 `domain` / `hostname` / `address`
// → `server_ip`，**本包这份没补** ⇒ 同一个**声明类型**在两条路径上得到**相反待遇**：
//
//	声明 domain  →  legacy 导入路径：(server_ip, log)  只记录不遮蔽
//	             →  本包的导出路径：(custom, mask)  照遮蔽
//
// 而「声明类型」正是运维写的那一列（`pii_patterns.pattern_type`）⇒ 同一行 DB
// 规则，走哪条路决定它遮不遮域名。
//
// ⇒ 修法不是「把 domain 也补进本包这份 switch」（那还是两份），而是
// **两个面都调同一个函数**：本包的行与 legacy 导入的行本来就是**同一张表**
// （`pii_patterns`）的同一种行 ⇒ 用同一个构造器。
//
// ⇒ r1040 另建一道门（`cmd/tools/address_family_decision_audit`）：全仓
// 「谁在判断某类型算不算地址族」被普查 + 登记，**每个判定面要么调 SSOT、
// 要么在清单里写明为什么可以自己写**。
func (c *Checker) RuleConfig() sanitizerules.Config {
	cfg := sanitizerules.Config{}
	for index, p := range c.piiPatterns {
		id := p.ID
		if id == 0 {
			id = index + 1
		}
		// 同一行 DB 规则 ⇒ 同一个构造器（SSOT）。
		// 地址族在这里同样只记录、不遮蔽（action 由 SSOT 判定）。
		cfg.Rules = append(cfg.Rules, sanitizerules.LegacyPIIRule(id, p.PatternName, p.PatternType, p.RegexPattern, p.Enabled))
	}
	// Other secret shapes already exist in the YAML baseline. Keep internal IP
	// telemetry as an explicit log rule rather than silently redacting addresses.
	cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "compliance.internal_ip", Name: "Internal IP telemetry", Type: "internal_ip", Kind: "pattern", Pattern: `\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3})\b`, Scopes: []string{"output"}, Action: "log", Enabled: true, Description: "记录响应里出现的内网地址形态（10.x / 172.16-31.x / 192.168.x）。只记录、不遮蔽、不阻断：内网地址本身不是机密，遮蔽它会破坏运维排查。"})
	return cfg
}

type ruleFieldContextKey struct{}
type RuleField struct{ Tool, Field string }

func WithRuleField(ctx context.Context, tool, field string) context.Context {
	return context.WithValue(ctx, ruleFieldContextKey{}, RuleField{tool, field})
}
func RuleFieldFromContext(ctx context.Context) RuleField {
	v, _ := ctx.Value(ruleFieldContextKey{}).(RuleField)
	return v
}
