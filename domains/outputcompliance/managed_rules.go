package outputcompliance

import (
	"context"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ManagedRuleCheck is resolved at request time because both checkers are built
// before the shared sanitization manager during gateway startup. Nil restores
// legacy behavior only when version management has not been installed.
type ManagedRuleCheck func(context.Context, string, string) (*ComplianceResult, error)

func (c *Checker) SetManagedRuleSource(source func() ManagedRuleCheck) { c.managedRuleSource = source }

// RuleConfig exports the old output-only PII rules into the first immutable
// snapshot. Existing disabled DB rows stay disabled; later edits use versions.
func (c *Checker) RuleConfig() sanitizerules.Config {
	cfg := sanitizerules.Config{}
	for index, p := range c.piiPatterns {
		typ := p.PatternType
		switch typ {
		case "bank_card":
			typ = "credit_card"
		case "ip", "ip_address", "ipv6":
			typ = "server_ip"
		case "phone", "id_card", "email", "credit_card", "secret", "account", "name", "internal_ip", "server_ip", "custom":
		default:
			typ = "custom"
		}
		id := p.ID
		if id == 0 {
			id = index + 1
		}
		cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "compliance.pii." + strconv.Itoa(id), Name: p.PatternName, Type: typ, Kind: "pattern", Pattern: p.RegexPattern, Scopes: []string{"output"}, Action: "mask", Enabled: p.Enabled})
	}
	// Other secret shapes already exist in the YAML baseline. Keep internal IP
	// telemetry as an explicit log rule rather than silently redacting addresses.
	cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "compliance.internal_ip", Name: "Internal IP telemetry", Type: "internal_ip", Kind: "pattern", Pattern: `\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3})\b`, Scopes: []string{"output"}, Action: "log", Enabled: true})
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
