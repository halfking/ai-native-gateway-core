package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestAllProtocolRequestsPinBeforeInputAndWithoutInput(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		for _, input := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/no-input", true: "/input"}[input], func(t *testing.T) {
				cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "test", Name: "Test", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true}}}
				m, _ := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), cfg, sanitizerules.Options{})
				if err := m.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
				d := sanitize.NewPatternDetector()
				d.SetRuleManager(m)
				s, _ := sanitize.NewSanitizer(d)
				mw, _ := sanitize.NewSanitizeInputMiddleware(s, nil, time.Minute)
				capture := &correlationCaptureExecutor{}
				h := NewChatHandler(nil, nil, nil, nil, nil, nil)
				if input {
					h.SetSanitizeInputMiddleware(mw.Wrap)
				}
				called := 0
				h.SetRuleSnapshotPin(func(ctx context.Context) context.Context {
					called++
					ctx = m.Pin(ctx)
					changed := cfg
					changed.Rules = append([]sanitizerules.Rule(nil), cfg.Rules...)
					changed.Rules[0].Action = "log"
					if _, err := m.Publish(context.Background(), 1, changed, "test", "in-flight refresh"); err != nil {
						t.Fatal(err)
					}
					return ctx
				})
				h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
				h.provider = durableEndpointResolver{}
				h.executor = &executors.Executor{}
				h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
				h.survivalAttemptExec = capture
				body := `{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"VALUE-1234"}]}`
				if path == "/v1/responses" {
					body = `{"model":"m","stream":true,"input":"VALUE-1234"}`
				}
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer sk-test")
				rec := httptest.NewRecorder()
				var handler http.Handler = h
				if path == "/v1/messages" {
					handler = NewMessagesHandler(h)
				}
				if path == "/v1/responses" {
					handler = NewResponsesHandler(h)
				}
				handler.ServeHTTP(rec, req)
				if called != 1 {
					t.Fatalf("pin calls %d", called)
				}
				if capture.params == nil {
					t.Fatalf("executor missing %d", rec.Code)
				}
				if input {
					raw, _ := json.Marshal(capture.params)
					if strings.Contains(string(raw), "VALUE-1234") {
						t.Fatal("refreshed policy changed in-flight request")
					}
				}
			})
		}
	}
}
