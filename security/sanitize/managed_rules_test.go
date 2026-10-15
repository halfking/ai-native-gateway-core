package sanitize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func managedFixture(t *testing.T) (*PatternDetector, *sanitizerules.Manager) {
	t.Helper()
	d := NewPatternDetector()
	cfg := d.RuleConfig()
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), cfg, sanitizerules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d.SetRuleManager(m)
	if err = m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d, m
}
func TestManagedRulesActionsConstraintsAndPinnedRequest(t *testing.T) {
	d, m := managedFixture(t)
	ctx := m.Pin(WithAuthenticatedTenant(context.Background(), "tenant-a"))
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "custom", Name: "Custom", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"input", "output"}, Action: "log", Enabled: true, TenantID: "tenant-a"}}}
	if _, err := m.Publish(context.Background(), 1, cfg, "tester", "log"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.DetectScope(WithAuthenticatedTenant(context.Background(), "tenant-a"), "VALUE-1234", ScopeInputDeclarations); len(got) != 0 {
		t.Fatal("log rule masked text")
	}
	cfg.Rules[0].Action = "block"
	if _, err := m.Publish(context.Background(), 2, cfg, "tester", "block"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DetectScope(WithAuthenticatedTenant(context.Background(), "tenant-a"), "VALUE-1234", ScopeInputDeclarations); !errors.Is(err, ErrRuleBlocked) {
		t.Fatalf("block ignored: %v", err)
	}
	if _, err := d.DetectScope(ctx, "VALUE-1234", ScopeInputDeclarations); err != nil {
		t.Fatal("inflight snapshot changed")
	}
	if _, err := d.DetectScope(WithAuthenticatedTenant(context.Background(), "tenant-b"), "VALUE-1234", ScopeInputDeclarations); err != nil {
		t.Fatal("tenant rule leaked")
	}
	if err := d.ReloadFromFile(); err == nil {
		t.Fatal("local reload bypassed version manager")
	}
}
func TestManagedBaselineKeepsNaturalDeclarationsInputOnly(t *testing.T) {
	d, _ := managedFixture(t)
	if got, err := d.DetectScope(context.Background(), "密码改为 Test9987#", ScopeInputDeclarations); err != nil || len(got) == 0 {
		t.Fatalf("input declaration lost %v", err)
	}
	if got, err := d.DetectScope(context.Background(), "密码改为 Test9987#", ScopeEgress); err != nil || len(got) != 0 {
		t.Fatalf("natural declaration widened egress: %v %v", got, err)
	}
}

func TestManagedRuleDisablingStopsCredentialFieldChain(t *testing.T) {
	d, m := managedFixture(t)
	s, _ := NewSanitizer(d)
	guard := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	result, err := guard.CheckField(ctx, "a", "__tool_json@Write", `{"password":"R7#kQ?92!"}`)
	if err != nil || !result.Blocked {
		t.Fatal("baseline credential guard missing", result, err)
	}
	cfg := m.Current().Config()
	for i := range cfg.Rules {
		if cfg.Rules[i].ID == "builtin.secret_field.egress" {
			cfg.Rules[i].Enabled = false
		}
	}
	if _, err := m.Publish(ctx, 1, cfg, "tester", "disable field"); err != nil {
		t.Fatal(err)
	}
	result, err = guard.CheckField(ctx, "a", "__tool_json@Write", `{"password":"R7#kQ?92!"}`)
	if err != nil || result.Blocked {
		t.Fatal("disabled rule still enforced by old chain", result, err)
	}
}
func TestManagedToolFieldInputConstraints(t *testing.T) {
	d, m := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "tool", Name: "Tool", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true, Tools: []string{"Bash"}, Fields: []string{"command"}}}}
	if _, err := m.Publish(ctx, 1, cfg, "tester", "scoped"); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(d)
	worker := &requestInputSanitizer{ctx: ctx, sanitizer: s, offset: map[SensitiveType]int{}, mapping: SanitizeMap{}, usedCount: map[string]int{}}
	raw := []byte(`[{"function":{"name":"Bash","arguments":"{\"command\":\"VALUE-1234\",\"content\":\"VALUE-5678\"}"}}]`)
	out, changed, err := worker.sanitizeToolCalls(raw)
	if err != nil || !changed || strings.Contains(string(out), "VALUE-1234") || !strings.Contains(string(out), "VALUE-5678") {
		t.Fatalf("scope not honored: %s %v", out, err)
	}
}

func TestManagedOutputTrustAndExplicitScopeActions(t *testing.T) {
	d, m := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "explicit", Name: "Explicit", Type: "account", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"output", "egress"}, Action: "block", Enabled: true, Tools: []string{"Write"}, Fields: []string{"content"}}}}
	if _, err := m.Publish(ctx, 1, cfg, "tester", "explicit block"); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(d)
	guard := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
	result, err := guard.CheckField(ctx, "a", "__tool_json@Write", `{"content":"VALUE-1234"}`)
	if err != nil || !result.Blocked {
		t.Fatal("inert/identity exemption bypassed explicit block", result, err)
	}
	c := outputcompliance.NewBuiltinChecker()
	c.SetManagedRuleSource(func() outputcompliance.ManagedRuleCheck { return d.CheckManagedOutput })
	result, err = c.CheckField(ctx, "a", "__tool_json@Write", `{"content":"VALUE-1234"}`)
	if err != nil || !result.Blocked {
		t.Fatal("output-only scope lost tool metadata", result, err)
	}
	trusted := outputcompliance.WithRestoredValues(ctx, outputcompliance.NewRestoredValues([]string{"VALUE-1234"}))
	trusted = outputcompliance.WithRuleField(trusted, "Write", "content")
	result, err = d.CheckManagedOutput(trusted, "a", "VALUE-1234")
	if err != nil || result.Blocked || result.RedactedOutput != "VALUE-1234" {
		t.Fatal("trusted restoration destroyed", result, err)
	}
	// Mandatory guard never inherits the post-restoration trust exemption.
	result, err = guard.CheckField(trusted, "a", "__tool_json@Write", `{"content":"VALUE-1234"}`)
	if err != nil || !result.Blocked {
		t.Fatal("mandatory guard trusted unproven model content", result, err)
	}
}
func TestManagedTypeMustBeRestorable(t *testing.T) {
	d := NewPatternDetector()
	cfg := d.RuleConfig()
	cfg.Rules[0].Type = "Secret-1"
	if err := sanitizerules.Validate(cfg); err == nil {
		t.Fatal("unrestorable type accepted")
	}
}

func TestManagedOverlappingRulesKeepStrictBlock(t *testing.T) {
	d, m := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	weak := sanitizerules.Rule{ID: "weak", Name: "Weak", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"egress"}, Action: "block", Enabled: true, Priority: 100, Exemptions: []string{"inert_tool"}}
	strict := weak
	strict.ID = "strict"
	strict.Name = "Strict"
	strict.Priority = 0
	strict.Exemptions = nil
	if _, err := m.Publish(ctx, 1, sanitizerules.Config{Rules: []sanitizerules.Rule{weak, strict}}, "test", "overlap"); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(d)
	guard := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
	result, err := guard.CheckField(ctx, "a", "__tool_json@Write", `{"content":"VALUE-1234"}`)
	if err != nil || !result.Blocked {
		t.Fatal("weaker overlap swallowed block", result, err)
	}
}
func TestManagedMaskIgnoresLegacyGlobalBlock(t *testing.T) {
	d, m := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "mask", Name: "Mask", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"output"}, Action: "mask", Enabled: true}}}
	if _, err := m.Publish(ctx, 1, cfg, "test", "mask"); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(d)
	guard := &outputSensitiveChecker{sanitizer: s, action: OutputBlock}
	result, err := guard.Check(ctx, "a", "VALUE-1234")
	if err != nil || result.Blocked || result.RedactedOutput == "VALUE-1234" {
		t.Fatal("old environment overrode rule action", result, err)
	}
}

func TestManagedSpecialCombinationsQueueSafeStructuralEvidence(t *testing.T) {
	d, m := managedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	request := WithAuthenticatedTenant(ctx, "a")
	const raw = "R7#kQ?92!"
	for _, separator := range []string{"/", "|", ":"} {
		if _, err := d.DetectScope(request, "管理员账号改成ops"+separator+raw, ScopeInputDeclarations); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := m.Store().ListEvents(ctx, sanitizerules.EventFilter{Action: "pending"})
		if len(events) == 3 {
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), "ops") {
				t.Fatal("raw combination persisted")
			}
			fields := map[string]bool{}
			for _, e := range events {
				fields[e.Field] = true
			}
			if !fields["pair_slash"] || !fields["pair_pipe"] || !fields["pair_colon"] {
				t.Fatal("combination shapes collapsed", fields)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("special combination candidates not recorded")
}
