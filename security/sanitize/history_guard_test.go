package sanitize

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestHistoryGuardCompleteSourceAndReadFailure(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	guard := d.HistoryGuard(context.Background())
	for _, tc := range []struct {
		text string
		safe bool
	}{
		{"billing port 6432 in /srv/billing/db.yaml", true},
		{"prefix\n-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("A", 300_000) + "billing SYNTHETICPEMFRAGMENT\n-----END PRIVATE KEY-----", false},
		{`{"password":"` + strings.Repeat("x", 300_000) + `billing SYNTHETICQUOTED"}`, false},
		{`"数据库密码改为 synthetic-test-password"`, false},
	} {
		safe, err := guard(context.Background(), func() io.Reader { return strings.NewReader(tc.text) })
		if err != nil || safe != tc.safe {
			t.Fatalf("safe=%v expected=%v err=%v", safe, tc.safe, err)
		}
	}
	want := errors.New("archive read failed")
	if safe, err := guard(context.Background(), func() io.Reader { return historyFailReader{want} }); safe || !errors.Is(err, want) {
		t.Fatalf("read failure allowed: %v %v", safe, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if safe, err := guard(ctx, func() io.Reader { return strings.NewReader("safe") }); safe || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation allowed: %v %v", safe, err)
	}
}

type historyFailReader struct{ err error }

func (r historyFailReader) Read([]byte) (int, error) { return 0, r.err }

func TestHistoryGuardIncludesScopedManagedRulesAndPinsPolicy(t *testing.T) {
	d, m := managedFixture(t)
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "history", Name: "History", Type: "secret", Kind: "pattern", Pattern: `SYNTHETICVALUE[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true, TenantID: "a", Fields: []string{"arguments"}, Tools: []string{"Write"}}}}
	if _, err := m.Publish(context.Background(), 1, cfg, "test", "guard"); err != nil {
		t.Fatal(err)
	}
	ctx := m.Pin(WithAuthenticatedTenant(context.Background(), "a"))
	guard := d.HistoryGuard(ctx)
	open := func() io.Reader { return strings.NewReader(`{"arguments":"SYNTHETICVALUE42"}`) }
	if safe, err := guard(ctx, open); safe || err != nil {
		t.Fatalf("scoped mask lost: %v %v", safe, err)
	}
	other := WithAuthenticatedTenant(context.Background(), "b")
	if safe, err := d.HistoryGuard(other)(other, open); !safe || err != nil {
		t.Fatalf("tenant crossed: %v %v", safe, err)
	}
	cfg.Rules[0].Action = "block"
	if _, err := m.Publish(context.Background(), 2, cfg, "test", "block"); err != nil {
		t.Fatal(err)
	}
	current := WithAuthenticatedTenant(context.Background(), "a")
	if safe, err := d.HistoryGuard(current)(current, open); safe || !errors.Is(err, ErrRuleBlocked) {
		t.Fatalf("block erased: %v %v", safe, err)
	}
	if safe, err := guard(ctx, open); safe || err != nil {
		t.Fatalf("pinned policy changed: %v %v", safe, err)
	}
}

func TestHistoryGuardManagedBaselinePreservesFactsAndRecognizesCredentialFields(t *testing.T) {
	d, m := managedFixture(t)
	ctx := m.Pin(WithAuthenticatedTenant(context.Background(), "a"))
	guard := d.HistoryGuard(ctx)
	for _, tc := range []struct {
		text string
		safe bool
	}{
		{`{"role":"assistant","content":"billing database port 6432 in /srv/billing/db.yaml"}`, true},
		{`{"role":"tool","content":{"password":"SYNTHETICVALUE42"}}`, false},
		{`{"role":"tool","content":{"username":"service-account"}}`, false},
	} {
		safe, err := guard(ctx, func() io.Reader { return strings.NewReader(tc.text) })
		if err != nil || safe != tc.safe {
			t.Fatalf("safe=%v expected=%v err=%v", safe, tc.safe, err)
		}
	}
	d.historyMu.Lock()
	count := len(d.historyPolicies)
	d.historyMu.Unlock()
	_ = d.HistoryGuard(ctx)
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if len(d.historyPolicies) != count {
		t.Fatal("immutable pinned policy was not reused")
	}
}

func TestHistoryGuardKeepsAnchoredLeafPolicy(t *testing.T) {
	for _, action := range []string{"mask", "block"} {
		t.Run(action, func(t *testing.T) {
			d, m := managedFixture(t)
			cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "anchored", Name: "Anchored", Type: "secret", Kind: "pattern", Pattern: `^SYNTHETICBLOCK$`, Fields: []string{"content"}, Action: action, Scopes: []string{"input"}, Enabled: true}}}
			if _, err := m.Publish(context.Background(), 1, cfg, "test", "leaf anchors"); err != nil {
				t.Fatal(err)
			}
			safe, err := d.HistoryGuard(context.Background())(context.Background(), func() io.Reader { return strings.NewReader(`{"content":"SYNTHETICBLOCK"}`) })
			if safe || action == "block" && !errors.Is(err, ErrRuleBlocked) || action == "mask" && err != nil {
				t.Fatalf("anchored %s bypassed: %v %v", action, safe, err)
			}
		})
	}
	got, err := historyPatternSuperset(`^literal\^[$]\$$`)
	if err != nil {
		t.Fatal(err)
	}
	re, err := regexp.Compile(got)
	if err != nil || !re.MatchString(`literal^$$`) || re.MatchString(`literalXYZ`) {
		t.Fatalf("literal anchors corrupted: %s %v", got, err)
	}
}
