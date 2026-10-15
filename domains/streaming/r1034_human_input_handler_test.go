package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
)

func TestR1034HandlersSanitizeHumanCredentialDeclarations(t *testing.T) {
	const prompt = "请部署到211，管理员帐号改成admin/R7#kQ?92!，请继续"
	q, _ := json.Marshal(prompt)
	for _, tc := range []struct {
		path, body string
		wrap       func(*ChatHandler) http.Handler
	}{
		{"/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":` + string(q) + `}]}`, func(h *ChatHandler) http.Handler { return h }},
		{"/v1/messages", `{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":` + string(q) + `}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"/v1/responses", `{"model":"m","stream":true,"input":` + string(q) + `}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			mini := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			detector, err := sanitize.NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
			if err != nil {
				t.Fatal(err)
			}
			s, err := sanitize.NewSanitizer(detector)
			if err != nil {
				t.Fatal(err)
			}
			mw, err := sanitize.NewSanitizeInputMiddleware(s, client, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			capture := &correlationCaptureExecutor{}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.SetSanitizeInputMiddleware(mw.Wrap)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
			h.provider = durableEndpointResolver{}
			h.executor = &executors.Executor{}
			h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
			h.survivalAttemptExec = capture
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gw_r1034_input")
			req.Header.Set("X-Gw-Tenant-Id", "tenant-1")
			rec := httptest.NewRecorder()
			tc.wrap(h).ServeHTTP(rec, req)
			if capture.params == nil {
				t.Fatalf("request never reached executor: %d", rec.Code)
			}
			for _, body := range [][]byte{capture.params.BodyBytes, capture.params.ResponsesBodyBytes} {
				if strings.Contains(string(body), "R7#kQ") || strings.Contains(string(body), "92!") {
					t.Fatal("raw or partial credential reached upstream")
				}
			}
			if !strings.Contains(string(capture.params.BodyBytes), "{SENSITIVE:secret:") {
				t.Fatal("upstream body lacks typed secret marker")
			}
		})
	}
}
