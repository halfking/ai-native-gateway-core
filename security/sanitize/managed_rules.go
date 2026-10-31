package sanitize

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

var ErrRuleBlocked = errors.New("request blocked by sensitive information policy")

type ruleFieldKey struct{}
type managedTrustedKey struct{}
type ruleField struct{ tool, field string }

// WithRuleField supplies authenticated protocol metadata, never raw request headers.
func WithRuleField(ctx context.Context, tool, field string) context.Context {
	return context.WithValue(ctx, ruleFieldKey{}, ruleField{tool, strings.ToLower(field)})
}
func (d *PatternDetector) SetRuleManager(m *sanitizerules.Manager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rules = m
}
func (d *PatternDetector) RuleManager() *sanitizerules.Manager {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.rules
}
func phaseFor(scope DetectionScope) string {
	switch scope {
	case ScopeInputDeclarations:
		return "input"
	case ScopeEgress:
		return "egress"
	default:
		return "output"
	}
}

// RuleConfig imports the validated local baseline once. Semantic declarations are
// separate from structured output assignments so editing prose cannot widen tools.
// builtinRuleDescriptionText 取内建规则的说明（事实源 = 内建规则目录）。
//
// ★ 2026-10-29 输出合规误报轮：★ 内建规则（builtin.*）的说明早就写在
// sanitizerules 的内建目录里，★ 但规则模型里没有「说明」字段 ⇒ ★★ 说明在
// 装载时丢掉 ⇒ ★★ 管理面显示不出来 ⇒ ★★ 「规则太简单，看不懂它在判什么」。
func builtinRuleDescriptionText(ruleID string) string {
	if desc, ok := sanitizerules.RuleDescription(ruleID); ok {
		return desc
	}
	return ""
}

func (d *PatternDetector) RuleConfig() sanitizerules.Config {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cfg := sanitizerules.Config{}
	for i, p := range d.patterns {
		// ★ 登记规则说明（2026-10-29 输出合规误报轮）：★ 说明的事实源是 YAML ⇒
		// ★ 装载时登记进规则说明目录 ⇒ ★★ 管理面「读规则」时能补上说明
		// （★ 历史规则快照里没有说明 ⇒ 不补的话管理面永远只显示一个规则 ID）。
		if p.description != "" {
			sanitizerules.RegisterRuleDescription(p.id, p.description)
		}
		id := p.id
		if id == "" {
			id = "builtin." + string(p.sType) + "." + strconv.Itoa(i)
		}
		kind := p.kind
		if kind == "" {
			kind = "pattern"
		}
		if p.vendorKeyShape {
			kind = "vendor_key_shape"
		}
		r := sanitizerules.Rule{ID: id, Name: id, Type: string(p.sType), Kind: kind, Pattern: p.regex.String(), Group: p.group, Action: "mask", Enabled: true, Scopes: []string{"input", "output"}, Description: p.description}
		if p.action != "" {
			r.Action = p.action
		}
		switch kind {
		case "credential_declaration", "account_declaration", "credential_pair":
			r.Scopes = []string{"input"}
			cfg.Rules = append(cfg.Rules, r)
			if kind == "credential_pair" {
				continue
			}
			r.ID += ".assignment"
			r.Name = r.ID
			r.Scopes = []string{"output", "egress"}
			r.Action = "block"
			if kind == "credential_declaration" {
				r.Kind = "credential_assignment"
				r.Pattern = structuredPassword.String()
			} else {
				r.Kind = "account_assignment"
				r.Pattern = structuredAccount.String()
				r.Action = "log"
				r.Scopes = []string{"output"}
			}
			// Output assignments mask while tool assignments block.
			out := r
			out.ID += ".output"
			out.Name = out.ID
			out.Scopes = []string{"output"}
			out.Action = "mask"
			cfg.Rules = append(cfg.Rules, out)
			r.Scopes = []string{"egress"}
			if r.Kind == "account_assignment" {
				continue
			}
		default:
			if scopedType(p.sType) {
				r.Scopes = []string{"egress"}
				r.Action = "block"
			} else {
				e := r
				e.ID += ".egress"
				e.Name = e.ID
				e.Scopes = []string{"egress"}
				e.Action = "block"
				cfg.Rules = append(cfg.Rules, e)
			}
		}
		if len(p.scopes) > 0 {
			r.Scopes = nil
			for _, s := range p.scopes {
				r.Scopes = append(r.Scopes, phaseFor(s))
			}
		}
		cfg.Rules = append(cfg.Rules, r)
	}

	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		for _, phase := range r.Scopes {
			if phase == "egress" {
				r.Exemptions = []string{"inert_tool", "nonroutable_destination", "identity"}
			}
		}
	}
	cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "builtin.network_destination", Name: "公网网络目的地", Type: "custom", Kind: "network_destination", Pattern: `(?s).+`, Scopes: []string{"egress"}, Action: "block", Enabled: true, Exemptions: []string{"inert_tool"}, Description: builtinRuleDescriptionText("builtin.network_destination")},
		sanitizerules.Rule{ID: "builtin.review_combination", Name: "特殊帐号组合待复核（仅记录）", Type: "custom", Kind: "review_combination", Pattern: `(?i)(?:管理员|账号|帐号|账户|用户名|credentials|account)[^\r\n]{0,32}(?:[:=：]|改成|改为|设为|是|为)[^\r\n]{1,120}[/／|｜:][^\r\n]{1,120}`, Scopes: []string{"input"}, Action: "log", Enabled: true, Description: "识别「账号/凭据 + 赋值 + 看起来像凭据的值」这种组合形态：只记录（log），不遮蔽、不阻断，等人工判断是不是真的在传凭据。"})
	for _, spec := range []struct {
		id, typ, phase, action string
		fields                 []string
	}{
		{"builtin.secret_field.input", "secret", "input", "mask", []string{"password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "access_key", "authorization", "密码", "口令"}},
		{"builtin.secret_field.egress", "secret", "egress", "block", []string{"password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "access_key", "authorization", "密码", "口令"}},
		{"builtin.account_field.input", "account", "input", "mask", []string{"account", "username", "user_name", "login", "帐号", "账号", "账户", "用户名"}},
	} {
		cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: spec.id, Name: spec.id, Type: spec.typ, Kind: "pattern", Pattern: `(?s).+`, Scopes: []string{spec.phase}, Action: spec.action, Enabled: true, Fields: spec.fields, Description: builtinRuleDescriptionText(spec.id)})
	}
	return cfg
}
func (d *PatternDetector) managedPatterns(ctx context.Context, scope DetectionScope) ([]patternEntry, *sanitizerules.Snapshot) {
	m := d.RuleManager()
	if m == nil {
		return nil, nil
	}
	snapshot := m.SnapshotFor(ctx)
	meta, _ := ctx.Value(ruleFieldKey{}).(ruleField)
	if shared := outputcompliance.RuleFieldFromContext(ctx); shared.Field != "" || shared.Tool != "" {
		meta = ruleField{shared.Tool, shared.Field}
	}
	tenant := authenticatedTenant(ctx)
	phase := phaseFor(scope)
	patterns := make([]patternEntry, 0, len(snapshot.Rules))
	for _, r := range snapshot.Rules {
		if !r.Applies(phase, tenant, meta.tool, meta.field) {
			continue
		}
		// r1037（2026-10-20，211）：action **不改写**。是否「终止回合」由硬阻断
		// 清单在**决策点**判定（detector 的 fail-closed 分支 / egress 片段闸），
		// 而不是把规则改成 mask —— 改 action 会连带改变凭据重叠判定
		// （`overlap && p.action != "block"`）与其它以 action 为条件的分支。
		patterns = append(patterns, patternEntry{sType: SensitiveType(r.Type), regex: r.Regex, group: r.Group, id: r.ID, kind: r.Kind, action: r.Action, vendorKeyShape: r.Kind == "vendor_key_shape", exemptions: r.Exemptions, description: r.Description})
	}
	return patterns, snapshot
}
func (d *PatternDetector) observeRule(ctx context.Context, id string, version int64, scope DetectionScope, action string) {
	if m := d.RuleManager(); m != nil && id != "" {
		m.Record(ctx, sanitizerules.Event{TenantID: authenticatedTenant(ctx), RuleID: id, Version: version, Phase: phaseFor(scope), Action: action, Type: ruleType(m.SnapshotFor(ctx), id), Field: ruleKind(m.SnapshotFor(ctx), id, ctx)})
	}
}
func (d *PatternDetector) observeFragment(ctx context.Context, f SensitiveFragment, action string) {
	d.observeRule(ctx, f.RuleID, f.Version, ScopeEgress, action)
}
func ruleBlockError(id string) error { return fmt.Errorf("%w (rule=%s)", ErrRuleBlocked, id) }

// CheckManagedOutput adapts the same detector for post-restoration and write-time
// compliance. Trusted values are excluded by the existing interceptor upstream.
func (d *PatternDetector) CheckManagedOutput(ctx context.Context, tenant, text string) (*outputcompliance.ComplianceResult, error) {
	ctx = WithAuthenticatedTenant(ctx, tenant)
	ctx = context.WithValue(ctx, managedTrustedKey{}, true)
	s, err := NewSanitizer(d)
	if err != nil {
		return nil, err
	}
	return (&outputSensitiveChecker{sanitizer: s, action: OutputMask}).Check(ctx, tenant, text)
}

func ruleType(s *sanitizerules.Snapshot, id string) string {
	for _, r := range s.Rules {
		if r.ID == id {
			return r.Type
		}
	}
	return "custom"
}
func ruleKind(s *sanitizerules.Snapshot, id string, ctx context.Context) string {
	if f, ok := ctx.Value(ruleFieldKey{}).(ruleField); ok && f.field != "" {
		return f.field
	}
	for _, r := range s.Rules {
		if r.ID == id {
			return r.Kind
		}
	}
	return "pattern"
}
func (c *outputSensitiveChecker) managed() bool {
	d, ok := c.sanitizer.detector.(*PatternDetector)
	return ok && d.RuleManager() != nil
}

func (d *PatternDetector) observeCombination(ctx context.Context, id string, version int64, value string) {
	shape := "pair_other"
	switch {
	case strings.ContainsAny(value, "/／"):
		shape = "pair_slash"
	case strings.ContainsAny(value, "|｜"):
		shape = "pair_pipe"
	case strings.Contains(value, ":"):
		shape = "pair_colon"
	}
	if m := d.RuleManager(); m != nil {
		m.Record(ctx, sanitizerules.Event{TenantID: authenticatedTenant(ctx), RuleID: id, Version: version, Phase: "input", Action: "pending", Type: "custom", Field: shape})
	}
}
