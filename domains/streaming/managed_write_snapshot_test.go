package streaming

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestWriteTimeUsesPinnedRuleVersionAndAuthenticatedTenant(t *testing.T) {
	r1013RequireComplianceOn(t)
	d := sanitize.NewPatternDetector()
	rule := sanitizerules.Rule{ID: "phone", Name: "Phone", Type: "phone", Kind: "pattern", Pattern: `13800138000`, Scopes: []string{"output"}, Action: "log", Enabled: true, TenantID: "tenant-1"}
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{rule}}
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), cfg, sanitizerules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d.SetRuleManager(m)
	if err = m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	checker := outputcompliance.NewBuiltinChecker()
	checker.SetManagedRuleSource(func() outputcompliance.ManagedRuleCheck { return d.CheckManagedOutput })
	redactor := BuildRedactBodyWithContextFn(checker, nil, nil)
	run := func(publish bool) int {
		rec, _ := runChatOutputCommitWith(t, nil, nil, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, func(e *executors.Executor, h *ChatHandler, _ *http.Request) {
			h.SetRuleSnapshotPin(m.Pin)
			e.RedactBodyWithContextFn = func(ctx context.Context, body []byte, session, tenant string) []byte {
				if publish {
					cfg.Rules[0].Action = "block"
					if _, err := m.Publish(context.Background(), 1, cfg, "test", "new block"); err != nil {
						t.Fatal(err)
					}
				}
				return redactor(ctx, body, session, tenant)
			}
		})
		if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "13800138000") {
			t.Fatal("allowed fixture never reached output check")
		}
		return rec.Code
	}
	if code := run(true); code != http.StatusOK {
		t.Fatalf("in-flight v1 request changed under publish: %d", code)
	}
	if code := run(false); code != http.StatusForbidden {
		t.Fatalf("new request missed tenant-scoped v2 block: %d", code)
	}
}
