package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestLargeIngressScreensCompleteHistoryBeforeExcerptSanitization(t *testing.T) {
	detector, err := sanitize.NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), detector.RuleConfig(), sanitizerules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	detector.SetRuleManager(manager)
	sani, err := sanitize.NewSanitizer(detector)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(sani, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo=\n", 400) + "billing SYNTHETICPEMFRAGMENT\n" + strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo=\n", 400) + "-----END PRIVATE KEY-----",
		"password=\"" + strings.Repeat("private ", 4000) + "billing SYNTHETICQUOTEDFRAGMENT" + strings.Repeat("private ", 4000) + "\"",
	} {
		t.Run(secret[:8], func(t *testing.T) {
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.SetHistoryGuardFactory(detector.HistoryGuard)
			h.SetRuleSnapshotPin(manager.Pin)
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			history := strings.Repeat("irrelevant\n", 60_000) + secret + strings.Repeat("irrelevant\n", 60_000)
			source, err := json.Marshal(map[string]any{"model": "m", "messages": []any{
				map[string]string{"role": "user", "content": "Maintain configuration"},
				map[string]string{"role": "assistant", "content": history},
				map[string]string{"role": "assistant", "content": "billing port 6432 is in /srv/billing/db.yaml"},
				map[string]string{"role": "user", "content": "Find billing configuration port in db.yaml"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			var actual []byte
			next := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ref, ok := LargeRequestArchive(r.Context())
				if !ok || ref.Size != int64(len(source)) {
					t.Fatalf("archive lost: %+v", ref)
				}
				if err := store.Verify(context.Background(), ref); err != nil {
					t.Fatal(err)
				}
				actual, err = io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(source))
			NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(next).ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if bytes.Contains(actual, []byte("SYNTHETIC")) {
				t.Fatal("excerpt leaked an unrecognizable secret fragment")
			}
			for _, fact := range []string{"6432", "/srv/billing/db.yaml", "Maintain configuration"} {
				if !bytes.Contains(actual, []byte(fact)) {
					t.Fatalf("safe source fact %q lost", fact)
				}
			}
		})
	}
}

func TestLargeIngressProcessingDeadlineIsRetryable(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.SetHistoryGuardFactory(func(context.Context) func(context.Context, func() io.Reader) (bool, error) {
		return func(context.Context, func() io.Reader) (bool, error) { return false, context.DeadlineExceeded }
	})
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := `{"messages":[{"role":"user","content":"first task"},{"role":"assistant","content":"` + strings.Repeat("x", 1_100_000) + `"},{"role":"user","content":"current task"}]}`
	w := httptest.NewRecorder()
	NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("expired preprocessing forwarded") })).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(source)))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "request_processing_timeout") || !strings.Contains(w.Body.String(), `"retryable":true`) {
		t.Fatalf("compute timeout misreported as input mismatch: status=%d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Gw-Archive-Id") == "" {
		t.Fatal("timeout lost committed RAW recovery reference")
	}
}
