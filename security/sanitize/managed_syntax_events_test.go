package sanitize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestManagedSedExemptionIsAuditedWithoutHidingStrictRules(t *testing.T) {
	for _, phase := range []string{"input", "egress"} {
		t.Run(phase, func(t *testing.T) {
			d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
			if err != nil {
				t.Fatal(err)
			}
			m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), d.RuleConfig(), sanitizerules.Options{})
			if err != nil {
				t.Fatal(err)
			}
			d.SetRuleManager(m)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { m.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			if err = m.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			request := WithAuthenticatedTenant(ctx, "tenant-a")
			const script = `sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env`
			scope := ScopeInputDeclarations
			id := "secret.password"
			if phase == "egress" {
				scope = ScopeEgress
				id = "secret.password.assignment"
				request = WithRuleField(request, "Bash", "command")
			}
			got, err := d.DetectScope(request, script, scope)
			if err != nil || len(got) != 0 {
				t.Fatal("valid redaction syntax changed", got, err)
			}
			deadline := time.Now().Add(2 * time.Second)
			found := false
			for time.Now().Before(deadline) {
				events, _ := m.Store().ListEvents(ctx, sanitizerules.EventFilter{TenantID: "tenant-a", Action: "exempted"})
				encoded, _ := json.Marshal(events)
				if strings.Contains(string(encoded), "PASSWORD") || strings.Contains(string(encoded), "sed ") {
					t.Fatal("raw syntax persisted")
				}
				for _, e := range events {
					if e.RuleID == id && e.Version == 1 && e.Phase == phase && e.Type == "secret" {
						found = true
					}
				}
				if found {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !found {
				t.Fatal("redaction exemption was invisible to audit")
			}
			// A second, explicitly strict pattern must still win over syntax handling.
			cfg := m.Current().Config()
			cfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "strict", Name: "Strict", Type: "secret", Kind: "pattern", Pattern: "PASSWORD", Scopes: []string{"input"}, Action: "block", Enabled: true})
			if _, err = m.Publish(ctx, 1, cfg, "test", "strict"); err != nil {
				t.Fatal(err)
			}
			if _, err = d.DetectScope(request, script, ScopeInputDeclarations); !errors.Is(err, ErrRuleBlocked) {
				t.Fatal("syntax swallowed strict block", err)
			}
			fragments, err := d.DetectScope(request, `sed 's/PASSWORD=.*/ActualValue987!/' .env`, ScopeRedact)
			if err != nil || len(fragments) == 0 {
				t.Fatal("real replacement became exempt", fragments, err)
			}
		})
	}
}
